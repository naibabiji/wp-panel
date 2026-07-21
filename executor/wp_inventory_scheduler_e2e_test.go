package executor

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type wpInventoryE2EClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *wpInventoryE2EClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *wpInventoryE2EClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestWPInventorySchedulerE2ESuccessFailureWindowsAndOldInventory(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	clock := &wpInventoryE2EClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	scheduler := newWPInventoryE2EScheduler(t, store, clock)
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{
		{result: sampleWPInventoryResult()},
		{err: runError(WPInventoryRunnerTimeout, WPInventoryStageExecute, -1, true, errors.New("fixed fixture failure"))},
	}}
	worker := newWPInventoryE2EWorker(t, store, collector, clock, "worker-scheduler-e2e")

	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatalf("first schedule cycle: %v", err)
	}
	firstJob := latestWPInventoryScheduledJobID(t, store, siteID)
	if err := worker.Start(); err != nil {
		t.Fatalf("worker Start(): %v", err)
	}
	waitWPInventoryJobStatus(t, store, firstJob, wpInventoryJobSucceeded)
	firstState, err := store.getState(context.Background(), siteID)
	if err != nil || firstState.Status != "complete" || firstState.CollectionID != firstJob {
		t.Fatalf("first state=%+v err=%v", firstState, err)
	}
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatalf("same-window cycle: %v", err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 1 {
		t.Fatalf("same window created %d jobs, want 1", got)
	}

	clock.Advance(6 * time.Hour)
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatalf("next-window cycle: %v", err)
	}
	secondJob := latestWPInventoryScheduledJobID(t, store, siteID)
	if secondJob == firstJob {
		t.Fatal("next stable window did not create a new job")
	}
	waitWPInventoryJobStatus(t, store, secondJob, wpInventoryJobFailed)
	failedState, err := store.getState(context.Background(), siteID)
	if err != nil || failedState.Status != "failed" || failedState.CollectionID != firstJob || failedState.LastSuccessAt == "" {
		t.Fatalf("failed state did not preserve old inventory: %+v err=%v", failedState, err)
	}
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatalf("failed same-window cycle: %v", err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 2 {
		t.Fatalf("failed same window created %d jobs, want 2", got)
	}
	if collector.callCount() != 2 || collector.maxConcurrentCount() != 1 {
		t.Fatalf("collector calls=%d maxConcurrent=%d", collector.callCount(), collector.maxConcurrentCount())
	}
	stopWPInventoryWorker(t, worker)
}

