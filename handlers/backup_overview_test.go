package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

type overviewSite struct {
	SiteID      int              `json:"site_id"`
	Domain      string           `json:"domain"`
	DBBackups   []map[string]any `json:"db_backups"`
	FileBackups []map[string]any `json:"file_backups"`
}

type overviewData struct {
	Sites          []overviewSite   `json:"sites"`
	PanelDBBackups []map[string]any `json:"panel_db_backups"`
}

type overviewResponse struct {
	Success bool         `json:"success"`
	Message string       `json:"message"`
	Data    overviewData `json:"data"`
}

func setupBackupOverviewTestDB(t *testing.T) {
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
}

func TestGetBackupOverviewReturnsSitesAndPanelBackups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (1, 'site', 'overview.example.com', 'u1', '/www/wwwroot/overview.example.com', '/www/wwwlogs/overview.example.com', 'db1', 'u1', '/p', '/n')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (1, 'db.sql.gz', 10, 'db1', 'synced')`); err != nil {
		t.Fatalf("insert db_backups: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups (site_id, filename, file_size, mode, transport_status) VALUES (1, 'file_full_x.tar.gz', 20, 'full', 'local')`); err != nil {
		t.Fatalf("insert file_backups: %v", err)
	}

	backupRoot := t.TempDir()
	panelDir := filepath.Join(backupRoot, "panel-db")
	if err := os.MkdirAll(panelDir, 0700); err != nil {
		t.Fatalf("mkdir panel-db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(panelDir, "panel_20260101_000000.db"), []byte("x"), 0600); err != nil {
		t.Fatalf("write panel backup: %v", err)
	}
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: backupRoot}}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	router := gin.New()
	router.GET("/api/backups/overview", GetBackupOverview)

	req := httptest.NewRequest(http.MethodGet, "/api/backups/overview", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if !resp.Success {
		t.Fatalf("success = false, want true; message=%s", resp.Message)
	}
	if len(resp.Data.Sites) != 1 {
		t.Fatalf("sites count = %d, want 1", len(resp.Data.Sites))
	}
	site := resp.Data.Sites[0]
	if site.Domain != "overview.example.com" {
		t.Fatalf("site domain = %q, want overview.example.com", site.Domain)
	}
	if len(site.DBBackups) != 1 || site.DBBackups[0]["filename"] != "db.sql.gz" {
		t.Fatalf("db_backups = %+v, want one entry for db.sql.gz", site.DBBackups)
	}
	if len(site.FileBackups) != 1 || site.FileBackups[0]["filename"] != "file_full_x.tar.gz" {
		t.Fatalf("file_backups = %+v, want one entry for file_full_x.tar.gz", site.FileBackups)
	}
	if len(resp.Data.PanelDBBackups) != 1 {
		t.Fatalf("panel_db_backups count = %d, want 1", len(resp.Data.PanelDBBackups))
	}
}

