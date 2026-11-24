package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

type maintenanceRunnerFixture struct {
	now                              time.Time
	files                            []maintenanceRuntimeEvidence
	commands                         []string
	held, busy, keepOld, rebootError bool
	boot                             string
	installs                         int
}

func setupMaintenanceRunner(t *testing.T) (*config.Config, *maintenanceRunnerFixture) {
	t.Helper()
	enableMaintenanceForTest(t)
	cfg := &config.Config{Panel: config.PanelConfig{DataDir: t.TempDir()}}
	var seq int
	var name string
	if err := database.GetDB().QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &cfg.SQLite.Path); err != nil {
		t.Fatal(err)
	}
	oldCfg := config.AppConfig
	config.AppConfig = cfg
	oldCommand, oldScan, oldGate, oldReady := maintenanceCommand, maintenanceScan, maintenanceGate, maintenanceReady
	oldClock, oldPackages, oldDPKG, oldReboot, oldWait, oldStart := maintenanceClock, maintenancePackagesBusy, maintenanceDPKGQuery, maintenanceRebootTarget, maintenanceWaitReboot, maintenanceStartInstall
	oldBoot, oldObserver, oldLast := systemPackageBootID, maintenanceObserver, maintenanceLastScan
	oldOutput := systemPackageUpdateCommandOutput
	oldInspection := SnapshotSystemMaintenanceInspection()
	maintenanceInspectionMu.Lock()
	maintenanceInspection = SystemMaintenanceInspection{Status: "not_checked"}
	maintenanceInspectionMu.Unlock()
	t.Cleanup(func() {
		maintenanceInspectionMu.Lock()
		maintenanceInspection = oldInspection
		maintenanceInspectionMu.Unlock()
	})
	t.Cleanup(func() {
		config.AppConfig = oldCfg
		maintenanceCommand = oldCommand
		maintenanceScan = oldScan
		maintenanceGate = oldGate
		maintenanceReady = oldReady
		maintenanceClock = oldClock
		maintenancePackagesBusy = oldPackages
		maintenanceDPKGQuery = oldDPKG
		maintenanceRebootTarget = oldReboot
		maintenanceWaitReboot = oldWait
		maintenanceStartInstall = oldStart
		systemPackageBootID = oldBoot
		maintenanceObserver = oldObserver
		maintenanceLastScan = oldLast
		systemPackageUpdateCommandOutput = oldOutput
	})
	location, err := currentMaintenanceLocation()
	if err != nil {
		t.Fatal(err)
	}
	f := &maintenanceRunnerFixture{now: time.Date(2026, 11, 22, 4, 30, 0, 0, location), boot: "boot-A"}
	maintenanceClock = func() time.Time { return f.now }
	maintenanceObserver = maintenanceRuntimeObserver{}
	maintenanceLastScan = time.Time{}
	maintenancePackagesBusy = func(*config.Config) (bool, error) { return f.busy, nil }
	maintenanceDPKGQuery = func() (bool, error) { return f.busy, nil }
	maintenanceRebootTarget = func(context.Context) (string, error) { return "", nil }
	systemPackageBootID = func() (string, error) { return f.boot, nil }
	maintenanceScan = func(context.Context) (maintenanceRuntimeSnapshot, error) {
		return maintenanceRuntimeSnapshot{At: f.now, Evidence: append([]maintenanceRuntimeEvidence(nil), f.files...), UnknownUnits: map[string]bool{}}, nil
	}
	maintenanceGate = func(*config.Config, *sql.DB) (func(), string, error) {
		if f.busy {
			return nil, "file_backup_busy", nil
		}
		if f.held {
			t.Fatal("overlapping maintenance actions")
		}
		f.held = true
		return func() { f.held = false }, "", nil
	}
	maintenanceReady = func(context.Context, *config.Config, string) error { return nil }
	maintenanceCommand = func(_ context.Context, name string, args ...string) error {
		f.commands = append(f.commands, name+" "+strings.Join(args, " "))
		if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
			if !f.held {
				t.Fatal("restart without held gate")
			}
			if !f.keepOld {
				var next []maintenanceRuntimeEvidence
				for _, e := range f.files {
					if e.Unit != args[1] {
						next = append(next, e)
					}
				}
				f.files = next
			}
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "reboot" && f.rebootError {
			return errors.New("inhibited")
		}
		return nil
	}
	maintenanceWaitReboot = func(context.Context) error {
		if !f.held {
			t.Fatal("reboot locks released before shutdown")
		}
		return context.Canceled
	}
	maintenanceStartInstall = func(*config.Config, int64) (SystemPackageUpdateStatus, error) {
		f.installs++
		return SystemPackageUpdateStatus{Status: "running"}, nil
	}
	systemPackageUpdateCommandOutput = func(string, ...string) (string, error) { return "Listing... Done\n", nil }
	setSecuritySetting("alert_system_maintenance", "false")
	if err := SaveSystemMaintenancePolicy(database.GetDB(), SystemMaintenancePolicy{true, "scheduled", "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	return cfg, f
}

func maintenanceFixtureRound(t *testing.T, target maintenanceTarget, overtime bool) systemMaintenanceAction {
	t.Helper()
	r, err := createMaintenanceRound(database.GetDB(), []maintenanceTarget{target}, false, overtime)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMaintenanceServiceControlBusyDoesNotConsumeAttempt(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	f.files = []maintenanceRuntimeEvidence{e}
	target := maintenanceTarget{"service", e.Unit, compactMaintenanceEvidence(f.files)}
	round := maintenanceFixtureRound(t, target, true)
	release, err := tryServiceControlLocks(cfg.Panel.DataDir, e.Unit)
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, f.files, false)
	if err != nil || !deferred {
		t.Fatalf("deferred=%v %v", deferred, err)
	}
	var count int
	database.GetDB().QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE kind='service'`).Scan(&count)
	if count != 0 || strings.Contains(strings.Join(f.commands, " "), "restart") {
		t.Fatalf("busy attempt consumed: %d %v", count, f.commands)
	}
	release()
	if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, f.files, false); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := database.GetDB().QueryRow(`SELECT attempts FROM system_maintenance_actions WHERE kind='service'`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d %v", attempts, err)
	}
}

func TestMaintenanceServiceControlHeldThroughVerification(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	f.files = []maintenanceRuntimeEvidence{e}
	target := maintenanceTarget{"service", e.Unit, compactMaintenanceEvidence(f.files)}
	round := maintenanceFixtureRound(t, target, true)
	maintenanceReady = func(context.Context, *config.Config, string) error {
		if other, err := tryServiceControlLocks(cfg.Panel.DataDir, e.Unit); !errors.Is(err, errServiceControlBusy) {
			if other != nil {
				other()
			}
			t.Fatalf("ready without service lock: %v", err)
		}
		other, err := tryServiceControlLocks(cfg.Panel.DataDir, "mariadb")
		if err != nil {
			t.Fatalf("unrelated service blocked: %v", err)
		}
		other()
		return nil
	}
	if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, f.files, false); err != nil {
		t.Fatal(err)
	}
	release, err := tryServiceControlLocks(cfg.Panel.DataDir, e.Unit)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestMaintenanceRebootServiceLocksHeldThroughGrace(t *testing.T) {
	cfg, _ := setupMaintenanceRunner(t)
	oldRunning, oldGreater := systemPackageRunningKernel, systemPackageKernelGreater
	t.Cleanup(func() { systemPackageRunningKernel, systemPackageKernelGreater = oldRunning, oldGreater })
	systemPackageRunningKernel = func() (string, error) { return "6.12.115-amd64", nil }
	systemPackageKernelGreater = func(string, string) (bool, error) { return true, nil }
	target := maintenanceTarget{Kind: "reboot", Target: "kernel/6.12.117-amd64"}
	round := maintenanceFixtureRound(t, target, true)
	maintenanceWaitReboot = func(context.Context) error {
		for _, unit := range guardServiceNames {
			if other, err := tryServiceControlLocks(cfg.Panel.DataDir, unit); !errors.Is(err, errServiceControlBusy) {
				if other != nil {
					other()
				}
				t.Fatalf("reboot grace unlocked %s: %v", unit, err)
			}
		}
		return nil // Existing grace expires without observing reboot.
	}
	if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, nil, false); err != nil {
		t.Fatal(err)
	}
	var status, code string
	database.GetDB().QueryRow(`SELECT status,message_code FROM system_maintenance_actions WHERE kind='reboot'`).Scan(&status, &code)
	if status != "failed" || code != "reboot_not_observed" {
		t.Fatalf("%s %s", status, code)
	}
	release, err := tryServiceControlLocks(cfg.Panel.DataDir, guardServiceNames...)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestMaintenanceCycleHandlesExternalUpdateWithoutNewPackages(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	f.files = []maintenanceRuntimeEvidence{e}
	maintenanceObserver.previous = maintenanceRuntimeSnapshot{At: f.now.Add(-systemMaintenanceObservationInterval), Evidence: f.files}
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 0 || !strings.Contains(strings.Join(f.commands, "\n"), "systemctl restart nginx.service") {
		t.Fatalf("external update not converged: %v installs=%d", f.commands, f.installs)
	}
	var status string
	if err := database.GetDB().QueryRow(`SELECT status FROM system_maintenance_actions WHERE kind='service'`).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("status=%s %v", status, err)
	}
}

func TestMaintenanceInstallationOnlyStartsInsideWindow(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	systemPackageUpdateCommandOutput = func(string, ...string) (string, error) { return "nginx/stable 1.26 amd64\n", nil }
	f.now = f.now.Add(5 * time.Hour)
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 0 {
		t.Fatal("installation outside window")
	}
	f.now = f.now.Add(-5 * time.Hour)
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 1 {
		t.Fatalf("installs=%d", f.installs)
	}
	var count int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE kind='round' AND message_code='installing' AND overtime_allowed=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("install round: %d %v", count, err)
	}
}

func TestMaintenanceInstallationDoesNotDependOnRuntimeScanner(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	systemPackageUpdateCommandOutput = func(string, ...string) (string, error) { return "nginx/stable 1.26 amd64\n", nil }
	maintenanceScan = func(context.Context) (maintenanceRuntimeSnapshot, error) {
		t.Fatal("runtime inspection must not gate package installation")
		return maintenanceRuntimeSnapshot{}, errors.New("unreadable process")
	}
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 1 {
		t.Fatalf("installs=%d", f.installs)
	}
}

func TestMaintenanceScanFailureClosesRuntimeRoundAsUnknown(t *testing.T) {
	cfg, _ := setupMaintenanceRunner(t)
	round := maintenanceFixtureRound(t, maintenanceTarget{"service", "nginx.service", nil}, true)
	maintenanceScan = func(context.Context) (maintenanceRuntimeSnapshot, error) {
		return maintenanceRuntimeSnapshot{}, errors.New("unreadable process")
	}
	if err := runMaintenanceCycle(context.Background(), cfg, true); err == nil {
		t.Fatal("expected inspection error")
	}
	var status string
	var overtime bool
	if err := database.GetDB().QueryRow(`SELECT status,overtime_allowed FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&status, &overtime); err != nil {
		t.Fatal(err)
	}
	if status != "unknown" || overtime {
		t.Fatalf("status=%s overtime=%v", status, overtime)
	}
}

