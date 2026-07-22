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

type fakeWPCoreUpdateAPIService struct{ confirmCalls int }

func (f *fakeWPCoreUpdateAPIService) Preview(context.Context, int, string) (models.WPCoreUpdatePreview, error) {
	return models.WPCoreUpdatePreview{Available: true}, nil
}
func (f *fakeWPCoreUpdateAPIService) Confirm(context.Context, int, string, string, string) (models.WPCoreUpdateTask, error) {
	f.confirmCalls++
	return models.WPCoreUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", Status: "queued"}, nil
}
func (f *fakeWPCoreUpdateAPIService) Task(context.Context, int, string) (models.WPCoreUpdateTask, error) {
	return models.WPCoreUpdateTask{}, executor.ErrWPCoreUpdateNotFound
}

func TestWPCoreUpdateConfirmRequiresCSRFBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPCoreUpdateAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() }, middleware.CSRF())
	r.POST("/api/websites/:id/wp-core-update/confirm", (&WPCoreUpdateHandler{Service: service}).Confirm)
	req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-core-update/confirm", strings.NewReader(`{"confirmation_token":"token","target_version":"7.0.2","confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || service.confirmCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, service.confirmCalls, rec.Body.String())
	}
}

func TestWPCoreUpdateUnavailableServiceReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.GET("/api/websites/:id/wp-core-update/preview", (&WPCoreUpdateHandler{}).Preview)
	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/wp-core-update/preview", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestWPCoreUpdateConfirmRejectsUnknownJSONAndAcceptsExactBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPCoreUpdateAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.POST("/api/websites/:id/wp-core-update/confirm", (&WPCoreUpdateHandler{Service: service}).Confirm)
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"confirmation_token":"token","target_version":"7.0.2","confirm":true,"download_url":"https://evil.example/x.zip"}`, http.StatusBadRequest},
		{`{"confirmation_token":"token","target_version":"7.0.2","confirm":true}`, http.StatusAccepted},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-core-update/confirm", strings.NewReader(test.body))
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
