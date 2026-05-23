package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

// canonical column list shared by all website queries.
const websiteCols = `id, name, domain, aliases, status, system_user, web_root, log_dir,
	db_name, db_user, php_pool_path, nginx_conf_path, site_type, ssl_enabled,
	ssl_cert_path, ssl_key_path, ssl_expires_at, template_version, access_log_mode,
	fastcgi_cache_enabled, fastcgi_cache_ttl, fastcgi_cache_key,
	monitoring_enabled, monitoring_interval, expires_at, created_at, updated_at`

// scanWebsite scans the canonical columns into a Website model.
// scanner is either row.Scan (for QueryRow) or rows.Scan (for Rows).
func scanWebsite(scanner func(dest ...interface{}) error) (*models.Website, error) {
	var w models.Website
	var aliases, status string
	var sslEnabled, fCacheEnabled, monitoringEnabled int
	var monitoringInterval int

	err := scanner(
		&w.ID, &w.Name, &w.Domain, &aliases, &status, &w.SystemUser,
		&w.WebRoot, &w.LogDir, &w.DBName, &w.DBUser, &w.PHPPoolPath,
		&w.NginxConfPath, &w.SiteType, &sslEnabled, &w.SSLCertPath, &w.SSLKeyPath,
		&w.SSLExpiresAt, &w.TemplateVersion, &w.AccessLogMode,
		&fCacheEnabled, &w.FCacheTTL, &w.FCacheKey,
		&monitoringEnabled, &monitoringInterval, &w.ExpiresAt,
		&w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	w.Aliases = aliases
	w.Status = models.WebsiteStatus(status)
	w.SSLEnabled = sslEnabled == 1
	w.FCacheEnabled = fCacheEnabled == 1
	w.MonitoringEnabled = monitoringEnabled == 1
	w.MonitoringInterval = monitoringInterval
	return &w, nil
}

type WebsiteHandler struct {
	DB *sql.DB
}

func (h *WebsiteHandler) List(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query("SELECT " + websiteCols + " FROM websites ORDER BY created_at DESC")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	var websites []models.Website
	for rows.Next() {
		w, err := scanWebsite(rows.Scan)
		if err != nil {
			continue
		}
		websites = append(websites, *w)
	}
	if websites == nil {
		websites = []models.Website{}
	}

	type siteRow struct {
		models.Website
		AccessLogEnabled bool `json:"access_log_enabled"`
		AccessLogMode    string `json:"access_log_mode"`
		FCacheEnabled    bool `json:"fastcgi_cache_enabled"`
		BackupEnabled    bool `json:"backup_enabled"`
	}
	result := make([]siteRow, len(websites))
	for i, w := range websites {
		result[i] = siteRow{
			Website:         w,
			AccessLogMode:   w.AccessLogMode,
			FCacheEnabled:   w.FCacheEnabled,
			AccessLogEnabled: w.AccessLogMode != "off",
		}
		var be int
		db.QueryRow("SELECT enabled FROM backup_settings WHERE site_id = ?", w.ID).Scan(&be)
		result[i].BackupEnabled = be == 1
	}

	c.JSON(http.StatusOK, models.SuccessResponse(result))
}

func (h *WebsiteHandler) Get(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	w, err := scanWebsite(database.GetDB().QueryRow(
		"SELECT "+websiteCols+" FROM websites WHERE id = ?", id,
	).Scan)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(w))
}

