package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func TestSaveWPOptimizationsRecordsOperationLog(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)

	router := gin.New()
	handler := &WebsiteHandler{}
	router.PUT("/api/websites/:id/wp-optimizations", handler.SaveWPOptimizations)

	body := `{
		"fcache_enabled": false,
		"fcache_ttl": 300,
		"disable_wp_updates": false,
		"disable_file_editing": false,
		"xmlrpc_enabled": false,
		"wp_debug_enabled": true,
		"wp_debug_display": true,
		"wp_post_revisions": -1,
		"wp_memory_limit": ""
	}`
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-optimizations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var operation, target, message string
	err := database.GetDB().QueryRow(`SELECT operation, target, message FROM operation_logs ORDER BY id DESC LIMIT 1`).Scan(&operation, &target, &message)
	if err != nil {
		t.Fatalf("query operation log: %v", err)
	}
	if operation != "wp_optimizations" {
		t.Fatalf("operation = %q, want wp_optimizations", operation)
	}
	if target != "example.com" {
		t.Fatalf("target = %q, want example.com", target)
	}
	if !strings.Contains(message, "WP_DEBUG=开启") {
		t.Fatalf("message missing WP_DEBUG state: %q", message)
	}
	if !strings.Contains(message, "浏览器错误显示=开启") {
		t.Fatalf("message missing WP_DEBUG_DISPLAY state: %q", message)
	}
}

