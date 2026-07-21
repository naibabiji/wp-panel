package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

func TestWPInventoryScheduleEligibility(t *testing.T) {
	t.Parallel()

	for _, status := range []models.WebsiteStatus{models.StatusActive, models.StatusPaused, models.StatusError} {
		if !wpInventoryScheduleEligible(wpInventoryScheduleSite{ID: 1, SiteType: "wordpress", Status: status}) {
			t.Fatalf("expected status %q to be eligible", status)
		}
	}
	for _, site := range []wpInventoryScheduleSite{
		{ID: 0, SiteType: "wordpress", Status: models.StatusActive},
		{ID: 1, SiteType: "php", Status: models.StatusActive},
		{ID: 1, SiteType: "wordpress", Status: models.StatusCreating},
		{ID: 1, SiteType: "wordpress", Status: models.StatusDeleting},
		{ID: 1, SiteType: "wordpress", Status: models.WebsiteStatus("future")},
	} {
		if wpInventoryScheduleEligible(site) {
			t.Fatalf("unexpected eligible site: %+v", site)
		}
	}
}

func TestWPInventoryScheduleOffsetIsStableAndBounded(t *testing.T) {
	t.Parallel()

	interval := 6 * time.Hour
	first, ok := wpInventoryScheduleOffset(42, interval)
	if !ok || first < 0 || first >= interval {
		t.Fatalf("invalid offset: %v, ok=%v", first, ok)
	}
	for i := 0; i < 100; i++ {
		got, ok := wpInventoryScheduleOffset(42, interval)
		if !ok || got != first {
			t.Fatalf("offset drifted: got %v want %v", got, first)
		}
	}
	if other, _ := wpInventoryScheduleOffset(43, interval); other == first {
		t.Fatal("adjacent site IDs unexpectedly received the same offset")
	}
	for _, input := range []struct {
		id       int
		interval time.Duration
	}{{0, interval}, {-1, interval}, {1, 0}, {1, -time.Second}} {
		if _, ok := wpInventoryScheduleOffset(input.id, input.interval); ok {
			t.Fatalf("invalid input accepted: %+v", input)
		}
	}
}

func TestWPInventoryScheduleDueUsesStableWindows(t *testing.T) {
	t.Parallel()

	interval := 6 * time.Hour
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	site := wpInventoryScheduleSite{ID: 7, SiteType: "wordpress", Status: models.StatusActive}
	if !wpInventoryScheduleDue(site, now, interval) {
		t.Fatal("site without successful inventory should be due")
	}
	offset, _ := wpInventoryScheduleOffset(site.ID, interval)
	windowStart := now.Add(-offset).Truncate(interval).Add(offset)
	justBefore := windowStart.Add(-time.Nanosecond)
	justAfter := windowStart.Add(time.Nanosecond)
	site.LastSuccess = &justBefore
	if !wpInventoryScheduleDue(site, justAfter, interval) {
		t.Fatal("site should be due after entering the next stable window")
	}
	site.LastSuccess = &justAfter
	if wpInventoryScheduleDue(site, justAfter.Add(time.Second), interval) {
		t.Fatal("site should not be due twice in the same stable window")
	}
	if wpInventoryScheduleDue(site, time.Time{}, interval) || wpInventoryScheduleDue(site, now, 0) {
		t.Fatal("invalid time or interval should not be due")
	}
}

func TestWPInventoryScheduleDueUsesLatestScheduledRequest(t *testing.T) {
	t.Parallel()
	interval := 6 * time.Hour
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	oldSuccess := now.Add(-24 * time.Hour)
	recentScheduled := now.Add(-time.Minute)
	site := wpInventoryScheduleSite{ID: 9, SiteType: "wordpress", Status: models.StatusActive,
		LastSuccess: &oldSuccess, LastScheduledRequest: &recentScheduled}
	if wpInventoryScheduleDue(site, now, interval) {
		t.Fatal("scheduled attempt in current window should prevent an immediate retry")
	}
	if !wpInventoryScheduleDue(site, now.Add(interval), interval) {
		t.Fatal("scheduled attempt should be eligible in the next stable window")
	}
}

