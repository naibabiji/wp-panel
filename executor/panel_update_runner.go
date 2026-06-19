package executor

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const (
	panelBinaryName           = "wp-panel"
	panelInstallPath          = "/usr/local/bin/wp-panel"
	releasePubKeyHex          = "ee8ec641204d785c6469b003c710666126a3156d902b78665bb73e859b6f9546"
	updateTerminalStatusTTL   = 5 * time.Minute
	autoUpdateCheckInterval   = 10 * time.Minute
	autoUpdateFetchInterval   = 24 * time.Hour
	autoUpdateFailureCooldown = 24 * time.Hour
	panelBinaryBackupKeep     = 5
	panelDBBackupKeep         = 7
)

type PanelUpdateStatus struct {
	Running         bool      `json:"running"`
	Completed       bool      `json:"completed"`
	Stage           string    `json:"stage"`
	Message         string    `json:"message"`
	Percent         int       `json:"percent"`
	DownloadPercent int       `json:"download_percent"`
	DownloadedBytes int64     `json:"downloaded_bytes"`
	TotalBytes      int64     `json:"total_bytes"`
	HasTotal        bool      `json:"has_total"`
	Error           string    `json:"error"`
	UpdatedAt       time.Time `json:"-"`
}

type PanelUpdateOptions struct {
	Trigger        string
	CurrentVersion string
	Proxy          string
	ConfigPath     string
	Config         *config.Config
	UseWatchdog    bool
}

type rollbackPlan struct {
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	BackupBinary   string `json:"backup_binary"`
	BackupDB       string `json:"backup_db"`
	PlanPath       string `json:"plan_path"`
	ConfigPath     string `json:"config_path"`
	HealthURL      string `json:"health_url"`
	CreatedAt      string `json:"created_at"`
	Trigger        string `json:"trigger"`
}

type autoUpdateSettings struct {
	Enabled                  bool
	Mode                     string
	Window                   string
	ReleaseDelay             time.Duration
	SignatureTimeout         time.Duration
	LastTargetVersion        string
	LastAttemptAt            time.Time
	LastCheckAt              time.Time
	LastStatus               string
	LastStage                string
	LastSignatureWaitVersion string
	LastSignatureWaitAt      time.Time
}

var (
	panelUpdateMu            sync.Mutex
	panelUpdateStatusMu      sync.Mutex
	currentPanelUpdateStatus = PanelUpdateStatus{
		Stage:     "idle",
		Message:   "等待更新",
		UpdatedAt: time.Now(),
	}
	panelUpdateAutoStarted sync.Once
)

