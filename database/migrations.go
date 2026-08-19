package database

var migrations = append([]string{
	// ============================================================
	// admin_users
	// ============================================================
	`CREATE TABLE IF NOT EXISTS admin_users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	// ============================================================
	// websites
	// ============================================================
	`CREATE TABLE IF NOT EXISTS websites (
		id                    INTEGER PRIMARY KEY AUTOINCREMENT,
		name                  TEXT    NOT NULL,
		domain                TEXT    NOT NULL UNIQUE,
		aliases               TEXT    DEFAULT '',
		status                TEXT    NOT NULL DEFAULT 'active',
		system_user           TEXT    NOT NULL,
		web_root              TEXT    NOT NULL,
		document_root_subdir  TEXT    NOT NULL DEFAULT '',
		log_dir               TEXT    NOT NULL,
		db_name               TEXT    NOT NULL,
		db_user               TEXT    NOT NULL,
		php_pool_path         TEXT    NOT NULL,
		nginx_conf_path       TEXT    NOT NULL,
		site_type             TEXT    NOT NULL DEFAULT 'wordpress',
		ssl_enabled           INTEGER NOT NULL DEFAULT 0,
		ssl_cert_path         TEXT    DEFAULT '',
		ssl_key_path          TEXT    DEFAULT '',
		ssl_expires_at        DATETIME,
		ssl_last_error        TEXT    NOT NULL DEFAULT '',
		ssl_export_enabled    INTEGER NOT NULL DEFAULT 0,
		template_version      TEXT    NOT NULL DEFAULT 'v1.0',
		access_log_mode       TEXT    NOT NULL DEFAULT 'error_only',
		fastcgi_cache_enabled INTEGER NOT NULL DEFAULT 0,
		fastcgi_cache_ttl     INTEGER NOT NULL DEFAULT 300,
		fastcgi_cache_key     TEXT    NOT NULL DEFAULT '',
		plugin_api_key        TEXT    NOT NULL DEFAULT '',
		monitoring_enabled    INTEGER NOT NULL DEFAULT 0,
		monitoring_interval   INTEGER NOT NULL DEFAULT 5,
		disable_wp_updates    INTEGER NOT NULL DEFAULT 0,
		disable_file_editing  INTEGER NOT NULL DEFAULT 0,
		xmlrpc_enabled        INTEGER NOT NULL DEFAULT 0,
		wp_debug_enabled      INTEGER NOT NULL DEFAULT 0,
		wp_post_revisions     INTEGER NOT NULL DEFAULT -1,
		wp_memory_limit       TEXT    NOT NULL DEFAULT '',
		file_lock_enabled     INTEGER NOT NULL DEFAULT 0,
		file_lock_enabled_at  TEXT    NOT NULL DEFAULT '',
		file_lock_mode        TEXT    NOT NULL DEFAULT '',
		file_lock_apply_status TEXT   NOT NULL DEFAULT '',
		password_reset_mode   TEXT    NOT NULL DEFAULT 'allow',
		log_retention_days    INTEGER NOT NULL DEFAULT 7,
		cdn_realip_enabled    INTEGER NOT NULL DEFAULT 0,
		php_fpm_max_children  INTEGER NOT NULL DEFAULT 10,
		expires_at            DATETIME,
		created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_websites_status ON websites(status)`,
	`CREATE INDEX IF NOT EXISTS idx_websites_domain ON websites(domain)`,

	// ============================================================
	// cron_jobs
	// ============================================================
	`CREATE TABLE IF NOT EXISTS cron_jobs (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		name            TEXT    NOT NULL,
		cron_expression TEXT    NOT NULL,
		command         TEXT    NOT NULL,
		site_id         INTEGER DEFAULT NULL,
		run_as_user     TEXT    DEFAULT '',
		task_type       TEXT    NOT NULL DEFAULT 'command',
		backup_mode     TEXT    NOT NULL DEFAULT 'incremental',
		keep_count      INTEGER NOT NULL DEFAULT 3,
		notify_fail     INTEGER NOT NULL DEFAULT 0,
		enabled         INTEGER NOT NULL DEFAULT 1,
		running         INTEGER NOT NULL DEFAULT 0,
		last_run_at     DATETIME,
		last_status     TEXT    DEFAULT '',
		last_output     TEXT    DEFAULT '',
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE SET NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_cron_jobs_enabled ON cron_jobs(enabled)`,

	// ============================================================
	// monitoring_metrics
	// ============================================================
	`CREATE TABLE IF NOT EXISTS monitoring_metrics (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		cpu_percent        REAL,
		memory_percent     REAL,
		memory_used_bytes  INTEGER,
		memory_total_bytes INTEGER,
		disk_read_bytes    INTEGER,
		disk_write_bytes   INTEGER,
		load_avg_1         REAL,
		load_avg_5         REAL,
		load_avg_15        REAL,
		recorded_at        DATETIME NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_recorded ON monitoring_metrics(recorded_at)`,

	// ============================================================
	// firewall_bans
	// ============================================================
	`CREATE TABLE IF NOT EXISTS firewall_bans (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		ip_address  TEXT    NOT NULL,
		ban_level   INTEGER NOT NULL DEFAULT 2,
		reason      TEXT    DEFAULT '',
		source_jail TEXT    DEFAULT 'panel',
		banned_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at  DATETIME,
		unbanned_at DATETIME,
		ban_count   INTEGER NOT NULL DEFAULT 1,
		is_manual   INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_bans_ip ON firewall_bans(ip_address)`,
	`CREATE INDEX IF NOT EXISTS idx_bans_status ON firewall_bans(unbanned_at)`,

	// ============================================================
	// login_attempts
	// ============================================================
	`CREATE TABLE IF NOT EXISTS login_attempts (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		ip_address   TEXT    NOT NULL,
		attempt_type TEXT    NOT NULL,
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_attempts_ip_type ON login_attempts(ip_address, attempt_type, created_at)`,

	// ============================================================
	// security_settings
	// ============================================================
	`CREATE TABLE IF NOT EXISTS security_settings (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		skey        TEXT    NOT NULL UNIQUE,
		svalue      TEXT    NOT NULL DEFAULT '',
		description TEXT    DEFAULT '',
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	// ============================================================
	// operation_logs
	// ============================================================
	`CREATE TABLE IF NOT EXISTS operation_logs (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		operation  TEXT    NOT NULL,
		target     TEXT    DEFAULT '',
		status     TEXT    NOT NULL DEFAULT 'success',
		message    TEXT    DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	// ============================================================
	// file_security_events
	// ============================================================
	`CREATE TABLE IF NOT EXISTS file_security_events (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id        INTEGER NOT NULL DEFAULT 0,
		domain         TEXT    NOT NULL DEFAULT '',
		event_type     TEXT    NOT NULL DEFAULT '',
		source         TEXT    NOT NULL DEFAULT '',
		risk_level     TEXT    NOT NULL DEFAULT 'medium',
		path           TEXT    NOT NULL DEFAULT '',
		request_method TEXT    NOT NULL DEFAULT '',
		ip_address     TEXT    NOT NULL DEFAULT '',
		user_agent     TEXT    NOT NULL DEFAULT '',
		status         INTEGER NOT NULL DEFAULT 0,
		file_size      INTEGER NOT NULL DEFAULT 0,
		file_mtime     DATETIME,
		message        TEXT    NOT NULL DEFAULT '',
		first_seen     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		event_count    INTEGER NOT NULL DEFAULT 1,
		resolved_at    DATETIME,
		created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_file_security_events_unique ON file_security_events(site_id, event_type, path, ip_address, request_method)`,
	`CREATE INDEX IF NOT EXISTS idx_file_security_events_last_seen ON file_security_events(resolved_at, last_seen)`,
	`CREATE INDEX IF NOT EXISTS idx_file_security_events_site ON file_security_events(site_id, resolved_at, last_seen)`,

	// ============================================================
	// ssl_certificates
	// ============================================================
	`CREATE TABLE IF NOT EXISTS ssl_certificates (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id    INTEGER NOT NULL UNIQUE,
		domains    TEXT    NOT NULL,
		cert_path  TEXT    NOT NULL,
		key_path   TEXT    NOT NULL,
		issuer     TEXT    DEFAULT 'Let''s Encrypt',
		issued_at  DATETIME,
		expires_at DATETIME NOT NULL,
		auto_renew INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,

	// ============================================================
	// template_versions
	// ============================================================
	`CREATE TABLE IF NOT EXISTS template_versions (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		template_type TEXT    NOT NULL,
		version       TEXT    NOT NULL,
		description   TEXT    DEFAULT '',
		is_active     INTEGER NOT NULL DEFAULT 1,
		created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(template_type, version)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_template_active ON template_versions(template_type, is_active)`,

	// ============================================================
	// cdn_realip_groups
	// ============================================================
	`CREATE TABLE IF NOT EXISTS cdn_realip_groups (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT    NOT NULL UNIQUE,
		provider    TEXT    NOT NULL DEFAULT 'custom',
		header_name TEXT    NOT NULL,
		ip_ranges   TEXT    NOT NULL DEFAULT '',
		builtin     INTEGER NOT NULL DEFAULT 0,
		enabled     INTEGER NOT NULL DEFAULT 1,
		description TEXT    DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_cdn_realip_groups_enabled ON cdn_realip_groups(enabled)`,
	`CREATE TABLE IF NOT EXISTS website_cdn_realip_groups (
		website_id INTEGER NOT NULL,
		group_id   INTEGER NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (website_id, group_id),
		FOREIGN KEY (website_id) REFERENCES websites(id) ON DELETE CASCADE,
		FOREIGN KEY (group_id) REFERENCES cdn_realip_groups(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_website_cdn_realip_groups_group ON website_cdn_realip_groups(group_id)`,

	// ============================================================
	// seed: security_settings
	// ============================================================
	`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES
		('panel_title',              'WP Panel', '面板标题（显示在侧边栏和浏览器标签）'),
		('whitelist_ips',            '',         '合并的官方与自定义白名单IP/段'),
		('fail2ban_maxretry',        '5',        'Fail2ban触发阈值'),
		('fail2ban_findtime',        '60',       'Fail2ban统计时间窗口(秒)'),
		('fail2ban_bantime',         '600',      'Fail2ban初犯封禁时间(秒)'),
		('auto_whitelist_enabled',   'true',     '是否每周自动更新官方白名单'),
		('official_whitelist_ips',   '',         '官方自动拉取的白名单IP/段'),
		('cloudflare_realip_ips',    '',         'Cloudflare Real IP 专用官方IP段'),
		('last_whitelist_update',    '',         '上次白名单更新时间'),
		('rate_limit_enabled',       'true',     '是否开启全局限速'),
		('rate_limit_rpm',           '60',       '每IP每分钟最大请求数'),
		('rate_limit_burst',         '300',      '突发缓冲允许量'),
		('bot_limit_enabled',        'false',    '是否开启Bot UA统一限速'),
		('bot_limit_rpm',            '30',       '每站点Bot每分钟最大请求数'),
		('bot_limit_burst',          '20',       'Bot突发缓冲允许量'),
		('googlebot_ips',            '',         'Googlebot官方IP段缓存'),
		('googlebot_ips_source',     '',         'Googlebot IP段当前来源'),
		('googlebot_ips_last_success_at','',      'Googlebot IP段最近成功更新时间'),
		('googlebot_ips_last_error', '',         'Googlebot IP段最近刷新错误'),
		('bingbot_ips',              '',         'Bingbot官方IP段缓存'),
		('wp_security_log_whitelist','',         'WordPress安全日志路径白名单'),
		('smtp_host',                '',         'SMTP 服务器地址'),
		('smtp_port',                '587',      'SMTP 端口'),
		('smtp_encryption',          'starttls', '加密方式：starttls/ssl/none'),
		('smtp_user',                '',         '发件邮箱账号'),
		('smtp_pass',                '',         '发件邮箱密码/授权码'),
		('admin_email',              '',         '管理员通知邮箱'),
		('alert_cpu',                'true',     'CPU > 80% 持续 5 分钟告警'),
		('alert_memory',             'true',     '可用内存 < 10% 持续 5 分钟告警'),
		('alert_oom',                'true',     '系统发生 OOM 强制终止进程时告警'),
		('alert_disk',               'true',     '磁盘 > 90% 告警'),
		('alert_service',            'true',     '服务进程异常重启告警'),
		('alert_ssl',                'true',     'SSL 证书到期告警'),
		('alert_backup',             'true',     '数据库备份失败告警'),
		('alert_website_expiry',     'true',     '网站到期告警'),
		('alert_remote_backup',      'false',    '远程备份失败告警（需先启用远程备份）'),
		('alert_cron_fail',          'true',     '计划任务执行失败告警'),
		('alert_site',               'true',     '网站不可用告警'),
		('alert_system_update',      'true',     '系统可用更新告警'),
		('alert_panel_update',       'true',     '面板新版本告警'),
		('telemetry_enabled',        'true',     '匿名安装统计（仅上报机器标识和版本号）'),
		('telemetry_url',            '',         '自定义统计上报地址（留空使用默认）'),
		('github_proxy',             '',          'GitHub 反代地址，留空直连'),
		('panel_auto_update_enabled','false',     '是否启用面板自动更新'),
		('panel_auto_update_mode',   'patch_only','面板自动更新模式：patch_only/all_stable'),
		('panel_auto_update_window', '03:00-05:00','面板自动更新时间窗口'),
		('panel_auto_update_release_delay_minutes','15','面板自动更新发布延迟分钟数'),
		('panel_auto_update_signature_timeout_minutes','120','面板自动更新等待签名超时分钟数'),
		('panel_auto_update_last_target_version','','面板自动更新最近目标版本'),
		('panel_auto_update_last_check_at','','面板自动更新最近检查时间'),
		('panel_auto_update_last_attempt_at','','面板自动更新最近尝试时间'),
		('panel_auto_update_last_status','','面板自动更新最近状态'),
		('panel_auto_update_last_stage','','面板自动更新最近阶段'),
		('panel_auto_update_last_error','','面板自动更新最近错误'),
		('panel_auto_update_last_success_at','','面板自动更新最近成功时间'),
		('panel_auto_update_last_success_version','','面板自动更新最近成功版本'),
		('panel_auto_update_signature_wait_version','','面板自动更新等待签名版本'),
		('panel_auto_update_signature_wait_at','','面板自动更新等待签名开始时间')`,

	// ============================================================
	// seed: template_versions
	// ============================================================
	`INSERT OR IGNORE INTO template_versions (template_type, version, description, is_active) VALUES
		('nginx_http',   'v1.0', 'HTTP默认模板',             1),
		('nginx_https',  'v1.0', 'HTTPS(含SSL)模板',         1),
		('php_fpm_pool', 'v1.0', 'PHP-FPM Pool隔离模板',     1)`,

	// ============================================================
	// seed: cdn_realip_groups
	// ============================================================
	`INSERT OR IGNORE INTO cdn_realip_groups (name, provider, header_name, ip_ranges, builtin, enabled, description) VALUES
		('Cloudflare', 'cloudflare', 'CF-Connecting-IP', '', 1, 1, 'Cloudflare 官方 IP 段由面板自动拉取'),
		('通用 CDN（兼容模式）', 'compatible', 'X-Forwarded-For', '', 1, 1, '不校验来源 IP，直接信任 X-Forwarded-For')`,

	// ============================================================
	// db_backups
	// ============================================================
	`CREATE TABLE IF NOT EXISTS db_backups (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id           INTEGER NOT NULL,
		filename          TEXT    NOT NULL,
		file_size         INTEGER DEFAULT 0,
		db_name           TEXT    NOT NULL,
		auto              INTEGER NOT NULL DEFAULT 0,
		transport_status  TEXT    DEFAULT 'local',
		transport_message TEXT    DEFAULT '',
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_backups_site ON db_backups(site_id, created_at)`,

	// ============================================================
	// file_backups（网站文件备份记录）
	// ============================================================
	`CREATE TABLE IF NOT EXISTS file_backups (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id           INTEGER NOT NULL,
		filename          TEXT    NOT NULL,
		file_size         INTEGER DEFAULT 0,
		mode              TEXT    NOT NULL DEFAULT 'full',
		transport_status  TEXT    DEFAULT 'local',
		transport_message TEXT    DEFAULT '',
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_file_backups_site ON file_backups(site_id, created_at)`,

	// ============================================================
	// backup_settings
	// ============================================================
	`CREATE TABLE IF NOT EXISTS backup_settings (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id    INTEGER NOT NULL UNIQUE,
		enabled    INTEGER NOT NULL DEFAULT 0,
		keep_count INTEGER NOT NULL DEFAULT 7,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,

	// ============================================================
	// process_guard_incidents
	// ============================================================
	`CREATE TABLE IF NOT EXISTS process_guard_incidents (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		service    TEXT    NOT NULL,
		event      TEXT    NOT NULL,
		message    TEXT    DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_guard_service ON process_guard_incidents(service, created_at)`,

	// ============================================================
	// alert_log
	// ============================================================
	`CREATE TABLE IF NOT EXISTS alert_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		alert_type TEXT    NOT NULL,
		level      TEXT    NOT NULL DEFAULT 'warning',
		message    TEXT    NOT NULL,
		resolved   INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_alert_log_type ON alert_log(alert_type, created_at)`,

	// ============================================================
	// system_oom_events
	// ============================================================
	`CREATE TABLE IF NOT EXISTS system_oom_events (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		event_key   TEXT     NOT NULL UNIQUE,
		process     TEXT     NOT NULL,
		pid         INTEGER  NOT NULL,
		message     TEXT     NOT NULL,
		occurred_at DATETIME NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_oom_events_occurred ON system_oom_events(occurred_at)`,

	// ============================================================
	// wp_extension_config — 默认主题/插件自动安装配置
	// ============================================================
	`CREATE TABLE IF NOT EXISTS wp_extension_config (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		etype   TEXT    NOT NULL,
		slug    TEXT    NOT NULL,
		name    TEXT    NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		UNIQUE(etype, slug)
	)`,
	`INSERT OR IGNORE INTO wp_extension_config (etype, slug, name, enabled) VALUES
		('theme',  'hello-elementor',   'Hello Elementor',  1),
		('theme',  'astra',             'Astra',            1),
		('theme',  'kadence',           'Kadence',          1),
		('theme',  'blocksy',           'Blocksy',          1),
		('plugin', 'elementor',         'Elementor',        1),
		('plugin', 'wordpress-seo',     'Yoast SEO',        1),
		('plugin', 'seo-by-rank-math',  'Rank Math SEO',    1),
		('plugin', 'woocommerce',       'WooCommerce',      1),
		('plugin', 'naibabiji-b2b-product-showcase', 'B2B Product Catalog', 1),
		('plugin', 'redis-cache',          'Redis Cache',      1)`,

	// 远程备份设置
	`CREATE TABLE IF NOT EXISTS remote_backup_settings (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			enabled     INTEGER NOT NULL DEFAULT 0,
			backup_type TEXT    NOT NULL DEFAULT 'rsync',
			host        TEXT    NOT NULL DEFAULT '',
			port        INTEGER NOT NULL DEFAULT 22,
			username    TEXT    NOT NULL DEFAULT 'root',
			auth_type   TEXT    NOT NULL DEFAULT 'password',
			connection_mode TEXT NOT NULL DEFAULT 'auto',
			server_id   TEXT    NOT NULL DEFAULT '',
			password    TEXT    NOT NULL DEFAULT '',
			ssh_key     TEXT    NOT NULL DEFAULT '',
			remote_path TEXT    NOT NULL DEFAULT '',
			remote_base_path TEXT NOT NULL DEFAULT '',
			isolate_path INTEGER NOT NULL DEFAULT 1,
			keep_local  INTEGER NOT NULL DEFAULT 1,
			s3_endpoint      TEXT NOT NULL DEFAULT '',
			s3_bucket        TEXT NOT NULL DEFAULT '',
			s3_region        TEXT NOT NULL DEFAULT 'auto',
			s3_access_key_id TEXT NOT NULL DEFAULT '',
			s3_secret_key    TEXT NOT NULL DEFAULT '',
			s3_path_prefix   TEXT NOT NULL DEFAULT '',
			s3_base_prefix   TEXT NOT NULL DEFAULT '',
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	`INSERT OR IGNORE INTO remote_backup_settings (id) VALUES (1)`,

	`CREATE TABLE IF NOT EXISTS remote_backup_site_state (
		site_id          INTEGER PRIMARY KEY,
		status           TEXT NOT NULL DEFAULT 'unknown',
		rebuild_required INTEGER NOT NULL DEFAULT 0,
		message          TEXT NOT NULL DEFAULT '',
		last_checked_at  DATETIME,
		updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,

	// ============================================================
	// ai_settings
	// ============================================================
	`CREATE TABLE IF NOT EXISTS ai_settings (
		id              INTEGER PRIMARY KEY,
		enabled         INTEGER NOT NULL DEFAULT 0,
		provider        TEXT    NOT NULL DEFAULT 'deepseek',
		base_url        TEXT    NOT NULL DEFAULT 'https://api.deepseek.com',
		model           TEXT    NOT NULL DEFAULT 'deepseek-v4-pro',
		api_key         TEXT    NOT NULL DEFAULT '',
		timeout_seconds INTEGER NOT NULL DEFAULT 180,
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`INSERT OR IGNORE INTO ai_settings (id) VALUES (1)`,

	// ============================================================
	// ai_sessions
	// ============================================================
	`CREATE TABLE IF NOT EXISTS ai_sessions (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id        INTEGER NOT NULL,
		symptom        TEXT    NOT NULL DEFAULT '',
		status         TEXT    NOT NULL DEFAULT 'pending',
		risk_level     TEXT    NOT NULL DEFAULT '',
		summary        TEXT    NOT NULL DEFAULT '',
		report_json    TEXT    NOT NULL DEFAULT '',
		raw_text       TEXT    NOT NULL DEFAULT '',
		prompt_chars   INTEGER NOT NULL DEFAULT 0,
		response_chars INTEGER NOT NULL DEFAULT 0,
		error_message  TEXT    NOT NULL DEFAULT '',
		context_type   TEXT    NOT NULL DEFAULT '',
		context_id     INTEGER NOT NULL DEFAULT 0,
		context_json   TEXT    NOT NULL DEFAULT '',
		focus_kind     TEXT    NOT NULL DEFAULT '',
		focus_value    TEXT    NOT NULL DEFAULT '',
		created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_sessions_site ON ai_sessions(site_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_sessions_status ON ai_sessions(site_id, status)`,

	// ============================================================
	// ai_messages
	// ============================================================
	`CREATE TABLE IF NOT EXISTS ai_messages (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id     INTEGER NOT NULL,
		role           TEXT    NOT NULL DEFAULT '',
		content        TEXT    NOT NULL DEFAULT '',
		prompt_chars   INTEGER NOT NULL DEFAULT 0,
		response_chars INTEGER NOT NULL DEFAULT 0,
		error_message  TEXT    NOT NULL DEFAULT '',
		created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES ai_sessions(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_messages_session ON ai_messages(session_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS ai_tool_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL,
		tool_name TEXT NOT NULL DEFAULT '',
		result_summary TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES ai_sessions(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_tool_events_session ON ai_tool_events(session_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS ai_ip_aliases (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL,
		alias TEXT NOT NULL,
		ip_address TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (session_id) REFERENCES ai_sessions(id) ON DELETE CASCADE,
		UNIQUE (session_id, alias),
		UNIQUE (session_id, ip_address)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_ip_aliases_session ON ai_ip_aliases(session_id, alias)`,

	// ============================================================
	// log_analysis_jobs
	// ============================================================
	`CREATE TABLE IF NOT EXISTS log_analysis_jobs (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id           INTEGER NOT NULL,
		status            TEXT    NOT NULL DEFAULT 'pending',
		start_at          DATETIME NOT NULL,
		end_at            DATETIME NOT NULL,
		use_ai            INTEGER NOT NULL DEFAULT 0,
		local_report_json TEXT    NOT NULL DEFAULT '',
		ai_analysis       TEXT    NOT NULL DEFAULT '',
		error_message     TEXT    NOT NULL DEFAULT '',
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_log_analysis_jobs_site ON log_analysis_jobs(site_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_log_analysis_jobs_status ON log_analysis_jobs(status, updated_at)`,

	// ============================================================
	// wp_security_events — 方案 D 阶段三：持久化 WordPress 安全探测事件，
	// 避免仅依赖日志尾部读取导致 logrotate 跨天后历史丢失
	// ============================================================
	`CREATE TABLE IF NOT EXISTS wp_security_events (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id     INTEGER NOT NULL DEFAULT 0,
		domain      TEXT    NOT NULL DEFAULT '',
		ip_address  TEXT    NOT NULL DEFAULT '',
		event_type  TEXT    NOT NULL DEFAULT '',
		risk_level  TEXT    NOT NULL DEFAULT 'low',
		method      TEXT    NOT NULL DEFAULT '',
		path        TEXT    NOT NULL DEFAULT '',
		user_agent  TEXT    NOT NULL DEFAULT '',
		status      INTEGER NOT NULL DEFAULT 0,
		message     TEXT    NOT NULL DEFAULT '',
		occurred_at DATETIME NOT NULL,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wp_security_events_ip_type_time ON wp_security_events(ip_address, event_type, occurred_at)`,
	`CREATE INDEX IF NOT EXISTS idx_wp_security_events_site_time ON wp_security_events(site_id, occurred_at)`,

	// ============================================================
	// wp_security_log_positions — 记录每个站点 wp-security.log 已读取到的字节偏移，
	// 支撑增量读取（配合 copytruncate 轮转：文件变小即视为已轮转，从头重新读取）
	// ============================================================
	`CREATE TABLE IF NOT EXISTS wp_security_log_positions (
		site_id     INTEGER PRIMARY KEY,
		byte_offset INTEGER NOT NULL DEFAULT 0,
		first_line_hash TEXT NOT NULL DEFAULT '',
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,

	// ============================================================
	// 方案 D 阶段四：WordPress 安全探测告警开关，默认关闭；
	// 阈值与统计窗口可配置（审核优化项 3.1）
	// ============================================================
	`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES
		('alert_wp_sqli_probe',          'false', 'WordPress SQL 注入探测告警（默认关闭）'),
		('alert_wp_fake_search_bot',     'false', '伪装搜索引擎爬虫告警（默认关闭）'),
		('alert_wp_security_threshold',  '10',    'WordPress 安全探测告警阈值（每 IP 触发次数，默认 10）'),
		('alert_wp_security_window_hours','24',   'WordPress 安全探测告警统计窗口（小时，默认 24）')`,
}, append(wpInventorySchemaStatements, append(wpUpdateSchemaStatements, imageOptimizerSchemaStatements...)...)...)

// wpInventorySchemaStatements 同时供全新安装和 1.0.31 增量升级使用，避免两条路径的
// 表结构、约束或索引随维护产生漂移。
var wpInventorySchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS site_wp_inventory_state (
		site_id                           INTEGER PRIMARY KEY,
		status                            TEXT NOT NULL,
		wordpress_version                 TEXT NOT NULL DEFAULT '',
		wordpress_locale                  TEXT NOT NULL DEFAULT '',
		is_multisite                      INTEGER NOT NULL DEFAULT 0,
		current_theme_key                 TEXT NOT NULL DEFAULT '',
		plugin_count                      INTEGER NOT NULL DEFAULT 0,
		active_plugin_count               INTEGER NOT NULL DEFAULT 0,
		theme_count                       INTEGER NOT NULL DEFAULT 0,
		core_update_count                 INTEGER NOT NULL DEFAULT 0,
		plugin_update_count               INTEGER NOT NULL DEFAULT 0,
		theme_update_count                INTEGER NOT NULL DEFAULT 0,
		core_updates_transient_present    INTEGER NOT NULL DEFAULT 0,
		core_updates_last_checked         INTEGER NOT NULL DEFAULT 0,
		core_version_checked              TEXT NOT NULL DEFAULT '',
		plugin_updates_transient_present  INTEGER NOT NULL DEFAULT 0,
		plugin_updates_last_checked       INTEGER NOT NULL DEFAULT 0,
		theme_updates_transient_present   INTEGER NOT NULL DEFAULT 0,
		theme_updates_last_checked        INTEGER NOT NULL DEFAULT 0,
		collection_id                     TEXT NOT NULL DEFAULT '',
		last_attempt_at                   DATETIME,
		last_success_at                   DATETIME,
		last_error_code                   TEXT NOT NULL DEFAULT '',
		last_error_stage                  TEXT NOT NULL DEFAULT '',
		updated_at                        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		CHECK (status IN ('unknown','complete','partial','failed','unsupported'))
	)`,
	`CREATE TABLE IF NOT EXISTS site_wp_components (
		site_id          INTEGER NOT NULL,
		component_type   TEXT NOT NULL,
		component_key    TEXT NOT NULL,
		name             TEXT NOT NULL DEFAULT '',
		version          TEXT NOT NULL DEFAULT '',
		is_active        INTEGER NOT NULL DEFAULT 0,
		is_network_active INTEGER NOT NULL DEFAULT 0,
		is_current_theme INTEGER NOT NULL DEFAULT 0,
		collection_id    TEXT NOT NULL,
		collected_at     DATETIME NOT NULL,
		PRIMARY KEY (site_id, component_type, component_key),
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		CHECK (component_type IN ('core','plugin','theme'))
	)`,
	`CREATE TABLE IF NOT EXISTS site_wp_component_updates (
		site_id        INTEGER NOT NULL,
		component_type TEXT NOT NULL,
		component_key  TEXT NOT NULL,
		target_version TEXT NOT NULL,
		response       TEXT NOT NULL DEFAULT '',
		locale         TEXT NOT NULL DEFAULT '',
		collection_id  TEXT NOT NULL,
		collected_at   DATETIME NOT NULL,
		PRIMARY KEY (site_id, component_type, component_key, target_version, locale, response),
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		CHECK (component_type IN ('core','plugin','theme'))
	)`,
	`CREATE TABLE IF NOT EXISTS site_wp_inventory_jobs (
		id                       TEXT PRIMARY KEY,
		site_id                  INTEGER NOT NULL,
		trigger_type              TEXT NOT NULL,
		priority                  INTEGER NOT NULL DEFAULT 20,
		status                   TEXT NOT NULL,
		requested_at             DATETIME NOT NULL,
		not_before               DATETIME NOT NULL,
		lease_owner              TEXT NOT NULL DEFAULT '',
		lease_expires_at         DATETIME,
		attempt_count            INTEGER NOT NULL DEFAULT 0,
		lease_recovery_count     INTEGER NOT NULL DEFAULT 0,
		started_at               DATETIME,
		finished_at              DATETIME,
		error_code               TEXT NOT NULL DEFAULT '',
		error_stage              TEXT NOT NULL DEFAULT '',
		timed_out                INTEGER NOT NULL DEFAULT 0,
		exit_code                INTEGER NOT NULL DEFAULT -1,
		wall_time_ms             INTEGER NOT NULL DEFAULT 0,
		user_cpu_ms              INTEGER NOT NULL DEFAULT 0,
		system_cpu_ms            INTEGER NOT NULL DEFAULT 0,
		max_rss_kib              INTEGER NOT NULL DEFAULT 0,
		stdout_bytes             INTEGER NOT NULL DEFAULT 0,
		stderr_bytes             INTEGER NOT NULL DEFAULT 0,
		protocol_bytes           INTEGER NOT NULL DEFAULT 0,
		runner_hash              TEXT NOT NULL DEFAULT '',
		runner_version           TEXT NOT NULL DEFAULT '',
		inventory_schema_version INTEGER NOT NULL DEFAULT 0,
		created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		CHECK (trigger_type IN ('manual','site_created','update_followup','scheduled')),
		CHECK (status IN ('queued','running','succeeded','failed'))
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_site_wp_inventory_jobs_active_site
		ON site_wp_inventory_jobs(site_id) WHERE status IN ('queued','running')`,
	`CREATE INDEX IF NOT EXISTS ix_site_wp_inventory_jobs_claim
		ON site_wp_inventory_jobs(status, priority, not_before, requested_at)`,
	`CREATE INDEX IF NOT EXISTS ix_site_wp_inventory_jobs_finished
		ON site_wp_inventory_jobs(status, finished_at)`,
	`CREATE TABLE IF NOT EXISTS site_wp_inventory_job_warnings (
		job_id       TEXT NOT NULL,
		warning_code TEXT NOT NULL,
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (job_id, warning_code),
		FOREIGN KEY (job_id) REFERENCES site_wp_inventory_jobs(id) ON DELETE CASCADE,
		CHECK (warning_code IN ('stale_runner_cleanup_failed','unknown_runner_entry'))
	)`,
}
