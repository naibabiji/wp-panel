package handlers

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

// Ed25519 公钥，用于验证 Release 签名的 .sha256 文件。
// 对应私钥离线存储，不在 GitHub / CI 上。
const releasePubKeyHex = "ee8ec641204d785c6469b003c710666126a3156d902b78665bb73e859b6f9546"

type UpdateHandler struct {
	CurrentVersion string
}

const (
	binaryName  = "wp-panel"
	installPath = "/usr/local/bin/wp-panel"
)

var updateMu sync.Mutex
var updateStatusMu sync.Mutex

const updateTerminalStatusTTL = 5 * time.Minute

type panelUpdateStatus struct {
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

var currentUpdateStatus = panelUpdateStatus{
	Stage:     "idle",
	Message:   "等待更新",
	UpdatedAt: time.Now(),
}

func getGithubProxy() string {
	var v string
	database.GetDB().QueryRow("SELECT svalue FROM security_settings WHERE skey = 'github_proxy'").Scan(&v)
	return v
}

func proxyURL(proxy, original string) string {
	if proxy != "" {
		return proxy + "/" + original
	}
	return original
}

func (h *UpdateHandler) Check(c *gin.Context) {
	latest, err := executor.FetchLatestPanelRelease(getGithubProxy())
	if err != nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"current_version": h.CurrentVersion,
			"latest_version":  "",
			"has_update":      false,
			"error":           "获取版本信息失败",
		}))
		return
	}

	hasUpdate := executor.CompareVersions(latest.TagName, h.CurrentVersion) > 0

	notes := latest.Body
	if idx := strings.Index(notes, "**Full Changelog**"); idx >= 0 {
		notes = strings.TrimSpace(notes[:idx])
	}
	if notes == "" {
		notes = "（无更新说明）"
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"current_version": h.CurrentVersion,
		"latest_version":  latest.TagName,
		"release_notes":   notes,
		"has_update":      hasUpdate,
	}))
}

func (h *UpdateHandler) Status(c *gin.Context) {
	c.JSON(http.StatusOK, models.SuccessResponse(snapshotUpdateStatus()))
}

