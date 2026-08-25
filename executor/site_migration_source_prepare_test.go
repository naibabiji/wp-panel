package executor

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type fakeSiteMigrationSourcePrepareOps struct {
	events []string
	failAt string
	db     *sql.DB
}

func (f *fakeSiteMigrationSourcePrepareOps) run(name string) error {
	f.events = append(f.events, name)
	if f.failAt == name {
		return errors.New("injected " + name)
	}
	return nil
}
func (f *fakeSiteMigrationSourcePrepareOps) Freeze(context.Context, string, string) error {
	if err := f.run("freeze"); err != nil {
		return err
	}
	_, err := f.db.Exec(`UPDATE site_migration_sites SET stage='source_frozen',settings_snapshot=json_set(settings_snapshot,'$.source_marker_token',?)`, strings.Repeat("m", 48))
	return err
}
func (f *fakeSiteMigrationSourcePrepareOps) BuildManifest(context.Context, string) error {
	if err := f.run("manifest"); err != nil {
		return err
	}
	_, err := f.db.Exec(`UPDATE site_migration_sites SET stage='manifest_ready'`)
	return err
}
func (f *fakeSiteMigrationSourcePrepareOps) BuildDatabase(context.Context, string) error {
	if err := f.run("database"); err != nil {
		return err
	}
	_, err := f.db.Exec(`UPDATE site_migration_sites SET stage='transferring_database'`)
	return err
}
func (f *fakeSiteMigrationSourcePrepareOps) BuildCertificates(context.Context, string) error {
	return f.run("certificates")
}
func (f *fakeSiteMigrationSourcePrepareOps) Settings(context.Context, string) (SiteMigrationRuntimeSettings, error) {
	return SiteMigrationRuntimeSettings{MarkerToken: strings.Repeat("m", 48)}, f.run("settings")
}

func setupSiteMigrationSourcePreparationTest(t *testing.T) (*SiteMigrationSourcePreparationService, *fakeSiteMigrationSourcePrepareOps, int64) {
	t.Helper()
	database.Close()
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	result, err := database.GetDB().Exec(`INSERT INTO websites(name,domain,aliases,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES ('one','one.example.com','www.one.example.com','wordpress','wp_one','/www/one','/logs/one','db_one','user_one','/php/one','/nginx/one')`)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	if _, err := database.GetDB().Exec(`INSERT INTO site_migration_peers(id,status,protocol_version) VALUES ('peer_00000000001','paired',?)`, siteMigrationProtocolVersion); err != nil {
		t.Fatal(err)
	}
	ops := &fakeSiteMigrationSourcePrepareOps{}
	ops.db = database.GetDB()
	service, err := newSiteMigrationSourcePreparationService(database.GetDB(), ops)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }
	return service, ops, siteID
}

func TestSiteMigrationSourceDeclarationIsAtomicAndIdempotent(t *testing.T) {
	service, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	declarations := []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "ONE.EXAMPLE.COM", Aliases: []string{" WWW.One.Example.COM "}, SiteType: "wordpress"}}
	for i := 0; i < 2; i++ {
		if err := service.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", declarations); err != nil {
			t.Fatal(err)
		}
	}
	var batches, sites, locks int
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_batches`).Scan(&batches)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_sites`).Scan(&sites)
	_ = service.db.QueryRow(`SELECT COUNT(*) FROM site_migration_locks WHERE status='active'`).Scan(&locks)
	if batches != 1 || sites != 1 || locks != 1 {
		t.Fatalf("batches=%d sites=%d locks=%d", batches, sites, locks)
	}
}

func TestSiteMigrationSourceDeclarationRejectsChangedReplay(t *testing.T) {
	service, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	declaration := SiteMigrationSourceDeclaration{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}
	if err := service.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{declaration}); err != nil {
		t.Fatal(err)
	}
	declaration.Aliases = nil
	if err := service.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{declaration}); err == nil {
		t.Fatal("changed replay was accepted")
	}
}

func TestSiteMigrationSourceDeclarationReplaysAfterRuntimeSnapshotEnrichment(t *testing.T) {
	service, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	declaration := SiteMigrationSourceDeclaration{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}
	if err := service.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{declaration}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(`UPDATE site_migration_sites SET settings_snapshot=json_set(settings_snapshot,'$.source_marker_token',?,'$.migration_warnings.skipped_custom_commands',2) WHERE id=?`, strings.Repeat("m", 48), declaration.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{declaration}); err != nil {
		t.Fatalf("enriched source snapshot prevented idempotent declaration replay: %v", err)
	}
}

