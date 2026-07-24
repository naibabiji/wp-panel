package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

func TestWPUpdateStoreCreatePluginBatchAndItems(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php", "b/b.php", "c/c.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != "running" || batch.TotalCount != 3 || batch.CreatedBy != "alice" {
		t.Fatalf("batch=%+v", batch)
	}
	items, err := store.listBatchItems(context.Background(), batch.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	for i, it := range items {
		if it.Position != i+1 || it.Status != "pending" {
			t.Fatalf("item[%d]=%+v", i, it)
		}
	}
	if _, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"d/d.php"}, now); err == nil {
		t.Fatal("expected a second running batch for the same site to be rejected")
	}
}

func TestWPUpdateStoreCreatePluginBatchRejectsDuplicatesAndBlockedSite(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	if _, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php", "a/a.php"}, now); err == nil {
		t.Fatal("expected duplicate component keys to be rejected")
	}
	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-plugin")
	if _, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php"}, now); err == nil {
		t.Fatal("expected batch creation to be rejected while site has a blocking task")
	}
}

// fakePluginBatchConfirmer 模拟 WPPluginUpdateService：ConfirmForBatch 成功时会在
// wp_update_tasks 里插入一行真实占用站点名额的记录，供编排器的「站点是否被阻塞」判断使用。
type fakePluginBatchConfirmer struct {
	store           *wpUpdateStore
	unavailable     map[string]bool
	confirmFails    map[string]bool
	confirmed       []string
	confirmedMode   map[string]string
	confirmedSource map[string]int64
}

func (f *fakePluginBatchConfirmer) Preview(_ context.Context, siteID int, _ string, componentKey string) (models.WPPluginUpdatePreview, error) {
	if f.unavailable[componentKey] {
		return models.WPPluginUpdatePreview{Available: false}, nil
	}
	return models.WPPluginUpdatePreview{Available: true, SiteID: siteID, ComponentKey: componentKey,
		CurrentVersion: "1.0.0", TargetVersion: "1.1.0", ConfirmationToken: "tok-" + componentKey}, nil
}

func (f *fakePluginBatchConfirmer) ConfirmForBatch(ctx context.Context, siteID int, _, componentKey, _, _, backupMode, batchID string, sourceID int64) (models.WPPluginUpdateTask, error) {
	if f.confirmFails[componentKey] {
		return models.WPPluginUpdateTask{}, errors.New("injected confirm failure")
	}
	id, err := newWPUpdateTaskID()
	if err != nil {
		return models.WPPluginUpdateTask{}, err
	}
	stamp := wpUpdateDBTime(time.Now().UTC())
	if _, err := f.store.db.ExecContext(ctx, `INSERT INTO wp_update_tasks
		(id,site_id,component_type,component_key,task_kind,trigger_type,status,stage,
		 current_version,target_version,package_source,download_url,auto_rollback,batch_id,requested_at,created_at,updated_at)
		VALUES (?,?,'plugin',?,'update','manual','queued','queued','1.0.0','1.1.0','wordpress.org','https://example.com/x.zip',0,?,?,?,?)`,
		id, siteID, componentKey, batchID, stamp, stamp, stamp); err != nil {
		return models.WPPluginUpdateTask{}, err
	}
	f.confirmed = append(f.confirmed, componentKey)
	if f.confirmedMode == nil {
		f.confirmedMode = map[string]string{}
	}
	if f.confirmedSource == nil {
		f.confirmedSource = map[string]int64{}
	}
	f.confirmedMode[componentKey] = backupMode
	f.confirmedSource[componentKey] = sourceID
	return models.WPPluginUpdateTask{ID: id, SiteID: siteID, ComponentKey: componentKey}, nil
}

