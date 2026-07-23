package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

func TestWPUpdateBackupListIsSiteScopedAndPathFree(t *testing.T) {
	setupWPUpdateBackupTest(t)
	insertWPUpdateBackupTestTask(t, 1, "wpu_0123456789abcdef0123456789abcdef", filepath.Join(t.TempDir(), "database.sql.gz"))
	insertWPUpdateBackupTestTask(t, 2, "wpu_1123456789abcdef0123456789abcdef", filepath.Join(t.TempDir(), "other.sql.gz"))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/websites/:id/wp-update-backups", (&WPUpdateBackupHandler{BackupDir: t.TempDir()}).List)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/websites/1/wp-update-backups", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "file_path") || strings.Contains(rec.Body.String(), "sha256") ||
		strings.Contains(rec.Body.String(), "other.sql.gz") {
		t.Fatalf("response exposes internal backup data: %s", rec.Body.String())
	}
	var response models.ApiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	items, ok := response.Data.([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("items=%#v", response.Data)
	}
}

func TestWPUpdateBackupRestoreRejectsUnavailableFile(t *testing.T) {
	setupWPUpdateBackupTest(t)
	root := t.TempDir()
	insertWPUpdateBackupTestTask(t, 1, "wpu_2123456789abcdef0123456789abcdef", filepath.Join(root, "missing.sql.gz"))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/websites/:id/wp-update-backups/:backup_id/restore", (&WPUpdateBackupHandler{BackupDir: root}).Restore)
	req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-update-backups/1/restore", strings.NewReader(`{"confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWPUpdateBackupRestoreRejectsConcurrentSiteOperation(t *testing.T) {
	setupWPUpdateBackupTest(t)
	executor.InitQueue(nil)
	root := t.TempDir()
	insertRealWPUpdateBackupTestFile(t, root, 1, "wpu_3123456789abcdef0123456789abcdef")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/websites/:id/wp-update-backups/:backup_id/restore", (&WPUpdateBackupHandler{BackupDir: root}).Restore)

	// Simulate a WP core/plugin/theme update already holding the site's op lock.
	if !executor.TryAcquireSiteOpLock(1, "wp_core_update") {
		t.Fatal("test setup: expected to acquire site lock")
	}
	t.Cleanup(func() { executor.ReleaseSiteOpLock(1) })

	req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-update-backups/1/restore", strings.NewReader(`{"confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	executor.ReleaseSiteOpLock(1)
	req = httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-update-backups/1/restore", strings.NewReader(`{"confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func setupWPUpdateBackupTest(t *testing.T) {
	t.Helper()
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		database.DB = oldDB
	})
	for id, domain := range map[int]string{1: "one.example", 2: "two.example"} {
		_, err := database.GetDB().Exec(`INSERT INTO websites
			(id,name,domain,aliases,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,site_type)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, domain, domain, "", "active", "wp_test", "/www/wwwroot/"+domain, "/www/wwwlogs/"+domain,
			"db_test", "user_test", "/etc/php/8.3/fpm/pool.d/test.conf", "/etc/nginx/sites-available/test.conf", "wordpress")
		if err != nil {
			t.Fatal(err)
		}
	}
}

func insertWPUpdateBackupTestTask(t *testing.T, siteID int, taskID, path string) {
	t.Helper()
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := database.GetDB().Exec(`INSERT INTO wp_update_tasks
		(id,site_id,component_type,component_key,task_kind,trigger_type,status,stage,current_version,target_version,
		 package_source,download_url,downloaded_sha256,verification_level,package_snapshot_path,backup_ready,
		 database_backup_mode,plan_sealed_at,requested_at,finished_at,created_at,updated_at)
		VALUES(?,?,'plugin','sample/sample.php','update','manual','success','complete','1.0.0','1.1.0',
		 'wordpress.org','https://downloads.wordpress.org/plugin/sample.1.1.0.zip',?,'structure_only','/tmp/package.zip',1,
		 'fresh',?,?,?,?,?)`,
		taskID, siteID, strings.Repeat("a", 64), now, now, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetDB().Exec(`INSERT INTO wp_update_task_backups
		(task_id,kind,file_path,file_size,sha256,protected,created_at) VALUES(?,'database',?,123,?,1,?)`,
		taskID, path, strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatal(err)
	}
}

// insertRealWPUpdateBackupTestFile writes an actual file under root/wp-updates
// and records it as a restorable database backup for siteID, so a Restore
// request can pass file-existence and SHA-256 validation end to end.
func insertRealWPUpdateBackupTestFile(t *testing.T, root string, siteID int, taskID string) {
	t.Helper()
	dir := filepath.Join(root, "wp-updates")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "database.sql.gz")
	content := []byte("fake backup content")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := database.GetDB().Exec(`INSERT INTO wp_update_tasks
		(id,site_id,component_type,component_key,task_kind,trigger_type,status,stage,current_version,target_version,
		 package_source,download_url,downloaded_sha256,verification_level,package_snapshot_path,backup_ready,
		 database_backup_mode,plan_sealed_at,requested_at,finished_at,created_at,updated_at)
		VALUES(?,?,'plugin','sample/sample.php','update','manual','success','complete','1.0.0','1.1.0',
		 'wordpress.org','https://downloads.wordpress.org/plugin/sample.1.1.0.zip',?,'structure_only','/tmp/package.zip',1,
		 'fresh',?,?,?,?,?)`,
		taskID, siteID, strings.Repeat("a", 64), now, now, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetDB().Exec(`INSERT INTO wp_update_task_backups
		(task_id,kind,file_path,file_size,sha256,protected,created_at) VALUES(?,'database',?,?,?,1,?)`,
		taskID, path, len(content), sha, now)
	if err != nil {
		t.Fatal(err)
	}
}
