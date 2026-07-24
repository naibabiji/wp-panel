package executor

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestWPUpdateStorePlanSealClaimAndSuccess(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	task, err := store.createCoreManualPlan(ctx, WPUpdatePlan{
		SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.2.zip",
	}, now)
	if err != nil || task.Status != wpUpdatePreparing || task.ComponentType != "core" {
		t.Fatalf("create task = %+v, err=%v", task, err)
	}
	sealed, err := store.sealPlan(ctx, task.ID, strings.Repeat("a", 64), "official_verified", filepath.Join(t.TempDir(), "snapshot.zip"), now.Add(time.Second))
	if err != nil || sealed.Status != wpUpdateQueued || sealed.PlanSealedAt == "" {
		t.Fatalf("sealed task = %+v, err=%v", sealed, err)
	}
	claimed, err := store.claimCoreUpdate(ctx, task.ID, "worker-a", "7.0.1", now.Add(2*time.Second))
	if err != nil || claimed.Status != wpUpdateRunning || claimed.LeaseOwner != "worker-a" {
		t.Fatalf("claimed task = %+v, err=%v", claimed, err)
	}
	if ok, err := store.heartbeat(ctx, task.ID, "worker-a", now.Add(3*time.Second)); err != nil || !ok {
		t.Fatalf("heartbeat = %v, err=%v", ok, err)
	}
	if err := store.markSuccess(ctx, task.ID, "worker-a", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	finished, err := store.getTask(ctx, task.ID)
	if err != nil || finished.Status != wpUpdateSuccess || finished.LeaseOwner != "" {
		t.Fatalf("finished task = %+v, err=%v", finished, err)
	}
	if success, err := store.hasEffectiveManualSuccess(ctx, siteID); err != nil || !success {
		t.Fatalf("effective success=%v err=%v", success, err)
	}
	var events int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM wp_update_task_events WHERE task_id=?", task.ID).Scan(&events); err != nil || events != 4 {
		t.Fatalf("events=%d err=%v", events, err)
	}
}

func TestWPUpdateStoreRefreshCoreInventoryAfterUpdate(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	// Seed a pending core upgrade candidate and a stale cached version.
	stamp := wpUpdateDBTime(now)
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates
		(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?, 'core', 'wordpress', '7.0.2', 'upgrade', 'en_US', 'collection-a', ?)`, siteID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET wordpress_version='7.0.1', last_success_at=? WHERE site_id=?`, stamp, siteID); err != nil {
		t.Fatal(err)
	}
	if err := store.refreshCoreInventoryAfterUpdate(ctx, siteID, "7.0.2", now); err != nil {
		t.Fatal(err)
	}
	var version, lastSuccess string
	if err := store.db.QueryRow(`SELECT wordpress_version, last_success_at FROM site_wp_inventory_state WHERE site_id=?`, siteID).Scan(&version, &lastSuccess); err != nil {
		t.Fatal(err)
	}
	if version != "7.0.2" {
		t.Fatalf("wordpress_version = %q, want 7.0.2", version)
	}
	parsed, err := parseRequiredWPInventoryTime(lastSuccess)
	if err != nil {
		t.Fatalf("parse last_success_at: %v", err)
	}
	if !parsed.Equal(now) {
		t.Fatalf("last_success_at = %v, want %v", parsed, now)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM site_wp_component_updates WHERE site_id=? AND component_type='core' AND component_key='wordpress' AND response='upgrade'`, siteID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("remaining core candidates = %d, want 0", remaining)
	}
}

func TestWPUpdateStoreCreatesPluginPlanFromCurrentInventoryCandidate(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-a")
	task, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if task.ComponentType != "plugin" || task.ComponentKey != "sample/sample.php" || task.Status != wpUpdatePreparing {
		t.Fatalf("plugin task=%+v", task)
	}
}

func TestWPUpdateStoreCreatesThemePlanFromCurrentInventoryCandidate(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	seedThemeUpdateCandidate(t, store, siteID, "twentytwentyfive", "1.4", "1.5", "collection-theme")
	task, err := store.createThemeManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "twentytwentyfive", CurrentVersion: "1.4", TargetVersion: "1.5",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/theme/twentytwentyfive.1.5.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if task.ComponentType != "theme" || task.ComponentKey != "twentytwentyfive" || task.Status != wpUpdatePreparing {
		t.Fatalf("theme task=%+v", task)
	}
}

func TestWPUpdateStoreRejectsStaleOrUnsafePluginPlan(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-a")
	cases := []WPUpdatePlan{
		{SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "0.9.0", TargetVersion: "1.1.0", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip"},
		{SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.2.0", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.2.0.zip"},
		{SiteID: siteID, ComponentKey: "hello.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/hello.1.1.0.zip"},
		{SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0", PackageSource: "wordpress.org", DownloadURL: "https://evil.example/sample.zip"},
	}
	for _, plan := range cases {
		if _, err := store.createPluginManualPlan(context.Background(), plan, time.Now().UTC()); err == nil {
			t.Fatalf("unsafe plugin plan accepted: %+v", plan)
		}
	}
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET collection_id='collection-b' WHERE site_id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
	}, time.Now().UTC()); err == nil {
		t.Fatal("stale collection plugin plan accepted")
	}
}

func TestWPUpdateStoreSealedPlanIsImmutable(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now)
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET target_version='9.9.9' WHERE id=?`, task.ID); err == nil || !strings.Contains(err.Error(), "sealed update plan is immutable") {
		t.Fatalf("immutable trigger error=%v", err)
	}
}

