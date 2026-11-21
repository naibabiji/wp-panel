package database

// A single record owns each target's current evidence, action budget and
// recovery state. Installation remains owned by the existing APT runner.
var systemMaintenanceSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS system_maintenance_actions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL CHECK(kind IN ('round','service','reboot','notice')),
		target TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending'
			CHECK(status IN ('pending','requested','succeeded','failed','unknown')),
		phase TEXT NOT NULL DEFAULT '' CHECK(phase IN ('','install','runtime')),
		evidence_json TEXT NOT NULL DEFAULT '[]',
		last_success_json TEXT NOT NULL DEFAULT '[]',
		health_json TEXT NOT NULL DEFAULT '[]',
		round_id INTEGER REFERENCES system_maintenance_actions(id) ON DELETE SET NULL,
		boot_id_before TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
		manual_retry INTEGER NOT NULL DEFAULT 0 CHECK(manual_retry IN (0,1)),
		overtime_allowed INTEGER NOT NULL DEFAULT 0 CHECK(overtime_allowed IN (0,1)),
		deferred_since TEXT NOT NULL DEFAULT '',
		deferred_windows INTEGER NOT NULL DEFAULT 0 CHECK(deferred_windows>=0),
		deferred_window_key TEXT NOT NULL DEFAULT '',
		requested_at TEXT NOT NULL DEFAULT '',
		finished_at TEXT NOT NULL DEFAULT '',
		message_code TEXT NOT NULL DEFAULT '',
		notified_status TEXT NOT NULL DEFAULT '',
		notify_after TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(kind,target)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_system_maintenance_actions_status
		ON system_maintenance_actions(status,updated_at)`,
	// RunMigrations precedes RunUpgrades on old installations too. Do not
	// insert false before observing the old administrator preference.
	`INSERT OR IGNORE INTO system_maintenance_actions(kind,target,status,message_code)
		SELECT 'notice','migration','pending','maintenance_migrated'
		WHERE NOT EXISTS(SELECT 1 FROM security_settings WHERE skey='system_maintenance_enabled')
		AND EXISTS(SELECT 1 FROM security_settings WHERE skey='system_package_auto_update_enabled' AND svalue='true')`,
	`INSERT OR IGNORE INTO security_settings(skey,svalue,description)
		SELECT 'system_maintenance_enabled',
		CASE WHEN EXISTS(SELECT 1 FROM security_settings WHERE skey='system_package_auto_update_enabled' AND svalue='true')
		THEN 'true' ELSE 'false' END,'系统自动维护：安装与必要重启'`,
	`INSERT OR IGNORE INTO security_settings(skey,svalue,description) VALUES
		('system_maintenance_mode','scheduled','自动维护执行方式'),
		('system_maintenance_window','04:00-06:00','自动维护允许开始的时段'),
		('alert_system_maintenance','true','系统自动维护通知')`,
}