func ExecutePanelUpdate(opts PanelUpdateOptions) (*GithubRelease, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("仅支持 Linux 服务器更新")
	}
	if !panelUpdateMu.TryLock() {
		return nil, fmt.Errorf("已有更新任务正在执行，请稍后再试")
	}
	defer panelUpdateMu.Unlock()

	resetPanelUpdateStatus()
	trigger := opts.Trigger
	if trigger == "" {
		trigger = "manual"
	}
	recordPanelUpdateStage(trigger, "", "fetch_release", "running", "正在获取版本信息")
	setPanelUpdateStep("fetch_release", "正在获取版本信息...", 5)

	latest, err := FetchLatestPanelRelease(opts.Proxy)
	if err != nil {
		return nil, panelUpdateFail(trigger, "", "fetch_release", "获取版本信息失败: "+err.Error())
	}
	if latest == nil || latest.TagName == "" {
		return nil, panelUpdateFail(trigger, "", "fetch_release", "获取版本信息失败: release 为空")
	}
	if CompareVersions(latest.TagName, opts.CurrentVersion) <= 0 {
		return nil, panelUpdateFail(trigger, latest.TagName, "compare_version", "已经是最新版本")
	}

	setPanelUpdateStep("resolve_assets", "正在准备更新文件...", 10)
	downloadURL, sha256URL, sigURL := resolvePanelAssets(latest)
	if downloadURL == "" {
		return nil, panelUpdateFail(trigger, latest.TagName, "resolve_assets", "未找到适用于当前系统的二进制文件")
	}
	if sha256URL == "" {
		return nil, panelUpdateFail(trigger, latest.TagName, "resolve_assets", "未找到 SHA256 校验文件，无法验证更新完整性")
	}
	if sigURL == "" {
		setPanelUpdateFailed("未找到 Ed25519 签名文件，等待签名发布")
		recordPanelUpdateStage(trigger, latest.TagName, "waiting_signature", "waiting", "未找到 wp-panel.sha256.sig，等待签名发布")
		return latest, errWaitingSignature
	}

	setPanelUpdateStep("prepare_download", "正在创建临时目录...", 12)
	tmpDir, err := os.MkdirTemp("", "wp-panel-update-*")
	if err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "prepare_download", "创建临时目录失败: "+err.Error())
	}
	defer os.RemoveAll(tmpDir)

	newBinary := filepath.Join(tmpDir, panelBinaryName)
	setPanelUpdateStep("download_binary", "正在下载更新包...", 15)
	if err := downloadFileWithProgress(proxyURL(opts.Proxy, downloadURL), newBinary, 10*time.Minute, setPanelBinaryDownloadProgress); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "download_binary", "更新包下载失败: "+err.Error())
	}
	if err := os.Chmod(newBinary, 0755); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "download_binary", "设置新版本权限失败: "+err.Error())
	}

	setPanelUpdateStep("download_sha256", "正在下载校验文件...", 62)
	shaFile := filepath.Join(tmpDir, panelBinaryName+".sha256")
	if err := downloadFile(proxyURL(opts.Proxy, sha256URL), shaFile); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "download_sha256", "SHA256 校验文件下载失败: "+err.Error())
	}
	setPanelUpdateStep("download_signature", "正在下载签名文件...", 66)
	sigFile := filepath.Join(tmpDir, panelBinaryName+".sha256.sig")
	if err := downloadFile(proxyURL(opts.Proxy, sigURL), sigFile); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "download_signature", "签名文件下载失败: "+err.Error())
	}
	setPanelUpdateStep("verify_signature", "正在校验更新来源...", 72)
	if err := verifyEd25519(shaFile, sigFile); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "verify_signature", "签名校验失败: "+err.Error())
	}
	setPanelUpdateStep("verify_sha256", "正在校验更新包完整性...", 78)
	if err := verifySHA256(newBinary, shaFile); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "verify_sha256", "SHA256 校验失败: "+err.Error())
	}

	setPanelUpdateStep("preflight", "正在预检新版本...", 82)
	if err := preflightBinary(newBinary); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "preflight", "新版本预检失败: "+err.Error())
	}
	if opts.UseWatchdog {
		if _, err := exec.LookPath("systemd-run"); err != nil {
			return nil, panelUpdateFail(trigger, latest.TagName, "preflight", "systemd-run 不可用，无法启动独立更新守护进程: "+err.Error())
		}
	}
	setPanelUpdateStep("disk_check", "正在检查磁盘空间...", 84)
	if err := checkUpdateDiskSpace(newBinary, opts.Config); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "disk_check", err.Error())
	}

	setPanelUpdateStep("backup", "正在备份当前版本...", 88)
	backupPath := versionedBackupPath(opts.CurrentVersion)
	if err := copyPanelFile(panelInstallPath, backupPath, 0755); err != nil {
		return nil, panelUpdateFail(trigger, latest.TagName, "backup_binary", "备份旧版本失败: "+err.Error())
	}

	backupDB := ""
	if opts.Config != nil {
		backupDir := filepath.Join(opts.Config.Panel.BackupDir, "panel-db")
		path, err := database.BackupDatabase(backupDir)
		if err != nil {
			return nil, panelUpdateFail(trigger, latest.TagName, "backup_database", "备份面板数据库失败: "+err.Error())
		}
		if err := database.VerifyDBBackup(path); err != nil {
			return nil, panelUpdateFail(trigger, latest.TagName, "backup_database", "数据库备份校验失败: "+err.Error())
		}
		backupDB = path
	}

	plan := rollbackPlan{
		CurrentVersion: opts.CurrentVersion,
		TargetVersion:  latest.TagName,
		BackupBinary:   backupPath,
		BackupDB:       backupDB,
		ConfigPath:     opts.ConfigPath,
		HealthURL:      healthURL(opts.Config),
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		Trigger:        trigger,
	}
	if opts.UseWatchdog && opts.Config != nil {
		planPath, err := writeRollbackPlan(opts.Config, plan)
		if err != nil {
			return nil, panelUpdateFail(trigger, latest.TagName, "write_rollback_plan", "写入回滚计划失败: "+err.Error())
		}
		plan.PlanPath = planPath
		if err := writeRollbackPlanFile(planPath, plan); err != nil {
			return nil, panelUpdateFail(trigger, latest.TagName, "write_rollback_plan", "写入回滚计划失败: "+err.Error())
		}
	}

	setPanelUpdateStep("replace_binary", "正在替换面板文件...", 92)
	stagedBinary := panelInstallPath + ".new"
	_ = os.Remove(stagedBinary)
	if err := copyPanelFile(newBinary, stagedBinary, 0755); err != nil {
		_ = os.Remove(stagedBinary)
		return nil, panelUpdateFail(trigger, latest.TagName, "replace_binary", "暂存新版本失败: "+err.Error())
	}
	if err := os.Rename(stagedBinary, panelInstallPath); err != nil {
		_ = os.Remove(stagedBinary)
		return nil, panelUpdateFail(trigger, latest.TagName, "replace_binary", "替换失败，旧版本仍保留: "+err.Error())
	}
	if err := os.Chmod(panelInstallPath, 0755); err != nil {
		if rbErr := copyPanelFile(backupPath, panelInstallPath, 0755); rbErr != nil {
			return nil, panelUpdateFail(trigger, latest.TagName, "replace_binary", "替换后权限设置失败，且自动回滚失败: "+rbErr.Error())
		}
		return nil, panelUpdateFail(trigger, latest.TagName, "replace_binary", "替换后权限设置失败，已回滚: "+err.Error())
	}

	if opts.UseWatchdog && plan.PlanPath != "" {
		if err := startUpdateWatchdog(backupPath, plan.PlanPath, opts.ConfigPath); err != nil {
			if rbErr := copyPanelFile(backupPath, panelInstallPath, 0755); rbErr != nil {
				return nil, panelUpdateFail(trigger, latest.TagName, "start_watchdog", "启动更新健康检查进程失败，且恢复旧二进制失败: "+rbErr.Error())
			}
			return nil, panelUpdateFail(trigger, latest.TagName, "start_watchdog", "启动更新健康检查进程失败: "+err.Error())
		}
	}

	setPanelUpdateStep("restart", "正在重启面板...", 98)
	recordPanelUpdateStage(trigger, latest.TagName, "restart", "running", "二进制替换完成，正在重启 wp-panel")
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", "wp-panel").Run()
	}()

	setPanelUpdateCompleted("更新文件已替换，面板正在重启并等待健康检查...")
	return latest, nil
}

