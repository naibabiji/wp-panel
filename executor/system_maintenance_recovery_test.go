package executor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func TestMaintenanceExternalResolutionRequiresReadinessAndDoesNotRetry(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	db := database.GetDB()
	e := maintenanceTestEvidence(1)
	a, ok, err := claimMaintenanceAction(db, "service", "nginx.service", []maintenanceRuntimeEvidence{e}, f.boot, f.now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := finishMaintenanceAction(db, a, "failed", "service_verification_failed", f.now); err != nil {
		t.Fatal(err)
	}
	actions, _ := loadMaintenanceActions(db)
	clear := maintenanceRuntimeSnapshot{At: f.now, UnknownUnits: map[string]bool{}}
	maintenanceReady = func(context.Context, *config.Config, string) error { return errors.New("service not ready") }
	if err := resolveExternallyCompletedMaintenance(context.Background(), cfg, actions, clear); err != nil {
		t.Fatal(err)
	}
	var status string
	db.QueryRow(`SELECT status FROM system_maintenance_actions WHERE id=?`, a.ID).Scan(&status)
	if status != "failed" {
		t.Fatal("absence alone resolved failed service")
	}
	maintenanceReady = func(context.Context, *config.Config, string) error { return nil }
	if err := resolveExternallyCompletedMaintenance(context.Background(), cfg, actions, clear); err != nil {
		t.Fatal(err)
	}
	var code string
	var attempts int
	db.QueryRow(`SELECT status,message_code,attempts FROM system_maintenance_actions WHERE id=?`, a.ID).Scan(&status, &code, &attempts)
	if status != "succeeded" || code != "externally_resolved" || attempts != 1 || len(f.commands) != 0 {
		t.Fatalf("resolution changed action budget or executed: %s %s %d %v", status, code, attempts, f.commands)
	}
	if _, ok, err := claimMaintenanceAction(db, "service", "nginx.service", []maintenanceRuntimeEvidence{e}, f.boot, f.now); err != nil || ok {
		t.Fatal("resolved old evidence reacquired budget")
	}
}

func TestMaintenanceRebootDoesNotCompleteWithUnreadyCoreService(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	db := database.GetDB()
	_, ok, err := claimMaintenanceActionWithHealth(db, "reboot", "kernel/6.12.117-amd64", nil, "boot-A", f.now.Add(-time.Hour), []string{"nginx.service"})
	if err != nil || !ok {
		t.Fatal(err)
	}
	f.boot = "boot-B"
	oldRunning, oldGreater := systemPackageRunningKernel, systemPackageKernelGreater
	oldTimeout, oldPoll := safeApplyReadinessPollTimeout, safeApplyReadinessPollInterval
	t.Cleanup(func() {
		systemPackageRunningKernel = oldRunning
		systemPackageKernelGreater = oldGreater
		safeApplyReadinessPollTimeout = oldTimeout
		safeApplyReadinessPollInterval = oldPoll
	})
	systemPackageRunningKernel = func() (string, error) { return "6.12.117-amd64", nil }
	systemPackageKernelGreater = func(string, string) (bool, error) { return false, nil }
	safeApplyReadinessPollTimeout = 5 * time.Millisecond
	safeApplyReadinessPollInterval = time.Millisecond
	maintenanceReady = func(context.Context, *config.Config, string) error { return errors.New("nginx down") }
	actions, _ := loadMaintenanceActions(db)
	if _, err := recoverMaintenanceActions(context.Background(), cfg, actions); err != nil {
		t.Fatal(err)
	}
	var status string
	db.QueryRow(`SELECT status FROM system_maintenance_actions WHERE kind='reboot'`).Scan(&status)
	if status == "succeeded" {
		t.Fatal("new boot/kernel falsely proved core readiness")
	}
}

func TestMaintenanceCronChildrenAndUserServicesBecomeRebootEvidence(t *testing.T) {
	setupMaintenanceRunner(t)
	oldPID := maintenanceUnitMainPID
	t.Cleanup(func() { maintenanceUnitMainPID = oldPID })
	maintenanceUnitMainPID = func(context.Context, string) (int, error) { return 123, nil }
	main, child, user := maintenanceTestEvidence(1), maintenanceTestEvidence(2), maintenanceTestEvidence(3)
	main.Unit = "cron.service"
	child.Unit = "cron.service"
	child.PID = 124
	user.Unit = "user@0.service"
	snapshot := maintenanceRuntimeSnapshot{Evidence: []maintenanceRuntimeEvidence{main, child, user}, UnknownUnits: map[string]bool{}}
	targets, err := classifyMaintenanceTargets(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Kind != "service" || targets[0].Target != "cron.service" || len(targets[0].Evidence) != 1 || targets[1].Kind != "reboot" || len(targets[1].Evidence) != 2 {
		t.Fatalf("incorrect classification: %+v", targets)
	}
	child.Old = main.Old
	after := maintenanceRuntimeSnapshot{Evidence: []maintenanceRuntimeEvidence{child}, UnknownUnits: map[string]bool{}}
	if present, err := maintenanceTargetStillPresent(targets[0], after); err != nil || present {
		t.Fatalf("old child falsely invalidated new cron MainPID: %v %v", present, err)
	}
}

func TestMaintenanceAutomaticInstallFailureDoesNotLoopEveryTick(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	systemPackageUpdateCommandOutput = func(string, ...string) (string, error) { return "nginx/stable 1.26 amd64\n", nil }
	maintenanceStartInstall = func(*config.Config, int64) (SystemPackageUpdateStatus, error) {
		f.installs++
		return SystemPackageUpdateStatus{}, errors.New("cannot start")
	}
	if err := runMaintenanceCycle(context.Background(), cfg, true); err == nil {
		t.Fatal("missing install start error")
	}
	f.now = f.now.Add(10 * time.Minute)
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 1 {
		t.Fatal("installation failure retried in every tick")
	}
}

func TestMaintenancePackageRunnerSealsScopeAndSkipsInactiveRedisHealth(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	db := database.GetDB()
	round, err := createMaintenanceRound(db, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(cfg.Panel.DataDir, "plan.json")
	plan := systemPackageUpdatePlan{ID: "auto-fixture", PlanPath: planPath, StatusPath: SystemPackageUpdateStatusPath(cfg), MaintenanceRoundID: round.ID, MaintenanceDBPath: cfg.SQLite.Path}
	if err := writePanelDBRestoreJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	oldCommand, oldPreflight, oldRequired := systemPackageUpdateCommand, systemPackageUpdatePreflight, systemPackageRebootRequired
	t.Cleanup(func() {
		systemPackageUpdateCommand = oldCommand
		systemPackageUpdatePreflight = oldPreflight
		systemPackageRebootRequired = oldRequired
	})
	redisChecks := 0
	systemPackageUpdateCommand = func(_ context.Context, name string, args ...string) error {
		if name == "systemctl" && args[len(args)-1] == "redis-server" {
			redisChecks++
			return errors.New("intentionally stopped")
		}
		return nil
	}
	systemPackageUpdatePreflight = func(context.Context) (string, error) {
		return "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", nil
	}
	systemPackageRebootRequired = func() (bool, error) { return false, nil }
	if err := RunSystemPackageUpdatePlan(planPath); err != nil {
		t.Fatal(err)
	}
	if redisChecks != 1 {
		t.Fatalf("inactive Redis was required after install: %d", redisChecks)
	}
	var code string
	db.QueryRow(`SELECT message_code FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&code)
	if code != "ready" || ReadSystemPackageUpdateStatus(cfg).Status != "success" || f.installs != 0 {
		t.Fatalf("runner failed to seal: %s", code)
	}
}

func TestMaintenanceUnknownKernelDoesNotDiscardKnownServiceEvidence(t *testing.T) {
	setupMaintenanceRunner(t)
	maintenanceRebootTarget = func(context.Context) (string, error) { return "", errors.New("kernel unknown") }
	snapshot := maintenanceRuntimeSnapshot{Evidence: []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}, UnknownUnits: map[string]bool{}}
	targets, err := classifyMaintenanceTargets(context.Background(), snapshot)
	if err != nil || len(targets) != 1 || targets[0].Target != "nginx.service" || !snapshot.UnknownUnits["kernel_or_marker"] {
		t.Fatalf("kernel warning lost independent service evidence: %+v %v", targets, err)
	}
}

func TestMaintenanceSealFailurePreservesSuccessfulInstallationAndRebootHint(t *testing.T) {
	for _, failure := range []string{"scan_error", "global_unknown", "classification_error"} {
		t.Run(failure, func(t *testing.T) {
			cfg, f := setupMaintenanceRunner(t)
			db := database.GetDB()
			round, err := createMaintenanceRound(db, nil, true, true)
			if err != nil {
				t.Fatal(err)
			}
			planPath := filepath.Join(cfg.Panel.DataDir, "plan.json")
			plan := systemPackageUpdatePlan{ID: "seal-failure", PlanPath: planPath, StatusPath: SystemPackageUpdateStatusPath(cfg), MaintenanceRoundID: round.ID, MaintenanceDBPath: cfg.SQLite.Path}
			if err := writePanelDBRestoreJSON(planPath, plan); err != nil {
				t.Fatal(err)
			}
			oldCommand, oldPreflight, oldRequired, oldPID := systemPackageUpdateCommand, systemPackageUpdatePreflight, systemPackageRebootRequired, maintenanceUnitMainPID
			t.Cleanup(func() {
				systemPackageUpdateCommand, systemPackageUpdatePreflight, systemPackageRebootRequired, maintenanceUnitMainPID = oldCommand, oldPreflight, oldRequired, oldPID
			})
			systemPackageUpdateCommand = func(context.Context, string, ...string) error { return nil }
			systemPackageUpdatePreflight = func(context.Context) (string, error) {
				return "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", nil
			}
			systemPackageRebootRequired = func() (bool, error) { return true, nil }
			maintenanceScan = func(context.Context) (maintenanceRuntimeSnapshot, error) {
				snapshot := maintenanceRuntimeSnapshot{At: f.now, UnknownUnits: map[string]bool{}}
				switch failure {
				case "scan_error":
					return snapshot, errors.New("scan timeout")
				case "global_unknown":
					snapshot.UnknownUnits["*"] = true
				case "classification_error":
					e := maintenanceTestEvidence(1)
					e.Unit = "cron.service"
					snapshot.Evidence = []maintenanceRuntimeEvidence{e}
				}
				return snapshot, nil
			}
			maintenanceUnitMainPID = func(context.Context, string) (int, error) { return 0, errors.New("MainPID unreadable") }
			if err := RunSystemPackageUpdatePlan(planPath); err != nil {
				t.Fatal(err)
			}
			status := ReadSystemPackageUpdateStatus(cfg)
			if status.Status != "success" || !status.RebootRequired || status.BootID != f.boot || status.MessageKey != "settings.system_update_status_runtime_unknown" {
				t.Fatalf("successful installation lost: %+v", status)
			}
			var state, code, phase string
			var overtime bool
			if err := db.QueryRow(`SELECT status,message_code,phase,overtime_allowed FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&state, &code, &phase, &overtime); err != nil || state != "unknown" || code != "runtime_unknown" || phase != "runtime" || overtime {
				t.Fatalf("runtime result: %s %s %s %v %v", state, code, phase, overtime, err)
			}
		})
	}
}