func TestWPUpdateStoreFailPreparingPlan(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task, err := store.createCoreManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.2.zip",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.failPreparingPlan(context.Background(), task.ID, "package_prepare_failed", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failed, err := store.getTask(context.Background(), task.ID)
	if err != nil || failed.Status != wpUpdateFailed || failed.FailureStage != "package_prepare" || failed.FinishedAt == "" {
		t.Fatalf("failed task=%+v err=%v", failed, err)
	}
	var events int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events WHERE task_id=? AND stage='package_prepare' AND result='failed'`, task.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("failed events=%d err=%v", events, err)
	}
}

func haltPluginTaskForManualDecision(t *testing.T, store *wpUpdateStore, siteID int, now time.Time) WPUpdateTask {
	t.Helper()
	task, err := store.createCoreManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", PackageSource: "wordpress.org",
		DownloadURL: "https://downloads.wordpress.org/release.zip", SkipAutoRollback: true, BatchID: "wpub_test",
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

func TestWPUpdateStoreDisposeFailedTaskIgnored(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	if task.Status != wpUpdateFailed || task.RollbackStatus != "pending" || !task.RequiresAttention {
		t.Fatalf("halted task=%+v", task)
	}
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	disposed, err := store.getTask(context.Background(), task.ID)
	if err != nil || disposed.RequiresAttention || disposed.ManualDisposition != "marked_failed_no_action" || disposed.RollbackStatus != "pending" {
		t.Fatalf("disposed=%+v err=%v", disposed, err)
	}
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(5*time.Second)); err == nil {
		t.Fatal("expected second ignore-dispose to be rejected")
	}
}

func TestWPUpdateStoreManualRollbackClaimIsExclusive(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-b", now.Add(5*time.Second)); err == nil {
		t.Fatal("expected a second concurrent manual rollback claim to be rejected")
	}
	if err := store.abandonManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-b", now.Add(7*time.Second)); err != nil {
		t.Fatalf("expected claim to succeed after abandon: %v", err)
	}
	if err := store.finishManualRollback(context.Background(), task.ID, "rollback-owner-b", true, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	finished, err := store.getTask(context.Background(), task.ID)
	if err != nil || finished.RollbackStatus != "success" || finished.RequiresAttention || finished.ManualDisposition != "manually_rolled_back" {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestWPUpdateStoreIgnoreRejectedWhileRollbackClaimHeld(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(1*time.Second)); err != nil {
		t.Fatal(err)
	}
	// 回滚仍在进行（lease_owner 非空），忽略必须被拒绝，不能在回滚跑完前抢先改写处置结果。
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(2*time.Second)); err == nil {
		t.Fatal("expected ignore to be rejected while a manual rollback claim is held")
	}
	if err := store.finishManualRollback(context.Background(), task.ID, "rollback-owner-a", false, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	// 回滚本身失败了（rollback_status='failed'），但 requires_attention 仍然是 1、
	// manual_disposition 还没写入——这个任务仍然「等待决定」，忽略现在应该被允许
	// （否则用户会永久卡在无法处置这个任务的状态，见第三方审核发现的问题）。
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(4*time.Second)); err != nil {
		t.Fatalf("expected ignore to succeed after a failed rollback attempt: %v", err)
	}
	finished, err := store.getTask(context.Background(), task.ID)
	if err != nil || finished.RequiresAttention || finished.ManualDisposition != "marked_failed_no_action" || finished.RollbackStatus != "failed" {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestWPUpdateStoreManualRollbackRetrySucceedsAfterPreviousFailure(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(1*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.finishManualRollback(context.Background(), task.ID, "rollback-owner-a", false, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// 上一次回滚失败了，但用户选择再试一次，应该能重新认领。
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-b", now.Add(3*time.Second)); err != nil {
		t.Fatalf("expected retry claim to succeed after a previous rollback failure: %v", err)
	}
	if err := store.finishManualRollback(context.Background(), task.ID, "rollback-owner-b", true, now.Add(4*time.Second)); err != nil {
		t.Fatalf("expected retry to record success: %v", err)
	}
	finished, err := store.getTask(context.Background(), task.ID)
	if err != nil || finished.RequiresAttention || finished.RollbackStatus != "success" || finished.ManualDisposition != "manually_rolled_back" {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

// TestWPUpdateStoreAbandonManualRollbackReleasesRetryClaimAfterPreviousFailure 覆盖第三方
// 审核发现的问题：第一次人工回滚失败后（rollback_status='failed'），用户再次发起回滚，
// 但这次在 loadExecution/Prepare 阶段就出错（比如执行上下文已经损坏）需要放弃认领——
// abandonManualRollback 不能再要求 rollback_status='pending'，否则这次放弃会静默地
// 影响 0 行，lease_owner 永远不会被清空，用户既不能重新回滚也不能 Ignore。
func TestWPUpdateStoreAbandonManualRollbackReleasesRetryClaimAfterPreviousFailure(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	// 第一次人工回滚：认领后执行失败。
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.finishManualRollback(context.Background(), task.ID, "rollback-owner-a", false, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// 用户再次发起回滚：认领成功（rollback_status 现在是 'failed'，不是 'pending'）。
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-b", now.Add(3*time.Second)); err != nil {
		t.Fatalf("expected retry claim to succeed: %v", err)
	}
	// 这次在 loadExecution/Prepare 阶段就失败了，需要放弃认领。
	if err := store.abandonManualRollback(context.Background(), task.ID, "rollback-owner-b", now.Add(4*time.Second)); err != nil {
		t.Fatalf("expected abandon to release the retry claim: %v", err)
	}
	released, err := store.getTask(context.Background(), task.ID)
	if err != nil || released.LeaseOwner != "" {
		t.Fatalf("released=%+v err=%v, want lease_owner cleared", released, err)
	}
	// lease 已经释放，用户现在既能选择再次回滚，也能选择忽略——这里验证忽略能成功。
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(5*time.Second)); err != nil {
		t.Fatalf("expected ignore to succeed after the retry claim was abandoned: %v", err)
	}
}

// TestWPUpdateStoreAbandonManualRollbackRejectsWrongOwnerOrAlreadyDisposed 确认
// abandonManualRollback 现在会正确报告"没有实际释放任何东西"，而不是静默返回 nil：
// owner 不匹配、或者任务已经被处置过，都应该返回错误。
func TestWPUpdateStoreAbandonManualRollbackRejectsWrongOwnerOrAlreadyDisposed(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.abandonManualRollback(context.Background(), task.ID, "wrong-owner", now.Add(2*time.Second)); err == nil {
		t.Fatal("expected abandon with the wrong owner to fail")
	}
	if err := store.abandonManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	// 任务已经被忽略处置过，再次放弃认领应该失败（没有东西可放弃）。
	if err := store.abandonManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(5*time.Second)); err == nil {
		t.Fatal("expected abandon on an already-disposed task to fail")
	}
}

func TestWPUpdateStoreIgnoreSucceedsAfterRollbackLeaseAbandoned(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(1*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.abandonManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(3*time.Second)); err != nil {
		t.Fatalf("expected ignore to succeed once the rollback claim was released: %v", err)
	}
}

func TestWPUpdateStoreManualRollbackClaimRenewalExtendsLeaseAndBlocksIgnore(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := haltPluginTaskForManualDecision(t, store, siteID, now)
	if err := store.beginManualRollback(context.Background(), task.ID, "rollback-owner-a", now.Add(1*time.Second)); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟租约即将过期前的续租（人工回滚每一步开始前都会做一次），续租时刻要比认领时刻晚，
	// 否则算出来的新 lease_expires_at 会和原来一样，看不出续租效果。
	renewAt := now.Add(wpUpdateLease / 2)
	renewed, err := store.renewManualRollbackClaim(context.Background(), task.ID, "rollback-owner-a", renewAt)
	if err != nil || !renewed {
		t.Fatalf("renewed=%v err=%v", renewed, err)
	}
	afterRenew, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRenew.LeaseExpiresAt <= claimed.LeaseExpiresAt {
		t.Fatalf("lease not extended: before=%s after=%s", claimed.LeaseExpiresAt, afterRenew.LeaseExpiresAt)
	}
	// 一个不知道 owner 的第二方续租应该失败（说明续租本身也在做持有权校验，不是无条件续期）。
	if renewed, err := store.renewManualRollbackClaim(context.Background(), task.ID, "rollback-owner-b", renewAt); err != nil || renewed {
		t.Fatalf("renewed=%v err=%v, want a different owner's renewal to fail", renewed, err)
	}
	// 续租期间忽略仍然应该被拒绝。
	if err := store.disposeFailedTaskIgnored(context.Background(), task.ID, now.Add(5*time.Second)); err == nil {
		t.Fatal("expected ignore to still be rejected after lease renewal")
	}
}

func TestWPUpdateStoreCannotFailSealedPlanAsPreparing(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now)
	if err := store.failPreparingPlan(context.Background(), task.ID, "package_prepare_failed", now.Add(time.Second)); err == nil {
		t.Fatal("sealed plan was changed by preparing failure path")
	}
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateQueued {
		t.Fatalf("current task=%+v err=%v", current, err)
	}
}

func TestWPUpdateStoreLatestCoreUpdateTaskIsSiteScoped(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	now := time.Now().UTC()
	first, err := store.createCoreManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", PackageSource: "wordpress.org",
		DownloadURL: "https://downloads.wordpress.org/release/wordpress-7.0.2.zip",
	}, now)
	if err != nil || store.failPreparingPlan(context.Background(), first.ID, "package_prepare_failed", now) != nil {
		t.Fatalf("first task=%+v err=%v", first, err)
	}
	task := createAndSealUpdateTask(t, store, siteID, now)
	latest, err := store.latestCoreUpdateTask(context.Background(), siteID)
	if err != nil || latest.ID != task.ID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	if _, err := store.latestCoreUpdateTask(context.Background(), siteID+1); err == nil {
		t.Fatal("cross-site latest task lookup succeeded")
	}
}

func TestWPUpdateStoreBlocksConcurrentAndUnresolvedTasks(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now)
	_, err := store.createCoreManualPlan(ctx, WPUpdatePlan{SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.3", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/x.zip"}, now)
	if err == nil {
		t.Fatal("expected active-task conflict")
	}
	if _, err := store.claimCoreUpdate(ctx, task.ID, "worker-a", "7.0.1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.recoverExpired(ctx, now.Add(wpUpdateLease+2*time.Second)); err != nil || recovered != 1 {
		t.Fatalf("recover=%d err=%v", recovered, err)
	}
	interrupted, _ := store.getTask(ctx, task.ID)
	if interrupted.Status != wpUpdateInterrupted || !interrupted.RequiresAttention {
		t.Fatalf("interrupted task=%+v", interrupted)
	}
	if _, err := store.createCoreManualPlan(ctx, WPUpdatePlan{SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.3", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/x.zip"}, now); err == nil {
		t.Fatal("expected unresolved interruption to block new plan")
	}
	if err := store.disposeInterrupted(ctx, task.ID, "confirmed_target_version", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if success, err := store.hasEffectiveManualSuccess(ctx, siteID); err != nil || !success {
		t.Fatalf("confirmed result success=%v err=%v", success, err)
	}
}

func TestWPUpdateStoreClaimVersionMismatchFailsClosed(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now)
	if _, err := store.claimCoreUpdate(ctx, task.ID, "worker-a", "7.0.0", now.Add(time.Second)); err == nil {
		t.Fatal("expected version mismatch")
	}
	failed, err := store.getTask(ctx, task.ID)
	if err != nil || failed.Status != wpUpdateFailed || failed.FailureStage != "precheck" {
		t.Fatalf("failed task=%+v err=%v", failed, err)
	}
	if ok, err := store.heartbeat(ctx, task.ID, "worker-a", now); err != nil || ok {
		t.Fatalf("heartbeat after failure=%v err=%v", ok, err)
	}
}

func TestWPUpdateStoreClaimRechecksSiteStatus(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now)
	if _, err := store.db.Exec("UPDATE websites SET status='paused' WHERE id=?", siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.claimCoreUpdate(ctx, task.ID, "worker-a", "7.0.1", now.Add(time.Second)); err == nil {
		t.Fatal("expected paused site precheck failure")
	}
	failed, err := store.getTask(ctx, task.ID)
	if err != nil || failed.Status != wpUpdateFailed || failed.FailureStage != "precheck" {
		t.Fatalf("failed task=%+v err=%v", failed, err)
	}
}

func TestWPUpdateStoreConcurrentPlanCreation(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	ctx := context.Background()
	const callers = 8
	var wg sync.WaitGroup
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.createCoreManualPlan(ctx, WPUpdatePlan{
				SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2",
				PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/release.zip",
			}, time.Now().UTC())
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent plans=%d, want 1", succeeded)
	}
}

func newWPUpdateStoreTest(t *testing.T) (*wpUpdateStore, int) {
	t.Helper()
	if database.DB != nil {
		_ = database.Close()
	}
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := database.RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	result, err := database.DB.Exec(`INSERT INTO websites
		(name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,site_type)
		VALUES ('update.example.com','update.example.com','active','wp_update','/tmp/www','/tmp/log','db','dbuser','/tmp/php','/tmp/nginx','wordpress')`)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	_, err = database.DB.Exec(`INSERT INTO site_wp_inventory_state(site_id,status,wordpress_version,is_multisite)
		VALUES (?,'complete','7.0.1',0)`, siteID)
	if err != nil {
		t.Fatal(err)
	}
	return newWPUpdateStore(database.DB), int(siteID)
}

func createAndSealUpdateTask(t *testing.T, store *wpUpdateStore, siteID int, now time.Time) WPUpdateTask {
	t.Helper()
	task, err := store.createCoreManualPlan(context.Background(), WPUpdatePlan{SiteID: siteID, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/release.zip"}, now)
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.sealPlan(context.Background(), task.ID, strings.Repeat("a", 64), "official_verified", filepath.Join(t.TempDir(), "snapshot.zip"), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func seedPluginUpdateCandidate(t *testing.T, store *wpUpdateStore, siteID int, key, current, target, collection string) {
	seedComponentUpdateCandidate(t, store, siteID, "plugin", key, current, target, collection)
}

func seedThemeUpdateCandidate(t *testing.T, store *wpUpdateStore, siteID int, key, current, target, collection string) {
	seedComponentUpdateCandidate(t, store, siteID, "theme", key, current, target, collection)
}

func seedComponentUpdateCandidate(t *testing.T, store *wpUpdateStore, siteID int, componentType, key, current, target, collection string) {
	t.Helper()
	now := wpUpdateDBTime(time.Now().UTC())
	if _, err := store.db.Exec(`UPDATE site_wp_inventory_state SET collection_id=? WHERE site_id=?`, collection, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_components
		(site_id,component_type,component_key,name,version,collection_id,collected_at)
		VALUES (?,?,?,'Sample',?,?,?)`, siteID, componentType, key, current, collection, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates
		(site_id,component_type,component_key,target_version,response,locale,collection_id,collected_at)
		VALUES (?,?,?,?,'','',?,?)`, siteID, componentType, key, target, collection, now); err != nil {
		t.Fatal(err)
	}
}
