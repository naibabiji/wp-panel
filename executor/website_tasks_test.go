package executor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestCreateWebsiteInsertOverridesLegacyLogRetentionDefault(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`CREATE TABLE websites (
		name TEXT, domain TEXT, aliases TEXT, status TEXT, system_user TEXT, web_root TEXT,
		document_root_subdir TEXT, log_dir TEXT, db_name TEXT, db_user TEXT, php_pool_path TEXT,
		nginx_conf_path TEXT, site_type TEXT, ssl_enabled INTEGER, ssl_cert_path TEXT,
		ssl_key_path TEXT, ssl_expires_at DATETIME, ssl_last_error TEXT, template_version TEXT,
		access_log_mode TEXT, disable_application_passwords INTEGER,
		log_retention_days INTEGER NOT NULL DEFAULT 7, php_fpm_max_children INTEGER, expires_at DATETIME
	)`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(createWebsiteInsertSQL,
		"legacy-default", "legacy-default.example.com", "", "wp_legacy", "/www/legacy", "", "/logs/legacy",
		"db_legacy", "user_legacy", "/php/legacy.conf", "/nginx/legacy.conf", "wordpress", 0,
		"", "", nil, "", defaultSiteLogRetentionDays, 5, nil,
	)
	if err != nil {
		t.Fatalf("execute create website insert: %v", err)
	}

	var retentionDays, applicationPasswordsDisabled int
	if err := db.QueryRow(`SELECT log_retention_days, disable_application_passwords FROM websites`).Scan(&retentionDays, &applicationPasswordsDisabled); err != nil {
		t.Fatal(err)
	}
	if retentionDays != defaultSiteLogRetentionDays {
		t.Fatalf("log_retention_days = %d, want %d even when schema default is 7", retentionDays, defaultSiteLogRetentionDays)
	}
	if applicationPasswordsDisabled != 1 {
		t.Fatalf("disable_application_passwords = %d, want 1", applicationPasswordsDisabled)
	}
}

