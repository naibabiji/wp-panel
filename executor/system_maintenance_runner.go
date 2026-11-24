package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

var maintenanceWake = make(chan struct{}, 1)
var maintenanceEngineMu sync.Mutex
var maintenanceObserver maintenanceRuntimeObserver
var maintenanceLastScan time.Time
var maintenanceGate = acquireMaintenanceGate
var maintenanceReady = maintenanceServiceReady
var maintenanceStartInstall = startMaintenancePackageUpdate
var errMaintenanceBudgetSpent = errors.New("maintenance target was already attempted; explicit retry required")
var maintenanceClock = time.Now
var maintenancePackagesBusy = maintenancePackageBusy
var maintenanceDPKGQuery = maintenanceDPKGLocksBusy
var maintenanceWaitReboot = func(ctx context.Context) error {
	timer := time.NewTimer(systemMaintenanceRebootGrace)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
var maintenanceInspectionMu sync.RWMutex
var maintenanceInspection = SystemMaintenanceInspection{Status: "not_checked"}

type SystemMaintenanceInspection struct {
	Status        string `json:"status"`
	CheckedAt     string `json:"checked_at"`
	UnknownUnits  int    `json:"unknown_units"`
	Unsupported   int    `json:"unsupported"`
	BudgetBlocked bool   `json:"budget_blocked"`
	RebootUnknown bool   `json:"reboot_unknown"`
	sampledAt     time.Time
	unknownUnits  []string
	unknownReason string
	budgetOwned   bool
	checkFailed   bool
}

func SnapshotSystemMaintenanceInspection() SystemMaintenanceInspection {
	maintenanceInspectionMu.RLock()
	defer maintenanceInspectionMu.RUnlock()
	return maintenanceInspection
}

func recordMaintenanceInspection(s maintenanceRuntimeSnapshot, err error) {
	v := SystemMaintenanceInspection{Status: "checked", CheckedAt: maintenanceClock().UTC().Format(time.RFC3339), Unsupported: s.Unsupported, BudgetBlocked: s.UnknownUnits["action_budget"], RebootUnknown: s.UnknownUnits["kernel_or_marker"], sampledAt: s.At, budgetOwned: s.budgetOwned}
	for unit, unknown := range s.UnknownUnits {
		if unknown && unit != "action_budget" && unit != "kernel_or_marker" {
			v.UnknownUnits++
			v.unknownUnits = append(v.unknownUnits, unit)
		}
	}
	sort.Strings(v.unknownUnits)
	if len(v.unknownUnits) > 0 {
		v.unknownReason = s.UnknownReasons[v.unknownUnits[0]]
	}
	if len(v.unknownUnits) > 3 {
		v.unknownUnits = v.unknownUnits[:3]
	}
	if err != nil {
		v.sampledAt = time.Time{}
		v.checkFailed = true
	}
	if err != nil || v.UnknownUnits > 0 {
		v.Status = "unknown"
	} else if v.RebootUnknown {
		v.Status = "reboot_unknown"
	} else if v.BudgetBlocked {
		v.Status = "attention"
	} else if len(s.Evidence) > 0 || s.pendingAction {
		v.Status = "waiting"
	} else if s.At.IsZero() {
		v.Status = "busy"
	}
	maintenanceInspectionMu.Lock()
	maintenanceInspection = v
	maintenanceInspectionMu.Unlock()
}

func WakeSystemMaintenance() {
	select {
	case maintenanceWake <- struct{}{}:
	default:
	}
}

func SystemMaintenanceActions() ([]SystemMaintenanceActionView, error) {
	if database.GetDB() == nil {
		return nil, errors.New("maintenance database unavailable")
	}
	actions, err := loadMaintenanceActions(database.GetDB())
	if err != nil {
		return nil, err
	}
	result := make([]SystemMaintenanceActionView, 0)
	for _, a := range actions {
		result = append(result, SystemMaintenanceActionView{a.ID, a.Kind, a.Target, a.Status, a.MessageCode, a.RequestedAt, a.ManualRetry})
	}
	return result, nil
}

func runSystemMaintenanceTick(cfg *config.Config, force bool) {
	if cfg == nil || database.GetDB() == nil || !maintenanceEngineMu.TryLock() {
		return
	}
	defer maintenanceEngineMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := runMaintenanceCycle(ctx, cfg, force); err != nil {
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, err)
		log.Printf("系统自动维护检查未完成: %v", err)
	}
	// Evaluate recovery before sending round results so the two paths can
	// share the round's persisted channel markers for a verified recovery.
	if err := maintenanceAlerts.run(ctx, database.GetDB()); err != nil {
		log.Printf("维护通知未完成: %v", err)
	}
}

