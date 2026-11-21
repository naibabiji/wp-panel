package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/i18n"
)

func TestMaintenanceMigrationNotificationTracksSuccessfulChannelsOnly(t *testing.T) {
	_, f := setupMaintenanceRunner(t)
	db := database.GetDB()
	setSecuritySetting("alert_system_maintenance", "true")
	if _, err := db.Exec(`INSERT INTO system_maintenance_actions(kind,target,message_code) VALUES('notice','migration','maintenance_migrated')`); err != nil {
		t.Fatal(err)
	}
	// Unconfigured channels must not be marked delivered. Local event logging
	// is idempotent and does not stop maintenance.
	if err := notifyMaintenanceActions(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := notifyMaintenanceActions(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var marker string
	db.QueryRow(`SELECT notified_status FROM system_maintenance_actions WHERE kind='notice'`).Scan(&marker)
	if strings.Contains(marker, "mail,") || strings.Contains(marker, "webhook,") {
		t.Fatalf("undelivered channel marked: %s", marker)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_system_maintenance'`).Scan(&count)
	if count != 1 {
		t.Fatalf("local notice duplicated: %d", count)
	}
	for key, value := range map[string]string{"smtp_host": "mail.example.test", "smtp_user": "test@example.test", "smtp_pass": "fixture", "smtp_from_email": "test@example.test", "admin_email": "admin@example.test", "webhook_url": "https://example.test/hook"} {
		setSecuritySetting(key, value)
	}
	oldMail, oldWebhook := maintenanceSendMail, maintenanceSendWebhook
	t.Cleanup(func() { maintenanceSendMail = oldMail; maintenanceSendWebhook = oldWebhook })
	mailCalls, webhookCalls := 0, 0
	maintenanceSendMail = func(string, string, string) error {
		mailCalls++
		if mailCalls == 1 {
			return errors.New("temporary failure")
		}
		return nil
	}
	maintenanceSendWebhook = func(string, string) error { webhookCalls++; return nil }
	if err := notifyMaintenanceActions(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT notified_status FROM system_maintenance_actions WHERE kind='notice'`).Scan(&marker)
	if strings.Contains(marker, "mail,") || !strings.Contains(marker, "webhook,") {
		t.Fatalf("incorrect delivery result: %s", marker)
	}
	if err := notifyMaintenanceActions(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if mailCalls != 1 {
		t.Fatal("failed channel ignored notification backoff")
	}
	f.now = f.now.Add(time.Hour)
	if err := notifyMaintenanceActions(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := notifyMaintenanceActions(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if mailCalls != 2 || webhookCalls != 1 {
		t.Fatalf("successful channel resent: mail=%d webhook=%d", mailCalls, webhookCalls)
	}
}

func TestMaintenanceFailureReasonsAreTranslatedInBothLanguagesAndNotices(t *testing.T) {
	codes := []string{"service_verification_failed", "reboot_request_failed", "reboot_not_observed", "post_reboot_health_failed", "verification_failed", "interrupted", "installation_interrupted", "installation_failed", "authorization_revoked", "outside_window", "timezone_unknown", "runtime_unknown"}
	for _, code := range codes {
		key := SystemMaintenanceReasonKey(code)
		for _, lang := range []string{i18n.DefaultLang, i18n.English} {
			if got := i18n.T(lang, key); got == key || key == "settings.maintenance_reason_attention" {
				t.Fatalf("missing failure reason: %s %s", lang, code)
			}
		}
	}
	_, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	a, ok, err := claimMaintenanceAction(database.GetDB(), "service", e.Unit, []maintenanceRuntimeEvidence{e}, f.boot, f.now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := finishMaintenanceAction(database.GetDB(), a, "failed", "service_verification_failed", f.now); err != nil {
		t.Fatal(err)
	}
	setSecuritySetting("alert_system_maintenance", "true")
	if err := notifyMaintenanceActions(context.Background(), database.GetDB()); err != nil {
		t.Fatal(err)
	}
	var message string
	if err := database.GetDB().QueryRow(`SELECT message FROM alert_log WHERE alert_type='alert_system_maintenance' ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message, "service_verification_failed") || !strings.Contains(message, i18n.T(i18n.DefaultLang, SystemMaintenanceReasonKey("service_verification_failed"))) {
		t.Fatalf("untranslated notice: %s", message)
	}
}
