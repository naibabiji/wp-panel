package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const (
	wpCoreUpdateWorkerPollInterval      = time.Second
	wpCoreUpdateWorkerHeartbeatInterval = 30 * time.Second
	wpCoreUpdateWorkerSweepInterval     = 30 * time.Second
)

var wpCoreUpdateWorkerSlot = make(chan struct{}, 1)
var wpCoreUpdateWorkerInstance struct {
	sync.Mutex
	active bool
}

type wpCoreUpdateBackupPreparer interface {
	prepareCoreBackups(context.Context, string, string) error
}

type wpCoreUpdateTaskExecutor interface {
	Execute(context.Context, string, string) error
}

type wpCoreUpdateVersionObserver func(context.Context, int) (string, error)

type WPCoreUpdateWorker struct {
	store             *wpUpdateStore
	backups           wpCoreUpdateBackupPreparer
	executor          wpCoreUpdateTaskExecutor
	observeVersion    wpCoreUpdateVersionObserver
	owner             string
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	sweepInterval     time.Duration
	now               func() time.Time

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

type wpCoreUpdateWorkerOptions struct {
	store             *wpUpdateStore
	backups           wpCoreUpdateBackupPreparer
	executor          wpCoreUpdateTaskExecutor
	observeVersion    wpCoreUpdateVersionObserver
	owner             string
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	sweepInterval     time.Duration
	now               func() time.Time
}

func newWPCoreUpdateWorker(opts wpCoreUpdateWorkerOptions) (*WPCoreUpdateWorker, error) {
	if opts.store == nil || opts.store.db == nil || opts.backups == nil || opts.executor == nil || opts.observeVersion == nil ||
		opts.owner == "" || opts.pollInterval <= 0 || opts.heartbeatInterval <= 0 || opts.sweepInterval <= 0 || opts.now == nil {
		return nil, errors.New("invalid core update worker options")
	}
	return &WPCoreUpdateWorker{
		store: opts.store, backups: opts.backups, executor: opts.executor, observeVersion: opts.observeVersion,
		owner: opts.owner, pollInterval: opts.pollInterval, heartbeatInterval: opts.heartbeatInterval,
		sweepInterval: opts.sweepInterval, now: opts.now,
	}, nil
}

func newWPCoreUpdateWorkerOwner() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "core-worker-" + hex.EncodeToString(raw[:]), nil
}

func (w *WPCoreUpdateWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("core update worker already started")
	}
	wpCoreUpdateWorkerInstance.Lock()
	defer wpCoreUpdateWorkerInstance.Unlock()
	if wpCoreUpdateWorkerInstance.active {
		return errors.New("core update worker already active")
	}
	if _, err := w.store.recoverAfterRestart(context.Background(), w.now().UTC()); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	wpCoreUpdateWorkerInstance.active = true
	go w.run(ctx, w.done)
	return nil
}

func (w *WPCoreUpdateWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	w.cancel()
	done := w.done
	w.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *WPCoreUpdateWorker) run(ctx context.Context, done chan struct{}) {
	defer func() {
		w.mu.Lock()
		w.started = false
		w.cancel = nil
		w.mu.Unlock()
		wpCoreUpdateWorkerInstance.Lock()
		wpCoreUpdateWorkerInstance.active = false
		wpCoreUpdateWorkerInstance.Unlock()
		close(done)
	}()
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		w.sweep(ctx)
	}()
	defer func() { <-sweepDone }()
	for {
		if ctx.Err() != nil {
			return
		}
		if w.processOne(ctx) {
			continue
		}
		timer := time.NewTimer(w.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (w *WPCoreUpdateWorker) sweep(ctx context.Context) {
	ticker := time.NewTicker(w.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = w.store.recoverExpired(context.Background(), w.now().UTC())
		}
	}
}

func (w *WPCoreUpdateWorker) processOne(ctx context.Context) bool {
	select {
	case wpCoreUpdateWorkerSlot <- struct{}{}:
		defer func() { <-wpCoreUpdateWorkerSlot }()
	case <-ctx.Done():
		return false
	}
	task, err := w.store.nextQueuedCoreUpdate(ctx)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return false
	}
	observed, err := w.observeVersion(ctx, task.SiteID)
	if err != nil {
		return false
	}
	claimed, err := w.store.claimCoreUpdate(ctx, task.ID, w.owner, observed, w.now().UTC())
	if err != nil {
		return false
	}
	w.runOwned(ctx, claimed)
	return true
}

func (w *WPCoreUpdateWorker) runOwned(parent context.Context, task WPUpdateTask) {
	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- errors.New("core update worker execution panic")
			}
		}()
		if err := w.backups.prepareCoreBackups(runCtx, task.ID, w.owner); err != nil {
			if runCtx.Err() == nil {
				_ = w.store.markFailure(context.Background(), task.ID, w.owner, "backup", false, w.now().UTC())
			}
			done <- err
			return
		}
		done <- w.executor.Execute(runCtx, task.ID, w.owner)
	}()
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			cancel()
			<-done
			w.interruptIfOwned(task.ID, "worker_stopped")
			return
		case <-ticker.C:
			owned, err := w.store.heartbeat(context.Background(), task.ID, w.owner, w.now().UTC())
			if err != nil || !owned {
				cancel()
				<-done
				if err != nil {
					w.resolveUncertainHeartbeat(parent, task.ID)
				}
				return
			}
		case <-done:
			w.interruptIfOwned(task.ID, "execution_result_unknown")
			return
		}
	}
}

func (w *WPCoreUpdateWorker) resolveUncertainHeartbeat(ctx context.Context, taskID string) {
	deadline := time.Now().Add(wpUpdateLease)
	retry := w.heartbeatInterval
	if retry > time.Second {
		retry = time.Second
	}
	for time.Now().Before(deadline) {
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		owned, err := w.store.heartbeat(context.Background(), taskID, w.owner, w.now().UTC())
		if err != nil {
			continue
		}
		if owned {
			w.interruptIfOwned(taskID, "heartbeat_uncertain")
		}
		return
	}
}

func (w *WPCoreUpdateWorker) interruptIfOwned(taskID, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = w.store.interruptOwned(ctx, taskID, w.owner, code, w.now().UTC())
}