func TestWPPluginBatchOrchestratorDispatchesSequentially(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php", "b/b.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmer := &fakePluginBatchConfirmer{store: store}
	orchestrator, err := newWPPluginBatchOrchestrator(store, confirmer)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	if want := []string{"a/a.php"}; len(confirmer.confirmed) != 1 || confirmer.confirmed[0] != want[0] {
		t.Fatalf("confirmed=%v want=%v", confirmer.confirmed, want)
	}
	items, err := store.listBatchItems(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != "dispatched" || items[0].TaskID == "" || items[1].Status != "pending" {
		t.Fatalf("items=%+v", items)
	}
	// 第一项仍占用站点唯一活跃任务名额，第二次 tick 不应该派发第二项。
	orchestrator.Tick(context.Background())
	if len(confirmer.confirmed) != 1 {
		t.Fatalf("confirmed=%v want only the first item dispatched while site is blocked", confirmer.confirmed)
	}
	// 模拟 worker 把第一项跑完。
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',finished_at=?,updated_at=? WHERE id=?`,
		wpUpdateDBTime(now.Add(time.Minute)), wpUpdateDBTime(now.Add(time.Minute)), items[0].TaskID); err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	if want := []string{"a/a.php", "b/b.php"}; len(confirmer.confirmed) != 2 || confirmer.confirmed[1] != want[1] {
		t.Fatalf("confirmed=%v want=%v", confirmer.confirmed, want)
	}
	items, err = store.listBatchItems(context.Background(), batch.ID)
	if err != nil || items[1].Status != "dispatched" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	// 模拟第二项也跑完，批量应当整体完成。
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',finished_at=?,updated_at=? WHERE id=?`,
		wpUpdateDBTime(now.Add(2*time.Minute)), wpUpdateDBTime(now.Add(2*time.Minute)), items[1].TaskID); err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	finishedBatch, err := store.getBatch(context.Background(), batch.ID)
	if err != nil || finishedBatch.Status != "completed" {
		t.Fatalf("finishedBatch=%+v err=%v", finishedBatch, err)
	}
}