func (h *UpdateHandler) Update(c *gin.Context) {
	if runtime.GOOS != "linux" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅支持 Linux 服务器更新"))
		return
	}
	if !updateMu.TryLock() {
		c.JSON(http.StatusConflict, models.ErrorResponse("已有更新任务正在执行，请稍后再试"))
		return
	}
	defer updateMu.Unlock()

	resetUpdateStatus()
	proxy := getGithubProxy()
	setUpdateStep("fetch_release", "正在获取版本信息...", 5)
	fail := func(code int, message string) {
		setUpdateFailed(message)
		c.JSON(code, models.ErrorResponse(message))
	}

	latest, err := executor.FetchLatestPanelRelease(proxy)
	if err != nil {
		fail(http.StatusInternalServerError, "获取版本信息失败")
		return
	}

	if executor.CompareVersions(latest.TagName, h.CurrentVersion) <= 0 {
		fail(http.StatusBadRequest, "已经是最新版本")
		return
	}

	setUpdateStep("resolve_assets", "正在准备更新文件...", 10)
	var downloadURL string
	var sha256URL string
	var sigURL string
	for _, a := range latest.Assets {
		if a.Name == binaryName {
			downloadURL = a.BrowserDownloadURL
		}
		if a.Name == binaryName+".sha256" {
			sha256URL = a.BrowserDownloadURL
		}
		if a.Name == binaryName+".sha256.sig" {
			sigURL = a.BrowserDownloadURL
		}
	}
	if downloadURL == "" {
		fail(http.StatusInternalServerError, "未找到适用于当前系统的二进制文件")
		return
	}
	if sha256URL == "" {
		fail(http.StatusInternalServerError, "未找到 SHA256 校验文件，无法验证更新完整性")
		return
	}
	if sigURL == "" {
		fail(http.StatusInternalServerError, "未找到 Ed25519 签名文件，无法验证更新来源")
		return
	}

	// Download new binary
	setUpdateStep("prepare_download", "正在创建临时目录...", 12)
	tmpDir, err := os.MkdirTemp("", "wp-panel-update-*")
	if err != nil {
		fail(http.StatusInternalServerError, "创建临时目录失败")
		return
	}
	defer os.RemoveAll(tmpDir)

	newBinary := filepath.Join(tmpDir, binaryName)
	setUpdateStep("download_binary", "正在下载更新包...", 15)
	binaryURL := proxyURL(proxy, downloadURL)
	if err := downloadFileWithProgress(binaryURL, newBinary, 10*time.Minute, setBinaryDownloadProgress); err != nil {
		log.Printf("下载更新包失败 url=%s: %v", binaryURL, err)
		fail(http.StatusInternalServerError, "更新包下载失败，可能是 GitHub 或服务器网络临时异常，请稍后重试")
		return
	}
	if err := os.Chmod(newBinary, 0755); err != nil {
		fail(http.StatusInternalServerError, "设置新版本权限失败")
		return
	}

	// Verify SHA256
	setUpdateStep("download_sha256", "正在下载校验文件...", 62)
	shaFile := filepath.Join(tmpDir, binaryName+".sha256")
	resolvedSHA256URL := proxyURL(proxy, sha256URL)
	if err := downloadFile(resolvedSHA256URL, shaFile); err != nil {
		log.Printf("下载 SHA256 校验文件失败 url=%s: %v", resolvedSHA256URL, err)
		fail(http.StatusInternalServerError, "SHA256 校验文件下载失败，可能是 GitHub 或服务器网络临时异常，请稍后重试")
		return
	}
	setUpdateStep("verify_sha256", "正在校验更新包完整性...", 68)
	if err := verifySHA256(newBinary, shaFile); err != nil {
		fail(http.StatusInternalServerError, "校验失败")
		return
	}

	// Verify Ed25519 signature of checksum file
	setUpdateStep("download_signature", "正在下载签名文件...", 72)
	sigFile := filepath.Join(tmpDir, binaryName+".sha256.sig")
	resolvedSigURL := proxyURL(proxy, sigURL)
	if err := downloadFile(resolvedSigURL, sigFile); err != nil {
		log.Printf("下载签名文件失败 url=%s: %v", resolvedSigURL, err)
		fail(http.StatusInternalServerError, "签名文件下载失败，可能是 GitHub 或服务器网络临时异常，请稍后重试")
		return
	}
	setUpdateStep("verify_signature", "正在校验更新来源...", 78)
	if err := verifyEd25519(shaFile, sigFile); err != nil {
		fail(http.StatusInternalServerError, "签名校验失败")
		return
	}

	setUpdateStep("preflight", "正在预检新版本...", 82)
	if err := preflightBinary(newBinary); err != nil {
		fail(http.StatusInternalServerError, "新版本预检失败")
		return
	}

	setUpdateStep("backup", "正在备份当前版本...", 88)
	backupPath := versionedBackupPath(h.CurrentVersion)
	if err := copyFile(installPath, backupPath, 0755); err != nil {
		fail(http.StatusInternalServerError, "备份旧版本失败")
		return
	}

	setUpdateStep("replace_binary", "正在替换面板文件...", 92)
	stagedBinary := installPath + ".new"
	os.Remove(stagedBinary)
	if err := copyFile(newBinary, stagedBinary, 0755); err != nil {
		os.Remove(stagedBinary)
		fail(http.StatusInternalServerError, "暂存新版本失败")
		return
	}
	if err := os.Rename(stagedBinary, installPath); err != nil {
		os.Remove(stagedBinary)
		fail(http.StatusInternalServerError, "替换失败，旧版本仍保留")
		return
	}
	if err := os.Chmod(installPath, 0755); err != nil {
		if rbErr := copyFile(backupPath, installPath, 0755); rbErr != nil {
			fail(http.StatusInternalServerError, "替换后权限设置失败，且自动回滚失败")
			return
		}
		fail(http.StatusInternalServerError, "替换后权限设置失败，已回滚")
		return
	}

	// Restart service
	setUpdateStep("restart", "正在重启面板...", 98)
	go func() {
		time.Sleep(500 * time.Millisecond)
		exec.Command("systemctl", "restart", "wp-panel").Run()
	}()

	setUpdateCompleted("更新完成，面板正在重启...")
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": fmt.Sprintf("正在更新到 %s，面板即将重启...", latest.TagName),
	}))
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

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}

	if progress != nil {
		progress(0, resp.ContentLength)
	}
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				os.Remove(dest)
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
			out.Close()
			os.Remove(dest)
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	return nil
}

