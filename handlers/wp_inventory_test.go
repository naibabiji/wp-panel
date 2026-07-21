package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

type wpInventorySummaryAPIResponse struct {
	Success bool                      `json:"success"`
	Data    models.WPInventorySummary `json:"data"`
}

type wpInventoryRefreshAPIResponse struct {
	Success bool                            `json:"success"`
	Data    models.WPInventoryRefreshResult `json:"data"`
}

type wpInventoryTaskAPIResponse struct {
	Success bool                   `json:"success"`
	Data    models.WPInventoryTask `json:"data"`
}

func TestWPInventoryHandlerSummaryRefreshAndTask(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	router := newWPInventoryHandlerTestRouter(db)

	summaryRec := performWPInventoryRequest(router, http.MethodGet, "/api/websites/1/wp-inventory")
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("summary status = %d, body = %s", summaryRec.Code, summaryRec.Body.String())
	}
	var summary wpInventorySummaryAPIResponse
	if err := json.Unmarshal(summaryRec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if !summary.Success || summary.Data.SiteID != 1 || summary.Data.CollectionStatus != "unknown" || summary.Data.ActiveTask != nil {
		t.Fatalf("summary = %+v", summary)
	}

	refreshRec := performWPInventoryRequest(router, http.MethodPost, "/api/websites/1/wp-inventory/refresh")
	if refreshRec.Code != http.StatusAccepted {
		t.Fatalf("refresh status = %d, body = %s", refreshRec.Code, refreshRec.Body.String())
	}
	var refresh wpInventoryRefreshAPIResponse
	if err := json.Unmarshal(refreshRec.Body.Bytes(), &refresh); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}
	if !refresh.Success || !refresh.Data.Created || refresh.Data.Task.Status != "queued" || refresh.Data.Task.SiteID != 1 {
		t.Fatalf("refresh = %+v", refresh)
	}

	secondRec := performWPInventoryRequest(router, http.MethodPost, "/api/websites/1/wp-inventory/refresh")
	var second wpInventoryRefreshAPIResponse
	if secondRec.Code != http.StatusAccepted || json.Unmarshal(secondRec.Body.Bytes(), &second) != nil ||
		second.Data.Created || second.Data.Task.ID != refresh.Data.Task.ID {
		t.Fatalf("deduplicated refresh status/body = %d/%s", secondRec.Code, secondRec.Body.String())
	}

	taskRec := performWPInventoryRequest(router, http.MethodGet, "/api/websites/1/wp-inventory/tasks/"+refresh.Data.Task.ID)
	if taskRec.Code != http.StatusOK {
		t.Fatalf("task status = %d, body = %s", taskRec.Code, taskRec.Body.String())
	}
	var task wpInventoryTaskAPIResponse
	if err := json.Unmarshal(taskRec.Body.Bytes(), &task); err != nil || task.Data.ID != refresh.Data.Task.ID || task.Data.Status != "queued" {
		t.Fatalf("task = %+v, err = %v", task, err)
	}

	responseText := summaryRec.Body.String() + refreshRec.Body.String() + taskRec.Body.String()
	for _, forbidden := range []string{"system_user", "web_root", "lease_owner", "lease_expires_at", "runner_hash", "runner_version", "stdout", "stderr", "protocol_bytes", "max_rss"} {
		if strings.Contains(responseText, forbidden) {
			t.Fatalf("API response exposed forbidden field %q: %s", forbidden, responseText)
		}
	}
}