func TestWPInventoryScheduleBatchesBoundariesAndCopies(t *testing.T) {
	t.Parallel()

	for _, count := range []int{0, 1, 100, 101, 300} {
		ids := make([]int, count)
		for i := range ids {
			ids[i] = i + 1
		}
		got := wpInventoryScheduleBatches(ids, 20, 100)
		wantTotal := count
		if wantTotal > 100 {
			wantTotal = 100
		}
		total := 0
		for _, batch := range got {
			if len(batch) == 0 || len(batch) > 20 {
				t.Fatalf("count %d produced invalid batch size %d", count, len(batch))
			}
			total += len(batch)
		}
		if total != wantTotal {
			t.Fatalf("count %d: got total %d want %d", count, total, wantTotal)
		}
	}

	ids := []int{1, 2, 3}
	got := wpInventoryScheduleBatches(ids, 2, 3)
	if !reflect.DeepEqual(got, [][]int{{1, 2}, {3}}) {
		t.Fatalf("unexpected batches: %#v", got)
	}
	ids[0] = 99
	if got[0][0] != 1 {
		t.Fatal("returned batches alias the caller slice")
	}
	if got := wpInventoryScheduleBatches(ids, 0, 3); got != nil {
		t.Fatalf("invalid batch size returned %#v", got)
	}
}

type fakeWPInventorySchedulerStore struct {
	mu         sync.Mutex
	candidates []wpInventoryScheduleSite
	enqueued   []int
	listCalls  int
}

func (f *fakeWPInventorySchedulerStore) listScheduleCandidates(context.Context, int) ([]wpInventoryScheduleSite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return append([]wpInventoryScheduleSite(nil), f.candidates...), nil
}

func (f *fakeWPInventorySchedulerStore) enqueueEligibleScheduled(_ context.Context, siteID int, _ time.Time) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, siteID)
	return "job", true, nil
}

func TestWPInventorySchedulerCycleBatchesAndStopsAtLimit(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := &fakeWPInventorySchedulerStore{}
	for i := 1; i <= 300; i++ {
		store.candidates = append(store.candidates, wpInventoryScheduleSite{ID: i, SiteType: "wordpress", Status: models.StatusActive})
	}
	waits := make([]time.Duration, 0)
	scheduler, err := newWPInventoryScheduler(wpInventorySchedulerOptions{
		store: store, now: func() time.Time { return now },
		wait:            func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true },
		refreshInterval: 6 * time.Hour, pollInterval: 15 * time.Minute, batchDelay: time.Second,
		batchSize: 20, maxPerCycle: 100, candidateLimit: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.runCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.enqueued) != 100 {
		t.Fatalf("enqueued=%d want 100", len(store.enqueued))
	}
	if !reflect.DeepEqual(waits, []time.Duration{time.Second, time.Second, time.Second, time.Second}) {
		t.Fatalf("batch waits=%v", waits)
	}
}

