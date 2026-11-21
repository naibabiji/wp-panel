package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func createMaintenanceRound(db *sql.DB, scope []maintenanceTarget, installing, overtime bool) (systemMaintenanceAction, error) {
	var a systemMaintenanceAction
	encoded, err := json.Marshal(scope)
	if err != nil || len(encoded) > 8<<20 {
		return a, errors.New("maintenance round exceeds limit")
	}
	now := maintenanceClock().UTC().Format(time.RFC3339Nano)
	code := "ready"
	phase := "runtime"
	if installing {
		code = "installing"
		phase = "install"
	}
	result, err := db.Exec(`INSERT INTO system_maintenance_actions(kind,target,status,phase,evidence_json,overtime_allowed,requested_at,message_code) VALUES('round',?,'pending',?,?,?,?,?)`, now, phase, string(encoded), overtime, now, code)
	if err != nil {
		return a, err
	}
	id, err := result.LastInsertId()
	a = systemMaintenanceAction{ID: id, Kind: "round", Target: now, Status: "pending", EvidenceJSON: string(encoded), OvertimeAllowed: overtime, RequestedAt: now, MessageCode: code}
	a.Phase = phase
	// Round history alone is bounded; deleting completed rounds never resets
	// service/reboot budgets. Notice uses one static migration record.
	_, _ = db.Exec(`DELETE FROM system_maintenance_actions WHERE kind='round' AND status NOT IN ('pending','requested') AND id NOT IN (SELECT id FROM system_maintenance_actions WHERE kind='round' ORDER BY id DESC LIMIT 8)`)
	return a, err
}

func sealMaintenanceRound(ctx context.Context, db *sql.DB, id int64) error {
	snapshot, err := maintenanceScan(ctx)
	if err != nil {
		return err
	}
	targets, err := classifyMaintenanceTargets(ctx, snapshot)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(targets)
	if err != nil || len(encoded) > 8<<20 {
		return errors.New("maintenance round exceeds limit")
	}
	result, err := db.Exec(`UPDATE system_maintenance_actions SET evidence_json=?,message_code='ready',phase='runtime',updated_at=CURRENT_TIMESTAMP WHERE id=? AND kind='round' AND phase='install' AND status='pending'`, string(encoded), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("maintenance round no longer installing")
	}
	return nil
}

func deferMaintenanceRound(db *sql.DB, round *systemMaintenanceAction, reason string) error {
	policy, err := ReadSystemMaintenancePolicy(db)
	if err != nil {
		return err
	}
	location, err := currentMaintenanceLocation()
	if err != nil {
		return err
	}
	return deferMaintenanceRoundAt(db, round, reason, maintenanceClock(), policy, location)
}

func deferMaintenanceRoundAt(db *sql.DB, round *systemMaintenanceAction, reason string, now time.Time, policy SystemMaintenancePolicy, location *time.Location) error {
	key := ""
	if policy.Mode == "scheduled" {
		start, _, err := maintenanceWindowMinutes(policy.Window)
		if err != nil {
			return err
		}
		local := now.In(location)
		window := time.Date(local.Year(), local.Month(), local.Day(), start/60, start%60, 0, 0, location)
		if local.Before(window) {
			window = window.AddDate(0, 0, -1)
		}
		key = window.Format(time.RFC3339)
	}
	var previous string
	var count int
	if err := db.QueryRow(`SELECT deferred_window_key,deferred_windows FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&previous, &count); err != nil {
		return err
	}
	if key != "" && key != previous {
		last, lastErr := time.Parse(time.RFC3339, previous)
		current, _ := time.Parse(time.RFC3339, key)
		if lastErr == nil && current.In(location).AddDate(0, 0, -1).Equal(last) {
			count++
		} else {
			count = 1
		}
	}
	_, err := db.Exec(`UPDATE system_maintenance_actions SET overtime_allowed=0,deferred_since=CASE WHEN deferred_since='' THEN ? ELSE deferred_since END,deferred_windows=?,deferred_window_key=?,message_code=?,updated_at=? WHERE id=?`, now.UTC().Format(time.RFC3339Nano), count, key, reason, now.UTC().Format(time.RFC3339Nano), round.ID)
	round.OvertimeAllowed = false
	return err
}