func (h *WebsiteHandler) Create(c *gin.Context) {
	var req models.CreateWebsiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误: "+err.Error()))
		return
	}

	if strings.TrimSpace(req.Domain) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("域名不能为空"))
		return
	}

	siteType := req.SiteType
	if siteType != "php" {
		siteType = "wordpress"
	}

	payload := &executor.CreateSitePayload{
		Domain:     req.Domain,
		Aliases:    req.Aliases,
		SSLEnabled: req.SSLEnabled,
		DBPassword: req.DBPassword,
		ExpiresAt:  req.ExpiresAt,
		SiteType:   siteType,
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskCreateSite, payload)
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(result.Data))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) Delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	payload := &executor.DeleteSitePayload{Site: site}
	task := executor.GlobalQueue.Enqueue(executor.TaskDeleteSite, payload)
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) ToggleStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	var req models.UpdateWebsiteStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误: "+err.Error()))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	var taskType executor.TaskType
	switch req.Action {
	case "pause":
		taskType = executor.TaskPauseSite
	case "enable":
		taskType = executor.TaskEnableSite
	default:
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效操作"))
		return
	}

	var payload interface{}
	if taskType == executor.TaskPauseSite {
		payload = &executor.PauseSitePayload{Site: site}
	} else {
		payload = &executor.EnableSitePayload{Site: site}
	}

	task := executor.GlobalQueue.Enqueue(taskType, payload)
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) EnableSSL(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	var req struct {
		Mode        string `json:"mode" binding:"required,oneof=auto manual"`
		Certificate string `json:"certificate"`
		PrivateKey  string `json:"private_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误: "+err.Error()))
		return
	}

	if req.Mode == "manual" && (strings.TrimSpace(req.Certificate) == "" || strings.TrimSpace(req.PrivateKey) == "") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("手动模式需要填写证书内容和私钥"))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskEnableSSL, &executor.EnableSSLPayload{
		Site: site, Mode: req.Mode, Certificate: req.Certificate, PrivateKey: req.PrivateKey,
	})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) RemoveSSL(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	if !site.SSLEnabled {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("该网站未启用SSL"))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskRemoveSSL, &executor.RemoveSSLPayload{Site: site})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) UpdateDomains(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	var req struct {
		NewDomain string   `json:"new_domain"`
		Aliases   []string `json:"aliases"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误: "+err.Error()))
		return
	}

	targetDomain := strings.ToLower(strings.TrimSpace(req.NewDomain))
	if targetDomain == "" {
		targetDomain = site.Domain
	}

	if targetDomain != site.Domain {
		var existingID int
		err := database.GetDB().QueryRow("SELECT id FROM websites WHERE domain = ? AND id != ?", targetDomain, site.ID).Scan(&existingID)
		if err == nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("域名 "+targetDomain+" 已被其他网站使用"))
			return
		}
	}

	for _, alias := range req.Aliases {
		alias = strings.TrimSpace(alias)
		if alias != "" && alias == targetDomain {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("别名不能与主域名相同"))
			return
		}
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskUpdateDomains, &executor.UpdateDomainsPayload{
		Site: site, NewDomain: targetDomain, Aliases: req.Aliases,
	})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) ChangeDBPassword(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskChangeDBPassword, &executor.ChangeDBPasswordPayload{
		Site: site, NewPassword: req.NewPassword,
	})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(result.Data))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) ViewLogs(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	logType := c.Query("type")
	if logType != "error" && logType != "access" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error 或 access"))
		return
	}

	linesStr := c.DefaultQuery("lines", "200")
	lines, _ := strconv.Atoi(linesStr)
	if lines <= 0 || lines > 1000 {
		lines = 200
	}

	var logFile string
	if logType == "error" {
		logFile = filepath.Join(site.LogDir, "error.log")
	} else {
		logFile = filepath.Join(site.LogDir, "access.log")
	}

	cleanPath := filepath.Clean(logFile)
	if !strings.HasPrefix(cleanPath, filepath.Clean(site.LogDir)) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("禁止访问该路径"))
		return
	}

	content := tailFile(cleanPath, lines)
	if content == "" {
		if logType == "access" {
			content = "（访问日志：Nginx 默认关闭 access_log 以提升性能，如需启用请在 Nginx 配置中开启）"
		} else {
			content = "（暂无错误日志，网站运行正常）"
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"log_type": logType, "content": content}))
}

func (h *WebsiteHandler) ClearLogs(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	logType := c.Query("type")
	if logType != "error" && logType != "access" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error 或 access"))
		return
	}

	var logFile string
	if logType == "error" {
		logFile = filepath.Join(site.LogDir, "error.log")
	} else {
		logFile = filepath.Join(site.LogDir, "access.log")
	}

	cleanPath := filepath.Clean(logFile)
	if !strings.HasPrefix(cleanPath, filepath.Clean(site.LogDir)) {
		c.JSON(http.StatusForbidden, models.ErrorResponse("禁止访问该路径"))
		return
	}

	if err := os.WriteFile(cleanPath, []byte{}, 0644); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("清空日志失败: "+err.Error()))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "日志已清空"}))
}

