package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

const systemPackageUpdateStatusFile = "system-package-update-status.json"
const systemPackageKernelCommandTimeout = 5 * time.Second
const systemPackageIndexRefreshTimeout = 2 * time.Minute
const systemPackageAutoCheckInterval = 24 * time.Hour
const systemPackageAutoCheckStartDelay = 2 * time.Minute

type SystemPackageUpdateStatus struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	Stage           string   `json:"stage"`
	MessageKey      string   `json:"message_key"`
	StartedAt       string   `json:"started_at,omitempty"`
	UpdatedAt       string   `json:"updated_at"`
	RemovalPackages []string `json:"removal_packages,omitempty"`
	RebootRequired  bool     `json:"reboot_required,omitempty"`
	BootID          string   `json:"boot_id,omitempty"`
}

type systemPackageUpdatePlan struct {
	ID                 string `json:"id"`
	StatusPath         string `json:"status_path"`
	PlanPath           string `json:"plan_path"`
	MaintenanceRoundID int64  `json:"maintenance_round_id,omitempty"`
	MaintenanceDBPath  string `json:"maintenance_db_path,omitempty"`
}

var (
	errSystemPackageUpdateBusy    = errors.New("系统更新正在执行")
	systemPackageUpdateCommand    = runSystemPackageUpdateCommand
	systemPackageUpdatePreflight  = runSystemPackageUpdatePreflight
	systemPackageRebootRequired   = detectSystemPackageRebootRequired
	systemPackageRebootMarker     = rebootRequiredMarkerExists
	systemPackageRunningKernel    = runningKernelRelease
	systemPackageInstalledKernels = installedKernelReleases
	systemPackageKernelGreater    = kernelReleaseGreater
	systemPackageBootID           = currentBootID
	systemPackageUpdateUnitLive   = func(id string) bool {
		return exec.Command("systemctl", "is-active", "--quiet", "wp-panel-system-update-"+id).Run() == nil
	}
	systemPackageUpdateLockPath = "/run/lock/wp-panel-system-update.lock"
	systemPackageUpdateStartMu  sync.Mutex
	systemPackageAutoCheckOnce  sync.Once
	startSystemPackageUpdate    = StartSystemPackageUpdate
)

// StartSystemPackageAutoUpdateScheduler refreshes APT indexes once per day,
// independently of dashboard visits. Package installation is opt-in and uses
// the same detached, guarded full-upgrade worker as the manual action.
func StartSystemPackageAutoUpdateScheduler(cfg *config.Config) {
	systemPackageAutoCheckOnce.Do(func() {
		go func() {
			time.Sleep(systemPackageAutoCheckStartDelay)
			runSystemPackageAutoCheck(cfg)
			runSystemMaintenanceTick(cfg, false)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					runSystemPackageAutoCheck(cfg)
					runSystemMaintenanceTick(cfg, false)
				case <-maintenanceWake:
					runSystemMaintenanceTick(cfg, true)
				}
			}
		}()
	})
}

func runSystemPackageAutoCheck(cfg *config.Config) {
	if cfg == nil || !systemPackageAutoCheckDue(time.Now()) {
		return
	}
	if status := ReconcileSystemPackageUpdateStatus(cfg); status.Status == "running" {
		return
	}
	started := time.Now().UTC()
	setSecuritySetting("system_package_auto_check_at", started.Format(time.RFC3339))
	setSecuritySetting("system_package_auto_check_status", "running")
	setSecuritySetting("system_package_auto_check_error", "")
	if err := RefreshSystemPackageIndexes(); err != nil {
		log.Printf("系统软件包每日刷新软件源失败: %v", err)
		recordSystemPackageAutoCheck("failed", 0, "刷新软件源失败，请检查服务器网络和软件源配置")
		return
	}
	ClearSystemUpdateAlertCache()
	output, err := systemPackageUpdateCommandOutput("apt", "list", "--upgradable")
	if err != nil {
		log.Printf("系统软件包每日读取更新列表失败: %v", err)
		recordSystemPackageAutoCheck("failed", 0, "读取更新列表失败，请检查服务器软件包状态")
		return
	}
	count := countAPTUpgradablePackages(output)
	setSecuritySetting("system_package_auto_check_count", strconv.Itoa(count))
	if count == 0 {
		recordSystemPackageAutoCheck("up_to_date", 0, "")
		return
	}
	recordSystemPackageAutoCheck("updates_available", count, "")
}

