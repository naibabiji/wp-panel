package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

// canonical column list shared by all website queries.
const websiteCols = `id, name, domain, aliases, status, system_user, web_root, document_root_subdir, log_dir,
	db_name, db_user, php_pool_path, nginx_conf_path, site_type, ssl_enabled,
	ssl_cert_path, ssl_key_path, ssl_expires_at, ssl_last_error, ssl_export_enabled, template_version, access_log_mode,
	fastcgi_cache_enabled, fastcgi_cache_ttl, fastcgi_cache_key,
	monitoring_enabled, monitoring_interval, disable_wp_updates, disable_file_editing,
		xmlrpc_enabled, disable_application_passwords, wp_debug_enabled, wp_post_revisions, wp_memory_limit,
		file_lock_enabled, file_lock_mode, file_lock_apply_status,
		password_reset_mode,
		log_retention_days, cdn_realip_enabled, php_fpm_max_children, expires_at, created_at, updated_at`

const fileLockBlockedMessage = "该站点已开启文件锁定，请先解除文件锁定后再执行此维护操作"

var wpOptimizationSiteLocks sync.Map // siteID(int) -> *sync.Mutex

func wpOptimizationSiteLock(id int) *sync.Mutex {
	value, _ := wpOptimizationSiteLocks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

type wpOptimizationConfig struct {
	disableUpdates     bool
	disableFileEditing bool
	debugEnabled       bool
	debugDisplay       *bool
	postRevisions      int
	memoryLimit        string
}

func applyWPOptimizationConfig(site *models.Website, cfg wpOptimizationConfig) (bool, func() error, error) {
	if site.SiteType != "wordpress" {
		return false, nil, nil
	}
	debugDisplay := executor.WPDebugDisplayEnabled(site.WebRoot)
	if cfg.debugDisplay != nil {
		debugDisplay = *cfg.debugDisplay
	}
	rollback, err := executor.ApplyWPOptimizationsReversible(site.WebRoot, executor.WPOptimizations{
		DisableUpdates:     cfg.disableUpdates,
		DisableFileEditing: cfg.disableFileEditing,
		WPDebug:            cfg.debugEnabled,
		WPDebugDisplay:     debugDisplay,
		WPPostRevisions:    cfg.postRevisions,
		WPMemoryLimit:      cfg.memoryLimit,
	})
	return debugDisplay, rollback, err
}

type siteLogFileInfo struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	Current    bool      `json:"current"`
	Compressed bool      `json:"compressed"`
}

// scanWebsite scans the canonical columns into a Website model.
// scanner is either row.Scan (for QueryRow) or rows.Scan (for Rows).
func scanWebsite(scanner func(dest ...interface{}) error) (*models.Website, error) {
	var w models.Website
	var aliases, status string
	var sslEnabled, sslExportEnabled, fCacheEnabled, monitoringEnabled int
	var monitoringInterval int
	var disableWPUpdates, disableFileEditing, xmlrpcEnabled, disableApplicationPasswords int
	var wpDebugEnabled int
	var wpPostRevisions int
	var wpMemoryLimit string
	var fileLockEnabled int
	var passwordResetMode string
	var logRetentionDays int
	var cdnRealIPEnabled int

	err := scanner(
		&w.ID, &w.Name, &w.Domain, &aliases, &status, &w.SystemUser,
		&w.WebRoot, &w.DocumentRootSubdir, &w.LogDir, &w.DBName, &w.DBUser, &w.PHPPoolPath,
		&w.NginxConfPath, &w.SiteType, &sslEnabled, &w.SSLCertPath, &w.SSLKeyPath,
		&w.SSLExpiresAt, &w.SSLLastError, &sslExportEnabled, &w.TemplateVersion, &w.AccessLogMode,
		&fCacheEnabled, &w.FCacheTTL, &w.FCacheKey,
		&monitoringEnabled, &monitoringInterval, &disableWPUpdates, &disableFileEditing,
		&xmlrpcEnabled, &disableApplicationPasswords, &wpDebugEnabled, &wpPostRevisions, &wpMemoryLimit,
		&fileLockEnabled, &w.FileLockMode, &w.FileLockApplyStatus,
		&passwordResetMode,
		&logRetentionDays, &cdnRealIPEnabled, &w.PHPFPMMaxChildren, &w.ExpiresAt,
		&w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	w.Aliases = aliases
	w.Status = models.WebsiteStatus(status)
	w.SSLEnabled = sslEnabled == 1
	w.SSLExportEnabled = sslExportEnabled == 1
	w.FCacheEnabled = fCacheEnabled == 1
	w.MonitoringEnabled = monitoringEnabled == 1
	w.MonitoringInterval = monitoringInterval
	w.DisableWPUpdates = disableWPUpdates == 1
	w.DisableFileEditing = disableFileEditing == 1
	w.XMLRPCEnabled = xmlrpcEnabled == 1
	w.DisableApplicationPasswords = disableApplicationPasswords == 1
	w.WPDebugEnabled = wpDebugEnabled == 1
	w.WPPostRevisions = wpPostRevisions
	w.WPMemoryLimit = wpMemoryLimit
	w.FileLockEnabled = fileLockEnabled == 1
	w.PasswordResetMode = passwordResetMode
	w.LogRetentionDays = logRetentionDays
	w.CDNRealIPEnabled = cdnRealIPEnabled == 1
	return &w, nil
}

type WebsiteHandler struct {
	DB *sql.DB
}

type sslPreflightDomain struct {
	Domain      string   `json:"domain"`
	Addresses   []string `json:"addresses"`
	Matched     bool     `json:"matched"`
	HasIPv6     bool     `json:"has_ipv6"`
	MatchedIPv6 bool     `json:"matched_ipv6"`
}

type sslPreflightResult struct {
	OK           bool                 `json:"ok"`
	Warnings     []string             `json:"warnings"`
	HardWarnings []string             `json:"hard_warnings"`
	Domains      []sslPreflightDomain `json:"domains"`
}

func normalizeWPSiteURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("URL must start with http:// or https://")
	}
	return value, nil
}

func replaceWPSiteURLDomain(raw, oldDomain, newDomain string) (string, error) {
	value, err := normalizeWPSiteURL(raw)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("empty URL")
	}
	parsed, _ := url.Parse(value)
	if !strings.EqualFold(parsed.Hostname(), oldDomain) {
		return "", fmt.Errorf("URL domain does not match current site domain")
	}
	port := parsed.Port()
	parsed.Host = newDomain
	if port != "" {
		parsed.Host += ":" + port
	}
	return parsed.String(), nil
}

func localInterfaceIPs() map[string]bool {
	result := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			result[ip.String()] = true
		}
	}
	return result
}

func uniqueRequestDomains(domain string, aliases []string) []string {
	seen := map[string]bool{}
	var domains []string
	for _, raw := range append([]string{domain}, aliases...) {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		domains = append(domains, d)
	}
	return domains
}

func runSSLPreflight(ctx context.Context, domain string, aliases []string) (sslPreflightResult, error) {
	domains := uniqueRequestDomains(domain, aliases)
	if len(domains) == 0 {
		return sslPreflightResult{}, fmt.Errorf("域名不能为空")
	}
	for _, domain := range domains {
		if !executor.IsValidDomain(domain) {
			return sslPreflightResult{}, fmt.Errorf("域名格式不合法: %s", domain)
		}
	}

	localIPs := localInterfaceIPs()
	result := sslPreflightResult{}
	for _, domain := range domains {
		records, err := net.DefaultResolver.LookupIPAddr(ctx, domain)
		if err != nil || len(records) == 0 {
			msg := domain + " 未解析到 A/AAAA 记录，Let's Encrypt 无法访问验证文件。"
			if ctx.Err() != nil {
				msg = domain + " DNS 解析超时，请稍后重试或检查 DNS 服务。"
			}
			result.HardWarnings = append(result.HardWarnings, msg)
			result.Domains = append(result.Domains, sslPreflightDomain{Domain: domain})
			continue
		}

		item := sslPreflightDomain{Domain: domain}
		for _, record := range records {
			ip := record.IP
			ipText := ip.String()
			item.Addresses = append(item.Addresses, ipText)
			if ip.To4() == nil {
				item.HasIPv6 = true
			}
			if localIPs[ipText] {
				item.Matched = true
				if ip.To4() == nil {
					item.MatchedIPv6 = true
				}
			}
		}
		if !item.Matched {
			result.Warnings = append(result.Warnings, domain+" 没有解析到当前服务器网卡 IP。如果使用 CDN，请确认 CDN 已正确回源到当前服务器，并且未缓存、重写或拦截 /.well-known/acme-challenge/。")
		}
		if item.HasIPv6 && !item.MatchedIPv6 {
			result.Warnings = append(result.Warnings, domain+" 存在 AAAA 记录，但未匹配到当前服务器 IPv6。Let's Encrypt 可能访问 IPv6 并导致验证 404，请删除错误 AAAA 记录或配置正确 IPv6。")
		}
		result.Domains = append(result.Domains, item)
	}
	result.OK = len(result.Warnings) == 0 && len(result.HardWarnings) == 0
	return result, nil
}

