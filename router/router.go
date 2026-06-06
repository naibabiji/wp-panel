package router

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/handlers"
	"github.com/naibabiji/wp-panel/middleware"

	"github.com/gin-gonic/gin"
)

var panelVersion string

func SetupRouter(cfg *config.Config, tmplFS embed.FS, staticFS embed.FS, version string) *gin.Engine {
	panelVersion = version
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.SetTrustedProxies(nil)

	r.Use(middleware.CustomRecovery())
	r.Use(middleware.SecurityHeaders())

	db := database.GetDB()
	r.Use(middleware.ScanDefense(db, cfg.Panel.RandomSuffix))

	attemptTracker := middleware.NewLoginAttemptTracker(
		db,
		cfg.Security.MaxLoginAttempts,
		cfg.Security.AttemptWindowMinutes,
		cfg.Security.BanDurationHours,
	)

	basicAuthChecker := &middleware.BasicAuthChecker{
		RecordAttempt: attemptTracker.RecordAttempt,
		IsBanned:      attemptTracker.IsBanned,
	}

	staticPrefix := "/" + cfg.Panel.RandomSuffix + "/assets"
	staticFileSystem, _ := fs.Sub(staticFS, "static")
	r.StaticFS(staticPrefix, http.FS(staticFileSystem))

	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusNotFound, "Not Found")
	})
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	suffix := cfg.Panel.RandomSuffix
	prefix := "/" + suffix

	panelGroup := r.Group(prefix)
	panelGroup.Use(middleware.RandomPath(suffix))
	panelGroup.Use(middleware.BasicAuth(basicAuthChecker))

	// 面板根路径重定向到登录页（解决用户访问面板地址不带 /login 的问题）
	panelGroup.GET("", func(c *gin.Context) {
		if c.Request.URL.Path == "/"+suffix {
			c.Redirect(http.StatusFound, "/"+suffix+"/login")
			return
		}
		c.Next()
	})

	panelGroup.GET("/login", func(c *gin.Context) {
		middleware.SetCSRFToken(c)
		csrfToken := middleware.GetCSRFToken(c)
		c.HTML(http.StatusOK, "login.html", gin.H{
			"Title":        "登录",
			"PanelTitle":   handlers.GetPanelTitle(),
			"RandomSuffix": suffix,
			"Active":       "login",
			"AssetPrefix":  prefix + "/assets",
			"CSRFToken":    csrfToken,
		})
	})

	panelGroup.POST("/api/auth/login", func(c *gin.Context) {
		authHandler := &handlers.AuthHandler{DB: db, Prefix: suffix, Tracker: attemptTracker}
		authHandler.Login(c)
	})

	cacheHelper := &handlers.CacheHelperHandler{}

	pluginGroup := r.Group(prefix)
	pluginGroup.Use(middleware.RandomPath(suffix))
	pluginGroup.GET("/api/sites/find", cacheHelper.FindByDomain)
	pluginGroup.DELETE("/api/sites/clear-cache", cacheHelper.ClearByDomain)
	pluginGroup.PUT("/api/sites/cache-settings", cacheHelper.UpdateCacheSettings)
	pluginGroup.PUT("/api/sites/optimizer-settings", cacheHelper.UpdateOptimizerSettings)

	protected := panelGroup.Group("")
	protected.Use(middleware.SessionRequired())
	protected.Use(func(c *gin.Context) {
		middleware.SetCSRFToken(c)
		c.Next()
	})
	protected.Use(middleware.CSRF())

	authHandler := &handlers.AuthHandler{DB: db, Prefix: suffix, Tracker: attemptTracker}
	protected.POST("/api/auth/logout", authHandler.Logout)
	protected.GET("/api/auth/check", authHandler.Check)
	protected.GET("/api/auth/csrf-token", authHandler.CSRFToken)

	websiteHandler := &handlers.WebsiteHandler{DB: db}
	protected.GET("/api/websites", websiteHandler.List)
	protected.POST("/api/websites", websiteHandler.Create)
	protected.GET("/api/websites/:id", websiteHandler.Get)
	protected.DELETE("/api/websites/:id", websiteHandler.Delete)
	protected.PATCH("/api/websites/:id/status", websiteHandler.ToggleStatus)
	protected.POST("/api/websites/:id/ssl", websiteHandler.EnableSSL)
	protected.DELETE("/api/websites/:id/ssl", websiteHandler.RemoveSSL)
	protected.PUT("/api/websites/:id/db-password", websiteHandler.ChangeDBPassword)
	protected.POST("/api/websites/:id/fix-wp-config", websiteHandler.FixWPConfig)
	protected.GET("/api/websites/:id/detect-table-prefix", websiteHandler.DetectDBTablePrefix)
	protected.GET("/api/websites/:id/wp-site-urls", websiteHandler.GetWPSiteURLs)
	protected.PUT("/api/websites/:id/wp-site-urls", websiteHandler.UpdateWPSiteURLs)
	protected.GET("/api/websites/:id/logs", websiteHandler.ViewLogs)
	protected.DELETE("/api/websites/:id/logs", websiteHandler.ClearLogs)
	protected.PUT("/api/websites/:id/domains", websiteHandler.UpdateDomains)
	protected.PUT("/api/websites/:id/cache", websiteHandler.UpdateCache)
	protected.DELETE("/api/websites/:id/cache", websiteHandler.ClearCache)
	protected.PUT("/api/websites/:id/wp-optimizations", websiteHandler.SaveWPOptimizations)
	protected.PUT("/api/websites/:id/monitoring", websiteHandler.SaveMonitoring)
	protected.POST("/api/websites/:id/install-plugin", websiteHandler.InstallPlugin)
	protected.GET("/api/websites/:id/install-plugin/status", websiteHandler.InstallPluginStatus)
	protected.POST("/api/websites/:id/reinstall-wp", websiteHandler.ReinstallWordPress)
	protected.GET("/api/websites/:id/nginx-custom", websiteHandler.GetNginxCustom)
	protected.PUT("/api/websites/:id/nginx-custom", websiteHandler.SaveNginxCustom)
	protected.PUT("/api/websites/:id/access-log", websiteHandler.SetAccessLogMode)
	protected.PUT("/api/websites/:id/log-retention", websiteHandler.SetLogRetention)
	protected.PUT("/api/websites/:id/expiry", websiteHandler.UpdateExpiry)
	backupHandler := &handlers.BackupHandler{}
	protected.GET("/api/websites/:id/backups", backupHandler.List)
	protected.POST("/api/websites/:id/backups", backupHandler.Create)
	protected.DELETE("/api/websites/:id/backups/:bid", backupHandler.Delete)
	protected.GET("/api/websites/:id/backups/:bid/download", backupHandler.Download)
	protected.POST("/api/websites/:id/backups/:bid/restore", backupHandler.Restore)
	protected.POST("/api/websites/:id/backups/upload-restore", backupHandler.UploadRestore)
	protected.GET("/api/websites/:id/backups/settings", backupHandler.GetSettings)
	protected.PUT("/api/websites/:id/backups/settings", backupHandler.UpdateSettings)
	protected.POST("/api/websites/:id/backups/clear-database", backupHandler.ClearDatabase)

	dashboardHandler := &handlers.DashboardHandler{}
	protected.GET("/api/dashboard/stats", dashboardHandler.GetStats)
	protected.GET("/api/dashboard/metrics", dashboardHandler.GetMetrics)
	protected.GET("/api/dashboard/site-resources", dashboardHandler.GetSiteResources)
	protected.GET("/api/announcement", handlers.GetAnnouncement)

	firewallHandler := &handlers.FirewallHandler{}
	protected.GET("/api/firewall/bans", firewallHandler.ListBans)
	protected.POST("/api/firewall/bans", firewallHandler.ManualBan)
	protected.DELETE("/api/firewall/bans/:id", firewallHandler.Unban)
	protected.POST("/api/firewall/bans/:id/permanent", firewallHandler.PermanentBan)

	securityHandler := &handlers.SecurityHandler{}
	protected.GET("/api/security/settings", securityHandler.GetSettings)
	protected.PUT("/api/security/settings", securityHandler.UpdateSettings)
	protected.POST("/api/security/whitelist/refresh", securityHandler.RefreshWhitelist)

	alertHandler := &handlers.AlertHandler{}
	protected.GET("/api/alert/settings", alertHandler.GetSettings)
	protected.PUT("/api/alert/settings", alertHandler.SaveSettings)
	protected.POST("/api/alert/test-smtp", alertHandler.TestSMTP)
	protected.POST("/api/alert/test-webhook", alertHandler.TestWebhook)
	protected.GET("/api/alert/log", alertHandler.GetLog)

	cronHandler := &handlers.CronHandler{}
	protected.GET("/api/cron", cronHandler.List)
	protected.POST("/api/cron", cronHandler.Create)
	protected.PUT("/api/cron/:id", cronHandler.Update)
	protected.DELETE("/api/cron/:id", cronHandler.Delete)
	protected.POST("/api/cron/:id/run", cronHandler.Run)
	protected.GET("/api/cron/system", cronHandler.SystemList)
	protected.GET("/api/cron/logs", cronHandler.ViewLogs)

	fileHandler := &handlers.FileHandler{}
	protected.GET("/api/files/list", fileHandler.List)
	protected.POST("/api/files/upload", fileHandler.Upload)
	protected.POST("/api/files/upload/init", fileHandler.UploadInit)
	protected.POST("/api/files/upload/chunk", fileHandler.UploadChunk)
	protected.POST("/api/files/upload/complete", fileHandler.UploadComplete)
	protected.GET("/api/files/download", fileHandler.Download)
	protected.DELETE("/api/files/delete", fileHandler.Delete)
	protected.PUT("/api/files/rename", fileHandler.Rename)
	protected.GET("/api/files/permissions", fileHandler.Permissions)
	protected.POST("/api/files/batch-zip", fileHandler.BatchCompress)
	protected.POST("/api/files/move", fileHandler.Move)
	protected.POST("/api/files/copy", fileHandler.Copy)
	protected.POST("/api/files/zip", fileHandler.Compress)
	protected.POST("/api/files/unzip", fileHandler.Decompress)
	protected.POST("/api/files/mkdir", fileHandler.CreateDir)
	protected.POST("/api/files/fix-permissions", fileHandler.FixPermissions)

	settingsHandler := &handlers.SettingsHandler{}
	protected.GET("/api/settings", settingsHandler.GetSettings)
	protected.PUT("/api/settings", settingsHandler.UpdateSettings)
	protected.GET("/api/settings/logs", settingsHandler.GetOperationLogs)
	protected.GET("/api/settings/wp-package", settingsHandler.GetWPPackage)
	protected.POST("/api/settings/wp-package/upload", settingsHandler.UploadWPPackage)
	protected.POST("/api/settings/wp-package/download", settingsHandler.DownloadWPPackage)
	protected.DELETE("/api/settings/wp-package", settingsHandler.DeleteWPPackage)
	protected.GET("/api/settings/remote-backup", handlers.GetRemoteBackup)
	protected.PUT("/api/settings/remote-backup", handlers.SaveRemoteBackup)
	protected.POST("/api/settings/remote-backup/test", handlers.TestRemoteBackup)
	protected.GET("/api/settings/db-backup", settingsHandler.GetDBBackups)
	protected.POST("/api/settings/db-backup", settingsHandler.CreateDBBackup)
	protected.POST("/api/settings/db-backup/restore", settingsHandler.RestoreDBBackup)
	protected.DELETE("/api/settings/db-backup", settingsHandler.DeleteDBBackup)
		protected.GET("/api/proxy/test", settingsHandler.TestProxy)

	extensionHandler := &handlers.ExtensionHandler{}
	protected.GET("/api/extensions", extensionHandler.List)
	protected.PUT("/api/extensions", extensionHandler.Save)
	protected.DELETE("/api/extensions/:id", extensionHandler.Delete)
	protected.POST("/api/extensions/reset", extensionHandler.Reset)

	protected.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "dashboard.html", pageData(suffix, "dashboard", "dashboard_content", c))
	})
	protected.GET("/websites", func(c *gin.Context) {
		c.HTML(http.StatusOK, "websites.html", pageData(suffix, "websites", "websites_content", c))
	})
	protected.GET("/websites/new", func(c *gin.Context) {
		c.HTML(http.StatusOK, "website_new.html", pageData(suffix, "websites", "websites_new_content", c))
	})
	protected.GET("/websites/:id", func(c *gin.Context) {
		c.HTML(http.StatusOK, "website_detail.html", pageData(suffix, "websites", "websites_detail_content", c))
	})
	protected.GET("/cron", func(c *gin.Context) {
		c.HTML(http.StatusOK, "cron.html", pageData(suffix, "cron", "cron_content", c))
	})
	protected.GET("/firewall", func(c *gin.Context) {
		c.HTML(http.StatusOK, "firewall.html", pageData(suffix, "firewall", "firewall_content", c))
	})
	protected.GET("/files", func(c *gin.Context) {
		c.HTML(http.StatusOK, "files.html", pageData(suffix, "files", "files_content", c))
	})
	protected.GET("/security", func(c *gin.Context) {
		c.HTML(http.StatusOK, "security.html", pageData(suffix, "security", "security_content", c))
	})
	protected.GET("/alert", func(c *gin.Context) {
		c.HTML(http.StatusOK, "alert.html", pageData(suffix, "alert", "alert_content", c))
	})
	protected.GET("/extensions", func(c *gin.Context) {
		c.HTML(http.StatusOK, "extension.html", pageData(suffix, "extensions", "extensions_content", c))
	})
	protected.GET("/settings", func(c *gin.Context) {
		c.HTML(http.StatusOK, "settings.html", pageData(suffix, "settings", "settings_content", c))
	})

	softwareHandler := &handlers.SoftwareHandler{}
	protected.GET("/software", func(c *gin.Context) {
		c.HTML(http.StatusOK, "software.html", pageData(suffix, "software", "software_content", c))
	})
	protected.GET("/api/software", softwareHandler.List)
	protected.GET("/api/software/guard", softwareHandler.GetGuardStatus)
	protected.POST("/api/software/guard/action", softwareHandler.GuardAction)
	protected.PUT("/api/software/config", softwareHandler.SaveConfig)
	protected.GET("/api/software/log", softwareHandler.ViewLog)
	protected.DELETE("/api/software/log", softwareHandler.ClearLog)
	updateHandler := &handlers.UpdateHandler{CurrentVersion: version}
	protected.GET("/api/update/check", updateHandler.Check)
	protected.POST("/api/update/do", updateHandler.Update)
		sysUpdateHandler := &handlers.SystemUpdateHandler{}
		protected.GET("/api/system/updates", sysUpdateHandler.Check)
		protected.POST("/api/system/updates/do", sysUpdateHandler.Update)

	tmpl := template.Must(template.New("").ParseFS(tmplFS, "templates/*.html"))
	r.SetHTMLTemplate(tmpl)

	return r
}

var pageTitles = map[string]string{
	"dashboard":  "控制台",
	"websites":   "网站管理",
	"cron":       "计划任务",
	"firewall":   "安全防御",
	"security":   "安全设置",
	"files":      "文件管理",
	"software":   "软件管理",
	"alert":      "告警通知",
	"extensions": "扩展配置",
	"settings":   "面板设置",
}

func pageData(suffix string, active string, contentTpl string, c *gin.Context) gin.H {
	csrfToken := middleware.GetCSRFToken(c)
	title := pageTitles[active]
	return gin.H{
		"Title":           title,
		"PanelTitle":      handlers.GetPanelTitle(),
		"PanelVersion":    panelVersion,
		"ContentTemplate": contentTpl,
		"RandomSuffix":    suffix,
		"Active":          active,
		"AssetPrefix":     "/" + suffix + "/assets",
		"CSRFToken":       csrfToken,
	}
}
