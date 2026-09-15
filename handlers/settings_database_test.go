package handlers

import (
	"net/http"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestUpdateSettingsSavesDatabaseOptionsTogether(t *testing.T) {
	setupBackupOverviewTestDB(t)
	recorder := updateSystemSetting(t, `{
		"panel_title":"Test Panel",
		"github_proxy":"https://proxy.example.com/",
		"panel_auto_update_enabled":"true",
		"panel_auto_update_mode":"all_stable",
		"panel_auto_update_window":"02:00-04:00",
		"panel_auto_update_release_delay_minutes":"30",
		"panel_auto_update_signature_timeout_minutes":"90",
		"wp_package_auto_check_enabled":"false"
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	want := map[string]string{
		"panel_title":                                 "Test Panel",
		"github_proxy":                                "https://proxy.example.com",
		"panel_auto_update_enabled":                   "true",
		"panel_auto_update_mode":                      "all_stable",
		"panel_auto_update_window":                    "02:00-04:00",
		"panel_auto_update_release_delay_minutes":     "30",
		"panel_auto_update_signature_timeout_minutes": "90",
		"wp_package_auto_check_enabled":               "false",
	}
	for key, expected := range want {
		var actual string
		if err := database.GetDB().QueryRow(`SELECT svalue FROM security_settings WHERE skey=?`, key).Scan(&actual); err != nil || actual != expected {
			t.Fatalf("setting %s=%q err=%v, want %q", key, actual, err, expected)
		}
	}
}

func TestUpdateSettingsValidatesAllDatabaseOptionsBeforeSaving(t *testing.T) {
	setupBackupOverviewTestDB(t)
	if _, err := database.GetDB().Exec(`INSERT INTO security_settings(skey,svalue) VALUES('panel_auto_update_enabled','false')
		ON CONFLICT(skey) DO UPDATE SET svalue=excluded.svalue`); err != nil {
		t.Fatal(err)
	}
	recorder := updateSystemSetting(t, `{
		"panel_auto_update_enabled":"true",
		"panel_auto_update_signature_timeout_minutes":"invalid"
	}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var actual string
	if err := database.GetDB().QueryRow(`SELECT svalue FROM security_settings WHERE skey='panel_auto_update_enabled'`).Scan(&actual); err != nil || actual != "false" {
		t.Fatalf("enabled=%q err=%v, want unchanged false", actual, err)
	}
}

func TestUpdateSettingsRejectsInvalidClockWindow(t *testing.T) {
	setupBackupOverviewTestDB(t)
	recorder := updateSystemSetting(t, `{"panel_auto_update_window":"25:70-30:99"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateSettingsReportsDatabaseWriteFailure(t *testing.T) {
	setupBackupOverviewTestDB(t)
	if err := database.GetDB().Close(); err != nil {
		t.Fatal(err)
	}
	recorder := updateSystemSetting(t, `{"panel_title":"Must Not Report Success"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSaveDatabaseSettingsRollsBackWholeGroup(t *testing.T) {
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO security_settings(skey,svalue) VALUES('panel_title','Before')
		ON CONFLICT(skey) DO UPDATE SET svalue=excluded.svalue`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_proxy_setting BEFORE INSERT ON security_settings
		WHEN NEW.skey='github_proxy' BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := saveSecuritySettingsTransaction(db, map[string]string{
		"panel_title":  "After",
		"github_proxy": "https://proxy.example.com",
	})
	if err == nil {
		t.Fatal("saveSecuritySettingsTransaction succeeded, want injected failure")
	}
	var title string
	if err := db.QueryRow(`SELECT svalue FROM security_settings WHERE skey='panel_title'`).Scan(&title); err != nil || title != "Before" {
		t.Fatalf("panel title=%q err=%v, want rolled back", title, err)
	}
}