var errWaitingSignature = fmt.Errorf("waiting_signature")

func SnapshotPanelUpdateStatus() PanelUpdateStatus {
	panelUpdateStatusMu.Lock()
	defer panelUpdateStatusMu.Unlock()
	if panelUpdateStatusExpiredLocked(time.Now()) {
		resetPanelUpdateStatusLocked()
	}
	return currentPanelUpdateStatus
}

func StartPanelAutoUpdateScheduler(currentVersion, configPath string, cfg *config.Config) {
	panelUpdateAutoStarted.Do(func() {
		go func() {
			time.Sleep(2 * time.Minute)
			runPanelAutoUpdateCheck(currentVersion, configPath, cfg)
			ticker := time.NewTicker(autoUpdateCheckInterval)
			defer ticker.Stop()
			for range ticker.C {
				runPanelAutoUpdateCheck(currentVersion, configPath, cfg)
			}
		}()
	})
}

func FinalizePendingPanelUpdate(cfg *config.Config, currentVersion string) {
	if cfg == nil {
		return
	}
	planPath := rollbackPlanPath(cfg)
	data, err := os.ReadFile(planPath)
	if err != nil {
		return
	}
	var plan rollbackPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		recordPanelUpdateStage("unknown", "", "finalize", "failed", "读取回滚计划失败: "+err.Error())
		return
	}
	if currentVersion == plan.TargetVersion {
		recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, "new_process_started", "running", "新版本进程已启动，等待 watchdog 健康检查")
	}
}

func RunUpdateWatchdog(cfg *config.Config, planPath string) {
	data, err := os.ReadFile(planPath)
	if err != nil {
		log.Printf("[更新守护] 读取回滚计划失败: %v", err)
		return
	}
	var plan rollbackPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		log.Printf("[更新守护] 解析回滚计划失败: %v", err)
		return
	}
	deadline := time.Now().Add(90 * time.Second)
	var healthErr error
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		if err := healthCheck(plan.HealthURL); err == nil {
			recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, "health_check", "success", "更新后健康检查通过")
			if plan.Trigger == "auto" {
				sendPanelUpdateMail(true, plan.TargetVersion, "health_check", "自动更新成功，健康检查通过")
			}
			setSecuritySetting("panel_auto_update_last_status", "success")
			setSecuritySetting("panel_auto_update_last_success_at", time.Now().Format(time.RFC3339))
			setSecuritySetting("panel_auto_update_last_success_version", plan.TargetVersion)
			clearPanelUpdateCache()
			cleanupPanelUpdateBackups(plan)
			_ = os.Remove(planPath)
			return
		} else {
			healthErr = err
		}
	}
	msg := "健康检查超时"
	if healthErr != nil {
		msg += ": " + healthErr.Error()
	}
	recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, "health_check", "failed", msg)
	if err := copyPanelFile(plan.BackupBinary, panelInstallPath, 0755); err != nil {
		failMsg := "恢复旧二进制失败: " + err.Error()
		recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, "rollback_binary", "failed", failMsg)
		sendPanelUpdateMail(false, plan.TargetVersion, "rollback_binary", failMsg)
		return
	}
	recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, "rollback_binary", "success", "旧二进制已恢复")
	if shouldRestoreDBAfterHealthFailure(plan.BackupDB) {
		if err := restoreDBFile(cfg, plan.BackupDB); err != nil {
			recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, "rollback_database", "failed", "恢复数据库失败: "+err.Error())
		} else {
			recordPanelUpdateStage(plan.Trigger, plan.TargetVersion, "rollback_database", "success", "数据库备份已恢复")
		}
	}
	setSecuritySetting("panel_auto_update_last_status", "failed")
	setSecuritySetting("panel_auto_update_last_stage", "health_check")
	setSecuritySetting("panel_auto_update_last_error", msg)
	if plan.Trigger == "auto" {
		sendPanelUpdateMail(false, plan.TargetVersion, "health_check", msg+"；已尝试回滚旧版本")
	}
	_ = os.Remove(planPath)
	_ = exec.Command("systemctl", "restart", "wp-panel").Run()
}