func runMaintenanceCycle(ctx context.Context, cfg *config.Config, force bool) error {
	db := database.GetDB()
	policy, err := ReadSystemMaintenancePolicy(db)
	if err != nil {
		return err
	}
	// Accepted actions must be reconciled even if authorization was disabled.
	actions, err := loadMaintenanceActions(db)
	if err != nil {
		return err
	}
	if pending, err := recoverMaintenanceActions(ctx, cfg, actions); err != nil || pending {
		if pending {
			recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, nil)
		}
		return err
	}
	if !policy.Enabled {
		maintenanceObserver.previous = maintenanceRuntimeSnapshot{}
		return nil
	}
	location, err := currentMaintenanceLocation()
	if err != nil {
		return err
	}
	allowed, err := policy.AllowsStart(maintenanceClock(), location)
	if err != nil {
		return err
	}
	var round *systemMaintenanceAction
	manual := false
	for i := range actions {
		a := &actions[i]
		if a.Kind == "round" && !strings.HasPrefix(a.Target, "manual/") && (a.Status == "pending" || a.Status == "requested") {
			round = a
		}
		if a.ManualRetry {
			manual = true
		}
	}
	if round != nil && round.Phase == "install" {
		status := ReconcileSystemPackageUpdateStatus(cfg)
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{pendingAction: status.Status == "deferred"}, nil)
		if status.Status == "running" {
			maintenanceObserver.previous = maintenanceRuntimeSnapshot{}
			return nil
		}
		if status.Status == "deferred" {
			if !allowed {
				return nil
			}
			updated, parseErr := time.Parse(time.RFC3339, status.UpdatedAt)
			if !force && parseErr == nil && maintenanceClock().Sub(updated) < systemMaintenanceObservationInterval {
				return nil
			}
			_, err := maintenanceStartInstall(cfg, round.ID)
			if errors.Is(err, errSystemPackageUpdateBusy) {
				_ = deferMaintenanceRound(db, round, "package_manager_busy")
				status.UpdatedAt = maintenanceClock().UTC().Format(time.RFC3339)
				return writePanelDBRestoreJSON(SystemPackageUpdateStatusPath(cfg), status)
			}
			if err != nil {
				recordSystemPackageAutoCheck("update_not_started", 0, "更新任务未能启动，请查看系统更新卡片")
				return err
			}
			setSecuritySetting("system_package_auto_check_status", "update_started")
			return nil
		}
		// A successful runner seals the round before it writes success. A
		// missing seal after completion is unknown, never overtime permission.
		result, err := db.Exec(`UPDATE system_maintenance_actions SET status='unknown',message_code='installation_interrupted',overtime_allowed=0,updated_at=CURRENT_TIMESTAMP WHERE id=? AND phase='install' AND status='pending'`, round.ID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 1 {
			setSecuritySetting("system_package_auto_check_status", "update_started")
		}
		return nil
	}
	if !force && maintenanceClock().Sub(maintenanceLastScan) < systemMaintenanceObservationInterval {
		return nil
	}
	maintenanceLastScan = maintenanceClock()
	busy := func() (bool, error) { return maintenancePackagesBusy(cfg) }
	// Installation is independent of runtime inspection. An unreadable process
	// must not prevent otherwise authorized package patches from being installed.
	if busyNow, busyErr := busy(); busyErr != nil {
		return busyErr
	} else if busyNow {
		maintenanceObserver.previous = maintenanceRuntimeSnapshot{}
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, nil)
		if round != nil {
			return deferMaintenanceRound(db, round, "package_manager_busy")
		}
		return nil
	}
	checkStatus := readSecuritySetting("system_package_auto_check_status")
	if round == nil && allowed && checkStatus != "update_started" && checkStatus != "update_not_started" && checkStatus != "failed" {
		out, err := systemPackageUpdateCommandOutput("apt", "list", "--upgradable")
		if err != nil {
			return err
		}
		if countAPTUpgradablePackages(out) > 0 {
			r, err := createMaintenanceRound(db, nil, true, allowed)
			if err != nil {
				return err
			}
			if _, err := maintenanceStartInstall(cfg, r.ID); err != nil {
				recordSystemPackageAutoCheck("update_not_started", countAPTUpgradablePackages(out), "更新任务未能启动，请查看系统更新卡片")
				_, _ = db.Exec(`UPDATE system_maintenance_actions SET status='failed',message_code='installation_failed',overtime_allowed=0 WHERE id=?`, r.ID)
				return err
			}
			recordSystemPackageAutoCheck("update_started", countAPTUpgradablePackages(out), "")
			maintenanceObserver.previous = maintenanceRuntimeSnapshot{}
			recordMaintenanceInspection(maintenanceRuntimeSnapshot{}, nil)
			return nil
		}
	}
	snapshot, stable, err := maintenanceObserver.observe(ctx, busy, maintenanceScan)
	if err != nil {
		recordMaintenanceInspection(snapshot, err)
		if round != nil {
			_, _ = db.Exec(`UPDATE system_maintenance_actions SET status='unknown',overtime_allowed=0,message_code='needs_attention',updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`, round.ID)
		}
		return err
	}
	if snapshot.At.IsZero() {
		recordMaintenanceInspection(snapshot, nil)
		if round != nil {
			return deferMaintenanceRound(db, round, "package_manager_busy")
		}
		return nil
	}
	current, err := classifyMaintenanceTargets(ctx, snapshot)
	if err != nil {
		return err
	}
	snapshot.pendingAction = len(current) > 0
	if err := resolveExternallyCompletedMaintenance(ctx, cfg, actions, snapshot); err != nil {
		return err
	}
	if manual {
		recordMaintenanceInspection(snapshot, nil)
		return runManualMaintenanceRetry(ctx, cfg, actions, snapshot, stable)
	}
	if round == nil {
		var blocked []maintenanceTarget
		current, blocked, err = eligibleMaintenanceTargets(db, current, actions)
		if err != nil {
			return err
		}
		if len(blocked) > 0 {
			snapshot.UnknownUnits["action_budget"] = true
			snapshot.budgetOwned = maintenanceBlockedActionsOwnNotices(blocked, actions)
		}
		recordMaintenanceInspection(snapshot, nil)
		if len(current) == 0 || (!allowed && !manual) {
			return nil
		}
		r, err := createMaintenanceRound(db, current, false, allowed)
		if err != nil {
			return err
		}
		round = &r
	}
	recordMaintenanceInspection(snapshot, nil)
	var scope []maintenanceTarget
	if err := json.Unmarshal([]byte(round.EvidenceJSON), &scope); err != nil {
		return err
	}
	if !allowed && !manual && !round.OvertimeAllowed {
		return nil
	}
	// These are the frozen targets. New evidence cannot join a round outside
	// its start window. Only already authorized retries may bypass that window.
	stableKeys := map[string]bool{}
	for _, e := range stable {
		stableKeys[e.observationKey()] = true
	}
	for _, target := range scope {
		var evidence []maintenanceRuntimeEvidence
		for _, file := range target.Evidence {
			key := (maintenanceRuntimeEvidence{Path: file.Path, Old: file.Old}).fileKey()
			for _, e := range snapshot.Evidence {
				if e.fileKey() == key && e.Unit == file.Unit && stableKeys[e.observationKey()] {
					evidence = append(evidence, e)
					break
				}
			}
		}
		// Runtime-only targets require persistence; kernel/marker targets use
		// their independent current local facts.
		independentReboot := false
		if target.Kind == "reboot" && target.Target != "runtime" {
			reached, err := maintenanceRebootReached(ctx, target.Target)
			if err != nil {
				return err
			}
			if reached && len(evidence) == 0 {
				continue
			}
			independentReboot = !reached
		}
		if !independentReboot && len(target.Evidence) > 0 && len(evidence) == 0 {
			continue
		}
		var retry bool
		_ = db.QueryRow(`SELECT manual_retry FROM system_maintenance_actions WHERE kind=? AND target=?`, target.Kind, target.Target).Scan(&retry)
		if !allowed && !round.OvertimeAllowed && !retry {
			continue
		}
		deferred, err := executeMaintenanceTarget(ctx, cfg, round, target, evidence, retry)
		if err != nil {
			if errors.Is(err, errMaintenanceBudgetSpent) {
				_, _ = db.Exec(`UPDATE system_maintenance_actions SET status='failed',overtime_allowed=0,message_code='needs_attention',updated_at=CURRENT_TIMESTAMP WHERE id=?`, round.ID)
			}
			return err
		}
		if deferred {
			return nil
		}
	}
	// A fresh observation is needed to finish the round. Unknown units keep
	// it open; absence alone never turns a failed action into success.
	remaining, failed, unknown := false, false, false
	for _, target := range scope {
		present, err := maintenanceTargetStillPresent(target, snapshot)
		if err != nil {
			unknown = true
		}
		if err != nil || present {
			remaining = true
		}
		if target.Kind == "reboot" && target.Target != "runtime" {
			reached, checkErr := maintenanceRebootReached(ctx, target.Target)
			if checkErr != nil {
				unknown = true
			}
			if checkErr != nil || !reached {
				remaining = true
			}
		}
		var failures int
		_ = db.QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE kind=? AND target=? AND status IN ('failed','unknown','requested')`, target.Kind, target.Target).Scan(&failures)
		if failures > 0 {
			failed = true
		}
	}
	if failed {
		_, err = db.Exec(`UPDATE system_maintenance_actions SET status='failed',overtime_allowed=0,message_code='needs_attention',updated_at=CURRENT_TIMESTAMP WHERE id=?`, round.ID)
	} else if unknown {
		_, err = db.Exec(`UPDATE system_maintenance_actions SET status='unknown',overtime_allowed=0,message_code='needs_attention',updated_at=CURRENT_TIMESTAMP WHERE id=?`, round.ID)
	} else if !remaining {
		_, err = db.Exec(`UPDATE system_maintenance_actions SET status='succeeded',evidence_json='[]',overtime_allowed=0,finished_at=?,message_code='completed',updated_at=CURRENT_TIMESTAMP WHERE id=?`, maintenanceClock().UTC().Format(time.RFC3339Nano), round.ID)
	}
	return err
}

func executeMaintenanceTarget(ctx context.Context, cfg *config.Config, round *systemMaintenanceAction, target maintenanceTarget, evidence []maintenanceRuntimeEvidence, retry bool) (bool, error) {
	db := database.GetDB()
	release, reason, err := maintenanceGate(cfg, db)
	if err != nil {
		_ = deferMaintenanceRound(db, round, reason)
		return false, err
	}
	if release == nil {
		return true, deferMaintenanceRound(db, round, reason)
	}
	defer release()
	policy, err := ReadSystemMaintenancePolicy(db)
	if err != nil || !policy.Enabled {
		return false, err
	}
	location, err := currentMaintenanceLocation()
	if err != nil {
		return false, err
	}
	allowed, err := policy.AllowsStart(maintenanceClock(), location)
	if err != nil {
		return false, err
	}
	if !allowed && !round.OvertimeAllowed && !retry {
		return false, nil
	}
	if retry {
		if err := db.QueryRow(`SELECT manual_retry FROM system_maintenance_actions WHERE kind=? AND target=?`, target.Kind, target.Target).Scan(&retry); err != nil {
			return false, err
		}
		if !retry {
			return false, nil
		}
	}
	fresh, err := maintenanceScan(ctx)
	if err != nil {
		return false, err
	}
	present, err := maintenanceTargetStillPresent(target, fresh)
	if err != nil {
		return false, err
	}
	needsReboot := present
	if target.Kind == "service" {
		if !present && !retry {
			return false, nil
		}
	} else if target.Target != "runtime" {
		reached, err := maintenanceRebootReached(ctx, target.Target)
		if err != nil {
			return false, err
		}
		needsReboot = present || !reached
	}
	if target.Kind == "reboot" && !needsReboot && !retry {
		return false, nil
	}
	busy, err := maintenanceDPKGQuery()
	if err != nil || busy {
		writeErr := deferMaintenanceRound(db, round, "package_manager_busy")
		if err != nil {
			return true, err
		}
		return true, writeErr
	}
	if target.Kind == "service" {
		if !maintenanceServices[target.Target] {
			return false, errors.New("unsupported service")
		}
		if err := maintenanceCommand(ctx, "systemctl", "is-active", "--quiet", target.Target); err != nil && !retry {
			return false, errors.New("service was not active; automatic start refused")
		}
	}
	boot, err := systemPackageBootID()
	if err != nil {
		return false, err
	}
	// Reserve the whole sealed file set once any process supplied persistent
	// evidence. A partial scan must not grant a second attempt for the same
	// update when another worker's old files become persistent later.
	evidence = nil
	for _, e := range target.Evidence {
		evidence = append(evidence, maintenanceRuntimeEvidence{Unit: e.Unit, Path: e.Path, Old: e.Old, Package: e.Package})
	}
	var health []string
	if target.Kind == "reboot" && needsReboot {
		units := []string{"wp-panel.service"}
		for unit := range maintenanceServices {
			units = append(units, unit)
		}
		sort.Strings(units)
		for _, unit := range units {
			err := maintenanceCommand(ctx, "systemctl", "is-active", "--quiet", unit)
			if err == nil {
				health = append(health, unit)
				continue
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || (exit.ExitCode() != 3 && exit.ExitCode() != 4) {
				return false, err
			}
		}
	}
	if target.Kind == "reboot" && retry && !needsReboot {
		var stored string
		if err := db.QueryRow(`SELECT health_json FROM system_maintenance_actions WHERE kind='reboot' AND target=?`, target.Target).Scan(&stored); err != nil {
			return false, err
		}
		if err := json.Unmarshal([]byte(stored), &health); err != nil {
			return false, err
		}
	}
	if busy, err := maintenanceDPKGQuery(); err != nil || busy {
		writeErr := deferMaintenanceRound(db, round, "package_manager_busy")
		if err != nil {
			return true, err
		}
		return true, writeErr
	}
	// Lock before claiming: temporary guard/config activity must not consume
	// a maintenance attempt. Non-guarded units (cron/rsyslog) need no lock.
	var controlled []string
	if target.Kind == "reboot" {
		controlled = guardServiceNames
	} else if name, ok := guardedServiceName(target.Target); ok {
		controlled = []string{name}
	}
	releaseServices, err := tryServiceControlLocks(cfg.Panel.DataDir, controlled...)
	if err != nil {
		if errors.Is(err, errServiceControlBusy) {
			return true, deferMaintenanceRound(db, round, "maintenance_busy")
		}
		return false, err
	}
	defer releaseServices()
	if target.Kind == "service" && !retry {
		if err := maintenanceCommand(ctx, "systemctl", "is-active", "--quiet", target.Target); err != nil {
			return false, errors.New("service was not active; automatic start refused")
		}
	}
	a, claimed, err := claimMaintenanceActionWithHealth(db, target.Kind, target.Target, evidence, boot, maintenanceClock(), health)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, errMaintenanceBudgetSpent
	}
	if _, err := db.Exec(`UPDATE system_maintenance_actions SET round_id=? WHERE id=? AND attempts=?`, round.ID, a.ID, a.Attempts); err != nil {
		return false, err
	}
	if target.Kind == "service" {
		if retry && !present && !fresh.UnknownUnits[target.Target] && !fresh.UnknownUnits["*"] && maintenanceReady(ctx, cfg, target.Target) == nil {
			err = nil
		} else {
			err = restartMaintenanceService(ctx, cfg, target)
		}
		status, code := "succeeded", "completed"
		if err != nil {
			status, code = "failed", "service_verification_failed"
		}
		return false, finishMaintenanceAction(db, a, status, code, maintenanceClock())
	}
	if !needsReboot {
		status, code := "succeeded", "completed"
		if err := maintenanceHealthReady(ctx, cfg, health); err != nil {
			status, code = "failed", "post_reboot_health_failed"
		}
		return false, finishMaintenanceAction(db, a, status, code, maintenanceClock())
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = maintenanceCommand(requestCtx, "systemctl", "reboot")
	cancel()
	if err != nil {
		return false, finishMaintenanceAction(db, a, "failed", "reboot_request_failed", maintenanceClock())
	}
	// Do not release site/package locks immediately after systemctl returns:
	// the server may still be serving requests before shutdown actually starts.
	if err := maintenanceWaitReboot(context.WithoutCancel(ctx)); err != nil {
		return false, err
	}
	return false, finishMaintenanceAction(db, a, "failed", "reboot_not_observed", maintenanceClock())
}

func restartMaintenanceService(ctx context.Context, cfg *config.Config, target maintenanceTarget) error {
	switch target.Target {
	case "nginx.service":
		if err := maintenanceCommand(ctx, "nginx", "-t"); err != nil {
			return err
		}
	case "php8.3-fpm.service":
		if err := maintenanceCommand(ctx, "php-fpm8.3", "-t"); err != nil {
			return err
		}
	}
	if err := maintenanceCommand(ctx, "systemctl", "restart", target.Target); err != nil {
		return err
	}
	if err := pollServiceReady(func(check context.Context) error { return maintenanceReady(check, cfg, target.Target) }); err != nil {
		return err
	}
	after, err := maintenanceScan(ctx)
	if err != nil {
		return err
	}
	present, err := maintenanceServiceHasOldMappings(ctx, target.Target, after)
	if err != nil {
		return err
	}
	if present {
		return errors.New("old files still mapped")
	}
	return nil
}

func eligibleMaintenanceTargets(db *sql.DB, targets []maintenanceTarget, actions []systemMaintenanceAction) ([]maintenanceTarget, []maintenanceTarget, error) {
	result := make([]maintenanceTarget, 0, len(targets))
	var blocked []maintenanceTarget
	boot, err := systemPackageBootID()
	if err != nil {
		return nil, nil, err
	}
	for _, t := range targets {
		eligible := true
		for _, a := range actions {
			if a.Kind != t.Kind || a.Target != t.Target || a.Attempts == 0 || a.ManualRetry {
				continue
			}
			var old, completed []maintenanceRuntimeEvidence
			if json.Unmarshal([]byte(a.EvidenceJSON), &old) != nil || json.Unmarshal([]byte(a.LastSuccessJSON), &completed) != nil {
				eligible = false
				break
			}
			encoded, _ := json.Marshal(append(old, completed...))
			var evidence []maintenanceRuntimeEvidence
			for _, e := range t.Evidence {
				evidence = append(evidence, maintenanceRuntimeEvidence{Unit: e.Unit, Path: e.Path, Old: e.Old, Package: e.Package})
			}
			fresh, err := hasNewMaintenanceEvidence(string(encoded), evidence)
			if a.BootIDBefore != boot && len(evidence) > 0 {
				fresh = true
			}
			eligible = err == nil && fresh
			break
		}
		if eligible && t.Kind == "reboot" {
			var evidence []maintenanceRuntimeEvidence
			for _, e := range t.Evidence {
				evidence = append(evidence, maintenanceRuntimeEvidence{Path: e.Path, Old: e.Old})
			}
			fresh, err := freshMaintenanceRebootIntent(db, t.Target, evidence, boot)
			if err != nil {
				return nil, nil, err
			}
			eligible = fresh
		}
		if eligible {
			result = append(result, t)
		} else {
			blocked = append(blocked, t)
		}
	}
	return result, blocked, nil
}
