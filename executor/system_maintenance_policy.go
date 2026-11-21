package executor

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const systemMaintenanceObservationInterval = 10 * time.Minute
const systemMaintenanceRebootGrace = 15 * time.Minute
const systemMaintenanceDeferredNotice = 72 * time.Hour

type SystemMaintenancePolicy struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	Window  string `json:"window"`
}

// Missing, malformed and unreadable policy is an error, not authorization.
func ReadSystemMaintenancePolicy(db *sql.DB) (SystemMaintenancePolicy, error) {
	var p SystemMaintenancePolicy
	if db == nil {
		return p, errors.New("maintenance database unavailable")
	}
	values := map[string]string{}
	rows, err := db.Query(`SELECT skey,svalue FROM security_settings WHERE skey IN
		('system_maintenance_enabled','system_maintenance_mode','system_maintenance_window')`)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return p, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if len(values) != 3 || (values["system_maintenance_enabled"] != "true" && values["system_maintenance_enabled"] != "false") {
		return p, errors.New("maintenance policy missing or invalid")
	}
	p = SystemMaintenancePolicy{values["system_maintenance_enabled"] == "true", values["system_maintenance_mode"], values["system_maintenance_window"]}
	if err := p.Validate(); err != nil {
		return SystemMaintenancePolicy{}, err
	}
	return p, nil
}

func (p SystemMaintenancePolicy) Validate() error {
	if p.Mode != "immediate" && p.Mode != "scheduled" {
		return errors.New("invalid maintenance execution mode")
	}
	_, _, err := maintenanceWindowMinutes(p.Window)
	return err
}

func maintenanceWindowMinutes(window string) (int, int, error) {
	parts := strings.Split(window, "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("invalid maintenance window")
	}
	clock := func(s string) (int, error) {
		if len(s) != 5 {
			return 0, errors.New("maintenance clock must be HH:MM")
		}
		t, err := time.Parse("15:04", s)
		return t.Hour()*60 + t.Minute(), err
	}
	start, err := clock(parts[0])
	if err != nil {
		return 0, 0, err
	}
	end, err := clock(parts[1])
	if err != nil {
		return 0, 0, err
	}
	if duration := (end - start + 1440) % 1440; duration < 60 {
		return 0, 0, errors.New("maintenance window must be at least one hour")
	}
	return start, end, nil
}

// Load a fresh location each time. time.Local can remain cached after the
// administrator changes /etc/localtime through timedatectl.
func currentMaintenanceLocation() (*time.Location, error) {
	data, err := os.ReadFile("/etc/localtime")
	if err != nil {
		return nil, err
	}
	return time.LoadLocationFromTZData("system", data)
}

// End is exclusive: 04:00-06:00 does not start a new round at 06:00.
func (p SystemMaintenancePolicy) AllowsStart(now time.Time, location *time.Location) (bool, error) {
	if err := p.Validate(); err != nil {
		return false, err
	}
	if !p.Enabled {
		return false, nil
	}
	if p.Mode == "immediate" {
		return true, nil
	}
	if location == nil {
		return false, errors.New("system timezone unavailable")
	}
	start, end, _ := maintenanceWindowMinutes(p.Window)
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()
	if start < end {
		return minute >= start && minute < end, nil
	}
	return minute >= start || minute < end, nil
}

// Synchronize the old installation-only key for binary rollback. Nothing
// clears failed-action budgets here; toggling authorization is not a retry.
func SaveSystemMaintenancePolicy(db *sql.DB, p SystemMaintenancePolicy) error {
	return SaveSystemMaintenancePolicyWithSettings(db, p, nil)
}

// Settings uses the same transaction for policy, rollback compatibility and
// its other validated fields; a later failure must not partially change it.
func SaveSystemMaintenancePolicyWithSettings(db *sql.DB, p SystemMaintenancePolicy, extra map[string]string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if db == nil {
		return errors.New("maintenance database unavailable")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updates := make(map[string]string, len(extra)+4)
	for key, value := range extra {
		updates[key] = value
	}
	for key, value := range map[string]string{
		"system_maintenance_enabled":         fmt.Sprint(p.Enabled),
		"system_package_auto_update_enabled": fmt.Sprint(p.Enabled),
		"system_maintenance_mode":            p.Mode,
		"system_maintenance_window":          p.Window,
	} {
		updates[key] = value
	}
	for key, value := range updates {
		if _, err := tx.Exec(`INSERT INTO security_settings(skey,svalue) VALUES(?,?)
			ON CONFLICT(skey) DO UPDATE SET svalue=excluded.svalue,updated_at=CURRENT_TIMESTAMP`, key, value); err != nil {
			return err
		}
	}
	if !p.Enabled {
		// Re-enabling maintenance must not resurrect an earlier button click.
		// This revokes unused authorization; it does not reset the attempt budget.
		if _, err := tx.Exec(`UPDATE system_maintenance_actions SET manual_retry=0 WHERE manual_retry=1`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE system_maintenance_actions SET overtime_allowed=0 WHERE kind='round' AND status='pending'`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE system_maintenance_actions SET status='unknown',message_code='authorization_revoked' WHERE kind='round' AND status='pending' AND target LIKE 'manual/%'`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