func TestSiteMigrationSourceBatchQueuesOnlyDeclaredReadySites(t *testing.T) {
	service, _, siteID := setupSiteMigrationSourcePreparationTest(t)
	if err := service.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.QueueBatch(context.Background(), "peer_00000000001", "batch_0000000001"); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = service.db.QueryRow(`SELECT status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status)
	if status != "queued" {
		t.Fatalf("status=%q", status)
	}
	if err := service.QueueBatch(context.Background(), "peer_00000000001", "batch_0000000001"); err != nil {
		t.Fatalf("lost queue response must be safely replayable: %v", err)
	}
}

func TestSiteMigrationSourcePreparationAcceptsWorkerClaimLease(t *testing.T) {
	service, ops, siteID := setupSiteMigrationSourcePreparationTest(t)
	ctx := context.Background()
	if err := service.DeclareBatch(ctx, "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.QueueBatch(ctx, "peer_00000000001", "batch_0000000001"); err != nil {
		t.Fatal(err)
	}
	store, err := newSiteMigrationStore(service.db)
	if err != nil {
		t.Fatal(err)
	}
	now := service.now().UTC()
	job, err := store.claimNext(ctx, "worker_00000000001", now, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if _, err := service.Prepare(ctx, "peer_00000000001", job.ID, strings.Repeat("m", 48), job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := service.db.QueryRow(`SELECT status FROM site_migration_sites WHERE id=?`, job.ID).Scan(&status); err != nil || status != "awaiting_cutover" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if got := strings.Join(ops.events, "|"); got != "freeze|manifest|database|certificates|settings" {
		t.Fatalf("events=%s", got)
	}
}

func TestSiteMigrationSourcePreparationRejectsInsufficientSpaceBeforeFreeze(t *testing.T) {
	service, ops, siteID := setupSiteMigrationSourcePreparationTest(t)
	ctx := context.Background()
	declaration := SiteMigrationSourceDeclaration{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress", DatabaseBytes: 32 << 20}
	if err := service.DeclareBatch(ctx, "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{declaration}); err != nil {
		t.Fatal(err)
	}
	service.dataRoot = t.TempDir()
	service.availableBytes = func(string) (uint64, error) { return 80 << 20, nil }
	if _, err := service.Prepare(ctx, "peer_00000000001", declaration.ID, strings.Repeat("m", 48), ""); err == nil {
		t.Fatal("insufficient source preparation space accepted")
	}
	if len(ops.events) != 0 {
		t.Fatalf("source freeze started before space rejection: %v", ops.events)
	}
	var status, code string
	if err := service.db.QueryRow(`SELECT status,error_code FROM site_migration_sites WHERE id=?`, declaration.ID).Scan(&status, &code); err != nil || status != "failed_retryable" || code != "source_prepare_failed" {
		t.Fatalf("status=%q code=%q err=%v", status, code, err)
	}
}

func TestSiteMigrationSourcePreparationFailureCanRetry(t *testing.T) {
	service, ops, siteID := setupSiteMigrationSourcePreparationTest(t)
	if err := service.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: siteID, Domain: "one.example.com", Aliases: []string{"www.one.example.com"}, SiteType: "wordpress"}}); err != nil {
		t.Fatal(err)
	}
	ops.failAt = "manifest"
	if _, err := service.Prepare(context.Background(), "peer_00000000001", "migration_0000001", strings.Repeat("m", 48), ""); err == nil {
		t.Fatal("failure was not reported")
	}
	var status, code string
	_ = service.db.QueryRow(`SELECT status,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &code)
	if status != "failed_retryable" || code != "source_prepare_failed" {
		t.Fatalf("status=%q code=%q", status, code)
	}
	ops.failAt = ""
	if _, err := service.Prepare(context.Background(), "peer_00000000001", "migration_0000001", strings.Repeat("m", 48), ""); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ops.events, "|") != "freeze|manifest|manifest|database|certificates|settings" {
		t.Fatalf("events=%v", ops.events)
	}
}
