package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func installDatabaseDumpStub(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mysqldump")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'CREATE TABLE test (id INT);\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func setupDatabaseBackupControlTest(t *testing.T) *models.Website {
	t.Helper()
	openTestDB(t)
	root := t.TempDir()
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: filepath.Join(root, "data"), BackupDir: filepath.Join(root, "backups")}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id,name,domain,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES (91,'backup','backup-control.example.com','active','php','u','/tmp','/tmp','backup_db','','','')`); err != nil {
		t.Fatal(err)
	}
	oldPassword := readMariaDBPasswordFunc
	readMariaDBPasswordFunc = func() string { return "test-password" }
	t.Cleanup(func() { readMariaDBPasswordFunc = oldPassword })
	installDatabaseDumpStub(t)
	return &models.Website{ID: 91, Domain: "backup-control.example.com", Status: models.StatusActive, DBName: "backup_db"}
}

func TestCreateDatabaseBackupCoreRecordsManagedBackupWithoutLeakingPath(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	result, err := createDatabaseBackupCore(site, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordID == 0 || result.Filename == "" || result.FileSize == 0 {
		t.Fatalf("result=%+v", result)
	}
	var filename string
	if err := database.GetDB().QueryRow("SELECT filename FROM db_backups WHERE id=?", result.RecordID).Scan(&filename); err != nil {
		t.Fatal(err)
	}
	if filename != result.Filename || filepath.Base(filename) != filename {
		t.Fatalf("filename=%q result=%q", filename, result.Filename)
	}
	if _, err := os.Stat(filepath.Join(config.AppConfig.Panel.BackupDir, site.Domain, "db", filename)); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseBackupPlanDryRunExecuteAndReplay(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	plan, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil || !plan.OK || plan.Mode != "dry_run" || plan.PlanDigest == "" || plan.Scope != databaseBackupScope {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if plan.Filename != "" || plan.LatestBackupFilename != "" {
		t.Fatalf("dry-run must not claim a future filename: %+v", plan)
	}
	result, err := ExecuteDatabaseBackupPlan(context.Background(), plan.PlanDigest, "dev")
	if err != nil || !result.OK || result.Mode != "execute" || !result.Consumed || result.BackupID == 0 || result.Filename == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.NotPerformed) != 4 || result.TransportStatus == "" {
		t.Fatalf("result=%+v", result)
	}
	if _, err := ExecuteDatabaseBackupPlan(context.Background(), plan.PlanDigest, "dev"); DatabaseBackupErrorCode(err) != "plan_already_used" {
		t.Fatalf("replay err=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
}

func TestDatabaseBackupDryRunLabelsExistingFilenameSeparately(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	if _, err := database.GetDB().Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (?, ?, ?, ?, ?)`, site.ID, "existing.sql.gz", 12, site.DBName, "local"); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Filename != "" || plan.LatestBackupFilename != "existing.sql.gz" {
		t.Fatalf("dry-run filename semantics=%+v", plan)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"filename":`) {
		t.Fatalf("dry-run must not expose a future filename field: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"latest_backup_filename":"existing.sql.gz"`) {
		t.Fatalf("dry-run must expose the existing filename explicitly: %s", encoded)
	}
}

