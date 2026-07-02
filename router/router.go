package router

import (
	"embed"
	"html/template"
	"io/fs"
	"net"
	"net/http"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/handlers"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/middleware"

	"github.com/gin-gonic/gin"
)

var panelVersion string

var i18nKeys = []string{
	"auth.connect_failed",
	"auth.login_failed",
	"auth.missing_credentials",
	"auth.session_expired",
	"common.cancel",
	"common.confirm",
	"common.none",
	"common.operation_success",
	"common.save",
	"common.network_error",
	"common.operation_failed",
	"common.request_cancelled",
	"common.request_failed",
	"common.request_timeout",
	"common.saving",
	"common.service_busy",
	"common.service_exception",
	"dashboard.chart_load",
	"dashboard.chart_memory",
	"dashboard.system_updates",
	"dashboard.tooltip_time",
	"dashboard.update_available",
	"files.root_directory",
	"settings.account_saved_please_relogin",
	"settings.ai_settings_saved",
	"settings.api_key_placeholder",
	"settings.auto_update_saved",
	"settings.available_count",
	"settings.backing_up",
	"settings.backup_finished",
	"settings.backup_now",
	"settings.basic_auth_password_min_length",
	"settings.check_updates",
	"settings.checking",
	"settings.command_copied",
	"settings.command_copied_short",
	"settings.confirm_delete_backup",
	"settings.confirm_delete_local_wp_package",
	"settings.confirm_restore_db_backup",
	"settings.confirm_system_update",
	"settings.confirm_update_version",
	"settings.connection_failed",
	"settings.connection_failed_with_reason",
	"settings.connection_ok",
	"settings.connection_ok_with_latency",
	"settings.current_password_required",
	"settings.delete",
	"settings.deleted",
	"settings.deleting",
	"settings.disabled",
	"settings.downloaded",
	"settings.downloading",
	"settings.downloading_update_package",
	"settings.enabled",
	"settings.failed",
	"settings.new_password_min_length",
	"settings.no_changes",
	"settings.no_data",
	"settings.ntp_not_synced",
	"settings.ntp_synced",
	"settings.one_click_update",
	"settings.online_download",
	"settings.package_download_complete",
	"settings.package_not_installed",
	"settings.package_ready",
	"settings.panel_restarting",
	"settings.passwords_not_match",
	"settings.preparing_update",
	"settings.proxy_required",
	"settings.refresh",
	"settings.restoring",
	"settings.save_account_settings",
	"settings.save_ai_settings",
	"settings.save_failed",
	"settings.save_settings",
	"settings.saved",
	"settings.saving",
	"settings.success",
	"settings.system_update_completed",
	"settings.system_update_failed",
	"settings.test",
	"settings.test_connection",
	"settings.test_failed",
	"settings.testing",
	"settings.time_sync_triggered",
	"settings.total_records",
	"settings.unknown_error",
	"settings.up_to_date_message",
	"settings.update_completed_refresh",
	"settings.update_completed_restart",
	"settings.update_now",
	"settings.update_started",
	"settings.update_status_timeout",
	"settings.updated",
	"settings.updating",
	"settings.upload_failed",
	"settings.upload_package",
	"settings.upload_success",
	"settings.uploading",
	"settings.zip_only",
	"alert.saving",
	"alert.send_failed",
	"alert.sending",
	"alert.smtp_config_saved",
	"alert.test_send",
	"alert.type_backup_failed",
	"alert.type_cpu_high_load",
	"alert.type_cron_failed",
	"alert.type_disk_pressure",
	"alert.type_memory_low",
	"alert.type_panel_update",
	"alert.type_remote_backup_failed",
	"alert.type_service_abnormal",
	"alert.type_site_unavailable",
	"alert.type_ssl_expire",
	"alert.type_system_update",
	"alert.type_website_expiry",
	"alert.webhook_config_saved",
	"alert.webhook_url_required",
	"cron.confirm_delete",
	"cron.deleted_success",
	"cron.job_created",
	"cron.job_run_success",
	"cron.job_running",
	"cron.job_updated",
	"cron.load_failed",
	"cron.load_failed_with_error",
	"cron.run",
	"cron.full_backup",
	"cron.smart_incremental",
	"cron.select_target_site",
	"cron.status_disabled",
	"cron.status_enabled",
	"cron.task_type_command",
	"cron.task_type_file_backup",
	"website.status_paused",
	"website.status_running",
	"website.generic_php_site",
	"website.auto_detect",
	"website.detecting",
	"website.backup_auto",
	"website.backup_manual",
	"website.backup_list",
	"website.save_other_cdn_settings",
	"website.save_settings",
	"website.restore",
	"website.processing",
	"website.sync_db_info",
	"website.clear_database",
	"website.clearing",
	"website.backing_up",
	"website.manual_backup",
	"website.restoring",
	"website.upload_restore",
	"website.unknown",
	"website.save_apply",
	"website.enable_lock",
	"website.unlock",
	"website.file_locked",
	"website.file_unlocked",
	"website.installing",
	"website.install_companion_plugin",
	"website.updating",
	"website.update_companion_plugin",
	"website.ssl_enabled",
	"website.ssl_not_enabled",
	"website.ssl_pending",
	"website.monitoring_enabled",
	"website.monitoring_disabled",
	"website.access_log_full",
	"website.access_log_error_only",
	"website.access_log_off",
	"website.enabled",
	"website.disabled",
	"website.never_expires",
	"website.delete_confirm",
	"website.delete_success",
	"website.site_pause",
	"website.site_enable",
	"website.reinstall",
	"website.reinstalling",
	"website.reinstalling_with_domain",
	"website.reinstall_completed",
	"ai_diagnostics.analyzing",
	"ai_diagnostics.chat_role_ai",
	"ai_diagnostics.chat_role_user",
	"ai_diagnostics.collapse_details",
	"ai_diagnostics.confidence_prefix",
	"ai_diagnostics.diagnosing",
	"ai_diagnostics.diagnosis_completed",
	"ai_diagnostics.diagnosis_failed",
	"ai_diagnostics.diagnosis_running",
	"ai_diagnostics.expand_details",
	"ai_diagnostics.followup_failed",
	"ai_diagnostics.followup_running",
	"ai_diagnostics.history_summary",
	"ai_diagnostics.reply_ready",
	"ai_diagnostics.report_title",
	"ai_diagnostics.select_site_for_history",
	"ai_diagnostics.risk_high",
	"ai_diagnostics.risk_low",
	"ai_diagnostics.risk_medium",
	"ai_diagnostics.risk_text_high",
	"ai_diagnostics.risk_text_low",
	"ai_diagnostics.risk_text_medium",
	"ai_diagnostics.risk_unknown",
	"ai_diagnostics.select_site_to_start",
	"ai_diagnostics.send_to_ai",
	"ai_diagnostics.start_diagnosis_button",
	"ai_diagnostics.status_completed",
	"ai_diagnostics.status_failed",
	"ai_diagnostics.status_running",
	"ai_diagnostics.status_waiting",
	"ai_diagnostics.symptom_cache_issue",
	"ai_diagnostics.symptom_db_connection",
	"ai_diagnostics.symptom_performance",
	"ai_diagnostics.symptom_site_500",
	"ai_diagnostics.symptom_ssl_failure",
	"ai_diagnostics.symptom_wp_admin_down",
	"ai_diagnostics.waiting",
	"ai_diagnostics.waiting_ai_request",
	"ai_diagnostics.waiting_analysis",
	"ai_diagnostics.waiting_collect",
	"ai_diagnostics.waiting_long",
	"security.cdn_mode_cloudflare_auto",
	"security.cdn_mode_compatible_missing_origin_ips",
	"security.cdn_mode_compatible_no_origin_ips",
	"security.confirm_delete_cdn_group",
	"security.fetch_cdn_groups_failed",
	"security.got_it",
	"security.help_title",
	"security.last_update_label",
	"security.refresh_triggered",
	"security.telemetry_disable_confirm",
	"security.telemetry_disabled",
	"security.telemetry_enabled",
	"security.cdn_mode_strict_trusted_ips",
	"common.saved",
	"extension.reset_confirm",
	"extension.restored_default",
	"extension.saved",
	"files.chunk_upload_failed",
	"files.clipboard_copy",
	"files.clipboard_cut",
	"files.clipboard_label",
	"files.compress_completed",
	"files.compress_directory_title",
	"files.compress_file_title",
	"files.compression_busy",
	"files.confirm_delete_items",
	"files.confirm_extract",
	"files.confirm_fix_permissions",
	"files.confirm_overwrite_existing",
	"files.copied_to_clipboard",
	"files.current_selection",
	"files.cut_to_clipboard",
	"files.decompression_busy",
	"files.delete_completed",
	"files.delete_failed",
	"files.deleted_item",
	"files.existing_items_conflict",
	"files.extract_completed",
	"files.extract_overwrite_conflict",
	"files.file_type_dir",
	"files.file_type_file",
	"files.item_count",
	"files.mkdir_success",
	"files.pagination_summary",
	"files.paste_target_required",
	"files.permissions_fixed",
	"files.prompt_archive_name",
	"files.remote_import_completed",
	"files.remote_import_failed",
	"files.remote_import_in_progress",
	"files.remote_import_preparing",
	"files.remote_import_start",
	"files.remote_import_url_placeholder",
	"files.rename_success",
	"files.skip_existing_prompt",
	"files.unknown_size",
	"files.upload",
	"files.upload_completed",
	"files.upload_failed_with_error",
	"files.upload_init_failed",
	"files.upload_merge_failed",
	"files.uploading",
	"firewall.added_to_blacklist",
	"firewall.analyzing",
	"firewall.ban",
	"firewall.ban_level_10m",
	"firewall.ban_level_24h",
	"firewall.ban_level_30d",
	"firewall.ban_level_empty",
	"firewall.ban_level_permanent",
	"firewall.ban_level_rate_limit",
	"firewall.ban_success",
	"firewall.banning",
	"firewall.clear_rescan",
	"firewall.confirm_clear_history",
	"firewall.confirm_permanent_ban",
	"firewall.confirm_unban",
	"firewall.copy_failed_manual",
	"firewall.copy_line_count",
	"firewall.copy_line_ip",
	"firewall.copy_line_last_seen",
	"firewall.copy_line_note",
	"firewall.copy_line_path",
	"firewall.copy_line_risk",
	"firewall.copy_line_site",
	"firewall.copy_line_source",
	"firewall.copy_line_status",
	"firewall.copy_line_type",
	"firewall.enter_ip",
	"firewall.event_runtime_php_access",
	"firewall.event_suspicious_php_file",
	"firewall.file_event_copied",
	"firewall.file_security_summary",
	"firewall.found",
	"firewall.incremental_refresh",
	"firewall.ip_filled_notice",
	"firewall.load_wp_report_failed",
	"firewall.permanent",
	"firewall.refresh_analysis",
	"firewall.refresh_file_security_failed",
	"firewall.refreshing",
	"firewall.report_copied",
	"firewall.risk_high",
	"firewall.risk_low",
	"firewall.risk_medium",
	"firewall.scanning",
	"firewall.source_404",
	"firewall.source_manual",
	"firewall.source_nftables",
	"firewall.source_nginx",
	"firewall.source_panel",
	"firewall.source_scan",
	"firewall.source_scanner",
	"firewall.source_ssh",
	"firewall.source_web_protect",
	"firewall.status_current_risk",
	"firewall.status_handled",
	"firewall.total_records",
	"firewall.unbanned",
	"firewall.unknown",
	"name",
	"overwrite",
	"site_id",
	"size",
	"skip",
	"symptom",
	"textarea",
	"time",
	"type",
	"website.at_least_one_url",
	"website.backup_completed",
	"website.backup_settings_saved",
	"website.cache_cleared",
	"website.cdn_compatible_no_origin_ips",
	"website.cdn_header_mismatch",
	"website.cdn_realip_saved",
	"website.cdn_select_non_cloudflare_help",
	"website.cdn_select_non_cloudflare_required",
	"website.cdn_strict_origin_ips",
	"website.confirm_change",
	"website.confirm_clear_cache",
	"website.confirm_clear_database",
	"website.confirm_clear_logs",
	"website.confirm_continue",
	"website.confirm_delete_backup",
	"website.confirm_delete_ssl",
	"website.confirm_restore_backup",
	"website.confirm_update",
	"website.confirm_update_site_urls",
	"website.database_cleared",
	"website.deleted",
	"website.detect_failed",
	"website.detect_multiple_prefixes",
	"website.detect_prefix_found",
	"website.detect_prefix_missing",
	"website.document_root_saved",
	"website.domain_required",
	"website.expiry_saved",
	"website.file_lock_disable_confirm",
	"website.file_lock_disabled",
	"website.file_lock_enable_confirm",
	"website.file_lock_enabled",
	"website.installed",
	"website.load_current_url_failed",
	"website.load_failed_with_error",
	"website.load_log_files_failed",
	"website.load_logs_failed",
	"website.log_empty_or_missing",
	"website.log_type_access",
	"website.log_type_error",
	"website.log_type_security",
	"website.logs_cleared",
	"website.monitoring_saved",
	"website.new_password_placeholder",
	"website.operation_failed_with_error",
	"website.optimization_saved",
	"website.password_updated",
	"website.processing_update",
	"website.reading",
	"website.save_failed",
	"website.save_failed_with_error",
	"website.site_urls_updated",
	"website.ssl_deleted",
	"website.ssl_export_saved",
	"website.ssl_manual_required",
	"website.sync_wp_config_base",
	"website.sync_wp_config_prefix",
	"website.unchanged",
	"website.update_failed",
	"website.update_failed_with_error",
	"website.updated",

	"2006-01-02",
	"20060102",
	"20060102_150405",
	"2d",
	"Cache-Control",
	"Content-Disposition",
	"Content-Type",
	"ai_diagnostics.already_running",
	"ai_diagnostics.already_running_followup",
	"ai_diagnostics.api_key_required",
	"ai_diagnostics.build_context_failed",
	"ai_diagnostics.collect_context_failed",
	"ai_diagnostics.create_session_failed",
	"ai_diagnostics.followup_required",
	"ai_diagnostics.followup_too_long",
	"ai_diagnostics.followup_wait",
	"ai_diagnostics.invalid_session_id",
	"ai_diagnostics.invalid_symptom",
	"ai_diagnostics.load_context_failed",
	"ai_diagnostics.load_messages_failed",
	"ai_diagnostics.load_sessions_failed",
	"ai_diagnostics.not_enabled",
	"ai_diagnostics.result_ready",
	"ai_diagnostics.save_followup_failed",
	"ai_diagnostics.save_reply_failed",
	"ai_diagnostics.save_result_failed",
	"ai_diagnostics.session_interrupted",
	"ai_diagnostics.session_not_found",
	"ai_diagnostics.user_error_bad_response",
	"ai_diagnostics.user_error_empty_response",
	"ai_diagnostics.user_error_network",
	"ai_diagnostics.user_error_rate_limited",
	"ai_diagnostics.user_error_timeout",
	"ai_diagnostics.user_error_unauthorized",
	"ai_settings.invalid_provider",
	"ai_settings.load_failed",
	"ai_settings.save_failed",
	"auth.invalid_credentials",
	"auth.missing_csrf",
	"auth.not_logged_in",
	"auth.provide_credentials",
	"common.invalid_params",
	"extension.deleted",
	"extension.query_failed",
	"files.path_out_of_bounds",
	"files.remote_import_chmod_failed",
	"files.remote_import_completed_fix_permissions",
	"files.remote_import_create_file_failed",
	"files.remote_import_disk_full",
	"files.remote_import_disk_space_low",
	"files.remote_import_downloading",
	"files.remote_import_failed_with_error",
	"files.remote_import_filename_required",
	"files.remote_import_host_invalid",
	"files.remote_import_host_local",
	"files.remote_import_host_private",
	"files.remote_import_host_resolve_failed",
	"files.remote_import_https_only",
	"files.remote_import_read_failed",
	"files.remote_import_rename_failed",
	"files.remote_import_request_failed",
	"files.remote_import_save_failed",
	"files.remote_import_status_code",
	"files.remote_import_task_missing",
	"files.remote_import_too_large",
	"files.remote_import_too_many_redirects",
	"files.remote_import_url_invalid",
	"files.remote_import_url_no_userinfo",
	"files.remote_import_url_required",
	"files.remote_import_waiting",
	"files.select_website_first",
	"files.target_directory_missing",
	"files.target_is_directory",
	"session_username",
	"software.action_success",
	"software.clear_failed",
	"software.client_max_body_size_hint",
	"software.client_max_body_size_label",
	"software.config_not_found",
	"software.config_updated_reloaded",
	"software.create_php_config_failed",
	"software.innodb_buffer_pool_size_hint",
	"software.innodb_buffer_pool_size_label",
	"software.installed",
	"software.invalid_action",
	"software.log_cleared",
	"software.log_empty_or_unreadable",
	"software.max_execution_time_hint",
	"software.max_execution_time_label",
	"software.max_input_time_hint",
	"software.max_input_time_label",
	"software.max_input_vars_hint",
	"software.max_input_vars_label",
	"software.maxmemory_hint",
	"software.maxmemory_label",
	"software.memory_limit_hint",
	"software.memory_limit_label",
	"software.nginx_value_no_semicolon",
	"software.operation_failed_with_error",
	"software.php_installed_extensions",
	"software.php_int_invalid",
	"software.php_pool_rebuild_failed",
	"software.php_size_invalid",
	"software.post_max_size_hint",
	"software.post_max_size_label",
	"software.read_config_failed",
	"software.running",
	"software.stopped",
	"software.syntax_check_failed_with_rollback",
	"software.unknown_software",
	"software.unsupported_config_item",
	"software.upload_max_filesize_hint",
	"software.upload_max_filesize_label",
	"software.value_no_newline",
	"software.write_config_failed",
	"website.invalid_site_id",
	"website.not_found",
	"website.restore_failed",
	"website.restore_file_invalid",
	"website.restore_started",
	"website.restore_still_running",
	"website.restore_success",
	"website.restore_task_failed",
}

