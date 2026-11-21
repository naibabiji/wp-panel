package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

func TestSystemMaintenanceSettingsSynchronizeLegacyAndValidateWindow(t *testing.T) {
	setupBackupOverviewTestDB(t)
	r := updateSystemSetting(t, `{"system_maintenance_enabled":"true","system_maintenance_mode":"immediate","system_maintenance_window":"22:00-01:00"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", r.Code, r.Body.String())
	}
	p, err := executor.ReadSystemMaintenancePolicy(database.GetDB())
	if err != nil || !p.Enabled || p.Mode != "immediate" {
		t.Fatalf("policy=%+v %v", p, err)
	}
	r = updateSystemSetting(t, `{"system_package_auto_update_enabled":"false"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("legacy disable: %d %s", r.Code, r.Body.String())
	}
	p, err = executor.ReadSystemMaintenancePolicy(database.GetDB())
	if err != nil || p.Enabled {
		t.Fatalf("legacy disable did not stop maintenance: %+v %v", p, err)
	}
	r = updateSystemSetting(t, `{"system_maintenance_enabled":"true","system_maintenance_window":"04:00-04:30"}`)
	if r.Code != http.StatusBadRequest {
		t.Fatalf("short window accepted: %d", r.Code)
	}
	p, _ = executor.ReadSystemMaintenancePolicy(database.GetDB())
	if p.Enabled {
		t.Fatal("invalid request partly saved policy")
	}
}

func TestSystemMaintenanceRetryRejectsDisabledDuplicateAndActiveRequest(t *testing.T) {
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	result, err := db.Exec(`INSERT INTO system_maintenance_actions(kind,target,status,attempts) VALUES('service','nginx.service','failed',1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	call := func() int {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/system/maintenance/retry", strings.NewReader(fmt.Sprintf(`{"id":%d}`, id)))
		c.Request.Header.Set("Content-Type", "application/json")
		(&SystemUpdateHandler{}).RetryMaintenance(c)
		return rec.Code
	}
	if code := call(); code != http.StatusConflict {
		t.Fatalf("disabled retry: %d", code)
	}
	if err := executor.SaveSystemMaintenancePolicy(db, executor.SystemMaintenancePolicy{Enabled: true, Mode: "scheduled", Window: "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	if code := call(); code != http.StatusAccepted {
		t.Fatalf("enabled retry: %d", code)
	}
	if code := call(); code != http.StatusConflict {
		t.Fatalf("duplicate retry: %d", code)
	}
	if _, err := db.Exec(`UPDATE system_maintenance_actions SET manual_retry=0,status='requested' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if code := call(); code != http.StatusConflict {
		t.Fatalf("requested retry: %d", code)
	}
}