func TestWPInventorySchedulerStartStopAndDuplicateStart(t *testing.T) {
	store := &fakeWPInventorySchedulerStore{}
	enteredWait := make(chan struct{}, 1)
	scheduler, err := newWPInventoryScheduler(wpInventorySchedulerOptions{
		store: store, now: time.Now,
		wait: func(ctx context.Context, _ time.Duration) bool {
			select {
			case enteredWait <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return false
		},
		refreshInterval: time.Hour, pollInterval: time.Hour, batchDelay: time.Second,
		batchSize: 20, maxPerCycle: 100, candidateLimit: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Start(); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Start(); err == nil {
		t.Fatal("duplicate Start succeeded")
	}
	select {
	case <-enteredWait:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not run immediately")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Stop(ctx); err != nil {
		t.Fatalf("duplicate Stop: %v", err)
	}
}

func TestWPInventorySchedulerMainWiringOrder(t *testing.T) {
	source, err := os.ReadFile("../main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	workerStarted := strings.Index(text, "inventoryWorker = candidate")
	schedulerStart := strings.Index(text, "executor.NewWPInventoryScheduler()")
	schedulerStop := strings.Index(text, "inventoryScheduler.Stop(context.Background())")
	workerStop := strings.Index(text, "inventoryWorker.Stop(context.Background())")
	if workerStarted < 0 || schedulerStart <= workerStarted {
		t.Fatal("scheduler is not started only after the worker succeeds")
	}
	if schedulerStop < 0 || workerStop <= schedulerStop {
		t.Fatal("shutdown order is not scheduler before worker")
	}
}

func TestWPInventoryStoreScheduledCandidatesAndEligibility(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	candidates, err := store.listScheduleCandidates(ctx, 300)
	if err != nil || len(candidates) != 1 || candidates[0].ID != siteID {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	jobID, created, err := store.enqueueEligibleScheduled(ctx, siteID, now)
	if err != nil || !created || jobID == "" {
		t.Fatalf("scheduled enqueue=%q/%t err=%v", jobID, created, err)
	}
	if candidates, err := store.listScheduleCandidates(ctx, 300); err != nil || len(candidates) != 0 {
		t.Fatalf("active job was not excluded: %+v err=%v", candidates, err)
	}
	if _, _, err := store.enqueueEligibleScheduled(ctx, siteID, now); err != nil {
		t.Fatalf("duplicate active job should be deduplicated: %v", err)
	}
	if got := countRows(t, store.db, "site_wp_inventory_jobs"); got != 1 {
		t.Fatalf("job rows=%d want 1", got)
	}
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_jobs SET status='failed', finished_at=?, error_code='runner_timeout' WHERE id=?`, wpInventoryDBTime(now.Add(time.Minute)), jobID); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.listScheduleCandidates(ctx, 300)
	if err != nil || len(candidates) != 1 || candidates[0].LastScheduledRequest == nil || !candidates[0].LastScheduledRequest.Equal(now) {
		t.Fatalf("latest scheduled request not retained: %+v err=%v", candidates, err)
	}
	if wpInventoryScheduleDue(candidates[0], now, 6*time.Hour) {
		t.Fatal("failed scheduled task was retried in the same stable window")
	}
	if _, err := store.db.Exec("DELETE FROM site_wp_inventory_jobs"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"UPDATE websites SET status='creating' WHERE id=?", "UPDATE websites SET site_type='php', status='active' WHERE id=?"} {
		if _, err := store.db.Exec("UPDATE websites SET site_type='wordpress', status='active' WHERE id=?", siteID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(change, siteID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.enqueueEligibleScheduled(ctx, siteID, now); !errors.Is(err, errWPInventoryScheduledSiteIneligible) {
			t.Fatalf("ineligible change %q returned %v", change, err)
		}
	}
}

func TestWPInventorySchedulerMaximumDatasetBudgetAndProgress(t *testing.T) {
	store, _ := newWPInventoryStoreTest(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 300; i++ {
		domain := fmt.Sprintf("scheduled-%03d.example.com", i)
		if _, err := tx.Exec(`INSERT INTO websites
			(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
			VALUES (?, ?, 'active', ?, ?, ?, ?, ?, ?, ?)`,
			domain, domain, fmt.Sprintf("wp_s%03d", i), "/tmp/www", "/tmp/log",
			fmt.Sprintf("db%d", i), fmt.Sprintf("dbu%d", i), "/tmp/php.conf", "/tmp/nginx.conf"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	candidates, err := store.listScheduleCandidates(ctx, 300)
	queryElapsed := time.Since(started)
	if err != nil || len(candidates) != 300 {
		t.Fatalf("candidates=%d err=%v", len(candidates), err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	started = time.Now()
	for _, candidate := range candidates[:100] {
		if _, created, err := store.enqueueEligibleScheduled(ctx, candidate.ID, now); err != nil || !created {
			t.Fatalf("enqueue site %d created=%t err=%v", candidate.ID, created, err)
		}
	}
	enqueueElapsed := time.Since(started)
	remaining, err := store.listScheduleCandidates(ctx, 300)
	if err != nil || len(remaining) != 200 {
		t.Fatalf("remaining=%d err=%v", len(remaining), err)
	}
	for _, candidate := range remaining {
		if candidate.ID <= 100 {
			t.Fatalf("active site %d still occupied the next candidate page", candidate.ID)
		}
	}
	t.Logf("300-site candidate query=%s, 100 scheduled enqueues=%s", queryElapsed, enqueueElapsed)
	if queryElapsed > 100*time.Millisecond || enqueueElapsed > time.Second {
		t.Fatalf("scheduler budget exceeded: query=%s enqueue=%s", queryElapsed, enqueueElapsed)
	}
}
