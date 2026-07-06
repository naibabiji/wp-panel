package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestDisableSiteBackupCronJobsDisablesOnlyMatchingSite(t *testing.T) {
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
		VALUES (3, 'unrelated command', '0 3 * * *', 'echo hi', 'command', NULL, 1)`)

	disabled, err := disableSiteBackupCronJobs(db, 1)
	if err != nil {
		t.Fatalf("disableSiteBackupCronJobs error = %v", err)
	}
	if !disabled {
		t.Fatal("disableSiteBackupCronJobs = false, want true when a matching job was disabled")
	}

	var enabledA, enabledB, enabledOther int
	if err := db.QueryRow(`SELECT enabled FROM cron_jobs WHERE id = 1`).Scan(&enabledA); err != nil {
		t.Fatalf("query job 1: %v", err)
	}
	if err := db.QueryRow(`SELECT enabled FROM cron_jobs WHERE id = 2`).Scan(&enabledB); err != nil {
		t.Fatalf("query job 2: %v", err)
	}
	if err := db.QueryRow(`SELECT enabled FROM cron_jobs WHERE id = 3`).Scan(&enabledOther); err != nil {
		t.Fatalf("query job 3: %v", err)
	}
	if enabledA != 0 {
		t.Fatal("site-a's file_backup cron job should be disabled")
	}
	if enabledB != 1 {
		t.Fatal("site-b's file_backup cron job should NOT be affected by site-a's deletion")
	}
	if enabledOther != 1 {
		t.Fatal("unrelated non-file_backup cron job should NOT be disabled")
	}

	// 幂等：再次调用不应报错，且因为已经没有 enabled=1 的匹配任务，应返回 false。
	disabledAgain, err := disableSiteBackupCronJobs(db, 1)
	if err != nil {
		t.Fatalf("second disableSiteBackupCronJobs error = %v", err)
	}
	if disabledAgain {
		t.Fatal("disableSiteBackupCronJobs = true on second call, want false (nothing left to disable)")
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