func TestMaintenanceBusyBeforeScanRevokesRoundOvertime(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	round := maintenanceFixtureRound(t, maintenanceTarget{"service", "nginx.service", nil}, true)
	f.busy = true
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	var overtime bool
	var reason string
	if err := database.GetDB().QueryRow(`SELECT overtime_allowed,message_code FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&overtime, &reason); err != nil {
		t.Fatal(err)
	}
	if overtime || reason != "package_manager_busy" {
		t.Fatalf("overtime=%v reason=%s", overtime, reason)
	}
}

func TestMaintenanceFrozenRoundDoesNotAddOutsideEvidence(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	old := maintenanceTestEvidence(1)
	newFile := maintenanceTestEvidence(1)
	newFile.Unit = "redis-server.service"
	maintenanceFixtureRound(t, maintenanceTarget{"service", "nginx.service", compactMaintenanceEvidence([]maintenanceRuntimeEvidence{old})}, true)
	f.now = f.now.Add(5 * time.Hour)
	f.files = []maintenanceRuntimeEvidence{old, newFile}
	maintenanceObserver.previous = maintenanceRuntimeSnapshot{At: f.now.Add(-systemMaintenanceObservationInterval), Evidence: f.files}
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.commands, "\n")
	if !strings.Contains(joined, "restart nginx.service") || strings.Contains(joined, "restart redis-server.service") {
		t.Fatalf("frozen scope bypassed: %s", joined)
	}
	f.now = f.now.Add(systemMaintenanceObservationInterval)
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	var status string
	database.GetDB().QueryRow(`SELECT status FROM system_maintenance_actions WHERE kind='round'`).Scan(&status)
	if status != "succeeded" {
		t.Fatalf("old round not finished: %s", status)
	}
}

func TestMaintenanceBusyDeferralRevokesOvertime(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	f.files = []maintenanceRuntimeEvidence{e}
	target := maintenanceTarget{"service", "nginx.service", compactMaintenanceEvidence(f.files)}
	round := maintenanceFixtureRound(t, target, true)
	f.busy = true
	deferred, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, f.files, false)
	if err != nil || !deferred || round.OvertimeAllowed {
		t.Fatalf("deferral: %v %v %+v", deferred, err, round)
	}
	f.busy = false
	f.now = f.now.Add(5 * time.Hour)
	if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, f.files, false); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 0 {
		t.Fatalf("deferred round executed in daytime: %v", f.commands)
	}
}

func TestMaintenanceFailedServiceDoesNotRepeatWhenEvidenceShrinks(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	f.keepOld = true
	one, two := maintenanceTestEvidence(1), maintenanceTestEvidence(2)
	f.files = []maintenanceRuntimeEvidence{one, two}
	target := maintenanceTarget{"service", "nginx.service", compactMaintenanceEvidence(f.files)}
	round := maintenanceFixtureRound(t, target, true)
	if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, []maintenanceRuntimeEvidence{one}, false); err != nil {
		t.Fatal(err)
	}
	f.files = []maintenanceRuntimeEvidence{two}
	if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, f.files, false); !errors.Is(err, errMaintenanceBudgetSpent) {
		t.Fatal(err)
	}
	if strings.Count(strings.Join(f.commands, "\n"), "systemctl restart nginx.service") != 1 {
		t.Fatalf("failed target repeated: %v", f.commands)
	}
}

func TestMaintenanceRebootRecoveryVerifiesKernelAndKeepsBudget(t *testing.T) {
	for _, oldKernel := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_kernel", true: "old_kernel"}[oldKernel], func(t *testing.T) {
			cfg, f := setupMaintenanceRunner(t)
			key := "kernel/6.12.117-amd64"
			target := maintenanceTarget{Kind: "reboot", Target: key}
			round := maintenanceFixtureRound(t, target, true)
			oldRunning, oldGreater := systemPackageRunningKernel, systemPackageKernelGreater
			t.Cleanup(func() { systemPackageRunningKernel = oldRunning; systemPackageKernelGreater = oldGreater })
			systemPackageRunningKernel = func() (string, error) {
				if oldKernel {
					return "6.12.115-amd64", nil
				}
				return "6.12.117-amd64", nil
			}
			systemPackageKernelGreater = func(a, b string) (bool, error) { return a > b, nil }
			// Before reboot, the target is not reached.
			systemPackageRunningKernel = func() (string, error) { return "6.12.115-amd64", nil }
			if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, nil, false); !errors.Is(err, context.Canceled) {
				t.Fatalf("reboot request: %v", err)
			}
			f.boot = "boot-B"
			systemPackageRunningKernel = func() (string, error) {
				if oldKernel {
					return "6.12.115-amd64", nil
				}
				return "6.12.117-amd64", nil
			}
			actions, err := loadMaintenanceActions(database.GetDB())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recoverMaintenanceActions(context.Background(), cfg, actions); err != nil {
				t.Fatal(err)
			}
			var status string
			database.GetDB().QueryRow(`SELECT status FROM system_maintenance_actions WHERE kind='reboot'`).Scan(&status)
			want := "succeeded"
			if oldKernel {
				want = "failed"
			}
			if status != want {
				t.Fatalf("status=%s want=%s", status, want)
			}
			if _, ok, err := claimMaintenanceAction(database.GetDB(), "reboot", key, nil, f.boot, f.now); err != nil || ok {
				t.Fatalf("reboot budget reset: %v %v", ok, err)
			}
			if strings.Count(strings.Join(f.commands, "\n"), "systemctl reboot") != 1 {
				t.Fatalf("reboot repeated: %v", f.commands)
			}
		})
	}
}

func TestMaintenanceKernelRebootSurvivesDisappearingSessionEvidence(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	session := maintenanceTestEvidence(1)
	session.Unit = "user@0.service"
	key := "kernel/6.12.117-amd64"
	round := maintenanceFixtureRound(t, maintenanceTarget{"reboot", key, compactMaintenanceEvidence([]maintenanceRuntimeEvidence{session})}, true)
	oldRunning, oldGreater := systemPackageRunningKernel, systemPackageKernelGreater
	t.Cleanup(func() { systemPackageRunningKernel = oldRunning; systemPackageKernelGreater = oldGreater })
	running := "6.12.115-amd64"
	systemPackageRunningKernel = func() (string, error) { return running, nil }
	systemPackageKernelGreater = func(a, b string) (bool, error) { return a > b, nil }
	maintenanceRebootTarget = func(context.Context) (string, error) {
		if running == "6.12.115-amd64" {
			return key, nil
		}
		return "", nil
	}
	// The session disappeared before a second stable observation. The kernel
	// target remains independently actionable without any runtime evidence.
	if err := runMaintenanceCycle(context.Background(), cfg, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("reboot request: %v", err)
	}
	f.boot, running = "boot-B", "6.12.117-amd64"
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := database.GetDB().QueryRow(`SELECT status FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("round stranded: %s %v", status, err)
	}
	if strings.Count(strings.Join(f.commands, "\n"), "systemctl reboot") != 1 {
		t.Fatalf("reboot count: %v", f.commands)
	}
	f.now = f.now.AddDate(0, 0, 1)
	systemPackageUpdateCommandOutput = func(string, ...string) (string, error) { return "nginx/stable 1.26 amd64\n", nil }
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil || f.installs != 1 {
		t.Fatalf("future installation blocked: installs=%d %v", f.installs, err)
	}
}