func TestGetBackupOverviewReportsLocalFileExistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (1, 'site', 'localcheck.example.com', 'u1', '/www/wwwroot/localcheck.example.com', '/www/wwwlogs/localcheck.example.com', 'db1', 'u1', '/p', '/n')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	// db_backups: 'present.sql.gz' 本地文件真实存在；'gone.sql.gz' 标记 synced 但本地文件已被
	// keep_local=0 清理（数据库记录仍保留，只是磁盘上没有对应文件了）。
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (1, 'present.sql.gz', 10, 'db1', 'local')`); err != nil {
		t.Fatalf("insert db_backups present: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, transport_status) VALUES (1, 'gone.sql.gz', 10, 'db1', 'synced')`); err != nil {
		t.Fatalf("insert db_backups gone: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups (site_id, filename, file_size, mode, transport_status) VALUES (1, 'file_full_present.tar.gz', 20, 'full', 'local')`); err != nil {
		t.Fatalf("insert file_backups present: %v", err)
	}

	backupRoot := t.TempDir()
	dbDir := filepath.Join(backupRoot, "localcheck.example.com", "db")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "present.sql.gz"), []byte("x"), 0600); err != nil {
		t.Fatalf("write present.sql.gz: %v", err)
	}
	// 注意：'gone.sql.gz' 故意不在磁盘上创建，模拟已被清理的情况。
	//
	// 数据库备份的本地路径读取可注入的 cfg.Panel.BackupDir（backupRoot），这里能真实覆盖
	// 本地文件存在/不存在两种情况。文件备份的本地路径固定读取 config.DefaultBackupDir
	// （生产环境和 executor/file_backup.go 的生成路径保持一致，不受这里注入的 backupRoot
	// 影响），所以此处不再构造文件备份的本地文件——那部分逻辑由
	// TestFileBackupLocalExistsFromRoot 单独覆盖，用可注入的 root 验证同样的判定逻辑。

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: backupRoot}}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	router := gin.New()
	router.GET("/api/backups/overview", GetBackupOverview)

	req := httptest.NewRequest(http.MethodGet, "/api/backups/overview", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Data.Sites) != 1 {
		t.Fatalf("sites count = %d, want 1", len(resp.Data.Sites))
	}
	dbBackups := resp.Data.Sites[0].DBBackups
	var present, gone map[string]any
	for _, b := range dbBackups {
		switch b["filename"] {
		case "present.sql.gz":
			present = b
		case "gone.sql.gz":
			gone = b
		}
	}
	if present == nil || present["local_exists"] != true {
		t.Fatalf("present.sql.gz local_exists = %+v, want true", present)
	}
	if gone == nil || gone["local_exists"] != false {
		t.Fatalf("gone.sql.gz local_exists = %+v, want false", gone)
	}

	// config.DefaultBackupDir（固定的 /www/server/panel/backups）在测试环境里不存在，
	// 所以这里 local_exists 必然是 false——这本身就是"目录不存在时不崩溃、如实报告"的有效断言。
	fileBackups := resp.Data.Sites[0].FileBackups
	if len(fileBackups) != 1 || fileBackups[0]["local_exists"] != false {
		t.Fatalf("file_backups = %+v, want one entry with local_exists=false (config.DefaultBackupDir does not exist in test env)", fileBackups)
	}
}

func TestGetBackupOverviewSurfacesQueryErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	db := database.GetDB()
	if _, err := db.Exec(`DROP TABLE db_backups`); err != nil {
		t.Fatalf("drop db_backups: %v", err)
	}

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: t.TempDir()}}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	router := gin.New()
	router.GET("/api/backups/overview", GetBackupOverview)

	req := httptest.NewRequest(http.MethodGet, "/api/backups/overview", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// db_backups 查询失败时必须显式报错，不能悄悄返回一个"成功但为空"的列表，
	// 否则会把数据库升级/schema 问题伪装成"暂无备份"。
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var resp overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if resp.Success {
		t.Fatal("success = true, want false when db_backups query fails")
	}
}

