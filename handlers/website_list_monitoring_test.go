package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func TestWebsiteListReturnsSeparateMonitoringStates(t *testing.T) {
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
		database.DB = oldDB
	})

	insertSite := func(domain, siteType string, monitoringEnabled int) int {
		t.Helper()
		result, err := database.GetDB().Exec(`INSERT INTO websites (
			name, domain, status, system_user, web_root, log_dir, db_name, db_user,
			php_pool_path, nginx_conf_path, site_type, monitoring_enabled
		) VALUES (?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			domain, domain, "user_"+domain, "/www/"+domain, "/logs/"+domain,
			"db_"+domain, "dbuser_"+domain, "/php/"+domain, "/nginx/"+domain,
			siteType, monitoringEnabled)
		if err != nil {
			t.Fatalf("insert %s: %v", domain, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("site id %s: %v", domain, err)
		}
		return int(id)
	}

	wpEnabledID := insertSite("wp-enabled.example", "wordpress", 1)
	wpDisabledID := insertSite("wp-disabled.example", "wordpress", 0)
	phpID := insertSite("php.example", "php", 1)
	if _, err := database.GetDB().Exec(`INSERT INTO site_wp_anomaly_state(site_id, enabled) VALUES (?, 1), (?, 0), (?, 1)`, wpEnabledID, wpDisabledID, phpID); err != nil {
		t.Fatalf("insert anomaly states: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/websites", (&WebsiteHandler{}).List)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/websites", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Success bool `json:"success"`
		Data    []struct {
			ID                          int  `json:"id"`
			MonitoringEnabled           bool `json:"monitoring_enabled"`
			AnomalyMonitoringEnabled    bool `json:"anomaly_monitoring_enabled"`
			AnomalyMonitoringApplicable bool `json:"anomaly_monitoring_applicable"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Success || len(response.Data) != 3 {
		t.Fatalf("unexpected response: %+v", response)
	}

	byID := make(map[int]struct {
		monitoring bool
		anomaly    bool
		applicable bool
	})
	for _, site := range response.Data {
		byID[site.ID] = struct {
			monitoring bool
			anomaly    bool
			applicable bool
		}{site.MonitoringEnabled, site.AnomalyMonitoringEnabled, site.AnomalyMonitoringApplicable}
	}
	if got := byID[wpEnabledID]; got != (struct {
		monitoring bool
		anomaly    bool
		applicable bool
	}{true, true, true}) {
		t.Fatalf("enabled WordPress states = %+v", got)
	}
	if got := byID[wpDisabledID]; got != (struct {
		monitoring bool
		anomaly    bool
		applicable bool
	}{false, false, true}) {
		t.Fatalf("disabled WordPress states = %+v", got)
	}
	if got := byID[phpID]; got != (struct {
		monitoring bool
		anomaly    bool
		applicable bool
	}{true, true, false}) {
		t.Fatalf("PHP states = %+v", got)
	}
}
