package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeSiteMigrationTargetRollbackOps struct {
	events []string
	failAt string
}

func (f *fakeSiteMigrationTargetRollbackOps) step(event string) error {
	f.events = append(f.events, event)
	if f.failAt == event {
		return errors.New("injected " + event)
	}
	return nil
}
func (f *fakeSiteMigrationTargetRollbackOps) StopRuntime(siteMigrationPublishSpec) error {
	return f.step("stop_runtime")
}
func (f *fakeSiteMigrationTargetRollbackOps) RemovePath(path string) error {
	return f.step("remove:" + path)
}
func (f *fakeSiteMigrationTargetRollbackOps) DropDatabase(name, user string) error {
	return f.step("drop_database:" + name + ":" + user)
}
func (f *fakeSiteMigrationTargetRollbackOps) RemoveUser(name string) error {
	return f.step("remove_user:" + name)
}
func (f *fakeSiteMigrationTargetRollbackOps) ReloadCron() error { return f.step("reload_cron") }

func TestSiteMigrationTargetRollbackRemovesOnlyOwnedResources(t *testing.T) {
	service, siteID, ops := setupSiteMigrationTargetRollbackTest(t, false)
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err != nil {
		t.Fatal(err)
	}
	var status, stage, cleanup, lockStatus string
	var remaining, website, cron, customGroup int
	_ = service.db.QueryRow(`SELECT status,stage,cleanup_status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage, &cleanup)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target'`).Scan(&lockStatus)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND status!='removed'`).Scan(&remaining)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&website)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE site_id=?`, siteID).Scan(&cron)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM cdn_realip_groups WHERE name='Private CDN'`).Scan(&customGroup)
	if status != "abandoned" || stage != "abandoned" || cleanup != "complete" || lockStatus != "released" || remaining != 0 || website != 0 || cron != 0 || customGroup != 0 {
		t.Fatalf("status=%q stage=%q cleanup=%q lock=%q remaining=%d website=%d cron=%d group=%d", status, stage, cleanup, lockStatus, remaining, website, cron, customGroup)
	}
	if len(ops.events) == 0 || ops.events[0] != "reload_cron" || ops.events[1] != "stop_runtime" {
		t.Fatalf("unsafe rollback order: %v", ops.events)
	}
	eventCount := len(ops.events)
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err != nil || len(ops.events) != eventCount {
		t.Fatalf("completed rollback retry was not idempotent: err=%v events=%v", err, ops.events)
	}
}

func TestSiteMigrationTargetRollbackRejectsActiveWorkerLease(t *testing.T) {
	service, siteID, ops := setupSiteMigrationTargetRollbackTest(t, false)
	var beforeMonitoring int
	if err := service.db.QueryRow(`SELECT monitoring_enabled FROM websites WHERE id=?`, siteID).Scan(&beforeMonitoring); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE site_migration_sites SET status='running',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, freezerTestTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("active worker lease allowed target rollback")
	}
	if len(ops.events) != 0 {
		t.Fatalf("rollback touched external resources: %v", ops.events)
	}
	var status, owner string
	var monitoring int
	if err := service.db.QueryRow(`SELECT status,lease_owner FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRow(`SELECT monitoring_enabled FROM websites WHERE id=?`, siteID).Scan(&monitoring); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != "worker_0000000001" || monitoring != beforeMonitoring {
		t.Fatalf("status=%q owner=%q monitoring=%d before=%d", status, owner, monitoring, beforeMonitoring)
	}
}

func TestSiteMigrationTargetRollbackIgnoresLegacySourceRetireAuthorization(t *testing.T) {
	service, _, ops := setupSiteMigrationTargetRollbackTest(t, true)
	if _, err := service.db.Exec(`INSERT INTO site_migration_events(migration_site_id,stage,result,message,created_at)
		VALUES ('migration_0000001','source_retire_authorized','info','legacy authorization',?)`, freezerTestTime()); err != nil {
		t.Fatal(err)
	}
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "danger", true); err != nil {
		t.Fatalf("legacy authorization blocked simplified deletion: %v", err)
	}
	if len(ops.events) == 0 {
		t.Fatal("rollback did not clean target resources")
	}
	var count int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_events WHERE migration_site_id='migration_0000001' AND stage='source_retire_authorized'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("authorization event count=%d err=%v", count, err)
	}
}

type fakeMigrationTargetAbandoner struct {
	events *[]string
	err    error
}

func (f fakeMigrationTargetAbandoner) Abandon(context.Context, string, string, string, string, bool) error {
	*f.events = append(*f.events, "target")
	return f.err
}

type fakeMigrationSourceRestorer struct {
	events *[]string
	err    error
}

