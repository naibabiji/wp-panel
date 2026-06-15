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

	normalized := make(map[string]string)
	for key, val := range raw {
		strVal, ok, err := normalizeSecuritySetting(key, val)
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
			return
		}
		if !ok {
			continue
		}
		normalized[key] = strVal
	}

	var oldWPSecurityWhitelist string
	if newVal, ok := normalized["wp_security_log_whitelist"]; ok {
		_ = db.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'wp_security_log_whitelist'").Scan(&oldWPSecurityWhitelist)
		if _, err := db.Exec("UPDATE security_settings SET svalue = ?, updated_at = CURRENT_TIMESTAMP WHERE skey = 'wp_security_log_whitelist'", newVal); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("安全设置保存失败"))
			return
		}
		if err := executor.EnsureLogMap(); err != nil {
			_, _ = db.Exec("UPDATE security_settings SET svalue = ?, updated_at = CURRENT_TIMESTAMP WHERE skey = 'wp_security_log_whitelist'", oldWPSecurityWhitelist)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("Nginx 日志规则应用失败，已回滚白名单设置: "+err.Error()))
			return
		}
		delete(normalized, "wp_security_log_whitelist")
	}

	for key, strVal := range normalized {
		if _, err := db.Exec("UPDATE security_settings SET svalue = ?, updated_at = CURRENT_TIMESTAMP WHERE skey = ?", strVal, key); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("安全设置保存失败"))
			return
		}
	}

	if needsFail2banApply(normalized) {
		if err := executor.ApplyFail2banSettings(); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("Fail2ban 配置应用失败: "+err.Error()))
			return
		}
	}
	if needsRateLimitApply(normalized) {
		if err := applyRateLimit(); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("Nginx 限速配置应用失败: "+err.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "安全设置已更新"}))
}

func needsFail2banApply(settings map[string]string) bool {
	for _, key := range []string{"fail2ban_maxretry", "fail2ban_findtime", "fail2ban_bantime", "auto_whitelist_enabled", "whitelist_ips"} {
		if _, ok := settings[key]; ok {
			return true
		}
	}
	return false
}

func needsRateLimitApply(settings map[string]string) bool {
	for _, key := range []string{"rate_limit_enabled", "rate_limit_rpm", "rate_limit_burst"} {
		if _, ok := settings[key]; ok {
			return true
		}
	}
	return false
}

func (h *SecurityHandler) RefreshWhitelist(c *gin.Context) {
	executor.GoSafe(refreshOfficialWhitelist)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "白名单刷新任务已提交"}))
}

func (h *SecurityHandler) ListCDNRealIPGroups(c *gin.Context) {
	groups, err := executor.ListCDNRealIPGroups()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询 CDN 配置组失败"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(groups))
}

