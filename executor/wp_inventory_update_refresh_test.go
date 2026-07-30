package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingInventoryRefreshRequester struct {
	mu       sync.Mutex
	siteIDs  []int
	err      error
	deadline time.Time
}

func (r *recordingInventoryRefreshRequester) Request(ctx context.Context, siteID int, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.siteIDs = append(r.siteIDs, siteID)
	r.deadline, _ = ctx.Deadline()
	return r.err
}

func (r *recordingInventoryRefreshRequester) calls() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.siteIDs...)
}

func (r *recordingInventoryRefreshRequester) requestDeadline() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deadline
}

func TestWPInventoryUpdateRefreshRequesterReusesActiveJob(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	requester, err := newWPInventoryUpdateRefreshRequester(store.db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := requester.Request(context.Background(), siteID, now); err != nil {
		t.Fatal(err)
	}
	if err := requester.Request(context.Background(), siteID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM site_wp_inventory_jobs
		WHERE site_id=? AND status IN ('queued','running')`, siteID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("active inventory jobs=%d, want 1", jobs)
	}
	var trigger string
	if err := store.db.QueryRow(`SELECT trigger_type FROM site_wp_inventory_jobs WHERE site_id=?`, siteID).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if trigger != string(wpInventoryTriggerUpdateFollowup) {
		t.Fatalf("inventory trigger=%q, want %q", trigger, wpInventoryTriggerUpdateFollowup)
	}
}

func TestWPCoreUpdateWorkerRequestsRefreshOnlyForSuccessfulNonBatchTask(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().UTC())
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',stage='complete' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	refresh := &recordingInventoryRefreshRequester{}
	worker := &WPCoreUpdateWorker{store: store, inventoryRefresh: refresh, now: time.Now}
	worker.requestInventoryRefresh(task.ID)
	if calls := refresh.calls(); len(calls) != 1 || calls[0] != siteID {
		t.Fatalf("refresh calls=%v, want [%d]", calls, siteID)
	}

	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET batch_id='wpb_test' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	worker.requestInventoryRefresh(task.ID)
	if calls := refresh.calls(); len(calls) != 1 {
		t.Fatalf("batch task triggered per-item refresh: calls=%v", calls)
	}
}

func TestWPCoreUpdateWorkerDoesNotRefreshFailedOrUncertainTask(t *testing.T) {
	for _, status := range []string{wpUpdateFailed, wpUpdateInterrupted} {
		t.Run(status, func(t *testing.T) {
			store, siteID := newWPUpdateStoreTest(t)
			task := createAndSealUpdateTask(t, store, siteID, time.Now().UTC())
			if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status=? WHERE id=?`, status, task.ID); err != nil {
				t.Fatal(err)
			}
			refresh := &recordingInventoryRefreshRequester{}
			worker := &WPCoreUpdateWorker{store: store, inventoryRefresh: refresh, now: time.Now}
			worker.requestInventoryRefresh(task.ID)
			if calls := refresh.calls(); len(calls) != 0 {
				t.Fatalf("status %s triggered refresh: calls=%v", status, calls)
			}
		})
	}
}

func TestWPPluginBatchOrchestratorRequestsOneRefreshOnCompletion(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"sample/sample.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := newWPInventoryUpdateRefreshRequester(store.db)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator, err := newWPPluginBatchOrchestrator(store, &fakePluginBatchConfirmer{store: store}, refresh)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	orchestrator.Tick(context.Background())
	if jobs := countRows(t, store.db, "site_wp_inventory_jobs"); jobs != 0 {
		t.Fatalf("running final item created %d inventory jobs", jobs)
	}
	items, err := store.listBatchItems(context.Background(), batch.ID)
	if err != nil || len(items) != 1 || items[0].TaskID == "" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',finished_at=?,updated_at=? WHERE id=?`,
		wpUpdateDBTime(now.Add(time.Minute)), wpUpdateDBTime(now.Add(time.Minute)), items[0].TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET status='paused' WHERE id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			orchestrator.Tick(context.Background())
		}()
	}
	wg.Wait()
	var jobs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM site_wp_inventory_jobs
		WHERE site_id=? AND trigger_type='update_followup'`, siteID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("update follow-up inventory jobs=%d, want 1", jobs)
	}
}

func TestWPPluginBatchRefreshFailureDoesNotBlockCompletion(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"sample/sample.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE wp_update_batch_items SET status='failed' WHERE batch_id=?`, batch.ID); err != nil {
		t.Fatal(err)
	}
	refresh := &recordingInventoryRefreshRequester{err: errors.New("injected enqueue failure")}
	orchestrator, err := newWPPluginBatchOrchestrator(store, &fakePluginBatchConfirmer{store: store}, refresh)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	loaded, err := store.getBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "completed" {
		t.Fatalf("batch status=%q, want completed after refresh failure", loaded.Status)
	}
	if jobs := countRows(t, store.db, "site_wp_inventory_jobs"); jobs != 0 {
		t.Fatalf("inventory jobs=%d after failed refresh request", jobs)
	}
	if calls := refresh.calls(); len(calls) != 1 || calls[0] != siteID {
		t.Fatalf("refresh calls=%v, want failed attempt for site %d", calls, siteID)
	}
	remaining := time.Until(refresh.requestDeadline())
	if remaining <= 0 || remaining > wpInventoryUpdateRefreshTimeout {
		t.Fatalf("refresh deadline remaining=%s, want within (0, %s]", remaining, wpInventoryUpdateRefreshTimeout)
	}
}