func TestMaintenanceManualRetryRoundKeepsFailureReason(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	f.files, f.keepOld = []maintenanceRuntimeEvidence{e}, true
	a, ok, err := claimMaintenanceAction(database.GetDB(), "service", e.Unit, f.files, f.boot, f.now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := finishMaintenanceAction(database.GetDB(), a, "failed", "service_verification_failed", f.now); err != nil {
		t.Fatal(err)
	}
	if err := requestMaintenanceActionRetry(database.GetDB(), a.ID); err != nil {
		t.Fatal(err)
	}
	actions, err := loadMaintenanceActions(database.GetDB())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := maintenanceRuntimeSnapshot{At: f.now, Evidence: f.files, UnknownUnits: map[string]bool{}}
	if err := runManualMaintenanceRetry(context.Background(), cfg, actions, snapshot, f.files); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err := database.GetDB().QueryRow(`SELECT status,message_code FROM system_maintenance_actions WHERE kind='round' AND target LIKE 'manual/%'`).Scan(&status, &code); err != nil || status != "failed" || code != "service_verification_failed" {
		t.Fatalf("retry round: %s %s %v", status, code, err)
	}
}

func TestMaintenanceInspectionSeparatesRuntimeUnknownFromOtherAttention(t *testing.T) {
	_, f := setupMaintenanceRunner(t)
	for _, tc := range []struct {
		unit, status string
		unknown      int
	}{
		{"action_budget", "attention", 0},
		{"kernel_or_marker", "reboot_unknown", 0},
		{"nginx.service", "unknown", 1},
	} {
		recordMaintenanceInspection(maintenanceRuntimeSnapshot{At: f.now, UnknownUnits: map[string]bool{tc.unit: true}}, nil)
		got := SnapshotSystemMaintenanceInspection()
		if got.Status != tc.status || got.UnknownUnits != tc.unknown {
			t.Fatalf("%s: %+v", tc.unit, got)
		}
		kind, message := maintenanceInspectionNotice(got, nil)
		if (kind != "") != (tc.unknown == 0) || strings.Contains(message, "运行检查无法确认") {
			t.Fatalf("misleading urgent alert for %s: %s %s", tc.unit, kind, message)
		}
	}
}

func TestMaintenanceRebootCommandErrorDoesNotWait(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	f.rebootError = true
	oldRunning, oldGreater := systemPackageRunningKernel, systemPackageKernelGreater
	t.Cleanup(func() { systemPackageRunningKernel = oldRunning; systemPackageKernelGreater = oldGreater })
	systemPackageRunningKernel = func() (string, error) { return "6.12.115-amd64", nil }
	systemPackageKernelGreater = func(string, string) (bool, error) { return true, nil }
	target := maintenanceTarget{Kind: "reboot", Target: "kernel/6.12.117-amd64"}
	round := maintenanceFixtureRound(t, target, true)
	maintenanceWaitReboot = func(context.Context) error { t.Fatal("rejected reboot waited for grace period"); return nil }
	if _, err := executeMaintenanceTarget(context.Background(), cfg, &round, target, nil, false); err != nil {
		t.Fatal(err)
	}
	var status, code string
	database.GetDB().QueryRow(`SELECT status,message_code FROM system_maintenance_actions WHERE kind='reboot'`).Scan(&status, &code)
	if status != "failed" || code != "reboot_request_failed" {
		t.Fatalf("%s %s", status, code)
	}
}

func TestMaintenanceManualRetryVerifiesAlreadyRepairedServiceWithoutRestart(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	a, ok, err := claimMaintenanceAction(database.GetDB(), "service", "nginx.service", []maintenanceRuntimeEvidence{e}, f.boot, f.now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := finishMaintenanceAction(database.GetDB(), a, "failed", "service_verification_failed", f.now); err != nil {
		t.Fatal(err)
	}
	if err := requestMaintenanceActionRetry(database.GetDB(), a.ID); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(5 * time.Hour)
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	var status string
	database.GetDB().QueryRow(`SELECT status FROM system_maintenance_actions WHERE id=?`, a.ID).Scan(&status)
	if status != "succeeded" || strings.Contains(strings.Join(f.commands, "\n"), "restart") {
		t.Fatalf("retry result: %s %v", status, f.commands)
	}
}

func TestMaintenanceSealedRoundAndConsecutiveDeferralWindows(t *testing.T) {
	_, f := setupMaintenanceRunner(t)
	e := maintenanceTestEvidence(1)
	f.files = []maintenanceRuntimeEvidence{e}
	round, err := createMaintenanceRound(database.GetDB(), nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := sealMaintenanceRound(context.Background(), database.GetDB(), round.ID); err != nil {
		t.Fatal(err)
	}
	var encoded string
	database.GetDB().QueryRow(`SELECT evidence_json FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&encoded)
	var scope []maintenanceTarget
	if err := json.Unmarshal([]byte(encoded), &scope); err != nil || len(scope) != 1 || scope[0].Target != "nginx.service" {
		t.Fatalf("seal: %s %v", encoded, err)
	}
	p := SystemMaintenancePolicy{true, "scheduled", "04:00-06:00"}
	for day := 0; day < 3; day++ {
		for tick := 0; tick < 2; tick++ {
			if err := deferMaintenanceRoundAt(database.GetDB(), &round, "file_backup_busy", f.now.AddDate(0, 0, day).Add(time.Duration(tick)*10*time.Minute), p, f.now.Location()); err != nil {
				t.Fatal(err)
			}
		}
	}
	var count int
	database.GetDB().QueryRow(`SELECT deferred_windows FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&count)
	if count != 3 {
		t.Fatalf("deferral counted ticks instead of windows: %d", count)
	}
}

func TestMaintenancePolicyDisabledNeverRunsAction(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	f.files = []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}
	if err := SaveSystemMaintenancePolicy(database.GetDB(), SystemMaintenancePolicy{false, "immediate", "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 0 || f.installs != 0 {
		t.Fatal("disabled policy caused an effect")
	}
}
