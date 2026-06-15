package executor

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

type rollbackStep struct {
	desc string
	fn   func() error
}

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

	// Step 1: Create system user
	if _, err := executeCommand("useradd", "-r", "-U", "-s", "/usr/sbin/nologin", "-M", "-d", "/nonexistent", systemUser); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			log.Printf("创建系统用户失败: %v", err)
			return TaskResult{Success: false, Message: "创建系统用户失败"}
		}
	}
	if err := ensureSitePrimaryGroup(systemUser); err != nil {
		log.Printf("创建站点用户组失败: %v", err)
		return TaskResult{Success: false, Message: "创建站点用户组失败"}
	}
	rollbacks = append(rollbacks, rollbackStep{"删除系统用户 " + systemUser, func() error {
		_, e := executeCommand("userdel", "-r", "-f", systemUser)
		return e
	}})

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

	// Step 3: Deploy site files
	if payload.SiteType != "php" {
		wpPackagePath := cfg.Paths.WordPressPackage
		tmpDir := "/tmp/wp_deploy_" + siteName + "_" + generatePassword(8)
		if err := deployWordPress(wpPackagePath, webRoot, tmpDir); err != nil {
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

	phpData := &PHPFPMPoolData{
		Domain:     domain,
		PoolName:   configBase,
		SystemUser: systemUser,
		WebRoot:    webRoot,
		SocketPath: cfg.Paths.PHPFPMSock,
		SocketName: configBase,
	}
	phpConfig, err := engine.RenderPHPFPMPool(phpData)
	if err != nil {
		rollback()
		log.Printf("渲染 PHP-FPM 配置失败: %v", err)
		return taskFailure("渲染 PHP-FPM 配置失败", err)
	}
	if err := engine.ApplyPHPFPMPool(phpConfig, phpPoolPath, logDir); err != nil {
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
		WebRoot:       webRoot,
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
	if payload.SSLEnabled {
		if sslErr := os.MkdirAll(certDir, 0700); sslErr != nil {
			rollback()
			log.Printf("创建SSL证书目录失败: %v", sslErr)
			return TaskResult{Success: false, Message: "创建SSL证书目录失败"}
		}
		rollbacks = append(rollbacks, rollbackStep{"删除SSL证书目录 " + certDir, func() error {
			os.RemoveAll(certDir)
			return nil
		}})
		expiry, sslErr := obtainLegoCert(domain, strings.Join(payload.Aliases, "\n"), webRoot, certDir)
		if sslErr != nil {
			rollback()
			log.Printf("申请 Let's Encrypt 证书失败: %v", sslErr)
			return TaskResult{Success: false, Message: "申请 Let's Encrypt 证书失败: " + sslErr.Error()}
		}

		sslData := &NginxSiteData{
			Domain:        domain,
			Aliases:       payload.Aliases,
			ServerNames:   allServerNames,
			WebRoot:       webRoot,
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
			rollback()
			log.Printf("渲染 HTTPS 配置失败: %v", sslErr)
			return taskFailure("渲染 HTTPS 配置失败", sslErr)
		}

		if sslErr := engine.ApplyNginxConfig(httpsConfig, nginxConfPath, nginxEnabledPath); sslErr != nil {
			rollback()
			log.Printf("应用 HTTPS 配置失败: %v", sslErr)
			return taskFailure("应用 HTTPS 配置失败", sslErr)
		}

		sslEnabled = 1
		sslExpiry = &expiry
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

	db := database.GetDB()
	_, err = db.Exec(
		`INSERT INTO websites (name, domain, aliases, status, system_user, web_root, log_dir,
		 db_name, db_user, php_pool_path, nginx_conf_path, site_type, ssl_enabled, ssl_cert_path, ssl_key_path, ssl_expires_at, template_version, access_log_mode, expires_at)
		 VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'v1.0', 'error_only', ?)`,
		siteName, domain, strings.Join(payload.Aliases, "\n"), systemUser,
		webRoot, logDir, dbName, dbUser, phpPoolPath, nginxConfPath, payload.SiteType, sslEnabled,
		certPath, keyPath, sslExpiry, nilIfEmpty(payload.ExpiresAt),
	)
	if err != nil {
		rollback()
		log.Printf("写入数据库失败: %v", err)
		return TaskResult{Success: false, Message: "写入数据库失败"}
	}
	if err := WriteSiteLogrotateConfig(domain, logDir, defaultSiteLogRetentionDays); err != nil {
		log.Printf("site logrotate config skipped after site create: %v", err)
	}
	if err := ReloadFail2ban(); err != nil {
		log.Printf("Fail2ban reload skipped after site create: %v", err)
	}

	sslMsg := ""
	if sslEnabled == 1 {
		sslMsg = fmt.Sprintf("，SSL 已启用（到期: %s）", sslExpiry.Format("2006-01-02"))
	}

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("网站 %s 创建成功%s", domain, sslMsg),
		Data: map[string]interface{}{
			"domain":      domain,
			"db_name":     dbName,
			"db_user":     dbUser,
			"db_password": maskedPassword,
			"web_root":    webRoot,
			"system_user": systemUser,
			"ssl_enabled": sslEnabled == 1,
		},
	}
}

func executeDeleteSite(task *Task) TaskResult {
	payload, ok := task.Payload.(*DeleteSitePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
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
	secretsDir, err := managedSubpath("/var/wp-panel/site-secrets", filepath.Join("/var/wp-panel/site-secrets", site.Domain), "站点密钥目录")
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

	if _, err := executeCommand("userdel", "-r", "-f", site.SystemUser); err != nil {
		fmt.Fprintf(os.Stderr, "删除系统用户警告: %v\n", err)
	}

	os.RemoveAll(webRoot)
	os.RemoveAll(logDir)
	os.RemoveAll(secretsDir)

	// Clean up logrotate config
	os.Remove(logrotatePath)

	_ = dropMariaDBDatabase(site.DBName, site.DBUser, cfg)

	os.Remove(phpPoolPath)
	os.Remove(enabledPath)
	os.Remove(nginxConfPath)

	exec.Command("nginx", "-s", "reload").Run()
	exec.Command("systemctl", "reload", "php8.3-fpm").Run()

	os.RemoveAll(certDir)

	db := database.GetDB()
	if _, err := db.Exec("DELETE FROM websites WHERE id = ?", site.ID); err != nil {
		return TaskResult{Success: false, Message: "清理数据库记录失败: " + err.Error()}
	}

	return TaskResult{Success: true, Message: "网站 " + site.Domain + " 已删除"}
}

func executePauseSite(task *Task) TaskResult {
	payload, ok := task.Payload.(*PauseSitePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
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
	var removedEnabled bool
	if _, err := os.Lstat(enabledPath); err == nil {
		if err := os.Remove(enabledPath); err != nil {
			return TaskResult{Success: false, Message: "移除Nginx启用链接失败: " + err.Error()}
		}
		removedEnabled = true
	} else if !os.IsNotExist(err) {
		return TaskResult{Success: false, Message: "检查Nginx启用链接失败: " + err.Error()}
	}

	if out, err := exec.Command("nginx", "-s", "reload").CombinedOutput(); err != nil {
		if removedEnabled {
			if restoreErr := os.Symlink(nginxConfPath, enabledPath); restoreErr != nil {
				log.Printf("暂停失败后恢复Nginx启用链接失败 path=%s: %v", enabledPath, restoreErr)
			} else {
				exec.Command("nginx", "-s", "reload").Run()
			}
		}
		return TaskResult{Success: false, Message: "Nginx 重载失败: " + string(out)}
	}

	db := database.GetDB()
	if _, err := db.Exec("UPDATE websites SET status = 'paused', updated_at = CURRENT_TIMESTAMP WHERE id = ?", site.ID); err != nil {
		return TaskResult{Success: false, Message: "更新网站状态失败: " + err.Error()}
	}

	return TaskResult{Success: true, Message: "网站 " + site.Domain + " 已暂停"}
}

func executeEnableSite(task *Task) TaskResult {
	payload, ok := task.Payload.(*EnableSitePayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}
	site := payload.Site
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
	}
	os.Remove(enabledPath)
	if err := os.Symlink(nginxConfPath, enabledPath); err != nil {
		log.Printf("创建软链接失败: %v", err)
		return TaskResult{Success: false, Message: "创建软链接失败"}
	}

	if out, err := exec.Command("nginx", "-s", "reload").CombinedOutput(); err != nil {
		_ = os.Remove(enabledPath)
		if hadOldLink {
			if restoreErr := os.Symlink(oldTarget, enabledPath); restoreErr != nil {
				log.Printf("启用失败后恢复Nginx启用链接失败 path=%s: %v", enabledPath, restoreErr)
			} else {
				exec.Command("nginx", "-s", "reload").Run()
			}
		}
		return TaskResult{Success: false, Message: "Nginx 重载失败: " + string(out)}
	}

	db := database.GetDB()
	if _, err := db.Exec("UPDATE websites SET status = 'active', updated_at = CURRENT_TIMESTAMP WHERE id = ?", site.ID); err != nil {
		return TaskResult{Success: false, Message: "更新网站状态失败: " + err.Error()}
	}

	return TaskResult{Success: true, Message: "网站 " + site.Domain + " 已启用"}
}

func executeUpdateDomains(task *Task) TaskResult {
	payload, ok := task.Payload.(*UpdateDomainsPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
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
	rollback := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			step := rollbacks[i]
			if e := step.fn(); e != nil {
				fmt.Fprintf(os.Stderr, "回滚失败 [%s]: %v\n", step.desc, e)
			}
		}
	}

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
		poolName := phpPoolName(newPHPPool, newDomain)
		if err := validateUnixSocketPath(phpSocketPath(cfg, newPHPPool, newDomain)); err != nil {
			return TaskResult{Success: false, Message: err.Error()}
		}
		os.Remove(oldEnabledLink)
		if newEnabledLink != oldEnabledLink {
			os.Remove(newEnabledLink)
		}

		nginxReload := func() { exec.Command("nginx", "-s", "reload").Run() }
		nginxRB := rollbackStep{"恢复Nginx配置", func() error {
			os.Symlink(oldNginxConf, oldEnabledLink)
			nginxReload()
			return nil
		}}
		rollbacks = append(rollbacks, nginxRB)

		oldPoolContent, _ := os.ReadFile(oldPHPPool)
		engine := NewTemplateEngine(cfg.Panel.BackupDir)
		phpData := &PHPFPMPoolData{
			Domain:     newDomain,
			PoolName:   poolName,
			SystemUser: site.SystemUser,
			WebRoot:    newWebRoot,
			SocketPath: cfg.Paths.PHPFPMSock,
			SocketName: poolName,
		}
		phpConfig, err := engine.RenderPHPFPMPool(phpData)
		if err != nil {
			rollback()
			log.Printf("渲染 PHP-FPM 配置失败: %v", err)
			return taskFailure("渲染 PHP-FPM 配置失败", err)
		}
		if err := os.Rename(oldWebRoot, newWebRoot); err != nil {
			rollback()
			log.Printf("重命名网站目录失败: %v", err)
			return TaskResult{Success: false, Message: "重命名网站目录失败"}
		}
		rollbacks = append(rollbacks, rollbackStep{"恢复网站目录 " + oldWebRoot, func() error {
			return os.Rename(newWebRoot, oldWebRoot)
		}})

		logDirMoved := true
		if err := moveSiteLogDir(oldLogDir, newLogDir); err != nil {
			logDirMoved = false
			log.Printf("重命名日志目录失败，改为创建新日志目录: %v", err)
			if createErr := createSiteLogDir(newLogDir); createErr != nil {
				rollback()
				log.Printf("创建新日志目录失败: %v", createErr)
				return TaskResult{Success: false, Message: "创建新日志目录失败"}
			}
		}
		if logDirMoved {
			rollbacks = append(rollbacks, rollbackStep{"恢复日志目录 " + oldLogDir, func() error {
				return os.Rename(newLogDir, oldLogDir)
			}})
		} else {
			rollbacks = append(rollbacks, rollbackStep{"删除新日志目录 " + newLogDir, func() error {
				_ = os.Remove(filepath.Join(newLogDir, "access.log"))
				_ = os.Remove(filepath.Join(newLogDir, "error.log"))
				return os.Remove(newLogDir)
			}})
		}

		if err := engine.ApplyPHPFPMPool(phpConfig, newPHPPool, newLogDir); err != nil {
			rollback()
			log.Printf("应用 PHP-FPM 配置失败: %v", err)
			return taskFailure("应用 PHP-FPM 配置失败", err)
		}
		phpRB := rollbackStep{"恢复PHP-FPM Pool " + oldPHPPool, func() error {
			os.Remove(newPHPPool)
			os.WriteFile(oldPHPPool, oldPoolContent, 0644)
			exec.Command("systemctl", "reload", "php8.3-fpm").Run()
			return nil
		}}
		rollbacks = append(rollbacks, phpRB)

		if _, err := os.Stat(oldCertDir); err == nil {
			if err := os.Rename(oldCertDir, newCertDir); err != nil {
				rollback()
				log.Printf("重命名SSL证书目录失败: %v", err)
				return TaskResult{Success: false, Message: "重命名SSL证书目录失败"}
			}
			certRB := rollbackStep{"恢复SSL证书目录", func() error {
				return os.Rename(newCertDir, oldCertDir)
			}}
			rollbacks = append(rollbacks, certRB)
		}

		site.WebRoot = newWebRoot
		site.LogDir = newLogDir
		site.NginxConfPath = newNginxConf

		site.PHPPoolPath = newPHPPool
		if site.SSLCertPath != "" {
			site.SSLCertPath = filepath.Join(newCertDir, "fullchain.pem")
			site.SSLKeyPath = filepath.Join(newCertDir, "privkey.pem")
		}

		aliasStr := strings.Join(newAliases, "\n")
		site.Domain = newDomain
		site.Aliases = aliasStr

		nginxData, err := nginxDataFromSiteChecked(site)
		if err != nil {
			rollback()
			return taskFailure("CDN 真实 IP 配置无效", err)
		}

		nginxConfig, err := engine.RenderNginxConfig(nginxData)
		if err != nil {
			rollback()
			log.Printf("渲染 Nginx 配置失败: %v", err)
			return taskFailure("渲染 Nginx 配置失败", err)
		}

		if err := engine.ApplyNginxConfig(nginxConfig, newNginxConf, newEnabledLink); err != nil {
			rollback()
			log.Printf("应用 Nginx 配置失败: %v", err)
			return taskFailure("应用 Nginx 配置失败", err)
		}

		db := database.GetDB()
		_, err = db.Exec(`UPDATE websites SET domain = ?, aliases = ?, web_root = ?, log_dir = ?,
			nginx_conf_path = ?, php_pool_path = ?, ssl_cert_path = ?, ssl_key_path = ?,
			updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			newDomain, aliasStr, newWebRoot, newLogDir,
			newNginxConf, newPHPPool, site.SSLCertPath, site.SSLKeyPath, site.ID)
		if err != nil {
			rollback()
			log.Printf("更新数据库失败: %v", err)
			return TaskResult{Success: false, Message: "更新数据库失败"}
		}

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
		return TaskResult{Success: true, Message: msg}
	}

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

	if err := engine.ApplyNginxConfig(nginxConfig, site.NginxConfPath,
		nginxEnabledPath(cfg, site.NginxConfPath, newDomain)); err != nil {
		log.Printf("应用 Nginx 配置失败: %v", err)
		return taskFailure("应用 Nginx 配置失败", err)
	}

	db := database.GetDB()
	_, err = db.Exec(`UPDATE websites SET aliases = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, aliasStr, site.ID)
	if err != nil {
		log.Printf("更新数据库失败: %v", err)
		return TaskResult{Success: false, Message: "更新数据库失败"}
	}

	msg := "别名已更新"
	if site.SSLEnabled {
		msg += "。若新增了别名，请重新申请 SSL 证书以覆盖新域名"
	}

	return TaskResult{Success: true, Message: msg}
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

func ReinstallWordPress(packagePath, webRoot, dbName, dbUser, systemUser string, cfg *config.Config,
	cleanDefaults, removeThemes bool, installThemes, installPlugins []string) error {
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
	if err := deployWordPress(packagePath, tmpWebRoot, tmpDir); err != nil {
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