func IsPatchBump(current, target string) bool {
	c, okC := parseStableSemver(current)
	t, okT := parseStableSemver(target)
	if !okC || !okT {
		return false
	}
	return c[0] == t[0] && c[1] == t[1] && t[2] > c[2]
}

func IsStableVersion(version string) bool {
	_, ok := parseStableSemver(version)
	return ok
}

func proxyURL(proxy, original string) string {
	if proxy != "" {
		return strings.TrimRight(proxy, "/") + "/" + original
	}
	return original
}

func resolvePanelAssets(latest *GithubRelease) (binaryURL, sha256URL, sigURL string) {
	for _, a := range latest.Assets {
		switch a.Name {
		case panelBinaryName:
			binaryURL = a.BrowserDownloadURL
		case panelBinaryName + ".sha256":
			sha256URL = a.BrowserDownloadURL
		case panelBinaryName + ".sha256.sig":
			sigURL = a.BrowserDownloadURL
		}
	}
	return
}

func downloadFile(url, dest string) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt-1) * 2 * time.Second)
		}
		lastErr = downloadFileWithProgress(url, dest, 60*time.Second, nil)
		if lastErr == nil {
			return nil
		}
		_ = os.Remove(dest)
	}
	return lastErr
}

func downloadFileWithProgress(url, dest string, timeout time.Duration, progress func(downloaded, total int64)) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	copied := false
	defer func() {
		out.Close()
		if !copied {
			_ = os.Remove(dest)
		}
	}()
	if progress != nil {
		progress(0, resp.ContentLength)
	}
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
			downloaded += int64(n)
			if progress != nil {
				progress(downloaded, resp.ContentLength)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	copied = true
	return nil
}

func verifySHA256(filePath, shaFile string) error {
	data, err := os.ReadFile(shaFile)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return fmt.Errorf("SHA256 文件为空")
	}
	expected := fields[0]
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("SHA256 长度异常")
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(expected, fmt.Sprintf("%x", h.Sum(nil))) {
		return fmt.Errorf("SHA256 不匹配")
	}
	return nil
}

func verifyEd25519(shaFile, sigFile string) error {
	pubKey, err := hex.DecodeString(releasePubKeyHex)
	if err != nil {
		return fmt.Errorf("解析内置公钥失败")
	}
	sig, err := os.ReadFile(sigFile)
	if err != nil {
		return err
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("签名长度异常: %d", len(sig))
	}
	message, err := os.ReadFile(shaFile)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pubKey, message, sig) {
		return fmt.Errorf("Ed25519 签名不匹配")
	}
	return nil
}

