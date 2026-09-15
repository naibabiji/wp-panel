package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

type rollbackStep struct {
	desc string
	fn   func() error
}

func runRollbackSteps(steps []rollbackStep) []string {
	var failures []string
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if err := step.fn(); err != nil {
			log.Printf("回滚失败 [%s]: %v", step.desc, err)
			failures = append(failures, step.desc+": "+err.Error())
		}
	}
	return failures
}

func domainUpdateFailure(message string, err error, rollbacks []rollbackStep) TaskResult {
	failures := runRollbackSteps(rollbacks)
	if len(failures) > 0 {
		return TaskResult{Success: false, Message: message + "，且状态恢复不完整，请检查：" + strings.Join(failures, "；")}
	}
	if err != nil {
		log.Printf("%s: %v", message, err)
	}
	return TaskResult{Success: false, Message: message}
}

func requireDomainTargetAvailable(path, label string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s已存在: %s", label, path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查%s失败: %w", label, err)
	}
	return nil
}

const createWebsiteInsertSQL = `INSERT INTO websites (name, domain, aliases, status, system_user, web_root, document_root_subdir, log_dir,
	 db_name, db_user, php_pool_path, nginx_conf_path, site_type, ssl_enabled, ssl_cert_path, ssl_key_path, ssl_expires_at, ssl_last_error, ssl_cert_source, template_version, access_log_mode, disable_application_passwords, log_retention_days, php_fpm_max_children, expires_at)
	 VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'v1.0', 'error_only', 1, ?, ?, ?)`

func moveSiteLogDir(oldLogDir, newLogDir string) error {
	if oldLogDir == newLogDir {
		return nil
	}
	if _, err := os.Stat(oldLogDir); err != nil {
		return fmt.Errorf("检查旧日志目录失败: %w", err)
	}
	if info, err := os.Stat(newLogDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("目标日志路径已存在且不是目录: %s", newLogDir)
		}
		entries, err := os.ReadDir(newLogDir)
		if err != nil {
			return fmt.Errorf("读取目标日志目录失败: %w", err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("目标日志目录已存在且不为空: %s", newLogDir)
		}
		if err := os.Remove(newLogDir); err != nil {
			return fmt.Errorf("清理空目标日志目录失败: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查目标日志目录失败: %w", err)
	}
	return os.Rename(oldLogDir, newLogDir)
}

func createSiteLogDir(logDir string) error {
	if strings.TrimSpace(logDir) == "" {
		return fmt.Errorf("日志目录为空")
	}
	if info, err := os.Lstat(logDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("日志目录不能是符号链接: %s", logDir)
		}
		if !info.IsDir() {
			return fmt.Errorf("日志路径已存在且不是目录: %s", logDir)
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return err
		}
	} else {
		return err
	}
	ensureSiteLogFiles(logDir)
	return nil
}

func managedSubpath(rootPath, targetPath, label string) (string, error) {
	rootPath = strings.TrimSpace(rootPath)
	targetPath = strings.TrimSpace(targetPath)
	if rootPath == "" || targetPath == "" {
		return "", fmt.Errorf("%s路径为空", label)
	}

	root := filepath.Clean(rootPath)
	target := filepath.Clean(targetPath)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("%s路径校验失败: %w", label, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s路径不在允许目录内: %s", label, targetPath)
	}
	return target, nil
}

func ensureCreateSiteResourcesAvailable(systemUser, webRoot, logDir, dbName, dbUser, phpPoolPath, nginxConfPath, nginxEnabledPath, phpSockPath string) error {
	db := database.GetDB()
	if db != nil {
		var domain string
		err := db.QueryRow(`
			SELECT domain
			FROM websites
			WHERE system_user = ?
			   OR web_root = ?
			   OR log_dir = ?
			   OR db_name = ?
			   OR db_user = ?
			   OR php_pool_path = ?
			   OR nginx_conf_path = ?
			LIMIT 1
		`, systemUser, webRoot, logDir, dbName, dbUser, phpPoolPath, nginxConfPath).Scan(&domain)
		if err == nil {
			return fmt.Errorf("internal resource is already used by site %s", domain)
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check existing site resources: %w", err)
		}
	}

	if _, err := executeCommand("id", "-u", systemUser); err == nil {
		return fmt.Errorf("system user already exists: %s", systemUser)
	}

	for label, path := range map[string]string{
		"web root":            webRoot,
		"log dir":             logDir,
		"php-fpm pool":        phpPoolPath,
		"nginx config":        nginxConfPath,
		"nginx enabled link":  nginxEnabledPath,
		"php-fpm socket file": phpSockPath,
	} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists: %s", label, path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check %s: %w", label, err)
		}
	}

	return nil
}

