package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

func TestWPInventoryWorkerEmptyStartStopIsInert(t *testing.T) {
	store, _ := newWPInventoryStoreTest(t)
	collector := &wpInventoryFakeCollector{}
	worker := newTestWPInventoryWorker(t, store, collector, "worker-empty")
	if err := worker.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	stopWPInventoryWorker(t, worker)
	if collector.callCount() != 0 {
		t.Fatalf("collector calls = %d, want 0", collector.callCount())
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 0 {
		t.Fatalf("jobs after empty start = %d, want 0", got)
	}
}

func TestWPInventoryWorkerEmptyDoesNotPrepareRunner(t *testing.T) {
	store, _ := newWPInventoryStoreTest(t)
	runner, fixture := newTestInventoryRunner(t, wpInventoryRunnerSource)
	worker, err := newWPInventoryWorker(wpInventoryWorkerOptions{
		cfg: fixture.cfg, store: store, collector: runner, owner: "worker-real-runner",
		pollInterval: 5 * time.Millisecond, lease: time.Minute, now: time.Now,
	})
	if err != nil {
		t.Fatalf("newWPInventoryWorker(): %v", err)
	}
	if err := worker.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	stopWPInventoryWorker(t, worker)
	if _, err := os.Stat(runner.runnerRoot); !os.IsNotExist(err) {
		t.Fatalf("runner root exists after empty worker start: %v", err)
	}
}

func TestWPInventoryWorkerPersistsSuccess(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	jobID := enqueueWPInventoryWorkerJob(t, store, siteID, time.Now().Add(-time.Second))
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{{result: sampleWPInventoryResult()}}}
	worker := newTestWPInventoryWorker(t, store, collector, "worker-success")
	if err := worker.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	waitWPInventoryJobStatus(t, store, jobID, wpInventoryJobSucceeded)
	stopWPInventoryWorker(t, worker)
	if collector.callCount() != 1 {
		t.Fatalf("collector calls = %d, want 1", collector.callCount())
	}
	state, err := store.getState(context.Background(), siteID)
	if err != nil || state.Status != "complete" || state.CollectionID != jobID {
		t.Fatalf("success state = %+v, err = %v", state, err)
	}
}

func TestWPInventoryWorkerRunnerFailureContinues(t *testing.T) {
	store, firstSite := newWPInventoryStoreTest(t)
	secondSite := insertWPInventoryWorkerSite(t, store, "second.example.com")
	now := time.Now().Add(-3 * time.Second)
	firstJob := enqueueWPInventoryWorkerJob(t, store, firstSite, now)
	secondJob := enqueueWPInventoryWorkerJob(t, store, secondSite, now.Add(time.Second))
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{
		{err: runError(WPInventoryRunnerTimeout, WPInventoryStageExecute, -1, true, errors.New("fixture timeout"))},
		{result: sampleWPInventoryResult()},
	}}
	worker := newTestWPInventoryWorker(t, store, collector, "worker-failure")
	if err := worker.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	waitWPInventoryJobStatus(t, store, firstJob, wpInventoryJobFailed)
	waitWPInventoryJobStatus(t, store, secondJob, wpInventoryJobSucceeded)
	stopWPInventoryWorker(t, worker)
	first, err := store.getJob(context.Background(), firstJob)
	if err != nil || first.ErrorCode != string(WPInventoryRunnerTimeout) || !first.TimedOut {
		t.Fatalf("failed job = %+v, err = %v", first, err)
	}
}