func preflightBinary(path string) error {
	cmd := exec.Command(path, "--info")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func versionedBackupPath(currentVersion string) string {
	version := sanitizeBackupPart(currentVersion)
	if version == "" {
		version = "unknown"
	}
	return fmt.Sprintf("%s.bak.%s.%s", panelInstallPath, version, time.Now().UTC().Format("20060102-150405"))
}

func sanitizeBackupPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func copyPanelFile(srcPath, dstPath string, mode os.FileMode) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	copied := false
	dstClosed := false
	defer func() {
		if !dstClosed {
			_ = dst.Close()
		}
		if !copied {
			_ = os.Remove(dstPath)
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	dstClosed = true
	if err := os.Chmod(dstPath, mode); err != nil {
		return err
	}
	copied = true
	return nil
}

func resetPanelUpdateStatus() {
	panelUpdateStatusMu.Lock()
	resetPanelUpdateStatusLocked()
	panelUpdateStatusMu.Unlock()
}

func resetPanelUpdateStatusLocked() {
	currentPanelUpdateStatus = PanelUpdateStatus{Stage: "idle", Message: "等待更新", UpdatedAt: time.Now()}
}

func panelUpdateStatusExpiredLocked(now time.Time) bool {
	if currentPanelUpdateStatus.Running {
		return false
	}
	if !currentPanelUpdateStatus.Completed && currentPanelUpdateStatus.Error == "" {
		return false
	}
	return now.Sub(currentPanelUpdateStatus.UpdatedAt) > updateTerminalStatusTTL
}

func setPanelUpdateStep(stage, message string, percent int) {
	panelUpdateStatusMu.Lock()
	currentPanelUpdateStatus.Running = true
	currentPanelUpdateStatus.Completed = false
	currentPanelUpdateStatus.Stage = stage
	currentPanelUpdateStatus.Message = message
	currentPanelUpdateStatus.Percent = clampPercent(percent)
	currentPanelUpdateStatus.DownloadPercent = 0
	currentPanelUpdateStatus.DownloadedBytes = 0
	currentPanelUpdateStatus.TotalBytes = 0
	currentPanelUpdateStatus.HasTotal = false
	currentPanelUpdateStatus.Error = ""
	currentPanelUpdateStatus.UpdatedAt = time.Now()
	panelUpdateStatusMu.Unlock()
}

func setPanelBinaryDownloadProgress(downloaded, total int64) {
	hasTotal := total > 0
	downloadPercent := 0
	overallPercent := 15
	if hasTotal {
		downloadPercent = clampPercent(int(downloaded * 100 / total))
		overallPercent = 15 + downloadPercent*45/100
	}
	panelUpdateStatusMu.Lock()
	currentPanelUpdateStatus.Running = true
	currentPanelUpdateStatus.Completed = false
	currentPanelUpdateStatus.Stage = "download_binary"
	currentPanelUpdateStatus.Message = "正在下载更新包..."
	currentPanelUpdateStatus.Percent = clampPercent(overallPercent)
	currentPanelUpdateStatus.DownloadPercent = downloadPercent
	currentPanelUpdateStatus.DownloadedBytes = downloaded
	currentPanelUpdateStatus.TotalBytes = total
	currentPanelUpdateStatus.HasTotal = hasTotal
	currentPanelUpdateStatus.Error = ""
	currentPanelUpdateStatus.UpdatedAt = time.Now()
	panelUpdateStatusMu.Unlock()
}

func setPanelUpdateFailed(message string) {
	panelUpdateStatusMu.Lock()
	currentPanelUpdateStatus.Running = false
	currentPanelUpdateStatus.Completed = false
	currentPanelUpdateStatus.Message = message
	currentPanelUpdateStatus.Error = message
	currentPanelUpdateStatus.UpdatedAt = time.Now()
	panelUpdateStatusMu.Unlock()
}

func setPanelUpdateCompleted(message string) {
	panelUpdateStatusMu.Lock()
	currentPanelUpdateStatus.Running = false
	currentPanelUpdateStatus.Completed = true
	currentPanelUpdateStatus.Stage = "completed"
	currentPanelUpdateStatus.Message = message
	currentPanelUpdateStatus.Percent = 100
	currentPanelUpdateStatus.DownloadPercent = 100
	currentPanelUpdateStatus.Error = ""
	currentPanelUpdateStatus.UpdatedAt = time.Now()
	panelUpdateStatusMu.Unlock()
}

func clampPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func panelUpdateFail(trigger, version, stage, message string) error {
	setPanelUpdateFailed(message)
	recordPanelUpdateStage(trigger, version, stage, "failed", message)
	return fmt.Errorf("%s", message)
}

func recordPanelUpdateStage(trigger, targetVersion, stage, status, message string) {
	if trigger == "" {
		trigger = "manual"
	}
	target := targetVersion
	if target == "" {
		target = "unknown"
	}
	recordOperationLog("panel_"+trigger+"_update", target, status, stage+": "+message)
	if trigger == "auto" {
		setSecuritySetting("panel_auto_update_last_target_version", targetVersion)
		setSecuritySetting("panel_auto_update_last_status", status)
		setSecuritySetting("panel_auto_update_last_stage", stage)
		setSecuritySetting("panel_auto_update_last_error", message)
		setSecuritySetting("panel_auto_update_last_attempt_at", time.Now().Format(time.RFC3339))
	}
}

func runPanelAutoUpdateCheck(currentVersion, configPath string, cfg *config.Config) {
	if currentVersion == "" || currentVersion == "dev" {
		return
	}
	settings := readAutoUpdateSettings()
	if !settings.Enabled || !withinAutoUpdateWindow(settings.Window, time.Now()) {
		return
	}
	if settings.LastStatus == "failed" && settings.LastAttemptAt.After(time.Now().Add(-autoUpdateFailureCooldown)) {
		return
	}
	if !shouldFetchForAutoUpdate(settings, time.Now()) {
		return
	}
	setSecuritySetting("panel_auto_update_last_check_at", time.Now().Format(time.RFC3339))
	latest, err := FetchLatestPanelRelease(readSecuritySetting("github_proxy"))
	if err != nil || latest == nil || latest.TagName == "" || CompareVersions(latest.TagName, currentVersion) <= 0 {
		return
	}
	if !IsStableVersion(latest.TagName) {
		return
	}
	if settings.Mode != "all_stable" && !IsPatchBump(currentVersion, latest.TagName) {
		recordPanelUpdateStage("auto", latest.TagName, "version_policy", "skipped", "当前策略仅允许 patch 自动更新")
		return
	}
	if wait, ok := shouldWaitForReleaseDelay(settings, latest.TagName); ok {
		recordPanelUpdateStage("auto", latest.TagName, "waiting_release_delay", "waiting", "等待发布成熟期: "+wait.String())
		return
	}
	_, _, sigURL := resolvePanelAssets(latest)
	if sigURL == "" {
		handleWaitingSignature(settings, latest.TagName)
		return
	}
	_, err = ExecutePanelUpdate(PanelUpdateOptions{
		Trigger:        "auto",
		CurrentVersion: currentVersion,
		Proxy:          readSecuritySetting("github_proxy"),
		ConfigPath:     configPath,
		Config:         cfg,
		UseWatchdog:    true,
	})
	if err != nil && err != errWaitingSignature {
		sendPanelUpdateMail(false, latest.TagName, readSecuritySetting("panel_auto_update_last_stage"), err.Error())
	}
}

func readAutoUpdateSettings() autoUpdateSettings {
	mode := readSecuritySetting("panel_auto_update_mode")
	if mode == "" {
		mode = "patch_only"
	}
	window := readSecuritySetting("panel_auto_update_window")
	if window == "" {
		window = "03:00-05:00"
	}
	delay := parseSettingMinutes("panel_auto_update_release_delay_minutes", 15)
	signatureTimeout := parseSettingMinutes("panel_auto_update_signature_timeout_minutes", 120)
	return autoUpdateSettings{
		Enabled:                  readSecuritySetting("panel_auto_update_enabled") == "true",
		Mode:                     mode,
		Window:                   window,
		ReleaseDelay:             delay,
		SignatureTimeout:         signatureTimeout,
		LastTargetVersion:        readSecuritySetting("panel_auto_update_last_target_version"),
		LastCheckAt:              parseSettingTime("panel_auto_update_last_check_at"),
		LastAttemptAt:            parseSettingTime("panel_auto_update_last_attempt_at"),
		LastStatus:               readSecuritySetting("panel_auto_update_last_status"),
		LastStage:                readSecuritySetting("panel_auto_update_last_stage"),
		LastSignatureWaitVersion: readSecuritySetting("panel_auto_update_signature_wait_version"),
		LastSignatureWaitAt:      parseSettingTime("panel_auto_update_signature_wait_at"),
	}
}

func shouldFetchForAutoUpdate(settings autoUpdateSettings, now time.Time) bool {
	if isWaitingForSignature(settings, now) {
		return true
	}
	if isReleaseDelayReady(settings, now) {
		return true
	}
	return settings.LastCheckAt.IsZero() || now.Sub(settings.LastCheckAt) >= autoUpdateFetchInterval
}

func isWaitingForSignature(settings autoUpdateSettings, now time.Time) bool {
	return settings.LastStatus == "waiting" &&
		settings.LastSignatureWaitVersion != "" &&
		!settings.LastSignatureWaitAt.IsZero() &&
		now.Sub(settings.LastSignatureWaitAt) <= settings.SignatureTimeout
}

func isReleaseDelayReady(settings autoUpdateSettings, now time.Time) bool {
	return settings.LastStatus == "waiting" &&
		settings.LastStage == "waiting_release_delay" &&
		settings.LastTargetVersion != "" &&
		!settings.LastAttemptAt.IsZero() &&
		now.Sub(settings.LastAttemptAt) >= settings.ReleaseDelay
}

func parseSettingMinutes(key string, fallback int) time.Duration {
	v, err := strconv.Atoi(readSecuritySetting(key))
	if err != nil || v <= 0 {
		v = fallback
	}
	return time.Duration(v) * time.Minute
}

func parseSettingTime(key string) time.Time {
	v := readSecuritySetting(key)
	if v == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

func shouldWaitForReleaseDelay(settings autoUpdateSettings, version string) (time.Duration, bool) {
	if settings.LastTargetVersion != version || settings.LastAttemptAt.IsZero() {
		setSecuritySetting("panel_auto_update_last_target_version", version)
		setSecuritySetting("panel_auto_update_last_attempt_at", time.Now().Format(time.RFC3339))
		return settings.ReleaseDelay, true
	}
	remaining := settings.ReleaseDelay - time.Since(settings.LastAttemptAt)
	return remaining, remaining > 0
}

func handleWaitingSignature(settings autoUpdateSettings, version string) {
	waitAt := settings.LastSignatureWaitAt
	if settings.LastSignatureWaitVersion != version || waitAt.IsZero() {
		waitAt = time.Now()
		setSecuritySetting("panel_auto_update_signature_wait_version", version)
		setSecuritySetting("panel_auto_update_signature_wait_at", waitAt.Format(time.RFC3339))
	}
	if time.Since(waitAt) > settings.SignatureTimeout {
		msg := "等待签名文件超时，未找到 wp-panel.sha256.sig"
		recordPanelUpdateStage("auto", version, "waiting_signature", "failed", msg)
		sendPanelUpdateMail(false, version, "waiting_signature", msg)
		return
	}
	recordPanelUpdateStage("auto", version, "waiting_signature", "waiting", "未找到 wp-panel.sha256.sig，等待签名发布")
}

func withinAutoUpdateWindow(window string, now time.Time) bool {
	parts := strings.Split(window, "-")
	if len(parts) != 2 {
		return true
	}
	start, err1 := parseClock(parts[0])
	end, err2 := parseClock(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	cur := now.Hour()*60 + now.Minute()
	if start <= end {
		return cur >= start && cur <= end
	}
	return cur >= start || cur <= end
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

func parseStableSemver(version string) ([3]int, bool) {
	var out [3]int
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" || strings.Contains(v, "-") {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func checkUpdateDiskSpace(binaryPath string, cfg *config.Config) error {
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("读取更新包大小失败: %w", err)
	}
	targetDirs := []string{filepath.Dir(panelInstallPath)}
	if cfg != nil && cfg.Panel.BackupDir != "" {
		targetDirs = append(targetDirs, cfg.Panel.BackupDir)
	}
	required := info.Size()*4 + 64*1024*1024
	for _, targetDir := range targetDirs {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(targetDir, &stat); err != nil {
			return fmt.Errorf("检查磁盘空间失败 (%s): %w", targetDir, err)
		}
		free := int64(stat.Bavail) * int64(stat.Bsize)
		if free < required {
			return fmt.Errorf("磁盘剩余空间不足 (%s): 可用 %d MB，需要至少 %d MB", targetDir, free/1024/1024, required/1024/1024)
		}
	}
	return nil
}

func rollbackPlanPath(cfg *config.Config) string {
	return filepath.Join(cfg.Panel.DataDir, "update_rollback.json")
}

func writeRollbackPlan(cfg *config.Config, plan rollbackPlan) (string, error) {
	path := rollbackPlanPath(cfg)
	return path, writeRollbackPlanFile(path, plan)
}

func writeRollbackPlanFile(path string, plan rollbackPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	plan.PlanPath = path
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func healthURL(cfg *config.Config) string {
	if cfg == nil {
		return "http://127.0.0.1:8080/healthz"
	}
	port := cfg.Panel.Port
	scheme := "http"
	if cfg.Panel.TLSPort > 0 && cfg.Panel.TLSCertPath != "" && cfg.Panel.TLSKeyPath != "" {
		port = cfg.Panel.TLSPort
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d/healthz", scheme, port)
}

func startUpdateWatchdog(backupBinary, planPath, configPath string) error {
	unit := fmt.Sprintf("wp-panel-update-watchdog-%d", time.Now().UnixNano())
	cmd := exec.Command(
		"systemd-run",
		"--unit", unit,
		"--collect",
		"--property", "Type=simple",
		"--property", "KillMode=process",
		backupBinary,
		"--update-watchdog", planPath,
		"--config", configPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func healthCheck(rawURL string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	if strings.HasPrefix(rawURL, "https://") {
		client.Transport = &http.Transport{TLSClientConfig: insecureLocalTLSConfig()}
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz HTTP %d", resp.StatusCode)
	}
	return nil
}

func insecureLocalTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true}
}

func shouldRestoreDBAfterHealthFailure(backupPath string) bool {
	if backupPath == "" {
		return false
	}
	db := database.GetDB()
	if db == nil {
		return true
	}
	var version string
	if err := db.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		return true
	}
	for _, table := range []string{"admin_users", "websites", "security_settings"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table + " LIMIT 1").Scan(&n); err != nil {
			return true
		}
	}
	return false
}

func restoreDBFile(cfg *config.Config, backupPath string) error {
	if cfg == nil || backupPath == "" {
		return nil
	}
	if err := database.VerifyDBBackup(backupPath); err != nil {
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	if err := copyPanelFile(backupPath, cfg.SQLite.Path, 0600); err != nil {
		return err
	}
	_ = os.Remove(cfg.SQLite.Path + "-wal")
	_ = os.Remove(cfg.SQLite.Path + "-shm")
	return nil
}

func stageLabel(stage string) string {
	switch stage {
	case "fetch_release":
		return "获取版本信息"
	case "compare_version":
		return "版本比较"
	case "resolve_assets":
		return "解析发布文件"
	case "waiting_release_delay":
		return "等待发布延迟"
	case "waiting_signature":
		return "等待签名文件"
	case "version_policy":
		return "版本策略检查"
	case "download_binary":
		return "下载二进制"
	case "download_sha256":
		return "下载校验文件"
	case "download_signature":
		return "下载签名文件"
	case "verify_signature":
		return "校验签名"
	case "verify_sha256":
		return "校验完整性"
	case "preflight":
		return "预检新版本"
	case "disk_check":
		return "磁盘空间检查"
	case "backup_binary":
		return "备份旧版本"
	case "backup_database":
		return "备份数据库"
	case "write_rollback_plan":
		return "写入回滚计划"
	case "replace_binary":
		return "替换二进制"
	case "start_watchdog":
		return "启动健康检查"
	case "restart":
		return "重启面板"
	case "health_check":
		return "健康检查"
	case "rollback_binary":
		return "回滚二进制"
	case "rollback_database":
		return "回滚数据库"
	default:
		return stage
	}
}

func sendPanelUpdateMail(success bool, targetVersion, stage, message string) {
	cfg := GetSMTPConfig()
	if cfg == nil || cfg.Host == "" || cfg.AdminEmail == "" {
		return
	}
	status := "失败"
	if success {
		status = "成功"
	}
	body := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; padding: 20px; color: #333;">
<h2>WP Panel 自动更新%s</h2>
<p>目标版本：%s</p>
<p>阶段：%s</p>
<p>详情：%s</p>
<p style="font-size: 12px; color: #aaa; margin-top: 20px;">来自 %s 面板</p>
</body></html>`, status, html.EscapeString(targetVersion), html.EscapeString(stageLabel(stage)), html.EscapeString(message), html.EscapeString(getPanelTitle()))
	if err := SendMail("", getPanelTitle()+" 自动更新"+status, body); err != nil {
		log.Printf("自动更新邮件发送失败: %v", err)
	}
}

func readSecuritySetting(key string) string {
	db := database.GetDB()
	if db == nil {
		return ""
	}
	var v string
	_ = db.QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", key).Scan(&v)
	return v
}

func setSecuritySetting(key, value string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	_, _ = db.Exec(`INSERT INTO security_settings (skey, svalue, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(skey) DO UPDATE SET svalue = excluded.svalue, updated_at = excluded.updated_at`, key, value)
}

func clearPanelUpdateCache() {
	panelUpdateCache.mu.Lock()
	panelUpdateCache.lastAt = time.Time{}
	panelUpdateCache.latest = ""
	panelUpdateCache.message = ""
	panelUpdateCache.mu.Unlock()
}

func cleanupPanelUpdateBackups(plan rollbackPlan) {
	if plan.BackupDB != "" {
		backupDir := filepath.Dir(plan.BackupDB)
		if removed := database.CleanupOldDBBackups(backupDir, panelDBBackupKeep); removed > 0 {
			recordOperationLog("panel_"+plan.Trigger+"_update", plan.TargetVersion, "success", fmt.Sprintf("cleanup_database_backups: 已清理 %d 份旧数据库备份", removed))
		}
	}
	if removed := cleanupPanelBinaryBackups(panelBinaryBackupKeep); removed > 0 {
		recordOperationLog("panel_"+plan.Trigger+"_update", plan.TargetVersion, "success", fmt.Sprintf("cleanup_binary_backups: 已清理 %d 份旧二进制备份", removed))
	}
}

func cleanupPanelBinaryBackups(keep int) int {
	if keep <= 0 {
		keep = panelBinaryBackupKeep
	}
	matches, err := filepath.Glob(panelInstallPath + ".bak.*")
	if err != nil || len(matches) <= keep {
		return 0
	}
	sort.Slice(matches, func(i, j int) bool {
		ii, ierr := os.Stat(matches[i])
		ji, jerr := os.Stat(matches[j])
		if ierr == nil && jerr == nil && !ii.ModTime().Equal(ji.ModTime()) {
			return ii.ModTime().After(ji.ModTime())
		}
		return matches[i] > matches[j]
	})
	removed := 0
	for _, path := range matches[keep:] {
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}

func LocalOnly(c net.Addr) bool {
	host, _, err := net.SplitHostPort(c.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
