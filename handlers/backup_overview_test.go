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