func TestDeleteSiteAndAssociatedCronJobsDeletesOnlyMatchingSite(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	insertMinimalWebsite(t, "site-a.example.com")
	mustExec(t, db, `INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (2, 'site-b', 'site-b.example.com', 'u2', '/www/wwwroot/site-b.example.com', '/www/wwwlogs/site-b.example.com', 'db2', 'u2', '/p2', '/n2')`)
	mustExec(t, db, `INSERT INTO cron_jobs (id, name, cron_expression, command, task_type, backup_mode, site_id, enabled)
		VALUES (1, 'site-a backup', '0 2 * * *', 'wp-panel file backup', 'file_backup', 'incremental', 1, 1)`)
	mustExec(t, db, `INSERT INTO cron_jobs (id, name, cron_expression, command, task_type, backup_mode, site_id, enabled)
		VALUES (2, 'site-b backup', '0 2 * * *', 'wp-panel file backup', 'file_backup', 'incremental', 2, 1)`)
	mustExec(t, db, `INSERT INTO cron_jobs (id, name, cron_expression, command, task_type, site_id, enabled)
		VALUES (4, 'site-a wp cron', '*/5 * * * *', 'site-a.example.com', 'wp_cron', 1, 1)`)
	mustExec(t, db, `INSERT INTO cron_jobs (id, name, cron_expression, command, task_type, site_id, enabled)
		VALUES (3, 'unrelated command', '0 3 * * *', 'echo hi', 'command', NULL, 1)`)

	deleted, err := deleteSiteAndAssociatedCronJobs(db, 1)
	if err != nil {
		t.Fatalf("deleteSiteAndAssociatedCronJobs error = %v", err)
	}
	if !deleted {
		t.Fatal("deleteSiteAndAssociatedCronJobs = false, want true when matching jobs were deleted")
	}

	var siteCount, countA, countWP, countB, countOther int
	if err := db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id = 1`).Scan(&siteCount); err != nil {
		t.Fatalf("query site 1 count: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE id = 1`).Scan(&countA); err != nil {
		t.Fatalf("query job 1 count: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE id = 4`).Scan(&countWP); err != nil {
		t.Fatalf("query job 4 count: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE id = 2`).Scan(&countB); err != nil {
		t.Fatalf("query job 2 count: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE id = 3`).Scan(&countOther); err != nil {
		t.Fatalf("query job 3 count: %v", err)
	}
	if siteCount != 0 {
		t.Fatal("site-a website row should be deleted")
	}
	if countA != 0 || countWP != 0 {
		t.Fatalf("site-a associated cron jobs should be deleted, file_backup=%d wp_cron=%d", countA, countWP)
	}
	if countB != 1 {
		t.Fatal("site-b's file_backup cron job should NOT be affected by site-a's deletion")
	}
	if countOther != 1 {
		t.Fatal("unrelated non-file_backup cron job should NOT be deleted")
	}

	// 幂等：再次调用不应报错，且因为已经没有匹配任务，应返回 false。
	deletedAgain, err := deleteSiteAndAssociatedCronJobs(db, 1)
	if err != nil {
		t.Fatalf("second deleteSiteAndAssociatedCronJobs error = %v", err)
	}
	if deletedAgain {
		t.Fatal("deleteSiteAndAssociatedCronJobs = true on second call, want false (nothing left to delete)")
	}
}

func TestEnableSiteRestoresMigratedMaintenanceLink(t *testing.T) {
	openTestDB(t)
	installStubNginx(t)
	db := database.GetDB()
	root := t.TempDir()
	cfg := &config.Config{Paths: config.PathsConfig{
		NginxSitesAvailable: filepath.Join(root, "available"),
		NginxSitesEnabled:   filepath.Join(root, "enabled"),
	}}
	oldCfg := config.AppConfig
	config.AppConfig = cfg
	t.Cleanup(func() { config.AppConfig = oldCfg })
	if err := os.MkdirAll(cfg.Paths.NginxSitesAvailable, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Paths.NginxSitesEnabled, 0755); err != nil {
		t.Fatal(err)
	}
	domain := "migrated.example.com"
	nginxConf := filepath.Join(cfg.Paths.NginxSitesAvailable, domain+".conf")
	maintenance := filepath.Join(cfg.Paths.NginxSitesAvailable, ".wp-panel-migration-migration_0000001.conf")
	enabled := filepath.Join(cfg.Paths.NginxSitesEnabled, domain+".conf")
	if err := os.WriteFile(nginxConf, []byte("server {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(maintenance, []byte("return 503;"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(maintenance, enabled); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO websites (id,name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES (31,'migrated','migrated.example.com','migrated','wp_migrated','/www/migrated','/logs/migrated','db','user','/php/migrated',?)`, nginxConf); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{ID: 31, Domain: domain, Status: models.StatusMigrated, NginxConfPath: nginxConf}
	result := executeEnableSite(&Task{Payload: &EnableSitePayload{Site: site}})
	if !result.Success {
		t.Fatalf("restore migrated site failed: %+v", result)
	}
	target, err := os.Readlink(enabled)
	if err != nil || filepath.Clean(target) != filepath.Clean(nginxConf) {
		t.Fatalf("enabled target=%q err=%v", target, err)
	}
	if _, err := os.Stat(maintenance); !os.IsNotExist(err) {
		t.Fatalf("maintenance file remains: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM websites WHERE id=31`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestDeleteSiteWithEnabledFileBackupCronDoesNotDeadlockQueue(t *testing.T) {
	openTestDB(t)
	db := database.GetDB()
	root := t.TempDir()
	cfg := &config.Config{
		Panel: config.PanelConfig{BackupDir: filepath.Join(root, "backups")},
		MariaDB: config.MariaDBConfig{
			RootUser:     "root",
			RootPassword: "test",
		},
		Paths: config.PathsConfig{
			WWWRoot:             filepath.Join(root, "wwwroot"),
			WWWLogs:             filepath.Join(root, "wwwlogs"),
			NginxSitesAvailable: filepath.Join(root, "nginx-available"),
			NginxSitesEnabled:   filepath.Join(root, "nginx-enabled"),
			PHPFPMPool:          filepath.Join(root, "php-pool"),
			PHPFPMSock:          filepath.Join(root, "php-sock"),
			Certificates:        filepath.Join(root, "certs"),
			CronFile:            filepath.Join(root, "wp-panel-cron"),
		},
	}
	oldCfg := config.AppConfig
	oldQueue := GlobalQueue
	config.AppConfig = cfg
	t.Cleanup(func() {
		config.AppConfig = oldCfg
		GlobalQueue = oldQueue
	})
	for _, dir := range []string{
		cfg.Paths.WWWRoot,
		cfg.Paths.WWWLogs,
		cfg.Paths.NginxSitesAvailable,
		cfg.Paths.NginxSitesEnabled,
		cfg.Paths.PHPFPMPool,
		cfg.Paths.PHPFPMSock,
		cfg.Paths.Certificates,
		filepath.Dir(cfg.Paths.CronFile),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	domain := "delete-cron.example.com"
	site := &models.Website{
		ID:            10,
		Domain:        domain,
		SystemUser:    "php_delete_cron",
		WebRoot:       filepath.Join(cfg.Paths.WWWRoot, domain),
		LogDir:        filepath.Join(cfg.Paths.WWWLogs, domain),
		DBName:        "db_delete_cron",
		DBUser:        "user_delete_cron",
		PHPPoolPath:   filepath.Join(cfg.Paths.PHPFPMPool, "delete-cron.conf"),
		NginxConfPath: filepath.Join(cfg.Paths.NginxSitesAvailable, "delete-cron.conf"),
		SiteType:      "php",
	}
	mustExec(t, db, `INSERT INTO websites (id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (10, 'delete-cron', 'delete-cron.example.com', 'active', 'php_delete_cron', '`+site.WebRoot+`', '`+site.LogDir+`', 'db_delete_cron', 'user_delete_cron', '`+site.PHPPoolPath+`', '`+site.NginxConfPath+`', 'php')`)
	mustExec(t, db, `INSERT INTO cron_jobs (name, cron_expression, command, task_type, backup_mode, site_id, enabled)
		VALUES ('delete-cron file backup', '0 2 * * *', 'wp-panel file backup', 'file_backup', 'incremental', 10, 1)`)
	mustExec(t, db, `INSERT INTO cron_jobs (name, cron_expression, command, task_type, site_id, enabled)
		VALUES ('delete-cron wp cron', '*/5 * * * *', 'delete-cron.example.com', 'wp_cron', 10, 1)`)

	queue := InitQueue(cfg)
	task := queue.Enqueue(TaskDeleteSite, &DeleteSitePayload{Site: site})
	select {
	case result := <-task.ResultCh:
		if !result.Success {
			t.Fatalf("delete result = %#v, want success", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delete site task timed out; likely queue self-deadlocked while refreshing cron")
	}

	var siteCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id = 10`).Scan(&siteCount); err != nil {
		t.Fatalf("query websites: %v", err)
	}
	if siteCount != 0 {
		t.Fatal("website row still exists after delete")
	}
	var cronCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE site_id = 10`).Scan(&cronCount); err != nil {
		t.Fatalf("query cron job count: %v", err)
	}
	if cronCount != 0 {
		t.Fatalf("associated cron job count = %d, want deleted", cronCount)
	}
}

func TestDeleteSiteRejectsActiveMigrationBeforeExternalCleanup(t *testing.T) {
	store, siteID := newSiteMigrationStoreTest(t)
	if err := store.acquireLock(context.Background(), "migration_0000001", &siteID, "example.com", "source", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	webRoot := t.TempDir()
	marker := filepath.Join(webRoot, "must-remain.txt")
	if err := os.WriteFile(marker, []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	result := executeDeleteSite(&Task{Payload: &DeleteSitePayload{Site: &models.Website{
		ID: siteID, Domain: "example.com", WebRoot: webRoot,
	}}})
	if result.Success || !strings.Contains(result.Message, "不能从网站列表直接删除") {
		t.Fatalf("result=%#v, want explicit migration-lock rejection", result)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "protected" {
		t.Fatalf("protected file changed: content=%q err=%v", got, err)
	}
	var count int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("website row count=%d err=%v, want 1", count, err)
	}
}

func TestDeleteSiteAllowsCompletedSourceAndClearsMigrationReferences(t *testing.T) {
	store, siteID := newSiteMigrationStoreTest(t)
	if err := store.acquireLock(context.Background(), "migration_0000001", &siteID, "example.com", "source", freezerTestTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET status='completed',stage='completed' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	blocked, err := SiteMigrationDeleteBlocked(context.Background(), siteID, "example.com")
	if err != nil || blocked {
		t.Fatalf("completed source delete blocked=%v err=%v", blocked, err)
	}
	if _, err := deleteSiteAndAssociatedCronJobs(store.db, siteID); err != nil {
		t.Fatal(err)
	}
	var websites int
	var lockStatus string
	var lockSite, sourceSite sql.NullInt64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE id=?`, siteID).Scan(&websites); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status,site_id FROM site_migration_locks WHERE migration_site_id='migration_0000001' AND direction='source'`).Scan(&lockStatus, &lockSite); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT source_site_id FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&sourceSite); err != nil {
		t.Fatal(err)
	}
	if websites != 0 || lockStatus != "released" || lockSite.Valid || sourceSite.Valid {
		t.Fatalf("websites=%d lock=%q lockSite=%v sourceSite=%v", websites, lockStatus, lockSite, sourceSite)
	}
}

func TestMoveSiteLogDirRemovesEmptyTargetCreatedByPoolApply(t *testing.T) {
	root := t.TempDir()
	oldLogDir := filepath.Join(root, "old.example.com")
	newLogDir := filepath.Join(root, "new.example.com")

	if err := os.MkdirAll(oldLogDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldLogDir, "access.log"), []byte("old log"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newLogDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := moveSiteLogDir(oldLogDir, newLogDir); err != nil {
		t.Fatalf("moveSiteLogDir failed: %v", err)
	}

	if _, err := os.Stat(oldLogDir); !os.IsNotExist(err) {
		t.Fatalf("old log dir still exists or stat failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(newLogDir, "access.log"))
	if err != nil {
		t.Fatalf("new log file missing: %v", err)
	}
	if string(got) != "old log" {
		t.Fatalf("new log content = %q, want old log", string(got))
	}
}

func TestMoveSiteLogDirRejectsNonEmptyTarget(t *testing.T) {
	root := t.TempDir()
	oldLogDir := filepath.Join(root, "old.example.com")
	newLogDir := filepath.Join(root, "new.example.com")

	if err := os.MkdirAll(oldLogDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldLogDir, "access.log"), []byte("old log"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newLogDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newLogDir, "access.log"), []byte("new log"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := moveSiteLogDir(oldLogDir, newLogDir); err == nil {
		t.Fatal("expected non-empty target log dir to be rejected")
	}
	if _, err := os.Stat(filepath.Join(oldLogDir, "access.log")); err != nil {
		t.Fatalf("old log file should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newLogDir, "access.log")); err != nil {
		t.Fatalf("target log file should remain: %v", err)
	}
}

func TestCreateSiteLogDirCreatesMissingLogs(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "88.vps17.top")

	if err := createSiteLogDir(logDir); err != nil {
		t.Fatalf("createSiteLogDir failed: %v", err)
	}
	for _, name := range []string{"access.log", "error.log", "wp-security.log", "php-error.log", "php-slow.log"} {
		if _, err := os.Stat(filepath.Join(logDir, name)); err != nil {
			t.Fatalf("%s should exist: %v", name, err)
		}
	}
}

func TestCreateSiteLogDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "88.vps17.top")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}

	if err := createSiteLogDir(link); err == nil {
		t.Fatal("expected symlink log dir to be rejected")
	}
}

func TestManagedSubpathAllowsOnlyChildren(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")
	target := filepath.Join(root, "example.com")

	got, err := managedSubpath(root, target, "网站目录")
	if err != nil {
		t.Fatalf("managedSubpath rejected child path: %v", err)
	}
	if got != filepath.Clean(target) {
		t.Fatalf("managedSubpath() = %q, want %q", got, filepath.Clean(target))
	}
}

func TestManagedSubpathRejectsRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")

	if _, err := managedSubpath(root, root, "网站目录"); err == nil {
		t.Fatal("expected root path to be rejected")
	}
}

func TestManagedSubpathRejectsEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sites")
	target := filepath.Join(root, "..", "outside")

	if _, err := managedSubpath(root, target, "网站目录"); err == nil {
		t.Fatal("expected escaped path to be rejected")
	}
}