func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (h *WebsiteHandler) GetNginxCustom(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	nginxCustomDir := "/www/server/panel/nginx-custom"
	prePath := filepath.Join(nginxCustomDir, site.Domain+".pre.conf")
	mainPath := filepath.Join(nginxCustomDir, site.Domain+".conf")

	preContent, _ := os.ReadFile(prePath)
	content, _ := os.ReadFile(mainPath)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"pre_content":        string(preContent),
		"content":            string(content),
		"access_log_enabled": site.AccessLogMode != "off",
		"access_log_mode":    site.AccessLogMode,
	}))
}

func (h *WebsiteHandler) SaveNginxCustom(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	var req struct {
		PreContent string `json:"pre_content"`
		Content    string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误: "+err.Error()))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskSaveNginxCustom, &executor.SaveNginxCustomPayload{
		Site: site, PreContent: req.PreContent, Content: req.Content,
	})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) SetAccessLogMode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误: "+err.Error()))
		return
	}
	if req.Mode != "off" && req.Mode != "error_only" && req.Mode != "full" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的日志模式"))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskSetAccessLogMode, &executor.SetAccessLogModePayload{
		Site: site, Mode: req.Mode,
	})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func getWebsiteByID(id int) *models.Website {
	w, err := scanWebsite(database.GetDB().QueryRow(
		"SELECT "+websiteCols+" FROM websites WHERE id = ?", id,
	).Scan)
	if err != nil {
		return nil
	}
	return w
}

func (h *WebsiteHandler) InstallPlugin(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	var domain, webRoot, systemUser string
	err = database.GetDB().QueryRow("SELECT domain, web_root, system_user FROM websites WHERE id = ?", id).Scan(&domain, &webRoot, &systemUser)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	src := "/www/server/panel/packages/wp-panel-cache-helper.php"
	pluginDir := filepath.Join(webRoot, "wp-content", "plugins", "wp-panel-cache-helper")
	dst := filepath.Join(pluginDir, "wp-panel-cache-helper.php")
	os.MkdirAll(pluginDir, 0755)

	srcData, err := os.ReadFile(src)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("插件源文件不存在，请先升级面板"))
		return
	}
	os.WriteFile(dst, srcData, 0644)

	apiKey := executor.NewCacheKey()
	database.GetDB().Exec("UPDATE websites SET plugin_api_key = ? WHERE id = ?", apiKey, id)

	cfg := config.AppConfig
	panelURL := fmt.Sprintf("https://127.0.0.1:%d/%s", cfg.Panel.TLSPort, cfg.Panel.RandomSuffix)
	cfgJSON, _ := json.Marshal(map[string]string{
		"panel_url": panelURL,
		"api_key":   apiKey,
	})
	os.WriteFile(filepath.Join(pluginDir, "wp-panel-config.json"), cfgJSON, 0644)

	exec.Command("chown", "-R", systemUser+":www-data", pluginDir).Run()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message":   "插件已安装",
		"path":      "wp-content/plugins/wp-panel-cache-helper/",
		"panel_url": panelURL,
	}))
}

func (h *WebsiteHandler) InstallPluginStatus(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	var domain, webRoot string
	err = database.GetDB().QueryRow("SELECT domain, web_root FROM websites WHERE id = ?", id).Scan(&domain, &webRoot)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	dst := filepath.Join(webRoot, "wp-content", "plugins", "wp-panel-cache-helper", "wp-panel-cache-helper.php")
	dstInfo, dstErr := os.Stat(dst)
	srcInfo, srcErr := os.Stat("/www/server/panel/packages/wp-panel-cache-helper.php")

	status := "not_installed"
	if dstErr == nil {
		status = "installed"
		if srcErr == nil && dstInfo.ModTime().Before(srcInfo.ModTime()) {
			status = "update_available"
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"status":      status,
		"plugin_path": "wp-content/plugins/wp-panel-cache-helper/",
	}))
}

