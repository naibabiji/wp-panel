package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	monitoring_enabled, monitoring_interval, disable_wp_updates, disable_file_editing,
		xmlrpc_enabled, wp_debug_enabled, wp_post_revisions, wp_memory_limit,
		log_retention_days, expires_at, created_at, updated_at`

// scanWebsite scans the canonical columns into a Website model.
// scanner is either row.Scan (for QueryRow) or rows.Scan (for Rows).
func scanWebsite(scanner func(dest ...interface{}) error) (*models.Website, error) {
	var w models.Website
	var aliases, status string
	var sslEnabled, fCacheEnabled, monitoringEnabled int
	var monitoringInterval int
	var disableWPUpdates, disableFileEditing, xmlrpcEnabled int
	var wpDebugEnabled int
	var wpPostRevisions int
	var wpMemoryLimit string
	var logRetentionDays int

	err := scanner(
		&w.ID, &w.Name, &w.Domain, &aliases, &status, &w.SystemUser,
		&w.WebRoot, &w.LogDir, &w.DBName, &w.DBUser, &w.PHPPoolPath,
		&w.NginxConfPath, &w.SiteType, &sslEnabled, &w.SSLCertPath, &w.SSLKeyPath,
		&w.SSLExpiresAt, &w.TemplateVersion, &w.AccessLogMode,
		&fCacheEnabled, &w.FCacheTTL, &w.FCacheKey,
		&monitoringEnabled, &monitoringInterval, &disableWPUpdates, &disableFileEditing,
		&xmlrpcEnabled, &wpDebugEnabled, &wpPostRevisions, &wpMemoryLimit,
		&logRetentionDays, &w.ExpiresAt,
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
	w.DisableWPUpdates = disableWPUpdates == 1
	w.DisableFileEditing = disableFileEditing == 1
	w.XMLRPCEnabled = xmlrpcEnabled == 1
	w.WPDebugEnabled = wpDebugEnabled == 1
	w.WPPostRevisions = wpPostRevisions
	w.WPMemoryLimit = wpMemoryLimit
	w.LogRetentionDays = logRetentionDays
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
		AccessLogEnabled bool   `json:"access_log_enabled"`
		AccessLogMode    string `json:"access_log_mode"`
		FCacheEnabled    bool   `json:"fastcgi_cache_enabled"`
		BackupEnabled    bool   `json:"backup_enabled"`
	}
	result := make([]siteRow, len(websites))
	for i, w := range websites {
		result[i] = siteRow{
			Website:          w,
			AccessLogMode:    w.AccessLogMode,
			FCacheEnabled:    w.FCacheEnabled,
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
	if w.SiteType == "wordpress" {
		if prefix, err := executor.ReadWPTablePrefix(w.WebRoot); err == nil {
			w.TablePrefix = prefix
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(w))
}

func isAliasConflicting(alias string, excludeID int) (bool, string) {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return false, ""
	}
	rows, err := database.GetDB().Query(
		"SELECT domain, aliases FROM websites WHERE id != ?", excludeID)
	if err != nil {
		return false, ""
	}
	defer rows.Close()
	for rows.Next() {
		var domain, aliases string
		rows.Scan(&domain, &aliases)
		if alias == strings.ToLower(domain) {
			return true, domain
		}
		for _, a := range strings.Split(aliases, "\n") {
			if alias == strings.ToLower(strings.TrimSpace(a)) {
				return true, domain
			}
		}
	}
	return false, ""
}

func (h *WebsiteHandler) Create(c *gin.Context) {
	var req models.CreateWebsiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	if strings.TrimSpace(req.Domain) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("域名不能为空"))
		return
	}
	if conflict, target := isAliasConflicting(req.Domain, 0); conflict {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("域名 "+req.Domain+" 已被站点 "+target+" 使用"))
		return
	}

	for _, alias := range req.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if alias == req.Domain {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("别名不能与主域名相同"))
			return
		}
		if conflict, target := isAliasConflicting(alias, 0); conflict {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("别名 "+alias+" 已被站点 "+target+" 使用"))
			return
		}
	}

	siteType := req.SiteType
	if siteType != "php" {
		siteType = "wordpress"
	}

	payload := &executor.CreateSitePayload{
		Domain:             req.Domain,
		Aliases:            req.Aliases,
		SSLEnabled:         req.SSLEnabled,
		DBPassword:         req.DBPassword,
		ExpiresAt:          req.ExpiresAt,
		SiteType:           siteType,
		CleanDefaults:      req.CleanDefaults,
		RemoveUnusedThemes: req.RemoveUnusedThemes,
		InstallThemes:      req.InstallThemes,
		InstallPlugins:     req.InstallPlugins,
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	targetDomain := strings.ToLower(strings.TrimSpace(req.NewDomain))
	if targetDomain == "" {
		targetDomain = site.Domain
	}

	if targetDomain != site.Domain {
		if conflict, existing := isAliasConflicting(targetDomain, site.ID); conflict {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("域名 "+targetDomain+" 已被站点 "+existing+" 使用"))
			return
		}
	}

	for _, alias := range req.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if alias == targetDomain {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("别名不能与主域名相同"))
			return
		}
		if conflict, target := isAliasConflicting(alias, site.ID); conflict {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("别名 "+alias+" 已被站点 "+target+" 使用"))
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

func (h *WebsiteHandler) FixWPConfig(c *gin.Context) {
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
		TablePrefix string `json:"table_prefix"`
	}
	c.ShouldBindJSON(&req)

	if err := executor.FixWPConfigCredentials(site.WebRoot, site.Domain, site.DBName, site.DBUser, req.TablePrefix); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(err.Error()))
		return
	}

	msg := "wp-config.php 数据库名和用户名已更新"
	if req.TablePrefix != "" {
		msg = "wp-config.php 数据库名、用户名和表前缀已更新"
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": msg}))
}

func (h *WebsiteHandler) DetectDBTablePrefix(c *gin.Context) {
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

	if site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持此功能"))
		return
	}

	cfg := config.AppConfig
	prefix, err := executor.DetectDBTablePrefix(site.DBName, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("检测失败: "+err.Error()))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"prefix": prefix}))
}

func (h *WebsiteHandler) GetWPSiteURLs(c *gin.Context) {
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

	if site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持此功能"))
		return
	}

	if site.TablePrefix == "" {
		if prefix, err := executor.ReadWPTablePrefix(site.WebRoot); err == nil {
			site.TablePrefix = prefix
		}
	}
	if site.TablePrefix == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("未检测到表前缀，请先同步数据库信息"))
		return
	}

	cfg := config.AppConfig
	siteURL, homeURL, err := executor.ReadWPSiteURLs(site.DBName, site.TablePrefix, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取失败: "+err.Error()))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"siteurl": siteURL,
		"home":    homeURL,
	}))
}

func (h *WebsiteHandler) UpdateWPSiteURLs(c *gin.Context) {
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

	if site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持此功能"))
		return
	}

	var req struct {
		SiteURL string `json:"siteurl"`
		HomeURL string `json:"home"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if strings.TrimSpace(req.SiteURL) == "" && strings.TrimSpace(req.HomeURL) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("至少填写一个 URL"))
		return
	}

	if site.TablePrefix == "" {
		if prefix, err := executor.ReadWPTablePrefix(site.WebRoot); err == nil {
			site.TablePrefix = prefix
		}
	}
	if site.TablePrefix == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("未检测到表前缀，请先同步数据库信息"))
		return
	}

	cfg := config.AppConfig
	if err := executor.UpdateWPSiteURLs(site.DBName, site.TablePrefix, req.SiteURL, req.HomeURL, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新失败: "+err.Error()))
		return
	}

	// 异步清理缓存，避免旧域名缓存导致后台显示不一致
	executor.GoSafe(func() {
		exec.Command("find", "/var/cache/nginx/fastcgi", "-type", "f", "-delete").Run()
	})
	executor.GoSafe(func() {
		redisPrefix := strings.ToLower(site.Domain) + ":"
		keys, err := exec.Command("redis-cli", "--scan", "--pattern", redisPrefix+"*").Output()
		if err != nil {
			return
		}
		for _, k := range strings.Split(string(keys), "\n") {
			k = strings.TrimSpace(k)
			if k != "" {
				exec.Command("redis-cli", "DEL", k).Run()
			}
		}
	})

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "站点 URL 已更新"}))
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
			content = "（暂无异常访问日志；默认仅记录 4xx/5xx 请求，正常访问不会写入 access.log）"
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
		log.Printf("清空日志失败 path=%s: %v", cleanPath, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("清空日志失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "日志已清空"}))
}

func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	const bufSize = 4096
	info, _ := f.Stat()
	pos := info.Size()
	var chunks [][]byte
	total := 0
	for pos > 0 && total < n*bufSize {
		readSize := int64(bufSize)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		b := make([]byte, readSize)
		f.ReadAt(b, pos)
		chunks = append(chunks, b)
		total += len(b)
	}
	var data []byte
	for i := len(chunks) - 1; i >= 0; i-- {
		data = append(data, chunks[i]...)
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
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

	src := "/www/server/panel/packages/wp-panel-optimizer.php"
	pluginDir := filepath.Join(webRoot, "wp-content", "plugins", "wp-panel-optimizer")
	dst := filepath.Join(pluginDir, "wp-panel-optimizer.php")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建插件目录失败"))
		return
	}

	srcData, err := os.ReadFile(src)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("插件源文件不存在，请先升级面板"))
		return
	}
	if err := os.WriteFile(dst, srcData, 0644); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("写入插件文件失败"))
		return
	}

	apiKey := executor.NewAPIKey()
	if _, err := database.GetDB().Exec("UPDATE websites SET plugin_api_key = ? WHERE id = ?", apiKey, id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存插件密钥失败"))
		return
	}

	cfg := config.AppConfig
	panelURL := fmt.Sprintf("https://127.0.0.1:%d/%s", cfg.Panel.TLSPort, cfg.Panel.RandomSuffix)
	cfgJSON, _ := json.Marshal(map[string]string{
		"panel_url": panelURL,
		"api_key":   apiKey,
	})
	baseSecretsDir := "/var/wp-panel/site-secrets"
	secretsDir := filepath.Join(baseSecretsDir, domain)
	if err := os.MkdirAll(baseSecretsDir, 0711); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建密钥目录失败"))
		return
	}
	if err := os.Chmod(baseSecretsDir, 0711); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("设置密钥目录权限失败"))
		return
	}
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("创建站点密钥目录失败"))
		return
	}

	// 清理旧路径下的配置文件（迁移到 Web 目录外之前的位置）
	os.Remove(filepath.Join(pluginDir, "wp-panel-config.json"))

	if err := os.WriteFile(filepath.Join(secretsDir, "wp-panel-config.json"), cfgJSON, 0600); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("写入插件密钥失败"))
		return
	}

	executor.InstallPluginPermissions(domain, systemUser, pluginDir)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message":   "插件已安装",
		"path":      "wp-content/plugins/wp-panel-optimizer/",
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

	dst := filepath.Join(webRoot, "wp-content", "plugins", "wp-panel-optimizer", "wp-panel-optimizer.php")
	dstInfo, dstErr := os.Stat(dst)
	srcInfo, srcErr := os.Stat("/www/server/panel/packages/wp-panel-optimizer.php")

	status := "not_installed"
	if dstErr == nil {
		status = "installed"
		if srcErr == nil && dstInfo.ModTime().Before(srcInfo.ModTime()) {
			status = "update_available"
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"status":      status,
		"plugin_path": "wp-content/plugins/wp-panel-optimizer/",
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

	executor.GoSafe(func() { executor.RegenerateSiteNginx(id) })

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "缓存设置已更新"}))
}

