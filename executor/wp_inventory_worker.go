package executor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

const (
	wpInventoryWorkerPollInterval = time.Second
	wpInventoryWorkerLease        = time.Minute
	wpInventoryWorkerPruneDays    = 30
	wpInventoryWorkerPruneEvery   = 100
)

var wpInventoryWorkerSlot = make(chan struct{}, 1)

type wpInventoryCollector interface {
	Collect(context.Context, *config.Config, *models.Website, bool) (WPInventoryRunResult, error)
}

type WPInventoryWorker struct {
	cfg          *config.Config
	store        *wpInventoryStore
	collector    wpInventoryCollector
	owner        string
	pollInterval time.Duration
	lease        time.Duration
	now          func() time.Time

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

type wpInventoryWorkerOptions struct {
	cfg          *config.Config
	store        *wpInventoryStore
	collector    wpInventoryCollector
	owner        string
	pollInterval time.Duration
	lease        time.Duration
	now          func() time.Time
}

func NewWPInventoryWorker(cfg *config.Config) (*WPInventoryWorker, error) {
	store, err := newWPInventoryStore()
	if err != nil {
		return nil, err
	}
	runner, err := NewWPInventoryRunner()
	if err != nil {
		return nil, err
	}
	id, err := newWPInventoryJobID()
	if err != nil {
		return nil, err
	}
	return newWPInventoryWorker(wpInventoryWorkerOptions{
		cfg: cfg, store: store, collector: runner, owner: "worker-" + id,
		pollInterval: wpInventoryWorkerPollInterval, lease: wpInventoryWorkerLease, now: time.Now,
	})
}

func newWPInventoryWorker(opts wpInventoryWorkerOptions) (*WPInventoryWorker, error) {
	if opts.cfg == nil || opts.store == nil || opts.collector == nil ||
		!validWPInventoryLeaseOwner(opts.owner) || opts.pollInterval <= 0 || opts.lease <= 0 || opts.now == nil {
		return nil, errors.New("invalid wordpress inventory worker options")
	}
	return &WPInventoryWorker{
		cfg: opts.cfg, store: opts.store, collector: opts.collector, owner: opts.owner,
		pollInterval: opts.pollInterval, lease: opts.lease, now: opts.now,
	}, nil
}

func (w *WPInventoryWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("wordpress inventory worker already started")
	}

	now := w.now().UTC()
	if _, _, err := w.store.recoverExpired(context.Background(), now); err != nil {
		return err
	}
	if _, err := w.store.prune(context.Background(), now.Add(-wpInventoryWorkerPruneDays*24*time.Hour), 200); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	go w.run(ctx)
	return nil
}

func (w *WPInventoryWorker) Stop(ctx context.Context) error {
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

func (w *WPInventoryWorker) run(ctx context.Context) {
	defer close(w.done)
	completed := 0
	for {
		if ctx.Err() != nil {
			return
		}
		hadJob, terminal := w.processOne(ctx)
		if terminal {
			completed++
			if completed%wpInventoryWorkerPruneEvery == 0 {
				_, _ = w.store.prune(context.Background(), w.now().UTC().Add(-wpInventoryWorkerPruneDays*24*time.Hour), 200)
			}
		}
		if hadJob {
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

func (w *WPInventoryWorker) processOne(ctx context.Context) (bool, bool) {
	select {
	case wpInventoryWorkerSlot <- struct{}{}:
		defer func() { <-wpInventoryWorkerSlot }()
	case <-ctx.Done():
		return false, false
	}

	job, err := w.store.claim(ctx, w.owner, w.now().UTC(), w.lease)
	if err != nil {
		return false, false
	}
	identity, err := w.store.loadSiteIdentity(ctx, job.SiteID)
	if err != nil {
		if ctx.Err() != nil {
			w.release(job.ID)
			return true, false
		}
		return true, w.persistInternalFailure(job.ID, WPInventoryRunMeta{}) == nil
	}

	site := &models.Website{
		ID: identity.ID, Domain: identity.Domain, Status: identity.Status,
		SystemUser: identity.SystemUser, WebRoot: identity.WebRoot, SiteType: identity.SiteType,
		DisableWPUpdates: identity.DisableWPUpdates,
	}
	// Scheduled inventory is the source of truth for the fleet overview, so it
	// must refresh WordPress' update transients just like an explicit manual
	// scan. Otherwise a present-but-never-checked transient is persisted as an
	// authoritative "no updates" result until somebody visits wp-admin.
	force := !identity.DisableWPUpdates && (job.Trigger == wpInventoryTriggerManual || job.Trigger == wpInventoryTriggerScheduled)
	result, runErr := w.collect(ctx, site, force)
	if ctx.Err() != nil {
		w.release(job.ID)
		return true, false
	}
	completedAt := w.now().UTC()
	if runErr != nil {
		if err := w.store.persistFailure(context.Background(), job.ID, w.owner, runErr, result.Meta, completedAt); err != nil {
			return true, false
		}
		return true, true
	}
	if err := w.store.persistSuccess(context.Background(), job.ID, w.owner, identity, result, completedAt); err != nil {
		return true, errors.Is(err, errWPInventorySiteChanged)
	}
	return true, true
}

func (w *WPInventoryWorker) collect(ctx context.Context, site *models.Website, force bool) (result WPInventoryRunResult, runErr *WPInventoryRunError) {
	defer func() {
		if recover() != nil {
			result = WPInventoryRunResult{}
			runErr = wpInventoryWorkerInternalError()
		}
	}()
	result, err := w.collector.Collect(ctx, w.cfg, site, force)
	if err == nil {
		return result, nil
	}
	if errors.As(err, &runErr) {
		return result, runErr
	}
	return result, wpInventoryWorkerInternalError()
}

func (w *WPInventoryWorker) persistInternalFailure(jobID string, meta WPInventoryRunMeta) error {
	return w.store.persistFailure(context.Background(), jobID, w.owner, wpInventoryWorkerInternalError(), meta, w.now().UTC())
}

func (w *WPInventoryWorker) release(jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = w.store.releaseOwned(ctx, jobID, w.owner, w.now().UTC())
}

func wpInventoryWorkerInternalError() *WPInventoryRunError {
	return runError(WPInventoryRunnerInternalError, WPInventoryStageExecute, -1, false, errors.New("inventory worker collector failure"))
}