func (h *SecurityHandler) CreateCDNRealIPGroup(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		HeaderName  string `json:"header_name"`
		IPRanges    string `json:"ip_ranges"`
		Enabled     *bool  `json:"enabled"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	name, header, ranges, enabled, desc, err := normalizeCDNRealIPGroupPayload(req.Name, req.HeaderName, req.IPRanges, req.Enabled, req.Description)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
		return
	}
	res, err := database.GetDB().Exec(`INSERT INTO cdn_realip_groups (name, provider, header_name, ip_ranges, builtin, enabled, description)
		VALUES (?, 'custom', ?, ?, 0, ?, ?)`, name, header, ranges, boolToInt(enabled), desc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建 CDN 配置组失败"))
		return
	}
	if err := executor.ApplyFail2banSettings(); err != nil {
		if id, idErr := res.LastInsertId(); idErr == nil {
			_, _ = database.GetDB().Exec(`DELETE FROM cdn_realip_groups WHERE id = ? AND builtin = 0`, id)
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("CDN 配置组已保存，但 Fail2ban 白名单应用失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "CDN 配置组已创建"}))
}

func (h *SecurityHandler) UpdateCDNRealIPGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的配置组ID"))
		return
	}
	group, err := executor.GetCDNRealIPGroup(id)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("CDN 配置组不存在"))
		return
	}
	if group.Builtin {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("内置 CDN 配置组不可修改"))
		return
	}

	var req struct {
		Name        string `json:"name"`
		HeaderName  string `json:"header_name"`
		IPRanges    string `json:"ip_ranges"`
		Enabled     *bool  `json:"enabled"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	name, header, ranges, enabled, desc, err := normalizeCDNRealIPGroupPayload(req.Name, req.HeaderName, req.IPRanges, req.Enabled, req.Description)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
		return
	}
	if _, err := database.GetDB().Exec(`UPDATE cdn_realip_groups
		SET name = ?, header_name = ?, ip_ranges = ?, enabled = ?, description = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, name, header, ranges, boolToInt(enabled), desc, id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存 CDN 配置组失败"))
		return
	}
	if err := executor.ApplyFail2banSettings(); err != nil {
		_, _ = database.GetDB().Exec(`UPDATE cdn_realip_groups
			SET name = ?, header_name = ?, ip_ranges = ?, enabled = ?, description = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`, group.Name, group.HeaderName, group.IPRanges, boolToInt(group.Enabled), group.Description, id)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("CDN 配置组已保存，但 Fail2ban 白名单应用失败: "+err.Error()))
		return
	}
	if err := executor.RegenerateAllSitesNginx(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("CDN 配置组已保存，但部分网站 Nginx 配置更新失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "CDN 配置组已保存"}))
}

func (h *SecurityHandler) DeleteCDNRealIPGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的配置组ID"))
		return
	}
	group, err := executor.GetCDNRealIPGroup(id)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("CDN 配置组不存在"))
		return
	}
	if group.Builtin {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("内置 CDN 配置组不可删除"))
		return
	}
	if _, err := database.GetDB().Exec(`DELETE FROM cdn_realip_groups WHERE id = ?`, id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("删除 CDN 配置组失败"))
		return
	}
	if err := executor.ApplyFail2banSettings(); err != nil {
		_, _ = database.GetDB().Exec(`INSERT OR IGNORE INTO cdn_realip_groups (id, name, provider, header_name, ip_ranges, builtin, enabled, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, group.ID, group.Name, group.Provider, group.HeaderName, group.IPRanges, boolToInt(group.Builtin), boolToInt(group.Enabled), group.Description, group.CreatedAt, group.UpdatedAt)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("CDN 配置组已删除，但 Fail2ban 白名单应用失败: "+err.Error()))
		return
	}
	if err := executor.RegenerateAllSitesNginx(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("CDN 配置组已删除，但部分网站 Nginx 配置更新失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "CDN 配置组已删除"}))
}

func refreshOfficialWhitelist() {
	executor.GlobalQueue.Enqueue(executor.TaskRefreshWhitelist, nil)
}

func normalizeCDNRealIPGroupPayload(name, headerName, rawRanges string, enabled *bool, description string) (string, string, string, bool, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 50 || strings.ContainsAny(name, "\r\n\t") {
		return "", "", "", false, "", fmt.Errorf("CDN 配置组名称格式不正确")
	}
	header, err := executor.NormalizeCDNRealIPHeader(headerName)
	if err != nil {
		return "", "", "", false, "", err
	}
	ranges, err := executor.NormalizeCDNRealIPRanges(rawRanges)
	if err != nil {
		return "", "", "", false, "", err
	}
	isEnabled := true
	if enabled != nil {
		isEnabled = *enabled
	}
	description = strings.TrimSpace(description)
	if len(description) > 200 {
		return "", "", "", false, "", fmt.Errorf("备注过长")
	}
	return name, header, executor.JoinCDNRealIPRanges(ranges), isEnabled, description, nil
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
	case "wp_security_log_whitelist":
		v, ok := val.(string)
		if !ok {
			return "", false, fmt.Errorf("WordPress安全日志白名单格式不正确")
		}
		patterns, err := executor.NormalizeWPSecurityLogWhitelist(v)
		if err != nil {
			return "", false, err
		}
		return strings.Join(patterns, "\n"), true, nil
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

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
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
