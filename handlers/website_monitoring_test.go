package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func TestSaveMonitoringReportsMissingWebsite(t *testing.T) {
	setupCacheHelperTestDB(t)
	router := gin.New()
	router.PUT("/api/websites/:id/monitoring", (&WebsiteHandler{}).SaveMonitoring)
	req := httptest.NewRequest(http.MethodPut, "/api/websites/999/monitoring", strings.NewReader(`{"enabled":true,"interval":5}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSaveMonitoringPersistsBeforeSuccess(t *testing.T) {
	setupCacheHelperTestDB(t)
	router := gin.New()
	router.PUT("/api/websites/:id/monitoring", (&WebsiteHandler{}).SaveMonitoring)
	req := httptest.NewRequest(http.MethodPut, "/api/websites/1/monitoring", strings.NewReader(`{"enabled":true,"interval":9}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var enabled, interval int
	if err := database.GetDB().QueryRow(`SELECT monitoring_enabled,monitoring_interval FROM websites WHERE id=1`).Scan(&enabled, &interval); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || interval != 9 {
		t.Fatalf("monitoring=(%d,%d)", enabled, interval)
	}
}
