package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestUpdateCDNRealIPGroupFail2banFailureRollsBackDB(t *testing.T) {
	setupSecurityTestDB(t)
	insertTestCDNRealIPGroup(t)
	restoreSecurityExecutorHooks(t)

	applyFail2banSettings = func() error { return errors.New("fail2ban failed") }
	regenerateAllSitesNginx = func() error {
		t.Fatal("nginx regenerate should not run after fail2ban failure")
		return nil
	}

	rec := performSecurityRequest(
		http.MethodPut,
		"/groups/99",
		`{"name":"New","header_name":"X-Real-IP","ip_ranges":"198.51.100.0/24","enabled":true,"description":"new desc"}`,
		func(router *gin.Engine, h *SecurityHandler) {
			router.PUT("/groups/:id", h.UpdateCDNRealIPGroup)
		},
	)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodeAPIResponse(t, rec)
	if !strings.Contains(resp.Message, "未生效") || !strings.Contains(resp.Message, "已回滚") {
		t.Fatalf("unexpected message: %s", resp.Message)
	}

	var name, header, ranges string
	var enabled int
	if err := database.GetDB().QueryRow(`SELECT name, header_name, ip_ranges, enabled FROM cdn_realip_groups WHERE id = 99`).
		Scan(&name, &header, &ranges, &enabled); err != nil {
		t.Fatalf("query group: %v", err)
	}
	if name != "Old" || header != "X-Forwarded-For" || ranges != "203.0.113.0/24" || enabled != 1 {
		t.Fatalf("group was not rolled back: name=%q header=%q ranges=%q enabled=%d", name, header, ranges, enabled)
	}
}