func (h *WebsiteHandler) SaveWPOptimizations(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}

	var req struct {
		FCacheEnabled      bool `json:"fcache_enabled"`
		FCacheTTL          int  `json:"fcache_ttl"`
		DisableWPUpdates   bool `json:"disable_wp_updates"`
		DisableFileEditing bool   `json:"disable_file_editing"`
		XMLRPCEnabled      bool   `json:"xmlrpc_enabled"`
		WPDebugEnabled     bool   `json:"wp_debug_enabled"`
		WPPostRevisions    int    `json:"wp_post_revisions"`
		WPMemoryLimit      string `json:"wp_memory_limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.FCacheTTL < 10 {
		req.FCacheTTL = 300
	}
	if req.FCacheTTL > 86400 {
		req.FCacheTTL = 86400
	}

	db := database.GetDB()

	// 检查 FastCGI / XML-RPC 配置是否变化，决定是否重载 Nginx
	var oldFCacheEnabled, oldFCacheTTL, oldXMLRPCEnabled int
	db.QueryRow("SELECT fastcgi_cache_enabled, fastcgi_cache_ttl, xmlrpc_enabled FROM websites WHERE id = ?", id).
		Scan(&oldFCacheEnabled, &oldFCacheTTL, &oldXMLRPCEnabled)

	fcEnabled := 0
	if req.FCacheEnabled {
		fcEnabled = 1
	}
	disableUpdates := 0
	if req.DisableWPUpdates {
		disableUpdates = 1
	}
	disableEditing := 0
	if req.DisableFileEditing {
		disableEditing = 1
	}
	xmlrpcEnabled := 0
	if req.XMLRPCEnabled {
		xmlrpcEnabled = 1
	}

	wpDebug := 0
	if req.WPDebugEnabled {
		wpDebug = 1
	}

	_, err = db.Exec(`UPDATE websites SET
		fastcgi_cache_enabled = ?, fastcgi_cache_ttl = ?,
		disable_wp_updates = ?, disable_file_editing = ?, xmlrpc_enabled = ?,
		wp_debug_enabled = ?, wp_post_revisions = ?, wp_memory_limit = ?
		WHERE id = ?`,
		fcEnabled, req.FCacheTTL, disableUpdates, disableEditing, xmlrpcEnabled,
		wpDebug, req.WPPostRevisions, req.WPMemoryLimit, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败"))
		return
	}

	// 更新 wp-config.php
	var webRoot string
	db.QueryRow("SELECT web_root FROM websites WHERE id = ?", id).Scan(&webRoot)
	if webRoot != "" {
		opts := executor.WPOptimizations{
			DisableUpdates:     req.DisableWPUpdates,
			DisableFileEditing: req.DisableFileEditing,
			WPDebug:            req.WPDebugEnabled,
			WPPostRevisions:    req.WPPostRevisions,
			WPMemoryLimit:      req.WPMemoryLimit,
		}
		if err := executor.ApplyWPOptimizations(webRoot, opts); err != nil {
			log.Printf("ApplyWPOptimizations 失败 (site %d): %v", id, err)
		}
	}

	// FastCGI / XML-RPC 配置变化时重载 Nginx
	if oldFCacheEnabled != fcEnabled || oldFCacheTTL != req.FCacheTTL || oldXMLRPCEnabled != xmlrpcEnabled {
		executor.GoSafe(func() { executor.RegenerateSiteNginx(id) })
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
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

	executor.GoSafe(func() { executor.ClearSiteCache(id) })

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

		var req struct {
			CleanDefaults      bool     `json:"clean_defaults"`
			RemoveUnusedThemes bool     `json:"remove_unused_themes"`
			InstallThemes      []string `json:"install_themes"`
			InstallPlugins     []string `json:"install_plugins"`
		}
		c.ShouldBindJSON(&req)

		if err := executor.ReinstallWordPress(cfg.Paths.WordPressPackage, webRoot, dbName, dbUser, systemUser, cfg,
			req.CleanDefaults, req.RemoveUnusedThemes, req.InstallThemes, req.InstallPlugins); err != nil {
		log.Printf("WordPress 重装失败 site=%d: %v", id, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("WordPress 重装失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": "WordPress 已重装完成，数据库和文件均已恢复为全新状态",
	}))
}

// ============================================================
// CacheHelperHandler — WordPress 插件 API
// ============================================================

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

type CacheHelperHandler struct{}

func (h *CacheHelperHandler) checkAPIKey(domain string, c *gin.Context) bool {
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return false
	}
	if host != "127.0.0.1" && host != "::1" {
		return false
	}
	key := c.GetHeader("X-WP-Panel-Key")
	if key == "" {
		return false
	}
	var storedKey string
	err = database.GetDB().QueryRow(
		"SELECT plugin_api_key FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'",
		domain, escapeLike(domain),
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
	_, err := db.Exec("UPDATE websites SET fastcgi_cache_ttl = ? WHERE (domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\')", req.TTL, req.Domain, escapeLike(req.Domain))
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新失败"))
		return
	}

	var siteID int
	db.QueryRow("SELECT id FROM websites WHERE (domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\')", req.Domain, escapeLike(req.Domain)).Scan(&siteID)
	if siteID > 0 {
		executor.GoSafe(func() { executor.RegenerateSiteNginx(siteID) })
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
		"SELECT id FROM websites WHERE (domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\')",
		req.Domain, escapeLike(req.Domain),
	).Scan(&siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	executor.GoSafe(func() { executor.ClearSiteCache(siteID) })
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

	var siteID, fcacheEnabled, fcacheTTL, disableUpdates, disableEditing, xmlrpcEnabled, wpDebugEnabled, wpPostRevisions int
	var wpMemoryLimit string
	err := database.GetDB().QueryRow(
		"SELECT id, fastcgi_cache_enabled, fastcgi_cache_ttl, disable_wp_updates, disable_file_editing, xmlrpc_enabled, wp_debug_enabled, wp_post_revisions, wp_memory_limit FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'",
		domain, escapeLike(domain),
	).Scan(&siteID, &fcacheEnabled, &fcacheTTL, &disableUpdates, &disableEditing, &xmlrpcEnabled, &wpDebugEnabled, &wpPostRevisions, &wpMemoryLimit)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"site_id":               siteID,
		"domain":                domain,
		"fastcgi_cache_enabled": fcacheEnabled == 1,
		"fastcgi_cache_ttl":     fcacheTTL,
		"disable_wp_updates":    disableUpdates == 1,
		"disable_file_editing":  disableEditing == 1,
		"xmlrpc_enabled":        xmlrpcEnabled == 1,
		"wp_debug_enabled":     wpDebugEnabled == 1,
		"wp_post_revisions":    wpPostRevisions,
		"wp_memory_limit":      wpMemoryLimit,
	}))
}

func (h *CacheHelperHandler) UpdateOptimizerSettings(c *gin.Context) {
	var req struct {
		Domain             string `json:"domain"`
		Enabled            bool   `json:"enabled"`
		TTL                int    `json:"ttl"`
		DisableWPUpdates   bool   `json:"disable_wp_updates"`
		DisableFileEditing bool   `json:"disable_file_editing"`
		WPDebugEnabled     bool   `json:"wp_debug_enabled"`
		WPPostRevisions    int    `json:"wp_post_revisions"`
		WPMemoryLimit      string `json:"wp_memory_limit"`
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

	var oldFCacheEnabled, oldFCacheTTL int
	db.QueryRow("SELECT fastcgi_cache_enabled, fastcgi_cache_ttl FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'", req.Domain, escapeLike(req.Domain)).
		Scan(&oldFCacheEnabled, &oldFCacheTTL)

	fcEnabled := 0
	if req.Enabled {
		fcEnabled = 1
	}
	disableUpdates := 0
	disableEditing := 0
	if req.DisableWPUpdates {
		disableUpdates = 1
	}
	if req.DisableFileEditing {
		disableEditing = 1
	}

	wpDebug2 := 0
	if req.WPDebugEnabled {
		wpDebug2 = 1
	}

	_, err := db.Exec(`UPDATE websites SET
		fastcgi_cache_enabled = ?, fastcgi_cache_ttl = ?,
		disable_wp_updates = ?, disable_file_editing = ?,
		wp_debug_enabled = ?, wp_post_revisions = ?, wp_memory_limit = ?
		WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\'`,
		fcEnabled, req.TTL, disableUpdates, disableEditing, wpDebug2, req.WPPostRevisions, req.WPMemoryLimit, req.Domain, escapeLike(req.Domain))
	if err != nil {
		log.Printf("UpdateOptimizerSettings DB 更新失败 (site %s): %v", req.Domain, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败: " + err.Error()))
		return
	}

	// 更新 wp-config.php
	var webRoot string
	db.QueryRow("SELECT web_root FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'", req.Domain, escapeLike(req.Domain)).Scan(&webRoot)
	if webRoot != "" {
		opts := executor.WPOptimizations{
			DisableUpdates:     req.DisableWPUpdates,
			DisableFileEditing: req.DisableFileEditing,
			WPDebug:            req.WPDebugEnabled,
			WPPostRevisions:    req.WPPostRevisions,
			WPMemoryLimit:      req.WPMemoryLimit,
		}
		if err := executor.ApplyWPOptimizations(webRoot, opts); err != nil {
			log.Printf("ApplyWPOptimizations 失败 (site %s): %v", req.Domain, err)
		}
	}

	// FastCGI 配置变化时重载 Nginx
	if oldFCacheEnabled != fcEnabled || oldFCacheTTL != req.TTL {
		var siteID int
		db.QueryRow("SELECT id FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'", req.Domain, escapeLike(req.Domain)).Scan(&siteID)
		if siteID > 0 {
			executor.GoSafe(func() { executor.RegenerateSiteNginx(siteID) })
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func (h *WebsiteHandler) SetLogRetention(c *gin.Context) {
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
		RetentionDays int `json:"retention_days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.RetentionDays < 0 {
		req.RetentionDays = 0
	}

	db := database.GetDB()
	db.Exec("UPDATE websites SET log_retention_days = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", req.RetentionDays, id)

	writeLogrotateConfig(site.Domain, site.LogDir, req.RetentionDays)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func (h *WebsiteHandler) UpdateExpiry(c *gin.Context) {
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
		ExpiresAt string `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	db := database.GetDB()
	req.ExpiresAt = strings.TrimSpace(req.ExpiresAt)
	var dbErr error
	if req.ExpiresAt == "" {
		_, dbErr = db.Exec("UPDATE websites SET expires_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?", id)
	} else {
		if _, err := time.Parse("2006-01-02", req.ExpiresAt); err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("日期格式不正确，请使用 YYYY-MM-DD"))
			return
		}
		_, dbErr = db.Exec("UPDATE websites SET expires_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", req.ExpiresAt, id)
	}
	if dbErr != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func writeLogrotateConfig(domain, logDir string, retentionDays int) {
	confPath := "/etc/logrotate.d/wppanel-" + domain

	if retentionDays <= 0 {
		os.Remove(confPath)
		return
	}

	content := fmt.Sprintf(`# WP Panel Generated - %s
%s/access.log
%s/error.log {
    daily
    rotate %d
    missingok
    notifempty
    compress
    delaycompress
    copytruncate
}
`, domain, logDir, logDir, retentionDays)

	os.WriteFile(confPath, []byte(content), 0644)
}
