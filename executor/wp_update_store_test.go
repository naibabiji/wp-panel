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