func executeCreateSite(task *Task) TaskResult {
	payload, ok := task.Payload.(*CreateSitePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	var rollbacks []rollbackStep
	rollback := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			step := rollbacks[i]
			if err := step.fn(); err != nil {
				fmt.Fprintf(os.Stderr, "回滚失败 [%s]: %v\n", step.desc, err)
			}
		}
	}

	cfg := config.AppConfig
	domain := strings.ToLower(strings.TrimSpace(payload.Domain))
	siteName := buildSiteName(domain)

	dbPassword := payload.DBPassword
	if dbPassword == "" {
		dbPassword = generatePassword(24)
	}

	if !IsValidDomain(domain) {
		return TaskResult{Success: false, Message: "域名格式不合法: " + domain}
	}
	for _, alias := range payload.Aliases {
		if !IsValidDomain(strings.TrimSpace(alias)) {
			return TaskResult{Success: false, Message: "附加域名格式不合法: " + alias}
		}
	}

	systemUser := "wp_" + siteName
	if payload.SiteType == "php" {
		systemUser = "php_" + siteName
	}
	documentRootSubdir, err := NormalizeDocumentRootSubdir(payload.SiteType, payload.DocumentRootSubdir)
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	webRoot := filepath.Join(cfg.Paths.WWWRoot, domain)
	logDir := filepath.Join(cfg.Paths.WWWLogs, domain)
	dbName := "db_" + siteName
	dbUser := "user_" + siteName
	configBase := siteConfigBaseName(siteName)
	phpPoolPath := filepath.Join(cfg.Paths.PHPFPMPool, configBase+".conf")
	nginxConfPath := filepath.Join(cfg.Paths.NginxSitesAvailable, configBase+".conf")
	nginxEnabledPath := filepath.Join(cfg.Paths.NginxSitesEnabled, configBase+".conf")
	phpSockPath := filepath.Join(cfg.Paths.PHPFPMSock, configBase+".sock")
	if err := validateUnixSocketPath(phpSockPath); err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}

	if err := ensureCreateSiteResourcesAvailable(systemUser, webRoot, logDir, dbName, dbUser, phpPoolPath, nginxConfPath, nginxEnabledPath, phpSockPath); err != nil {
		log.Printf("站点资源名冲突 domain=%s: %v", domain, err)
		return TaskResult{Success: false, Message: "站点资源名冲突: " + err.Error()}
	}

	// Step 1: Create system user. Register cleanup as soon as useradd succeeds so
	// a later primary-group verification failure cannot leave an orphan account.
	userRollback, err := createSiteSystemUser(systemUser, executeCommand, ensureSitePrimaryGroup)
	if err != nil {
		log.Printf("创建站点用户组失败: %v", err)
		return TaskResult{Success: false, Message: err.Error()}
	}
	rollbacks = append(rollbacks, rollbackStep{"删除系统用户 " + systemUser, userRollback})

	// Step 2: Create directories
	for _, dir := range []string{webRoot, logDir} {
		if _, err := executeCommand("mkdir", "-p", dir); err != nil {
			rollback()
			log.Printf("创建目录失败: %v", err)
			return TaskResult{Success: false, Message: "创建目录失败"}
		}
	}
	ensureSiteLogFiles(logDir)
	rollbacks = append(rollbacks, rollbackStep{"删除网站目录 " + webRoot, func() error {
		os.RemoveAll(webRoot)
		return nil
	}})
	rollbacks = append(rollbacks, rollbackStep{"删除日志目录 " + logDir, func() error {
		os.RemoveAll(logDir)
		return nil
	}})
	documentRoot, err := EnsureEffectiveDocumentRoot(webRoot, payload.SiteType, documentRootSubdir, systemUser)
	if err != nil {
		rollback()
		log.Printf("准备Web入口目录失败: %v", err)
		return taskFailure("准备Web入口目录失败", err)
	}

	// Step 3: Deploy site files
	if payload.SiteType != "php" {
		tmpDir := "/tmp/wp_deploy_" + siteName + "_" + generatePassword(8)
		if err := deployWordPress(context.Background(), cfg, webRoot, tmpDir); err != nil {
			rollback()
			log.Printf("WordPress 部署失败: %v", err)
			return TaskResult{Success: false, Message: "WordPress 部署失败"}
		}
	}

	// Step 4: Chown
	if _, err := executeCommand("chown", "-R", siteOwner(systemUser), webRoot); err != nil {
		rollback()
		log.Printf("设置目录权限失败: %v", err)
		return TaskResult{Success: false, Message: "设置目录权限失败"}
	}

	// Step 5: Create database
	if err := createMariaDBDatabase(dbName, dbUser, dbPassword, cfg); err != nil {
		rollback()
		log.Printf("创建数据库失败: %v", err)
		return TaskResult{Success: false, Message: "创建数据库失败"}
	}
	rollbacks = append(rollbacks, rollbackStep{"删除数据库 " + dbName, func() error {
		return dropMariaDBDatabase(dbName, dbUser, cfg)
	}})

	// Step 6: Generate wp-config.php (wordpress only)
	if payload.SiteType != "php" {
		if err := generateWPConfig(webRoot, domain, dbName, dbUser, dbPassword); err != nil {
			rollback()
			log.Printf("生成 wp-config.php 失败: %v", err)
			return TaskResult{Success: false, Message: "生成 wp-config.php 失败"}
		}
	}
	if err := HardenSiteSensitivePermissions(domain, webRoot, systemUser); err != nil {
		rollback()
		log.Printf("设置站点安全权限失败: %v", err)
		return TaskResult{Success: false, Message: "设置站点安全权限失败"}
	}

	// Step 7: Generate Nginx + PHP-FPM configs
	engine := NewTemplateEngine(cfg.Panel.BackupDir)

	allServerNames := buildServerNames(domain, payload.Aliases)

	// pm.max_children 按建站当时的服务器内存/CPU 计算一次，随即持久化到 websites 表，
	// 此后固定不变，不随服务器后续新增/删除站点而改变（见 PHPFPMPoolData.MaxChildren 注释）。
	maxChildren := RecommendPHPFPMMaxChildren(CollectSystemFacts())

	phpData := &PHPFPMPoolData{
		Domain:      domain,
		PoolName:    configBase,
		SystemUser:  systemUser,
		WebRoot:     webRoot,
		SocketPath:  cfg.Paths.PHPFPMSock,
		SocketName:  configBase,
		MaxChildren: strconv.Itoa(maxChildren),
	}
	phpConfig, err := engine.RenderPHPFPMPool(phpData)
	if err != nil {
		rollback()
		log.Printf("渲染 PHP-FPM 配置失败: %v", err)
		return taskFailure("渲染 PHP-FPM 配置失败", err)
	}
	if err := engine.ApplyPHPFPMPool(phpConfig, phpPoolPath, logDir, phpSockPath); err != nil {
		rollback()
		log.Printf("应用 PHP-FPM 配置失败: %v", err)
		return taskFailure("应用 PHP-FPM 配置失败", err)
	}
	rollbacks = append(rollbacks, rollbackStep{"删除PHP-FPM配置 " + phpPoolPath, func() error {
		os.Remove(phpPoolPath)
		exec.Command("systemctl", "reload", "php8.3-fpm").Run()
		return nil
	}})

	nginxData := &NginxSiteData{
		Domain:        domain,
		Aliases:       payload.Aliases,
		ServerNames:   allServerNames,
		WebRoot:       documentRoot,
		LogDir:        logDir,
		SystemUser:    systemUser,
		UseSSL:        false,
		PHPProxy:      "unix:" + phpSockPath,
		SiteType:      payload.SiteType,
		TemplateVer:   "v1.0",
		AccessLogMode: "error_only",
	}

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		rollback()
		log.Printf("渲染 Nginx 配置失败: %v", err)
		return taskFailure("渲染 Nginx 配置失败", err)
	}

	if err := engine.ApplyNginxConfig(nginxConfig, nginxConfPath, nginxEnabledPath); err != nil {
		rollback()
		log.Printf("应用 Nginx 配置失败: %v", err)
		return taskFailure("应用 Nginx 配置失败", err)
	}
	rollbacks = append(rollbacks, rollbackStep{"删除Nginx配置 " + nginxConfPath, func() error {
		os.Remove(nginxEnabledPath)
		os.Remove(nginxConfPath)
		exec.Command("nginx", "-s", "reload").Run()
		return nil
	}})

	maskedPassword := maskPassword(dbPassword)

	certDir := filepath.Join(cfg.Paths.Certificates, domain)
	certPath := filepath.Join(certDir, "fullchain.pem")
	keyPath := filepath.Join(certDir, "privkey.pem")

	sslEnabled := 0
	var sslExpiry *time.Time
	sslWarning := ""
	if payload.SSLEnabled {
		if sslErr := os.MkdirAll(certDir, 0700); sslErr != nil {
			rollback()
			log.Printf("创建SSL证书目录失败: %v", sslErr)
			return TaskResult{Success: false, Message: "创建SSL证书目录失败"}
		}
		expiry, sslErr := obtainLegoCert(domain, strings.Join(payload.Aliases, "\n"), documentRoot, certDir)
		if sslErr != nil {
			log.Printf("申请 Let's Encrypt 证书失败: %v", sslErr)
			sslWarning = FriendlySSLError(sslErr)
			os.RemoveAll(certDir)
		} else {
			sslData := &NginxSiteData{
				Domain:        domain,
				Aliases:       payload.Aliases,
				ServerNames:   allServerNames,
				WebRoot:       documentRoot,
				LogDir:        logDir,
				SystemUser:    systemUser,
				UseSSL:        true,
				SSLCertPath:   certPath,
				SSLKeyPath:    keyPath,
				PHPProxy:      "unix:" + phpSockPath,
				SiteType:      payload.SiteType,
				TemplateVer:   "v1.0",
				AccessLogMode: "error_only",
			}

			httpsConfig, sslErr := engine.RenderNginxConfig(sslData)
			if sslErr != nil {
				log.Printf("渲染 HTTPS 配置失败: %v", sslErr)
				sslWarning = "渲染 HTTPS 配置失败：" + sslErr.Error()
				os.RemoveAll(certDir)
			} else if sslErr := engine.ApplyNginxConfig(httpsConfig, nginxConfPath, nginxEnabledPath); sslErr != nil {
				log.Printf("应用 HTTPS 配置失败: %v", sslErr)
				os.RemoveAll(certDir)
				if restoreErr := engine.ApplyNginxConfig(nginxConfig, nginxConfPath, nginxEnabledPath); restoreErr != nil {
					rollback()
					log.Printf("恢复 HTTP 配置失败: %v", restoreErr)
					return taskFailure("应用 HTTPS 配置失败，且恢复 HTTP 配置失败", restoreErr)
				}
				sslWarning = "应用 HTTPS 配置失败：" + sslErr.Error()
			} else {
				sslEnabled = 1
				sslExpiry = &expiry
				rollbacks = append(rollbacks, rollbackStep{"删除SSL证书目录 " + certDir, func() error {
					os.RemoveAll(certDir)
					return nil
				}})
			}
		}
	}

	if payload.SiteType != "php" {
		if payload.CleanDefaults {
			removeDefaultPlugins(webRoot)
			log.Printf("已清理默认插件 site=%s", domain)
		}
		if payload.RemoveUnusedThemes {
			removeUnusedThemes(webRoot)
			log.Printf("已删除未使用默认主题 site=%s", domain)
		}
		if len(payload.InstallThemes) > 0 || len(payload.InstallPlugins) > 0 {
			installExtensions(webRoot, systemUser, payload.InstallThemes, payload.InstallPlugins)
			log.Printf("已安装扩展 site=%s themes=%v plugins=%v", domain, payload.InstallThemes, payload.InstallPlugins)
		}
	}

	sslCertSource := ""
	if sslEnabled == 1 {
		sslCertSource = "auto"
	}
	db := database.GetDB()
	insertResult, err := db.Exec(
		createWebsiteInsertSQL,
		siteName, domain, strings.Join(payload.Aliases, "\n"), systemUser,
		webRoot, documentRootSubdir, logDir, dbName, dbUser, phpPoolPath, nginxConfPath, payload.SiteType, sslEnabled,
		certPath, keyPath, sslExpiry, sslWarning, sslCertSource, defaultSiteLogRetentionDays, maxChildren, nilIfEmpty(payload.ExpiresAt),
	)
	if err != nil {
		rollback()
		log.Printf("写入数据库失败: %v", err)
		return TaskResult{Success: false, Message: "写入数据库失败"}
	}
	siteID, _ := insertResult.LastInsertId()
	if err := WriteSiteLogrotateConfig(domain, logDir, defaultSiteLogRetentionDays); err != nil {
		log.Printf("site logrotate config skipped after site create: %v", err)
	}
	if err := ReloadFail2ban(); err != nil {
		log.Printf("Fail2ban reload skipped after site create: %v", err)
	}

	sslMsg := ""
	if sslEnabled == 1 {
		sslMsg = fmt.Sprintf("，SSL 已启用（到期: %s）", sslExpiry.Format("2006-01-02"))
	} else if sslWarning != "" {
		sslMsg = "，但 SSL 未启用: " + sslWarning
	}

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("网站 %s 创建成功%s", domain, sslMsg),
		Data: map[string]interface{}{
			"domain":      domain,
			"id":          siteID,
			"db_name":     dbName,
			"db_user":     dbUser,
			"db_password": maskedPassword,
			"web_root":    webRoot,
			"system_user": systemUser,
			"ssl_enabled": sslEnabled == 1,
			"ssl_warning": sslWarning,
		},
	}
}

