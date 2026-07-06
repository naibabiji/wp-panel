package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func setupBackupUsageTestDB(t *testing.T) {
	t.Helper()
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
		database.DB = oldDB
	})

	_, err := database.GetDB().Exec(`
		INSERT INTO websites (
			id, name, domain, aliases, status, system_user, web_root, log_dir,
			db_name, db_user, php_pool_path, nginx_conf_path, site_type
		) VALUES (
			1, 'example', 'example.com', '', 'active', 'wp_example', '/www/wwwroot/example.com', '/www/wwwlogs/example.com',
			'db_example', 'user_example', '/etc/php/8.3/fpm/pool.d/example.conf', '/etc/nginx/sites-available/example.conf',
			'wordpress'
		)
	`)
	if err != nil {
		t.Fatalf("insert website: %v", err)
	}
}

type backupUsageResponse struct {
	Success bool                      `json:"success"`
	Data    models.WebsiteBackupUsage `json:"data"`
}

func TestBackupUsageReturnsZeroWhenNoBackupData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupUsageTestDB(t)

	router := gin.New()
	handler := &WebsiteHandler{}
	router.GET("/api/websites/:id/backup-usage", handler.BackupUsage)

	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/backup-usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp backupUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if resp.Data.DBBackupCount != 0 || resp.Data.FileBackupCount != 0 || resp.Data.AutoBackupEnabled || len(resp.Data.CronJobs) != 0 {
		t.Fatalf("usage = %+v, want all-zero/empty", resp.Data)
	}
	if resp.Data.HasBackupData() {
		t.Fatal("HasBackupData() = true, want false when nothing exists")
	}
}

func TestBackupUsageReportsExistingBackupData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupUsageTestDB(t)
	db := database.GetDB()

	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name) VALUES (1, 'a.sql.gz', 10, 'db_example')`); err != nil {
		t.Fatalf("insert db_backups: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name) VALUES (1, 'b.sql.gz', 10, 'db_example')`); err != nil {
		t.Fatalf("insert db_backups: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups (site_id, filename, file_size, mode) VALUES (1, 'file_full_x.tar.gz', 20, 'full')`); err != nil {
		t.Fatalf("insert file_backups: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO backup_settings (site_id, enabled, keep_count) VALUES (1, 1, 7)`); err != nil {
		t.Fatalf("insert backup_settings: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cron_jobs (name, cron_expression, command, task_type, backup_mode, site_id, enabled)
		VALUES ('nightly backup', '0 2 * * *', 'wp-panel file backup', 'file_backup', 'incremental', 1, 1)`); err != nil {
		t.Fatalf("insert cron_jobs: %v", err)
	}

	router := gin.New()
	handler := &WebsiteHandler{}
	router.GET("/api/websites/:id/backup-usage", handler.BackupUsage)

	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/backup-usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp backupUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	usage := resp.Data
	if usage.DBBackupCount != 2 {
		t.Fatalf("db_backup_count = %d, want 2", usage.DBBackupCount)
	}
	if usage.FileBackupCount != 1 {
		t.Fatalf("file_backup_count = %d, want 1", usage.FileBackupCount)
	}
	if !usage.AutoBackupEnabled {
		t.Fatal("auto_backup_enabled = false, want true")
	}
	if len(usage.CronJobs) != 1 || usage.CronJobs[0].Name != "nightly backup" {
		t.Fatalf("cron_jobs = %+v, want one entry named 'nightly backup'", usage.CronJobs)
	}
	if !usage.HasBackupData() {
		t.Fatal("HasBackupData() = false, want true")
	}
}

func TestBackupUsageListsMultipleAndDisabledCronJobs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupUsageTestDB(t)
	db := database.GetDB()

	if _, err := db.Exec(`INSERT INTO cron_jobs (name, cron_expression, command, task_type, backup_mode, site_id, enabled)
		VALUES ('nightly backup', '0 2 * * *', 'wp-panel file backup', 'file_backup', 'incremental', 1, 1)`); err != nil {
		t.Fatalf("insert cron_jobs: %v", err)
	}
	// 已经被禁用的备份计划任务（例如上一次删除流程里被自动禁用过）也应该出现在列表里，
	// 因为它仍然需要管理员自行去删除，不是"已经处理完了"。
	if _, err := db.Exec(`INSERT INTO cron_jobs (name, cron_expression, command, task_type, backup_mode, site_id, enabled)
		VALUES ('weekly full backup', '0 3 * * 0', 'wp-panel file backup', 'file_backup', 'full', 1, 0)`); err != nil {
		t.Fatalf("insert cron_jobs: %v", err)
	}

	router := gin.New()
	handler := &WebsiteHandler{}
	router.GET("/api/websites/:id/backup-usage", handler.BackupUsage)

	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/backup-usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp backupUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	names := map[string]bool{}
	for _, j := range resp.Data.CronJobs {
		names[j.Name] = true
	}
	if len(resp.Data.CronJobs) != 2 {
		t.Fatalf("cron_jobs count = %d, want 2; got %+v", len(resp.Data.CronJobs), resp.Data.CronJobs)
	}
	if !names["nightly backup"] || !names["weekly full backup"] {
		t.Fatalf("cron_jobs = %+v, want both nightly backup and weekly full backup listed regardless of enabled state", resp.Data.CronJobs)
	}
	if !resp.Data.HasBackupData() {
		t.Fatal("HasBackupData() = false, want true")
	}
}

func TestBackupUsageRejectsMissingWebsite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupUsageTestDB(t)

	router := gin.New()
	handler := &WebsiteHandler{}
	router.GET("/api/websites/:id/backup-usage", handler.BackupUsage)

	req := httptest.NewRequest(http.MethodGet, "/api/websites/999/backup-usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
