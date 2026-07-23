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

type fakeWPPluginUpdateAPIService struct{ confirmCalls int }

func (f *fakeWPPluginUpdateAPIService) Preview(context.Context, int, string, string) (models.WPPluginUpdatePreview, error) {
	return models.WPPluginUpdatePreview{Available: true}, nil
}
func (f *fakeWPPluginUpdateAPIService) Confirm(context.Context, int, string, string, string, string, string) (models.WPPluginUpdateTask, error) {
	f.confirmCalls++
	return models.WPPluginUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", Status: "queued"}, nil
}
func (f *fakeWPPluginUpdateAPIService) Task(context.Context, int, string) (models.WPPluginUpdateTask, error) {
	return models.WPPluginUpdateTask{}, executor.ErrWPPluginUpdateNotFound
}
func (f *fakeWPPluginUpdateAPIService) LatestTask(context.Context, int, string) (models.WPPluginUpdateTask, error) {
	return models.WPPluginUpdateTask{}, executor.ErrWPPluginUpdateNotFound
}

func TestWPPluginUpdateConfirmRequiresCSRFBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPPluginUpdateAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() }, middleware.CSRF())
	r.POST("/api/websites/:id/wp-plugin-update/confirm", (&WPPluginUpdateHandler{Service: service}).Confirm)
	req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-plugin-update/confirm", strings.NewReader(
		`{"component_key":"sample/sample.php","confirmation_token":"token","target_version":"1.1.0","confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || service.confirmCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.confirmCalls, rec.Body.String())
	}
}

func TestWPPluginUpdateUnavailableServiceReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.GET("/api/websites/:id/wp-plugin-update/preview", (&WPPluginUpdateHandler{}).Preview)
	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/wp-plugin-update/preview?component_key=sample%2Fsample.php", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

type fakeWPPluginUpdateNotInRepositoryService struct{ fakeWPPluginUpdateAPIService }

func (f *fakeWPPluginUpdateNotInRepositoryService) Preview(context.Context, int, string, string) (models.WPPluginUpdatePreview, error) {
	return models.WPPluginUpdatePreview{}, executor.ErrWPPluginUpdateNotInRepository
}

func TestWPPluginUpdateNotInRepositoryReturns409WithSpecificMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.GET("/api/websites/:id/wp-plugin-update/preview", (&WPPluginUpdateHandler{Service: &fakeWPPluginUpdateNotInRepositoryService{}}).Preview)
	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/wp-plugin-update/preview?component_key=sample%2Fsample.php", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WordPress.org") {
		t.Fatalf("body should explain the plugin is not in the official repository: %s", rec.Body.String())
	}
}

func TestWPPluginUpdateConfirmRejectsUnknownJSONAndAcceptsExactBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPPluginUpdateAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.POST("/api/websites/:id/wp-plugin-update/confirm", (&WPPluginUpdateHandler{Service: service}).Confirm)
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"component_key":"sample/sample.php","confirmation_token":"token","target_version":"1.1.0","confirm":true,"download_url":"https://evil.example/x.zip"}`, http.StatusBadRequest},
		{`{"component_key":"sample/sample.php","confirmation_token":"token","target_version":"1.1.0","confirm":true}`, http.StatusAccepted},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-plugin-update/confirm", strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("body=%s status=%d want=%d response=%s", test.body, rec.Code, test.status, rec.Body.String())
		}
	}
	if service.confirmCalls != 1 {
		t.Fatalf("confirm calls=%d", service.confirmCalls)
	}
}
