package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func maintenanceAlertFixture(t *testing.T) (*maintenanceRunnerFixture, *maintenanceAlertMonitor, *[]string, *[]string) {
	t.Helper()
	_, f := setupMaintenanceRunner(t)
	setSecuritySetting(maintenanceAlertKey, "true")
	setSecuritySetting("alert_notification_mode", "detailed")
	for key, value := range map[string]string{
		"smtp_host": "mail.example.test", "smtp_user": "test@example.test",
		"smtp_pass": "fixture", "admin_email": "admin@example.test",
		"webhook_url": "https://example.test/hook",
	} {
		setSecuritySetting(key, value)
	}
	oldMail, oldWebhook := maintenanceSendMail, maintenanceSendWebhook
	t.Cleanup(func() { maintenanceSendMail, maintenanceSendWebhook = oldMail, oldWebhook })
	var mail, webhook []string
	maintenanceSendMail = func(_, title, body string) error { mail = append(mail, title+" "+body); return nil }
	maintenanceSendWebhook = func(_ *WebhookConfig, title, body string) error {
		webhook = append(webhook, title+" "+body)
		return nil
	}
	return f, &maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}, &mail, &webhook
}

func TestMaintenanceAlertReplaysWindowAndRequiresFreshRecovery(t *testing.T) {
	f, monitor, mail, webhook := maintenanceAlertFixture(t)
	location, err := currentMaintenanceLocation()
	if err != nil {
		t.Fatal(err)
	}
	f.now = time.Date(2026, 11, 23, 23, 8, 0, 0, location)
	first := f.now
	db := database.GetDB()
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{Enabled: true, Mode: "scheduled", Window: "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	scanUnknown := func() {
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{
			At: f.now, UnknownUnits: map[string]bool{"php8.3-fpm.service": true},
			UnknownReasons: map[string]string{"php8.3-fpm.service": "mapping_path_missing"},
		}, nil)
	}
	check := func(want int) {
		t.Helper()
		if err := monitor.run(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		if len(*mail) != want || len(*webhook) != want {
			t.Fatalf("%s: mail=%d webhook=%d want=%d", f.now, len(*mail), len(*webhook), want)
		}
	}
	scanUnknown()
	check(0)
	f.now = first.Add(5 * time.Minute)
	check(0) // Same snapshot.
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, nil)
	check(0)
	f.now = first.Add(10 * time.Minute)
	scanUnknown()
	check(1)
	if !strings.Contains((*webhook)[0], "php8.3-fpm.service") || !strings.Contains((*webhook)[0], "文件映射") {
		t.Fatal((*webhook)[0])
	}
	for f.now = first.Add(40 * time.Minute); f.now.Before(first.Add(7 * time.Hour)); f.now = f.now.Add(30 * time.Minute) {
		scanUnknown()
		check(1)
	}
	f.now = time.Date(2026, 11, 24, 6, 29, 0, 0, location)
	scanUnknown()
	check(1)
	f.now = f.now.Add(time.Minute)
	check(1) // Window grace elapsed, but the sample predates it.
	scanUnknown()
	check(2)
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, nil)
	f.now = f.now.Add(24 * time.Hour)
	check(2) // Busy never closes an active alert or advances the reminder.
	monitor = &maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}
	loadAlertRuntimeState([]*alertRule{&monitor.rule})
	check(2)
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now}, nil)
	check(3)
	check(3)
	if monitor.rule.firing || !strings.Contains((*webhook)[2], "告警已解除") {
		t.Fatalf("recovery: %v", *webhook)
	}
}

func TestMaintenanceActionOwnsFailedChannelRetry(t *testing.T) {
	f, monitor, mail, webhook := maintenanceAlertFixture(t)
	db := database.GetDB()
	ctx := context.Background()
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now}, nil)
	e := maintenanceTestEvidence(1)
	action, ok, err := claimMaintenanceAction(db, "service", e.Unit, []maintenanceRuntimeEvidence{e}, f.boot, f.now)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := finishMaintenanceAction(db, action, "failed", "service_verification_failed", f.now); err != nil {
		t.Fatal(err)
	}
	sendMail := maintenanceSendMail
	attempts := 0
	maintenanceSendMail = func(to, title, body string) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary failure")
		}
		return sendMail(to, title, body)
	}
	for _, elapsed := range []time.Duration{0, 30 * time.Minute, 30 * time.Minute, time.Minute} {
		f.now = f.now.Add(elapsed)
		if err := monitor.run(ctx, db); err != nil {
			t.Fatal(err)
		}
		if len(*webhook) != 1 {
			t.Fatalf("duplicated webhook: %v", *webhook)
		}
	}
	if attempts != 2 || len(*mail) != 1 || !monitor.rule.firing {
		t.Fatalf("attempts=%d mail=%v firing=%v", attempts, *mail, monitor.rule.firing)
	}
}

