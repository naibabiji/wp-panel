package executor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var maintenanceTargetName = regexp.MustCompile(`^[A-Za-z0-9_.:/+-]{1,256}$`)
var maintenanceServiceTarget = regexp.MustCompile(`^[A-Za-z0-9_.:-]+\.service$`)

type systemMaintenanceAction struct {
	ID                int64
	Kind              string
	Target            string
	Status            string
	EvidenceJSON      string
	LastSuccessJSON   string
	HealthJSON        string
	Phase             string
	BootIDBefore      string
	Attempts          int
	ManualRetry       bool
	RequestedAt       string
	RoundID           int64
	MessageCode       string
	DeferredSince     string
	DeferredWindows   int
	DeferredWindowKey string
	OvertimeAllowed   bool
	NotifiedStatus    string
	NotifyAfter       string
}

func loadMaintenanceActions(db *sql.DB) ([]systemMaintenanceAction, error) {
	rows, err := db.Query(`SELECT id,kind,target,status,evidence_json,last_success_json,boot_id_before,attempts,manual_retry,requested_at,
		COALESCE(round_id,0),message_code,deferred_since,overtime_allowed,notified_status,deferred_windows,deferred_window_key,notify_after,health_json,phase
		FROM system_maintenance_actions ORDER BY updated_at DESC,id DESC LIMIT 256`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []systemMaintenanceAction
	for rows.Next() {
		var a systemMaintenanceAction
		if err := rows.Scan(&a.ID, &a.Kind, &a.Target, &a.Status, &a.EvidenceJSON, &a.LastSuccessJSON, &a.BootIDBefore, &a.Attempts, &a.ManualRetry, &a.RequestedAt,
			&a.RoundID, &a.MessageCode, &a.DeferredSince, &a.OvertimeAllowed, &a.NotifiedStatus, &a.DeferredWindows, &a.DeferredWindowKey, &a.NotifyAfter, &a.HealthJSON, &a.Phase); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// Claim commits requested before the caller may cause any external effect.
// A SQLite write serializes competing processes; no in-memory-only budget.
func claimMaintenanceAction(db *sql.DB, kind, target string, evidence []maintenanceRuntimeEvidence, bootID string, now time.Time) (systemMaintenanceAction, bool, error) {
	return claimMaintenanceActionWithHealth(db, kind, target, evidence, bootID, now, nil)
}

func claimMaintenanceActionWithHealth(db *sql.DB, kind, target string, evidence []maintenanceRuntimeEvidence, bootID string, now time.Time, health []string) (systemMaintenanceAction, bool, error) {
	var action systemMaintenanceAction
	if db == nil || (kind != "service" && kind != "reboot") || !maintenanceTargetName.MatchString(target) ||
		(kind == "service" && (len(evidence) == 0 || !maintenanceServiceTarget.MatchString(target))) || bootID == "" {
		return action, false, errors.New("invalid maintenance action")
	}
	for _, e := range evidence {
		if kind == "service" && e.Unit != target {
			return action, false, errors.New("maintenance evidence does not match service target")
		}
	}
	for _, unit := range health {
		if !maintenanceServices[unit] && unit != "wp-panel.service" {
			return action, false, errors.New("invalid maintenance health service")
		}
	}
	healthJSON, err := json.Marshal(health)
	if err != nil {
		return action, false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return action, false, err
	}
	defer tx.Rollback()
	// The first statement obtains SQLite's write reservation, including for
	// existing targets; competing claims cannot both read an old budget.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO system_maintenance_actions(kind,target) VALUES(?,?)`, kind, target); err != nil {
		return action, false, err
	}
	var enabled string
	if err := tx.QueryRow(`SELECT svalue FROM security_settings WHERE skey='system_maintenance_enabled'`).Scan(&enabled); err != nil || enabled != "true" {
		return action, false, errors.New("automatic maintenance is disabled or unavailable")
	}
	err = tx.QueryRow(`SELECT id,kind,target,status,evidence_json,last_success_json,boot_id_before,attempts,manual_retry,requested_at,message_code
		FROM system_maintenance_actions WHERE kind=? AND target=?`, kind, target).Scan(
		&action.ID, &action.Kind, &action.Target, &action.Status, &action.EvidenceJSON, &action.LastSuccessJSON,
		&action.BootIDBefore, &action.Attempts, &action.ManualRetry, &action.RequestedAt, &action.MessageCode)
	if err != nil {
		return action, false, err
	}
	if action.Status == "requested" {
		return action, false, nil
	}
	if kind == "reboot" && !action.ManualRetry {
		fresh, err := freshMaintenanceRebootIntent(tx, target, evidence, bootID)
		if err != nil || !fresh {
			return action, false, err
		}
	}
	newEvidence, err := hasNewMaintenanceEvidence(action.EvidenceJSON, evidence)
	if err != nil {
		return action, false, fmt.Errorf("invalid stored maintenance evidence: %w", err)
	}
	if newEvidence {
		var failed []maintenanceRuntimeEvidence
		var completed []maintenanceRuntimeEvidence
		if err := json.Unmarshal([]byte(action.EvidenceJSON), &failed); err != nil {
			return action, false, err
		}
		if err := json.Unmarshal([]byte(action.LastSuccessJSON), &completed); err != nil {
			return action, false, err
		}
		known, err := json.Marshal(append(failed, completed...))
		if err != nil {
			return action, false, err
		}
		newEvidence, err = hasNewMaintenanceEvidence(string(known), evidence)
		if err != nil {
			return action, false, err
		}
	}
	if action.Attempts > 0 && !action.ManualRetry && !newEvidence {
		if action.BootIDBefore != bootID && len(evidence) > 0 {
			newEvidence = true
		}
	}
	if action.Attempts > 0 && !action.ManualRetry && !newEvidence {
		return action, false, nil
	}
	var previous []maintenanceRuntimeEvidence
	if err := json.Unmarshal([]byte(action.EvidenceJSON), &previous); err != nil {
		return action, false, err
	}
	if action.ManualRetry && action.MessageCode == "evidence_budget_exhausted" {
		previous = nil
	}
	if action.BootIDBefore != "" && action.BootIDBefore != bootID {
		previous = nil
	}
	// Preserve the union for this target. A worker exiting only shrinks the
	// observed set; it does not restore a failed target's automatic budget.
	keys := map[string]bool{}
	for _, e := range previous {
		keys[e.fileKey()] = true
	}
	for _, e := range evidence {
		if !keys[e.fileKey()] {
			previous = append(previous, e)
			keys[e.fileKey()] = true
		}
	}
	encoded, err := json.Marshal(compactMaintenanceEvidence(previous))
	if err != nil {
		return action, false, err
	}
	if len(encoded) > 8<<20 {
		if _, err := tx.Exec(`UPDATE system_maintenance_actions SET status='failed',message_code='evidence_budget_exhausted',updated_at=CURRENT_TIMESTAMP WHERE id=?`, action.ID); err != nil {
			return action, false, err
		}
		if err := tx.Commit(); err != nil {
			return action, false, err
		}
		return action, false, errors.New("maintenance evidence budget exceeds limit")
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec(`UPDATE system_maintenance_actions SET status='requested',evidence_json=?,
		boot_id_before=?,attempts=attempts+1,manual_retry=0,requested_at=?,finished_at='',
		message_code='',notified_status='',notify_after='',health_json=?,updated_at=? WHERE id=?`, string(encoded), bootID, stamp, string(healthJSON), stamp, action.ID)
	if err != nil {
		return action, false, err
	}
	if _, err := tx.Exec(`INSERT INTO operation_logs(operation,target,status,message) VALUES(?,?,?,?)`, "system_maintenance_"+kind, target, "running", "维护动作已持久登记，等待执行与验证"); err != nil {
		return action, false, err
	}
	if err := tx.Commit(); err != nil {
		return action, false, err
	}
	action.Status, action.EvidenceJSON, action.BootIDBefore = "requested", string(encoded), bootID
	action.Attempts++
	action.ManualRetry, action.RequestedAt = false, stamp
	action.HealthJSON = string(healthJSON)
	return action, true, nil
}

func finishMaintenanceAction(db *sql.DB, action systemMaintenanceAction, status, code string, now time.Time) error {
	if status != "succeeded" && status != "failed" && status != "unknown" {
		return errors.New("invalid maintenance action outcome")
	}
	// Retain only the latest successful target fingerprint, replacing the old
	// one. Failed unions are released only after verified success. Clearing all
	// evidence without this bounded tombstone would re-authorize a stale sample.
	result, err := db.Exec(`UPDATE system_maintenance_actions SET status=?,message_code=?,finished_at=?,updated_at=?,
		last_success_json=CASE WHEN ?='succeeded' THEN evidence_json ELSE last_success_json END,
		evidence_json=CASE WHEN ?='succeeded' THEN '[]' ELSE evidence_json END
		WHERE id=? AND status='requested' AND attempts=?`, status, code,
		now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), status, status, action.ID, action.Attempts)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.New("maintenance action no longer owns this attempt")
	}
	logStatus := "failed"
	if status == "succeeded" {
		logStatus = "success"
	}
	if _, err := db.Exec(`INSERT INTO operation_logs(operation,target,status,message) VALUES(?,?,?,?)`, "system_maintenance_"+action.Kind, action.Target, logStatus, code); err != nil {
		return err
	}
	return nil
}

// One explicit retry authorization, not deletion/reset of the budget. The
// eventual runner must recheck evidence, busy gates and enabled policy again.
func requestMaintenanceActionRetry(db *sql.DB, id int64) error {
	if db == nil || id < 1 {
		return errors.New("invalid maintenance retry")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Reserve the writer before checking requested actions and policy.
	if _, err := tx.Exec(`UPDATE system_maintenance_actions SET manual_retry=manual_retry WHERE id=?`, id); err != nil {
		return err
	}
	var enabled string
	if err := tx.QueryRow(`SELECT svalue FROM security_settings WHERE skey='system_maintenance_enabled'`).Scan(&enabled); err != nil || enabled != "true" {
		return errors.New("automatic maintenance is disabled or unavailable")
	}
	var running int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE status='requested' AND kind IN ('service','reboot')`).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return errors.New("maintenance action still requested; recheck its result first")
	}
	result, err := tx.Exec(`UPDATE system_maintenance_actions SET manual_retry=1,round_id=NULL,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND kind IN ('service','reboot') AND status IN ('failed','unknown') AND manual_retry=0`, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.New("maintenance retry unavailable or already requested")
	}
	return tx.Commit()
}
