package database

// managedMailSchemaStatements are shared by fresh installs and the 1.0.68
// upgrade. Message bodies and attachments deliberately never enter panel
// SQLite; only policy, quota/idempotency facts and operational metadata live
// here.
var managedMailSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS managed_mail_sites (
		site_id             INTEGER PRIMARY KEY,
		authorized          INTEGER NOT NULL DEFAULT 0,
		site_confirmed       INTEGER NOT NULL DEFAULT 0,
		from_name            TEXT    NOT NULL DEFAULT '',
		reply_to             TEXT    NOT NULL DEFAULT '',
		lock_from_name       INTEGER NOT NULL DEFAULT 0,
		minute_limit         INTEGER NOT NULL DEFAULT 3,
		hour_limit           INTEGER NOT NULL DEFAULT 15,
		day_limit            INTEGER NOT NULL DEFAULT 50,
		conflict_plugin      TEXT    NOT NULL DEFAULT '',
		conflict_detected_at DATETIME,
		paused_until         DATETIME,
		paused_reason        TEXT    NOT NULL DEFAULT '',
		created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		CHECK (authorized IN (0,1)),
		CHECK (site_confirmed IN (0,1)),
		CHECK (lock_from_name IN (0,1)),
		CHECK (minute_limit BETWEEN 1 AND 10000),
		CHECK (hour_limit BETWEEN 1 AND 100000),
		CHECK (day_limit BETWEEN 1 AND 1000000)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_managed_mail_sites_state
		ON managed_mail_sites(authorized, site_confirmed, paused_until)`,
	`CREATE TABLE IF NOT EXISTS managed_mail_deliveries (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		request_id      TEXT    NOT NULL UNIQUE,
		site_id         INTEGER NOT NULL,
		status          TEXT    NOT NULL,
		recipient_count INTEGER NOT NULL DEFAULT 0,
		message_size    INTEGER NOT NULL DEFAULT 0,
		message_id      TEXT    NOT NULL DEFAULT '',
		error_code      TEXT    NOT NULL DEFAULT '',
		smtp_code       INTEGER NOT NULL DEFAULT 0,
		duration_ms     INTEGER NOT NULL DEFAULT 0,
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		CHECK (recipient_count BETWEEN 0 AND 10000),
		CHECK (message_size >= 0),
		CHECK (duration_ms >= 0)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_managed_mail_deliveries_site_time
		ON managed_mail_deliveries(site_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_managed_mail_deliveries_status_time
		ON managed_mail_deliveries(status, created_at)`,
	`INSERT OR IGNORE INTO security_settings (skey,svalue,description) VALUES
		('smtp_from_email','','SMTP 发件邮箱（与认证用户名分离）'),
		('managed_mail_limit_preset','conservative','WordPress 托管发信总量预设'),
		('managed_mail_total_minute_limit','10','全部网站每分钟发送单位上限'),
		('managed_mail_total_hour_limit','30','全部网站每小时发送单位上限'),
		('managed_mail_total_day_limit','100','全部网站每天发送单位上限'),
		('managed_mail_max_recipients','10','WordPress 托管邮件单封收件人数上限'),
		('managed_mail_max_message_bytes','10485760','WordPress 托管邮件最终大小上限'),
		('managed_mail_max_attachments','5','WordPress 托管邮件单封附件数上限'),
		('managed_mail_system_day_limit','20','面板系统告警每日保护上限'),
		('managed_mail_quota_notice_state','','WordPress 托管发信额度提醒去重状态')`,
}