func TestDatabaseBackupPlanRejectsExpiryAndLatestRecordChange(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	plan, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	path, err := databaseBackupPlanPath(plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file databaseBackupPlanFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	file.Payload.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	expiredDigest, err := digestDatabaseBackupPlan(file.Payload)
	if err != nil {
		t.Fatal(err)
	}
	file.PlanDigest = expiredDigest
	if err := writeDatabaseBackupPlan(expiredDigest, file); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteDatabaseBackupPlan(context.Background(), expiredDigest, "dev"); DatabaseBackupErrorCode(err) != "plan_expired" {
		t.Fatalf("expired error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}

	plan, err = PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (?, ?, ?, ?, ?)`, site.ID, "newer.sql.gz", 1, site.DBName, "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteDatabaseBackupPlan(context.Background(), plan.PlanDigest, "dev"); DatabaseBackupErrorCode(err) != "plan_stale" {
		t.Fatalf("latest record error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
}

func TestDatabaseBackupPlanBecomesStaleWhenSiteStatusChanges(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	plan, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec("UPDATE websites SET status='paused' WHERE id=?", site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteDatabaseBackupPlan(context.Background(), plan.PlanDigest, "dev"); DatabaseBackupErrorCode(err) != "plan_stale" {
		t.Fatalf("status change error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
}

func TestDatabaseBackupPlanGatesAndBusyLock(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	if _, err := database.GetDB().Exec(`INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home)
		VALUES (?, 'enabled', 'wp', '/tmp', '/bin/bash', '/tmp')`, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev"); DatabaseBackupErrorCode(err) != "ai_development_active" {
		t.Fatalf("AI gate error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
	if _, err := database.GetDB().Exec("DELETE FROM website_ai_development_access WHERE site_id=?", site.ID); err != nil {
		t.Fatal(err)
	}
	if !TryAcquireCompanionDeployLock(site.ID) {
		t.Fatal("failed to hold site lock")
	}
	defer ReleaseSiteOpLock(site.ID)
	if _, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev"); DatabaseBackupErrorCode(err) != "site_lock_busy" {
		t.Fatalf("busy error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
}

func TestDatabaseBackupPlanRejectsCorruptPlanAndMigrationLock(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	plan, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	path, err := databaseBackupPlanPath(plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteDatabaseBackupPlan(context.Background(), plan.PlanDigest, "dev"); DatabaseBackupErrorCode(err) != "plan_invalid" {
		t.Fatalf("corrupt error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO site_migration_peers(id,status,outbound_credential,protocol_version) VALUES ('peer_backup_test','paired','',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_batches(id,peer_id,direction,status) VALUES ('batch_backup_test','peer_backup_test','source','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_sites(id,batch_id,source_site_id,source_domain,target_domain,site_type,status,stage)
		VALUES ('migration_backup_test','batch_backup_test',?,?,?,'wordpress','running','transferring_database')`, site.ID, site.Domain, site.Domain); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status) VALUES (?,?,?,?,?)`, site.Domain, site.ID, "migration_backup_test", "source", "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev"); DatabaseBackupErrorCode(err) != "migration_locked" {
		t.Fatalf("migration error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
}

func TestDatabaseBackupMaintenanceWindowIsAllowed(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET maintenance_security=? WHERE id=?`, `{"window":{"state":"locked"}}`, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev"); err != nil {
		t.Fatalf("maintenance window should be allowed: %v", err)
	}
}

func TestDatabaseBackupPlanConcurrentDigestHasSingleConsumer(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	plan, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	codes := []string{}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, runErr := ExecuteDatabaseBackupPlan(context.Background(), plan.PlanDigest, "dev")
			mu.Lock()
			defer mu.Unlock()
			if runErr == nil {
				successes++
			} else {
				codes = append(codes, DatabaseBackupErrorCode(runErr))
			}
		}()
	}
	wg.Wait()
	if successes != 1 || len(codes) != 1 || (codes[0] != "plan_already_used" && codes[0] != "site_lock_busy") {
		t.Fatalf("successes=%d codes=%v", successes, codes)
	}
}

func TestWPAdminBackupCoreDoesNotReacquireSiteLock(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	if !TryAcquireSiteOpLock(site.ID, "wp_administrator_update") {
		t.Fatal("failed to hold site lock")
	}
	defer ReleaseSiteOpLock(site.ID)
	result := executeCreateBackup(&Task{Payload: &CreateBackupPayload{Site: site}})
	if !result.Success {
		t.Fatalf("result=%+v", result)
	}
}

func TestDatabaseBackupPublicErrorMessageDoesNotLeakDetails(t *testing.T) {
	err := &DatabaseBackupError{Code: "backup_failed", Message: "Unknown database 'wp_secret' at /srv/private/backups/site/db/file.sql.gz"}
	message := DatabaseBackupPublicErrorMessage(err)
	if strings.Contains(message, "wp_secret") || strings.Contains(message, "/srv/private") {
		t.Fatalf("message leaked details: %q", message)
	}
}

func TestDatabaseBackupVerificationRejectsMissingFile(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	result, err := database.GetDB().Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (?, ?, ?, ?, ?)`, site.ID, "missing.sql.gz", 10, site.DBName, "local")
	if err != nil {
		t.Fatal(err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifyDatabaseBackupResult(context.Background(), site, databaseBackupCoreResult{RecordID: recordID, Filename: "missing.sql.gz", FileSize: 10})
	if DatabaseBackupErrorCode(err) != "rollback_required" {
		t.Fatalf("missing file error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
}

func TestDatabaseBackupDumpFailureIsStructuredAndSanitized(t *testing.T) {
	site := setupDatabaseBackupControlTest(t)
	dir := t.TempDir()
	dump := filepath.Join(dir, "mysqldump")
	if err := os.WriteFile(dump, []byte("#!/bin/sh\necho \"Unknown database 'secret_db' at /srv/private\" >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	plan, err := PrepareDatabaseBackupPlan(context.Background(), site.Domain, "dev")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteDatabaseBackupPlan(context.Background(), plan.PlanDigest, "dev")
	if DatabaseBackupErrorCode(err) != "backup_failed" {
		t.Fatalf("dump error=%v code=%s", err, DatabaseBackupErrorCode(err))
	}
	message := DatabaseBackupPublicErrorMessage(err)
	if strings.Contains(message, "secret_db") || strings.Contains(message, "/srv/private") {
		t.Fatalf("dump error leaked details: %q", message)
	}
}