func (h *WebsiteHandler) SSLPreflight(c *gin.Context) {
	var req models.CreateWebsiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := runSSLPreflight(ctx, req.Domain, req.Aliases)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(result))
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
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取网站列表失败"))
		return
	}
	if err := rows.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取网站列表失败"))
		return
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
		AIDevelopment    bool   `json:"ai_development_enabled"`
	}
	aiDevelopmentSites := make(map[int]bool)
	aiRows, err := db.Query("SELECT site_id FROM website_ai_development_access")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询 AI 开发授权状态失败"))
		return
	}
	for aiRows.Next() {
		var siteID int
		if err := aiRows.Scan(&siteID); err != nil {
			aiRows.Close()
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取 AI 开发授权状态失败"))
			return
		}
		aiDevelopmentSites[siteID] = true
	}
	if err := aiRows.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取 AI 开发授权状态失败"))
		return
	}
	result := make([]siteRow, len(websites))
	for i, w := range websites {
		result[i] = siteRow{
			Website:          w,
			AccessLogMode:    w.AccessLogMode,
			FCacheEnabled:    w.FCacheEnabled,
			AccessLogEnabled: w.AccessLogMode != "off",
			AIDevelopment:    aiDevelopmentSites[w.ID],
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
		w.WPDebugDisplay = executor.WPDebugDisplayEnabled(w.WebRoot)
	}
	executor.LoadWebsiteCDNRealIPGroups(w)

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
	documentRootSubdir, err := executor.NormalizeDocumentRootSubdir(siteType, req.DocumentRootSubdir)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
		return
	}
	if req.SSLEnabled {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		preflight, preflightErr := runSSLPreflight(ctx, req.Domain, req.Aliases)
		cancel()
		if preflightErr != nil {
			log.Printf("SSL 预检跳过 domain=%s: %v", req.Domain, preflightErr)
		} else if !preflight.OK {
			log.Printf("SSL 预检风险 domain=%s hard=%v warnings=%v", req.Domain, preflight.HardWarnings, preflight.Warnings)
		}
	}

	payload := &executor.CreateSitePayload{
		Domain:             req.Domain,
		Aliases:            req.Aliases,
		SSLEnabled:         req.SSLEnabled,
		DBPassword:         req.DBPassword,
		ExpiresAt:          req.ExpiresAt,
		SiteType:           siteType,
		DocumentRootSubdir: documentRootSubdir,
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

func (h *WebsiteHandler) SetDocumentRoot(c *gin.Context) {
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
	if rejectIfAIDevelopmentAccessActive(c, id) {
		return
	}
	if site.SiteType != "php" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("只有通用 PHP 网站支持修改 Web 入口目录"))
		return
	}

	var req struct {
		DocumentRootSubdir string `json:"document_root_subdir"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	documentRootSubdir, err := executor.NormalizeDocumentRootSubdir(site.SiteType, req.DocumentRootSubdir)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(err.Error()))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskSetDocumentRoot, &executor.SetDocumentRootPayload{
		Site: site, DocumentRootSubdir: documentRootSubdir,
	})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
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
	if rejectIfAIDevelopmentAccessActive(c, id) {
		return
	}
	if locked, lockErr := executor.SiteMigrationDeleteBlocked(c.Request.Context(), site.ID, site.Domain); lockErr != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return
	} else if locked {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "site_migration.delete_locked")))
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

// BackupUsage 返回网站当前的备份数据规模（数据库备份数、文件备份数、是否启用自动备份、
// 关联计划任务），供前端在删除网站前提醒管理员：面板不会自动删除备份文件，
// 但会在删除网站时自动删除仍关联该网站的计划任务。
func (h *WebsiteHandler) BackupUsage(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}
	if getWebsiteByID(id) == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	db := database.GetDB()
	usage := models.WebsiteBackupUsage{CronJobs: []models.BackupCronJobRef{}}

	if err := db.QueryRow(`SELECT COUNT(*) FROM db_backups WHERE site_id = ?`, id).Scan(&usage.DBBackupCount); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询数据库备份数量失败"))
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_backups WHERE site_id = ?`, id).Scan(&usage.FileBackupCount); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询文件备份数量失败"))
		return
	}
	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM backup_settings WHERE site_id = ?`, id).Scan(&enabled); err != nil && err != sql.ErrNoRows {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询自动备份设置失败"))
		return
	}
	usage.AutoBackupEnabled = enabled == 1

	rows, err := db.Query(`SELECT id, name FROM cron_jobs WHERE site_id = ?`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询关联计划任务失败"))
		return
	}
	for rows.Next() {
		var ref models.BackupCronJobRef
		if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
			log.Printf("备份使用量检查: 扫描计划任务行失败 site=%d: %v", id, err)
			continue
		}
		usage.CronJobs = append(usage.CronJobs, ref)
	}
	rows.Close()

	c.JSON(http.StatusOK, models.SuccessResponse(usage))
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
	if locked, lockErr := executor.SiteMigrationLocked(c.Request.Context(), site.ID, site.Domain); lockErr != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return
	} else if locked {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "site_migration.site_locked")))
		return
	}

	var taskType executor.TaskType
	switch req.Action {
	case "pause":
		if site.Status != models.StatusActive {
			c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "website.status_action_unavailable")))
			return
		}
		taskType = executor.TaskPauseSite
	case "enable":
		if site.Status == models.StatusMigrated {
			c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "website.status_action_unavailable")))
			return
		}
		taskType = executor.TaskEnableSite
	case "restore_migrated":
		if site.Status != models.StatusMigrated {
			c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "website.status_action_unavailable")))
			return
		}
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

func (h *WebsiteHandler) DownloadSSLPackage(c *gin.Context) {
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

	zipData, filename, err := buildSSLCertificatePackage(site)
	if err != nil {
		c.JSON(sslDownloadStatus(err), models.ErrorResponse(err.Error()))
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Data(http.StatusOK, "application/zip", zipData)
}

func (h *WebsiteHandler) SetSSLExport(c *gin.Context) {
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
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	enabled := 0
	if req.Enabled {
		enabled = 1
	}
	if _, err := database.GetDB().Exec(
		`UPDATE websites SET ssl_export_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		enabled, site.ID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存 SSL 证书导出权限失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "SSL 证书导出权限已保存"}))
}

type sslDownloadError struct {
	status  int
	message string
}

func (e sslDownloadError) Error() string {
	return e.message
}

func sslDownloadStatus(err error) int {
	if e, ok := err.(sslDownloadError); ok {
		return e.status
	}
	return http.StatusInternalServerError
}

func newSSLDownloadError(status int, message string) error {
	return sslDownloadError{status: status, message: message}
}

func buildSSLCertificatePackage(site *models.Website) ([]byte, string, error) {
	if site == nil {
		return nil, "", newSSLDownloadError(http.StatusNotFound, "网站不存在")
	}
	if config.AppConfig == nil || strings.TrimSpace(config.AppConfig.Paths.Certificates) == "" {
		return nil, "", fmt.Errorf("证书目录未配置")
	}
	if !executor.IsValidDomain(site.Domain) {
		return nil, "", newSSLDownloadError(http.StatusBadRequest, "网站域名格式不合法")
	}

	certDir := filepath.Join(config.AppConfig.Paths.Certificates, site.Domain)
	certPath := filepath.Join(certDir, "fullchain.pem")
	keyPath := filepath.Join(certDir, "privkey.pem")
	if !sslPathWithin(config.AppConfig.Paths.Certificates, certPath) ||
		!sslPathWithin(config.AppConfig.Paths.Certificates, keyPath) {
		return nil, "", newSSLDownloadError(http.StatusForbidden, "证书路径无效")
	}

	certData, err := readSSLDownloadFile(certPath)
	if err != nil {
		return nil, "", err
	}
	keyData, err := readSSLDownloadFile(keyPath)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := addZipFile(zw, "fullchain.pem", certData); err != nil {
		zw.Close()
		return nil, "", err
	}
	if err := addZipFile(zw, "privkey.pem", keyData); err != nil {
		zw.Close()
		return nil, "", err
	}
	if err := addZipFile(zw, "README.txt", []byte(sslCertificatePackageReadme(site))); err != nil {
		zw.Close()
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}

	filename := fmt.Sprintf("%s-ssl-cert-%s.zip", site.Domain, time.Now().Format("20060102"))
	return buf.Bytes(), filename, nil
}

func readSSLDownloadFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return nil, newSSLDownloadError(http.StatusNotFound, "证书文件不存在，请重新申请证书")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, newSSLDownloadError(http.StatusNotFound, "证书文件不存在，请重新申请证书")
	}
	return data, nil
}

func sslPathWithin(basePath, targetPath string) bool {
	baseAbs, err := filepath.Abs(filepath.Clean(basePath))
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func addZipFile(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0600)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func sslCertificatePackageReadme(site *models.Website) string {
	aliases := strings.TrimSpace(site.Aliases)
	if aliases == "" {
		aliases = "无"
	}
	expiresAt := "未知"
	if site.SSLExpiresAt != nil {
		expiresAt = site.SSLExpiresAt.Format("2006-01-02")
	}

	return fmt.Sprintf(`WP Panel SSL 证书包

站点域名：%s
附加域名：
%s
证书到期：%s

文件说明：
- fullchain.pem：完整证书链。上传到 CDN 后台的“证书”或“公钥”字段。
- privkey.pem：私钥。上传到 CDN 后台的“私钥”字段。

阿里云 CDN 自定义上传时，通常选择“自定义上传（证书+私钥）”：
- 证书（公钥）：填写 fullchain.pem 内容。
- 私钥：填写 privkey.pem 内容。

注意事项：
- 私钥是敏感信息，请勿发送给无关人员，也不要上传到不可信位置。
- CDN 侧不会自动同步源站证书。面板续期后，需要重新下载证书包并上传到 CDN。
- 如果同一站点有多个 CDN 加速域名，例如主域名和 www 域名，需要在 CDN 后台分别更新。
`, site.Domain, aliases, expiresAt)
}

func sslCertificateExportPayload(site *models.Website) (gin.H, error) {
	if site == nil {
		return nil, newSSLDownloadError(http.StatusNotFound, "网站不存在")
	}
	if !site.SSLEnabled {
		return nil, newSSLDownloadError(http.StatusBadRequest, "该网站未启用SSL")
	}
	if config.AppConfig == nil || strings.TrimSpace(config.AppConfig.Paths.Certificates) == "" {
		return nil, fmt.Errorf("证书目录未配置")
	}
	if !executor.IsValidDomain(site.Domain) {
		return nil, newSSLDownloadError(http.StatusBadRequest, "网站域名格式不合法")
	}

	certDir := filepath.Join(config.AppConfig.Paths.Certificates, site.Domain)
	certPath := filepath.Join(certDir, "fullchain.pem")
	keyPath := filepath.Join(certDir, "privkey.pem")
	if !sslPathWithin(config.AppConfig.Paths.Certificates, certPath) ||
		!sslPathWithin(config.AppConfig.Paths.Certificates, keyPath) {
		return nil, newSSLDownloadError(http.StatusForbidden, "证书路径无效")
	}
	certData, err := readSSLDownloadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyData, err := readSSLDownloadFile(keyPath)
	if err != nil {
		return nil, err
	}

	aliases := []string{}
	for _, raw := range strings.Split(site.Aliases, "\n") {
		alias := strings.TrimSpace(raw)
		if alias != "" {
			aliases = append(aliases, alias)
		}
	}

	return gin.H{
		"domain":      site.Domain,
		"aliases":     aliases,
		"expires_at":  site.SSLExpiresAt,
		"certificate": string(certData),
		"private_key": string(keyData),
	}, nil
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
	if rejectIfAIDevelopmentAccessActive(c, id) {
		return
	}

	var req struct {
		NewDomain      string   `json:"new_domain"`
		Aliases        []string `json:"aliases"`
		SyncWPSiteURLs bool     `json:"sync_wp_site_urls"`
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

	payload := &executor.UpdateDomainsPayload{Site: site, NewDomain: targetDomain, Aliases: req.Aliases}
	if req.SyncWPSiteURLs {
		if site.SiteType != "wordpress" {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.sync_wp_urls_wordpress_only")))
			return
		}
		if targetDomain == site.Domain {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.sync_wp_urls_domain_unchanged")))
			return
		}
		if site.TablePrefix == "" {
			if prefix, readErr := executor.ReadWPTablePrefix(site.WebRoot); readErr == nil {
				site.TablePrefix = prefix
			}
		}
		if site.TablePrefix == "" {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.sync_wp_urls_prefix_required")))
			return
		}
		oldSiteURL, oldHomeURL, readErr := executor.ReadWPSiteURLs(site.DBName, site.TablePrefix, config.AppConfig)
		if readErr != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.sync_wp_urls_read_failed", i18n.P{"error": readErr.Error()})))
			return
		}
		newSiteURL, replaceErr := replaceWPSiteURLDomain(oldSiteURL, site.Domain, targetDomain)
		if replaceErr != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.sync_wp_siteurl_mismatch")))
			return
		}
		newHomeURL, replaceErr := replaceWPSiteURLDomain(oldHomeURL, site.Domain, targetDomain)
		if replaceErr != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.sync_wp_home_mismatch")))
			return
		}
		payload.OldWPSiteURL = oldSiteURL
		payload.OldWPHomeURL = oldHomeURL
		payload.NewWPSiteURL = newSiteURL
		payload.NewWPHomeURL = newHomeURL
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskUpdateDomains, payload)
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
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
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
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
	}

	var req struct {
		TablePrefix string `json:"table_prefix"`
	}
	c.ShouldBindJSON(&req)
	req.TablePrefix = strings.TrimSpace(req.TablePrefix)
	if req.TablePrefix != "" && !executor.IsValidWPTablePrefix(req.TablePrefix) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("表前缀只能包含字母、数字和下划线，且长度不能超过 56 个字符"))
		return
	}

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
	prefix, candidates, err := executor.DetectDBTablePrefix(site.DBName, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("检测失败: "+err.Error()))
		return
	}

	// 如果 wp-config.php 中的前缀存在于候选列表中，优先推荐
	if site.WebRoot != "" {
		if configPrefix, err := executor.ReadWPTablePrefix(site.WebRoot); err == nil {
			for _, c := range candidates {
				if c == configPrefix {
					prefix = configPrefix
					break
				}
			}
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"prefix":     prefix,
		"candidates": candidates,
	}))
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
		SiteURL         string `json:"siteurl"`
		HomeURL         string `json:"home"`
		SyncPanelDomain bool   `json:"sync_panel_domain"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	req.SiteURL, err = normalizeWPSiteURL(req.SiteURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("siteurl 格式不正确，请使用 http:// 或 https:// 开头的完整 URL"))
		return
	}
	req.HomeURL, err = normalizeWPSiteURL(req.HomeURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("home 格式不正确，请使用 http:// 或 https:// 开头的完整 URL"))
		return
	}
	if req.SiteURL == "" && req.HomeURL == "" {
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
	if req.SyncPanelDomain {
		if rejectIfAIDevelopmentAccessActive(c, id) {
			return
		}
		oldSiteURL, oldHomeURL, readErr := executor.ReadWPSiteURLs(site.DBName, site.TablePrefix, cfg)
		if readErr != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取当前站点 URL 失败: "+readErr.Error()))
			return
		}
		newSiteURL := req.SiteURL
		if newSiteURL == "" {
			newSiteURL = oldSiteURL
		}
		newHomeURL := req.HomeURL
		if newHomeURL == "" {
			newHomeURL = oldHomeURL
		}
		targetDomain, domainErr := panelDomainFromWPSiteURLs(newSiteURL, newHomeURL)
		if domainErr != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(domainErr.Error()))
			return
		}
		if targetDomain == strings.ToLower(site.Domain) {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("WordPress URL 域名与网站主域名相同，无需同步"))
			return
		}
		if conflict, existing := isAliasConflicting(targetDomain, site.ID); conflict {
			c.JSON(http.StatusBadRequest, models.ErrorResponse("域名 "+targetDomain+" 已被站点 "+existing+" 使用"))
			return
		}
		aliases := make([]string, 0)
		for _, alias := range strings.Split(site.Aliases, "\n") {
			alias = strings.TrimSpace(alias)
			if alias != "" && !strings.EqualFold(alias, targetDomain) {
				aliases = append(aliases, alias)
			}
		}
		payload := &executor.UpdateDomainsPayload{
			Site: site, NewDomain: targetDomain, Aliases: aliases,
			OldWPSiteURL: oldSiteURL, OldWPHomeURL: oldHomeURL,
			NewWPSiteURL: newSiteURL, NewWPHomeURL: newHomeURL,
		}
		task := executor.GlobalQueue.Enqueue(executor.TaskUpdateDomains, payload)
		result := <-task.ResultCh
		if result.Success {
			c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": result.Message}))
		} else {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
		}
		return
	}
	if err := executor.UpdateWPSiteURLs(site.DBName, site.TablePrefix, req.SiteURL, req.HomeURL, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("更新失败: "+err.Error()))
		return
	}

	// 异步清理 FastCGI 和 Redis Object Cache，避免旧缓存继续返回旧站点 URL。
	executor.GoSafe(func() { executor.ClearWPSiteRuntimeCaches(site.ID, site.Domain, site.WebRoot) })

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "站点 URL 已更新"}))
}

func panelDomainFromWPSiteURLs(siteURL, homeURL string) (string, error) {
	parseHost := func(raw string) (string, error) {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Hostname() == "" {
			return "", fmt.Errorf("WordPress URL 缺少有效域名")
		}
		host := strings.ToLower(parsed.Hostname())
		if !executor.IsValidDomain(host) {
			return "", fmt.Errorf("WordPress URL 中的域名不能作为网站主域名")
		}
		return host, nil
	}
	siteHost, err := parseHost(siteURL)
	if err != nil {
		return "", err
	}
	homeHost, err := parseHost(homeURL)
	if err != nil {
		return "", err
	}
	if siteHost != homeHost {
		return "", fmt.Errorf("siteurl 与 home 的域名不同，无法自动判断网站主域名")
	}
	return siteHost, nil
}

func prepareWPAdministratorSite(c *gin.Context) *models.Website {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_invalid_site")))
		return nil
	}
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_site_not_found")))
		return nil
	}
	if site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_wordpress_only")))
		return nil
	}
	if site.TablePrefix == "" {
		if prefix, err := executor.ReadWPTablePrefix(site.WebRoot); err == nil {
			site.TablePrefix = prefix
		}
	}
	if !executor.IsValidWPTablePrefix(site.TablePrefix) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_prefix_required")))
		return nil
	}
	return site
}

func (h *WebsiteHandler) ListWPAdministrators(c *gin.Context) {
	site := prepareWPAdministratorSite(c)
	if site == nil {
		return
	}
	result, err := executor.ListWPAdministrators(c.Request.Context(), site)
	if err != nil {
		log.Printf("读取 WordPress 管理员失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_load_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(result))
}

func wpAdministratorErrorKey(err error) string {
	switch executor.WPAdminManagerErrorCode(err) {
	case "administrator_not_found":
		return "website.wp_admin_not_found"
	case "invalid_login":
		return "website.wp_admin_invalid_login"
	case "login_exists":
		return "website.wp_admin_login_exists"
	case "invalid_email":
		return "website.wp_admin_invalid_email"
	case "email_exists":
		return "website.wp_admin_email_exists"
	case "invalid_display_name":
		return "website.wp_admin_invalid_display_name"
	case "invalid_password":
		return "website.wp_admin_invalid_password"
	case "multisite_unsupported":
		return "website.wp_admin_multisite_unsupported"
	case "database_mismatch":
		return "website.wp_admin_database_mismatch"
	case "non_transactional_engine":
		return "website.wp_admin_non_transactional_engine"
	case "transaction_failed":
		return "website.wp_admin_transaction_failed"
	case "verification_failed", "password_verification_failed":
		return "website.wp_admin_verification_failed"
	case "commit_failed", "login_update_failed", "user_update_failed":
		return "website.wp_admin_write_failed"
	default:
		return "website.wp_admin_update_failed"
	}
}

func (h *WebsiteHandler) UpdateWPAdministrator(c *gin.Context) {
	site := prepareWPAdministratorSite(c)
	if site == nil {
		return
	}
	var req executor.WPAdministratorUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_invalid_params")))
		return
	}
	req.Login = strings.TrimSpace(req.Login)
	req.Email = strings.TrimSpace(req.Email)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.UserID <= 0 || req.Login == "" || len(req.Login) > 60 || req.Email == "" || req.DisplayName == "" || len(req.Password) > 4096 || (req.Password != "" && len(req.Password) < 8) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_invalid_params")))
		return
	}
	if !executor.TryAcquireSiteOpLock(site.ID, "wp_administrator_update") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_site_busy")))
		return
	}
	releaseLock := true
	defer func() {
		if releaseLock {
			executor.ReleaseSiteOpLock(site.ID)
		}
	}()

	if err := executor.PreflightWPAdministratorUpdate(c.Request.Context(), site, req); err != nil {
		log.Printf("WordPress 管理员修改预检失败 site=%d user=%d: %v", site.ID, req.UserID, err)
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, wpAdministratorErrorKey(err))))
		return
	}

	backupTask := executor.GlobalQueue.Enqueue(executor.TaskCreateBackup, &executor.CreateBackupPayload{Site: site, Auto: false})
	var backupResult executor.TaskResult
	select {
	case backupResult = <-backupTask.ResultCh:
	case <-time.After(15 * time.Minute):
		releaseLock = false
		executor.GoSafe(func() {
			<-backupTask.ResultCh
			executor.ReleaseSiteOpLock(site.ID)
		})
		c.JSON(http.StatusGatewayTimeout, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_backup_timeout")))
		return
	case <-c.Request.Context().Done():
		releaseLock = false
		executor.GoSafe(func() {
			<-backupTask.ResultCh
			executor.ReleaseSiteOpLock(site.ID)
		})
		return
	}
	if !backupResult.Success {
		log.Printf("修改 WordPress 管理员前备份失败 site=%d: %s", site.ID, backupResult.Message)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.wp_admin_backup_failed")))
		return
	}

	result, err := executor.UpdateWPAdministrator(c.Request.Context(), site, req)
	if err != nil {
		log.Printf("修改 WordPress 管理员失败 site=%d user=%d: %v", site.ID, req.UserID, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, wpAdministratorErrorKey(err))))
		return
	}
	executor.GoSafe(func() { executor.ClearWPSiteRuntimeCaches(site.ID, site.Domain, site.WebRoot) })
	log.Printf("WordPress 管理员信息已修改 site=%d user=%d login=%q", site.ID, req.UserID, result.Administrator.Login)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"administrator":    result.Administrator,
		"site_admin_email": result.SiteAdminEmail,
		"backup_message":   backupResult.Message,
	}))
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
	if logType != "error" && logType != "access" && logType != "security" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error、access 或 security"))
		return
	}
	if logType == "security" && site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持安全日志"))
		return
	}

	linesStr := c.DefaultQuery("lines", "200")
	lines, _ := strconv.Atoi(linesStr)
	if lines <= 0 || lines > 5000 {
		lines = 200
	}

	baseName, ok := siteLogBaseName(logType)
	if !ok {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error、access 或 security"))
		return
	}
	cleanPath, err := resolveSiteLogFile(site.LogDir, logType, baseName)
	if err != nil {
		c.JSON(http.StatusForbidden, models.ErrorResponse("禁止访问该路径"))
		return
	}
	if info, err := os.Lstat(cleanPath); err == nil && !info.Mode().IsRegular() {
		c.JSON(http.StatusForbidden, models.ErrorResponse("禁止访问该日志文件"))
		return
	}

	content := tailFile(cleanPath, lines)
	if content == "" {
		if logType == "access" {
			content = "（暂无异常访问日志；默认仅记录 4xx/5xx 请求，正常访问不会写入 access.log）"
		} else if logType == "security" {
			content = "（暂无 WordPress 安全日志，暂未发现异常路径访问）"
		} else {
			content = "（暂无错误日志，网站运行正常）"
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"log_type": logType, "content": content}))
}

func (h *WebsiteHandler) ListLogFiles(c *gin.Context) {
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
	if logType != "error" && logType != "access" && logType != "security" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error、access 或 security"))
		return
	}
	if logType == "security" && site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持安全日志"))
		return
	}

	files, err := listSiteLogFiles(site.LogDir, logType)
	if err != nil {
		c.JSON(http.StatusForbidden, models.ErrorResponse("读取日志文件列表失败"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"log_type": logType, "files": files}))
}

func (h *WebsiteHandler) DownloadLogFile(c *gin.Context) {
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
	if logType != "error" && logType != "access" && logType != "security" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error、access 或 security"))
		return
	}
	if logType == "security" && site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持安全日志"))
		return
	}

	filename := strings.TrimSpace(c.Query("file"))
	cleanPath, err := resolveSiteLogFile(site.LogDir, logType, filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志文件名无效"))
		return
	}

	info, err := os.Lstat(cleanPath)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("日志文件不存在"))
		return
	}
	if !info.Mode().IsRegular() {
		c.JSON(http.StatusForbidden, models.ErrorResponse("禁止下载该日志文件"))
		return
	}

	c.Header("Cache-Control", "no-store")
	c.FileAttachment(cleanPath, filename)
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
	if logType != "error" && logType != "access" && logType != "security" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error、access 或 security"))
		return
	}
	if logType == "security" && site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持安全日志"))
		return
	}

	baseName, ok := siteLogBaseName(logType)
	if !ok {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("日志类型无效，仅支持 error、access 或 security"))
		return
	}
	cleanPath, err := resolveSiteLogFile(site.LogDir, logType, baseName)
	if err != nil {
		c.JSON(http.StatusForbidden, models.ErrorResponse("禁止访问该路径"))
		return
	}
	if info, err := os.Lstat(cleanPath); err == nil && !info.Mode().IsRegular() {
		c.JSON(http.StatusForbidden, models.ErrorResponse("禁止清空该日志文件"))
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

func siteLogBaseName(logType string) (string, bool) {
	switch logType {
	case "access":
		return "access.log", true
	case "error":
		return "error.log", true
	case "security":
		return "wp-security.log", true
	default:
		return "", false
	}
}

func isAllowedSiteLogFilename(logType, name string) bool {
	baseName, ok := siteLogBaseName(logType)
	if !ok || name == "" || name != filepath.Base(name) {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return false
	}
	if name == baseName {
		return true
	}

	if strings.HasPrefix(name, baseName+".") {
		suffix := strings.TrimPrefix(name, baseName+".")
		suffix = strings.TrimSuffix(suffix, ".gz")
		return suffix != "" && allDigits(suffix)
	}

	if strings.HasPrefix(name, baseName+"-") {
		suffix := strings.TrimPrefix(name, baseName+"-")
		suffix = strings.TrimSuffix(suffix, ".gz")
		_, err := time.Parse("2006-01-02", suffix)
		return err == nil
	}

	return false
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func resolveSiteLogFile(logDir, logType, filename string) (string, error) {
	if !isAllowedSiteLogFilename(logType, filename) {
		return "", fmt.Errorf("invalid log filename")
	}
	cleanLogDir := filepath.Clean(logDir)
	if cleanLogDir == "." || !filepath.IsAbs(cleanLogDir) {
		return "", fmt.Errorf("invalid log dir")
	}
	cleanPath := filepath.Clean(filepath.Join(cleanLogDir, filename))
	if filepath.Dir(cleanPath) != cleanLogDir {
		return "", fmt.Errorf("log path outside site log dir")
	}
	return cleanPath, nil
}

func listSiteLogFiles(logDir, logType string) ([]siteLogFileInfo, error) {
	baseName, ok := siteLogBaseName(logType)
	if !ok {
		return nil, fmt.Errorf("invalid log type")
	}
	cleanLogDir := filepath.Clean(logDir)
	if cleanLogDir == "." || !filepath.IsAbs(cleanLogDir) {
		return nil, fmt.Errorf("invalid log dir")
	}

	entries, err := os.ReadDir(cleanLogDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []siteLogFileInfo{}, nil
		}
		return nil, err
	}

	files := make([]siteLogFileInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !isAllowedSiteLogFilename(logType, name) {
			continue
		}
		cleanPath, err := resolveSiteLogFile(cleanLogDir, logType, name)
		if err != nil {
			continue
		}
		info, err := os.Lstat(cleanPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, siteLogFileInfo{
			Name:       name,
			Size:       info.Size(),
			ModifiedAt: info.ModTime(),
			Current:    name == baseName,
			Compressed: strings.HasSuffix(name, ".gz"),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Current != files[j].Current {
			return files[i].Current
		}
		if !files[i].ModifiedAt.Equal(files[j].ModifiedAt) {
			return files[i].ModifiedAt.After(files[j].ModifiedAt)
		}
		return files[i].Name < files[j].Name
	})

	return files, nil
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

func (h *WebsiteHandler) SetCDNRealIP(c *gin.Context) {
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
		Enabled  bool  `json:"enabled"`
		GroupIDs []int `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.Enabled && len(req.GroupIDs) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("启用 CDN 真实 IP 时至少选择一个配置组"))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskSetCDNRealIP, &executor.SetCDNRealIPPayload{
		Site: site, Enabled: req.Enabled, GroupIDs: req.GroupIDs,
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
	executor.LoadWebsiteCDNRealIPGroups(w)
	return w
}