func TestFileBackupLocalExistsFromRoot(t *testing.T) {
	root := t.TempDir()
	filesDir := filepath.Join(root, "present.example.com", "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatalf("mkdir files dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "file_full_x.tar.gz"), []byte("x"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if !fileBackupLocalExistsFromRoot(root, "present.example.com", "file_full_x.tar.gz") {
		t.Fatal("fileBackupLocalExistsFromRoot() = false, want true for a file that exists on disk")
	}
	if fileBackupLocalExistsFromRoot(root, "present.example.com", "file_full_missing.tar.gz") {
		t.Fatal("fileBackupLocalExistsFromRoot() = true, want false for a filename that was never created")
	}
	if fileBackupLocalExistsFromRoot(root, "no-such-site.example.com", "file_full_x.tar.gz") {
		t.Fatal("fileBackupLocalExistsFromRoot() = true, want false when the site's backup directory doesn't exist at all")
	}
}

func TestFileBackupDownloadAndDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (1, 'site', 'download-file.example.com', 'u1', '/www/wwwroot/download-file.example.com', '/www/wwwlogs/download-file.example.com', 'db1', 'u1', '/p', '/n')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups (id, site_id, filename, file_size, mode, transport_status) VALUES (9, 1, 'file_full_x.tar.gz', 20, 'full', 'local')`); err != nil {
		t.Fatalf("insert file_backups: %v", err)
	}

	root := t.TempDir()
	oldRoot := fileBackupStorageRoot
	fileBackupStorageRoot = root
	t.Cleanup(func() { fileBackupStorageRoot = oldRoot })
	filesDir := filepath.Join(root, "download-file.example.com", "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatalf("mkdir files dir: %v", err)
	}
	backupPath := filepath.Join(filesDir, "file_full_x.tar.gz")
	if err := os.WriteFile(backupPath, []byte("backup"), 0644); err != nil {
		t.Fatalf("write file backup: %v", err)
	}

	router := gin.New()
	handler := &BackupHandler{}
	router.GET("/api/websites/:id/file-backups/:bid/download", handler.DownloadFileBackup)
	router.DELETE("/api/websites/:id/file-backups/:bid", handler.DeleteFileBackup)

	downloadReq := httptest.NewRequest(http.MethodGet, "/api/websites/1/file-backups/9/download", nil)
	downloadRec := httptest.NewRecorder()
	router.ServeHTTP(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusOK {
		t.Fatalf("download status = %d, want %d; body=%s", downloadRec.Code, http.StatusOK, downloadRec.Body.String())
	}
	if got := downloadRec.Body.String(); got != "backup" {
		t.Fatalf("download body = %q, want backup", got)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/websites/1/file-backups/9", nil)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d; body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup file still exists or unexpected stat error: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_backups WHERE id = 9`).Scan(&count); err != nil {
		t.Fatalf("query file_backups: %v", err)
	}
	if count != 0 {
		t.Fatalf("file_backups row count = %d, want 0", count)
	}
}

func TestDBBackupDeleteRemovesFileAndRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (1, 'site', 'delete-db-backup.example.com', 'u1', '/www/wwwroot/delete-db-backup.example.com', '/www/wwwlogs/delete-db-backup.example.com', 'db1', 'u1', '/p', '/n')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO db_backups (id, site_id, filename, file_size, db_name, transport_status) VALUES (11, 1, 'db.sql.gz', 20, 'db1', 'local')`); err != nil {
		t.Fatalf("insert db_backups: %v", err)
	}

	root := t.TempDir()
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: root}}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	dbDir := filepath.Join(root, "delete-db-backup.example.com", "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("mkdir db dir: %v", err)
	}
	backupPath := filepath.Join(dbDir, "db.sql.gz")
	if err := os.WriteFile(backupPath, []byte("backup"), 0644); err != nil {
		t.Fatalf("write db backup: %v", err)
	}

	router := gin.New()
	handler := &BackupHandler{}
	router.DELETE("/api/websites/:id/backups/:bid", handler.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/api/websites/1/backups/11", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup file still exists or unexpected stat error: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM db_backups WHERE id = 11`).Scan(&count); err != nil {
		t.Fatalf("query db_backups: %v", err)
	}
	if count != 0 {
		t.Fatalf("db_backups row count = %d, want 0", count)
	}
}

func TestDeleteMissingDBBackupCleansRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (1, 'site', 'missing-db-backup.example.com', 'u1', '/www/wwwroot/missing-db-backup.example.com', '/www/wwwlogs/missing-db-backup.example.com', 'db1', 'u1', '/p', '/n')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO db_backups (id, site_id, filename, file_size, db_name, transport_status) VALUES (12, 1, 'missing.sql.gz', 20, 'db1', 'local')`); err != nil {
		t.Fatalf("insert db_backups: %v", err)
	}

	root := t.TempDir()
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: root}}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	router := gin.New()
	handler := &BackupHandler{}
	router.DELETE("/api/websites/:id/backups/:bid", handler.Delete)

	req := httptest.NewRequest(http.MethodDelete, "/api/websites/1/backups/12", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM db_backups WHERE id = 12`).Scan(&count); err != nil {
		t.Fatalf("query db_backups: %v", err)
	}
	if count != 0 {
		t.Fatalf("db_backups row count = %d, want 0", count)
	}
}

func TestDeleteMissingFileBackupCleansRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (1, 'site', 'missing-file.example.com', 'u1', '/www/wwwroot/missing-file.example.com', '/www/wwwlogs/missing-file.example.com', 'db1', 'u1', '/p', '/n')`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO file_backups (id, site_id, filename, file_size, mode, transport_status) VALUES (10, 1, 'file_full_missing.tar.gz', 20, 'full', 'local')`); err != nil {
		t.Fatalf("insert file_backups: %v", err)
	}

	root := t.TempDir()
	oldRoot := fileBackupStorageRoot
	fileBackupStorageRoot = root
	t.Cleanup(func() { fileBackupStorageRoot = oldRoot })

	router := gin.New()
	handler := &BackupHandler{}
	router.DELETE("/api/websites/:id/file-backups/:bid", handler.DeleteFileBackup)

	req := httptest.NewRequest(http.MethodDelete, "/api/websites/1/file-backups/10", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_backups WHERE id = 10`).Scan(&count); err != nil {
		t.Fatalf("query file_backups: %v", err)
	}
	if count != 0 {
		t.Fatalf("file_backups row count = %d, want 0", count)
	}
}

func TestLocalFileExistsTreatsUnknownStatErrorsAsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "backup.sql.gz")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("running as root; permission-based stat failure cannot be simulated")
	}

	// 去掉父目录的执行权限，让 os.Stat(path) 因权限不足失败，而不是"文件不存在"。
	if err := os.Chmod(filepath.Dir(path), 0644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(path), 0755) })

	// 权限不足这类"无法确认"的错误不能被当成"文件不存在"，否则会误报"本地文件缺失"。
	if !localFileExists(path) {
		t.Fatal("localFileExists() = false, want true when stat fails for a reason other than not-exist")
	}
}

func TestReconcileBackupStatusReturnsErrorWhenRemoteBackupDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)

	router := gin.New()
	router.POST("/api/backups/reconcile-status", ReconcileBackupStatus)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/reconcile-status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// 远程备份未启用时，管理员点了"核对远程状态"按钮应该看到明确的错误提示，
	// 而不是静默返回"成功，修正 0 条"让人误以为核对真的执行过了。
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if resp.Success {
		t.Fatal("success = true, want false when remote backup is disabled")
	}
}

func TestReconcileBackupStatusReturnsUpdatedCountWhenNothingPending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	// 启用远程备份，但没有任何 transport_status='local' 的记录：应该在不发远程请求的情况下
	// 直接返回 updated=0。
	if _, err := db.Exec(`UPDATE remote_backup_settings SET enabled = 1, backup_type = 's3',
		s3_endpoint = 'https://invalid.example.invalid', s3_bucket = 'x', s3_access_key_id = 'k', s3_secret_key = 'secret' WHERE id = 1`); err != nil {
		t.Fatalf("update remote_backup_settings: %v", err)
	}

	router := gin.New()
	router.POST("/api/backups/reconcile-status", ReconcileBackupStatus)

	req := httptest.NewRequest(http.MethodPost, "/api/backups/reconcile-status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Updated int `json:"updated"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if !resp.Success {
		t.Fatal("success = false, want true")
	}
	if resp.Data.Updated != 0 {
		t.Fatalf("updated = %d, want 0", resp.Data.Updated)
	}
}
