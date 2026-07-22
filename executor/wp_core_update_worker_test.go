package executor

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeWPCoreWorkerBackups struct {
	store    *wpUpdateStore
	mu       sync.Mutex
	calls    []string
	block    bool
	panicNow bool
	started  chan struct{}
	release  <-chan struct{}
}

func (f *fakeWPCoreWorkerBackups) prepareCoreBackups(ctx context.Context, id, owner string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "backup")
	f.mu.Unlock()
	if f.started != nil {
		close(f.started)
	}
	if f.panicNow {
		panic("secret panic payload")
	}
	if f.release != nil {
		<-f.release
		return ctx.Err()
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.store.advanceOwnedStage(ctx, id, owner, "claimed", "backups_ready", time.Now().UTC())
}

type fakeWPCoreWorkerExecutor struct {
	store *wpUpdateStore
	mu    sync.Mutex
	calls []string
}

func (f *fakeWPCoreWorkerExecutor) Execute(ctx context.Context, id, owner string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "execute")
	f.mu.Unlock()
	return f.store.markSuccess(ctx, id, owner, time.Now().UTC())
}

func TestWPCoreUpdateWorkerClaimsBacksUpAndExecutes(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	backups := &fakeWPCoreWorkerBackups{store: store}
	executor := &fakeWPCoreWorkerExecutor{store: store}
	worker := newTestWPCoreUpdateWorker(t, store, backups, executor, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateSuccess)
	stopWPCoreUpdateWorker(t, worker)
	backups.mu.Lock()
	backupCalls := append([]string(nil), backups.calls...)
	backups.mu.Unlock()
	executor.mu.Lock()
	executorCalls := append([]string(nil), executor.calls...)
	executor.mu.Unlock()
	if len(backupCalls) != 1 || len(executorCalls) != 1 {
		t.Fatalf("backup calls=%v executor calls=%v", backupCalls, executorCalls)
	}
}

func TestWPCoreUpdateWorkerRestartNeverResumesRunningStage(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Hour))
	old := time.Now()
	if _, err := store.claimCoreUpdate(context.Background(), task.ID, "dead-worker", "7.0.1", old); err != nil {
		t.Fatal(err)
	}
	if err := store.advanceOwnedStage(context.Background(), task.ID, "dead-worker", "claimed", "updating_core", old); err != nil {
		t.Fatal(err)
	}
	backups := &fakeWPCoreWorkerBackups{store: store}
	executor := &fakeWPCoreWorkerExecutor{store: store}
	worker := newTestWPCoreUpdateWorker(t, store, backups, executor, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateInterrupted)
	stopWPCoreUpdateWorker(t, worker)
	if len(backups.calls) != 0 || len(executor.calls) != 0 {
		t.Fatalf("expired task was resumed: backup=%v executor=%v", backups.calls, executor.calls)
	}
}

func TestWPCoreUpdateWorkerPanicBecomesInterruptedWithoutPayload(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	backups := &fakeWPCoreWorkerBackups{store: store, panicNow: true}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateInterrupted)
	stopWPCoreUpdateWorker(t, worker)
	var leaked int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events WHERE task_id=? AND (summary LIKE '%secret%' OR error_code LIKE '%secret%')`, task.ID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("panic payload leaked: count=%d err=%v", leaked, err)
	}
}

func TestWPCoreUpdateWorkerStopMarksOwnedTaskInterrupted(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, block: true, started: started}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	stopWPCoreUpdateWorker(t, worker)
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateInterrupted || !current.RequiresAttention || current.LeaseOwner != "" {
		t.Fatalf("task=%+v err=%v", current, err)
	}
}

func TestWPCoreUpdateWorkerHeartbeatOwnershipLossStopsWithoutOverwrite(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, block: true, started: started}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	worker.heartbeatInterval = 5 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET lease_owner='replacement-worker' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateRunning || current.LeaseOwner != "replacement-worker" {
		t.Fatalf("task=%+v err=%v", current, err)
	}
	stopWPCoreUpdateWorker(t, worker)
}

func TestWPCoreUpdateWorkerHeartbeatRenewsLease(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, block: true, started: started}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	worker.heartbeatInterval = 5 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	before, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, err := store.getTask(context.Background(), task.ID)
		if err == nil && current.LeaseExpiresAt > before.LeaseExpiresAt {
			stopWPCoreUpdateWorker(t, worker)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopWPCoreUpdateWorker(t, worker)
	t.Fatal("lease was not renewed")
}

func TestWPCoreUpdateWorkerStopTimeoutReleasesInstanceAfterRunExits(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	release := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, started: started, release: release}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := worker.Stop(stopCtx)
	cancel()
	if err == nil {
		t.Fatal("expected Stop timeout")
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		next := newTestWPCoreUpdateWorker(t, store, &fakeWPCoreWorkerBackups{store: store}, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
		if err := next.Start(); err == nil {
			stopWPCoreUpdateWorker(t, next)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("worker instance remained permanently active")
}

func newTestWPCoreUpdateWorker(t *testing.T, store *wpUpdateStore, backups wpCoreUpdateBackupPreparer, executor wpCoreUpdateTaskExecutor, version string) *WPCoreUpdateWorker {
	t.Helper()
	worker, err := newWPCoreUpdateWorker(wpCoreUpdateWorkerOptions{
		store: store, backups: backups, executor: executor,
		observeVersion: func(context.Context, int) (string, error) { return version, nil },
		owner:          "test-core-worker", pollInterval: 5 * time.Millisecond,
		heartbeatInterval: 20 * time.Millisecond, sweepInterval: 20 * time.Millisecond, now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func stopWPCoreUpdateWorker(t *testing.T, worker *WPCoreUpdateWorker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitWPUpdateTaskStatus(t *testing.T, store *wpUpdateStore, id, status string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.getTask(context.Background(), id)
		if err == nil && task.Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	task, err := store.getTask(context.Background(), id)
	t.Fatalf("task status=%s err=%v want=%s", task.Status, err, status)
}