func (h *WebsiteHandler) UpdateCache(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
		TTL     int  `json:"ttl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.TTL < 10 {
		req.TTL = 300
	}
	if req.TTL > 86400 {
		req.TTL = 86400
	}

	enabled := 0
	if req.Enabled {
		enabled = 1
	}
	database.GetDB().Exec("UPDATE websites SET fastcgi_cache_enabled = ?, fastcgi_cache_ttl = ? WHERE id = ?", enabled, req.TTL, id)

	go executor.RegenerateSiteNginx(id)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "缓存设置已更新"}))
}

func (h *WebsiteHandler) SaveMonitoring(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	var req struct {
		Enabled  bool `json:"enabled"`
		Interval int  `json:"interval"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.Interval < 1 {
		req.Interval = 5
	}
	enabled := 0
	if req.Enabled {
		enabled = 1
	}
	database.GetDB().Exec("UPDATE websites SET monitoring_enabled = ?, monitoring_interval = ? WHERE id = ?", enabled, req.Interval, id)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func (h *WebsiteHandler) ClearCache(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	go executor.ClearSiteCache(id)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "缓存已清除，旧缓存将在60分钟内自动回收"}))
}

func (h *WebsiteHandler) ReinstallWordPress(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	var domain, webRoot, systemUser, dbName, dbUser, siteType string
	err = database.GetDB().QueryRow(
		"SELECT domain, web_root, system_user, db_name, db_user, site_type FROM websites WHERE id = ?", id,
	).Scan(&domain, &webRoot, &systemUser, &dbName, &dbUser, &siteType)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	if siteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持重装功能"))
		return
	}

	cfg := config.AppConfig
	if err := executor.ReinstallWordPress(cfg.Paths.WordPressPackage, webRoot, dbName, dbUser, systemUser, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("WordPress 重装失败: "+err.Error()))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": "WordPress 已重装完成，数据库和文件均已恢复为全新状态",
	}))
}

// ============================================================
// CacheHelperHandler — WordPress 插件 API
// ============================================================

type CacheHelperHandler struct{}

func (h *CacheHelperHandler) checkAPIKey(domain string, c *gin.Context) bool {
	key := c.GetHeader("X-WP-Panel-Key")
	if key == "" {
		return false
	}
	var storedKey string
	err := database.GetDB().QueryRow(
		"SELECT plugin_api_key FROM websites WHERE domain = ? OR aliases LIKE ?",
		domain, "%"+domain+"%",
	).Scan(&storedKey)
	if err != nil {
		return false
	}
	return storedKey != "" && key == storedKey
}

func (h *CacheHelperHandler) UpdateCacheSettings(c *gin.Context) {
	var req struct {
		Domain string `json:"domain"`
		TTL    int    `json:"ttl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Domain == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if !h.checkAPIKey(req.Domain, c) {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}
	if req.TTL < 10 {
		req.TTL = 300
	}
	if req.TTL > 86400 {
		req.TTL = 86400
	}

	db := database.GetDB()
	_, err := db.Exec("UPDATE websites SET fastcgi_cache_ttl = ? WHERE (domain = ? OR aliases LIKE '%' || ? || '%')", req.TTL, req.Domain, req.Domain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新失败"))
		return
	}

	var siteID int
	db.QueryRow("SELECT id FROM websites WHERE (domain = ? OR aliases LIKE '%' || ? || '%')", req.Domain, req.Domain).Scan(&siteID)
	if siteID > 0 {
		go executor.RegenerateSiteNginx(siteID)
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "TTL 已更新", "ttl": req.TTL}))
}

func (h *CacheHelperHandler) ClearByDomain(c *gin.Context) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Domain == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if !h.checkAPIKey(req.Domain, c) {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}

	var siteID int
	err := database.GetDB().QueryRow(
		"SELECT id FROM websites WHERE (domain = ? OR aliases LIKE '%' || ? || '%')",
		req.Domain, req.Domain,
	).Scan(&siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	go executor.ClearSiteCache(siteID)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "缓存已清除"}))
}

func (h *CacheHelperHandler) FindByDomain(c *gin.Context) {
	domain := c.Query("domain")
	if domain == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if !h.checkAPIKey(domain, c) {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}

	var siteID int
	err := database.GetDB().QueryRow(
		"SELECT id FROM websites WHERE domain = ? OR aliases LIKE ?",
		domain, "%"+domain+"%",
	).Scan(&siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"site_id": siteID, "domain": domain}))
}