func TestWPInventoryWorkerPanicBecomesFixedFailureAndContinues(t *testing.T) {
	store, firstSite := newWPInventoryStoreTest(t)
	secondSite := insertWPInventoryWorkerSite(t, store, "panic-next.example.com")
	now := time.Now().Add(-3 * time.Second)
	firstJob := enqueueWPInventoryWorkerJob(t, store, firstSite, now)
	secondJob := enqueueWPInventoryWorkerJob(t, store, secondSite, now.Add(time.Second))
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{
		{panicValue: "secret panic payload"},
		{result: sampleWPInventoryResult()},
	}}
	worker := newTestWPInventoryWorker(t, store, collector, "worker-panic")
	if err := worker.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	waitWPInventoryJobStatus(t, store, firstJob, wpInventoryJobFailed)
	waitWPInventoryJobStatus(t, store, secondJob, wpInventoryJobSucceeded)
	stopWPInventoryWorker(t, worker)
	first, err := store.getJob(context.Background(), firstJob)
	if err != nil || first.ErrorCode != string(WPInventoryRunnerInternalError) || first.ErrorStage != string(WPInventoryStageExecute) {
		t.Fatalf("panic job = %+v, err = %v", first, err)
	}
}

func TestWPInventoryWorkerStopRequeuesRunningJob(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	jobID := enqueueWPInventoryWorkerJob(t, store, siteID, time.Now().Add(-time.Second))
	started := make(chan struct{})
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{{waitForCancel: true, started: started}}}
	worker := newTestWPInventoryWorker(t, store, collector, "worker-stop")
	if err := worker.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not start")
	}
	stopWPInventoryWorker(t, worker)
	job, err := store.getJob(context.Background(), jobID)
	if err != nil || job.Status != wpInventoryJobQueued || job.LeaseOwner != "" || job.ErrorCode != "" {
		t.Fatalf("stopped job = %+v, err = %v", job, err)
	}
	state, err := store.getState(context.Background(), siteID)
	if err != nil || state.Status != "unknown" {
		t.Fatalf("state after stop = %+v, err = %v", state, err)
	}
}

func TestWPInventoryWorkerRecoversExpiredJobOnRestart(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	old := time.Now().Add(-2 * time.Minute)
	jobID := enqueueWPInventoryWorkerJob(t, store, siteID, old)
	if _, err := store.claim(context.Background(), "worker-dead", old, time.Second); err != nil {
		t.Fatalf("claim expired job: %v", err)
	}
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{{result: sampleWPInventoryResult()}}}
	worker := newTestWPInventoryWorker(t, store, collector, "worker-restart")
	if err := worker.Start(); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	waitWPInventoryJobStatus(t, store, jobID, wpInventoryJobSucceeded)
	stopWPInventoryWorker(t, worker)
	job, err := store.getJob(context.Background(), jobID)
	if err != nil || job.AttemptCount != 2 || job.LeaseRecoveryCount != 1 {
		t.Fatalf("recovered job = %+v, err = %v", job, err)
	}
}

func TestWPInventoryWorkerGlobalSlotAcrossInstances(t *testing.T) {
	store, firstSite := newWPInventoryStoreTest(t)
	secondSite := insertWPInventoryWorkerSite(t, store, "parallel.example.com")
	now := time.Now().Add(-3 * time.Second)
	firstJob := enqueueWPInventoryWorkerJob(t, store, firstSite, now)
	secondJob := enqueueWPInventoryWorkerJob(t, store, secondSite, now.Add(time.Second))
	collector := &wpInventoryFakeCollector{steps: []wpInventoryFakeStep{
		{result: sampleWPInventoryResult(), delay: 40 * time.Millisecond},
		{result: sampleWPInventoryResult(), delay: 40 * time.Millisecond},
	}}
	firstWorker := newTestWPInventoryWorker(t, store, collector, "worker-global-a")
	secondWorker := newTestWPInventoryWorker(t, store, collector, "worker-global-b")
	if err := firstWorker.Start(); err != nil {
		t.Fatalf("first Start(): %v", err)
	}
	if err := secondWorker.Start(); err != nil {
		t.Fatalf("second Start(): %v", err)
	}
	waitWPInventoryJobStatus(t, store, firstJob, wpInventoryJobSucceeded)
	waitWPInventoryJobStatus(t, store, secondJob, wpInventoryJobSucceeded)
	stopWPInventoryWorker(t, firstWorker)
	stopWPInventoryWorker(t, secondWorker)
	if collector.maxConcurrentCount() != 1 {
		t.Fatalf("maximum concurrent collectors = %d, want 1", collector.maxConcurrentCount())
	}
}