func TestWPInventorySchedulerE2EStopDuringBatchPreservesQueuedTasks(t *testing.T) {
	store, firstSiteID := newWPInventoryStoreTest(t)
	for i := 0; i < 24; i++ {
		insertWPInventoryWorkerSite(t, store, "scheduler-stop-"+time.Unix(int64(i), 0).UTC().Format("150405")+".test")
	}
	clock := &wpInventoryE2EClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	waitStarted := make(chan struct{}, 1)
	scheduler, err := newWPInventoryScheduler(wpInventorySchedulerOptions{
		store: store, now: clock.Now,
		wait: func(ctx context.Context, _ time.Duration) bool {
			select {
			case waitStarted <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return false
		},
		refreshInterval: 6 * time.Hour, pollInterval: 15 * time.Minute, batchDelay: time.Second,
		batchSize: 20, maxPerCycle: 100, candidateLimit: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waitStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not reach the batch wait")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 20 {
		t.Fatalf("queued jobs after stop=%d want 20", got)
	}
	jobID := latestWPInventoryScheduledJobID(t, store, firstSiteID)
	job, err := store.getJob(context.Background(), jobID)
	if err != nil || job.Status != wpInventoryJobQueued {
		t.Fatalf("queued job after stop=%+v err=%v", job, err)
	}
}

func TestWPInventorySchedulerE2ERepeatedLifecycleDoesNotLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		store := &fakeWPInventorySchedulerStore{}
		scheduler, err := newWPInventoryScheduler(wpInventorySchedulerOptions{
			store: store, now: time.Now,
			wait:            wpInventoryScheduleWait,
			refreshInterval: time.Hour, pollInterval: time.Hour, batchDelay: time.Second,
			batchSize: 20, maxPerCycle: 100, candidateLimit: 300,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := scheduler.Start(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := scheduler.Stop(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}
	runtime.GC()
	after := runtime.NumGoroutine()
	if after > before+4 {
		t.Fatalf("goroutines grew from %d to %d", before, after)
	}
}

func TestWPInventorySchedulerE2EEmptyDatabaseIsInert(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	if _, err := store.db.Exec("DELETE FROM websites WHERE id=?", siteID); err != nil {
		t.Fatal(err)
	}
	clock := &wpInventoryE2EClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	collector := &wpInventoryFakeCollector{}
	worker := newWPInventoryE2EWorker(t, store, collector, clock, "worker-empty-e2e")
	scheduler := newWPInventoryE2EScheduler(t, store, clock)
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 0 {
		t.Fatalf("empty database created %d jobs", got)
	}
	if collector.callCount() != 0 {
		t.Fatalf("empty database collector calls=%d", collector.callCount())
	}
}

func TestWPInventorySchedulerE2ETwoSchedulersDeduplicate(t *testing.T) {
	store, _ := newWPInventoryStoreTest(t)
	clock := &wpInventoryE2EClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	first := newWPInventoryE2EScheduler(t, store, clock)
	second := newWPInventoryE2EScheduler(t, store, clock)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, scheduler := range []*WPInventoryScheduler{first, second} {
		go func(s *WPInventoryScheduler) {
			<-start
			errs <- s.runCycle(context.Background())
		}(scheduler)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 1 {
		t.Fatalf("two schedulers created %d jobs, want 1", got)
	}
}

func TestWPInventorySchedulerE2EWorkerSingleConcurrencyAcrossHundredJobs(t *testing.T) {
	store, _ := newWPInventoryStoreTest(t)
	for i := 2; i <= 100; i++ {
		insertWPInventoryWorkerSite(t, store, e2eDomain(i))
	}
	clock := &wpInventoryE2EClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	scheduler := newWPInventoryE2EScheduler(t, store, clock)
	steps := make([]wpInventoryFakeStep, 100)
	for i := range steps {
		steps[i] = wpInventoryFakeStep{result: sampleWPInventoryResult(), delay: time.Millisecond}
	}
	collector := &wpInventoryFakeCollector{steps: steps}
	worker := newWPInventoryE2EWorker(t, store, collector, clock, "worker-hundred-e2e")
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 100 {
		t.Fatalf("scheduled jobs=%d want 100", got)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPInventoryTerminalJobCount(t, store, 100)
	stopWPInventoryWorker(t, worker)
	if collector.callCount() != 100 || collector.maxConcurrentCount() != 1 {
		t.Fatalf("collector calls=%d maxConcurrent=%d", collector.callCount(), collector.maxConcurrentCount())
	}
}

func TestWPInventorySchedulerE2ERestartRecoversLeaseWithoutDuplicate(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	clock := &wpInventoryE2EClock{now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
	scheduler := newWPInventoryE2EScheduler(t, store, clock)
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobID := latestWPInventoryScheduledJobID(t, store, siteID)
	job, err := store.claim(context.Background(), "dead-worker", clock.Now(), time.Minute)
	if err != nil || job.ID != jobID {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	clock.Advance(2 * time.Minute)
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 1 {
		t.Fatalf("expired active lease caused duplicate jobs=%d", got)
	}
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{{result: sampleWPInventoryResult()}}}
	worker := newWPInventoryE2EWorker(t, store, collector, clock, "worker-restart-e2e")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPInventoryJobStatus(t, store, jobID, wpInventoryJobSucceeded)
	stopWPInventoryWorker(t, worker)
	recovered, err := store.getJob(context.Background(), jobID)
	if err != nil || recovered.LeaseRecoveryCount != 1 || recovered.Status != wpInventoryJobSucceeded {
		t.Fatalf("recovered job=%+v err=%v", recovered, err)
	}
	clock.Advance(6 * time.Hour)
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 2 {
		t.Fatalf("next window after recovery jobs=%d want 2", got)
	}
}

func newWPInventoryE2EScheduler(t *testing.T, store *wpInventoryStore, clock *wpInventoryE2EClock) *WPInventoryScheduler {
	t.Helper()
	scheduler, err := newWPInventoryScheduler(wpInventorySchedulerOptions{
		store: store, now: clock.Now,
		wait:            func(context.Context, time.Duration) bool { return true },
		refreshInterval: 6 * time.Hour, pollInterval: 15 * time.Minute, batchDelay: time.Second,
		batchSize: 20, maxPerCycle: 100, candidateLimit: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

func newWPInventoryE2EWorker(t *testing.T, store *wpInventoryStore, collector wpInventoryCollector, clock *wpInventoryE2EClock, owner string) *WPInventoryWorker {
	t.Helper()
	worker, err := newWPInventoryWorker(wpInventoryWorkerOptions{
		cfg: &config.Config{}, store: store, collector: collector, owner: owner,
		pollInterval: 5 * time.Millisecond, lease: time.Minute, now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func latestWPInventoryScheduledJobID(t *testing.T, store *wpInventoryStore, siteID int) string {
	t.Helper()
	var jobID string
	if err := store.db.QueryRow(`SELECT id FROM site_wp_inventory_jobs
		WHERE site_id=? AND trigger_type='scheduled' ORDER BY requested_at DESC, id DESC LIMIT 1`, siteID).Scan(&jobID); err != nil {
		t.Fatalf("latest scheduled job: %v", err)
	}
	return jobID
}

func waitWPInventoryTerminalJobCount(t *testing.T, store *wpInventoryStore, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var got int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM site_wp_inventory_jobs WHERE status IN ('succeeded','failed')`).Scan(&got); err == nil && got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	var got int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM site_wp_inventory_jobs WHERE status IN ('succeeded','failed')`).Scan(&got)
	t.Fatalf("terminal jobs=%d want %d", got, want)
}

func e2eDomain(index int) string {
	return "scheduler-e2e-" + time.Unix(int64(index), 0).UTC().Format("150405") + ".test"
}
