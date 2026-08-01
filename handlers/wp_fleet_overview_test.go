package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

type wpFleetBulkRefreshAPIResponse struct {
	Success bool                                `json:"success"`
	Data    models.WPInventoryBulkRefreshResult `json:"data"`
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
	if _, err := db.Exec(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (1, 'plugin', 'fleet-plugin/fleet-plugin.php', '2.0', '', '', 'fleet-handler', ?)`, at); err != nil {
		t.Fatalf("insert plugin update: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (1, 'theme', 'fleet-theme-a', '2.0', '', '', 'fleet-handler', ?),
		       (1, 'theme', 'fleet-theme-b', '2.0', '', '', 'fleet-handler', ?)`, at, at); err != nil {
		t.Fatalf("insert theme updates: %v", err)
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

func TestWPFleetOverviewHandlerRefreshAll(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	router := newWPFleetOverviewTestRouter(db)
	req := httptest.NewRequest(http.MethodPost, "/api/wp-fleet/inventory-refresh", nil)
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response wpFleetBulkRefreshAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Success || len(response.Data.SiteIDs) != 1 || response.Data.Created != 1 || response.Data.Existing != 0 {
		t.Fatalf("response = %+v", response)
	}
}

func TestWPFleetOverviewHandlerRefreshAllInternalError(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	router := newWPFleetOverviewTestRouter(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wp-fleet/inventory-refresh", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestWPFleetOverviewHandlerRefreshAllSerializesEmptySiteIDsAsArray(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	if _, err := db.Exec(`UPDATE websites SET site_type='php'`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	newWPFleetOverviewTestRouter(db).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wp-fleet/inventory-refresh", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"site_ids":[]`) {
		t.Fatalf("site_ids must be JSON array: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"failed":0`) {
		t.Fatalf("failed must be zero: %s", rec.Body.String())
	}
}

func TestWPFleetOverviewHandlerRefreshAllReturnsMultiStatusForPartialFailure(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	if _, err := db.Exec(`DELETE FROM websites`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 101; id++ {
		if _, err := tx.Exec(`INSERT INTO websites (id,name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES (?,?,?,'active',?,'/tmp/www','/tmp/log','db','user','/tmp/php','/tmp/nginx')`, id, fmt.Sprintf("site-%d", id), fmt.Sprintf("site-%d.example", id), fmt.Sprintf("wp_%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_middle_handler_batch BEFORE INSERT ON site_wp_inventory_jobs WHEN NEW.site_id BETWEEN 51 AND 100 BEGIN SELECT RAISE(ABORT, 'injected handler batch failure'); END`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	newWPFleetOverviewTestRouter(db).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/wp-fleet/inventory-refresh", nil))
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response wpFleetBulkRefreshAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.SiteIDs) != 51 || response.Data.Created != 51 || response.Data.Existing != 0 || response.Data.Failed != 50 {
		t.Fatalf("response=%+v", response.Data)
	}
}

func newWPFleetOverviewTestRouter(db *sql.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &WPFleetOverviewHandler{DB: db}
	router.GET("/api/wp-fleet/overview", handler.Overview)
	router.POST("/api/wp-fleet/inventory-refresh", handler.RefreshAll)
	return router
}

func performWPFleetOverviewRequest(router http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/wp-fleet/overview", nil)
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
