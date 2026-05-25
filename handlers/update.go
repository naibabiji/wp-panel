package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type UpdateHandler struct {
	CurrentVersion string
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

const (
	repoOwner   = "naibabiji"
	repoName    = "wp-panel"
	binaryName  = "wp-panel"
	installPath = "/usr/local/bin/wp-panel"
)

func (h *UpdateHandler) Check(c *gin.Context) {
	latest, err := fetchLatestRelease()
	if err != nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"current_version": h.CurrentVersion,
			"latest_version":  "",
			"has_update":      false,
			"error":           "获取版本信息失败",
		}))
		return
	}

	hasUpdate := compareVersions(latest.TagName, h.CurrentVersion) > 0

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

	latest, err := fetchLatestRelease()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("获取版本信息失败"))
		return
	}

	if compareVersions(latest.TagName, h.CurrentVersion) <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("已经是最新版本"))
		return
	}

	var downloadURL string
	var sha256URL string
	for _, a := range latest.Assets {
		if a.Name == binaryName {
			downloadURL = a.BrowserDownloadURL
		}
		if a.Name == binaryName+".sha256" {
			sha256URL = a.BrowserDownloadURL
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

func fetchLatestRelease() (*githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("解析版本信息失败: %w", err)
	}
	return &release, nil
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

func compareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	ap := strings.SplitN(a, "-", 2)
	bp := strings.SplitN(b, "-", 2)
	aNums := strings.Split(ap[0], ".")
	bNums := strings.Split(bp[0], ".")
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(aNums) {
			fmt.Sscanf(aNums[i], "%d", &av)
		}
		if i < len(bNums) {
			fmt.Sscanf(bNums[i], "%d", &bv)
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	aPre := ""
	bPre := ""
	if len(ap) > 1 {
		aPre = ap[1]
	}
	if len(bp) > 1 {
		bPre = bp[1]
	}
	if aPre == "" && bPre == "" {
		return 0
	}
	if aPre == "" {
		return 1
	}
	if bPre == "" {
		return -1
	}
	return comparePreRelease(aPre, bPre)
}

func comparePreRelease(a, b string) int {
	aParts := splitAlphaNum(a)
	bParts := splitAlphaNum(b)
	n := len(aParts)
	if len(bParts) < n {
		n = len(bParts)
	}
	for i := 0; i < n; i++ {
		aIsNum := isNumeric(aParts[i])
		bIsNum := isNumeric(bParts[i])
		if aIsNum && bIsNum {
			av, bv := 0, 0
			fmt.Sscanf(aParts[i], "%d", &av)
			fmt.Sscanf(bParts[i], "%d", &bv)
			if av > bv {
				return 1
			}
			if av < bv {
				return -1
			}
		} else {
			if aParts[i] > bParts[i] {
				return 1
			}
			if aParts[i] < bParts[i] {
				return -1
			}
		}
	}
	if len(aParts) > len(bParts) {
		return 1
	}
	if len(aParts) < len(bParts) {
		return -1
	}
	return 0
}

func splitAlphaNum(s string) []string {
	var parts []string
	if s == "" {
		return parts
	}
	current := ""
	isDigit := -1
	for _, ch := range s {
		curIsDigit := 0
		if ch >= '0' && ch <= '9' {
			curIsDigit = 1
		}
		if curIsDigit != isDigit && current != "" {
			parts = append(parts, current)
			current = ""
		}
		isDigit = curIsDigit
		current += string(ch)
	}
	parts = append(parts, current)
	return parts
}

func isNumeric(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