func (f fakeMigrationSourceRestorer) AbandonSource(context.Context, string) error {
	*f.events = append(*f.events, "source")
	return f.err
}

func TestSiteMigrationAbandonRestoresSourceOnlyAfterTargetCleanup(t *testing.T) {
	var events []string
	targetFailure := fakeMigrationTargetAbandoner{events: &events, err: errors.New("target cleanup failed")}
	source := fakeMigrationSourceRestorer{events: &events}
	if err := AbandonSiteMigration(context.Background(), targetFailure, source, "migration_0000001", "migration_0000002", "example.com", "admin", "", false); err == nil {
		t.Fatal("target cleanup failure was ignored")
	}
	if strings.Join(events, "|") != "target" {
		t.Fatalf("source restored before target cleanup: %v", events)
	}
	events = nil
	if err := AbandonSiteMigration(context.Background(), fakeMigrationTargetAbandoner{events: &events}, source, "migration_0000001", "migration_0000002", "example.com", "admin", "", false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, "|") != "target|source" {
		t.Fatalf("abandon order=%v", events)
	}
}

func TestSiteMigrationActivatedTargetRollbackRequiresRiskAcceptance(t *testing.T) {
	service, _, _ := setupSiteMigrationTargetRollbackTest(t, true)
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("activated target rollback was accepted without risk confirmation")
	}
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "possible target writes will be discarded", true); err != nil {
		t.Fatal(err)
	}
}

func TestSiteMigrationTargetRollbackRejectsUnownedCron(t *testing.T) {
	service, siteID, _ := setupSiteMigrationTargetRollbackTest(t, false)
	if _, err := service.db.Exec(`INSERT INTO cron_jobs (name,cron_expression,command,site_id,run_as_user,task_type,backup_mode,keep_count,notify_fail,enabled) VALUES ('extra','0 * * * *','',?,'root','wp_cron','incremental',1,0,0)`, siteID); err != nil {
		t.Fatal(err)
	}
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("unowned target Cron was deleted")
	}
	var status, cleanup, lockStatus string
	var website, extra int
	_ = service.db.QueryRow(`SELECT status,cleanup_status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &cleanup)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target'`).Scan(&lockStatus)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&website)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE site_id=? AND name='extra'`, siteID).Scan(&extra)
	if status != "awaiting_cutover" || cleanup != "not_needed" || lockStatus != "active" || website != 1 || extra != 1 {
		t.Fatalf("status=%q cleanup=%q lock=%q website=%d extra=%d", status, cleanup, lockStatus, website, extra)
	}
}

func TestSiteMigrationTargetRollbackRejectsChangedCDNBinding(t *testing.T) {
	service, siteID, _ := setupSiteMigrationTargetRollbackTest(t, false)
	var replacementID int64
	if err := service.db.QueryRow(`SELECT id FROM cdn_realip_groups WHERE name!='Private CDN' ORDER BY id LIMIT 1`).Scan(&replacementID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`DELETE FROM website_cdn_realip_groups WHERE website_id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`INSERT INTO website_cdn_realip_groups (website_id,group_id) VALUES (?,?)`, siteID, replacementID); err != nil {
		t.Fatal(err)
	}
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("changed CDN binding was deleted")
	}
	var website, binding int
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&website)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM website_cdn_realip_groups WHERE website_id=? AND group_id=?`, siteID, replacementID).Scan(&binding)
	if website != 1 || binding != 1 {
		t.Fatalf("website=%d binding=%d", website, binding)
	}
}

func TestSiteMigrationTargetRollbackRetriesFailedResource(t *testing.T) {
	service, _, ops := setupSiteMigrationTargetRollbackTest(t, false)
	webRoot := opsWebRoot(t, &SiteMigrationTargetResourceService{db: service.db})
	ops.failAt = "remove:" + webRoot
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err == nil {
		t.Fatal("resource removal failure was not reported")
	}
	var resourceStatus, taskStatus, lockStatus string
	_ = service.db.QueryRow(`SELECT status FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND resource_type='web_root'`).Scan(&resourceStatus)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&taskStatus)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='target'`).Scan(&lockStatus)
	if resourceStatus != "remove_failed" || taskStatus != "cleanup_failed" || lockStatus != "active" {
		t.Fatalf("resource=%q task=%q lock=%q", resourceStatus, taskStatus, lockStatus)
	}
	ops.failAt = ""
	if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err != nil {
		t.Fatal(err)
	}
}

