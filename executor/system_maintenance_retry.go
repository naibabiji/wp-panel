package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func RetrySystemMaintenance(id int64) error {
	if err := requestMaintenanceActionRetry(database.GetDB(), id); err != nil {
		return err
	}
	WakeSystemMaintenance()
	return nil
}

type SystemMaintenanceActionView struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	Target       string `json:"target"`
	Status       string `json:"status"`
	MessageCode  string `json:"message_code"`
	RequestedAt  string `json:"requested_at"`
	RetryPending bool   `json:"retry_pending"`
}

func runManualMaintenanceRetry(ctx context.Context, cfg *config.Config, actions []systemMaintenanceAction, snapshot maintenanceRuntimeSnapshot, stable []maintenanceRuntimeEvidence) error {
	db := database.GetDB()
	for _, a := range actions {
		if !a.ManualRetry {
			continue
		}
		var files []maintenanceStoredEvidence
		if err := json.Unmarshal([]byte(a.EvidenceJSON), &files); err != nil {
			return err
		}
		if a.MessageCode == "evidence_budget_exhausted" {
			if len(snapshot.UnknownUnits) > 0 || snapshot.Unsupported > 0 {
				return errors.New("overflow retry runtime cannot be fully confirmed")
			}
			current, err := classifyMaintenanceTargets(ctx, snapshot)
			if err != nil {
				return err
			}
			if len(snapshot.UnknownUnits) > 0 {
				return errors.New("overflow target unknown")
			}
			var replacement []maintenanceStoredEvidence
			for _, t := range current {
				if t.Kind == a.Kind && (a.Kind == "reboot" || t.Target == a.Target) {
					replacement = append(replacement, t.Evidence...)
				}
			}
			if len(replacement) > 0 {
				files = replacement
			}
		}
		target := maintenanceTarget{a.Kind, a.Target, files}
		present, err := maintenanceTargetStillPresent(target, snapshot)
		if err != nil {
			return err
		}
		if present {
			persistent := false
			for _, e := range stable {
				for _, file := range files {
					if e.Unit == file.Unit && e.Path == file.Path && e.Old == file.Old {
						persistent = true
					}
				}
			}
			if !persistent {
				return nil
			}
		}
		var round systemMaintenanceAction
		for _, r := range actions {
			if r.ID == a.RoundID && r.Kind == "round" && r.Status == "pending" {
				round = r
				break
			}
		}
		if round.ID == 0 {
			round, err = createMaintenanceRound(db, []maintenanceTarget{target}, false, false)
			if err != nil {
				return err
			}
			name := fmt.Sprintf("manual/%d/%d", a.ID, a.Attempts+1)
			if _, err = db.Exec(`UPDATE system_maintenance_actions SET target=? WHERE id=?`, name, round.ID); err != nil {
				return err
			}
			if _, err = db.Exec(`UPDATE system_maintenance_actions SET round_id=? WHERE id=? AND manual_retry=1`, round.ID, a.ID); err != nil {
				return err
			}
		}
		deferred, err := executeMaintenanceTarget(ctx, cfg, &round, target, nil, true)
		if err != nil || deferred {
			return err
		}
		var status, code string
		if err = db.QueryRow(`SELECT status,message_code FROM system_maintenance_actions WHERE id=?`, a.ID).Scan(&status, &code); err != nil {
			return err
		}
		if status == "succeeded" || status == "failed" || status == "unknown" {
			_, err = db.Exec(`UPDATE system_maintenance_actions SET status=?,message_code=?,overtime_allowed=0,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, code, round.ID)
		}
		return err
	}
	return nil
}

// SystemMaintenanceReasonKey is shared by the API and background notices so
// persisted outcome codes have the same user-facing meaning in both places.
func SystemMaintenanceReasonKey(code string) string {
	keys := map[string]string{
		"ready": "ready", "installing": "installing", "completed": "completed",
		"": "requested", "externally_resolved": "completed", "maintenance_migrated": "migrated",
		"file_backup_busy": "backup", "package_manager_busy": "apt", "migration_busy": "migration",
		"wp_update_busy": "wp", "panel_lifecycle_busy": "panel", "panel_update_unconfirmed": "panel",
		"maintenance_busy": "busy", "evidence_budget_exhausted": "budget",
		"service_verification_failed": "service_verification_failed",
		"reboot_request_failed":       "reboot_request_failed", "reboot_not_observed": "reboot_not_observed",
		"post_reboot_health_failed": "post_reboot_health_failed", "verification_failed": "verification_failed",
		"interrupted": "interrupted", "installation_interrupted": "installation_interrupted",
		"installation_failed": "installation_failed", "authorization_revoked": "authorization_revoked",
		"outside_window": "outside_window", "timezone_unknown": "timezone_unknown", "runtime_unknown": "runtime_unknown",
	}
	key, ok := keys[code]
	if !ok {
		key = "attention"
		if strings.HasPrefix(code, "site_") {
			key = "site"
		}
	}
	return "settings.maintenance_reason_" + key
}
