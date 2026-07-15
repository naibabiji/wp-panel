package executor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

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
