package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

type fakeMigrationNginxRunner struct {
	testErr, reloadErr, verifyErr error
	tests, reloads, verifies      int
	reloadResults                 []error
}

func (f *fakeMigrationNginxRunner) Test(context.Context) error { f.tests++; return f.testErr }
func (f *fakeMigrationNginxRunner) Reload(context.Context) error {
	f.reloads++
	if len(f.reloadResults) >= f.reloads {
		return f.reloadResults[f.reloads-1]
	}
	return f.reloadErr
}
func (f *fakeMigrationNginxRunner) Verify(context.Context, string, bool, string) error {
	f.verifies++
	return f.verifyErr
}

func TestRetrySiteMigrationVerificationWaitsForReloadedWorker(t *testing.T) {
	attempts := 0
	err := retrySiteMigrationVerification(context.Background(), time.Second, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("old Nginx worker")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRetrySiteMigrationVerificationStopsAtDeadline(t *testing.T) {
	started := time.Now()
	err := retrySiteMigrationVerification(context.Background(), 20*time.Millisecond, func() error {
		return errors.New("still old")
	})
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(started))
	}
}

func TestSiteMigrationFreezeSourcePersistsMaintenanceOverlay(t *testing.T) {
	freezer, runner, enabledPath, originalPath := setupMigrationFreezeTest(t)
	marker := strings.Repeat("a", 48)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", marker); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(enabledPath)
	if err != nil {
		t.Fatal(err)
	}
	if target == originalPath || !strings.Contains(target, ".wp-panel-migration-migration_0000001.conf") {
		t.Fatalf("enabled target=%q, want task maintenance config", target)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"return 503", "Retry-After", "no-store", marker, "access_log off"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("maintenance config missing %q", want)
		}
	}
	for _, forbidden := range []string{"fastcgi_pass", "proxy_pass", "index.php"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("maintenance config contains %q", forbidden)
		}
	}
	var stage string
	if err := database.GetDB().QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage); err != nil || stage != "source_frozen" {
		t.Fatalf("stage=%q err=%v", stage, err)
	}
	var snapshot string
	if err := database.GetDB().QueryRow(`SELECT settings_snapshot FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&snapshot); err != nil || !strings.Contains(snapshot, `"existing":"kept"`) || !strings.Contains(snapshot, marker) {
		t.Fatalf("settings snapshot was not merged: %q err=%v", snapshot, err)
	}
	if runner.tests != 1 || runner.reloads != 1 || runner.verifies != 1 {
		t.Fatalf("runner=%+v", runner)
	}
	if err := NewTemplateEngine(t.TempDir()).ApplyNginxConfig("server {}", originalPath, enabledPath); !errors.Is(err, errSiteMigrationBusy) {
		t.Fatalf("ApplyNginxConfig during migration=%v, want busy", err)
	}
	site := &models.Website{ID: 1, Domain: "example.com"}
	for _, task := range []*Task{
		{Payload: &PauseSitePayload{Site: site}},
		{Payload: &EnableSitePayload{Site: site}},
	} {
		var result TaskResult
		if _, ok := task.Payload.(*PauseSitePayload); ok {
			result = executePauseSite(task)
		} else {
			result = executeEnableSite(task)
		}
		if result.Success || !strings.Contains(result.Message, "迁移维护") {
			t.Fatalf("unexpected status task result=%+v", result)
		}
	}
	custom := executeSaveNginxCustom(&Task{Payload: &SaveNginxCustomPayload{Site: site, Content: "", PreContent: ""}})
	if custom.Success || !strings.Contains(custom.Message, "迁移维护") {
		t.Fatalf("custom config result=%+v", custom)
	}
}

func TestSiteMigrationCompleteSourceKeepsMaintenanceUntilWebsiteDeletion(t *testing.T) {
	freezer, _, enabledPath, originalPath := setupMigrationFreezeTest(t)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("a", 48)); err != nil {
		t.Fatal(err)
	}
	if _, err := freezer.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='transferring_database' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := freezer.db.Exec(`UPDATE site_migration_batches SET status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if err := freezer.CompleteSource(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	if err := freezer.CompleteSource(context.Background(), "migration_0000001"); err != nil {
		t.Fatalf("completion replay was not idempotent: %v", err)
	}
	var status, stage, lockStatus, websiteStatus string
	if err := freezer.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage); err != nil {
		t.Fatal(err)
	}
	if err := freezer.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='source'`).Scan(&lockStatus); err != nil {
		t.Fatal(err)
	}
	if err := freezer.db.QueryRow(`SELECT status FROM websites WHERE id=1`).Scan(&websiteStatus); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(enabledPath)
	if err != nil {
		t.Fatal(err)
	}
	if status != "completed" || stage != "completed" || websiteStatus != "migrated" || lockStatus != "active" || filepath.Clean(target) == filepath.Clean(originalPath) {
		t.Fatalf("status=%q stage=%q website=%q lock=%q target=%q", status, stage, websiteStatus, lockStatus, target)
	}
}

func TestSiteMigrationControlCompleteSourceIsAtomic(t *testing.T) {
	freezer, _, _, _ := setupMigrationFreezeTest(t)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("a", 48)); err != nil {
		t.Fatal(err)
	}
	if _, err := freezer.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='transferring_database' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := freezer.db.Exec(`UPDATE site_migration_batches SET status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := freezer.db.Exec(`CREATE TRIGGER fail_source_task_forget BEFORE DELETE ON site_migration_sites BEGIN SELECT RAISE(ABORT, 'injected forget failure'); END`); err != nil {
		t.Fatal(err)
	}
	control := &SiteMigrationControlService{db: freezer.db, freezer: freezer, now: freezer.now}
	if err := control.CompleteSource(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("injected task deletion failure was ignored")
	}
	var taskStatus, stage, websiteStatus, lockStatus string
	_ = freezer.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&taskStatus, &stage)
	_ = freezer.db.QueryRow(`SELECT status FROM websites WHERE id=1`).Scan(&websiteStatus)
	_ = freezer.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001'`).Scan(&lockStatus)
	if taskStatus != "awaiting_cutover" || stage != "transferring_database" || websiteStatus != "active" || lockStatus != "active" {
		t.Fatalf("partial completion survived rollback: task=%q stage=%q website=%q lock=%q", taskStatus, stage, websiteStatus, lockStatus)
	}
	if _, err := freezer.db.Exec(`DROP TRIGGER fail_source_task_forget`); err != nil {
		t.Fatal(err)
	}
	if err := control.CompleteSource(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	var tasks, locks int
	_ = freezer.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&tasks)
	_ = freezer.db.QueryRow(`SELECT COUNT(*) FROM site_migration_locks WHERE migration_site_id='migration_0000001'`).Scan(&locks)
	_ = freezer.db.QueryRow(`SELECT status FROM websites WHERE id=1`).Scan(&websiteStatus)
	if tasks != 0 || locks != 0 || websiteStatus != "migrated" {
		t.Fatalf("completion did not converge: tasks=%d locks=%d website=%q", tasks, locks, websiteStatus)
	}
}