func (h *WebsiteHandler) InstallPlugin(c *gin.Context) {
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
	if !executor.TryAcquireSiteOpLock(id, "plugin_deploy") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}
	defer executor.ReleaseSiteOpLock(id)
	// Reload under the operation lock; a previous permission task may have
	// completed after the first lookup.
	site = getWebsiteByID(id)
	if site == nil {
		c.Status(http.StatusNotFound)
		return
	}
	domain, webRoot, systemUser := site.Domain, site.WebRoot, site.SystemUser

	pluginDir := filepath.Join(webRoot, "wp-content", "plugins", "wp-panel-optimizer")
	if site.FileLockEnabled {
		// The exception upgrades an existing companion; installation and
		// credential repair keep their existing explicit-unlock prerequisite.
		var key string
		err := database.GetDB().QueryRow(`SELECT plugin_api_key FROM websites WHERE id=?`, id).Scan(&key)
		_, statErr := os.Stat(filepath.Join(pluginDir, "wp-panel-optimizer.php"))
		if err != nil || statErr != nil || !executor.SitePluginIdentityAvailable(domain, key) {
			c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
			return
		}
	}
	if err := executor.DeploySiteCompanionPluginOwned(id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("部署插件文件失败: "+err.Error()))
		return
	}
	if site.FileLockEnabled {
		// Trusted upgrade only: preserve the existing identity and never run the
		// normal installer chown against the read-only published directory.
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.TE(c.Request, "website.installed"), "path": "wp-content/plugins/wp-panel-optimizer/"}))
		return
	}

	apiKey := executor.NewAPIKey()
	if _, err := database.GetDB().Exec("UPDATE websites SET plugin_api_key = ? WHERE id = ?", apiKey, id); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存插件密钥失败"))
		return
	}

	cfg := config.AppConfig
	panelURL := fmt.Sprintf("https://127.0.0.1:%d/%s", cfg.Panel.TLSPort, cfg.Panel.RandomSuffix)
	// 清理旧路径下的配置文件（迁移到 Web 目录外之前的位置）
	os.Remove(filepath.Join(pluginDir, "wp-panel-config.json"))

	if err := executor.WriteSitePluginIdentity(domain, systemUser, panelURL, apiKey, site.DisableApplicationPasswords); err != nil {
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

	site := getWebsiteByID(id)
	if site == nil || site.SiteType != "wordpress" {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	status := "unknown"
	if executor.TryAcquireSiteOpLock(id, "companion_status") {
		defer executor.ReleaseSiteOpLock(id)
		site = getWebsiteByID(id)
		if site != nil {
			var migration bool
			err = database.GetDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM site_migration_locks WHERE (site_id=? OR domain=?) AND status='active')`, id, site.Domain).Scan(&migration)
			if err == nil && !migration {
				status = executor.CompanionPluginStatus(c.Request.Context(), config.AppConfig, site)
				if status == "installed" {
					var pluginAPIKey string
					if err = database.GetDB().QueryRow("SELECT plugin_api_key FROM websites WHERE id=?", id).Scan(&pluginAPIKey); err != nil {
						status = "unknown"
					} else if installed, needsUpdate := executor.PluginNeedsUpdate(site.WebRoot); !installed {
						status = "unknown"
					} else if needsUpdate {
						status = "update_available"
					} else if !executor.SitePluginIdentityAvailable(site.Domain, pluginAPIKey) {
						status = "config_missing"
					}
				}
			}
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

	executor.GoSafe(func() {
		if err := executor.RegenerateSiteNginx(id); err != nil {
			log.Printf("刷新站点 Nginx 配置失败 site=%d: %v", id, err)
		}
	})

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "缓存设置已更新"}))
}

func (h *WebsiteHandler) SaveWPOptimizations(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效的网站ID"))
		return
	}
	if !executor.TryAcquireSiteOpLock(id, "wp_settings") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}
	defer executor.ReleaseSiteOpLock(id)
	lock := wpOptimizationSiteLock(id)
	lock.Lock()
	defer lock.Unlock()
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
	}

	var req struct {
		FCacheEnabled               bool   `json:"fcache_enabled"`
		FCacheTTL                   int    `json:"fcache_ttl"`
		DisableWPUpdates            bool   `json:"disable_wp_updates"`
		ExpectedWPUpdates           *bool  `json:"expected_disable_wp_updates"`
		DisableFileEditing          bool   `json:"disable_file_editing"`
		XMLRPCEnabled               bool   `json:"xmlrpc_enabled"`
		DisableApplicationPasswords bool   `json:"disable_application_passwords"`
		WPDebugEnabled              bool   `json:"wp_debug_enabled"`
		WPDebugDisplay              *bool  `json:"wp_debug_display"`
		WPPostRevisions             int    `json:"wp_post_revisions"`
		WPMemoryLimit               string `json:"wp_memory_limit"`
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
	var domain string
	var oldFCacheEnabled, oldFCacheTTL, oldXMLRPCEnabled, oldDisableWPUpdates, oldDisableApplicationPasswords int
	if err := db.QueryRow("SELECT domain, fastcgi_cache_enabled, fastcgi_cache_ttl, xmlrpc_enabled, disable_wp_updates, disable_application_passwords FROM websites WHERE id = ?", id).
		Scan(&domain, &oldFCacheEnabled, &oldFCacheTTL, &oldXMLRPCEnabled, &oldDisableWPUpdates, &oldDisableApplicationPasswords); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败"))
		return
	}
	if req.ExpectedWPUpdates != nil && *req.ExpectedWPUpdates != (oldDisableWPUpdates == 1) {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "website.optimization_conflict")))
		return
	}

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
	disableApplicationPasswords := 0
	if req.DisableApplicationPasswords {
		disableApplicationPasswords = 1
	}

	wpDebug := 0
	if req.WPDebugEnabled {
		wpDebug = 1
	}

	wpDebugDisplay, rollbackWPConfig, err := applyWPOptimizationConfig(site, wpOptimizationConfig{
		disableUpdates:     req.DisableWPUpdates,
		disableFileEditing: req.DisableFileEditing,
		debugEnabled:       req.WPDebugEnabled,
		debugDisplay:       req.WPDebugDisplay,
		postRevisions:      req.WPPostRevisions,
		memoryLimit:        req.WPMemoryLimit,
	})
	if err != nil {
		recordHandlerOperationLog("wp_optimizations", domain, "failed", "写入 wp-config.php 失败: "+err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败：无法更新 wp-config.php"))
		return
	}

	updateQuery := `UPDATE websites SET
		fastcgi_cache_enabled = ?, fastcgi_cache_ttl = ?,
		disable_wp_updates = ?, disable_file_editing = ?, xmlrpc_enabled = ?, disable_application_passwords = ?,
		wp_debug_enabled = ?, wp_post_revisions = ?, wp_memory_limit = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`
	updateArgs := []any{fcEnabled, req.FCacheTTL, disableUpdates, disableEditing, xmlrpcEnabled, disableApplicationPasswords,
		wpDebug, req.WPPostRevisions, req.WPMemoryLimit, id}
	if req.ExpectedWPUpdates != nil {
		updateQuery += " AND disable_wp_updates = ?"
		expected := 0
		if *req.ExpectedWPUpdates {
			expected = 1
		}
		updateArgs = append(updateArgs, expected)
	}
	applicationPasswordPolicyChanged := oldDisableApplicationPasswords != disableApplicationPasswords
	if applicationPasswordPolicyChanged {
		if err := executor.UpdateSitePluginApplicationPasswordPolicy(domain, site.SystemUser, req.DisableApplicationPasswords); err != nil {
			respondWPOptimizationFailure(c, rollbackWPConfig, domain, http.StatusInternalServerError, "保存失败：无法同步配套插件策略", err)
			return
		}
	}
	result, err := db.Exec(updateQuery, updateArgs...)
	if err != nil {
		rollbackApplicationPasswordPolicy(domain, site.SystemUser, oldDisableApplicationPasswords, applicationPasswordPolicyChanged)
		respondWPOptimizationFailure(c, rollbackWPConfig, domain, http.StatusInternalServerError, "保存失败", err)
		return
	}
	if req.ExpectedWPUpdates != nil {
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			rollbackApplicationPasswordPolicy(domain, site.SystemUser, oldDisableApplicationPasswords, applicationPasswordPolicyChanged)
			respondWPOptimizationFailure(c, rollbackWPConfig, domain, http.StatusInternalServerError, "保存失败", rowsErr)
			return
		}
		if affected == 0 {
			rollbackApplicationPasswordPolicy(domain, site.SystemUser, oldDisableApplicationPasswords, applicationPasswordPolicyChanged)
			respondWPOptimizationFailure(c, rollbackWPConfig, domain, http.StatusConflict, i18n.TE(c.Request, "website.optimization_conflict"), fmt.Errorf("concurrent update conflict"))
			return
		}
	}

	// FastCGI / XML-RPC 配置变化时重载 Nginx
	if oldFCacheEnabled != fcEnabled || oldFCacheTTL != req.FCacheTTL || oldXMLRPCEnabled != xmlrpcEnabled {
		executor.GoSafe(func() {
			if err := executor.RegenerateSiteNginx(id); err != nil {
				log.Printf("刷新站点 Nginx 配置失败 site=%d: %v", id, err)
			}
		})
	}
	if domain != "" {
		recordHandlerOperationLog("wp_optimizations", domain, "success", wpOptimizationsLogMessage(req.FCacheEnabled, req.FCacheTTL, req.DisableWPUpdates, req.DisableFileEditing, req.XMLRPCEnabled, req.DisableApplicationPasswords, req.WPDebugEnabled, wpDebugDisplay, req.WPPostRevisions, req.WPMemoryLimit))
	}
	if site.FileLockEnabled && site.FileLockApplyStatus == executor.FileLockApplyStatusReady {
		executor.RefreshWPCodeIntegrityBaselineBestEffort(id, "WordPress 优化设置保存成功")
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func rollbackApplicationPasswordPolicy(domain, systemUser string, previousValue int, changed bool) {
	if !changed {
		return
	}
	if err := executor.UpdateSitePluginApplicationPasswordPolicy(domain, systemUser, previousValue == 1); err != nil {
		log.Printf("回滚 WordPress 应用程序密码策略失败 domain=%s: %v", domain, err)
	}
}

func (h *WebsiteHandler) SetWPUpdateChecks(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.invalid_site_id")))
		return
	}
	if !executor.TryAcquireSiteOpLock(id, "wp_update_settings") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}
	defer executor.ReleaseSiteOpLock(id)
	lock := wpOptimizationSiteLock(id)
	lock.Lock()
	defer lock.Unlock()
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}
	if site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.update_checks_wordpress_only")))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	tx, err := database.GetDB().BeginTx(c.Request.Context(), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.update_checks_save_failed")))
		return
	}
	defer tx.Rollback()
	disabled := 1
	if req.Enabled {
		disabled = 0
	}
	if _, err := tx.ExecContext(c.Request.Context(), "UPDATE websites SET disable_wp_updates = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", disabled, id); err != nil {
		_ = tx.Rollback()
		recordHandlerOperationLog("wp_update_checks", site.Domain, "failed", err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.update_checks_save_failed")))
		return
	}
	if err := executor.SetWPUpdatesDisabled(site.WebRoot, !req.Enabled); err != nil {
		_ = tx.Rollback()
		recordHandlerOperationLog("wp_update_checks", site.Domain, "failed", err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.update_checks_save_failed")))
		return
	}
	if err := tx.Commit(); err != nil {
		if rollbackErr := executor.SetWPUpdatesDisabled(site.WebRoot, site.DisableWPUpdates); rollbackErr != nil {
			log.Printf("恢复 WordPress 更新检测设置失败 site=%d: %v", id, rollbackErr)
		}
		recordHandlerOperationLog("wp_update_checks", site.Domain, "failed", err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.update_checks_save_failed")))
		return
	}
	recordHandlerOperationLog("wp_update_checks", site.Domain, "success", fmt.Sprintf("WordPress 更新检测=%t", req.Enabled))
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"enabled": req.Enabled}))
}

func (h *WebsiteHandler) SetFileEditingProtection(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.invalid_site_id")))
		return
	}
	if !executor.TryAcquireSiteOpLock(id, "file_edit_settings") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}
	defer executor.ReleaseSiteOpLock(id)
	lock := wpOptimizationSiteLock(id)
	lock.Lock()
	defer lock.Unlock()
	site := getWebsiteByID(id)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}
	if site.SiteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.file_editing_wordpress_only")))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	if err := executor.SetWPFileEditingDisabled(site.WebRoot, req.Enabled); err != nil {
		recordHandlerOperationLog("wp_file_editor", site.Domain, "failed", err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.file_editing_save_failed")))
		return
	}

	enabled := 0
	if req.Enabled {
		enabled = 1
	}
	if _, err := database.GetDB().Exec("UPDATE websites SET disable_file_editing = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", enabled, id); err != nil {
		if rollbackErr := executor.SetWPFileEditingDisabled(site.WebRoot, site.DisableFileEditing); rollbackErr != nil {
			log.Printf("回滚后台代码编辑器设置失败 site=%d: %v", id, rollbackErr)
		}
		recordHandlerOperationLog("wp_file_editor", site.Domain, "failed", err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.file_editing_save_failed")))
		return
	}
	recordHandlerOperationLog("wp_file_editor", site.Domain, "success", fmt.Sprintf("后台代码编辑器关闭=%t", req.Enabled))
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message":              i18n.TE(c.Request, "website.file_editing_saved"),
		"disable_file_editing": req.Enabled,
	}))
}

func (h *WebsiteHandler) SetFileLock(c *gin.Context) {
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("只有 WordPress 站点支持文件锁定"))
		return
	}

	var req struct {
		Enabled bool   `json:"enabled"`
		Mode    string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.Enabled && rejectIfAIDevelopmentAccessActive(c, id) {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if req.Enabled {
		if mode != executor.FileLockModeStandard && mode != executor.FileLockModeStrict {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.file_lock_invalid_mode")))
			return
		}
	}
	if site.FileLockEnabled == req.Enabled && (!req.Enabled || (site.FileLockMode == mode && site.FileLockApplyStatus == executor.FileLockApplyStatusReady)) {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"message":                "文件锁定状态未变化",
			"file_lock_enabled":      req.Enabled,
			"file_lock_mode":         site.FileLockMode,
			"file_lock_apply_status": site.FileLockApplyStatus,
		}))
		return
	}

	task := executor.GlobalQueue.Enqueue(executor.TaskSetFileLock, &executor.SetFileLockPayload{
		Site:    site,
		Enabled: req.Enabled,
		Mode:    mode,
	})
	result := <-task.ResultCh
	if result.Success {
		c.JSON(http.StatusOK, models.SuccessResponse(result.Data))
	} else {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(result.Message))
	}
}

func (h *WebsiteHandler) PreviewFileLock(c *gin.Context) {
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("只有 WordPress 站点支持文件锁定"))
		return
	}
	mode := strings.ToLower(strings.TrimSpace(c.Query("mode")))
	if mode != executor.FileLockModeStandard && mode != executor.FileLockModeStrict {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "website.file_lock_invalid_mode")))
		return
	}
	preview, err := executor.PreviewSiteFileLock(site, mode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "website.file_lock_preview_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(preview))
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
	if !executor.TryAcquireSiteOpLock(id, "reinstall") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}
	defer executor.ReleaseSiteOpLock(id)

	var domain, webRoot, systemUser, dbName, dbUser, siteType string
	var fileLockEnabled int
	err = database.GetDB().QueryRow(
		"SELECT domain, web_root, system_user, db_name, db_user, site_type, file_lock_enabled FROM websites WHERE id = ?", id,
	).Scan(&domain, &webRoot, &systemUser, &dbName, &dbUser, &siteType, &fileLockEnabled)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}
	if rejectIfAIDevelopmentAccessActive(c, id) {
		return
	}

	if siteType != "wordpress" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("仅 WordPress 站点支持重装功能"))
		return
	}
	if fileLockEnabled == 1 {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
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

	if err := executor.ReinstallWordPress(c.Request.Context(), webRoot, dbName, dbUser, systemUser, cfg,
		req.CleanDefaults, req.RemoveUnusedThemes, req.InstallThemes, req.InstallPlugins); err != nil {
		log.Printf("WordPress 重装失败 site=%d: %v", id, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(reinstallWordPressErrorMessage(err)))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": "WordPress 已重装完成，数据库和文件均已恢复为全新状态",
	}))
}

func reinstallWordPressErrorMessage(err error) string {
	const prefix = "WordPress 重装失败"
	if err == nil {
		return prefix
	}
	stage := strings.TrimSpace(err.Error())
	if idx := strings.IndexAny(stage, ":："); idx >= 0 {
		stage = strings.TrimSpace(stage[:idx])
	}
	switch stage {
	case "该网站已启用文件锁，请先关闭文件锁",
		"网站目录路径为空",
		"网站目录路径校验失败",
		"网站目录路径不在允许目录内",
		"创建临时网站目录失败",
		"WordPress 部署失败",
		"删除旧数据库失败",
		"重建数据库失败",
		"生成 wp-config.php 失败",
		"清理旧网站目录失败",
		"替换网站目录失败":
		return prefix + "：" + stage
	default:
		return prefix
	}
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

func pluginRequestHostAllowed(c *gin.Context) bool {
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "::1"
}

func (h *CacheHelperHandler) checkAPIKey(domain string, c *gin.Context) bool {
	if !pluginRequestHostAllowed(c) {
		return false
	}
	key := c.GetHeader("X-WP-Panel-Key")
	if key == "" {
		return false
	}
	var storedKey string
	err := database.GetDB().QueryRow(
		"SELECT plugin_api_key FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'",
		domain, escapeLike(domain),
	).Scan(&storedKey)
	if err != nil {
		return false
	}
	return storedKey != "" && key == storedKey
}

func (h *CacheHelperHandler) pluginSiteByDomain(domain string, c *gin.Context) (*models.Website, bool) {
	if !pluginRequestHostAllowed(c) {
		return nil, false
	}
	key := c.GetHeader("X-WP-Panel-Key")
	if key == "" {
		return nil, false
	}

	var siteID int
	var storedKey string
	err := database.GetDB().QueryRow(
		"SELECT id, plugin_api_key FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'",
		domain, escapeLike(domain),
	).Scan(&siteID, &storedKey)
	if err != nil || storedKey == "" || key != storedKey {
		return nil, false
	}

	site := getWebsiteByID(siteID)
	return site, site != nil
}

func recordHandlerOperationLog(operation, target, status, message string) {
	if database.GetDB() == nil {
		return
	}
	_, _ = database.GetDB().Exec(
		"INSERT INTO operation_logs (operation, target, status, message) VALUES (?, ?, ?, ?)",
		operation, target, status, message,
	)
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
		executor.GoSafe(func() {
			if err := executor.RegenerateSiteNginx(siteID); err != nil {
				log.Printf("刷新站点 Nginx 配置失败 site=%d: %v", siteID, err)
			}
		})
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

	var siteID, fcacheEnabled, fcacheTTL, disableUpdates, disableEditing, xmlrpcEnabled, disableApplicationPasswords, wpDebugEnabled, wpPostRevisions, fileLockEnabled int
	var wpMemoryLimit string
	err := database.GetDB().QueryRow(
		"SELECT id, fastcgi_cache_enabled, fastcgi_cache_ttl, disable_wp_updates, disable_file_editing, xmlrpc_enabled, disable_application_passwords, wp_debug_enabled, wp_post_revisions, wp_memory_limit, file_lock_enabled FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'",
		domain, escapeLike(domain),
	).Scan(&siteID, &fcacheEnabled, &fcacheTTL, &disableUpdates, &disableEditing, &xmlrpcEnabled, &disableApplicationPasswords, &wpDebugEnabled, &wpPostRevisions, &wpMemoryLimit, &fileLockEnabled)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"site_id":                       siteID,
		"domain":                        domain,
		"fastcgi_cache_enabled":         fcacheEnabled == 1,
		"fastcgi_cache_ttl":             fcacheTTL,
		"disable_wp_updates":            disableUpdates == 1,
		"disable_file_editing":          disableEditing == 1,
		"xmlrpc_enabled":                xmlrpcEnabled == 1,
		"disable_application_passwords": disableApplicationPasswords == 1,
		"wp_debug_enabled":              wpDebugEnabled == 1,
		"wp_post_revisions":             wpPostRevisions,
		"wp_memory_limit":               wpMemoryLimit,
		"file_lock_enabled":             fileLockEnabled == 1,
	}))
}

func (h *CacheHelperHandler) ExportSSLCertificate(c *gin.Context) {
	domain := strings.ToLower(strings.TrimSpace(c.Query("domain")))
	if domain == "" || !executor.IsValidDomain(domain) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	site, ok := h.pluginSiteByDomain(domain, c)
	if !ok {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}
	if !site.SSLExportEnabled {
		recordHandlerOperationLog("ssl_certificate_export", site.Domain, "failed", "插件证书导出权限未开启")
		c.JSON(http.StatusForbidden, models.ErrorResponse("SSL 证书导出权限未开启"))
		return
	}

	payload, err := sslCertificateExportPayload(site)
	if err != nil {
		recordHandlerOperationLog("ssl_certificate_export", site.Domain, "failed", err.Error())
		c.JSON(sslDownloadStatus(err), models.ErrorResponse(err.Error()))
		return
	}

	recordHandlerOperationLog("ssl_certificate_export", site.Domain, "success", "插件已读取 SSL 证书")
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, models.SuccessResponse(payload))
}

func (h *CacheHelperHandler) UpdateOptimizerSettings(c *gin.Context) {
	var req struct {
		Domain             string `json:"domain"`
		Enabled            bool   `json:"enabled"`
		TTL                int    `json:"ttl"`
		DisableWPUpdates   bool   `json:"disable_wp_updates"`
		DisableFileEditing bool   `json:"disable_file_editing"`
		WPDebugEnabled     bool   `json:"wp_debug_enabled"`
		WPDebugDisplay     *bool  `json:"wp_debug_display"`
		WPPostRevisions    int    `json:"wp_post_revisions"`
		WPMemoryLimit      string `json:"wp_memory_limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Domain == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	site, ok := h.pluginSiteByDomain(req.Domain, c)
	if !ok {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("API Key 无效"))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
	}
	if req.TTL < 10 {
		req.TTL = 300
	}
	if req.TTL > 86400 {
		req.TTL = 86400
	}
	if !executor.TryAcquireSiteOpLock(site.ID, "wp_settings") {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "maintenance.operation_unavailable")))
		return
	}
	defer executor.ReleaseSiteOpLock(site.ID)
	lock := wpOptimizationSiteLock(site.ID)
	lock.Lock()
	defer lock.Unlock()
	site = getWebsiteByID(site.ID)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse("网站不存在"))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(fileLockBlockedMessage))
		return
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

	wpDebugDisplay, rollbackWPConfig, err := applyWPOptimizationConfig(site, wpOptimizationConfig{
		disableUpdates:     req.DisableWPUpdates,
		disableFileEditing: req.DisableFileEditing,
		debugEnabled:       req.WPDebugEnabled,
		debugDisplay:       req.WPDebugDisplay,
		postRevisions:      req.WPPostRevisions,
		memoryLimit:        req.WPMemoryLimit,
	})
	if err != nil {
		recordHandlerOperationLog("wp_optimizations", req.Domain, "failed", "写入 wp-config.php 失败: "+err.Error())
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败：无法更新 wp-config.php"))
		return
	}

	_, err = db.Exec(`UPDATE websites SET
		fastcgi_cache_enabled = ?, fastcgi_cache_ttl = ?,
		disable_wp_updates = ?, disable_file_editing = ?,
		wp_debug_enabled = ?, wp_post_revisions = ?, wp_memory_limit = ?
		WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\'`,
		fcEnabled, req.TTL, disableUpdates, disableEditing, wpDebug2, req.WPPostRevisions, req.WPMemoryLimit, req.Domain, escapeLike(req.Domain))
	if err != nil {
		log.Printf("UpdateOptimizerSettings DB 更新失败 (site %s): %v", req.Domain, err)
		respondWPOptimizationFailure(c, rollbackWPConfig, req.Domain, http.StatusInternalServerError, "保存失败: "+err.Error(), err)
		return
	}

	// FastCGI 配置变化时重载 Nginx
	if oldFCacheEnabled != fcEnabled || oldFCacheTTL != req.TTL {
		var siteID int
		db.QueryRow("SELECT id FROM websites WHERE domain = ? OR (char(10) || aliases || char(10)) LIKE ('%' || char(10) || ? || char(10) || '%') ESCAPE '\\'", req.Domain, escapeLike(req.Domain)).Scan(&siteID)
		if siteID > 0 {
			executor.GoSafe(func() {
				if err := executor.RegenerateSiteNginx(siteID); err != nil {
					log.Printf("刷新站点 Nginx 配置失败 site=%d: %v", siteID, err)
				}
			})
		}
	}
	recordHandlerOperationLog("wp_optimizations", req.Domain, "success", wpOptimizationsLogMessage(req.Enabled, req.TTL, req.DisableWPUpdates, req.DisableFileEditing, false, false, req.WPDebugEnabled, wpDebugDisplay, req.WPPostRevisions, req.WPMemoryLimit))
	if site.FileLockEnabled && site.FileLockApplyStatus == executor.FileLockApplyStatusReady {
		executor.RefreshWPCodeIntegrityBaselineBestEffort(site.ID, "WordPress 优化设置保存成功")
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func wpOptimizationsLogMessage(fcacheEnabled bool, fcacheTTL int, disableUpdates, disableEditing, xmlrpcEnabled, disableApplicationPasswords, wpDebugEnabled, wpDebugDisplay bool, postRevisions int, memoryLimit string) string {
	state := func(enabled bool) string {
		if enabled {
			return "开启"
		}
		return "关闭"
	}
	parts := []string{
		"FastCGI缓存=" + state(fcacheEnabled),
		fmt.Sprintf("缓存TTL=%d", fcacheTTL),
		"禁止更新=" + state(disableUpdates),
		"禁止文件编辑=" + state(disableEditing),
		"XML-RPC=" + state(xmlrpcEnabled),
		"禁用应用程序密码=" + state(disableApplicationPasswords),
		"WP_DEBUG=" + state(wpDebugEnabled),
		"浏览器错误显示=" + state(wpDebugEnabled && wpDebugDisplay),
		fmt.Sprintf("文章修订=%d", postRevisions),
	}
	if strings.TrimSpace(memoryLimit) != "" {
		parts = append(parts, "PHP内存限制="+strings.TrimSpace(memoryLimit))
	}
	return strings.Join(parts, "；")
}

func respondWPOptimizationFailure(c *gin.Context, rollback func() error, target string, status int, message string, cause error) {
	detail := cause.Error()
	if rollback != nil {
		if err := rollback(); err != nil {
			detail += "; wp-config.php rollback failed: " + err.Error()
			message += "；wp-config.php 恢复失败，请立即检查网站配置"
			status = http.StatusInternalServerError
		}
	}
	recordHandlerOperationLog("wp_optimizations", target, "failed", detail)
	c.JSON(status, models.ErrorResponse(message))
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

	if err := executor.WriteSiteLogrotateConfig(site.Domain, site.LogDir, req.RetentionDays); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("日志轮转配置应用失败"))
		return
	}

	db := database.GetDB()
	if _, err := db.Exec("UPDATE websites SET log_retention_days = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", req.RetentionDays, id); err != nil {
		_ = executor.WriteSiteLogrotateConfig(site.Domain, site.LogDir, site.LogRetentionDays)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("保存失败"))
		return
	}

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