func TestCreateCDNRealIPGroupFail2banFailureDeletesGroup(t *testing.T) {
	setupSecurityTestDB(t)
	restoreSecurityExecutorHooks(t)

	applyFail2banSettings = func() error { return errors.New("fail2ban failed") }

	rec := performSecurityRequest(
		http.MethodPost,
		"/groups",
		`{"name":"New","header_name":"X-Forwarded-For","ip_ranges":"198.51.100.0/24","enabled":true,"description":"new desc"}`,
		func(router *gin.Engine, h *SecurityHandler) {
			router.POST("/groups", h.CreateCDNRealIPGroup)
		},
	)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodeAPIResponse(t, rec)
	if !strings.Contains(resp.Message, "未创建") || !strings.Contains(resp.Message, "已回滚") {
		t.Fatalf("unexpected message: %s", resp.Message)
	}

	var count int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM cdn_realip_groups WHERE name = 'New'`).Scan(&count); err != nil {
		t.Fatalf("query group count: %v", err)
	}
	if count != 0 {
		t.Fatalf("created group was not rolled back, count=%d", count)
	}
}

func TestUpdateCDNRealIPGroupNginxFailureRollsBackRuntime(t *testing.T) {
	setupSecurityTestDB(t)
	insertTestCDNRealIPGroup(t)
	restoreSecurityExecutorHooks(t)

	applyCalls := 0
	applyFail2banSettings = func() error {
		applyCalls++
		return nil
	}
	nginxCalls := 0
	regenerateAllSitesNginx = func() error {
		nginxCalls++
		if nginxCalls == 1 {
			return errors.New("nginx failed")
		}
		return nil
	}

	rec := performSecurityRequest(
		http.MethodPut,
		"/groups/99",
		`{"name":"New","header_name":"X-Real-IP","ip_ranges":"198.51.100.0/24","enabled":true,"description":"new desc"}`,
		func(router *gin.Engine, h *SecurityHandler) {
			router.PUT("/groups/:id", h.UpdateCDNRealIPGroup)
		},
	)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodeAPIResponse(t, rec)
	if !strings.Contains(resp.Message, "nginx failed") {
		t.Fatalf("unexpected message: %s", resp.Message)
	}
	if applyCalls != 2 || nginxCalls != 2 {
		t.Fatalf("apply/nginx calls = %d/%d, want 2/2", applyCalls, nginxCalls)
	}
	assertTestCDNRealIPGroupRolledBack(t)
}

func TestUpdateCDNRealIPGroupNginxFailureReportsRollbackFailure(t *testing.T) {
	setupSecurityTestDB(t)
	insertTestCDNRealIPGroup(t)
	restoreSecurityExecutorHooks(t)

	applyFail2banSettings = func() error { return nil }
	nginxCalls := 0
	regenerateAllSitesNginx = func() error {
		nginxCalls++
		if nginxCalls == 1 {
			return errors.New("nginx failed")
		}
		return errors.New("rollback nginx failed")
	}

	rec := performSecurityRequest(
		http.MethodPut,
		"/groups/99",
		`{"name":"New","header_name":"X-Real-IP","ip_ranges":"198.51.100.0/24","enabled":true,"description":"new desc"}`,
		func(router *gin.Engine, h *SecurityHandler) {
			router.PUT("/groups/:id", h.UpdateCDNRealIPGroup)
		},
	)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodeAPIResponse(t, rec)
	if !strings.Contains(resp.Message, "rollback nginx failed") || !strings.Contains(resp.Message, "nginx failed") {
		t.Fatalf("unexpected message: %s", resp.Message)
	}
	if nginxCalls != 2 {
		t.Fatalf("nginx calls = %d, want 2", nginxCalls)
	}
	assertTestCDNRealIPGroupRolledBack(t)
}

func TestDeleteCDNRealIPGroupReportsRestoreFailure(t *testing.T) {
	setupSecurityTestDB(t)
	insertTestCDNRealIPGroup(t)
	restoreSecurityExecutorHooks(t)

	applyFail2banSettings = func() error { return errors.New("fail2ban failed") }
	websiteIDsForCDNRealIPGroup = func(int) ([]int, error) { return []int{101}, nil }
	restoreCDNRealIPGroupWithBindings = func(models.CDNRealIPGroup, []int) error {
		return errors.New("restore failed")
	}
	regenerateAllSitesNginx = func() error {
		t.Fatal("nginx regenerate should not run after fail2ban failure")
		return nil
	}

	rec := performSecurityRequest(
		http.MethodDelete,
		"/groups/99",
		"",
		func(router *gin.Engine, h *SecurityHandler) {
			router.DELETE("/groups/:id", h.DeleteCDNRealIPGroup)
		},
	)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodeAPIResponse(t, rec)
	if !strings.Contains(resp.Message, "数据库回滚失败") || !strings.Contains(resp.Message, "原始错误") {
		t.Fatalf("unexpected message: %s", resp.Message)
	}
}

func setupSecurityTestDB(t *testing.T) {
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

func insertTestCDNRealIPGroup(t *testing.T) {
	t.Helper()
	if _, err := database.GetDB().Exec(`INSERT INTO cdn_realip_groups
		(id, name, provider, header_name, ip_ranges, builtin, enabled, description)
		VALUES (99, 'Old', 'custom', 'X-Forwarded-For', '203.0.113.0/24', 0, 1, 'old desc')`); err != nil {
		t.Fatalf("insert cdn group: %v", err)
	}
}

func assertTestCDNRealIPGroupRolledBack(t *testing.T) {
	t.Helper()
	var name, header, ranges string
	var enabled int
	if err := database.GetDB().QueryRow(`SELECT name, header_name, ip_ranges, enabled FROM cdn_realip_groups WHERE id = 99`).
		Scan(&name, &header, &ranges, &enabled); err != nil {
		t.Fatalf("query group: %v", err)
	}
	if name != "Old" || header != "X-Forwarded-For" || ranges != "203.0.113.0/24" || enabled != 1 {
		t.Fatalf("group was not rolled back: name=%q header=%q ranges=%q enabled=%d", name, header, ranges, enabled)
	}
}

func restoreSecurityExecutorHooks(t *testing.T) {
	t.Helper()
	oldApplyFail2ban := applyFail2banSettings
	oldRegenerateAllSitesNginx := regenerateAllSitesNginx
	oldWebsiteIDsForCDNRealIPGroup := websiteIDsForCDNRealIPGroup
	oldRestoreCDNRealIPGroupWithBindings := restoreCDNRealIPGroupWithBindings
	t.Cleanup(func() {
		applyFail2banSettings = oldApplyFail2ban
		regenerateAllSitesNginx = oldRegenerateAllSitesNginx
		websiteIDsForCDNRealIPGroup = oldWebsiteIDsForCDNRealIPGroup
		restoreCDNRealIPGroupWithBindings = oldRestoreCDNRealIPGroupWithBindings
	})
}

func performSecurityRequest(method, path, body string, register func(*gin.Engine, *SecurityHandler)) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	register(router, &SecurityHandler{})

	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeAPIResponse(t *testing.T, rec *httptest.ResponseRecorder) models.ApiResponse {
	t.Helper()
	var resp models.ApiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	return resp
}
