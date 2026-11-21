package executor

import (
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestMaintenancePolicyWindowsUseExplicitCurrentTimezone(t *testing.T) {
	zone := time.FixedZone("changed-system-zone", 8*3600)
	now := time.Date(2026, 11, 21, 20, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		mode, window  string
		enabled, want bool
	}{
		{"scheduled", "04:00-06:00", true, true},
		{"scheduled", "23:00-05:00", true, true},
		{"scheduled", "03:00-04:00", true, false},
		{"immediate", "04:00-06:00", true, true},
		{"immediate", "04:00-06:00", false, false},
	} {
		p := SystemMaintenancePolicy{tc.enabled, tc.mode, tc.window}
		got, err := p.AllowsStart(now, zone)
		if err != nil || got != tc.want {
			t.Fatalf("%+v allowed=%v err=%v", p, got, err)
		}
	}
	for _, window := range []string{"04:00-04:00", "04:00-04:59", "23:30-00:20", "4:00-06:00", "25:00-06:00", "04:00-06:00-extra"} {
		if err := (SystemMaintenancePolicy{true, "scheduled", window}).Validate(); err == nil {
			t.Fatalf("invalid window accepted: %s", window)
		}
	}
	p := SystemMaintenancePolicy{true, "scheduled", "04:00-06:00"}
	if ok, err := p.AllowsStart(now, nil); err == nil || ok {
		t.Fatal("missing timezone authorized execution")
	}
}

func TestMaintenancePolicyFailsClosedAndSynchronizesRollbackKey(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	db := database.GetDB()
	p := SystemMaintenancePolicy{true, "scheduled", "23:30-00:30"}
	if err := SaveSystemMaintenancePolicy(db, p); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSystemMaintenancePolicy(db); err != nil || got != p {
		t.Fatalf("policy=%+v err=%v", got, err)
	}
	if got := readSecuritySetting("system_package_auto_update_enabled"); got != "true" {
		t.Fatal("rollback installation preference not synchronized")
	}
	if _, err := db.Exec(`UPDATE security_settings SET svalue='garbled' WHERE skey='system_maintenance_mode'`); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSystemMaintenancePolicy(db); err == nil || got.Enabled {
		t.Fatal("malformed policy authorized execution")
	}
	if _, err := db.Exec(`DELETE FROM security_settings WHERE skey='system_maintenance_enabled'`); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSystemMaintenancePolicy(db); err == nil || got.Enabled {
		t.Fatal("missing policy authorized execution")
	}
}

func TestMaintenancePolicyTransactionDoesNotResetActionBudget(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	db := database.GetDB()
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{true, "scheduled", "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_maintenance_actions(kind,target,status,attempts)
		VALUES('service','nginx','failed',1);
		CREATE TRIGGER reject_maintenance_rollback_key BEFORE UPDATE ON security_settings
		WHEN NEW.skey='system_package_auto_update_enabled' BEGIN SELECT RAISE(ABORT,'test rejection'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{false, "immediate", "05:00-07:00"}); err == nil {
		t.Fatal("expected atomic save failure")
	}
	got, err := ReadSystemMaintenancePolicy(db)
	if err != nil || !got.Enabled || got.Mode != "scheduled" || got.Window != "04:00-06:00" {
		t.Fatalf("partial settings commit: %+v %v", got, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_maintenance_rollback_key`); err != nil {
		t.Fatal(err)
	}
	got.Enabled = false
	if err := SaveSystemMaintenancePolicy(db, got); err != nil {
		t.Fatal(err)
	}
	if readSecuritySetting("system_package_auto_update_enabled") != "false" {
		t.Fatal("old binary would resume an installation disabled in new policy")
	}
	var attempts, retry int
	if err := db.QueryRow(`SELECT attempts,manual_retry FROM system_maintenance_actions WHERE target='nginx'`).Scan(&attempts, &retry); err != nil || attempts != 1 || retry != 0 {
		t.Fatalf("toggle reset action budget: %d %d %v", attempts, retry, err)
	}
}
