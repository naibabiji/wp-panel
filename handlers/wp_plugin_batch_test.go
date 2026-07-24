package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/middleware"
	"github.com/naibabiji/wp-panel/models"
)

type fakeWPPluginBatchAPIService struct {
	createCalls   int
	rollbackCalls int
	ignoreCalls   int
	rollbackErr   error
	ignoreErr     error
}

func (f *fakeWPPluginBatchAPIService) Create(_ context.Context, _ int, _ string, keys []string) (models.WPPluginBatch, error) {
	f.createCalls++
	items := make([]models.WPPluginBatchItem, len(keys))
	for i, key := range keys {
		items[i] = models.WPPluginBatchItem{Position: i + 1, ComponentKey: key, Status: "pending"}
	}
	return models.WPPluginBatch{ID: "wpub_0123456789abcdef0123456789abcdef", Status: "running", TotalCount: len(keys), Items: items}, nil
}
func (f *fakeWPPluginBatchAPIService) Get(context.Context, int, string) (models.WPPluginBatch, error) {
	return models.WPPluginBatch{}, executor.ErrWPPluginUpdateNotFound
}
func (f *fakeWPPluginBatchAPIService) ListForSite(context.Context, int) ([]models.WPPluginBatch, error) {
	return nil, nil
}
func (f *fakeWPPluginBatchAPIService) Rollback(context.Context, int, string) error {
	f.rollbackCalls++
	return f.rollbackErr
}
func (f *fakeWPPluginBatchAPIService) Ignore(context.Context, int, string) error {
	f.ignoreCalls++
	return f.ignoreErr
}

func TestWPPluginBatchCreateRequiresCSRFBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPPluginBatchAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() }, middleware.CSRF())
	r.POST("/api/websites/:id/wp-plugin-batch", (&WPPluginBatchHandler{Service: service}).Create)
	req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-plugin-batch", strings.NewReader(
		`{"component_keys":["sample/sample.php"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || service.createCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.createCalls, rec.Body.String())
	}
}

func TestWPPluginBatchCreateRejectsEmptyAndOversizedLists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPPluginBatchAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.POST("/api/websites/:id/wp-plugin-batch", (&WPPluginBatchHandler{Service: service}).Create)
	tooMany := strings.Repeat(`"a/a.php",`, wpPluginBatchMaxComponents)
	tooMany = "[" + strings.TrimSuffix(tooMany, ",") + "]"
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"component_keys":[]}`, http.StatusBadRequest},
		{`{"component_keys":` + tooMany + `,"extra":true}`, http.StatusBadRequest},
		{`{"component_keys":["a/a.php","b/b.php"]}`, http.StatusAccepted},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-plugin-batch", strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("body=%s status=%d want=%d response=%s", test.body, rec.Code, test.status, rec.Body.String())
		}
	}
	if service.createCalls != 1 {
		t.Fatalf("create calls=%d", service.createCalls)
	}
}

func TestWPPluginBatchUnavailableServiceReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.GET("/api/websites/:id/wp-plugin-batch/:batch_id", (&WPPluginBatchHandler{}).Get)
	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/wp-plugin-batch/wpub_0123456789abcdef0123456789abcdef", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestWPPluginBatchRollbackAndIgnoreRequireCSRF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPPluginBatchAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() }, middleware.CSRF())
	handler := &WPPluginBatchHandler{Service: service}
	r.POST("/api/websites/:id/wp-plugin-batch/tasks/:task_id/rollback", handler.Rollback)
	r.POST("/api/websites/:id/wp-plugin-batch/tasks/:task_id/ignore", handler.Ignore)
	for _, path := range []string{
		"/api/websites/1/wp-plugin-batch/tasks/wpu_0123456789abcdef0123456789abcdef/rollback",
		"/api/websites/1/wp-plugin-batch/tasks/wpu_0123456789abcdef0123456789abcdef/ignore",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("path=%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if service.rollbackCalls != 0 || service.ignoreCalls != 0 {
		t.Fatalf("rollback=%d ignore=%d, expected CSRF to block both", service.rollbackCalls, service.ignoreCalls)
	}
}

func TestWPPluginBatchRollbackAndIgnoreCallService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPPluginBatchAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	handler := &WPPluginBatchHandler{Service: service}
	r.POST("/api/websites/:id/wp-plugin-batch/tasks/:task_id/rollback", handler.Rollback)
	r.POST("/api/websites/:id/wp-plugin-batch/tasks/:task_id/ignore", handler.Ignore)
	for _, path := range []string{
		"/api/websites/1/wp-plugin-batch/tasks/wpu_0123456789abcdef0123456789abcdef/rollback",
		"/api/websites/1/wp-plugin-batch/tasks/wpu_0123456789abcdef0123456789abcdef/ignore",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	if service.rollbackCalls != 1 || service.ignoreCalls != 1 {
		t.Fatalf("rollback=%d ignore=%d", service.rollbackCalls, service.ignoreCalls)
	}
}
