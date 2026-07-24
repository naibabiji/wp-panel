package executor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newWPPluginBatchServiceTest(t *testing.T) (*WPPluginBatchService, *wpUpdateStore, int) {
	t.Helper()
	store, siteID := newWPUpdateStoreTest(t)
	if _, err := store.db.Exec(`UPDATE websites SET web_root=? WHERE id=?`, filepath.Join(t.TempDir(), "wordpress"), siteID); err != nil {
		t.Fatal(err)
	}
	service, err := NewWPPluginBatchService(store.db, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return service, store, siteID
}

func TestWPPluginBatchServiceCreateAndGetRoundTrip(t *testing.T) {
	service, _, siteID := newWPPluginBatchServiceTest(t)
	batch, err := service.Create(context.Background(), siteID, "alice", []string{"a/a.php", "b/b.php"})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != "running" || batch.TotalCount != 2 || len(batch.Items) != 2 {
		t.Fatalf("batch=%+v", batch)
	}
	fetched, err := service.Get(context.Background(), siteID, batch.ID)
	if err != nil || fetched.ID != batch.ID || len(fetched.Items) != 2 {
		t.Fatalf("fetched=%+v err=%v", fetched, err)
	}
	if _, err := service.Get(context.Background(), siteID+1, batch.ID); err != ErrWPPluginUpdateNotFound {
		t.Fatalf("cross-site get err=%v want=%v", err, ErrWPPluginUpdateNotFound)
	}
	list, err := service.ListForSite(context.Background(), siteID)
	if err != nil || len(list) != 1 || len(list[0].Items) != 0 {
		t.Fatalf("list=%+v err=%v (list view should omit items)", list, err)
	}
}

// haltPluginBatchTaskForDecision 建一个属于批量（auto_rollback=0, batch_id 非空）的
// 插件更新任务，并直接把它推进到「失败挂起等待人工决定」状态，不经过真实 executor 执行
// （这条路径需要 root 权限运行 PHP runner，单测环境不具备）。
func haltPluginBatchTaskForDecision(t *testing.T, store *wpUpdateStore, siteID int, now time.Time) WPUpdateTask {
	t.Helper()
	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-plugin-batch")
	task, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
		SkipAutoRollback: true, BatchID: "wpub_test",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.sealPlan(context.Background(), task.ID, strings.Repeat("a", 64), "official_verified", filepath.Join(t.TempDir(), "snapshot.zip"), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stamp := wpUpdateDBTime(now.Add(2 * time.Second))
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='running',stage='claimed',lease_owner='worker-halt',
		lease_expires_at=?,started_at=?,updated_at=? WHERE id=? AND status='queued'`, stamp, stamp, stamp, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.haltForManualRollback(context.Background(), task.ID, "worker-halt", "health_check", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	halted, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return halted
}

func TestWPPluginBatchServiceRollbackRejectsWrongSite(t *testing.T) {
	service, store, siteID := newWPPluginBatchServiceTest(t)
	now := time.Now().UTC()
	task := haltPluginBatchTaskForDecision(t, store, siteID, now)
	if err := service.Rollback(context.Background(), siteID+1, task.ID); err != ErrWPPluginUpdateNotFound {
		t.Fatalf("err=%v want=%v", err, ErrWPPluginUpdateNotFound)
	}
	if err := service.Ignore(context.Background(), siteID+1, task.ID); err != ErrWPPluginUpdateNotFound {
		t.Fatalf("err=%v want=%v", err, ErrWPPluginUpdateNotFound)
	}
}

func TestWPPluginBatchServiceRollbackRejectsNonBatchTask(t *testing.T) {
	service, store, siteID := newWPPluginBatchServiceTest(t)
	now := time.Now().UTC()
	// createAndSealUpdateTask 建的是普通（非批量、auto_rollback=1）核心更新任务。
	task := createAndSealUpdateTask(t, store, siteID, now)
	if err := service.Rollback(context.Background(), siteID, task.ID); err != ErrWPPluginUpdateNotFound {
		t.Fatalf("err=%v want=%v", err, ErrWPPluginUpdateNotFound)
	}
}

func TestWPPluginBatchServiceIgnoreRejectsAlreadyResolvedTask(t *testing.T) {
	service, store, siteID := newWPPluginBatchServiceTest(t)
	now := time.Now().UTC()
	task := haltPluginBatchTaskForDecision(t, store, siteID, now)
	if err := service.Ignore(context.Background(), siteID, task.ID); err != nil {
		t.Fatalf("first ignore err=%v", err)
	}
	if err := service.Ignore(context.Background(), siteID, task.ID); err != ErrWPPluginUpdateConflict {
		t.Fatalf("second ignore err=%v want=%v", err, ErrWPPluginUpdateConflict)
	}
	if err := service.Rollback(context.Background(), siteID, task.ID); err != ErrWPPluginUpdateConflict {
		t.Fatalf("rollback after ignore err=%v want=%v", err, ErrWPPluginUpdateConflict)
	}
}

func TestWPPluginBatchServiceIgnoreAllowsRetryAfterFailedRollback(t *testing.T) {
	service, store, siteID := newWPPluginBatchServiceTest(t)
	now := time.Now().UTC()
	task := haltPluginBatchTaskForDecision(t, store, siteID, now)
	// 模拟一次人工回滚本身失败了（rollback_status='failed'，requires_attention 仍是 1）。
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.finishManualRollback(context.Background(), task.ID, "rollback-owner-a", false, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// 用户可以选择忽略，而不是被卡死。
	if err := service.Ignore(context.Background(), siteID, task.ID); err != nil {
		t.Fatalf("expected ignore to succeed after a failed rollback attempt: %v", err)
	}
}

// interruptedUnknownBatchTaskForDecision 建一个属于批量的插件更新任务，并直接把它
// 推进到 interrupted_unknown 状态（模拟 runner supervision 不确定的场景）。
func interruptedUnknownBatchTaskForDecision(t *testing.T, store *wpUpdateStore, siteID int, now time.Time) WPUpdateTask {
	t.Helper()
	seedPluginUpdateCandidate(t, store, siteID, "sample2/sample2.php", "1.0.0", "1.1.0", "collection-plugin-batch-2")
	task, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample2/sample2.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample2.1.1.0.zip",
		SkipAutoRollback: true, BatchID: "wpub_test2",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.sealPlan(context.Background(), task.ID, strings.Repeat("b", 64), "official_verified", filepath.Join(t.TempDir(), "snapshot2.zip"), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	stamp := wpUpdateDBTime(now.Add(2 * time.Second))
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='running',stage='claimed',lease_owner='worker-halt2',
		lease_expires_at=?,started_at=?,updated_at=? WHERE id=? AND status='queued'`, stamp, stamp, stamp, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.interruptOwned(context.Background(), task.ID, "worker-halt2", "runner_supervision_uncertain", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	interrupted, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return interrupted
}

// TestWPPluginBatchServiceIgnoreResolvesInterruptedUnknown 覆盖第三方审核发现的
// 「interrupted_unknown 会阻塞后续插件，且批量 UI 无法处置」问题：Rollback 必须拒绝
// interrupted_unknown（执行结果不确定，不能贸然恢复文件/数据库），但 Ignore 必须能
// 把它标记为已确认，解除 siteHasBlockingTask 对整个站点的阻塞。
func TestWPPluginBatchServiceIgnoreResolvesInterruptedUnknown(t *testing.T) {
	service, store, siteID := newWPPluginBatchServiceTest(t)
	now := time.Now().UTC()
	task := interruptedUnknownBatchTaskForDecision(t, store, siteID, now)
	if task.Status != wpUpdateInterrupted {
		t.Fatalf("task=%+v, want status=interrupted_unknown", task)
	}
	blocked, err := store.siteHasBlockingTask(context.Background(), siteID)
	if err != nil || !blocked {
		t.Fatalf("blocked=%v err=%v, want the site blocked before disposal", blocked, err)
	}
	if err := service.Rollback(context.Background(), siteID, task.ID); err != ErrWPPluginUpdateConflict {
		t.Fatalf("rollback err=%v want=%v (rollback must be rejected for an uncertain outcome)", err, ErrWPPluginUpdateConflict)
	}
	if err := service.Ignore(context.Background(), siteID, task.ID); err != nil {
		t.Fatalf("expected ignore to resolve interrupted_unknown: %v", err)
	}
	blocked, err = store.siteHasBlockingTask(context.Background(), siteID)
	if err != nil || blocked {
		t.Fatalf("blocked=%v err=%v, want the site unblocked after disposal", blocked, err)
	}
	if err := service.Ignore(context.Background(), siteID, task.ID); err != ErrWPPluginUpdateConflict {
		t.Fatalf("second ignore err=%v want=%v", err, ErrWPPluginUpdateConflict)
	}
}
