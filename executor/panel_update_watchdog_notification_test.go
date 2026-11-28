package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func TestWatchdogRollbackPersistsTerminalWithoutDatabase(t *testing.T) {
	old := database.DB
	database.DB = nil
	t.Cleanup(func() { database.DB = old })

	cfg := &config.Config{}
	cfg.Panel.DataDir = t.TempDir()
	plan := rollbackPlan{CreatedAt: "test-time", TargetVersion: "v2.0.0", Trigger: "auto"}
	for _, stage := range []string{"rollback_database", "rollback_health", "rolled_back"} {
		plan.CreatedAt += "/" + stage
		reportWatchdogRollback(cfg, plan, stage, "test result", nil)
		filename := notificationTarget(plan.CreatedAt+"\x00"+plan.TargetVersion) + ".json"
		path := filepath.Join(cfg.Panel.DataDir, "update-watchdog-results", filename)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result watchdogRollbackResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		want := "需要人工处理"
		if stage == "rolled_back" {
			want = "旧版已恢复"
		}
		if result.Stage != stage || !strings.Contains(result.Message, want) || result.Submitted || !result.LastAttempt.IsZero() {
			t.Fatalf("unexpected terminal result: %+v", result)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("terminal file protection: %v, %v", info, err)
		}
	}
}

func TestWatchdogRollbackUsesReservedQuotaWithDatabaseUnavailable(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT, updated_at TEXT)`)
	mustExec(t, db, `INSERT INTO security_settings (skey,svalue) VALUES
		('smtp_host','smtp.example.test'),('smtp_user','sender@example.test'),('smtp_pass','test'),
		('admin_email','admin@example.test'),('managed_mail_system_day_limit','1'),
		('managed_mail_quota_notice_state','')`)
	reservation := reserveWatchdogMail()
	if reservation == nil || reserveWatchdogMail() != nil {
		t.Fatal("offline delivery must consume a slot from the existing daily quota")
	}
	database.DB = nil
	t.Cleanup(func() { database.DB = db })
	previousSender := watchdogSendReservedMail
	t.Cleanup(func() { watchdogSendReservedMail = previousSender })
	count := 0
	watchdogSendReservedMail = func(smtp *SMTPConfig, _, _, body string, protectSystemQuota bool) error {
		if protectSystemQuota || smtp.AdminEmail != "admin@example.test" || !strings.Contains(body, "回退失败") {
			t.Fatalf("unexpected reserved submission: recipient=%q quota=%t body=%s", smtp.AdminEmail, protectSystemQuota, body)
		}
		count++
		return nil
	}
	cfg := &config.Config{}
	cfg.Panel.DataDir = t.TempDir()
	plan := rollbackPlan{CreatedAt: "offline", TargetVersion: "v2.0.0", Trigger: "auto"}
	reportWatchdogRollback(cfg, plan, "rollback_health", "failed", reservation)
	reportWatchdogRollback(cfg, plan, "rollback_health", "failed", reservation)
	if count != 1 {
		t.Fatalf("expected one reserved delivery, got %d", count)
	}
}
