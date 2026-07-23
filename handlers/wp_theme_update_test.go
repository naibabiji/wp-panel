package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

type fakeWPThemeUpdateAPIService struct{ confirmCalls int }

func (f *fakeWPThemeUpdateAPIService) Preview(context.Context, int, string, string) (models.WPThemeUpdatePreview, error) {
	return models.WPThemeUpdatePreview{Available: true}, nil
}
func (f *fakeWPThemeUpdateAPIService) Confirm(context.Context, int, string, string, string, string, string, string) (models.WPThemeUpdateTask, error) {
	f.confirmCalls++
	return models.WPThemeUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", Status: "queued"}, nil
}
func (f *fakeWPThemeUpdateAPIService) Task(context.Context, int, string) (models.WPThemeUpdateTask, error) {
	return models.WPThemeUpdateTask{}, executor.ErrWPThemeUpdateNotFound
}
func (f *fakeWPThemeUpdateAPIService) LatestTask(context.Context, int, string) (models.WPThemeUpdateTask, error) {
	return models.WPThemeUpdateTask{}, executor.ErrWPThemeUpdateNotFound
}

func TestWPThemeUpdateConfirmRejectsUnknownJSONAndAcceptsRiskToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeWPThemeUpdateAPIService{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.POST("/api/websites/:id/wp-theme-update/confirm", (&WPThemeUpdateHandler{Service: service}).Confirm)
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"component_key":"sample-theme","confirmation_token":"token","risk_token":"risk","target_version":"1.1.0","confirm":true,"download_url":"https://evil.example/x.zip"}`, http.StatusBadRequest},
		{`{"component_key":"sample-theme","confirmation_token":"token","risk_token":"risk","target_version":"1.1.0","confirm":true}`, http.StatusAccepted},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/websites/1/wp-theme-update/confirm", strings.NewReader(test.body))
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

func TestWPThemeUpdateUnavailableServiceReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("session_username", "admin"); c.Next() })
	r.GET("/api/websites/:id/wp-theme-update/preview", (&WPThemeUpdateHandler{}).Preview)
	req := httptest.NewRequest(http.MethodGet, "/api/websites/1/wp-theme-update/preview?component_key=sample-theme", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}
