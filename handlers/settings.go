package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type SettingsHandler struct{}

func (h *SettingsHandler) GetSettings(c *gin.Context) {
	db := database.GetDB()
	var username string
	db.QueryRow("SELECT username FROM admin_users LIMIT 1").Scan(&username)

	basicAuthUser := readConfigValue("basic_auth", "username")

	var panelTitle string
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'panel_title'").Scan(&panelTitle)
	if panelTitle == "" {
		panelTitle = "WP Panel"
	}

	var githubProxy string
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'github_proxy'").Scan(&githubProxy)

	timezone := getTimezone()
	hostname := getHostname()
	ntpSynced, ntpServer := getNTPSyncStatus()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"username":        username,
		"basic_auth_user": basicAuthUser,
		"panel_title":     panelTitle,
		"github_proxy":    githubProxy,
		"timezone":        timezone,
		"hostname":        hostname,
		"ntp_synced":      ntpSynced,
		"ntp_server":      ntpServer,
		"server_time":     time.Now().UnixMilli(),
	}))
}

func (h *SettingsHandler) UpdateSettings(c *gin.Context) {
	var req struct {
		PanelTitle    *string `json:"panel_title"`
		Username      *string `json:"username"`
		BasicAuthUser *string `json:"basic_auth_user"`
		OldPassword   *string `json:"old_password"`
		NewPassword   *string `json:"new_password"`
		BasicAuthPw   *string `json:"basic_auth_password"`
		Timezone      *string `json:"timezone"`
		Hostname      *string `json:"hostname"`
		NtpSync       *bool   `json:"ntp_sync"`
		GithubProxy   *string `json:"github_proxy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	db := database.GetDB()

	if req.PanelTitle != nil && *req.PanelTitle != "" {
		_, err := db.Exec("UPDATE security_settings SET svalue = ?, updated_at = CURRENT_TIMESTAMP WHERE skey = 'panel_title'", *req.PanelTitle)
		if err != nil {
			_, _ = db.Exec("INSERT INTO security_settings (skey, svalue, description) VALUES ('panel_title', ?, '面板标题')", *req.PanelTitle)
		}
	}

	if req.Username != nil && *req.Username != "" {
		if _, err := db.Exec("UPDATE admin_users SET username = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1", *req.Username); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新用户名失败"))
			return
		}
	}

	if req.BasicAuthUser != nil && *req.BasicAuthUser != "" {
		if err := updateConfigValue("basic_auth", "username", *req.BasicAuthUser); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新BasicAuth用户名失败"))
			return
		}
		config.AppConfig.BasicAuth.Username = *req.BasicAuthUser
	}

	if req.NewPassword != nil && *req.NewPassword != "" {
		if req.OldPassword == nil || *req.OldPassword == "" {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("请输入当前密码"))
			return
		}
		if len(*req.NewPassword) < 8 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("新密码至少8位"))
			return
		}
		var currentHash string
		err := db.QueryRow("SELECT password_hash FROM admin_users LIMIT 1").Scan(&currentHash)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询用户失败"))
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(*req.OldPassword)); err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("当前密码错误"))
			return
		}
		newHash, err := bcrypt.GenerateFromPassword([]byte(*req.NewPassword), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("密码加密失败"))
			return
		}
		_, err = db.Exec("UPDATE admin_users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1", string(newHash))
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新密码失败"))
			return
		}
	}

	if req.BasicAuthPw != nil && *req.BasicAuthPw != "" {
		if len(*req.BasicAuthPw) < 8 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("BasicAuth密码至少8位"))
			return
		}
		newHash, err := bcrypt.GenerateFromPassword([]byte(*req.BasicAuthPw), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("密码加密失败"))
			return
		}
		if err := updateConfigValue("basic_auth", "password_hash", string(newHash)); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新BasicAuth密码失败"))
			return
		}
		config.AppConfig.BasicAuth.PasswordHash = string(newHash)
	}

	var tzRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_/+\\-]+(/[A-Za-z][A-Za-z0-9_/+\\-]+)*$`)

	if req.Timezone != nil && *req.Timezone != "" {
		tz := strings.TrimSpace(*req.Timezone)
		if !tzRe.MatchString(tz) {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的时区"))
			return
		}
		if err := exec.Command("timedatectl", "set-timezone", tz).Run(); err != nil {
			log.Printf("设置时区失败 (%s): %v", tz, err)
		}
	}

	var hostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?$`)

	if req.Hostname != nil && *req.Hostname != "" {
		host := strings.TrimSpace(*req.Hostname)
		if !hostRe.MatchString(host) || len(host) > 253 || strings.HasPrefix(host, "-") || strings.HasSuffix(host, "-") {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的主机名"))
			return
		}
		exec.Command("hostnamectl", "set-hostname", host).Run()
	}

	if req.NtpSync != nil && *req.NtpSync {
		exec.Command("bash", "-c", "timedatectl set-ntp true 2>/dev/null; systemctl restart systemd-timesyncd 2>/dev/null; ntpdate -u pool.ntp.org 2>/dev/null || true").Run()
	}

	if req.GithubProxy != nil {
		proxy := strings.TrimSpace(*req.GithubProxy)
		proxy = strings.TrimRight(proxy, "/")
		if proxy != "" && !strings.HasPrefix(proxy, "https://") {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("反代地址必须以 https:// 开头"))
			return
		}
		_, err := db.Exec("UPDATE security_settings SET svalue = ?, updated_at = CURRENT_TIMESTAMP WHERE skey = 'github_proxy'", proxy)
		if err != nil {
			_, _ = db.Exec("INSERT INTO security_settings (skey, svalue, description) VALUES ('github_proxy', ?, 'GitHub 反代地址')", proxy)
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "设置已更新"}))
}

func (h *SettingsHandler) TestProxy(c *gin.Context) {
	proxy := strings.TrimRight(strings.TrimSpace(c.Query("url")), "/")
	if proxy == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请提供反代地址"))
		return
	}
	if !strings.HasPrefix(proxy, "https://") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("反代地址必须以 https:// 开头"))
		return
	}

	testURL := proxy + "/https://api.github.com/repos/naibabiji/wp-panel/releases/latest"
	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Get(testURL)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"ok":     false,
			"error":  fmt.Sprintf("连接失败: %v", err),
			"latency": elapsed,
		}))
		return
	}
	resp.Body.Close()

	if resp.StatusCode == 200 {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"ok":     true,
			"latency": elapsed,
		}))
	} else {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"ok":     false,
			"error":  fmt.Sprintf("HTTP %d", resp.StatusCode),
			"latency": elapsed,
		}))
	}
}

func (h *SettingsHandler) GetOperationLogs(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query(
		`SELECT id, operation, target, status, message, created_at
		 FROM operation_logs ORDER BY created_at DESC LIMIT 50`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	var logs []models.OperationLog
	for rows.Next() {
		var l models.OperationLog
		if err := rows.Scan(&l.ID, &l.Operation, &l.Target, &l.Status, &l.Message, &l.CreatedAt); err != nil {
			continue
		}
		logs = append(logs, l)
	}
	if logs == nil {
		logs = []models.OperationLog{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(logs))
}

func GetPanelTitle() string {
	db := database.GetDB()
	if db == nil {
		return "WP Panel"
	}
	var title string
	db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'panel_title'").Scan(&title)
	if title == "" {
		return "WP Panel"
	}
	return title
}

func readConfigValue(section, key string) string {
	data, err := os.ReadFile("/www/server/panel/config.json")
	if err != nil {
		return ""
	}
	var cfg map[string]map[string]interface{}
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	if sec, ok := cfg[section]; ok {
		if v, ok := sec[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func getNTPSyncStatus() (bool, string) {
	out, _ := exec.Command("bash", "-c", "timedatectl show --property=NTP --value 2>/dev/null").CombinedOutput()
	synced := strings.TrimSpace(string(out)) == "yes"
	server := "pool.ntp.org"
	return synced, server
}

func getTimezone() string {
	out, _ := exec.Command("bash", "-c", "timedatectl show --property=Timezone --value 2>/dev/null").CombinedOutput()
	tz := strings.TrimSpace(string(out))
	if tz == "" {
		if data, err := os.ReadFile("/etc/timezone"); err == nil {
			tz = strings.TrimSpace(string(data))
		}
	}
	return tz
}

func getHostname() string {
	out, _ := exec.Command("bash", "-c", "hostnamectl hostname 2>/dev/null || hostname").CombinedOutput()
	return strings.TrimSpace(string(out))
}

// ============================================================
// WordPress 安装包管理
// ============================================================

func (h *SettingsHandler) GetWPPackage(c *gin.Context) {
	cfg := config.AppConfig
	pkgPath := cfg.Paths.WordPressPackage

	info, err := os.Stat(pkgPath)
	if err != nil {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"available": false,
			"path":      pkgPath,
		}))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"available":  true,
		"path":       pkgPath,
		"size":       info.Size(),
		"size_text":  formatFileSize(info.Size()),
		"updated_at": info.ModTime().Format("2006-01-02 15:04:05"),
	}))
}

func (h *SettingsHandler) UploadWPPackage(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请选择文件"))
		return
	}

	// 校验文件扩展名
	name := strings.ToLower(file.Filename)
	if !strings.HasSuffix(name, ".zip") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅支持 .zip 格式的安装包"))
		return
	}

	// 限制文件大小（WordPress 安装包通常 25-30MB，上限 100MB）
	if file.Size > 100*1024*1024 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("文件过大，上限 100MB"))
		return
	}

	cfg := config.AppConfig
	pkgPath := cfg.Paths.WordPressPackage
	pkgDir := filepath.Dir(pkgPath)

	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建目录失败"))
		return
	}

	// 先写入临时文件，校验成功后再替换
	tmpPath := pkgPath + ".upload_tmp"
	if err := c.SaveUploadedFile(file, tmpPath); err != nil {
		os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存文件失败"))
		return
	}

	// 基本校验：用 unzip -t 测试压缩包完整性
	if out, err := exec.Command("unzip", "-t", tmpPath).CombinedOutput(); err != nil {
		os.Remove(tmpPath)
		log.Printf("上传的 ZIP 校验失败: %s, %v", string(out), err)
		c.JSON(http.StatusBadRequest, models.ErrorResponse("文件校验失败，不是有效的 ZIP 压缩包"))
		return
	}

	// 替换旧文件
	os.Remove(pkgPath)
	if err := os.Rename(tmpPath, pkgPath); err != nil {
		os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("替换安装包失败"))
		return
	}

	log.Printf("WordPress 安装包已通过上传更新: %s (%s)", pkgPath, formatFileSize(file.Size))
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": "安装包上传成功",
	}))
}

func (h *SettingsHandler) DownloadWPPackage(c *gin.Context) {
	cfg := config.AppConfig
	pkgPath := cfg.Paths.WordPressPackage
	pkgDir := filepath.Dir(pkgPath)

	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建目录失败"))
		return
	}

	tmpPath := pkgPath + ".download_tmp"
	if out, err := exec.Command("wget", "-q", "-T", "30", "-t", "3", "-O", tmpPath,
		"https://wordpress.org/latest.zip").CombinedOutput(); err != nil {
		os.Remove(tmpPath)
		log.Printf("在线下载 WordPress 安装包失败: %s, %v", string(out), err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("下载失败，请检查服务器网络或手动上传安装包"))
		return
	}

	// 校验下载的文件
	if info, err := os.Stat(tmpPath); err != nil || info.Size() == 0 {
		os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("下载的文件无效"))
		return
	}

	// 校验 ZIP 完整性
	if out, err := exec.Command("unzip", "-t", tmpPath).CombinedOutput(); err != nil {
		os.Remove(tmpPath)
		log.Printf("下载的 ZIP 校验失败: %s, %v", string(out), err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("下载的文件校验失败，请重试或手动上传"))
		return
	}

	// 替换旧文件
	os.Remove(pkgPath)
	if err := os.Rename(tmpPath, pkgPath); err != nil {
		os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("替换安装包失败"))
		return
	}

	// 更新文件时间戳为当前时间（wget 默认保留远程 Last-Modified，导致 mtime 不反映实际下载时间）
	os.Chtimes(pkgPath, time.Now(), time.Now())

	info, _ := os.Stat(pkgPath)
	sizeText := ""
	if info != nil {
		sizeText = formatFileSize(info.Size())
	}

	log.Printf("WordPress 安装包已通过在线下载更新: %s (%s)", pkgPath, sizeText)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": "安装包下载成功",
	}))
}

func (h *SettingsHandler) DeleteWPPackage(c *gin.Context) {
	cfg := config.AppConfig
	pkgPath := cfg.Paths.WordPressPackage

	if err := os.Remove(pkgPath); err != nil && !os.IsNotExist(err) {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("删除失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": "安装包已删除",
	}))
}

func formatFileSize(size int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case size >= GB:
		return fmt.Sprintf("%.1f GB", float64(size)/float64(GB))
	case size >= MB:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(MB))
	case size >= KB:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(KB))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func updateConfigValue(section, key, value string) error {
	configPath := "/www/server/panel/config.json"
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败")
	}
	var cfg map[string]map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("解析配置文件失败")
	}
	sec, ok := cfg[section]
	if !ok {
		return fmt.Errorf("配置段 %s 不存在", section)
	}
	sec[key] = value
	newData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败")
	}
	if err := os.WriteFile(configPath, newData, 0600); err != nil {
		return fmt.Errorf("写入配置文件失败")
	}
	return nil
}