func TestReconcileMigratedWebsiteStatusesRecognizesOrphanedMaintenanceLink(t *testing.T) {
	freezer, _, _, _ := setupMigrationFreezeTest(t)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("a", 48)); err != nil {
		t.Fatal(err)
	}
	if _, err := freezer.db.Exec(`DELETE FROM site_migration_sites WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	changed, err := ReconcileMigratedWebsiteStatuses(context.Background(), freezer.db, freezer.cfg)
	if err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	var status string
	if err := freezer.db.QueryRow(`SELECT status FROM websites WHERE id=1`).Scan(&status); err != nil || status != "migrated" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestReconcileMigratedWebsiteStatusesSkipsActiveMigration(t *testing.T) {
	freezer, _, _, _ := setupMigrationFreezeTest(t)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("a", 48)); err != nil {
		t.Fatal(err)
	}
	changed, err := ReconcileMigratedWebsiteStatuses(context.Background(), freezer.db, freezer.cfg)
	if err != nil || changed != 0 {
		t.Fatalf("changed=%d err=%v", changed, err)
	}
	var status string
	if err := freezer.db.QueryRow(`SELECT status FROM websites WHERE id=1`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestSiteMigrationFreezeSourceRollsBackOnReloadFailure(t *testing.T) {
	freezer, runner, enabledPath, originalPath := setupMigrationFreezeTest(t)
	runner.reloadErr = errors.New("injected reload failure")
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("b", 48)); err == nil {
		t.Fatal("FreezeSource unexpectedly succeeded")
	}
	target, err := os.Readlink(enabledPath)
	if err != nil || target != originalPath {
		t.Fatalf("rolled back target=%q err=%v", target, err)
	}
	var stage string
	if err := database.GetDB().QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage); err != nil || stage != "preflight_passed" {
		t.Fatalf("rolled back stage=%q err=%v", stage, err)
	}
	var resources int
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001'`).Scan(&resources)
	if resources != 0 {
		t.Fatalf("resources=%d, want 0", resources)
	}
}

