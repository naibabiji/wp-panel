package database

var migrations = []string{
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
		template_version      TEXT    NOT NULL DEFAULT 'v1.0',
		access_log_mode       TEXT    NOT NULL DEFAULT 'off',
		fastcgi_cache_enabled INTEGER NOT NULL DEFAULT 0,
		fastcgi_cache_ttl     INTEGER NOT NULL DEFAULT 300,
		fastcgi_cache_key     TEXT    NOT NULL DEFAULT '',
		plugin_api_key        TEXT    NOT NULL DEFAULT '',
		monitoring_enabled    INTEGER NOT NULL DEFAULT 0,
		monitoring_interval   INTEGER NOT NULL DEFAULT 5,
		disable_wp_updates    INTEGER NOT NULL DEFAULT 0,
		disable_file_editing  INTEGER NOT NULL DEFAULT 0,
		xmlrpc_enabled        INTEGER NOT NULL DEFAULT 0,
		log_retention_days    INTEGER NOT NULL DEFAULT 7,
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
	`ALTER TABLE cron_jobs ADD COLUMN running INTEGER NOT NULL DEFAULT 0`,

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
		('last_whitelist_update',    '',         '上次白名单更新时间'),
		('rate_limit_enabled',       'true',     '是否开启全局限速'),
		('rate_limit_rpm',           '60',       '每IP每分钟最大请求数'),
		('rate_limit_burst',         '300',      '突发缓冲允许量'),
		('smtp_host',                '',         'SMTP 服务器地址'),
		('smtp_port',                '587',      'SMTP 端口'),
		('smtp_encryption',          'starttls', '加密方式：starttls/ssl/none'),
		('smtp_user',                '',         '发件邮箱账号'),
		('smtp_pass',                '',         '发件邮箱密码/授权码'),
		('admin_email',              '',         '管理员通知邮箱'),
		('alert_cpu',                'true',     'CPU > 80% 持续 5 分钟告警'),
		('alert_memory',             'true',     '内存 > 90% 持续 5 分钟告警'),
		('alert_disk',               'true',     '磁盘 > 90% 告警'),
		('alert_service',            'true',     '服务进程异常重启告警'),
		('alert_ssl',                'true',     'SSL 证书到期告警'),
		('alert_backup',             'true',     '数据库备份失败告警'),
		('alert_website_expiry',     'true',     '网站到期告警'),
		('alert_remote_backup',      'false',    '远程备份失败告警（需先启用远程备份）'),
		('alert_cron_fail',          'true',     '计划任务执行失败告警'),
		('alert_site',               'true',     '网站不可用告警')`,

	// ============================================================
	// seed: template_versions
	// ============================================================
	`INSERT OR IGNORE INTO template_versions (template_type, version, description, is_active) VALUES
		('nginx_http',   'v1.0', 'HTTP默认模板',             1),
		('nginx_https',  'v1.0', 'HTTPS(含SSL)模板',         1),
		('php_fpm_pool', 'v1.0', 'PHP-FPM Pool隔离模板',     1)`,

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
		('plugin', 'naibabiji-b2b-product-showcase', 'B2B Product Catalog', 1)`,

	// 远程备份设置
	`CREATE TABLE IF NOT EXISTS remote_backup_settings (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			enabled     INTEGER NOT NULL DEFAULT 0,
			host        TEXT    NOT NULL DEFAULT '',
			port        INTEGER NOT NULL DEFAULT 22,
			username    TEXT    NOT NULL DEFAULT 'root',
			auth_type   TEXT    NOT NULL DEFAULT 'password',
			password    TEXT    NOT NULL DEFAULT '',
			ssh_key     TEXT    NOT NULL DEFAULT '',
			remote_path TEXT    NOT NULL DEFAULT '',
			keep_local  INTEGER NOT NULL DEFAULT 1,
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	`INSERT OR IGNORE INTO remote_backup_settings (id) VALUES (1)`,
}
