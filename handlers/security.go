package handlers

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type SecurityHandler struct{}

func (h *SecurityHandler) GetSettings(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, skey, svalue, description, updated_at FROM security_settings")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	var settings []models.SecuritySetting
	for rows.Next() {
		var s models.SecuritySetting
		if err := rows.Scan(&s.ID, &s.Key, &s.Value, &s.Description, &s.UpdatedAt); err != nil {
			continue
		}
		settings = append(settings, s)
	}
	if settings == nil {
		settings = []models.SecuritySetting{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(settings))
}

func (h *SecurityHandler) UpdateSettings(c *gin.Context) {
	var raw map[string]interface{}
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	db := database.GetDB()

	for key, val := range raw {
		strVal, ok, err := normalizeSecuritySetting(key, val)
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
			return
		}
		if !ok {
			continue
		}
		if _, err := db.Exec("UPDATE security_settings SET svalue = ?, updated_at = CURRENT_TIMESTAMP WHERE skey = ?", strVal, key); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("安全设置保存失败"))
			return
		}
	}

	if err := executor.ApplyFail2banSettings(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("Fail2ban 配置应用失败: "+err.Error()))
		return
	}
	if err := applyRateLimit(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("Nginx 限速配置应用失败: "+err.Error()))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "安全设置已更新"}))
}

func (h *SecurityHandler) RefreshWhitelist(c *gin.Context) {
	go refreshOfficialWhitelist()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "白名单刷新任务已提交"}))
}

func refreshOfficialWhitelist() {
	executor.GlobalQueue.Enqueue(executor.TaskRefreshWhitelist, nil)
}

func applyRateLimit() error {
	enabled, rpm, burst := executor.GetRateLimitSettings()
	return executor.EnsureRateLimit(enabled, rpm, burst)
}

func normalizeSecuritySetting(key string, val interface{}) (string, bool, error) {
	switch key {
	case "fail2ban_maxretry":
		return normalizeRange(key, val, 1, 20)
	case "fail2ban_findtime":
		return normalizeRange(key, val, 10, 3600)
	case "fail2ban_bantime":
		return normalizeRange(key, val, 60, 86400)
	case "rate_limit_rpm":
		return normalizeRange(key, val, 10, 600)
	case "rate_limit_burst":
		return normalizeRange(key, val, 5, 600)
	case "auto_whitelist_enabled", "rate_limit_enabled":
		v, err := normalizeBool(val)
		return v, true, err
	case "whitelist_ips":
		v, ok := val.(string)
		if !ok {
			return "", false, fmt.Errorf("白名单格式不正确")
		}
		v = strings.TrimSpace(v)
		if err := validateWhitelistIPs(v); err != nil {
			return "", false, err
		}
		return v, true, nil
	default:
		return "", false, nil
	}
}

func normalizeRange(key string, val interface{}, min int, max int) (string, bool, error) {
	n, err := normalizeInt(val)
	if err != nil {
		return "", false, fmt.Errorf("%s 必须是数字", key)
	}
	if n < min || n > max {
		return "", false, fmt.Errorf("%s 必须在 %d-%d 之间", key, min, max)
	}
	return strconv.Itoa(n), true, nil
}

func normalizeInt(val interface{}) (int, error) {
	switch v := val.(type) {
	case string:
		return strconv.Atoi(strings.TrimSpace(v))
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("invalid int")
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("invalid int")
	}
}

func normalizeBool(val interface{}) (string, error) {
	switch v := val.(type) {
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case string:
		v = strings.TrimSpace(v)
		if v == "true" || v == "false" {
			return v, nil
		}
	}
	return "", fmt.Errorf("开关值不正确")
}

func validateWhitelistIPs(raw string) error {
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 500 {
		return fmt.Errorf("白名单数量过大")
	}
	for _, line := range lines {
		item := strings.TrimSpace(line)
		if item == "" {
			continue
		}
		if strings.ContainsAny(item, " \t\r") {
			return fmt.Errorf("白名单 %s 格式不正确", item)
		}
		if strings.Contains(item, "/") {
			if _, _, err := net.ParseCIDR(item); err != nil {
				return fmt.Errorf("白名单 %s 格式不正确", item)
			}
			continue
		}
		if net.ParseIP(item) == nil {
			return fmt.Errorf("白名单 %s 格式不正确", item)
		}
	}
	return nil
}
