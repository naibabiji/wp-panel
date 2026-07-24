package executor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWPUpdateRecentDatabaseBackupAndReuseChoice(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	root := t.TempDir()
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now.Add(-time.Hour))
	taskDir := filepath.Join(root, task.ID)
	if err := os.Mkdir(taskDir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(taskDir, "database.sql.gz")
	if err := os.WriteFile(dbPath, []byte("database backup"), 0600); err != nil {
		t.Fatal(err)
	}
	sha, size, err := hashRegularFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.db.Exec(`INSERT INTO wp_update_task_backups
		(task_id,kind,file_path,file_size,sha256,protected,created_at)
		VALUES (?,'database',?,?,?,1,?)`, task.ID, dbPath, size, sha, wpUpdateDBTime(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	backupID, _ := result.LastInsertId()
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',backup_ready=1,
		database_backup_source_id=?,finished_at=? WHERE id=?`, backupID, wpUpdateDBTime(now.Add(-time.Hour)), task.ID); err != nil {
		t.Fatal(err)
	}
	recent, err := store.recentDatabaseBackup(context.Background(), siteID, now)
	if err != nil || recent.BackupID != backupID {
		t.Fatalf("recent=%+v err=%v", recent, err)
	}
	if err := store.validateDatabaseBackupChoice(context.Background(), siteID, "reuse", backupID, root, now); err != nil {
		t.Fatalf("reuse rejected: %v", err)
	}
	if err := store.validateDatabaseBackupChoice(context.Background(), siteID, "reuse", backupID, root, now.Add(7*time.Hour)); err == nil {
		t.Fatal("expired database backup was accepted")
	}
}

func TestWPUpdateArtifactCleanupDeletesResolvedAfterOneDay(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	root := t.TempDir()
	service, err := newWPUpdateArtifactService(store, root, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now.Add(-26*time.Hour))
	taskDir := filepath.Join(root, task.ID)
	if err := os.Mkdir(taskDir, 0700); err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(taskDir, "package.zip")
	backupPath := filepath.Join(taskDir, "database.sql.gz")
	for _, name := range []string{packagePath, backupPath} {
		if err := os.WriteFile(name, []byte("artifact"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sha := strings.Repeat("a", 64)
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',backup_ready=1,
		finished_at=? WHERE id=?`, wpUpdateDBTime(now.Add(-25*time.Hour)), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO wp_update_task_backups
		(task_id,kind,file_path,file_size,sha256,protected,created_at)
		VALUES (?,'database',?,8,?,1,?)`, task.ID, backupPath, sha, wpUpdateDBTime(now.Add(-26*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := service.cleanupExpiredArtifacts(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Fatalf("expired task directory still exists: %v", err)
	}
	var protected int
	if err := store.db.QueryRow(`SELECT protected FROM wp_update_task_backups WHERE task_id=?`, task.ID).Scan(&protected); err != nil || protected != 0 {
		t.Fatalf("protected=%d err=%v", protected, err)
	}
}

func TestWPPluginUpdateReuseSkipsDatabaseDumpButKeepsPluginBackup(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	root := t.TempDir()
	now := time.Now().UTC()
	sourceTask := createAndSealUpdateTask(t, store, siteID, now.Add(-time.Hour))
	sourceDir := filepath.Join(root, sourceTask.ID)
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(sourceDir, "database.sql.gz")
	if err := os.WriteFile(dbPath, []byte("database backup"), 0600); err != nil {
		t.Fatal(err)
	}
	dbSHA, dbSize, _ := hashRegularFile(dbPath)
	result, err := store.db.Exec(`INSERT INTO wp_update_task_backups
		(task_id,kind,file_path,file_size,sha256,protected,created_at)
		VALUES (?,'database',?,?,?,1,?)`, sourceTask.ID, dbPath, dbSize, dbSHA, wpUpdateDBTime(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	backupID, _ := result.LastInsertId()
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',backup_ready=1,
		database_backup_source_id=?,finished_at=? WHERE id=?`, backupID, wpUpdateDBTime(now.Add(-time.Hour)), sourceTask.ID); err != nil {
		t.Fatal(err)
	}

	seedPluginUpdateCandidate(t, store, siteID, "sample/sample.php", "1.0.0", "1.1.0", "collection-reuse")
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	pluginDir := filepath.Join(webRoot, "wp-content", "plugins", "sample")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "sample.php"), []byte("plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=?,db_name='wordpress_db' WHERE id=?`, webRoot, siteID); err != nil {
		t.Fatal(err)
	}
	task, err := store.createPluginManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/plugin/sample.1.1.0.zip",
		DatabaseBackupMode: "reuse", DatabaseBackupSourceID: backupID,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	dumpCalls := 0
	service, err := newWPUpdateArtifactService(store, root, func(context.Context, string, string) error {
		dumpCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, _ := hashRegularFile(source)
	task, _, err = service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	task, err = service.validateAndClaimPluginUpdate(context.Background(), task.ID, "worker-reuse", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.preparePluginBackups(context.Background(), task.ID, "worker-reuse"); err != nil {
		t.Fatal(err)
	}
	if dumpCalls != 0 {
		t.Fatalf("database dump calls=%d, want 0", dumpCalls)
	}
	var databaseRows, pluginRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=? AND kind='database'`, task.ID).Scan(&databaseRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=? AND kind='plugin_files'`, task.ID).Scan(&pluginRows); err != nil {
		t.Fatal(err)
	}
	if databaseRows != 0 || pluginRows != 1 {
		t.Fatalf("database rows=%d plugin rows=%d", databaseRows, pluginRows)
	}
}

func insertWPUpdateLogEventForTest(t *testing.T, db *sql.DB, taskID, stage, result string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO wp_update_task_events(task_id,stage,result,error_code,summary,created_at)
		VALUES(?,?,?,'','',?)`, taskID, stage, result, wpUpdateDBTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
}

func countWPUpdateLogEvents(t *testing.T, db *sql.DB, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events WHERE task_id=?`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCleanupExpiredUpdateLogsDeletesOldEvents 验证严格 24h 清理：已结束且 finished_at
// 超过保留窗口的任务，其事件日志一律删除（不区分成功/失败/需人工介入）。
func TestCleanupExpiredUpdateLogsDeletesOldEvents(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	service, err := newWPUpdateArtifactService(store, t.TempDir(), func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := createAndSealUpdateTask(t, store, siteID, now.Add(-26*time.Hour))
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='failed',finished_at=? WHERE id=?`, wpUpdateDBTime(now.Add(-26*time.Hour)), task.ID); err != nil {
		t.Fatal(err)
	}
	insertWPUpdateLogEventForTest(t, store.db, task.ID, "rollback", "failed")
	insertWPUpdateLogEventForTest(t, store.db, task.ID, "manual_disposition", "manual")
	before := countWPUpdateLogEvents(t, store.db, task.ID)
	if before < 2 {
		t.Fatalf("seed events = %d want at least 2", before)
	}

	if err := service.cleanupExpiredUpdateLogs(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := countWPUpdateLogEvents(t, store.db, task.ID); got != 0 {
		t.Fatalf("events after cleanup = %d want 0 (strict 24h deletion)", got)
	}
}

// TestCleanupExpiredUpdateLogsKeepsRecentAndUnfinished 验证近 24h 内结束的任务与
// 尚未结束（finished_at IS NULL）的任务事件都保留，不被误删。
func TestCleanupExpiredUpdateLogsKeepsRecentAndUnfinished(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	service, err := newWPUpdateArtifactService(store, t.TempDir(), func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	recentTask := createAndSealUpdateTask(t, store, siteID, now.Add(-2*time.Hour))
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET status='success',finished_at=? WHERE id=?`, wpUpdateDBTime(now.Add(-1*time.Hour)), recentTask.ID); err != nil {
		t.Fatal(err)
	}
	insertWPUpdateLogEventForTest(t, store.db, recentTask.ID, "complete", "success")

	// recentTask 已置为 success（非 active），同一站点可再创建一个 active 任务。
	runningTask := createAndSealUpdateTask(t, store, siteID, now.Add(-time.Minute))
	// runningTask 保持 finished_at IS NULL（未结束）。
	insertWPUpdateLogEventForTest(t, store.db, runningTask.ID, "claimed", "info")

	recentBefore := countWPUpdateLogEvents(t, store.db, recentTask.ID)
	runningBefore := countWPUpdateLogEvents(t, store.db, runningTask.ID)
	if recentBefore == 0 || runningBefore == 0 {
		t.Fatalf("seed events recent=%d running=%d", recentBefore, runningBefore)
	}

	if err := service.cleanupExpiredUpdateLogs(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := countWPUpdateLogEvents(t, store.db, recentTask.ID); got != recentBefore {
		t.Fatalf("recent task events = %d want %d (kept)", got, recentBefore)
	}
	if got := countWPUpdateLogEvents(t, store.db, runningTask.ID); got != runningBefore {
		t.Fatalf("running task events = %d want %d (kept)", got, runningBefore)
	}
}