func TestSiteMigrationTargetRollbackRecoversPartiallyMarkedGroups(t *testing.T) {
	for _, test := range []struct {
		name  string
		first string
	}{
		{"web publish", "web_root"},
		{"staging identities", "target_staging_root"},
		{"database resources", "database"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := setupSiteMigrationTargetRollbackTest(t, false)
			if _, err := service.db.Exec(`UPDATE site_migration_resources SET status='removed' WHERE migration_site_id='migration_0000001' AND resource_type=?`, test.first); err != nil {
				t.Fatal(err)
			}
			if err := service.Abandon(context.Background(), "migration_0000001", "example.com", "admin", "", false); err != nil {
				t.Fatal(err)
			}
			var remaining int
			_ = service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001' AND status!='removed'`).Scan(&remaining)
			if remaining != 0 {
				t.Fatalf("remaining=%d", remaining)
			}
		})
	}
}

func TestSiteMigrationTargetRollbackFinalizeFailureIsPersisted(t *testing.T) {
	service, _, _ := setupSiteMigrationTargetRollbackTest(t, false)
	if _, err := service.db.Exec(`UPDATE site_migration_resources SET status='removed' WHERE migration_site_id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE site_migration_sites SET status='cancelling',stage='cancelling',cleanup_status='running' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE site_migration_locks SET status='released' WHERE migration_site_id='migration_0000001' AND direction='target'`); err != nil {
		t.Fatal(err)
	}
	if err := service.finishRollback(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("changed final lock state was accepted")
	}
	var status, cleanup, code string
	_ = service.db.QueryRow(`SELECT status,cleanup_status,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &cleanup, &code)
	if status != "cleanup_failed" || cleanup != "failed" || code != "target_rollback_finalize_failed" {
		t.Fatalf("status=%q cleanup=%q code=%q", status, cleanup, code)
	}
}

func TestSiteMigrationTargetRollbackBeginDoesNotDisableRacingCron(t *testing.T) {
	service, siteID, _ := setupSiteMigrationTargetRollbackTest(t, false)
	scope, err := service.loadScope(context.Background(), "migration_0000001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE websites SET monitoring_enabled=1 WHERE id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`INSERT INTO cron_jobs (name,cron_expression,command,site_id,run_as_user,task_type,backup_mode,keep_count,notify_fail,enabled) VALUES ('racing','0 * * * *','',?,'root','wp_cron','incremental',1,0,1)`, siteID); err != nil {
		t.Fatal(err)
	}
	if err := service.beginRollback(context.Background(), "migration_0000001", scope, "admin", ""); err == nil {
		t.Fatal("racing Cron ownership change was accepted")
	}
	var enabled, monitoring int
	var status string
	_ = service.db.QueryRow(`SELECT enabled FROM cron_jobs WHERE site_id=? AND name='racing'`, siteID).Scan(&enabled)
	_ = service.db.QueryRow(`SELECT monitoring_enabled FROM websites WHERE id=?`, siteID).Scan(&monitoring)
	_ = service.db.QueryRow(`SELECT status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status)
	if enabled != 1 || monitoring != 1 || status != "awaiting_cutover" {
		t.Fatalf("enabled=%d monitoring=%d status=%q", enabled, monitoring, status)
	}
}

func setupSiteMigrationTargetRollbackTest(t *testing.T, activate bool) (*SiteMigrationTargetRollbackService, int64, *fakeSiteMigrationTargetRollbackOps) {
	t.Helper()
	resourceService, root := setupConfiguredMigrationPublisherTest(t)
	publisher, _ := NewSiteMigrationTargetPublisher(resourceService.db, resourceService.cfg, root)
	publisher.now = freezerTestTime
	if err := publisher.ConfigureAndHealth(context.Background(), "migration_0000001", &fakeMigrationTargetConfigureOps{}); err != nil {
		t.Fatal(err)
	}
	var siteID int64
	_ = resourceService.db.QueryRow(`SELECT target_site_id FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&siteID)
	if activate {
		outbound := strings.Repeat("o", 48)
		_, _ = resourceService.db.Exec(`UPDATE site_migration_peers SET status='paired',outbound_credential=? WHERE id='peer_00000000001'`, outbound)
		cutover, _ := NewSiteMigrationCutoverService(resourceService.db)
		cutover.ops = &fakeSiteMigrationCutoverOps{}
		cutover.now = freezerTestTime
		if err := cutover.Confirm(context.Background(), "migration_0000001", "example.com", "admin", "operator override", true); err != nil {
			t.Fatal(err)
		}
	}
	service, _ := NewSiteMigrationTargetRollbackService(resourceService.db, resourceService.cfg, root)
	ops := &fakeSiteMigrationTargetRollbackOps{}
	service.ops = ops
	service.now = freezerTestTime
	return service, siteID, ops
}
