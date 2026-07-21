package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/models"
)

type wpFleetOverviewAPIResponse struct {
	Success bool                   `json:"success"`
	Message string                 `json:"message"`
	Data    models.WPFleetOverview `json:"data"`
}

func TestWPFleetOverviewHandlerReturnsSafePayload(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	at := "2026-07-21 06:00:00"
	if _, err := db.Exec(`INSERT INTO backup_settings (site_id, enabled) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert backup settings: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO site_wp_inventory_state
		(site_id, status, wordpress_version, plugin_update_count, theme_update_count,
		collection_id, last_attempt_at, last_success_at, updated_at)
		VALUES (1, 'complete', '7.0', 1, 2, 'fleet-handler', ?, ?, ?)`, at, at, at); err != nil {
		t.Fatalf("insert inventory state: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (1, 'core', 'wordpress', '7.1', 'upgrade', 'zh_CN', 'fleet-handler', ?)`, at); err != nil {
		t.Fatalf("insert core update: %v", err)
	}

	rec := performWPFleetOverviewRequest(newWPFleetOverviewTestRouter(db))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response wpFleetOverviewAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Success || len(response.Data.Sites) != 1 || response.Data.Sites[0].Inventory == nil ||
		response.Data.Sites[0].Inventory.UpdateTotal != 4 || response.Data.Counts.UpdateSites != 1 {
		t.Fatalf("response = %+v", response)
	}
	for _, forbidden := range []string{
		"system_user", "web_root", "log_dir", "db_name", "db_user", "php_pool_path", "nginx_conf_path",
		"fastcgi_cache_key", "ssl_last_error", "ssl_cert_path", "ssl_key_path", "collection_id",
		"lease_owner", "runner_hash", "runner_version", "last_error_stage", "response", "stdout", "max_rss", "/tmp/",
	} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestWPFleetOverviewHandlerInternalErrorIsFixed(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	router := newWPFleetOverviewTestRouter(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}
	rec := performWPFleetOverviewRequest(router)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "读取站群概览失败") {
		t.Fatalf("status/body = %d/%s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{"closed", "database", "SQL", "site_wp_", "/tmp/"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("error response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func newWPFleetOverviewTestRouter(db *sql.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &WPFleetOverviewHandler{DB: db}
	router.GET("/api/wp-fleet/overview", handler.Overview)
	return router
}

func performWPFleetOverviewRequest(router http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/wp-fleet/overview", nil)
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