func TestSiteMigrationFreezeFailsClosedWhenRuntimeRollbackFails(t *testing.T) {
	freezer, runner, enabledPath, originalPath := setupMigrationFreezeTest(t)
	runner.verifyErr = errors.New("injected verification failure")
	runner.reloadResults = []error{nil, errors.New("injected rollback reload failure")}
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("c", 48)); err == nil {
		t.Fatal("FreezeSource unexpectedly succeeded")
	}
	target, err := os.Readlink(enabledPath)
	if err != nil || target == originalPath {
		t.Fatalf("target=%q err=%v, maintenance overlay must remain", target, err)
	}
	var stage string
	if err := database.GetDB().QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage); err != nil || stage != "source_freezing" {
		t.Fatalf("stage=%q err=%v, want recoverable source_freezing", stage, err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("maintenance config removed after failed rollback: %v", err)
	}
}

func TestSiteMigrationFreezeRecoversAfterSymlinkSwitchRestart(t *testing.T) {
	freezer, runner, enabledPath, originalPath := setupMigrationFreezeTest(t)
	marker := strings.Repeat("d", 48)
	site, err := freezer.load(context.Background(), "migration_0000001")
	if err != nil {
		t.Fatal(err)
	}
	maintenancePath := filepath.Join(freezer.cfg.Paths.NginxSitesAvailable, ".wp-panel-migration-migration_0000001.conf")
	content, err := renderSiteMigrationMaintenance(site, "migration_0000001", marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := freezer.recordIntent(context.Background(), "migration_0000001", maintenancePath, originalPath, marker, site.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteMigrationFile(maintenancePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplaceSymlink(enabledPath, maintenancePath); err != nil {
		t.Fatal(err)
	}

	if err := freezer.FreezeSource(context.Background(), "migration_0000001", marker); err != nil {
		t.Fatal(err)
	}
	if runner.tests != 1 || runner.reloads != 1 || runner.verifies != 1 {
		t.Fatalf("recovery runner=%+v", runner)
	}
	var stage string
	_ = database.GetDB().QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage)
	if stage != "source_frozen" {
		t.Fatalf("recovered stage=%q", stage)
	}
}

func TestRenderSiteMigrationMaintenanceUsesEffectivePHPDocumentRootAndSSL(t *testing.T) {
	site := &siteMigrationFreezeSite{Domain: "example.com", WebRoot: "/srv/example", SiteType: "php", DocumentRootSubdir: "public",
		SSLEnabled: true, SSLCertPath: "/cert/fullchain.pem", SSLKeyPath: "/cert/key.pem"}
	content, err := renderSiteMigrationMaintenance(site, "migration_0000001", strings.Repeat("e", 48))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root /srv/example/public;", "listen 443 ssl", "ssl_certificate /cert/fullchain.pem", "ssl_certificate_key /cert/key.pem"} {
		if !strings.Contains(content, want) {
			t.Fatalf("SSL/public maintenance config missing %q", want)
		}
	}
}

func TestSiteMigrationAbandonSourceRestoresOriginalAndReleasesLock(t *testing.T) {
	freezer, _, enabledPath, originalPath := setupMigrationFreezeTest(t)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("r", 48)); err != nil {
		t.Fatal(err)
	}
	maintenancePath, err := os.Readlink(enabledPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := freezer.AbandonSource(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(enabledPath)
	if err != nil || target != originalPath {
		t.Fatalf("target=%q err=%v", target, err)
	}
	if _, err := os.Stat(maintenancePath); !os.IsNotExist(err) {
		t.Fatalf("maintenance config remains: %v", err)
	}
	var status, cleanup, lockStatus string
	var sourceSiteID, lockSiteID any
	_ = database.GetDB().QueryRow(`SELECT status,cleanup_status,source_site_id FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &cleanup, &sourceSiteID)
	_ = database.GetDB().QueryRow(`SELECT status,site_id FROM site_migration_locks WHERE migration_site_id='migration_0000001'`).Scan(&lockStatus, &lockSiteID)
	if status != "abandoned" || cleanup != "complete" || lockStatus != "released" || sourceSiteID != nil || lockSiteID != nil {
		t.Fatalf("status=%q cleanup=%q source_site_id=%v lock=%q lock_site_id=%v", status, cleanup, sourceSiteID, lockStatus, lockSiteID)
	}
	if _, err := database.GetDB().Exec(`DELETE FROM websites WHERE domain='example.com'`); err != nil {
		t.Fatalf("delete abandoned source website: %v", err)
	}
}

func TestSiteMigrationAbandonSourceRejectsActiveWorkerLease(t *testing.T) {
	freezer, _, enabledPath, _ := setupMigrationFreezeTest(t)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("r", 48)); err != nil {
		t.Fatal(err)
	}
	maintenancePath, err := os.Readlink(enabledPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := freezer.db.Exec(`UPDATE site_migration_sites SET status='running',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, freezerTestTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := freezer.AbandonSource(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("active worker lease allowed source restore")
	}
	target, err := os.Readlink(enabledPath)
	if err != nil || target != maintenancePath {
		t.Fatalf("maintenance link changed: target=%q err=%v", target, err)
	}
	var status, owner string
	if err := freezer.db.QueryRow(`SELECT status,lease_owner FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != "worker_0000000001" {
		t.Fatalf("status=%q owner=%q", status, owner)
	}
}

func TestSiteMigrationAbandonSourceReloadFailureKeepsMaintenanceAndCanRetry(t *testing.T) {
	freezer, runner, enabledPath, originalPath := setupMigrationFreezeTest(t)
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("s", 48)); err != nil {
		t.Fatal(err)
	}
	maintenancePath, _ := os.Readlink(enabledPath)
	runner.reloadResults = []error{nil, errors.New("injected restored reload failure")}
	if err := freezer.AbandonSource(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("reload failure reported abandon success")
	}
	target, err := os.Readlink(enabledPath)
	if err != nil || target != maintenancePath {
		t.Fatalf("maintenance rollback target=%q err=%v", target, err)
	}
	runner.reloadResults = nil
	if err := freezer.AbandonSource(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	target, err = os.Readlink(enabledPath)
	if err != nil || target != originalPath {
		t.Fatalf("retry target=%q err=%v", target, err)
	}
}

func TestSiteMigrationAbandonSourceArtifactCleanupFailureKeepsMaintenanceLock(t *testing.T) {
	freezer, _, enabledPath, _ := setupMigrationFreezeTest(t)
	freezer.cfg.Panel.DataDir = t.TempDir()
	if err := freezer.FreezeSource(context.Background(), "migration_0000001", strings.Repeat("t", 48)); err != nil {
		t.Fatal(err)
	}
	maintenancePath, _ := os.Readlink(enabledPath)
	freezer.removeAll = func(string) error { return errors.New("injected artifact cleanup failure") }
	if err := freezer.AbandonSource(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("artifact cleanup failure reported success")
	}
	target, err := os.Readlink(enabledPath)
	if err != nil || target != maintenancePath {
		t.Fatalf("maintenance target=%q err=%v", target, err)
	}
	var status, cleanup, lockStatus string
	_ = database.GetDB().QueryRow(`SELECT status,cleanup_status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &cleanup)
	_ = database.GetDB().QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001'`).Scan(&lockStatus)
	if status != "cleanup_failed" || cleanup != "failed" || lockStatus != "active" {
		t.Fatalf("status=%q cleanup=%q lock=%q", status, cleanup, lockStatus)
	}
}

func setupMigrationFreezeTest(t *testing.T) (*siteMigrationFreezer, *fakeMigrationNginxRunner, string, string) {
	t.Helper()
	store, siteID := newSiteMigrationStoreTest(t)
	base := t.TempDir()
	available, enabled := filepath.Join(base, "available"), filepath.Join(base, "enabled")
	if err := os.MkdirAll(available, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(enabled, 0755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(available, "example.conf")
	if err := os.WriteFile(original, []byte("server {}"), 0644); err != nil {
		t.Fatal(err)
	}
	enabledPath := filepath.Join(enabled, "example.conf")
	if err := os.Symlink(original, enabledPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=?,nginx_conf_path=?,aliases='',ssl_enabled=0 WHERE id=?`, filepath.Join(base, "www"), original, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET stage='preflight_passed',settings_snapshot='{"existing":"kept"}' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if err := store.acquireLock(context.Background(), "migration_0000001", &siteID, "example.com", "source", freezerTestTime()); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Paths.NginxSitesAvailable, cfg.Paths.NginxSitesEnabled = available, enabled
	runner := &fakeMigrationNginxRunner{}
	freezer, err := newSiteMigrationFreezer(store.db, cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	freezer.now = freezerTestTime
	return freezer, runner, enabledPath, original
}

func freezerTestTime() time.Time { return time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC) }
