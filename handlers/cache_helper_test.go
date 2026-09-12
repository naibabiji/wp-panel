package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func TestFindByDomainReturnsManagedSecurityStatuses(t *testing.T) {
	setupCacheHelperTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET password_reset_mode='admin' WHERE domain='example.com'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO site_wp_anomaly_state(site_id,enabled,last_success,last_error) VALUES(1,1,123,'')`); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/api/sites/find", (&CacheHelperHandler{}).FindByDomain)
	req := httptest.NewRequest(http.MethodGet, "/api/sites/find?domain=example.com", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-WP-Panel-Key", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			AnomalyMonitorStatus string `json:"anomaly_monitor_status"`
			PasswordResetMode    string `json:"password_reset_mode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Data.AnomalyMonitorStatus != "active" || response.Data.PasswordResetMode != "admin" {
		t.Fatalf("response=%+v", response)
	}
}

func TestCacheHelperAPIKeyRequiresLocalhost(t *testing.T) {
	setupCacheHelperTestDB(t)
	handler := &CacheHelperHandler{}

	local := cacheHelperContext("127.0.0.1:12345", "secret")
	if !handler.checkAPIKey("example.com", local) {
		t.Fatal("local request with valid key should be allowed")
	}

	remote := cacheHelperContext("203.0.113.10:12345", "secret")
	if handler.checkAPIKey("example.com", remote) {
		t.Fatal("remote request with valid key should be rejected")
	}

	missingKey := cacheHelperContext("127.0.0.1:12345", "")
	if handler.checkAPIKey("example.com", missingKey) {
		t.Fatal("local request without key should be rejected")
	}
}

func setupCacheHelperTestDB(t *testing.T) {
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
			name, domain, aliases, status, system_user, web_root, log_dir,
			db_name, db_user, php_pool_path, nginx_conf_path, site_type, plugin_api_key
		) VALUES (
			'example', 'example.com', '', 'active', 'wp_example', '/www/wwwroot/example.com', '/www/wwwlogs/example.com',
			'db_example', 'user_example', '/etc/php/8.3/fpm/pool.d/example.conf', '/etc/nginx/sites-available/example.conf',
			'wordpress', 'secret'
		)
	`)
	if err != nil {
		t.Fatalf("insert website: %v", err)
	}
}

func cacheHelperContext(remoteAddr, apiKey string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/sites/find?domain=example.com", nil)
	req.RemoteAddr = remoteAddr
	if apiKey != "" {
		req.Header.Set("X-WP-Panel-Key", apiKey)
	}
	c.Request = req
	return c
}