func insertFakeDatabaseBackup(t *testing.T, store *wpUpdateStore, taskID string) int64 {
	t.Helper()
	result, err := store.db.Exec(`INSERT INTO wp_update_task_backups(task_id,kind,file_path,file_size,sha256,protected)
		VALUES (?, 'database', ?, 100, ?, 1)`, taskID, "/tmp/"+taskID+"-db.sql.gz", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestWPPluginBatchOrchestratorReusesSharedBackupAcrossFailedItem 覆盖第三方审核发现的
// 「首个插件失败后，后续插件会再次备份数据库」问题：item1 的备份就绪后即使 item1 本身
// 失败进入 requires_attention=1，item2 也必须复用同一份备份，而不是重新走 fresh。
// 同时覆盖「批量在末项待决定时不能被判定为已完成」——item1 一直卡在失败待决定状态，
// 批量不应该在 item2 成功后就被标记 completed，直到 item1 也被处置（忽略）为止。
func TestWPPluginBatchOrchestratorReusesSharedBackupAcrossFailedItem(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php", "b/b.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmer := &fakePluginBatchConfirmer{store: store}
	orchestrator, err := newWPPluginBatchOrchestrator(store, confirmer)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	items, err := store.listBatchItems(context.Background(), batch.ID)
	if err != nil || items[0].Status != "dispatched" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if confirmer.confirmedMode["a/a.php"] != "fresh" {
		t.Fatalf("first item backup mode=%q want=fresh", confirmer.confirmedMode["a/a.php"])
	}
	backupID := insertFakeDatabaseBackup(t, store, items[0].TaskID)
	// item1 更新失败，进入「等待人工决定」状态：requires_attention=1, rollback_status='pending'。
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='failed',rollback_status='pending',requires_attention=1,
		finished_at=?,updated_at=? WHERE id=?`, wpUpdateDBTime(now.Add(time.Minute)), wpUpdateDBTime(now.Add(time.Minute)), items[0].TaskID); err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	if confirmer.confirmedMode["b/b.php"] != "reuse" || confirmer.confirmedSource["b/b.php"] != backupID {
		t.Fatalf("second item mode=%q source=%d, want reuse of item1's backup id=%d",
			confirmer.confirmedMode["b/b.php"], confirmer.confirmedSource["b/b.php"], backupID)
	}
	batchAfterDispatch, err := store.getBatch(context.Background(), batch.ID)
	if err != nil || batchAfterDispatch.DatabaseBackupSourceID != backupID {
		t.Fatalf("batch=%+v err=%v, want database_backup_source_id=%d fixed on the batch", batchAfterDispatch, err, backupID)
	}
	items, err = store.listBatchItems(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	// item2 也"完成"（成功），但 item1 仍然卡在等待决定，批量不能被判定为已完成。
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',finished_at=?,updated_at=? WHERE id=?`,
		wpUpdateDBTime(now.Add(2*time.Minute)), wpUpdateDBTime(now.Add(2*time.Minute)), items[1].TaskID); err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	stillRunning, err := store.getBatch(context.Background(), batch.ID)
	if err != nil || stillRunning.Status != "running" {
		t.Fatalf("batch=%+v err=%v, want still running while item1 awaits a decision", stillRunning, err)
	}
	// 用户忽略 item1 之后，批量才能被判定为完成。
	if err := store.disposeFailedTaskIgnored(context.Background(), items[0].TaskID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	completed, err := store.getBatch(context.Background(), batch.ID)
	if err != nil || completed.Status != "completed" {
		t.Fatalf("batch=%+v err=%v, want completed once item1 is resolved", completed, err)
	}
}

// TestWPPluginBatchOrchestratorRecoversOrphanedTaskInsteadOfDuplicating 覆盖第三方审核发现的
// 「Confirm 成功但标记 dispatched 失败」问题：批量项仍是 pending，但真实任务已经存在
// （比如上一次派发在 markBatchItemDispatched 之前崩溃）。编排器必须回填这个已有任务，
// 而不是再调用一次 Confirm 创建重复任务。
func TestWPPluginBatchOrchestratorRecoversOrphanedTaskInsteadOfDuplicating(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	orphanID := "wpu_orphan0000000000000000000000"
	stamp := wpUpdateDBTime(now)
	if _, err := store.db.Exec(`INSERT INTO wp_update_tasks
		(id,site_id,component_type,component_key,task_kind,trigger_type,status,stage,
		 current_version,target_version,package_source,download_url,auto_rollback,batch_id,requested_at,created_at,updated_at)
		VALUES (?,?,'plugin','a/a.php','update','manual','queued','queued','1.0.0','1.1.0','wordpress.org','https://example.com/x.zip',0,?,?,?,?)`,
		orphanID, siteID, batch.ID, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	confirmer := &fakePluginBatchConfirmer{store: store}
	orchestrator, err := newWPPluginBatchOrchestrator(store, confirmer)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	if len(confirmer.confirmed) != 0 {
		t.Fatalf("confirmed=%v, want ConfirmForBatch not called when an orphaned task already exists", confirmer.confirmed)
	}
	items, err := store.listBatchItems(context.Background(), batch.ID)
	if err != nil || items[0].Status != "dispatched" || items[0].TaskID != orphanID {
		t.Fatalf("items=%+v err=%v, want the orphaned task backfilled", items, err)
	}
}

// TestWPPluginBatchOrchestratorIsolatesMultipleSites 覆盖第三方审核建议补充的多站点隔离
// 测试：两个站点各自的批量互不干扰，一个站点的活跃任务不会阻塞另一个站点的批量派发，
// 且各自的共享数据库备份来源也不会串到对方身上。
func TestWPPluginBatchOrchestratorIsolatesMultipleSites(t *testing.T) {
	store, siteA := newWPUpdateStoreTest(t)
	siteB := seedSecondWebsiteForBatchTest(t, store)
	now := time.Now().UTC()
	batchA, err := store.createPluginBatch(context.Background(), siteA, "alice", []string{"a/a.php", "b/b.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	batchB, err := store.createPluginBatch(context.Background(), siteB, "bob", []string{"c/c.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmer := &fakePluginBatchConfirmer{store: store}
	orchestrator, err := newWPPluginBatchOrchestrator(store, confirmer)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	if want := map[string]bool{"a/a.php": true, "c/c.php": true}; len(confirmer.confirmed) != 2 || !want[confirmer.confirmed[0]] || !want[confirmer.confirmed[1]] {
		t.Fatalf("confirmed=%v, want site A's first item and site B's only item dispatched independently", confirmer.confirmed)
	}
	itemsA, err := store.listBatchItems(context.Background(), batchA.ID)
	if err != nil || itemsA[0].Status != "dispatched" || itemsA[1].Status != "pending" {
		t.Fatalf("itemsA=%+v err=%v", itemsA, err)
	}
	itemsB, err := store.listBatchItems(context.Background(), batchB.ID)
	if err != nil || itemsB[0].Status != "dispatched" {
		t.Fatalf("itemsB=%+v err=%v", itemsB, err)
	}
	// site B 的任务完成不应该影响 site A 仍然被自己的第一项占用而无法派发第二项。
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',updated_at=? WHERE id=?`,
		wpUpdateDBTime(now), itemsB[0].TaskID); err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	itemsA, err = store.listBatchItems(context.Background(), batchA.ID)
	if err != nil || itemsA[1].Status != "pending" {
		t.Fatalf("itemsA=%+v err=%v, want site A's second item still pending (unaffected by site B completing)", itemsA, err)
	}
	batchAAfter, err := store.getBatch(context.Background(), batchA.ID)
	if err != nil || batchAAfter.DatabaseBackupSourceID != 0 {
		t.Fatalf("batchA=%+v err=%v, want no backup source recorded yet (site A's first item never got a backup row in this test)", batchAAfter, err)
	}
}

func seedSecondWebsiteForBatchTest(t *testing.T, store *wpUpdateStore) int {
	t.Helper()
	result, err := store.db.Exec(`INSERT INTO websites
		(name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,site_type)
		VALUES ('update2.example.com','update2.example.com','active','wp_update2','/tmp/www2','/tmp/log2','db2','dbuser2','/tmp/php2','/tmp/nginx2','wordpress')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

// TestWPPluginBatchOrchestratorInterruptedUnknownBlocksUntilAcknowledged 覆盖第三方审核
// 发现的「interrupted_unknown 会阻塞后续插件，且批量无法处置」问题：item1 进入
// interrupted_unknown 后，item2 不会被派发（站点被阻塞）；只有在用户通过
// disposeInterrupted 确认之后，批量才能继续推进到 item2。
func TestWPPluginBatchOrchestratorInterruptedUnknownBlocksUntilAcknowledged(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php", "b/b.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmer := &fakePluginBatchConfirmer{store: store}
	orchestrator, err := newWPPluginBatchOrchestrator(store, confirmer)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	items, err := store.listBatchItems(context.Background(), batch.ID)
	if err != nil || items[0].Status != "dispatched" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	// item1 的 runner supervision 不确定，进入 interrupted_unknown（先模拟 worker 已经
	// 认领并在跑这个任务，interruptOwned 要求 status='running' 且 lease_owner 匹配）。
	runningStamp := wpUpdateDBTime(now.Add(30 * time.Second))
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='running',lease_owner='worker-plugin',
		lease_expires_at=?,updated_at=? WHERE id=?`, runningStamp, runningStamp, items[0].TaskID); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.interruptOwned(context.Background(), items[0].TaskID, "worker-plugin", "runner_supervision_uncertain", now.Add(time.Minute)); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	orchestrator.Tick(context.Background())
	if len(confirmer.confirmed) != 1 {
		t.Fatalf("confirmed=%v, want item2 not dispatched while item1 is interrupted_unknown and undisposed", confirmer.confirmed)
	}
	batchStillRunning, err := store.getBatch(context.Background(), batch.ID)
	if err != nil || batchStillRunning.Status != "running" {
		t.Fatalf("batch=%+v err=%v", batchStillRunning, err)
	}
	// 用户确认（等价于批量 UI 的「忽略」按钮对 interrupted_unknown 的处理）。
	if err := store.disposeInterrupted(context.Background(), items[0].TaskID, "escalated", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	if want := []string{"a/a.php", "b/b.php"}; len(confirmer.confirmed) != 2 || confirmer.confirmed[1] != want[1] {
		t.Fatalf("confirmed=%v want=%v, item2 should dispatch once item1 is acknowledged", confirmer.confirmed, want)
	}
}

func TestWPPluginBatchOrchestratorSkipsUnavailableItemInSameTick(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	batch, err := store.createPluginBatch(context.Background(), siteID, "alice", []string{"a/a.php", "b/b.php"}, now)
	if err != nil {
		t.Fatal(err)
	}
	confirmer := &fakePluginBatchConfirmer{store: store, unavailable: map[string]bool{"a/a.php": true}}
	orchestrator, err := newWPPluginBatchOrchestrator(store, confirmer)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.Tick(context.Background())
	items, err := store.listBatchItems(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != "failed" || items[0].Message == "" {
		t.Fatalf("items[0]=%+v", items[0])
	}
	if items[1].Status != "dispatched" || items[1].TaskID == "" {
		t.Fatalf("items[1]=%+v", items[1])
	}
	if len(confirmer.confirmed) != 1 || confirmer.confirmed[0] != "b/b.php" {
		t.Fatalf("confirmed=%v", confirmer.confirmed)
	}
}
