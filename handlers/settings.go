package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

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

	timezone := getTimezone()
	hostname := getHostname()
	ntpSynced, ntpServer := getNTPSyncStatus()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"username":        username,
		"basic_auth_user": basicAuthUser,
		"panel_title":     panelTitle,
		"timezone":        timezone,
		"hostname":        hostname,
		"ntp_synced":      ntpSynced,
		"ntp_server":      ntpServer,
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
	}

	var tzRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_/+\\-]+(/[A-Za-z][A-Za-z0-9_/+\\-]+)*$`)

	if req.Timezone != nil && *req.Timezone != "" {
		tz := strings.TrimSpace(*req.Timezone)
		if !tzRe.MatchString(tz) {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的时区"))
			return
		}
		exec.Command("timedatectl", "set-timezone", tz).Run()
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

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "设置已更新"}))
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
