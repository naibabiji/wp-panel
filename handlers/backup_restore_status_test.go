package handlers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

func TestUploadRestoreKeepsCompatibleResponseShape(t *testing.T) {
	setupBackupStatusTestDB(t)
	executor.InitQueue(nil)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "backup.sql")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("CREATE TABLE `app_settings` (`id` int);\n")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BackupHandler{}
	router.POST("/api/websites/:id/backups/upload-restore", handler.UploadRestore)

	req := httptest.NewRequest(http.MethodPost, "/api/websites/1/backups/upload-restore", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp models.ApiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("data type = %T, want map", resp.Data)
	}
	if data["message"] == "" {
		t.Fatalf("response missing data.message: %#v", data)
	}
	if data["task_id"] == "" {
		t.Fatalf("response missing data.task_id: %#v", data)
	}
}

func TestRestoreStatusRejectsInvalidTaskID(t *testing.T) {
	setupBackupStatusTestDB(t)
	executor.InitQueue(nil)

	rec := performBackupStatusRequest("/api/websites/1/backups/restore-tasks/missing")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRestoreStatusRejectsMismatchedTaskType(t *testing.T) {
	setupBackupStatusTestDB(t)
	executor.InitQueue(nil)
	task := executor.GlobalQueue.Enqueue(executor.TaskCreateBackup, &executor.CreateBackupPayload{
		Site: &models.Website{ID: 1, Domain: "example.com"},
	})

	rec := performBackupStatusRequest("/api/websites/1/backups/restore-tasks/" + task.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestRestoreStatusRejectsMismatchedSiteID(t *testing.T) {
	setupBackupStatusTestDB(t)
	executor.InitQueue(nil)
	task := executor.GlobalQueue.Enqueue(executor.TaskRestoreBackup, &executor.RestoreBackupPayload{
		Site: &models.Website{ID: 2, Domain: "other.com"},
	})

	rec := performBackupStatusRequest("/api/websites/1/backups/restore-tasks/" + task.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func setupBackupStatusTestDB(t *testing.T) {
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

func performBackupStatusRequest(path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BackupHandler{}
	router.GET("/api/websites/:id/backups/restore-tasks/:task_id", handler.RestoreStatus)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