func TestMaintenanceImmediateReminderAndUrgentEscalation(t *testing.T) {
	f, monitor, _, webhook := maintenanceAlertFixture(t)
	db := database.GetDB()
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{Enabled: true, Mode: "immediate", Window: "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	e := maintenanceTestEvidence(1)
	scan := func(unit string, want int) {
		t.Helper()
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now, UnknownUnits: map[string]bool{unit: true}}, nil)
		if err := monitor.run(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		if len(*webhook) != want {
			t.Fatalf("%s: %v, want %d notices", f.now, *webhook, want)
		}
	}
	scan(e.Unit, 0)
	f.now = f.now.Add(10 * time.Minute)
	scan(e.Unit, 1)
	f.now = f.now.Add(6*time.Hour - 11*time.Minute)
	scan(e.Unit, 1)
	f.now = f.now.Add(time.Minute)
	scan(e.Unit, 2)
	f.now = f.now.Add(24*time.Hour - time.Minute)
	scan(e.Unit, 2)
	f.now = f.now.Add(time.Minute)
	scan(e.Unit, 2)
	f.now = f.now.Add(time.Minute)
	scan("kernel_or_marker", 3)
	scan("kernel_or_marker", 3)
	if !strings.Contains((*webhook)[2], "内核或重启标记") {
		t.Fatal((*webhook)[2])
	}
}

func TestMaintenanceUnchangedUrgentEvidenceDoesNotResend(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit string
		err  error
	}{
		{name: "kernel", unit: "kernel_or_marker"},
		{name: "budget", unit: "action_budget"},
		{name: "failed_check", err: errors.New("fixture scan failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, monitor, mail, webhook := maintenanceAlertFixture(t)
			db := database.GetDB()
			sample := func() {
				recordMaintenanceInspection(maintenanceRuntimeSnapshot{
					At: f.now, UnknownUnits: map[string]bool{tc.unit: tc.unit != ""},
				}, tc.err)
			}
			check := func(want int) {
				t.Helper()
				if err := monitor.run(context.Background(), db); err != nil {
					t.Fatal(err)
				}
				if len(*mail) != want || len(*webhook) != want || !monitor.rule.firing {
					t.Fatalf("%s: mail=%v webhook=%v firing=%t, want %d notices per channel", f.now, *mail, *webhook, monitor.rule.firing, want)
				}
			}
			sample()
			check(1)
			f.now = f.now.Add(24*time.Hour - time.Minute)
			sample()
			check(1)
			f.now = f.now.Add(time.Minute)
			check(1) // The saved observation predates the reminder deadline.
			monitor = &maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}
			loadAlertRuntimeState([]*alertRule{&monitor.rule})
			recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, nil)
			check(1) // No new evidence while busy, even after restart.
			sample()
			check(1)
			f.now = f.now.Add(24 * time.Hour)
			sample()
			check(1)
		})
	}
}

func TestMaintenanceUnchangedReadFailureDoesNotResend(t *testing.T) {
	f, monitor, _, webhook := maintenanceAlertFixture(t)
	db := database.GetDB()
	readErr := errors.New("fixture read failure")
	for _, step := range []struct {
		elapsed time.Duration
		want    int
	}{{0, 1}, {23 * time.Hour, 1}, {time.Hour, 1}, {0, 1}} {
		f.now = f.now.Add(step.elapsed)
		_, err := monitor.evaluate(db, nil, SystemMaintenanceInspection{Status: "not_checked"},
			SystemMaintenancePolicy{}, nil, f.now, readErr)
		if err != nil {
			t.Fatal(err)
		}
		if len(*webhook) != step.want {
			t.Fatalf("%s: want %d notices, got %v", f.now, step.want, *webhook)
		}
	}
}

