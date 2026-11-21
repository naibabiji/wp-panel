package executor

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

// Verification is not a retry: no command that changes a service or server is
// issued here. A failed action can be resolved by SSH/package scripts later,
// but absence of a process alone is never enough to report recovery.
func resolveExternallyCompletedMaintenance(ctx context.Context, cfg *config.Config, actions []systemMaintenanceAction, snapshot maintenanceRuntimeSnapshot) error {
	if snapshot.UnknownUnits["*"] {
		return nil
	}
	for _, a := range actions {
		if (a.Kind != "service" && a.Kind != "reboot") || (a.Status != "failed" && a.Status != "unknown") || a.Attempts == 0 || a.ManualRetry || a.MessageCode == "evidence_budget_exhausted" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var files []maintenanceStoredEvidence
		if err := json.Unmarshal([]byte(a.EvidenceJSON), &files); err != nil {
			return err
		}
		target := maintenanceTarget{a.Kind, a.Target, files}
		present, err := maintenanceTargetStillPresent(target, snapshot)
		if err != nil || present {
			continue
		}
		if a.Kind == "service" {
			old, err := maintenanceServiceHasOldMappings(ctx, a.Target, snapshot)
			if err != nil || old {
				continue
			}
			if !maintenanceServices[a.Target] || maintenanceReady(ctx, cfg, a.Target) != nil {
				continue
			}
		} else {
			boot, err := systemPackageBootID()
			if err != nil || boot == a.BootIDBefore {
				continue
			}
			reached, err := maintenanceRebootReached(ctx, a.Target)
			if err != nil || !reached {
				continue
			}
			var health []string
			if err := json.Unmarshal([]byte(a.HealthJSON), &health); err != nil {
				return err
			}
			ready := true
			for _, unit := range health {
				if (!maintenanceServices[unit] && unit != "wp-panel.service") || maintenanceReady(ctx, cfg, unit) != nil {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
		}
		db := database.GetDB()
		result, err := db.Exec(`UPDATE system_maintenance_actions SET status='succeeded',last_success_json=evidence_json,evidence_json='[]',message_code='externally_resolved',notified_status='',notify_after='',finished_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status=? AND attempts=? AND manual_retry=0`, maintenanceClock().UTC().Format(time.RFC3339Nano), a.ID, a.Status, a.Attempts)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 1 {
			if _, err := db.Exec(`INSERT INTO operation_logs(operation,target,status,message) VALUES('system_maintenance_verify',?,'success','externally_resolved')`, a.Target); err != nil {
				return err
			}
		}
	}
	return nil
}

func recoverMaintenanceActions(ctx context.Context, cfg *config.Config, actions []systemMaintenanceAction) (bool, error) {
	for _, a := range actions {
		if a.Status != "requested" || (a.Kind != "service" && a.Kind != "reboot") {
			continue
		}
		boot, err := systemPackageBootID()
		if err != nil {
			return true, err
		}
		requested, err := time.Parse(time.RFC3339Nano, a.RequestedAt)
		if err != nil {
			return true, err
		}
		if boot == a.BootIDBefore && maintenanceClock().Sub(requested) < systemMaintenanceRebootGrace {
			return true, nil
		}
		var evidence []maintenanceStoredEvidence
		if err := json.Unmarshal([]byte(a.EvidenceJSON), &evidence); err != nil {
			return true, err
		}
		status, code := "unknown", "interrupted"
		if a.Kind == "reboot" && boot == a.BootIDBefore {
			status, code = "failed", "reboot_not_observed"
		} else {
			busy, err := maintenancePackagesBusy(cfg)
			if err != nil || busy {
				return true, err
			}
			snapshot, err := maintenanceScan(ctx)
			if err != nil {
				return true, err
			}
			target := maintenanceTarget{a.Kind, a.Target, evidence}
			present, checkErr := maintenanceTargetStillPresent(target, snapshot)
			reached := true
			if a.Kind == "reboot" {
				reached, err = maintenanceRebootReached(ctx, a.Target)
				if err == nil && reached && !present {
					var health []string
					if decodeErr := json.Unmarshal([]byte(a.HealthJSON), &health); decodeErr != nil {
						return true, decodeErr
					}
					err = maintenanceHealthReady(ctx, cfg, health)
				}
			} else {
				err = maintenanceReady(ctx, cfg, a.Target)
				other, mappingErr := maintenanceServiceHasOldMappings(ctx, a.Target, snapshot)
				present = present || other
				if checkErr == nil {
					checkErr = mappingErr
				}
			}
			if err == nil && checkErr == nil && !present && reached {
				status, code = "succeeded", "completed"
			} else if err == nil && checkErr == nil {
				status, code = "failed", "verification_failed"
			}
		}
		return true, finishMaintenanceAction(database.GetDB(), a, status, code, maintenanceClock())
	}
	return false, nil
}

func maintenanceHealthReady(ctx context.Context, cfg *config.Config, health []string) error {
	for _, unit := range health {
		if !maintenanceServices[unit] && unit != "wp-panel.service" {
			return errors.New("invalid stored health unit")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := pollServiceReady(func(check context.Context) error { return maintenanceReady(check, cfg, unit) }); err != nil {
			return err
		}
	}
	return nil
}