func createSiteSystemUser(systemUser string, run func(string, ...string) (string, error), ensureGroup func(string) error) (func() error, error) {
	if _, err := run("useradd", "-r", "-U", "-s", "/usr/sbin/nologin", "-M", "-d", "/nonexistent", systemUser); err != nil {
		return nil, fmt.Errorf("创建系统用户失败: %w", err)
	}
	rollback := func() error {
		_, err := run("userdel", "-r", "-f", systemUser)
		return err
	}
	if err := ensureGroup(systemUser); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return nil, fmt.Errorf("创建站点用户组失败: %v；清理新建系统用户失败: %w", err, rollbackErr)
		}
		return nil, fmt.Errorf("创建站点用户组失败，新建系统用户已清理: %w", err)
	}
	return rollback, nil
}

func executeDeleteSite(task *Task) TaskResult {
	payload, ok := task.Payload.(*DeleteSitePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
	if !TryAcquireSiteOpLock(site.ID, "delete") {
		return TaskResult{Success: false, Message: "网站维护操作尚未结束"}
	}
	defer ReleaseSiteOpLock(site.ID)
	if blocked, err := database.IsAIDevelopmentAccessBlocking(context.Background(), database.GetDB(), int64(site.ID)); err != nil {
		return TaskResult{Success: false, Message: "检查 AI 开发授权失败"}
	} else if blocked {
		return TaskResult{Success: false, Message: "该网站已开启 AI 开发访问，请先关闭授权"}
	}
	if locked, err := SiteMigrationDeleteBlocked(context.Background(), site.ID, site.Domain); err != nil {
		return TaskResult{Success: false, Message: "检查站点迁移锁失败"}
	} else if locked {
		return TaskResult{Success: false, Message: "该网站仍受网站搬家任务保护，不能从网站列表直接删除。请前往「网站搬家」处理该任务"}
	}
	cfg := config.AppConfig

	webRoot, err := managedSubpath(cfg.Paths.WWWRoot, site.WebRoot, "网站目录")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	logDir, err := managedSubpath(cfg.Paths.WWWLogs, site.LogDir, "日志目录")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	phpPoolPath, err := managedSubpath(cfg.Paths.PHPFPMPool, site.PHPPoolPath, "PHP-FPM配置")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	nginxConfPath, err := managedSubpath(cfg.Paths.NginxSitesAvailable, site.NginxConfPath, "Nginx配置")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	enabledPath := nginxEnabledPath(cfg, nginxConfPath, site.Domain)
	enabledPath, err = managedSubpath(cfg.Paths.NginxSitesEnabled, enabledPath, "Nginx启用链接")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	secretsDir, err := managedSubpath(siteSecretsRoot, sitePluginSecretsDir(site.Domain), "站点密钥目录")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	logrotatePath, err := managedSubpath("/etc/logrotate.d", filepath.Join("/etc/logrotate.d", "wppanel-"+site.Domain), "日志轮转配置")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	certDir, err := managedSubpath(cfg.Paths.Certificates, filepath.Join(cfg.Paths.Certificates, site.Domain), "证书目录")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	db := database.GetDB()
	maintenancePaths, err := terminalSourceMigrationMaintenancePaths(db, site.ID, cfg.Paths.NginxSitesAvailable)
	if err != nil {
		return TaskResult{Success: false, Message: "检查网站搬家维护配置失败"}
	}
	if currentTarget, readErr := os.Readlink(enabledPath); readErr == nil && strings.HasPrefix(filepath.Base(currentTarget), ".wp-panel-migration-") {
		currentTarget, pathErr := managedSubpath(cfg.Paths.NginxSitesAvailable, currentTarget, "迁移维护配置")
		if pathErr != nil {
			return TaskResult{Success: false, Message: pathErr.Error()}
		}
		maintenancePaths = append(maintenancePaths, currentTarget)
	}

	// Persist the destructive operation before touching external resources. If
	// final SQLite cleanup later fails (or the process stops), the surviving row
	// must not continue to look like a usable website. Re-running delete accepts
	// the existing deleting state and finishes the idempotent cleanup.
	if err := markWebsiteDeleting(db, site.ID); err != nil {
		return TaskResult{Success: false, Message: "标记网站删除状态失败: " + err.Error()}
	}

	if _, err := executeCommand("userdel", "-r", "-f", site.SystemUser); err != nil {
		fmt.Fprintf(os.Stderr, "删除系统用户警告: %v\n", err)
	}

	os.RemoveAll(webRoot)
	os.RemoveAll(logDir)
	os.RemoveAll(secretsDir)

	// Clean up logrotate config
	os.Remove(logrotatePath)

	dbCleanupWarning := ""
	if err := dropMariaDBDatabase(site.DBName, site.DBUser, cfg); err != nil {
		log.Printf("删除数据库失败 domain=%s db=%s: %v", site.Domain, site.DBName, err)
		dbCleanupWarning = "，但数据库清理失败，请检查 MariaDB 后手动清理"
	}

	os.Remove(phpPoolPath)
	os.Remove(enabledPath)
	os.Remove(nginxConfPath)
	for _, maintenancePath := range maintenancePaths {
		os.Remove(maintenancePath)
	}

	exec.Command("nginx", "-s", "reload").Run()
	exec.Command("systemctl", "reload", "php8.3-fpm").Run()

	os.RemoveAll(certDir)

	cronDeleted, err := deleteSiteAndAssociatedCronJobs(db, site.ID)
	if err != nil {
		return TaskResult{Success: false, Message: "清理数据库记录失败: " + err.Error()}
	}
	if cronDeleted {
		if result := renderCronConfig(); !result.Success {
			log.Printf("删除网站关联计划任务后刷新Cron配置失败 domain=%s: %s", site.Domain, result.Message)
		}
	}

	return TaskResult{Success: true, Message: "网站 " + site.Domain + " 已删除" + dbCleanupWarning}
}

func markWebsiteDeleting(db *sql.DB, siteID int) error {
	result, err := db.Exec(`UPDATE websites SET status=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status<>?`,
		models.StatusDeleting, siteID, models.StatusDeleting)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}

	var status models.WebsiteStatus
	if err := db.QueryRow(`SELECT status FROM websites WHERE id=?`, siteID).Scan(&status); err != nil {
		return err
	}
	if status != models.StatusDeleting {
		return errors.New("网站状态已变化")
	}
	return nil
}