func TestSaveWPOptimizationsPreservesDisplayWhenFieldIsMissing(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	var webRoot string
	if err := database.GetDB().QueryRow(`SELECT web_root FROM websites WHERE id=1`).Scan(&webRoot); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(webRoot, "wp-config.php")
	config := "<?php\ndefine('WP_DEBUG', true);\ndefine(\"WP_DEBUG_DISPLAY\", true);\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.PUT("/api/websites/:id/wp-optimizations", (&WebsiteHandler{}).SaveWPOptimizations)
	body := `{"fcache_enabled":false,"fcache_ttl":300,"disable_wp_updates":false,"disable_file_editing":false,"xmlrpc_enabled":false,"wp_debug_enabled":true,"wp_post_revisions":-1,"wp_memory_limit":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-optimizations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(updated)
	if strings.Count(got, "WP_DEBUG_DISPLAY") != 1 || !strings.Contains(got, "define('WP_DEBUG_DISPLAY', true);") {
		t.Fatalf("missing field did not preserve display state:\n%s", got)
	}
}

func TestSaveWPOptimizationsDoesNotUpdateDatabaseWhenWPConfigWriteFails(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	var webRoot string
	if err := database.GetDB().QueryRow(`SELECT web_root FROM websites WHERE id=1`).Scan(&webRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(webRoot, "wp-config.php")); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.PUT("/api/websites/:id/wp-optimizations", (&WebsiteHandler{}).SaveWPOptimizations)
	body := `{"fcache_enabled":false,"fcache_ttl":300,"disable_wp_updates":false,"disable_file_editing":false,"xmlrpc_enabled":false,"wp_debug_enabled":true,"wp_post_revisions":-1,"wp_memory_limit":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-optimizations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var enabled int
	if err := database.GetDB().QueryRow(`SELECT wp_debug_enabled FROM websites WHERE id=1`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatalf("wp_debug_enabled=%d, want unchanged 0", enabled)
	}
}

func TestSaveWPOptimizationsRestoresWPConfigWhenDatabaseUpdateFails(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	var webRoot string
	if err := database.GetDB().QueryRow(`SELECT web_root FROM websites WHERE id=1`).Scan(&webRoot); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(webRoot, "wp-config.php")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`CREATE TRIGGER reject_optimization_update BEFORE UPDATE ON websites BEGIN SELECT RAISE(FAIL, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.PUT("/api/websites/:id/wp-optimizations", (&WebsiteHandler{}).SaveWPOptimizations)
	body := `{"fcache_enabled":false,"fcache_ttl":300,"disable_wp_updates":false,"disable_file_editing":false,"xmlrpc_enabled":false,"wp_debug_enabled":true,"wp_post_revisions":-1,"wp_memory_limit":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-optimizations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("wp-config.php was not restored:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestPluginOptimizationsFailureKeepsDatabaseAndRestoresConfig(t *testing.T) {
	t.Run("wp-config write failure keeps database", func(t *testing.T) {
		setupWebsiteOptimizationsTestDB(t)
		var webRoot string
		if err := database.GetDB().QueryRow(`SELECT web_root FROM websites WHERE id=1`).Scan(&webRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetDB().Exec(`UPDATE websites SET plugin_api_key='secret' WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(webRoot, "wp-config.php")); err != nil {
			t.Fatal(err)
		}
		rec := performPluginOptimizationRequest(t)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var enabled int
		if err := database.GetDB().QueryRow(`SELECT wp_debug_enabled FROM websites WHERE id=1`).Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		if enabled != 0 {
			t.Fatalf("wp_debug_enabled=%d, want unchanged 0", enabled)
		}
	})

	t.Run("database failure restores wp-config", func(t *testing.T) {
		setupWebsiteOptimizationsTestDB(t)
		var webRoot string
		if err := database.GetDB().QueryRow(`SELECT web_root FROM websites WHERE id=1`).Scan(&webRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetDB().Exec(`UPDATE websites SET plugin_api_key='secret' WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(webRoot, "wp-config.php")
		before, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.GetDB().Exec(`CREATE TRIGGER reject_plugin_optimization_update BEFORE UPDATE ON websites BEGIN SELECT RAISE(FAIL, 'test failure'); END`); err != nil {
			t.Fatal(err)
		}
		rec := performPluginOptimizationRequest(t)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		after, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("wp-config.php was not restored:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

func TestSaveWPOptimizationsAllowsPHPWithoutWPConfig(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	var webRoot string
	if err := database.GetDB().QueryRow(`SELECT web_root FROM websites WHERE id=1`).Scan(&webRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE websites SET site_type='php' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(webRoot, "wp-config.php")); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.PUT("/api/websites/:id/wp-optimizations", (&WebsiteHandler{}).SaveWPOptimizations)
	body := `{"fcache_enabled":false,"fcache_ttl":600,"disable_wp_updates":false,"disable_file_editing":false,"xmlrpc_enabled":false,"wp_debug_enabled":false,"wp_post_revisions":-1,"wp_memory_limit":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-optimizations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var ttl int
	if err := database.GetDB().QueryRow(`SELECT fastcgi_cache_ttl FROM websites WHERE id=1`).Scan(&ttl); err != nil {
		t.Fatal(err)
	}
	if ttl != 600 {
		t.Fatalf("fastcgi_cache_ttl=%d, want 600", ttl)
	}
}

func performPluginOptimizationRequest(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return performPluginOptimizationRequestWithBody(t, `{"domain":"example.com","enabled":false,"ttl":300,"disable_wp_updates":false,"disable_file_editing":false,"wp_debug_enabled":true,"wp_post_revisions":-1,"wp_memory_limit":""}`)
}

func performPluginOptimizationRequestWithBody(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.PUT("/api/sites/optimizer-settings", (&CacheHelperHandler{}).UpdateOptimizerSettings)
	req := httptest.NewRequest(http.MethodPut, "/api/sites/optimizer-settings", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WP-Panel-Key", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPluginOptimizationFileLockSafeOnlyUpdatesCacheFields(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`UPDATE websites SET plugin_api_key='secret', file_lock_enabled=1, fastcgi_cache_enabled=0, fastcgi_cache_ttl=300, disable_wp_updates=1, disable_file_editing=1, wp_debug_enabled=1, wp_post_revisions=5, wp_memory_limit='128M' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	rec := performPluginOptimizationRequestWithBody(t, `{"domain":"example.com","enabled":true,"ttl":600,"disable_wp_updates":false,"disable_file_editing":false,"wp_debug_enabled":false,"wp_post_revisions":0,"wp_memory_limit":"32M","file_lock_safe_only":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var cacheEnabled, ttl, noUpdates, noEdit, debugEnabled, revisions int
	var memoryLimit string
	if err := db.QueryRow(`SELECT fastcgi_cache_enabled, fastcgi_cache_ttl, disable_wp_updates, disable_file_editing, wp_debug_enabled, wp_post_revisions, wp_memory_limit FROM websites WHERE id=1`).Scan(&cacheEnabled, &ttl, &noUpdates, &noEdit, &debugEnabled, &revisions, &memoryLimit); err != nil {
		t.Fatal(err)
	}
	if cacheEnabled != 1 || ttl != 600 {
		t.Fatalf("cache fields=(%d,%d), want (1,600)", cacheEnabled, ttl)
	}
	if noUpdates != 1 || noEdit != 1 || debugEnabled != 1 || revisions != 5 || memoryLimit != "128M" {
		t.Fatalf("protected fields changed: updates=%d edit=%d debug=%d revisions=%d memory=%q", noUpdates, noEdit, debugEnabled, revisions, memoryLimit)
	}
}

func TestPluginOptimizationFileLockRejectsFullUpdate(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET plugin_api_key='secret', file_lock_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	rec := performPluginOptimizationRequest(t)
	if rec.Code != http.StatusLocked {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPluginOptimizationRejectsStaleFileLockSafeOnlyUpdate(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET plugin_api_key='secret', file_lock_enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	rec := performPluginOptimizationRequestWithBody(t, `{"domain":"example.com","enabled":true,"ttl":600,"file_lock_safe_only":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWPOptimizationEndpointsSerializeBySite(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	if _, err := database.GetDB().Exec(`UPDATE websites SET plugin_api_key='secret' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	lock := wpOptimizationSiteLock(1)
	lock.Lock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- performPluginOptimizationRequest(t)
	}()
	select {
	case <-done:
		lock.Unlock()
		t.Fatal("plugin optimization request bypassed the site lock")
	case <-time.After(50 * time.Millisecond):
	}
	lock.Unlock()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plugin optimization request did not continue after unlocking")
	}
}

func TestWPOptimizationRollbackFailureIsReportedAndAudited(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	respondWPOptimizationFailure(
		context,
		func() error { return errors.New("restore denied") },
		"example.com",
		http.StatusConflict,
		"保存冲突",
		errors.New("database conflict"),
	)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "wp-config.php 恢复失败") {
		t.Fatalf("response does not report rollback failure: %s", recorder.Body.String())
	}
	var status, message string
	if err := database.GetDB().QueryRow(`SELECT status, message FROM operation_logs ORDER BY id DESC LIMIT 1`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(message, "rollback failed: restore denied") {
		t.Fatalf("status=%q message=%q", status, message)
	}
}

func TestSetFileEditingProtectionUpdatesOnlyEditorSetting(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)

	router := gin.New()
	handler := &WebsiteHandler{}
	router.PUT("/api/websites/:id/file-editor", handler.SetFileEditingProtection)

	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/file-editor", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var enabled int
	if err := database.GetDB().QueryRow("SELECT disable_file_editing FROM websites WHERE id = 1").Scan(&enabled); err != nil {
		t.Fatalf("query disable_file_editing: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("disable_file_editing = %d, want 1", enabled)
	}
	var webRoot string
	if err := database.GetDB().QueryRow("SELECT web_root FROM websites WHERE id = 1").Scan(&webRoot); err != nil {
		t.Fatalf("query web_root: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(webRoot, "wp-config.php"))
	if err != nil {
		t.Fatalf("read wp-config.php: %v", err)
	}
	if !strings.Contains(string(config), "define('DISALLOW_FILE_EDIT', true);") {
		t.Fatalf("DISALLOW_FILE_EDIT missing: %s", config)
	}
	if strings.Contains(string(config), "AUTOMATIC_UPDATER_DISABLED") {
		t.Fatalf("unrelated optimization constant changed: %s", config)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/websites/1/file-editor", strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := database.GetDB().QueryRow("SELECT disable_file_editing FROM websites WHERE id = 1").Scan(&enabled); err != nil {
		t.Fatalf("query disabled file editing: %v", err)
	}
	if enabled != 0 {
		t.Fatalf("disable_file_editing = %d, want 0", enabled)
	}
	config, err = os.ReadFile(filepath.Join(webRoot, "wp-config.php"))
	if err != nil {
		t.Fatalf("read disabled wp-config.php: %v", err)
	}
	if strings.Contains(string(config), "DISALLOW_FILE_EDIT") {
		t.Fatalf("DISALLOW_FILE_EDIT was not removed: %s", config)
	}
	if strings.Contains(string(config), "AUTOMATIC_UPDATER_DISABLED") {
		t.Fatalf("unrelated optimization constant changed while disabling: %s", config)
	}
}

func TestSetFileEditingProtectionRejectsLockedSite(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	if _, err := database.GetDB().Exec("UPDATE websites SET file_lock_enabled = 1, file_lock_mode = 'standard', file_lock_apply_status = 'ready' WHERE id = 1"); err != nil {
		t.Fatalf("lock site: %v", err)
	}

	router := gin.New()
	handler := &WebsiteHandler{}
	router.PUT("/api/websites/:id/file-editor", handler.SetFileEditingProtection)
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/file-editor", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusLocked {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusLocked, rec.Body.String())
	}
}

func TestSetWPUpdateChecksUpdatesOnlyUpdateSetting(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	router := gin.New()
	handler := &WebsiteHandler{}
	router.PUT("/api/websites/:id/wp-update-checks", handler.SetWPUpdateChecks)

	request := func(enabled bool) *httptest.ResponseRecorder {
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-update-checks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(false); rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	var disabled int
	var webRoot string
	if err := database.GetDB().QueryRow(`SELECT disable_wp_updates, web_root FROM websites WHERE id=1`).Scan(&disabled, &webRoot); err != nil {
		t.Fatal(err)
	}
	if disabled != 1 {
		t.Fatalf("disable_wp_updates=%d", disabled)
	}
	config, err := os.ReadFile(filepath.Join(webRoot, "wp-config.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "define('AUTOMATIC_UPDATER_DISABLED', true);") {
		t.Fatalf("constant missing: %s", config)
	}
	if strings.Contains(string(config), "DISALLOW_FILE_EDIT") {
		t.Fatalf("unrelated setting changed: %s", config)
	}

	if rec := request(true); rec.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := database.GetDB().QueryRow(`SELECT disable_wp_updates FROM websites WHERE id=1`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 0 {
		t.Fatalf("disable_wp_updates=%d", disabled)
	}
	config, err = os.ReadFile(filepath.Join(webRoot, "wp-config.php"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "AUTOMATIC_UPDATER_DISABLED") {
		t.Fatalf("constant not removed: %s", config)
	}
}

func TestSetWPUpdateChecksRejectsUnsupportedAndLockedSites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func()
		want    int
	}{
		{name: "php", prepare: func() { _, _ = database.GetDB().Exec(`UPDATE websites SET site_type='php' WHERE id=1`) }, want: http.StatusBadRequest},
		{name: "locked", prepare: func() {
			_, _ = database.GetDB().Exec(`UPDATE websites SET file_lock_enabled=1, file_lock_mode='standard', file_lock_apply_status='ready' WHERE id=1`)
		}, want: http.StatusLocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupWebsiteOptimizationsTestDB(t)
			tc.prepare()
			router := gin.New()
			router.PUT("/api/websites/:id/wp-update-checks", (&WebsiteHandler{}).SetWPUpdateChecks)
			req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-update-checks", strings.NewReader(`{"enabled":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestSaveWPOptimizationsRejectsStaleUpdateCheckState(t *testing.T) {
	setupWebsiteOptimizationsTestDB(t)
	var webRoot string
	if err := database.GetDB().QueryRow(`SELECT web_root FROM websites WHERE id=1`).Scan(&webRoot); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(webRoot, "wp-config.php")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE websites SET disable_wp_updates=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.PUT("/api/websites/:id/wp-optimizations", (&WebsiteHandler{}).SaveWPOptimizations)
	body := `{"fcache_enabled":false,"fcache_ttl":300,"disable_wp_updates":false,"expected_disable_wp_updates":false,"disable_file_editing":false,"xmlrpc_enabled":false,"wp_debug_enabled":false,"wp_post_revisions":-1,"wp_memory_limit":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/wp-optimizations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var disabled int
	if err := database.GetDB().QueryRow(`SELECT disable_wp_updates FROM websites WHERE id=1`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 1 {
		t.Fatalf("stale form overwrote disable_wp_updates=%d", disabled)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatal("stale form changed wp-config.php")
	}
}

func setupWebsiteOptimizationsTestDB(t *testing.T) {
	t.Helper()
	oldPublish := publishSiteNginxWithCacheRollback
	publishSiteNginxWithCacheRollback = func(int, int, int) error { return nil }
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	t.Cleanup(func() {
		publishSiteNginxWithCacheRollback = oldPublish
		_ = database.Close()
		database.DB = oldDB
	})

	webRoot := t.TempDir()
	config := `<?php
define('DB_NAME', 'db_example');
define('DB_USER', 'user_example');
define('DB_PASSWORD', 'secret');
$table_prefix = 'wp_';
`
	if err := os.WriteFile(filepath.Join(webRoot, "wp-config.php"), []byte(config), 0600); err != nil {
		t.Fatalf("write wp-config.php: %v", err)
	}

	_, err := database.GetDB().Exec(`
		INSERT INTO websites (
			id, name, domain, aliases, status, system_user, web_root, log_dir,
			db_name, db_user, php_pool_path, nginx_conf_path, site_type
		) VALUES (
			1, 'example', 'example.com', '', 'active', 'wp_example', ?, '/www/wwwlogs/example.com',
			'db_example', 'user_example', '/etc/php/8.3/fpm/pool.d/example.conf', '/etc/nginx/sites-available/example.conf',
			'wordpress'
		)
	`, webRoot)
	if err != nil {
		t.Fatalf("insert website: %v", err)
	}
}
