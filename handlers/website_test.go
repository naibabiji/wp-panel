package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func TestNormalizeWPSiteURL(t *testing.T) {
	got, err := normalizeWPSiteURL(" https://example.com/wp ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/wp" {
		t.Fatalf("normalizeWPSiteURL trimmed to %q", got)
	}

	if got, err := normalizeWPSiteURL(""); err != nil || got != "" {
		t.Fatalf("empty URL = %q, %v; want empty without error", got, err)
	}
}

func TestToggleStatusRejectsEnableForErrorSite(t *testing.T) {
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		database.DB = oldDB
	})
	result, err := database.GetDB().Exec(`INSERT INTO websites
		(name,domain,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path)
		VALUES ('error site','error.example.com','error','wordpress','u','/www/error','/logs/error','db','u','/php/error','/nginx/error')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &WebsiteHandler{}
	router.PUT("/api/websites/:id/status", handler.ToggleStatus)
	req := httptest.NewRequest(http.MethodPut, "/api/websites/"+strconv.FormatInt(id, 10)+"/status", strings.NewReader(`{"action":"enable"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestReplaceWPSiteURLDomainPreservesURLParts(t *testing.T) {
	got, err := replaceWPSiteURLDomain("https://old.example.com:8443/wp/", "old.example.com", "new.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://new.example.com:8443/wp/"; got != want {
		t.Fatalf("replaceWPSiteURLDomain() = %q, want %q", got, want)
	}
}

func TestReplaceWPSiteURLDomainRejectsDifferentHost(t *testing.T) {
	if _, err := replaceWPSiteURLDomain("https://cdn.example.com/wp", "old.example.com", "new.example.com"); err == nil {
		t.Fatal("replaceWPSiteURLDomain() accepted a URL using a different host")
	}
}

func TestPanelDomainFromWPSiteURLs(t *testing.T) {
	got, err := panelDomainFromWPSiteURLs(
		"https://new.example.com:8443/wp-core",
		"http://new.example.com/blog",
	)
	if err != nil {
		t.Fatalf("panelDomainFromWPSiteURLs() error = %v", err)
	}
	if got != "new.example.com" {
		t.Fatalf("panelDomainFromWPSiteURLs() = %q, want new.example.com", got)
	}
}

func TestPanelDomainFromWPSiteURLsRejectsDifferentHosts(t *testing.T) {
	if _, err := panelDomainFromWPSiteURLs("https://admin.example.com/wp", "https://www.example.com"); err == nil {
		t.Fatal("panelDomainFromWPSiteURLs() accepted different hosts")
	}
}

func TestPanelDomainFromWPSiteURLsRejectsIP(t *testing.T) {
	if _, err := panelDomainFromWPSiteURLs("http://192.0.2.1/wp", "http://192.0.2.1"); err == nil {
		t.Fatal("panelDomainFromWPSiteURLs() accepted an IP address")
	}
}

func TestNormalizeWPSiteURLRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"example.com", "ftp://example.com", "https://"} {
		if _, err := normalizeWPSiteURL(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestReinstallWordPressErrorMessageShowsSafeStage(t *testing.T) {
	msg := reinstallWordPressErrorMessage(errors.New("重建数据库失败: mysql: Access denied for /www/server/panel/config.json"))
	if msg != "WordPress 重装失败：重建数据库失败" {
		t.Fatalf("message = %q", msg)
	}
}

func TestReinstallWordPressErrorMessageHidesUnknownDetails(t *testing.T) {
	msg := reinstallWordPressErrorMessage(errors.New("mysql: Access denied for /www/server/panel/config.json"))
	if msg != "WordPress 重装失败" {
		t.Fatalf("message = %q", msg)
	}
}

func TestIsAllowedSiteLogFilenameSupportsCurrentLegacyAndDateNames(t *testing.T) {
	valid := []string{
		"access.log",
		"access.log.1",
		"access.log.2.gz",
		"access.log-2026-06-29",
		"access.log-2026-06-29.gz",
	}
	for _, name := range valid {
		if !isAllowedSiteLogFilename("access", name) {
			t.Fatalf("expected %q to be allowed", name)
		}
	}
}

func TestIsAllowedSiteLogFilenameRejectsWrongTypeAndTraversal(t *testing.T) {
	invalid := []string{
		"error.log",
		"access.log.backup",
		"access.log-2026-99-99",
		"../access.log",
		"sub/access.log",
		`sub\access.log`,
	}
	for _, name := range invalid {
		if isAllowedSiteLogFilename("access", name) {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}

func TestResolveSiteLogFileRequiresAbsoluteLogDir(t *testing.T) {
	if _, err := resolveSiteLogFile("relative/logs", "access", "access.log"); err == nil {
		t.Fatal("expected relative log dir to be rejected")
	}
}

func TestAllDigitsEdgeCases(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"123", true},
		{"abc", false},
		{"12a", false},
	}
	for _, tc := range cases {
		if got := allDigits(tc.value); got != tc.want {
			t.Fatalf("allDigits(%q) = %v; want %v", tc.value, got, tc.want)
		}
	}
}

func TestListSiteLogFilesIncludesCurrentLegacyAndDateFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"access.log",
		"access.log.1",
		"access.log-2026-06-29.gz",
		"error.log",
		"access.log.backup",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "access.log"), filepath.Join(dir, "access.log.2")); err != nil {
		t.Fatal(err)
	}

	files, err := listSiteLogFiles(dir, "access")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.Name)
	}
	want := map[string]bool{
		"access.log":               true,
		"access.log.1":             true,
		"access.log-2026-06-29.gz": true,
	}
	if len(got) != len(want) {
		t.Fatalf("files = %v; want only %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected file %q in %v", name, got)
		}
	}
	if !files[0].Current || files[0].Name != "access.log" {
		t.Fatalf("current log should be listed first, got %+v", files)
	}
}

