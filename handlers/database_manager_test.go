package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func TestDatabaseManagerListAggregatesWebsiteAndBackupInfo(t *testing.T) {
	setupDatabaseManagerTestDB(t)
	oldLookup := databaseManagerSizeLookup
	databaseManagerSizeLookup = func(*config.Config) (map[string]int64, error) {
		return map[string]int64{"db_example": 12345}, nil
	}
	t.Cleanup(func() { databaseManagerSizeLookup = oldLookup })

	router := gin.New()
	router.GET("/api/databases", (&DatabaseManagerHandler{}).List)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Success bool               `json:"success"`
		Data    []databaseListItem `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Success || len(response.Data) != 1 {
		t.Fatalf("response = %+v", response)
	}
	item := response.Data[0]
	if item.Domain != "example.com" || item.DBName != "db_example" || item.DBUser != "user_example" {
		t.Fatalf("database identity = %+v", item)
	}
	if !item.SizeAvailable || item.DatabaseSize != 12345 {
		t.Fatalf("database size = %+v", item)
	}
	if !item.BackupEnabled || item.BackupKeepCount != 5 || item.BackupCount != 2 || item.LatestBackupAt == "" {
		t.Fatalf("backup summary = %+v", item)
	}
}

func TestDatabaseManagerListKeepsWorkingWhenSizeLookupFails(t *testing.T) {
	setupDatabaseManagerTestDB(t)
	oldLookup := databaseManagerSizeLookup
	databaseManagerSizeLookup = func(*config.Config) (map[string]int64, error) {
		return nil, errors.New("mysql unavailable")
	}
	t.Cleanup(func() { databaseManagerSizeLookup = oldLookup })

	router := gin.New()
	router.GET("/api/databases", (&DatabaseManagerHandler{}).List)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data []databaseListItem `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].SizeAvailable {
		t.Fatalf("data = %+v", response.Data)
	}
}

func TestDatabaseManagerListMarksUnknownDatabaseSizeUnavailable(t *testing.T) {
	setupDatabaseManagerTestDB(t)
	oldLookup := databaseManagerSizeLookup
	databaseManagerSizeLookup = func(*config.Config) (map[string]int64, error) {
		return map[string]int64{"another_database": 12345}, nil
	}
	t.Cleanup(func() { databaseManagerSizeLookup = oldLookup })

	router := gin.New()
	router.GET("/api/databases", (&DatabaseManagerHandler{}).List)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data []databaseListItem `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].SizeAvailable {
		t.Fatalf("data = %+v", response.Data)
	}
}

func setupDatabaseManagerTestDB(t *testing.T) {
	t.Helper()
	oldDB := database.DB
	oldConfig := config.AppConfig
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	config.AppConfig = &config.Config{}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = oldDB
		config.AppConfig = oldConfig
	})

	if _, err := database.GetDB().Exec(`
		INSERT INTO websites (
			id, name, domain, aliases, status, system_user, web_root, log_dir,
			db_name, db_user, php_pool_path, nginx_conf_path, site_type
		) VALUES (
			1, 'example', 'example.com', '', 'active', 'wp_example', '/www/wwwroot/example.com', '/www/wwwlogs/example.com',
			'db_example', 'user_example', '/etc/php/8.3/fpm/pool.d/example.conf', '/etc/nginx/sites-available/example.conf', 'wordpress'
		)`); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	if _, err := database.GetDB().Exec("INSERT INTO backup_settings (site_id, enabled, keep_count) VALUES (1, 1, 5)"); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO db_backups (site_id, filename, file_size, db_name, created_at) VALUES
		(1, 'old.sql.gz', 10, 'db_example', '2026-07-18 10:00:00'),
		(1, 'new.sql.gz', 20, 'db_example', '2026-07-19 10:00:00')`); err != nil {
		t.Fatalf("insert backups: %v", err)
	}
}