func TestWPInventoryHandlerErrorMapping(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES
		(2, 'php.example.com', 'php.example.com', 'active', 'wp_php', '/tmp/php', '/tmp/log', 'db2', 'u2', '/tmp/php.conf', '/tmp/nginx.conf', 'php'),
		(3, 'creating.example.com', 'creating.example.com', 'creating', 'wp_creating', '/tmp/creating', '/tmp/log', 'db3', 'u3', '/tmp/php3.conf', '/tmp/nginx3.conf', 'wordpress')`); err != nil {
		t.Fatalf("insert error mapping sites: %v", err)
	}
	router := newWPInventoryHandlerTestRouter(db)

	tests := []struct {
		name   string
		method string
		path   string
		status int
		text   string
	}{
		{name: "invalid site", method: http.MethodGet, path: "/api/websites/nope/wp-inventory", status: 400, text: "库存请求参数无效"},
		{name: "missing site", method: http.MethodGet, path: "/api/websites/999/wp-inventory", status: 404, text: "未找到"},
		{name: "php summary", method: http.MethodGet, path: "/api/websites/2/wp-inventory", status: 409, text: "只有 WordPress 网站支持组件库存"},
		{name: "creating refresh", method: http.MethodPost, path: "/api/websites/3/wp-inventory/refresh", status: 409, text: "网站当前状态不允许刷新库存"},
		{name: "invalid task", method: http.MethodGet, path: "/api/websites/1/wp-inventory/tasks/not-a-task", status: 400, text: "库存请求参数无效"},
		{name: "missing task", method: http.MethodGet, path: "/api/websites/1/wp-inventory/tasks/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", status: 404, text: "库存任务不存在"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := performWPInventoryRequest(router, tc.method, tc.path)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("status/body = %d/%s, want %d containing %q", rec.Code, rec.Body.String(), tc.status, tc.text)
			}
		})
	}
}

func TestWPInventoryHandlerTaskCannotCrossSites(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	if _, err := db.Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (2, 'other.example.com', 'other.example.com', 'active', 'wp_other', '/tmp/other', '/tmp/log', 'db2', 'u2', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`); err != nil {
		t.Fatalf("insert other site: %v", err)
	}
	router := newWPInventoryHandlerTestRouter(db)
	refreshRec := performWPInventoryRequest(router, http.MethodPost, "/api/websites/1/wp-inventory/refresh")
	var refresh wpInventoryRefreshAPIResponse
	if refreshRec.Code != http.StatusAccepted || json.Unmarshal(refreshRec.Body.Bytes(), &refresh) != nil {
		t.Fatalf("refresh = %d/%s", refreshRec.Code, refreshRec.Body.String())
	}
	rec := performWPInventoryRequest(router, http.MethodGet, "/api/websites/2/wp-inventory/tasks/"+refresh.Data.Task.ID)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "库存任务不存在") {
		t.Fatalf("cross-site task = %d/%s", rec.Code, rec.Body.String())
	}
}

func TestWPInventoryHandlerInternalErrorIsFixed(t *testing.T) {
	db := setupWPInventoryHandlerTestDB(t)
	router := newWPInventoryHandlerTestRouter(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close test DB: %v", err)
	}
	rec := performWPInventoryRequest(router, http.MethodGet, "/api/websites/1/wp-inventory")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "读取 WordPress 库存失败") {
		t.Fatalf("internal error = %d/%s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{"closed", "database", "SQL", "site_wp_", "/tmp/"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("internal response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func setupWPInventoryHandlerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	oldDB := database.DB
	if database.DB != nil {
		_ = database.Close()
	}
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("database.Open(): %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = oldDB
	})
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations(): %v", err)
	}
	if err := database.RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades(): %v", err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (1, 'inventory.example.com', 'inventory.example.com', 'active', 'wp_inventory', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`); err != nil {
		t.Fatalf("insert inventory site: %v", err)
	}
	return database.GetDB()
}

func newWPInventoryHandlerTestRouter(db *sql.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &WPInventoryHandler{DB: db}
	router.GET("/api/websites/:id/wp-inventory", handler.Summary)
	router.POST("/api/websites/:id/wp-inventory/refresh", handler.Refresh)
	router.GET("/api/websites/:id/wp-inventory/tasks/:task_id", handler.Task)
	return router
}

func performWPInventoryRequest(router http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
