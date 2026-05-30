package executor

import (
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
	phpPoolPath := filepath.Join(cfg.Paths.PHPFPMPool, domain+".conf")
	nginxConfPath := filepath.Join(cfg.Paths.NginxSitesAvailable, domain+".conf")
	nginxEnabledPath := filepath.Join(cfg.Paths.NginxSitesEnabled, domain+".conf")
	phpSockPath := filepath.Join(cfg.Paths.PHPFPMSock, domain+".sock")

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
		if err := generateWPConfig(webRoot, dbName, dbUser, dbPassword); err != nil {
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
		SystemUser: systemUser,
		WebRoot:    webRoot,
		SocketPath: cfg.Paths.PHPFPMSock,
	}
	phpConfig, err := engine.RenderPHPFPMPool(phpData)
	if err != nil {
		rollback()
		log.Printf("渲染 PHP-FPM 配置失败: %v", err)
		return TaskResult{Success: false, Message: "渲染 PHP-FPM 配置失败"}
	}
	if err := engine.ApplyPHPFPMPool(phpConfig, phpPoolPath, logDir); err != nil {
		rollback()
		log.Printf("应用 PHP-FPM 配置失败: %v", err)
		return TaskResult{Success: false, Message: "应用 PHP-FPM 配置失败"}
	}
	rollbacks = append(rollbacks, rollbackStep{"删除PHP-FPM配置 " + phpPoolPath, func() error {
		os.Remove(phpPoolPath)
		exec.Command("systemctl", "reload", "php8.3-fpm").Run()
		return nil
	}})

	nginxData := &NginxSiteData{
		Domain:      domain,
		Aliases:     payload.Aliases,
		ServerNames: allServerNames,
		WebRoot:     webRoot,
		LogDir:      logDir,
		SystemUser:  systemUser,
		UseSSL:      false,
		PHPProxy:    "unix:" + phpSockPath,
		SiteType:    payload.SiteType,
		TemplateVer: "v1.0",
	}

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		rollback()
		log.Printf("渲染 Nginx 配置失败: %v", err)
		return TaskResult{Success: false, Message: "渲染 Nginx 配置失败"}
	}

	if err := engine.ApplyNginxConfig(nginxConfig, nginxConfPath, nginxEnabledPath); err != nil {
		rollback()
		log.Printf("应用 Nginx 配置失败: %v", err)
		return TaskResult{Success: false, Message: "应用 Nginx 配置失败"}
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
			Domain:      domain,
			Aliases:     payload.Aliases,
			ServerNames: allServerNames,
			WebRoot:     webRoot,
			LogDir:      logDir,
			SystemUser:  systemUser,
			UseSSL:      true,
			SSLCertPath: certPath,
			SSLKeyPath:  keyPath,
			PHPProxy:    "unix:" + phpSockPath,
			SiteType:    payload.SiteType,
			TemplateVer: "v1.0",
		}

		httpsConfig, sslErr := engine.RenderNginxConfig(sslData)
		if sslErr != nil {
			rollback()
			log.Printf("渲染 HTTPS 配置失败: %v", sslErr)
			return TaskResult{Success: false, Message: "渲染 HTTPS 配置失败"}
		}

		if sslErr := engine.ApplyNginxConfig(httpsConfig, nginxConfPath, nginxEnabledPath); sslErr != nil {
			rollback()
			log.Printf("应用 HTTPS 配置失败: %v", sslErr)
			return TaskResult{Success: false, Message: "应用 HTTPS 配置失败"}
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
		 VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'v1.0', 'off', ?)`,
		siteName, domain, strings.Join(payload.Aliases, "\n"), systemUser,
		webRoot, logDir, dbName, dbUser, phpPoolPath, nginxConfPath, payload.SiteType, sslEnabled,
		certPath, keyPath, sslExpiry, nilIfEmpty(payload.ExpiresAt),
	)
	if err != nil {
		rollback()
		log.Printf("写入数据库失败: %v", err)
		return TaskResult{Success: false, Message: "写入数据库失败"}
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

	if _, err := executeCommand("userdel", "-r", "-f", site.SystemUser); err != nil {
		fmt.Fprintf(os.Stderr, "删除系统用户警告: %v\n", err)
	}

	os.RemoveAll(site.WebRoot)
	os.RemoveAll(site.LogDir)
	os.RemoveAll(filepath.Join("/var/wp-panel/site-secrets", site.Domain))

	// Clean up logrotate config
	os.Remove("/etc/logrotate.d/wppanel-" + site.Domain)

	_ = dropMariaDBDatabase(site.DBName, site.DBUser, cfg)

	os.Remove(site.PHPPoolPath)
	enabledPath := filepath.Join(cfg.Paths.NginxSitesEnabled, site.Domain+".conf")
	os.Remove(enabledPath)
	os.Remove(site.NginxConfPath)

	exec.Command("nginx", "-s", "reload").Run()
	exec.Command("systemctl", "reload", "php8.3-fpm").Run()

	os.RemoveAll(filepath.Join(cfg.Paths.Certificates, site.Domain))

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

	enabledPath := filepath.Join(cfg.Paths.NginxSitesEnabled, site.Domain+".conf")
	os.Remove(enabledPath)

	if out, err := exec.Command("nginx", "-s", "reload").CombinedOutput(); err != nil {
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

	enabledPath := filepath.Join(cfg.Paths.NginxSitesEnabled, site.Domain+".conf")
	os.Remove(enabledPath)
	if err := os.Symlink(site.NginxConfPath, enabledPath); err != nil {
		log.Printf("创建软链接失败: %v", err)
		return TaskResult{Success: false, Message: "创建软链接失败"}
	}

	if out, err := exec.Command("nginx", "-s", "reload").CombinedOutput(); err != nil {
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
		oldEnabledLink := filepath.Join(cfg.Paths.NginxSitesEnabled, oldDomain+".conf")

		newWebRoot := filepath.Join(cfg.Paths.WWWRoot, newDomain)
		newLogDir := filepath.Join(cfg.Paths.WWWLogs, newDomain)
		newNginxConf := filepath.Join(cfg.Paths.NginxSitesAvailable, newDomain+".conf")
		newPHPPool := filepath.Join(cfg.Paths.PHPFPMPool, newDomain+".conf")
		newCertDir := filepath.Join(cfg.Paths.Certificates, newDomain)
		newEnabledLink := filepath.Join(cfg.Paths.NginxSitesEnabled, newDomain+".conf")
		os.Remove(oldEnabledLink)
		if _, err := os.Stat(newEnabledLink); err == nil {
			os.Remove(newEnabledLink)
		}

		nginxReload := func() { exec.Command("nginx", "-s", "reload").Run() }

		if err := os.Rename(oldNginxConf, newNginxConf); err != nil {
			nginxReload()
			log.Printf("重命名 Nginx 配置文件失败: %v", err)
			return TaskResult{Success: false, Message: "重命名 Nginx 配置文件失败"}
		}
		nginxRB := rollbackStep{"恢复Nginx配置", func() error {
			os.Rename(newNginxConf, oldNginxConf)
			os.Symlink(oldNginxConf, oldEnabledLink)
			nginxReload()
			return nil
		}}
		rollbacks = append(rollbacks, nginxRB)

		oldPoolContent, _ := os.ReadFile(oldPHPPool)
		os.Remove(oldPHPPool)
		engine := NewTemplateEngine(cfg.Panel.BackupDir)
		phpData := &PHPFPMPoolData{
			Domain:     newDomain,
			SystemUser: site.SystemUser,
			WebRoot:    newWebRoot,
			SocketPath: cfg.Paths.PHPFPMSock,
		}
		phpConfig, err := engine.RenderPHPFPMPool(phpData)
		if err != nil {
			rollback()
			log.Printf("渲染 PHP-FPM 配置失败: %v", err)
			return TaskResult{Success: false, Message: "渲染 PHP-FPM 配置失败"}
		}
		if err := engine.ApplyPHPFPMPool(phpConfig, newPHPPool, newLogDir); err != nil {
			rollback()
			log.Printf("应用 PHP-FPM 配置失败: %v", err)
			return TaskResult{Success: false, Message: "应用 PHP-FPM 配置失败"}
		}
		phpRB := rollbackStep{"恢复PHP-FPM Pool " + oldPHPPool, func() error {
			os.Remove(newPHPPool)
			os.WriteFile(oldPHPPool, oldPoolContent, 0644)
			exec.Command("systemctl", "reload", "php8.3-fpm").Run()
			return nil
		}}
		rollbacks = append(rollbacks, phpRB)

		if err := os.Rename(oldWebRoot, newWebRoot); err != nil {
			rollback()
			log.Printf("重命名网站目录失败: %v", err)
			return TaskResult{Success: false, Message: "重命名网站目录失败"}
		}
		rollbacks = append(rollbacks, rollbackStep{"恢复网站目录 " + oldWebRoot, func() error {
			return os.Rename(newWebRoot, oldWebRoot)
		}})

		if err := os.Rename(oldLogDir, newLogDir); err != nil {
			rollback()
			log.Printf("重命名日志目录失败: %v", err)
			return TaskResult{Success: false, Message: "重命名日志目录失败"}
		}
		rollbacks = append(rollbacks, rollbackStep{"恢复日志目录 " + oldLogDir, func() error {
			return os.Rename(newLogDir, oldLogDir)
		}})

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

		// Rename logrotate config if it exists
		oldLogrotate := "/etc/logrotate.d/wppanel-" + oldDomain
		newLogrotate := "/etc/logrotate.d/wppanel-" + newDomain
		if _, err := os.Stat(oldLogrotate); err == nil {
			os.Rename(oldLogrotate, newLogrotate)
		}
		site.PHPPoolPath = newPHPPool
		if site.SSLCertPath != "" {
			site.SSLCertPath = filepath.Join(newCertDir, "fullchain.pem")
			site.SSLKeyPath = filepath.Join(newCertDir, "privkey.pem")
		}

		aliasStr := strings.Join(newAliases, "\n")
		site.Domain = newDomain
		site.Aliases = aliasStr

		nginxData := nginxDataFromSite(site)

		nginxConfig, err := engine.RenderNginxConfig(nginxData)
		if err != nil {
			rollback()
			log.Printf("渲染 Nginx 配置失败: %v", err)
			return TaskResult{Success: false, Message: "渲染 Nginx 配置失败"}
		}

		if err := engine.ApplyNginxConfig(nginxConfig, newNginxConf, newEnabledLink); err != nil {
			rollback()
			log.Printf("应用 Nginx 配置失败: %v", err)
			return TaskResult{Success: false, Message: "应用 Nginx 配置失败"}
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
		if site.SSLEnabled {
			msg += "。请重新申请 SSL 证书以匹配新域名"
		}
		return TaskResult{Success: true, Message: msg}
	}

	aliasStr := strings.Join(newAliases, "\n")
	site.Aliases = aliasStr

	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	nginxData := nginxDataFromSite(site)

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		log.Printf("渲染 Nginx 配置失败: %v", err)
		return TaskResult{Success: false, Message: "渲染 Nginx 配置失败"}
	}

	if err := engine.ApplyNginxConfig(nginxConfig, site.NginxConfPath,
		filepath.Join(cfg.Paths.NginxSitesEnabled, newDomain+".conf")); err != nil {
		log.Printf("应用 Nginx 配置失败: %v", err)
		return TaskResult{Success: false, Message: "应用 Nginx 配置失败"}
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
	os.RemoveAll(webRoot)
	if err := os.MkdirAll(webRoot, 0755); err != nil {
		return fmt.Errorf("重建网站目录失败: %w", err)
	}

	tmpDir := "/tmp/wp_reinstall_" + dbName + "_" + generatePassword(8)
	if err := deployWordPress(packagePath, webRoot, tmpDir); err != nil {
		return fmt.Errorf("WordPress 部署失败: %w", err)
	}

	if err := dropMariaDBDatabase(dbName, dbUser, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "删除旧数据库警告: %v\n", err)
	}

	dbPassword := generatePassword(24)
	if err := createMariaDBDatabase(dbName, dbUser, dbPassword, cfg); err != nil {
		return fmt.Errorf("重建数据库失败: %w", err)
	}

	if err := generateWPConfig(webRoot, dbName, dbUser, dbPassword); err != nil {
		return fmt.Errorf("生成 wp-config.php 失败: %w", err)
	}

	if _, err := executeCommand("chown", "-R", siteOwner(systemUser), webRoot); err != nil {
		fmt.Fprintf(os.Stderr, "设置权限警告: %v\n", err)
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