func systemPackageAutoCheckDue(now time.Time) bool {
	last, err := time.Parse(time.RFC3339, readSecuritySetting("system_package_auto_check_at"))
	return err != nil || now.Sub(last) >= systemPackageAutoCheckInterval
}

func recordSystemPackageAutoCheck(status string, count int, message string) {
	setSecuritySetting("system_package_auto_check_status", status)
	setSecuritySetting("system_package_auto_check_count", strconv.Itoa(count))
	setSecuritySetting("system_package_auto_check_error", message)
	if message != "" {
		log.Printf("系统软件包每日检查失败或未启动更新: %s", message)
	}
}

func countAPTUpgradablePackages(output string) int {
	return len(parseAPTUpgradableNames(output))
}

// parseAPTUpgradableNames accepts only apt's package-list rows. The heading
// printed by apt is localized (for example "Listing..." or "正在列表...") and
// must never be treated as a package name.
func parseAPTUpgradableNames(output string) []string {
	names := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		packageToken := fields[0]
		packageName, _, ok := strings.Cut(packageToken, "/")
		if !ok || packageName == "" {
			continue
		}
		names = append(names, packageName)
	}
	return names
}

// systemPackageUpdateCommandOutput captures standard output only so apt
// warnings on stderr never leak into the upgradable-package count.
var systemPackageUpdateCommandOutput = func(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

func SystemPackageUpdateStatusPath(cfg *config.Config) string {
	return filepath.Join(cfg.Panel.DataDir, systemPackageUpdateStatusFile)
}

func ReadSystemPackageUpdateStatus(cfg *config.Config) SystemPackageUpdateStatus {
	status := SystemPackageUpdateStatus{Status: "idle"}
	if cfg == nil {
		return status
	}
	data, err := os.ReadFile(SystemPackageUpdateStatusPath(cfg))
	if err == nil {
		_ = json.Unmarshal(data, &status)
	}
	return status
}

// ReconcileSystemPackageUpdateStatus converts a stale running state left by an
// interrupted detached process into an explicit failure visible to the UI.
func ReconcileSystemPackageUpdateStatus(cfg *config.Config) SystemPackageUpdateStatus {
	status := ReadSystemPackageUpdateStatus(cfg)
	if status.Status == "success" && status.RebootRequired {
		clearReboot := false
		if currentBootID, err := systemPackageBootID(); err == nil {
			if status.BootID != "" && currentBootID != status.BootID {
				if required, checkErr := systemPackageRebootRequired(); checkErr == nil {
					clearReboot = !required
				}
			}
		}
		if status.BootID == "" {
			if rebootRequired, err := systemPackageRebootRequired(); err == nil {
				clearReboot = !rebootRequired
			}
		}
		if clearReboot {
			status.RebootRequired = false
			status.BootID = ""
			status.MessageKey = "settings.system_update_status_success"
			status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			_ = writePanelDBRestoreJSON(SystemPackageUpdateStatusPath(cfg), status)
		}
	}
	if status.Status != "running" || status.ID == "" || systemPackageUpdateUnitLive(status.ID) {
		return status
	}
	startedAt, err := time.Parse(time.RFC3339, status.StartedAt)
	if err == nil && time.Since(startedAt) < 10*time.Second {
		return status
	}
	status.Status = "failed"
	status.Stage = "interrupted"
	status.MessageKey = "settings.system_update_status_interrupted"
	status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = writePanelDBRestoreJSON(SystemPackageUpdateStatusPath(cfg), status)
	return status
}

func StartSystemPackageUpdate(cfg *config.Config) (SystemPackageUpdateStatus, error) {
	return startSystemPackageUpdateForRound(cfg, 0)
}

func startMaintenancePackageUpdate(cfg *config.Config, roundID int64) (SystemPackageUpdateStatus, error) {
	return startSystemPackageUpdateForRound(cfg, roundID)
}

func startSystemPackageUpdateForRound(cfg *config.Config, roundID int64) (SystemPackageUpdateStatus, error) {
	systemPackageUpdateStartMu.Lock()
	defer systemPackageUpdateStartMu.Unlock()

	if cfg == nil {
		return SystemPackageUpdateStatus{}, errors.New("panel config unavailable")
	}
	// Admission probe only; the detached runner remains the lock owner during
	// installation. Holding this fd through systemd-run would race its startup.
	probe, probeErr := tryMaintenanceFileLock(systemPackageUpdateLockPath)
	if errors.Is(probeErr, syscall.EWOULDBLOCK) || errors.Is(probeErr, syscall.EAGAIN) {
		return SystemPackageUpdateStatus{}, errSystemPackageUpdateBusy
	}
	if probeErr != nil {
		return SystemPackageUpdateStatus{}, errors.New("system package lock unavailable")
	}
	probe.Close()
	current := ReconcileSystemPackageUpdateStatus(cfg)
	if current.Status == "running" {
		return SystemPackageUpdateStatus{}, errSystemPackageUpdateBusy
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return SystemPackageUpdateStatus{}, errors.New("systemd-run unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		return SystemPackageUpdateStatus{}, err
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	planPath := filepath.Join(cfg.Panel.DataDir, "system-package-update-"+id+".json")
	planPath, err = filepath.Abs(planPath)
	if err != nil {
		return SystemPackageUpdateStatus{}, err
	}
	plan := systemPackageUpdatePlan{ID: id, StatusPath: SystemPackageUpdateStatusPath(cfg), PlanPath: planPath}
	plan.StatusPath, err = filepath.Abs(plan.StatusPath)
	if err != nil {
		return SystemPackageUpdateStatus{}, err
	}
	if roundID > 0 {
		if strings.TrimSpace(cfg.SQLite.Path) == "" {
			return SystemPackageUpdateStatus{}, errors.New("maintenance database path unavailable")
		}
		plan.MaintenanceRoundID = roundID
		plan.MaintenanceDBPath, err = filepath.Abs(cfg.SQLite.Path)
		if err != nil {
			return SystemPackageUpdateStatus{}, err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	status := SystemPackageUpdateStatus{ID: id, Status: "running", Stage: "queued", MessageKey: "settings.system_update_status_queued", StartedAt: now, UpdatedAt: now}
	if err := writePanelDBRestoreJSON(planPath, plan); err != nil {
		return SystemPackageUpdateStatus{}, err
	}
	if err := writePanelDBRestoreJSON(plan.StatusPath, status); err != nil {
		_ = os.Remove(planPath)
		return SystemPackageUpdateStatus{}, err
	}
	out, err := exec.Command("systemd-run", "--unit", "wp-panel-system-update-"+id, "--collect", "--property", "Type=exec", executable, "--system-package-update-plan", planPath).CombinedOutput()
	if err != nil {
		status.Status, status.Stage, status.MessageKey, status.UpdatedAt = "failed", "start", "settings.system_update_status_start_failed", time.Now().UTC().Format(time.RFC3339)
		_ = writePanelDBRestoreJSON(plan.StatusPath, status)
		_ = os.Remove(planPath)
		return SystemPackageUpdateStatus{}, fmt.Errorf("启动系统更新失败: %s", strings.TrimSpace(string(out)))
	}
	return status, nil
}

// RefreshSystemPackageIndexes updates APT metadata only; it never installs packages.
func RefreshSystemPackageIndexes() error {
	ctx, cancel := context.WithTimeout(context.Background(), systemPackageIndexRefreshTimeout)
	defer cancel()
	lock, err := acquireSystemPackageLock(ctx)
	if err != nil {
		return err
	}
	defer releaseSystemPackageLock(lock)
	return systemPackageUpdateCommand(ctx, "apt-get", "update")
}

func RunSystemPackageUpdatePlan(planPath string) error {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan systemPackageUpdatePlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return err
	}
	defer os.Remove(plan.PlanPath)
	var maintenanceDB *sql.DB
	if plan.MaintenanceRoundID > 0 {
		var release func()
		var reason string
		maintenanceDB, release, reason, err = prepareMaintenancePackageRun(plan)
		if maintenanceDB != nil {
			defer maintenanceDB.Close()
		}
		if release == nil {
			if maintenanceDB != nil {
				_ = deferMaintenanceRound(maintenanceDB, &systemMaintenanceAction{ID: plan.MaintenanceRoundID}, reason)
			}
			status := "deferred"
			key := "settings.maintenance_install_deferred"
			if err != nil {
				status = "failed"
				key = "settings.system_update_status_start_failed"
			}
			writeErr := writePanelDBRestoreJSON(plan.StatusPath, SystemPackageUpdateStatus{ID: plan.ID, Status: status, Stage: "start", MessageKey: key, StartedAt: time.Now().UTC().Format(time.RFC3339), UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
			if err != nil {
				return err
			}
			return writeErr
		}
		defer release()
	} else {
		lock, err := os.OpenFile(systemPackageUpdateLockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = writePanelDBRestoreJSON(plan.StatusPath, SystemPackageUpdateStatus{ID: plan.ID, Status: "failed", Stage: "start", MessageKey: "settings.system_update_status_start_failed", UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
			return errors.New("another system update is running")
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	started := time.Now().UTC().Format(time.RFC3339)
	writeStatus := func(status, stage, messageKey string, removalPackages []string) error {
		return writePanelDBRestoreJSON(plan.StatusPath, SystemPackageUpdateStatus{ID: plan.ID, Status: status, Stage: stage, MessageKey: messageKey, StartedAt: started, UpdatedAt: time.Now().UTC().Format(time.RFC3339), RemovalPackages: removalPackages})
	}
	fail := func(stage, messageKey string) error {
		_ = writeStatus("failed", stage, messageKey, nil)
		return errors.New(messageKey)
	}
	// This runner, not its short-lived systemd-run caller, owns the service
	// descriptors through package scripts, health checks and final status.
	releaseServices, err := tryServiceControlLocks(filepath.Dir(plan.StatusPath), guardServiceNames...)
	if err != nil {
		if plan.MaintenanceRoundID > 0 && errors.Is(err, errServiceControlBusy) {
			if err := deferMaintenanceRound(maintenanceDB, &systemMaintenanceAction{ID: plan.MaintenanceRoundID}, "maintenance_busy"); err != nil {
				return fail("start", "settings.system_update_status_start_failed")
			}
			return writeStatus("deferred", "start", "settings.maintenance_install_deferred", nil)
		}
		_ = writeStatus("failed", "start", "settings.system_update_status_start_failed", nil)
		return err
	}
	defer releaseServices()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	// Preserve intentionally stopped services in automated installations.
	wasActive := map[string]bool{}
	if plan.MaintenanceRoundID > 0 {
		for _, service := range []string{"nginx", "php8.3-fpm", "mariadb", "redis-server", "wp-panel"} {
			wasActive[service] = systemPackageUpdateCommand(ctx, "systemctl", "is-active", "--quiet", service) == nil
		}
	}
	_ = writeStatus("running", "refresh", "settings.system_update_status_refresh", nil)
	if err := systemPackageUpdateCommand(ctx, "apt-get", "update"); err != nil {
		return fail("refresh", "settings.system_update_status_failed")
	}
	_ = writeStatus("running", "preflight", "settings.system_update_status_preflight", nil)
	preflightOutput, err := systemPackageUpdatePreflight(ctx)
	if err != nil {
		return fail("preflight", "settings.system_update_status_failed")
	}
	removalPackages, removalPlanned, parseErr := parseAPTPreflightRemovals(preflightOutput)
	if removalPlanned {
		return writeStatus("blocked", "preflight", "settings.system_update_status_removal_blocked", removalPackages)
	}
	if parseErr != nil {
		return fail("preflight", "settings.system_update_status_failed")
	}
	for _, step := range []struct {
		stage, messageKey, name string
		args                    []string
	}{
		{"upgrade", "settings.system_update_status_upgrading", "env", []string{"LC_ALL=C", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "--no-remove", "-o", "Dpkg::Options::=--force-confold", "full-upgrade"}},
		{"packages", "settings.system_update_status_checking_packages", "apt-get", []string{"check"}},
		{"packages", "settings.system_update_status_checking_packages", "dpkg", []string{"--audit"}},
		{"services", "settings.system_update_status_checking_services", "nginx", []string{"-t"}},
	} {
		_ = writeStatus("running", step.stage, step.messageKey, nil)
		if err := systemPackageUpdateCommand(ctx, step.name, step.args...); err != nil {
			return fail(step.stage, "settings.system_update_status_failed")
		}
	}
	for _, service := range []string{"nginx", "php8.3-fpm", "mariadb", "redis-server", "wp-panel"} {
		if plan.MaintenanceRoundID > 0 && !wasActive[service] {
			continue
		}
		if err := systemPackageUpdateCommand(ctx, "systemctl", "is-active", "--quiet", service); err != nil {
			return fail("services", "settings.system_update_status_health_failed")
		}
	}
	messageKey := "settings.system_update_status_success"
	rebootRequired, _ := systemPackageRebootRequired()
	if rebootRequired {
		messageKey = "settings.system_update_status_success_reboot"
	}
	bootID := ""
	if rebootRequired {
		bootID, _ = systemPackageBootID()
	}
	status := SystemPackageUpdateStatus{ID: plan.ID, Status: "success", Stage: "complete", MessageKey: messageKey, StartedAt: started, UpdatedAt: time.Now().UTC().Format(time.RFC3339), RebootRequired: rebootRequired, BootID: bootID}
	if plan.MaintenanceRoundID > 0 {
		err = sealMaintenanceRound(ctx, maintenanceDB, plan.MaintenanceRoundID)
		if err != nil {
			// Package installation succeeded independently of runtime inspection.
			// Preserve its reboot hint even when the scope cannot be sealed.
			status.MessageKey = "settings.system_update_status_runtime_unknown"
			if writeErr := writePanelDBRestoreJSON(plan.StatusPath, status); writeErr != nil {
				return writeErr
			}
			_, err = maintenanceDB.Exec(`UPDATE system_maintenance_actions SET status='unknown',phase='runtime',message_code='runtime_unknown',overtime_allowed=0,updated_at=CURRENT_TIMESTAMP WHERE id=? AND kind='round' AND phase='install' AND status='pending'`, plan.MaintenanceRoundID)
			return err
		}
	}
	return writePanelDBRestoreJSON(plan.StatusPath, status)
}

func runSystemPackageUpdatePreflight(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "env", "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-s", "full-upgrade").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("apt-get preflight failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

var aptRemovalSummaryPattern = regexp.MustCompile(`(?m)(?:^|[^0-9])([0-9]+) to remove(?:[,. ]|$)`)

func parseAPTPreflightRemovals(output string) ([]string, bool, error) {
	seen := make(map[string]struct{})
	var packages []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "Remv" && fields[0] != "Purg") {
			continue
		}
		name := fields[1]
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		packages = append(packages, name)
	}
	match := aptRemovalSummaryPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return packages, len(packages) > 0, errors.New("apt removal summary missing")
	}
	count, err := strconv.Atoi(match[1])
	if err != nil {
		return packages, len(packages) > 0, errors.New("invalid apt removal summary")
	}
	planned := count > 0 || len(packages) > 0
	if count != len(packages) {
		return packages, planned, errors.New("apt removal summary mismatch")
	}
	return packages, planned, nil
}

func detectSystemPackageRebootRequired() (bool, error) {
	marker, err := systemPackageRebootMarker()
	if err != nil || marker {
		return marker, err
	}
	running, err := systemPackageRunningKernel()
	if err != nil {
		return false, err
	}
	installed, err := systemPackageInstalledKernels()
	if err != nil {
		return false, err
	}
	flavour, err := kernelReleaseFlavour(running)
	if err != nil {
		return false, err
	}
	matched := false
	for _, candidate := range installed {
		candidateFlavour, candidateErr := kernelReleaseFlavour(candidate)
		if candidateErr != nil || candidateFlavour != flavour {
			continue
		}
		matched = true
		greater, compareErr := systemPackageKernelGreater(candidate, running)
		if compareErr != nil {
			return false, compareErr
		}
		if greater {
			return true, nil
		}
	}
	if !matched {
		return false, errors.New("running kernel flavour not found")
	}
	return false, nil
}

func kernelReleaseFlavour(release string) (string, error) {
	parts := strings.Split(strings.TrimSpace(release), "-")
	if len(parts) < 2 || parts[0] == "" {
		return "", errors.New("invalid kernel release")
	}
	index := 1
	for index < len(parts) {
		if _, err := strconv.Atoi(parts[index]); err != nil {
			break
		}
		index++
	}
	if index >= len(parts) {
		return "", errors.New("kernel flavour missing")
	}
	return strings.Join(parts[index:], "-"), nil
}

func rebootRequiredMarkerExists() (bool, error) {
	_, err := os.Stat("/var/run/reboot-required")
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func runningKernelRelease() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemPackageKernelCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return "", err
	}
	release := strings.TrimSpace(string(out))
	if release == "" {
		return "", errors.New("empty running kernel release")
	}
	return release, nil
}

func installedKernelReleases() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemPackageKernelCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/linux-version", "list").Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func kernelReleaseGreater(candidate, running string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemPackageKernelCommandTimeout)
	defer cancel()
	err := exec.CommandContext(ctx, "/usr/bin/linux-version", "compare", candidate, "gt", running).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func currentBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	bootID := strings.TrimSpace(string(data))
	if bootID == "" {
		return "", errors.New("empty boot id")
	}
	return bootID, nil
}

func runSystemPackageUpdateCommand(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	if name == "dpkg" && strings.TrimSpace(string(out)) != "" {
		return errors.New("dpkg reports unfinished packages")
	}
	return nil
}
