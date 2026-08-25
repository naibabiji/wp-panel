package executor

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func newSiteMigrationStoreTest(t *testing.T) (*siteMigrationStore, int) {
	t.Helper()
	database.Close()
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	result, err := database.GetDB().Exec(`INSERT INTO websites
		(name,domain,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES ('example','example.com','wp_example','/www/example','/logs/example','db_example','user_example','/php/example','/nginx/example')`)
	if err != nil {
		t.Fatal(err)
	}
	siteID64, _ := result.LastInsertId()
	if _, err := database.GetDB().Exec(`INSERT INTO site_migration_peers(id,status,protocol_version) VALUES ('peer_00000000001','paired',?)`, siteMigrationProtocolVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO site_migration_batches(id,peer_id,direction) VALUES ('batch_0000000001','peer_00000000001','source')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO site_migration_sites
		(id,batch_id,source_site_id,source_domain,target_domain,site_type,status,stage)
		VALUES ('migration_0000001','batch_0000000001',?,'example.com','example.com','wordpress','queued','publishing')`, siteID64); err != nil {
		t.Fatal(err)
	}
	store, err := newSiteMigrationStore(database.GetDB())
	if err != nil {
		t.Fatal(err)
	}
	return store, int(siteID64)
}

func TestSiteMigrationStoreLockPersistsAndReleases(t *testing.T) {
	store, siteID := newSiteMigrationStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if err := store.acquireLock(ctx, "migration_0000001", &siteID, "example.com", "source", now); err != nil {
		t.Fatal(err)
	}
	if err := store.acquireLock(ctx, "migration_0000001", &siteID, "example.com", "source", now); !errors.Is(err, errSiteMigrationBusy) {
		t.Fatalf("second acquire=%v, want busy", err)
	}
	locked, err := store.isLocked(ctx, siteID, "example.com")
	if err != nil || !locked {
		t.Fatalf("locked=%v err=%v", locked, err)
	}
	if err := store.releaseLock(ctx, "migration_0000001", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	locked, err = store.isLocked(ctx, siteID, "example.com")
	if err != nil || locked {
		t.Fatalf("after release locked=%v err=%v", locked, err)
	}
}

func TestSiteMigrationStoreClaimHeartbeatAndRecovery(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	job, err := store.claimNext(ctx, "worker_0000000001", now, time.Minute)
	if err != nil || job == nil || job.Status != "running" || job.AttemptCount != 1 {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	if err := store.heartbeat(ctx, job.ID, "worker_0000000001", now.Add(20*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.recoverExpired(ctx, now.Add(30*time.Second)); err != nil || changed != 0 {
		t.Fatalf("early recovery changed=%d err=%v", changed, err)
	}
	if changed, err := store.recoverExpired(ctx, now.Add(2*time.Minute)); err != nil || changed != 1 {
		t.Fatalf("expired recovery changed=%d err=%v", changed, err)
	}
	recovered, err := getSiteMigrationSite(ctx, store.db, job.ID)
	if err != nil || recovered.Status != "interrupted_unknown" || recovered.Stage != "publishing" || recovered.LeaseOwner != "" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
}

func TestSiteMigrationStoreReleaseClaimPreservesProcessorState(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	job, err := store.claimNext(context.Background(), "worker_0000000001", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='awaiting_cutover' WHERE id=? AND lease_owner=?`, job.ID, "worker_0000000001"); err != nil {
		t.Fatal(err)
	}
	if err := store.releaseClaim(context.Background(), job.ID, "worker_0000000001", nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var status, stage, owner string
	var expires sql.NullTime
	_ = store.db.QueryRow(`SELECT status,stage,lease_owner,lease_expires_at FROM site_migration_sites WHERE id=?`, job.ID).Scan(&status, &stage, &owner, &expires)
	if status != "awaiting_cutover" || stage != "awaiting_cutover" || owner != "" || expires.Valid {
		t.Fatalf("status=%q stage=%q owner=%q expires=%v", status, stage, owner, expires.Valid)
	}
}

func TestSiteMigrationStoreRecoversExpiredTargetAwaitingAutomaticActivation(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET direction='target',status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	job, err := store.claimNext(ctx, "worker_0000000001", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='awaiting_cutover' WHERE id=? AND lease_owner=?`, job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.recoverExpired(ctx, now.Add(2*time.Minute)); err != nil || changed != 1 {
		t.Fatalf("recover changed=%d err=%v", changed, err)
	}
	recovered, err := getSiteMigrationSite(ctx, store.db, job.ID)
	if err != nil || recovered.Status != "queued" || recovered.Stage != "awaiting_cutover" || recovered.LeaseOwner != "" || recovered.LeaseExpiresAt.Valid {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	reclaimed, err := store.claimNext(ctx, "worker_0000000002", now.Add(2*time.Minute), time.Minute)
	if err != nil || reclaimed == nil || reclaimed.ID != job.ID || reclaimed.Stage != "awaiting_cutover" || reclaimed.Status != "running" {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
}

func TestSiteMigrationStoreDoesNotRequeueSourceAwaitingUserDecision(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='transferring_database',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.recoverExpired(context.Background(), now); err != nil || changed != 0 {
		t.Fatalf("source recovery changed=%d err=%v", changed, err)
	}
	recovered, err := getSiteMigrationSite(context.Background(), store.db, "migration_0000001")
	if err != nil || recovered.Status != "awaiting_cutover" || recovered.Stage != "transferring_database" {
		t.Fatalf("source task changed=%+v err=%v", recovered, err)
	}
}

func TestSiteMigrationStoreReleaseMarksInterruptedTargetActivationRetryable(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET direction='target',status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	job, err := store.claimNext(context.Background(), "worker_0000000001", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='awaiting_cutover' WHERE id=? AND lease_owner=?`, job.ID, job.LeaseOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.releaseClaim(context.Background(), job.ID, job.LeaseOwner, errors.New("stopped before activation"), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var status, stage, code, owner string
	var expires sql.NullTime
	if err := store.db.QueryRow(`SELECT status,stage,error_code,lease_owner,lease_expires_at FROM site_migration_sites WHERE id=?`, job.ID).Scan(&status, &stage, &code, &owner, &expires); err != nil {
		t.Fatal(err)
	}
	if status != "failed_retryable" || stage != "awaiting_cutover" || code != "worker_stage_failed" || owner != "" || expires.Valid {
		t.Fatalf("status=%q stage=%q code=%q owner=%q expires=%v", status, stage, code, owner, expires.Valid)
	}
}
