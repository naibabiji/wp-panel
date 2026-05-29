package handlers

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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

func (h *UpdateHandler) Check(c *gin.Context) {
	latest, err := executor.FetchLatestPanelRelease()
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

func (h *UpdateHandler) Update(c *gin.Context) {
	if runtime.GOOS != "linux" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅支持 Linux 服务器更新"))
		return
	}

	latest, err := executor.FetchLatestPanelRelease()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("获取版本信息失败"))
		return
	}

	if executor.CompareVersions(latest.TagName, h.CurrentVersion) <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("已经是最新版本"))
		return
	}

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
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("未找到适用于当前系统的二进制文件"))
		return
	}

	// Download new binary
	tmpDir := "/tmp/wp-panel-update"
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	newBinary := filepath.Join(tmpDir, binaryName)
	if err := downloadFile(downloadURL, newBinary); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("下载失败"))
		return
	}
	os.Chmod(newBinary, 0755)

	// Verify SHA256
	if sha256URL != "" {
		shaFile := filepath.Join(tmpDir, binaryName+".sha256")
		if err := downloadFile(sha256URL, shaFile); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("SHA256 校验文件下载失败"))
			return
		}
		if err := verifySHA256(newBinary, shaFile); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("校验失败"))
			return
		}

		// Verify Ed25519 signature of checksum file
		if sigURL == "" {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("未找到 Ed25519 签名文件，无法验证更新来源"))
			return
		}
		sigFile := filepath.Join(tmpDir, binaryName+".sha256.sig")
		if err := downloadFile(sigURL, sigFile); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("签名文件下载失败"))
			return
		}
		if err := verifyEd25519(shaFile, sigFile); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("签名校验失败"))
			return
		}
	}

	// Backup old binary
	backupPath := installPath + ".bak"
	if _, err := os.Stat(installPath); err == nil {
		os.Rename(installPath, backupPath)
	}

	// Copy new binary (cannot os.Rename cross-filesystem from /tmp)
	src, err := os.Open(newBinary)
	if err != nil {
		os.Rename(backupPath, installPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("替换失败，已回滚"))
		return
	}
	defer src.Close()
	dst, err := os.Create(installPath)
	if err != nil {
		src.Close()
		os.Rename(backupPath, installPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("替换失败，已回滚"))
		return
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		src.Close()
		os.Remove(installPath)
		os.Rename(backupPath, installPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("替换失败，已回滚"))
		return
	}
	dst.Close()
	os.Chmod(installPath, 0755)

	// Restart service
	go func() {
		time.Sleep(500 * time.Millisecond)
		exec.Command("systemctl", "restart", "wp-panel").Run()
	}()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": fmt.Sprintf("正在更新到 %s，面板即将重启...", latest.TagName),
	}))
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func verifySHA256(filePath, shaFile string) error {
	data, err := os.ReadFile(shaFile)
	if err != nil {
		return err
	}
	expected := strings.Fields(string(data))[0]

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	io.Copy(h, f)
	actual := fmt.Sprintf("%x", h.Sum(nil))

	if !strings.EqualFold(expected, actual) {
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