func TestMaintenanceRoundRecoveryRequiresPostActionSample(t *testing.T) {
	f, monitor, mail, webhook := maintenanceAlertFixture(t)
	db := database.GetDB()
	ctx := context.Background()
	e := maintenanceTestEvidence(1)
	run := func() {
		t.Helper()
		if err := monitor.run(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now, UnknownUnits: map[string]bool{e.Unit: true}}, nil)
		run()
		f.now = f.now.Add(10 * time.Minute)
	}
	if len(*mail) != 1 {
		t.Fatalf("ordinary first notice: %v", *mail)
	}
	round, err := createMaintenanceRound(db, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	child, ok, err := claimMaintenanceAction(db, "service", e.Unit, []maintenanceRuntimeEvidence{e}, f.boot, f.now)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := finishMaintenanceAction(db, child, "succeeded", "completed", f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE system_maintenance_actions SET round_id=? WHERE id=?`, round.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE system_maintenance_actions SET status='succeeded',message_code='completed' WHERE id=?`, round.ID); err != nil {
		t.Fatal(err)
	}
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now}, nil)
	run()
	if len(*mail) != 1 || !monitor.rule.firing {
		t.Fatal("round used a stale check")
	}
	f.now = f.now.Add(time.Minute)
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now}, nil)
	run()
	run()
	if len(*mail) != 3 || len(*webhook) != 3 || !strings.Contains((*webhook)[1], "告警已解除") {
		t.Fatalf("expected one recovery and a detailed round result: %v", *webhook)
	}
	f.now = f.now.Add(time.Minute)
	routine, err := createMaintenanceRound(db, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE system_maintenance_actions SET status='succeeded',message_code='completed' WHERE id=?`, routine.ID); err != nil {
		t.Fatal(err)
	}
	run()
	if len(*webhook) != 4 || strings.Contains((*webhook)[3], "告警已解除") {
		t.Fatalf("routine success notice: %v", *webhook)
	}
}

func TestMaintenanceUnknownDeadlineWithoutRoundSurvivesRestart(t *testing.T) {
	f, monitor, mail, webhook := maintenanceAlertFixture(t)
	setSecuritySetting("alert_notification_mode", "actionable")
	db := database.GetDB()
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{Enabled: true, Mode: "scheduled", Window: "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	location, err := currentMaintenanceLocation()
	if err != nil {
		t.Fatal(err)
	}
	f.now = time.Date(2026, 11, 23, 7, 0, 0, 0, location)
	first := f.now
	run := func() {
		t.Helper()
		if err := monitor.run(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	unknown := maintenanceRuntimeSnapshot{At: first, UnknownUnits: map[string]bool{"cron.service": true}}
	recordMaintenanceInspection(unknown, nil)
	run()
	monitor = &maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}
	loadAlertRuntimeState([]*alertRule{&monitor.rule})
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, nil)
	f.now = time.Date(2026, 11, 26, 5, 59, 0, 0, location)
	run()
	if len(*mail) != 0 {
		t.Fatalf("reported before the deadline: %v", *mail)
	}
	f.now = f.now.Add(time.Minute)
	run()
	if len(*mail) != 1 || !strings.Contains((*mail)[0], "复查长期未完成") {
		t.Fatalf("deadline mail: %v", *mail)
	}
	f.now = f.now.Add(time.Minute)
	setSecuritySetting("admin_email", "new-admin@example.test")
	unknown.At = f.now
	recordMaintenanceInspection(unknown, nil)
	run()
	f.now = f.now.Add(time.Minute)
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now}, nil)
	run()
	if len(*mail) != 1 || len(*webhook) != 2 || !strings.Contains((*webhook)[1], "已解除") {
		t.Fatalf("recovery must go to the original target only: mail=%v webhook=%v", *mail, *webhook)
	}
}

func TestMaintenanceRecoveryRetriesOnlyFailedTargetAfterRestart(t *testing.T) {
	f, monitor, mail, webhook := maintenanceAlertFixture(t)
	db := database.GetDB()
	run := func() {
		t.Helper()
		if err := monitor.run(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{
			At: f.now, UnknownUnits: map[string]bool{"cron.service": true},
		}, nil)
		run()
		f.now = f.now.Add(10 * time.Minute)
	}
	send := maintenanceSendMail
	maintenanceSendMail = func(to, title, body string) error {
		return errors.New("temporary recovery failure")
	}
	recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now}, nil)
	run()
	if len(*mail) != 1 || len(*webhook) != 2 || monitor.rule.firing {
		t.Fatalf("unexpected recovery state: mail=%v webhook=%v", *mail, *webhook)
	}
	maintenanceSendMail = send
	f.now = f.now.Add(time.Hour)
	monitor = &maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}
	loadAlertRuntimeState([]*alertRule{&monitor.rule})
	run()
	run()
	if len(*mail) != 2 || len(*webhook) != 2 || !strings.Contains((*mail)[1], "已解除") {
		t.Fatalf("retry should only submit the failed recovery channel: mail=%v webhook=%v", *mail, *webhook)
	}
}

func TestMaintenanceUnknownDeadlineWithStaleSnapshot(t *testing.T) {
	f, monitor, mail, _ := maintenanceAlertFixture(t)
	setSecuritySetting("alert_notification_mode", "actionable")
	db := database.GetDB()
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{Enabled: true, Mode: "immediate", Window: "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	first := f.now
	run := func() {
		t.Helper()
		if err := monitor.run(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{
			At: f.now, UnknownUnits: map[string]bool{"cron.service": true},
		}, nil)
		run()
		f.now = f.now.Add(10 * time.Minute)
	}
	// An early APT error leaves the last sample intact rather than clearing it.
	f.now = first.Add(6*time.Hour - time.Second)
	run()
	if len(*mail) != 0 {
		t.Fatalf("notified before the deadline: %v", *mail)
	}
	monitor = &maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}
	loadAlertRuntimeState([]*alertRule{&monitor.rule})
	f.now = first.Add(6 * time.Hour)
	run()
	run()
	if len(*mail) != 1 || !strings.Contains((*mail)[0], "复查长期未完成") {
		t.Fatalf("stale sample must trigger one deadline notification: %v", *mail)
	}
}

func TestMaintenanceIndependentlyConfirmedProblemsCanNotify(t *testing.T) {
	for _, mode := range []string{"actionable", "detailed"} {
		t.Run(mode, func(t *testing.T) {
			f, monitor, mail, _ := maintenanceAlertFixture(t)
			setSecuritySetting("alert_notification_mode", mode)
			db := database.GetDB()
			if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{Enabled: true, Mode: "immediate", Window: "04:00-06:00"}); err != nil {
				t.Fatal(err)
			}
			scan := func(cronReason string, extra map[string]string) {
				t.Helper()
				units := map[string]bool{"cron.service": true}
				reasons := map[string]string{"cron.service": cronReason}
				for unit, reason := range extra {
					units[unit], reasons[unit] = true, reason
				}
				recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now, UnknownUnits: units, UnknownReasons: reasons}, nil)
				if err := monitor.run(context.Background(), db); err != nil {
					t.Fatal(err)
				}
			}
			first := f.now
			scan("mapping_path_missing", nil)
			f.now = first.Add(10 * time.Minute)
			scan("mapping_path_missing", nil)
			f.now = first.Add(6 * time.Hour)
			scan("mapping_path_missing", nil)
			original := len(*mail)
			if original == 0 {
				t.Fatal("original problem did not notify")
			}

			f.now = f.now.Add(time.Minute)
			scan("mapping_path_missing", map[string]string{"transient.service": "mapping_unreadable"})
			f.now = f.now.Add(time.Minute)
			scan("mapping_path_missing", nil)
			if len(*mail) != original {
				t.Fatalf("a transient object notified: %v", *mail)
			}

			f.now = f.now.Add(time.Minute)
			second := f.now
			extra := map[string]string{"dbus.service": "non_host_view"}
			scan("mapping_path_missing", extra)
			scan("mapping_path_missing", extra)
			if len(*mail) != original {
				t.Fatalf("a repeated sample confirmed a new problem: %v", *mail)
			}
			monitor = &maintenanceAlertMonitor{rule: alertRule{key: maintenanceAlertKey}}
			loadAlertRuntimeState([]*alertRule{&monitor.rule})
			f.now = second.Add(10 * time.Minute)
			scan("mapping_path_missing", extra)
			f.now = second.Add(6 * time.Hour)
			scan("mapping_path_missing", extra)
			f.now = f.now.Add(time.Minute)
			scan("mapping_path_missing", extra)
			if len(*mail) != original+1 || !strings.Contains((*mail)[original], "dbus.service") {
				t.Fatalf("expected a single independent notification: %v", *mail)
			}

			f.now = f.now.Add(time.Minute)
			third := f.now
			scan("mapping_unreadable", extra)
			f.now = third.Add(10 * time.Minute)
			scan("mapping_unreadable", extra)
			f.now = third.Add(6 * time.Hour)
			scan("mapping_unreadable", extra)
			if len(*mail) != original+2 || !strings.Contains((*mail)[original+1], "cron.service") {
				t.Fatalf("expected a single notification for an independent reason: %v", *mail)
			}
		})
	}
}