type wpInventoryFakeStep struct {
	result        WPInventoryRunResult
	err           error
	panicValue    any
	waitForCancel bool
	started       chan struct{}
	delay         time.Duration
}

type wpInventoryFakeCollector struct {
	mu            sync.Mutex
	steps         []wpInventoryFakeStep
	calls         int
	active        int
	maxConcurrent int
}

func (f *wpInventoryFakeCollector) Collect(ctx context.Context, _ *config.Config, _ *models.Website) (WPInventoryRunResult, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxConcurrent {
		f.maxConcurrent = f.active
	}
	var step wpInventoryFakeStep
	if len(f.steps) > 0 {
		step = f.steps[0]
		f.steps = f.steps[1:]
	} else {
		step.err = errors.New("unexpected collector call")
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	if step.started != nil {
		close(step.started)
	}
	if step.waitForCancel {
		<-ctx.Done()
		return WPInventoryRunResult{}, ctx.Err()
	}
	if step.delay > 0 {
		timer := time.NewTimer(step.delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return WPInventoryRunResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	if step.panicValue != nil {
		panic(step.panicValue)
	}
	return step.result, step.err
}

func (f *wpInventoryFakeCollector) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *wpInventoryFakeCollector) maxConcurrentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxConcurrent
}

func newTestWPInventoryWorker(t *testing.T, store *wpInventoryStore, collector wpInventoryCollector, owner string) *WPInventoryWorker {
	t.Helper()
	worker, err := newWPInventoryWorker(wpInventoryWorkerOptions{
		cfg: &config.Config{}, store: store, collector: collector, owner: owner,
		pollInterval: 5 * time.Millisecond, lease: time.Minute, now: time.Now,
	})
	if err != nil {
		t.Fatalf("newWPInventoryWorker(): %v", err)
	}
	return worker
}

func stopWPInventoryWorker(t *testing.T, worker *WPInventoryWorker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
}

func enqueueWPInventoryWorkerJob(t *testing.T, store *wpInventoryStore, siteID int, at time.Time) string {
	t.Helper()
	jobID, created, err := store.enqueue(context.Background(), siteID, wpInventoryTriggerManual, at, at)
	if err != nil || !created {
		t.Fatalf("enqueue job = %q/%t, err = %v", jobID, created, err)
	}
	return jobID
}

func insertWPInventoryWorkerSite(t *testing.T, store *wpInventoryStore, domain string) int {
	t.Helper()
	result, err := store.db.Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (?, ?, 'active', 'wp_worker', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf')`, domain, domain)
	if err != nil {
		t.Fatalf("insert worker site %s: %v", domain, err)
	}
	siteID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("worker site id %s: %v", domain, err)
	}
	return int(siteID)
}

func waitWPInventoryJobStatus(t *testing.T, store *wpInventoryStore, jobID string, want wpInventoryJobStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.getJob(context.Background(), jobID)
		if err == nil && job.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, err := store.getJob(context.Background(), jobID)
	t.Fatalf("job %s status = %s, err = %v, want %s", jobID, job.Status, err, want)
}

func TestWPInventoryWorkerDoesNotExposeArbitraryTaskSurface(t *testing.T) {
	store, _ := newWPInventoryStoreTest(t)
	_, err := newWPInventoryWorker(wpInventoryWorkerOptions{
		cfg: &config.Config{}, store: store, collector: &wpInventoryFakeCollector{},
		owner: "worker invalid owner", pollInterval: time.Second, lease: time.Minute, now: time.Now,
	})
	if err == nil {
		t.Fatal("invalid worker owner was accepted")
	}
	if fmt.Sprint(err) == "" {
		t.Fatal("invalid worker error is empty")
	}
}