func SetupRouter(cfg *config.Config, tmplFS embed.FS, staticFS embed.FS, version string, configPath string) *gin.Engine {
	panelVersion = version
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.SetTrustedProxies(nil)

	r.Use(middleware.CustomRecovery())
	r.Use(middleware.SecurityHeaders())

	// /healthz 必须在 ScanDefense 之前注册，否则本机健康检查会被扫描防御误封
	r.GET("/healthz", func(c *gin.Context) {
		ip := net.ParseIP(c.ClientIP())
		if ip == nil || !ip.IsLoopback() {
			c.Status(http.StatusNotFound)
			return
		}
		db := database.GetDB()
		if db == nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		var version string
		if err := db.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil || version == "" {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		for _, table := range []string{"admin_users", "websites", "security_settings"} {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM " + table + " LIMIT 1").Scan(&count); err != nil {
				c.Status(http.StatusServiceUnavailable)
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

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
		i18n.MaybeSetLanguageCookie(c.Writer, c.Request)
		lang := i18n.LangFromRequest(c.Request)
		middleware.SetCSRFToken(c)
		csrfToken := middleware.GetCSRFToken(c)
		c.HTML(http.StatusOK, "login.html", gin.H{
			"Title":        i18n.T(lang, "auth.login"),
			"PanelTitle":   handlers.GetPanelTitle(),
			"PanelVersion": version,
			"AssetVersion": version,
			"RandomSuffix": suffix,
			"Active":       "login",
			"AssetPrefix":  prefix + "/assets",
			"CSRFToken":    csrfToken,
			"Lang":         lang,
			"MessagesJSON": i18n.MessagesJSON(lang, i18nKeys),
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
	pluginGroup.GET("/api/sites/ssl/export", cacheHelper.ExportSSLCertificate)
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
	protected.POST("/api/websites/ssl-preflight", websiteHandler.SSLPreflight)
	protected.GET("/api/websites/:id", websiteHandler.Get)
	protected.DELETE("/api/websites/:id", websiteHandler.Delete)
	protected.PATCH("/api/websites/:id/status", websiteHandler.ToggleStatus)
	protected.POST("/api/websites/:id/ssl", websiteHandler.EnableSSL)
	protected.GET("/api/websites/:id/ssl/download", websiteHandler.DownloadSSLPackage)
	protected.PUT("/api/websites/:id/ssl/export", websiteHandler.SetSSLExport)
	protected.DELETE("/api/websites/:id/ssl", websiteHandler.RemoveSSL)
	protected.PUT("/api/websites/:id/db-password", websiteHandler.ChangeDBPassword)
	protected.POST("/api/websites/:id/fix-wp-config", websiteHandler.FixWPConfig)
	protected.GET("/api/websites/:id/detect-table-prefix", websiteHandler.DetectDBTablePrefix)
	protected.GET("/api/websites/:id/wp-site-urls", websiteHandler.GetWPSiteURLs)
	protected.PUT("/api/websites/:id/wp-site-urls", websiteHandler.UpdateWPSiteURLs)
	protected.GET("/api/websites/:id/logs", websiteHandler.ViewLogs)
	protected.GET("/api/websites/:id/log-files", websiteHandler.ListLogFiles)
	protected.GET("/api/websites/:id/logs/download", websiteHandler.DownloadLogFile)
	protected.DELETE("/api/websites/:id/logs", websiteHandler.ClearLogs)
	protected.PUT("/api/websites/:id/domains", websiteHandler.UpdateDomains)
	protected.PUT("/api/websites/:id/cache", websiteHandler.UpdateCache)
	protected.DELETE("/api/websites/:id/cache", websiteHandler.ClearCache)
	protected.PUT("/api/websites/:id/wp-optimizations", websiteHandler.SaveWPOptimizations)
	protected.PUT("/api/websites/:id/file-lock", websiteHandler.SetFileLock)
	protected.PUT("/api/websites/:id/monitoring", websiteHandler.SaveMonitoring)
	protected.POST("/api/websites/:id/install-plugin", websiteHandler.InstallPlugin)
	protected.GET("/api/websites/:id/install-plugin/status", websiteHandler.InstallPluginStatus)
	protected.POST("/api/websites/:id/reinstall-wp", websiteHandler.ReinstallWordPress)
	protected.GET("/api/websites/:id/nginx-custom", websiteHandler.GetNginxCustom)
	protected.PUT("/api/websites/:id/nginx-custom", websiteHandler.SaveNginxCustom)
	protected.PUT("/api/websites/:id/access-log", websiteHandler.SetAccessLogMode)
	protected.PUT("/api/websites/:id/document-root", websiteHandler.SetDocumentRoot)
	protected.PUT("/api/websites/:id/cdn-realip", websiteHandler.SetCDNRealIP)
	protected.PUT("/api/websites/:id/log-retention", websiteHandler.SetLogRetention)
	protected.PUT("/api/websites/:id/expiry", websiteHandler.UpdateExpiry)
	backupHandler := &handlers.BackupHandler{}
	protected.GET("/api/websites/:id/backups", backupHandler.List)
	protected.POST("/api/websites/:id/backups", backupHandler.Create)
	protected.DELETE("/api/websites/:id/backups/:bid", backupHandler.Delete)
	protected.GET("/api/websites/:id/backups/:bid/download", backupHandler.Download)
	protected.POST("/api/websites/:id/backups/:bid/restore", backupHandler.Restore)
	protected.POST("/api/websites/:id/backups/upload-restore", backupHandler.UploadRestore)
	protected.GET("/api/websites/:id/backups/restore-tasks/:task_id", backupHandler.RestoreStatus)
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
	protected.GET("/api/firewall/wp-security-report", firewallHandler.WPSecurityReport)
	protected.GET("/api/firewall/file-security-events", firewallHandler.ListFileSecurityEvents)
	protected.POST("/api/firewall/file-security-events/refresh", firewallHandler.RefreshFileSecurityEvents)
	protected.POST("/api/firewall/bans", firewallHandler.ManualBan)
	protected.DELETE("/api/firewall/bans/:id", firewallHandler.Unban)
	protected.POST("/api/firewall/bans/:id/permanent", firewallHandler.PermanentBan)

	securityHandler := &handlers.SecurityHandler{}
	protected.GET("/api/security/settings", securityHandler.GetSettings)
	protected.PUT("/api/security/settings", securityHandler.UpdateSettings)
	protected.POST("/api/security/whitelist/refresh", securityHandler.RefreshWhitelist)
	protected.GET("/api/security/cdn-realip-groups", securityHandler.ListCDNRealIPGroups)
	protected.POST("/api/security/cdn-realip-groups", securityHandler.CreateCDNRealIPGroup)
	protected.PUT("/api/security/cdn-realip-groups/:id", securityHandler.UpdateCDNRealIPGroup)
	protected.DELETE("/api/security/cdn-realip-groups/:id", securityHandler.DeleteCDNRealIPGroup)

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
	protected.POST("/api/files/remote-import", fileHandler.RemoteImport)
	protected.GET("/api/files/remote-import/:id", fileHandler.RemoteImportStatus)
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
	aiHandler := &handlers.AIHandler{}
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
	protected.GET("/api/settings/db-backup/:filename/download", settingsHandler.DownloadDBBackup)
	protected.GET("/api/proxy/test", settingsHandler.TestProxy)
	protected.GET("/api/ai/settings", aiHandler.GetSettings)
	protected.PUT("/api/ai/settings", aiHandler.SaveSettings)
	protected.POST("/api/ai/test", aiHandler.Test)
	protected.POST("/api/websites/:id/ai/diagnose", aiHandler.Diagnose)
	protected.GET("/api/websites/:id/ai/sessions", aiHandler.ListSessions)
	protected.GET("/api/websites/:id/ai/sessions/:session_id", aiHandler.GetSession)
	protected.GET("/api/websites/:id/ai/sessions/:session_id/messages", aiHandler.ListMessages)
	protected.POST("/api/websites/:id/ai/sessions/:session_id/messages", aiHandler.SendMessage)

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
	protected.GET("/ai-diagnostics", func(c *gin.Context) {
		c.HTML(http.StatusOK, "ai_diagnostics.html", pageData(suffix, "ai-diagnostics", "ai_diagnostics_content", c))
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
	updateHandler := &handlers.UpdateHandler{CurrentVersion: version, ConfigPath: configPath, Config: cfg}
	protected.GET("/api/update/check", updateHandler.Check)
	protected.GET("/api/update/status", updateHandler.Status)
	protected.POST("/api/update/do", updateHandler.Update)
	sysUpdateHandler := &handlers.SystemUpdateHandler{}
	protected.GET("/api/system/updates", sysUpdateHandler.Check)
	protected.POST("/api/system/updates/do", sysUpdateHandler.Update)

	tmpl := template.Must(template.New("").Funcs(i18n.FuncMap()).ParseFS(tmplFS, "templates/*.html"))
	r.SetHTMLTemplate(tmpl)

	return r
}

var pageTitleKeys = map[string]string{
	"dashboard":      "nav.dashboard",
	"websites":       "nav.websites",
	"ai-diagnostics": "nav.ai_diagnostics",
	"cron":           "nav.cron",
	"firewall":       "nav.firewall",
	"security":       "nav.security",
	"files":          "nav.files",
	"software":       "nav.software",
	"alert":          "nav.alert",
	"extensions":     "nav.extensions",
	"settings":       "nav.settings",
}

func pageData(suffix string, active string, contentTpl string, c *gin.Context) gin.H {
	i18n.MaybeSetLanguageCookie(c.Writer, c.Request)
	lang := i18n.LangFromRequest(c.Request)
	csrfToken := middleware.GetCSRFToken(c)
	title := i18n.T(lang, pageTitleKeys[active])
	return gin.H{
		"Title":           title,
		"PanelTitle":      handlers.GetPanelTitle(),
		"PanelVersion":    panelVersion,
		"AssetVersion":    panelVersion,
		"ContentTemplate": contentTpl,
		"RandomSuffix":    suffix,
		"Active":          active,
		"AssetPrefix":     "/" + suffix + "/assets",
		"CSRFToken":       csrfToken,
		"Lang":            lang,
		"MessagesJSON":    i18n.MessagesJSON(lang, i18nKeys),
	}
}
