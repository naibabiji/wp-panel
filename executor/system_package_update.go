package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

const systemPackageUpdateStatusFile = "system-package-update-status.json"

type SystemPackageUpdateStatus struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Stage      string `json:"stage"`
	MessageKey string `json:"message_key"`
	StartedAt  string `json:"started_at,omitempty"`
	UpdatedAt  string `json:"updated_at"`
}

type systemPackageUpdatePlan struct {
	ID         string `json:"id"`
	StatusPath string `json:"status_path"`
	PlanPath   string `json:"plan_path"`
}

var (
	systemPackageUpdateCommand  = runSystemPackageUpdateCommand
	systemPackageUpdateUnitLive = func(id string) bool {
		return exec.Command("systemctl", "is-active", "--quiet", "wp-panel-system-update-"+id).Run() == nil
	}
	systemPackageUpdateLockPath = "/run/lock/wp-panel-system-update.lock"
	systemPackageUpdateStartMu  sync.Mutex
)

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
	systemPackageUpdateStartMu.Lock()
	defer systemPackageUpdateStartMu.Unlock()

	if cfg == nil {
		return SystemPackageUpdateStatus{}, errors.New("panel config unavailable")
	}
	current := ReconcileSystemPackageUpdateStatus(cfg)
	if current.Status == "running" {
		return SystemPackageUpdateStatus{}, errors.New("系统更新正在执行")
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
	plan := systemPackageUpdatePlan{ID: id, StatusPath: SystemPackageUpdateStatusPath(cfg), PlanPath: planPath}
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
	lock, err := os.OpenFile(systemPackageUpdateLockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another system update is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	started := time.Now().UTC().Format(time.RFC3339)
	writeStatus := func(status, stage, messageKey string) {
		_ = writePanelDBRestoreJSON(plan.StatusPath, SystemPackageUpdateStatus{ID: plan.ID, Status: status, Stage: stage, MessageKey: messageKey, StartedAt: started, UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	fail := func(stage, messageKey string) error {
		writeStatus("failed", stage, messageKey)
		return errors.New(messageKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	for _, step := range []struct {
		stage, messageKey, name string
		args                    []string
	}{
		{"refresh", "settings.system_update_status_refresh", "apt-get", []string{"update"}},
		{"preflight", "settings.system_update_status_preflight", "apt-get", []string{"-s", "upgrade"}},
		{"upgrade", "settings.system_update_status_upgrading", "env", []string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", "upgrade"}},
		{"packages", "settings.system_update_status_checking_packages", "apt-get", []string{"check"}},
		{"packages", "settings.system_update_status_checking_packages", "dpkg", []string{"--audit"}},
		{"services", "settings.system_update_status_checking_services", "nginx", []string{"-t"}},
	} {
		writeStatus("running", step.stage, step.messageKey)
		if err := systemPackageUpdateCommand(ctx, step.name, step.args...); err != nil {
			return fail(step.stage, "settings.system_update_status_failed")
		}
	}
	for _, service := range []string{"nginx", "php8.3-fpm", "mariadb", "redis-server", "wp-panel"} {
		if err := systemPackageUpdateCommand(ctx, "systemctl", "is-active", "--quiet", service); err != nil {
			return fail("services", "settings.system_update_status_health_failed")
		}
	}
	status := SystemPackageUpdateStatus{ID: plan.ID, Status: "success", Stage: "complete", MessageKey: "settings.system_update_status_success", StartedAt: started, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	return writePanelDBRestoreJSON(plan.StatusPath, status)
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