func terminalSourceMigrationMaintenancePaths(db *sql.DB, siteID int, nginxRoot string) ([]string, error) {
	rows, err := db.Query(`SELECT r.identifier FROM site_migration_resources r
		JOIN site_migration_sites ms ON ms.id=r.migration_site_id AND ms.source_site_id=?
		WHERE ms.status IN ('completed','abandoned') AND r.resource_type='source_maintenance_config' AND r.status='created'`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		path, err = managedSubpath(nginxRoot, path, "迁移维护配置")
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

// deleteSiteAndAssociatedCronJobs 原子删除网站记录和所有仍关联该网站的计划任务。
// cron_jobs.site_id 的外键是 ON DELETE SET NULL；这里必须先显式删除关联任务，
// 避免 wp_cron/file_backup 等任务在网站删除后变成继续执行的孤儿任务。
func deleteSiteAndAssociatedCronJobs(db *sql.DB, siteID int) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(`UPDATE site_migration_resources SET status='removed',updated_at=CURRENT_TIMESTAMP
		WHERE resource_type='source_maintenance_config' AND status='created' AND migration_site_id IN (
			SELECT id FROM site_migration_sites WHERE source_site_id=? AND status IN ('completed','abandoned')
		)`, siteID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE site_migration_locks SET status='released',site_id=NULL,released_at=COALESCE(released_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP
		WHERE site_id=? AND (status='released' OR (status='active' AND direction='source' AND EXISTS (
			SELECT 1 FROM site_migration_sites ms WHERE ms.id=site_migration_locks.migration_site_id
			AND ms.source_site_id=? AND ms.status='completed' AND ms.stage='completed'
		)))`, siteID, siteID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE site_migration_sites SET source_site_id=NULL WHERE source_site_id=? AND status IN ('completed','abandoned','failed_manual')`, siteID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE site_migration_sites SET target_site_id=NULL WHERE target_site_id=? AND status IN ('completed','abandoned','failed_manual')`, siteID); err != nil {
		return false, err
	}

	res, err := tx.Exec(`DELETE FROM cron_jobs WHERE site_id = ?`, siteID)
	if err != nil {
		return false, err
	}
	cronRows, _ := res.RowsAffected()
	if _, err := tx.Exec("DELETE FROM websites WHERE id = ?", siteID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	committed = true
	if err := RemoveWPCodeIntegrityBaseline(siteID); err != nil {
		log.Printf("删除网站后清理代码完整性基线失败 site=%d: %v", siteID, err)
	}
	return cronRows > 0, nil
}

func executePauseSite(task *Task) TaskResult {
	payload, ok := task.Payload.(*PauseSitePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
	if locked, err := SiteMigrationLocked(context.Background(), site.ID, site.Domain); err != nil {
		return TaskResult{Success: false, Message: "检查站点迁移锁失败"}
	} else if locked {
		return TaskResult{Success: false, Message: "网站正在迁移维护中"}
	}
	if site.Status != models.StatusActive {
		return TaskResult{Success: false, Message: "当前网站状态不能执行暂停"}
	}
	cfg := config.AppConfig

	nginxConfPath, err := managedSubpath(cfg.Paths.NginxSitesAvailable, site.NginxConfPath, "Nginx配置")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	enabledPath := nginxEnabledPath(cfg, nginxConfPath, site.Domain)
	enabledPath, err = managedSubpath(cfg.Paths.NginxSitesEnabled, enabledPath, "Nginx启用链接")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	if err := changeWebsiteStatus(site.ID, models.StatusActive, models.StatusPaused); err != nil {
		return TaskResult{Success: false, Message: "更新网站状态失败: " + err.Error()}
	}
	restoreStatus := func() error {
		return changeWebsiteStatus(site.ID, models.StatusPaused, models.StatusActive)
	}
	var removedEnabled bool
	if _, err := os.Lstat(enabledPath); err == nil {
		if err := os.Remove(enabledPath); err != nil {
			if restoreErr := restoreStatus(); restoreErr != nil {
				return TaskResult{Success: false, Message: "移除Nginx启用链接失败，网站状态恢复失败，请人工检查"}
			}
			return TaskResult{Success: false, Message: "移除Nginx启用链接失败: " + err.Error()}
		}
		removedEnabled = true
	} else if !os.IsNotExist(err) {
		if restoreErr := restoreStatus(); restoreErr != nil {
			return TaskResult{Success: false, Message: "检查Nginx启用链接失败，网站状态恢复失败，请人工检查"}
		}
		return TaskResult{Success: false, Message: "检查Nginx启用链接失败: " + err.Error()}
	}

	if out, err := runWebsiteStateNginxReload(); err != nil {
		var linkRestoreErr, reloadRestoreErr error
		if removedEnabled {
			if linkRestoreErr = os.Symlink(nginxConfPath, enabledPath); linkRestoreErr != nil {
				log.Printf("暂停失败后恢复Nginx启用链接失败 path=%s: %v", enabledPath, linkRestoreErr)
			} else {
				_, reloadRestoreErr = runWebsiteStateNginxReload()
			}
		}
		statusRestoreErr := restoreStatus()
		if linkRestoreErr != nil || reloadRestoreErr != nil || statusRestoreErr != nil {
			return TaskResult{Success: false, Message: "Nginx 重载失败，网站原状态恢复未完成，请人工检查"}
		}
		return TaskResult{Success: false, Message: "Nginx 重载失败: " + string(out)}
	}

	return TaskResult{Success: true, Message: "网站 " + site.Domain + " 已暂停"}
}

func executeEnableSite(task *Task) TaskResult {
	payload, ok := task.Payload.(*EnableSitePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
	if locked, err := SiteMigrationLocked(context.Background(), site.ID, site.Domain); err != nil {
		return TaskResult{Success: false, Message: "检查站点迁移锁失败"}
	} else if locked {
		return TaskResult{Success: false, Message: "网站正在迁移维护中"}
	}
	if site.Status != models.StatusPaused && site.Status != models.StatusMigrated {
		return TaskResult{Success: false, Message: "当前网站状态不能执行启用"}
	}
	cfg := config.AppConfig

	nginxConfPath, err := managedSubpath(cfg.Paths.NginxSitesAvailable, site.NginxConfPath, "Nginx配置")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	enabledPath := nginxEnabledPath(cfg, nginxConfPath, site.Domain)
	enabledPath, err = managedSubpath(cfg.Paths.NginxSitesEnabled, enabledPath, "Nginx启用链接")
	if err != nil {
		return TaskResult{Success: false, Message: err.Error()}
	}
	oldTarget, hadOldLink := "", false
	if target, err := os.Readlink(enabledPath); err == nil {
		oldTarget = target
		hadOldLink = true
	} else if !os.IsNotExist(err) {
		return TaskResult{Success: false, Message: "检查Nginx启用链接失败: " + err.Error()}
	}
	maintenancePath := ""
	if site.Status == models.StatusMigrated {
		if !hadOldLink {
			return TaskResult{Success: false, Message: "搬家维护配置不可用"}
		}
		if filepath.Clean(oldTarget) != filepath.Clean(nginxConfPath) {
			if !strings.HasPrefix(filepath.Base(oldTarget), ".wp-panel-migration-") {
				return TaskResult{Success: false, Message: "搬家维护配置已变化"}
			}
			maintenancePath, err = managedSubpath(cfg.Paths.NginxSitesAvailable, oldTarget, "迁移维护配置")
			if err != nil {
				return TaskResult{Success: false, Message: err.Error()}
			}
		}
	}
	if err := changeWebsiteStatus(site.ID, site.Status, models.StatusActive); err != nil {
		return TaskResult{Success: false, Message: "更新网站状态失败: " + err.Error()}
	}
	restoreStatus := func() error {
		return changeWebsiteStatus(site.ID, models.StatusActive, site.Status)
	}
	if err := atomicReplaceSymlink(enabledPath, nginxConfPath); err != nil {
		log.Printf("创建软链接失败: %v", err)
		if restoreErr := restoreStatus(); restoreErr != nil {
			return TaskResult{Success: false, Message: "创建软链接失败，网站状态恢复失败，请人工检查"}
		}
		return TaskResult{Success: false, Message: "创建软链接失败"}
	}

	if out, err := runWebsiteStateNginxReload(); err != nil {
		var linkRestoreErr, reloadRestoreErr error
		if hadOldLink {
			if linkRestoreErr = atomicReplaceSymlink(enabledPath, oldTarget); linkRestoreErr != nil {
				log.Printf("启用失败后恢复Nginx启用链接失败 path=%s: %v", enabledPath, linkRestoreErr)
			} else {
				_, reloadRestoreErr = runWebsiteStateNginxReload()
			}
		} else {
			linkRestoreErr = os.Remove(enabledPath)
			if linkRestoreErr == nil || os.IsNotExist(linkRestoreErr) {
				linkRestoreErr = nil
				_, reloadRestoreErr = runWebsiteStateNginxReload()
			}
		}
		statusRestoreErr := restoreStatus()
		if linkRestoreErr != nil || reloadRestoreErr != nil || statusRestoreErr != nil {
			return TaskResult{Success: false, Message: "Nginx 重载失败，网站原状态恢复未完成，请人工检查"}
		}
		return TaskResult{Success: false, Message: "Nginx 重载失败: " + string(out)}
	}
	if maintenancePath != "" {
		if err := os.Remove(maintenancePath); err != nil && !os.IsNotExist(err) {
			log.Printf("恢复已搬家网站后清理维护配置失败 site=%d path=%s: %v", site.ID, maintenancePath, err)
		}
	}

	if site.Status == models.StatusMigrated {
		return TaskResult{Success: true, Message: "网站 " + site.Domain + " 已解除搬家维护模式并恢复运行"}
	}
	return TaskResult{Success: true, Message: "网站 " + site.Domain + " 已启用" + enableSiteSSLWarning(site, time.Now())}
}

func enableSiteSSLWarning(site *models.Website, now time.Time) string {
	if site == nil || !site.SSLEnabled || site.SSLExpiresAt == nil {
		return ""
	}
	if !site.SSLExpiresAt.After(now) {
		return "，但 SSL 证书已过期，请立即重新申请"
	}
	if !site.SSLExpiresAt.After(now.AddDate(0, 0, 30)) {
		return "，SSL 证书将在 " + site.SSLExpiresAt.Format("2006-01-02") + " 到期，请尽快续期"
	}
	return ""
}

var runWebsiteStateNginxReload = func() ([]byte, error) {
	return exec.Command("nginx", "-s", "reload").CombinedOutput()
}

var changeWebsiteStatus = updateWebsiteStatus

var applyPrimaryDomainPHP = func(engine *TemplateEngine, content, targetPath, logDir, socketPath string) error {
	return engine.ApplyPHPFPMPool(content, targetPath, logDir, socketPath)
}

var applyPrimaryDomainNginx = func(engine *TemplateEngine, content, targetPath, enabledPath string) error {
	return engine.ApplyNginxConfig(content, targetPath, enabledPath)
}

var updatePrimaryDomainWPSiteURLs = UpdateWPSiteURLs

var readPrimaryDomainWPSiteURLs = ReadWPSiteURLs

var changeWebsitePrimaryDomain = updateWebsitePrimaryDomain

var reloadPrimaryDomainPHP = func() error {
	out, err := exec.Command("systemctl", "reload", "php8.3-fpm").CombinedOutput()
	if err != nil {
		return fmt.Errorf("reload php-fpm: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

var reloadPrimaryDomainNginx = func() error {
	out, err := exec.Command("nginx", "-s", "reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("reload nginx: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func updateWebsitePrimaryDomain(siteID int, fromDomain string, site *models.Website) error {
	result, err := database.GetDB().Exec(`UPDATE websites SET domain = ?, aliases = ?, web_root = ?, log_dir = ?,
		nginx_conf_path = ?, php_pool_path = ?, ssl_cert_path = ?, ssl_key_path = ?,
		updated_at = CURRENT_TIMESTAMP WHERE id = ? AND domain = ?`,
		site.Domain, site.Aliases, site.WebRoot, site.LogDir,
		site.NginxConfPath, site.PHPPoolPath, site.SSLCertPath, site.SSLKeyPath, siteID, fromDomain)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return errors.New("网站主域名已变化")
	}
	return nil
}

func updateWebsiteStatus(siteID int, from, to models.WebsiteStatus) error {
	result, err := database.GetDB().Exec(
		"UPDATE websites SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = ?", to, siteID, from,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return errors.New("网站状态已变化")
	}
	return nil
}

func executeUpdateDomains(task *Task) TaskResult {
	payload, ok := task.Payload.(*UpdateDomainsPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	if !TryAcquireSiteOpLock(site.ID, "domains") {
		return TaskResult{Success: false, Message: "网站维护操作尚未结束"}
	}
	defer ReleaseSiteOpLock(site.ID)
	if blocked, err := database.IsAIDevelopmentAccessBlocking(context.Background(), database.GetDB(), int64(site.ID)); err != nil {
		return TaskResult{Success: false, Message: "检查 AI 开发授权失败"}
	} else if blocked {
		return TaskResult{Success: false, Message: "该网站已开启 AI 开发访问，请先关闭授权"}
	}
	if locked, err := SiteMigrationLocked(context.Background(), site.ID, site.Domain); err != nil {
		return TaskResult{Success: false, Message: "检查站点迁移锁失败"}
	} else if locked {
		return TaskResult{Success: false, Message: "网站正在迁移维护中"}
	}
	if blocked := rejectPausedSiteConfiguration(site.ID); blocked != nil {
		return *blocked
	}
	cfg := config.AppConfig

	domainChanged := false
	oldDomain := site.Domain
	newDomain := strings.TrimSpace(payload.NewDomain)
	newAliases := payload.Aliases

	// Validate alias domains
	for _, alias := range newAliases {
		if !IsValidDomain(strings.TrimSpace(alias)) {
			return TaskResult{Success: false, Message: "别名域名格式不合法: " + alias}
		}
	}

	if newDomain != "" && newDomain != oldDomain {
		newDomain = strings.ToLower(newDomain)
		if !IsValidDomain(newDomain) {
			return TaskResult{Success: false, Message: "新域名格式不合法: " + newDomain}
		}
		domainChanged = true
	} else {
		newDomain = oldDomain
	}

	var rollbacks []rollbackStep

	if domainChanged {
		oldWebRoot := site.WebRoot
		oldLogDir := site.LogDir
		oldNginxConf := site.NginxConfPath
		oldPHPPool := site.PHPPoolPath
		oldCertDir := filepath.Join(cfg.Paths.Certificates, oldDomain)
		oldEnabledLink := nginxEnabledPath(cfg, oldNginxConf, oldDomain)

		newWebRoot := filepath.Join(cfg.Paths.WWWRoot, newDomain)
		newLogDir := filepath.Join(cfg.Paths.WWWLogs, newDomain)
		newNginxConf := oldNginxConf
		newPHPPool := oldPHPPool
		newCertDir := filepath.Join(cfg.Paths.Certificates, newDomain)
		newEnabledLink := nginxEnabledPath(cfg, newNginxConf, newDomain)
		oldBackupDir := filepath.Join(cfg.Panel.BackupDir, oldDomain)
		newBackupDir := filepath.Join(cfg.Panel.BackupDir, newDomain)
		oldCustomPaths := []string{filepath.Join(nginxCustomDir, oldDomain+".pre.conf"), filepath.Join(nginxCustomDir, oldDomain+".conf")}
		newCustomPaths := []string{filepath.Join(nginxCustomDir, newDomain+".pre.conf"), filepath.Join(nginxCustomDir, newDomain+".conf")}
		poolName := phpPoolName(newPHPPool, newDomain)
		if err := validateUnixSocketPath(phpSocketPath(cfg, newPHPPool, newDomain)); err != nil {
			return TaskResult{Success: false, Message: err.Error()}
		}

		// 预检查：所有目标和两份新配置都在触碰现有站点前确认可用。
		if info, err := os.Stat(oldWebRoot); err != nil || !info.IsDir() {
			return TaskResult{Success: false, Message: "原网站目录不可用"}
		}
		if err := requireDomainTargetAvailable(newWebRoot, "新网站目录"); err != nil {
			return taskFailure("主域名预检查失败", err)
		}
		if info, err := os.Stat(oldLogDir); err != nil || !info.IsDir() {
			return TaskResult{Success: false, Message: "原日志目录不可用"}
		}
		if err := requireDomainTargetAvailable(newLogDir, "新日志目录"); err != nil {
			return taskFailure("主域名预检查失败", err)
		}
		if _, err := os.Lstat(oldCertDir); err == nil {
			if err := requireDomainTargetAvailable(newCertDir, "新证书目录"); err != nil {
				return taskFailure("主域名预检查失败", err)
			}
		} else if !os.IsNotExist(err) {
			return taskFailure("检查原证书目录失败", err)
		}
		if _, err := os.Lstat(sitePluginSecretsDir(oldDomain)); err == nil {
			if err := requireDomainTargetAvailable(sitePluginSecretsDir(newDomain), "新插件身份目录"); err != nil {
				return taskFailure("主域名预检查失败", err)
			}
		} else if !os.IsNotExist(err) {
			return taskFailure("检查原插件身份目录失败", err)
		}
		if _, err := os.Lstat(oldBackupDir); err == nil {
			if err := requireDomainTargetAvailable(newBackupDir, "新备份目录"); err != nil {
				return taskFailure("主域名预检查失败", err)
			}
		} else if !os.IsNotExist(err) {
			return taskFailure("检查原备份目录失败", err)
		}
		for i, oldPath := range oldCustomPaths {
			if _, err := os.Lstat(oldPath); err == nil {
				if err := requireDomainTargetAvailable(newCustomPaths[i], "新自定义 Nginx 文件"); err != nil {
					return taskFailure("主域名预检查失败", err)
				}
			} else if !os.IsNotExist(err) {
				return taskFailure("检查原自定义 Nginx 文件失败", err)
			}
		}
		oldPoolContent, err := os.ReadFile(oldPHPPool)
		if err != nil {
			return taskFailure("读取原 PHP-FPM 配置失败", err)
		}
		oldNginxContent, err := os.ReadFile(oldNginxConf)
		if err != nil {
			return taskFailure("读取原 Nginx 配置失败", err)
		}
		oldEnabledTarget, oldEnabledErr := os.Readlink(oldEnabledLink)
		if oldEnabledErr != nil {
			return taskFailure("读取原 Nginx 启用链接失败", oldEnabledErr)
		}
		if newEnabledLink != oldEnabledLink {
			if err := requireDomainTargetAvailable(newEnabledLink, "新 Nginx 启用链接"); err != nil {
				return taskFailure("主域名预检查失败", err)
			}
		}

		engine := NewTemplateEngine(cfg.Panel.BackupDir)
		phpData := &PHPFPMPoolData{
			Domain:     newDomain,
			PoolName:   poolName,
			SystemUser: site.SystemUser,
			WebRoot:    newWebRoot,
			SocketPath: cfg.Paths.PHPFPMSock,
			SocketName: poolName,
			// 改域名不等于服务器规格变化，沿用建站时已经持久化的 pm.max_children，
			// 不重新计算——否则每次改域名都会悄悄改变这个站点的并发上限。
			MaxChildren: strconv.Itoa(site.PHPFPMMaxChildren),
		}
		phpConfig, err := engine.RenderPHPFPMPool(phpData)
		if err != nil {
			return taskFailure("渲染 PHP-FPM 配置失败", err)
		}

		proposed := *site
		proposed.WebRoot = newWebRoot
		proposed.LogDir = newLogDir
		proposed.Domain = newDomain
		proposed.Aliases = strings.Join(newAliases, "\n")
		if proposed.SSLCertPath != "" {
			proposed.SSLCertPath = filepath.Join(newCertDir, "fullchain.pem")
			proposed.SSLKeyPath = filepath.Join(newCertDir, "privkey.pem")
		}
		nginxData, err := nginxDataFromSiteChecked(&proposed)
		if err != nil {
			return taskFailure("CDN 真实 IP 配置无效", err)
		}
		nginxConfig, err := engine.RenderNginxConfig(nginxData)
		if err != nil {
			return taskFailure("渲染 Nginx 配置失败", err)
		}

		// 切换：每完成一步才登记对应恢复动作。
		if err := os.Rename(oldWebRoot, newWebRoot); err != nil {
			return taskFailure("重命名网站目录失败", err)
		}
		rollbacks = append(rollbacks, rollbackStep{"恢复网站目录 " + oldWebRoot, func() error {
			return os.Rename(newWebRoot, oldWebRoot)
		}})

		if err := moveSiteLogDir(oldLogDir, newLogDir); err != nil {
			return domainUpdateFailure("重命名日志目录失败", err, rollbacks)
		}
		rollbacks = append(rollbacks, rollbackStep{"恢复日志目录 " + oldLogDir, func() error {
			return os.Rename(newLogDir, oldLogDir)
		}})

		// 插件身份目录仍随面板主域名管理，但插件通过 PHP-FPM 注入的明确路径读取，
		// 不再依赖 WordPress home URL。目标目录存在时拒绝覆盖，避免误删其他身份。
		identityMoved, err := moveSitePluginIdentity(oldDomain, newDomain)
		if err != nil {
			return domainUpdateFailure("重命名插件密钥目录失败", err, rollbacks)
		}
		if identityMoved {
			rollbacks = append(rollbacks, rollbackStep{"恢复插件密钥目录", func() error {
				_, err := moveSitePluginIdentity(newDomain, oldDomain)
				return err
			}})
		}

		if _, err := os.Lstat(oldBackupDir); err == nil {
			if err := os.Rename(oldBackupDir, newBackupDir); err != nil {
				return domainUpdateFailure("重命名网站备份目录失败", err, rollbacks)
			}
			rollbacks = append(rollbacks, rollbackStep{"恢复网站备份目录", func() error {
				return os.Rename(newBackupDir, oldBackupDir)
			}})
		}
		if err := os.MkdirAll(nginxCustomDir, 0755); err != nil {
			return domainUpdateFailure("准备自定义 Nginx 目录失败", err, rollbacks)
		}
		for i, oldPath := range oldCustomPaths {
			newPath := newCustomPaths[i]
			if _, err := os.Lstat(oldPath); err == nil {
				if err := os.Rename(oldPath, newPath); err != nil {
					return domainUpdateFailure("重命名自定义 Nginx 文件失败", err, rollbacks)
				}
				rollbacks = append(rollbacks, rollbackStep{"恢复自定义 Nginx 文件", func() error {
					return os.Rename(newPath, oldPath)
				}})
			} else {
				if err := os.WriteFile(newPath, nil, 0644); err != nil {
					return domainUpdateFailure("创建新自定义 Nginx 文件失败", err, rollbacks)
				}
				rollbacks = append(rollbacks, rollbackStep{"删除新自定义 Nginx 文件", func() error {
					return os.Remove(newPath)
				}})
			}
		}

		// 身份目录搬迁后立即切换 PHP-FPM 指针，缩短旧运行配置与新身份路径不一致的窗口。
		if err := applyPrimaryDomainPHP(engine, phpConfig, newPHPPool, newLogDir, phpSocketPath(cfg, newPHPPool, newDomain)); err != nil {
			return domainUpdateFailure("应用 PHP-FPM 配置失败", err, rollbacks)
		}
		phpRB := rollbackStep{"恢复PHP-FPM Pool " + oldPHPPool, func() error {
			if err := os.WriteFile(oldPHPPool, oldPoolContent, 0644); err != nil {
				return err
			}
			return reloadPrimaryDomainPHP()
		}}
		rollbacks = append(rollbacks, phpRB)

		if _, err := os.Stat(oldCertDir); err == nil {
			if err := os.Rename(oldCertDir, newCertDir); err != nil {
				return domainUpdateFailure("重命名 SSL 证书目录失败", err, rollbacks)
			}
			certRB := rollbackStep{"恢复SSL证书目录", func() error {
				return os.Rename(newCertDir, oldCertDir)
			}}
			rollbacks = append(rollbacks, certRB)
		}

		if err := applyPrimaryDomainNginx(engine, nginxConfig, newNginxConf, newEnabledLink); err != nil {
			return domainUpdateFailure("应用 Nginx 配置失败", err, rollbacks)
		}
		rollbacks = append(rollbacks, rollbackStep{"恢复 Nginx 配置", func() error {
			if err := os.WriteFile(oldNginxConf, oldNginxContent, 0644); err != nil {
				return err
			}
			if newEnabledLink != oldEnabledLink {
				if err := os.Remove(newEnabledLink); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			_ = os.Remove(oldEnabledLink)
			if err := os.Symlink(oldEnabledTarget, oldEnabledLink); err != nil {
				return err
			}
			return reloadPrimaryDomainNginx()
		}})

		if payload.NewWPSiteURL != "" || payload.NewWPHomeURL != "" {
			if err := updatePrimaryDomainWPSiteURLs(site.DBName, site.TablePrefix, payload.NewWPSiteURL, payload.NewWPHomeURL, cfg); err != nil {
				return domainUpdateFailure("同步 WordPress 站点 URL 失败", err, rollbacks)
			}
			rollbacks = append(rollbacks, rollbackStep{"恢复 WordPress 站点 URL", func() error {
				return updatePrimaryDomainWPSiteURLs(site.DBName, site.TablePrefix, payload.OldWPSiteURL, payload.OldWPHomeURL, cfg)
			}})
			siteURL, homeURL, err := readPrimaryDomainWPSiteURLs(site.DBName, site.TablePrefix, cfg)
			if err != nil || siteURL != payload.NewWPSiteURL || homeURL != payload.NewWPHomeURL {
				return domainUpdateFailure("验证 WordPress 站点 URL 失败", err, rollbacks)
			}
		}

		if err := changeWebsitePrimaryDomain(site.ID, oldDomain, &proposed); err != nil {
			return domainUpdateFailure("更新数据库失败", err, rollbacks)
		}
		rollbacks = append(rollbacks, rollbackStep{"恢复网站数据库记录", func() error {
			return changeWebsitePrimaryDomain(site.ID, newDomain, site)
		}})

		// 验证：数据库、关键目录和启用链接必须共同指向新状态。
		var savedDomain, savedWebRoot, savedLogDir string
		if err := database.GetDB().QueryRow("SELECT domain, web_root, log_dir FROM websites WHERE id = ?", site.ID).
			Scan(&savedDomain, &savedWebRoot, &savedLogDir); err != nil || savedDomain != newDomain || savedWebRoot != newWebRoot || savedLogDir != newLogDir {
			return domainUpdateFailure("主域名切换验证失败", err, rollbacks)
		}
		for path, label := range map[string]string{newWebRoot: "网站目录", newLogDir: "日志目录"} {
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				return domainUpdateFailure("主域名切换验证失败："+label+"不可用", err, rollbacks)
			}
		}
		if target, err := os.Readlink(newEnabledLink); err != nil || filepath.Clean(target) != filepath.Clean(newNginxConf) {
			return domainUpdateFailure("主域名切换验证失败：Nginx 启用链接异常", err, rollbacks)
		}

		*site = proposed

		msg := fmt.Sprintf("主域名已从 %s 更换为 %s", oldDomain, newDomain)
		if err := WriteSiteLogrotateConfig(oldDomain, oldLogDir, 0); err != nil {
			log.Printf("old site logrotate config cleanup skipped after domain update: %v", err)
		}
		if err := WriteSiteLogrotateConfig(newDomain, newLogDir, site.LogRetentionDays); err != nil {
			log.Printf("site logrotate config skipped after domain update: %v", err)
		}

		if site.SSLEnabled {
			msg += "。请重新申请 SSL 证书以匹配新域名"
		}
		if payload.NewWPSiteURL != "" || payload.NewWPHomeURL != "" {
			GoSafe(func() { ClearWPSiteRuntimeCaches(site.ID, newDomain, newWebRoot) })
			msg += "。WordPress 站点 URL 已同步"
		}
		refreshWPCodeIntegrityBaselineBestEffort(site.ID, "网站目录迁移成功")
		return TaskResult{Success: true, Message: msg}
	}

	oldAliases := site.Aliases
	aliasStr := strings.Join(newAliases, "\n")
	site.Aliases = aliasStr

	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	nginxData, err := nginxDataFromSiteChecked(site)
	if err != nil {
		return taskFailure("CDN 真实 IP 配置无效", err)
	}

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		log.Printf("渲染 Nginx 配置失败: %v", err)
		return taskFailure("渲染 Nginx 配置失败", err)
	}

	if err := changeWebsiteAliases(site.ID, oldAliases, aliasStr); err != nil {
		site.Aliases = oldAliases
		return taskFailure("保存网站别名失败", err)
	}
	if err := applyAliasNginx(engine, nginxConfig, site.NginxConfPath,
		nginxEnabledPath(cfg, site.NginxConfPath, newDomain)); err != nil {
		log.Printf("应用 Nginx 配置失败: %v", err)
		site.Aliases = oldAliases
		if restoreErr := changeWebsiteAliases(site.ID, aliasStr, oldAliases); restoreErr != nil {
			return TaskResult{Success: false, Message: "应用 Nginx 配置失败，网站别名状态恢复失败，请人工检查"}
		}
		return taskFailure("应用 Nginx 配置失败", err)
	}

	msg := "别名已更新"
	if site.SSLEnabled {
		msg += "。若新增了别名，请重新申请 SSL 证书以覆盖新域名"
	}

	return TaskResult{Success: true, Message: msg}
}

var applyAliasNginx = func(engine *TemplateEngine, content, targetPath, enabledPath string) error {
	return engine.ApplyNginxConfig(content, targetPath, enabledPath)
}

var changeWebsiteAliases = updateWebsiteAliases

func updateWebsiteAliases(siteID int, from, to string) error {
	result, err := database.GetDB().Exec(
		"UPDATE websites SET aliases = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND aliases = ?", to, siteID, from,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return errors.New("网站别名已变化")
	}
	return nil
}

func executeUnbanIP(task *Task) TaskResult {
	return TaskResult{Success: true, Message: "IP解封暂未实现"}
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func ReinstallWordPress(ctx context.Context, webRoot, dbName, dbUser, systemUser string, cfg *config.Config,
	cleanDefaults, removeThemes bool, installThemes, installPlugins []string) error {
	var siteID int64
	var fileLockEnabled bool
	if err := database.GetDB().QueryRowContext(ctx, `SELECT id,file_lock_enabled FROM websites WHERE web_root=? AND system_user=?`, webRoot, systemUser).Scan(&siteID, &fileLockEnabled); err != nil {
		return fmt.Errorf("检查 AI 开发授权失败: %w", err)
	}
	if fileLockEnabled {
		return errors.New("该网站已启用文件锁，请先关闭文件锁")
	}
	if blocked, err := database.IsAIDevelopmentAccessBlocking(ctx, database.GetDB(), siteID); err != nil {
		return fmt.Errorf("检查 AI 开发授权失败: %w", err)
	} else if blocked {
		return errors.New("该网站已开启 AI 开发访问，请先关闭授权")
	}
	webRoot, err := managedSubpath(cfg.Paths.WWWRoot, webRoot, "网站目录")
	if err != nil {
		return err
	}

	tmpDir := "/tmp/wp_reinstall_" + dbName + "_" + generatePassword(8)
	tmpWebRoot := filepath.Join(filepath.Dir(webRoot), "."+filepath.Base(webRoot)+".reinstall_"+generatePassword(8))
	os.RemoveAll(tmpWebRoot)
	defer os.RemoveAll(tmpWebRoot)

	if err := os.MkdirAll(tmpWebRoot, 0755); err != nil {
		return fmt.Errorf("创建临时网站目录失败: %w", err)
	}
	if err := deployWordPress(ctx, cfg, tmpWebRoot, tmpDir); err != nil {
		return fmt.Errorf("WordPress 部署失败: %w", err)
	}

	if err := dropMariaDBDatabase(dbName, dbUser, cfg); err != nil {
		return fmt.Errorf("删除旧数据库失败: %w", err)
	}

	dbPassword := generatePassword(24)
	if err := createMariaDBDatabase(dbName, dbUser, dbPassword, cfg); err != nil {
		return fmt.Errorf("重建数据库失败: %w", err)
	}

	if err := generateWPConfig(tmpWebRoot, filepath.Base(webRoot), dbName, dbUser, dbPassword); err != nil {
		return fmt.Errorf("生成 wp-config.php 失败: %w", err)
	}

	if _, err := executeCommand("chown", "-R", siteOwner(systemUser), tmpWebRoot); err != nil {
		fmt.Fprintf(os.Stderr, "设置临时目录权限警告: %v\n", err)
	}

	if err := os.RemoveAll(webRoot); err != nil {
		return fmt.Errorf("清理旧网站目录失败: %w", err)
	}
	if err := os.Rename(tmpWebRoot, webRoot); err != nil {
		return fmt.Errorf("替换网站目录失败: %w", err)
	}
	if err := HardenSiteSensitivePermissions(filepath.Base(webRoot), webRoot, systemUser); err != nil {
		fmt.Fprintf(os.Stderr, "设置安全权限警告: %v\n", err)
	}

	if cleanDefaults {
		removeDefaultPlugins(webRoot)
	}
	if removeThemes {
		removeUnusedThemes(webRoot)
	}
	if len(installThemes) > 0 || len(installPlugins) > 0 {
		installExtensions(webRoot, systemUser, installThemes, installPlugins)
	}

	return nil
}
