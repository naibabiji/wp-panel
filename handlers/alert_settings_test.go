package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func saveAlertSettings(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/alert/settings", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	new(AlertHandler).SaveSettings(ctx)
	return recorder
}

func TestSaveAlertSettingsValidatesEverythingBeforeWriting(t *testing.T) {
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO security_settings(skey,svalue) VALUES('alert_cpu','false')
		ON CONFLICT(skey) DO UPDATE SET svalue=excluded.svalue`); err != nil {
		t.Fatal(err)
	}
	recorder := saveAlertSettings(t, `{"alert_cpu":"true","alert_wp_security_window_hours":"invalid"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var actual string
	if err := db.QueryRow(`SELECT svalue FROM security_settings WHERE skey='alert_cpu'`).Scan(&actual); err != nil || actual != "false" {
		t.Fatalf("alert_cpu=%q err=%v, want unchanged false", actual, err)
	}
}

func TestSaveAlertSettingsRollsBackOnWriteFailure(t *testing.T) {
	setupBackupOverviewTestDB(t)
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO security_settings(skey,svalue) VALUES('alert_cpu','false')
		ON CONFLICT(skey) DO UPDATE SET svalue=excluded.svalue`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_alert_memory BEFORE INSERT ON security_settings
		WHEN NEW.skey='alert_memory' BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	recorder := saveAlertSettings(t, `{"alert_cpu":"true","alert_memory":"true"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var actual string
	if err := db.QueryRow(`SELECT svalue FROM security_settings WHERE skey='alert_cpu'`).Scan(&actual); err != nil || actual != "false" {
		t.Fatalf("alert_cpu=%q err=%v, want rolled back false", actual, err)
	}
}

func TestSaveAlertSettingsCommitsWholeRequest(t *testing.T) {
	setupBackupOverviewTestDB(t)
	recorder := saveAlertSettings(t, `{
		"smtp_host":"smtp.example.com",
		"smtp_port":"587",
		"smtp_encryption":"starttls",
		"admin_email":"admin@example.com",
		"webhook_channel":"wecom",
		"webhook_url":"https://hooks.example.com/alert",
		"alert_cpu":"true"
	}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM security_settings WHERE skey IN
		('smtp_host','smtp_port','smtp_encryption','admin_email','webhook_channel','webhook_url','alert_cpu')`).Scan(&count); err != nil || count != 7 {
		t.Fatalf("saved count=%d err=%v", count, err)
	}
}