func snapshotUpdateStatus() panelUpdateStatus {
	updateStatusMu.Lock()
	defer updateStatusMu.Unlock()
	if updateStatusExpiredLocked(time.Now()) {
		resetUpdateStatusLocked()
	}
	return currentUpdateStatus
}

func resetUpdateStatus() {
	updateStatusMu.Lock()
	resetUpdateStatusLocked()
	updateStatusMu.Unlock()
}

func resetUpdateStatusLocked() {
	currentUpdateStatus = panelUpdateStatus{
		Stage:     "idle",
		Message:   "等待更新",
		UpdatedAt: time.Now(),
	}
}

func updateStatusExpiredLocked(now time.Time) bool {
	if currentUpdateStatus.Running {
		return false
	}
	if !currentUpdateStatus.Completed && currentUpdateStatus.Error == "" {
		return false
	}
	return now.Sub(currentUpdateStatus.UpdatedAt) > updateTerminalStatusTTL
}

func setUpdateStep(stage, message string, percent int) {
	updateStatusMu.Lock()
	currentUpdateStatus.Running = true
	currentUpdateStatus.Completed = false
	currentUpdateStatus.Stage = stage
	currentUpdateStatus.Message = message
	currentUpdateStatus.Percent = clampPercent(percent)
	currentUpdateStatus.DownloadPercent = 0
	currentUpdateStatus.DownloadedBytes = 0
	currentUpdateStatus.TotalBytes = 0
	currentUpdateStatus.HasTotal = false
	currentUpdateStatus.Error = ""
	currentUpdateStatus.UpdatedAt = time.Now()
	updateStatusMu.Unlock()
}

func setBinaryDownloadProgress(downloaded, total int64) {
	hasTotal := total > 0
	downloadPercent := 0
	overallPercent := 15
	if hasTotal {
		downloadPercent = clampPercent(int(downloaded * 100 / total))
		overallPercent = 15 + downloadPercent*45/100
	}

	updateStatusMu.Lock()
	currentUpdateStatus.Running = true
	currentUpdateStatus.Completed = false
	currentUpdateStatus.Stage = "download_binary"
	currentUpdateStatus.Message = "正在下载更新包..."
	currentUpdateStatus.Percent = clampPercent(overallPercent)
	currentUpdateStatus.DownloadPercent = downloadPercent
	currentUpdateStatus.DownloadedBytes = downloaded
	currentUpdateStatus.TotalBytes = total
	currentUpdateStatus.HasTotal = hasTotal
	currentUpdateStatus.Error = ""
	currentUpdateStatus.UpdatedAt = time.Now()
	updateStatusMu.Unlock()
}

func setUpdateFailed(message string) {
	updateStatusMu.Lock()
	currentUpdateStatus.Running = false
	currentUpdateStatus.Completed = false
	currentUpdateStatus.Message = message
	currentUpdateStatus.Error = message
	currentUpdateStatus.UpdatedAt = time.Now()
	updateStatusMu.Unlock()
}

func setUpdateCompleted(message string) {
	updateStatusMu.Lock()
	currentUpdateStatus.Running = false
	currentUpdateStatus.Completed = true
	currentUpdateStatus.Stage = "completed"
	currentUpdateStatus.Message = message
	currentUpdateStatus.Percent = 100
	currentUpdateStatus.DownloadPercent = 100
	currentUpdateStatus.Error = ""
	currentUpdateStatus.UpdatedAt = time.Now()
	updateStatusMu.Unlock()
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
	actual := fmt.Sprintf("%x", h.Sum(nil))

	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("SHA256 不匹配")
	}
	return nil
}

func preflightBinary(path string) error {
	// Depends on the --info flag registered in main.go; keep this lightweight
	// so updates fail before replacing the current binary when a build is broken.
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
	ts := time.Now().UTC().Format("20060102-150405")
	return fmt.Sprintf("%s.bak.%s.%s", installPath, version, ts)
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

func copyFile(srcPath, dstPath string, mode os.FileMode) error {
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
			dst.Close()
		}
		if !copied {
			os.Remove(dstPath)
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