func TestListLogFilesHandlerReturnsFiles(t *testing.T) {
	router, logDir, siteID := setupWebsiteLogFilesHandlerTest(t, "wordpress")
	if err := os.WriteFile(filepath.Join(logDir, "access.log"), []byte("current"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "access.log-2026-06-29.gz"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	rec := performWebsiteLogRequest(router, http.MethodGet, "/api/websites/"+siteID+"/log-files?type=access")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "access.log") || !strings.Contains(body, "access.log-2026-06-29.gz") {
		t.Fatalf("body missing log files: %s", body)
	}
}

func TestListLogFilesHandlerRejectsSecurityLogsForNonWordPressSite(t *testing.T) {
	router, _, siteID := setupWebsiteLogFilesHandlerTest(t, "php")

	rec := performWebsiteLogRequest(router, http.MethodGet, "/api/websites/"+siteID+"/log-files?type=security")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestDownloadLogFileHandlerRejectsMissingWebsite(t *testing.T) {
	router, _, _ := setupWebsiteLogFilesHandlerTest(t, "wordpress")

	rec := performWebsiteLogRequest(router, http.MethodGet, "/api/websites/999/logs/download?type=access&file=access.log")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestDownloadLogFileHandlerRejectsInvalidType(t *testing.T) {
	router, _, siteID := setupWebsiteLogFilesHandlerTest(t, "wordpress")

	rec := performWebsiteLogRequest(router, http.MethodGet, "/api/websites/"+siteID+"/logs/download?type=debug&file=access.log")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestDownloadLogFileHandlerRejectsSymlink(t *testing.T) {
	router, logDir, siteID := setupWebsiteLogFilesHandlerTest(t, "wordpress")
	target := filepath.Join(logDir, "access.log")
	if err := os.WriteFile(target, []byte("current"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(logDir, "access.log.1")); err != nil {
		t.Fatal(err)
	}

	rec := performWebsiteLogRequest(router, http.MethodGet, "/api/websites/"+siteID+"/logs/download?type=access&file=access.log.1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestDownloadLogFileHandlerReturnsFile(t *testing.T) {
	router, logDir, siteID := setupWebsiteLogFilesHandlerTest(t, "wordpress")
	if err := os.WriteFile(filepath.Join(logDir, "access.log"), []byte("current log"), 0644); err != nil {
		t.Fatal(err)
	}

	rec := performWebsiteLogRequest(router, http.MethodGet, "/api/websites/"+siteID+"/logs/download?type=access&file=access.log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "current log" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "access.log") {
		t.Fatalf("Content-Disposition = %q, want access.log", cd)
	}
}

func setupWebsiteLogFilesHandlerTest(t *testing.T, siteType string) (*gin.Engine, string, string) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	_ = database.Close()
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	logDir := t.TempDir()
	result, err := database.GetDB().Exec(`
		INSERT INTO websites (
			name, domain, aliases, status, system_user, web_root, log_dir,
			db_name, db_user, php_pool_path, nginx_conf_path, site_type
		) VALUES (
			'example', 'example.com', '', 'active', 'wp_example', ?, ?,
			'db_example', 'user_example', '/etc/php/8.3/fpm/pool.d/example.conf',
			'/etc/nginx/sites-available/example.conf', ?
		)
	`, t.TempDir(), logDir, siteType)
	if err != nil {
		t.Fatalf("insert website: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}

	router := gin.New()
	handler := &WebsiteHandler{}
	router.GET("/api/websites/:id/log-files", handler.ListLogFiles)
	router.GET("/api/websites/:id/logs/download", handler.DownloadLogFile)
	return router, logDir, strconv.FormatInt(id, 10)
}

func performWebsiteLogRequest(router *gin.Engine, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestDeleteWebsiteRejectsActiveMigrationWithActionableMessage(t *testing.T) {
	router, _, siteID := setupWebsiteLogFilesHandlerTest(t, "wordpress")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO site_migration_peers(id,status) VALUES ('peer_00000000001','paired')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_batches(id,peer_id,direction,status) VALUES ('batch_0000000001','peer_00000000001','source','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_sites(id,batch_id,source_site_id,source_domain,target_domain,site_type,status,stage)
		VALUES ('migration_0000001','batch_0000000001',?,'example.com','example.com','wordpress','awaiting_cutover','transferring_database')`, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status)
		VALUES ('example.com',?,'migration_0000001','source','active')`, siteID); err != nil {
		t.Fatal(err)
	}

	handler := &WebsiteHandler{}
	router.DELETE("/api/websites/:id", handler.Delete)
	rec := performWebsiteLogRequest(router, http.MethodDelete, "/api/websites/"+siteID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "不能从网站列表直接删除") || !strings.Contains(rec.Body.String(), "网站搬家") {
		t.Fatalf("body=%s, want actionable migration delete message", rec.Body.String())
	}
}
