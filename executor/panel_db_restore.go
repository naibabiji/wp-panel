package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const panelDBRestoreStatusFile = "panel-db-restore-status.json"

type PanelDBRestoreStatus struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Message        string `json:"message"`
	SafeBackupName string `json:"safe_backup_name,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

type panelDBRestorePlan struct {
	ID         string `json:"id"`
	DBPath     string `json:"db_path"`
	BackupPath string `json:"backup_path"`
	SafeBackup string `json:"safe_backup"`
	StatusPath string `json:"status_path"`
	PlanPath   string `json:"plan_path"`
	HealthURL  string `json:"health_url"`
}

var (
	panelLifecycleMu      sync.Mutex
	panelDBRestoreCommand = func(name string, args ...string) error {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	panelDBRestoreHealthCheck = healthCheck
	panelDBRestoreWaitHealth  = waitPanelDBRestoreHealth
	panelDBRestoreUnitActive  = func(id string) bool {
		return exec.Command("systemctl", "is-active", "--quiet", "wp-panel-db-restore-"+id).Run() == nil
	}
)

func PanelDBRestoreStatusPath(cfg *config.Config) string {
	return filepath.Join(cfg.Panel.DataDir, panelDBRestoreStatusFile)
}

func ReadPanelDBRestoreStatus(cfg *config.Config) PanelDBRestoreStatus {
	status := PanelDBRestoreStatus{Status: "idle"}
	if cfg == nil {
		return status
	}
	data, err := os.ReadFile(PanelDBRestoreStatusPath(cfg))
	if err == nil {
		_ = json.Unmarshal(data, &status)
	}
	return status
}

func ReconcilePanelDBRestoreStatus(cfg *config.Config) PanelDBRestoreStatus {
	status := ReadPanelDBRestoreStatus(cfg)
	if status.Status != "running" || status.ID == "" || panelDBRestoreUnitActive(status.ID) {
		return status
	}
	status.Status = "failed"
	status.Message = "面板数据库恢复任务意外中断，请检查面板状态后重试"
	status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = writePanelDBRestoreJSON(PanelDBRestoreStatusPath(cfg), status)
	return status
}

func StartPanelDBRestore(cfg *config.Config, backupPath string) (PanelDBRestoreStatus, error) {
	if cfg == nil {
		return PanelDBRestoreStatus{}, errors.New("面板配置不可用")
	}
	if !panelLifecycleMu.TryLock() {
		return PanelDBRestoreStatus{}, errors.New("已有面板维护操作正在执行")
	}
	defer panelLifecycleMu.Unlock()
	if SnapshotPanelUpdateStatus().Running {
		return PanelDBRestoreStatus{}, errors.New("面板正在更新，请稍后再恢复")
	}
	if current := ReconcilePanelDBRestoreStatus(cfg); current.Status == "running" {
		return PanelDBRestoreStatus{}, errors.New("已有面板数据库恢复正在执行")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return PanelDBRestoreStatus{}, errors.New("systemd-run 不可用，无法安全恢复面板数据库")
	}

	backupDir := filepath.Join(cfg.Panel.BackupDir, "panel-db")
	safeBackup, err := database.BackupDatabase(backupDir)
	if err != nil {
		return PanelDBRestoreStatus{}, fmt.Errorf("恢复前安全备份失败: %w", err)
	}
	if err := database.VerifyDBBackup(safeBackup); err != nil {
		_ = os.Remove(safeBackup)
		return PanelDBRestoreStatus{}, fmt.Errorf("恢复前安全备份校验失败: %w", err)
	}

	id := fmt.Sprintf("%d", time.Now().UnixNano())
	planPath := filepath.Join(cfg.Panel.DataDir, "panel-db-restore-"+id+".json")
	status := PanelDBRestoreStatus{ID: id, Status: "running", Message: "面板数据库恢复中", SafeBackupName: filepath.Base(safeBackup), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	plan := panelDBRestorePlan{ID: id, DBPath: cfg.SQLite.Path, BackupPath: backupPath, SafeBackup: safeBackup, StatusPath: PanelDBRestoreStatusPath(cfg), PlanPath: planPath, HealthURL: healthURL(cfg)}
	if err := writePanelDBRestoreJSON(planPath, plan); err != nil {
		return PanelDBRestoreStatus{}, err
	}
	if err := writePanelDBRestoreJSON(plan.StatusPath, status); err != nil {
		_ = os.Remove(planPath)
		return PanelDBRestoreStatus{}, err
	}

	executable, err := os.Executable()
	if err != nil {
		return PanelDBRestoreStatus{}, err
	}
	unit := "wp-panel-db-restore-" + id
	if err := panelDBRestoreCommand("systemd-run", "--unit", unit, "--collect", "--property", "Type=exec", "--property", "KillMode=process", executable, "--panel-db-restore-plan", planPath); err != nil {
		status.Status, status.Message, status.UpdatedAt = "failed", "无法启动独立恢复任务", time.Now().UTC().Format(time.RFC3339)
		_ = writePanelDBRestoreJSON(plan.StatusPath, status)
		_ = os.Remove(planPath)
		return PanelDBRestoreStatus{}, fmt.Errorf("启动独立恢复任务失败: %w", err)
	}
	return status, nil
}

func RunPanelDBRestorePlan(planPath string) error {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan panelDBRestorePlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return err
	}
	defer os.Remove(plan.PlanPath)

	fail := func(message string) error {
		_ = writePanelDBRestoreJSON(plan.StatusPath, PanelDBRestoreStatus{ID: plan.ID, Status: "failed", Message: message, SafeBackupName: filepath.Base(plan.SafeBackup), UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
		return errors.New(message)
	}
	if err := panelDBRestoreCommand("systemctl", "stop", "wp-panel"); err != nil {
		return fail("停止面板服务失败，数据库未替换")
	}
	if err := replacePanelDBFile(plan.BackupPath, plan.DBPath); err != nil {
		_ = panelDBRestoreCommand("systemctl", "start", "wp-panel")
		return fail("替换面板数据库失败，已尝试重新启动原面板")
	}
	removeSQLiteSidecars(plan.DBPath)
	if err := panelDBRestoreCommand("systemctl", "start", "wp-panel"); err == nil && panelDBRestoreWaitHealth(plan.HealthURL, 60*time.Second) == nil {
		return writePanelDBRestoreJSON(plan.StatusPath, PanelDBRestoreStatus{ID: plan.ID, Status: "success", Message: "面板数据库恢复成功", SafeBackupName: filepath.Base(plan.SafeBackup), UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
	}

	_ = panelDBRestoreCommand("systemctl", "stop", "wp-panel")
	if err := replacePanelDBFile(plan.SafeBackup, plan.DBPath); err != nil {
		return fail("恢复后的面板无法正常启动，恢复安全备份也失败，请人工处理")
	}
	removeSQLiteSidecars(plan.DBPath)
	if err := panelDBRestoreCommand("systemctl", "start", "wp-panel"); err != nil || panelDBRestoreWaitHealth(plan.HealthURL, 60*time.Second) != nil {
		return fail("恢复后的面板无法正常启动；安全备份已复制回去，但面板仍未恢复健康，请人工处理")
	}
	return writePanelDBRestoreJSON(plan.StatusPath, PanelDBRestoreStatus{ID: plan.ID, Status: "rolled_back", Message: "所选备份恢复失败，已自动恢复操作前的面板数据库", SafeBackupName: filepath.Base(plan.SafeBackup), UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
}

func waitPanelDBRestoreHealth(rawURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if last = panelDBRestoreHealthCheck(rawURL); last == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return last
}

func replacePanelDBFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	tmp := target + ".restore-new"
	_ = os.Remove(tmp)
	output, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	ok = true
	return nil
}

func removeSQLiteSidecars(dbPath string) {
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
}

func writePanelDBRestoreJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
