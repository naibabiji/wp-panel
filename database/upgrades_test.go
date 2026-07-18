package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTempDB(t *testing.T) {
	t.Helper()
	if DB != nil {
		_ = Close()
		DB = nil
	}
	if err := Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = Close()
		DB = nil
	})
}

func TestFreshInstallRunsMigrationsAndRecordsLatestVersion(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}

	var version string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != LatestVersion() {
		t.Fatalf("version = %q, want %q", version, LatestVersion())
	}

	for _, col := range []string{"php_pool_path", "nginx_conf_path", "wp_memory_limit", "file_lock_enabled", "file_lock_enabled_at", "file_lock_mode", "file_lock_apply_status", "cdn_realip_enabled", "ssl_last_error", "ssl_export_enabled", "document_root_subdir"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = ?", col).Scan(&exists); err != nil {
			t.Fatalf("query websites column %s: %v", col, err)
		}
		if exists != 1 {
			t.Fatalf("websites.%s exists = %d, want 1", col, exists)
		}
	}

	var groupCount int
	if err := DB.QueryRow("SELECT COUNT(*) FROM cdn_realip_groups WHERE builtin = 1").Scan(&groupCount); err != nil {
		t.Fatalf("query cdn_realip_groups: %v", err)
	}
	if groupCount < 2 {
		t.Fatalf("builtin cdn realip groups = %d, want at least 2", groupCount)
	}
	var aiModel string
	if err := DB.QueryRow("SELECT model FROM ai_settings WHERE id = 1").Scan(&aiModel); err != nil {
		t.Fatalf("query ai_settings: %v", err)
	}
	if aiModel != "deepseek-v4-pro" {
		t.Fatalf("ai default model = %q, want deepseek-v4-pro", aiModel)
	}
	for _, table := range []string{"ai_settings", "ai_sessions", "ai_messages"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&exists); err != nil {
			t.Fatalf("query %s table: %v", table, err)
		}
		if exists != 1 {
			t.Fatalf("%s exists = %d, want 1", table, exists)
		}
	}
	var fileSecurityEventsTable int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'file_security_events'").Scan(&fileSecurityEventsTable); err != nil {
		t.Fatalf("query file_security_events table: %v", err)
	}
	if fileSecurityEventsTable != 1 {
		t.Fatalf("file_security_events exists = %d, want 1", fileSecurityEventsTable)
	}
	for _, col := range []string{"backup_type", "s3_endpoint", "s3_bucket", "s3_region", "s3_access_key_id", "s3_secret_key", "s3_path_prefix"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('remote_backup_settings') WHERE name = ?", col).Scan(&exists); err != nil {
			t.Fatalf("query remote_backup_settings column %s: %v", col, err)
		}
		if exists != 1 {
			t.Fatalf("remote_backup_settings.%s exists = %d, want 1", col, exists)
		}
	}
	for _, setting := range []struct {
		key  string
		want string
	}{
		{"cloudflare_realip_ips", ""},
		{"bot_limit_enabled", "false"},
		{"bot_limit_rpm", "30"},
		{"bot_limit_burst", "20"},
		{"googlebot_ips", ""},
		{"bingbot_ips", ""},
	} {
		var got string
		if err := DB.QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", setting.key).Scan(&got); err != nil {
			t.Fatalf("query %s setting: %v", setting.key, err)
		}
		if got != setting.want {
			t.Fatalf("%s = %q, want %q", setting.key, got, setting.want)
		}
	}
}

func TestUpgradeAddsFileLockModesAndBackfillsLegacySites(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.29')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN file_lock_mode"); err != nil {
		t.Fatalf("drop file_lock_mode: %v", err)
	}
	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN file_lock_apply_status"); err != nil {
		t.Fatalf("drop file_lock_apply_status: %v", err)
	}
	for _, site := range []struct {
		domain  string
		enabled int
	}{
		{"locked.example.com", 1},
		{"unlocked.example.com", 0},
	} {
		if _, err := DB.Exec(`INSERT INTO websites
			(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, file_lock_enabled)
			VALUES (?, ?, 'active', 'wp_demo', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', ?)`,
			site.domain, site.domain, site.enabled); err != nil {
			t.Fatalf("insert %s: %v", site.domain, err)
		}
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := ensureFileLockModeColumns(); err != nil {
		t.Fatalf("second ensureFileLockModeColumns() error = %v", err)
	}

	for _, tt := range []struct {
		domain string
		mode   string
		status string
	}{
		{"locked.example.com", "legacy", "ready"},
		{"unlocked.example.com", "", ""},
	} {
		var mode, status string
		if err := DB.QueryRow("SELECT file_lock_mode, file_lock_apply_status FROM websites WHERE domain = ?", tt.domain).Scan(&mode, &status); err != nil {
			t.Fatalf("query %s: %v", tt.domain, err)
		}
		if mode != tt.mode || status != tt.status {
			t.Fatalf("%s mode/status = %q/%q, want %q/%q", tt.domain, mode, status, tt.mode, tt.status)
		}
	}
}

func TestUpgradeAddsS3RemoteBackupColumnsToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.19')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	for _, col := range []string{"backup_type", "s3_endpoint", "s3_bucket", "s3_region", "s3_access_key_id", "s3_secret_key", "s3_path_prefix"} {
		if _, err := DB.Exec("ALTER TABLE remote_backup_settings DROP COLUMN " + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	for _, col := range []string{"backup_type", "s3_endpoint", "s3_bucket", "s3_region", "s3_access_key_id", "s3_secret_key", "s3_path_prefix"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('remote_backup_settings') WHERE name = ?", col).Scan(&exists); err != nil {
			t.Fatalf("query %s: %v", col, err)
		}
		if exists != 1 {
			t.Fatalf("%s exists = %d, want 1", col, exists)
		}
	}
}

func TestUpgradeAddsFileLockEnabledColumnToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.20')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN file_lock_enabled"); err != nil {
		t.Fatalf("drop file_lock_enabled: %v", err)
	}
	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN file_lock_enabled_at"); err != nil {
		t.Fatalf("drop file_lock_enabled_at: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'file_lock_enabled'").Scan(&exists); err != nil {
		t.Fatalf("query file_lock_enabled: %v", err)
	}
	if exists != 1 {
		t.Fatalf("file_lock_enabled exists = %d, want 1", exists)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'file_lock_enabled_at'").Scan(&exists); err != nil {
		t.Fatalf("query file_lock_enabled_at: %v", err)
	}
	if exists != 1 {
		t.Fatalf("file_lock_enabled_at exists = %d, want 1", exists)
	}
}

func TestUpgradeAddsFileLockEnabledAtColumnToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.22')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN file_lock_enabled_at"); err != nil {
		t.Fatalf("drop file_lock_enabled_at: %v", err)
	}
	if _, err := DB.Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, file_lock_enabled)
		VALUES ('demo', 'example.com', 'active', 'wp_demo', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 1)`); err != nil {
		t.Fatalf("insert legacy file-lock-enabled site: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'file_lock_enabled_at'").Scan(&exists); err != nil {
		t.Fatalf("query file_lock_enabled_at: %v", err)
	}
	if exists != 1 {
		t.Fatalf("file_lock_enabled_at exists = %d, want 1", exists)
	}
	var enabledAt string
	if err := DB.QueryRow("SELECT file_lock_enabled_at FROM websites WHERE domain = 'example.com'").Scan(&enabledAt); err != nil {
		t.Fatalf("query example site lock time: %v", err)
	}
	if enabledAt == "" {
		t.Fatalf("legacy file-lock-enabled site should backfill file_lock_enabled_at")
	}
}

func TestUpgradeAddsFileSecurityEventsTableToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.21')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("DROP TABLE IF EXISTS file_security_events"); err != nil {
		t.Fatalf("drop file_security_events: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var tableExists, uniqueIndexExists, lastSeenIndexExists, siteIndexExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'file_security_events'").Scan(&tableExists); err != nil {
		t.Fatalf("query file_security_events table: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_file_security_events_unique'").Scan(&uniqueIndexExists); err != nil {
		t.Fatalf("query file_security_events unique index: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_file_security_events_last_seen'").Scan(&lastSeenIndexExists); err != nil {
		t.Fatalf("query file_security_events last_seen index: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_file_security_events_site'").Scan(&siteIndexExists); err != nil {
		t.Fatalf("query file_security_events site index: %v", err)
	}
	if tableExists != 1 || uniqueIndexExists != 1 || lastSeenIndexExists != 1 || siteIndexExists != 1 {
		t.Fatalf("file_security_events table/index exists = %d/%d/%d/%d, want 1/1/1/1", tableExists, uniqueIndexExists, lastSeenIndexExists, siteIndexExists)
	}
}

func TestUpgradeAddsWPSecurityEventsTablesToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.26')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("DROP TABLE IF EXISTS wp_security_events"); err != nil {
		t.Fatalf("drop wp_security_events: %v", err)
	}
	if _, err := DB.Exec("DROP TABLE IF EXISTS wp_security_log_positions"); err != nil {
		t.Fatalf("drop wp_security_log_positions: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var eventsTableExists, positionsTableExists, ipTypeIndexExists, siteIndexExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'wp_security_events'").Scan(&eventsTableExists); err != nil {
		t.Fatalf("query wp_security_events table: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'wp_security_log_positions'").Scan(&positionsTableExists); err != nil {
		t.Fatalf("query wp_security_log_positions table: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_wp_security_events_ip_type_time'").Scan(&ipTypeIndexExists); err != nil {
		t.Fatalf("query wp_security_events ip_type index: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_wp_security_events_site_time'").Scan(&siteIndexExists); err != nil {
		t.Fatalf("query wp_security_events site index: %v", err)
	}
	if eventsTableExists != 1 || positionsTableExists != 1 || ipTypeIndexExists != 1 || siteIndexExists != 1 {
		t.Fatalf("wp_security_events tables/index exist = %d/%d/%d/%d, want 1/1/1/1", eventsTableExists, positionsTableExists, ipTypeIndexExists, siteIndexExists)
	}
}

func TestFreshInstallHasWPSecurityEventsTables(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}

	var eventsTableExists, positionsTableExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'wp_security_events'").Scan(&eventsTableExists); err != nil {
		t.Fatalf("query wp_security_events table: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'wp_security_log_positions'").Scan(&positionsTableExists); err != nil {
		t.Fatalf("query wp_security_log_positions table: %v", err)
	}
	if eventsTableExists != 1 || positionsTableExists != 1 {
		t.Fatalf("fresh install wp_security_events tables exist = %d/%d, want 1/1", eventsTableExists, positionsTableExists)
	}
}

func TestWPSecurityAlertRulesDefaultDisabledOnFreshInstallAndUpgrade(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}

	for _, key := range []string{"alert_wp_sqli_probe", "alert_wp_fake_search_bot"} {
		var v string
		if err := DB.QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", key).Scan(&v); err != nil {
			t.Fatalf("fresh install missing %s: %v", key, err)
		}
		if v != "false" {
			t.Fatalf("fresh install %s = %q, want %q (must default off)", key, v, "false")
		}
	}

	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.27')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("DELETE FROM security_settings WHERE skey IN ('alert_wp_sqli_probe', 'alert_wp_fake_search_bot')"); err != nil {
		t.Fatalf("delete alert rows: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	for _, key := range []string{"alert_wp_sqli_probe", "alert_wp_fake_search_bot"} {
		var v string
		if err := DB.QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", key).Scan(&v); err != nil {
			t.Fatalf("upgraded install missing %s: %v", key, err)
		}
		if v != "false" {
			t.Fatalf("upgraded install %s = %q, want %q (must default off)", key, v, "false")
		}
	}
}

func TestUpgradeAddsWPSecurityAlertThresholdSettings(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.28')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("DELETE FROM security_settings WHERE skey IN ('alert_wp_security_threshold', 'alert_wp_security_window_hours')"); err != nil {
		t.Fatalf("delete threshold rows: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	for _, setting := range []struct {
		key  string
		want string
	}{
		{"alert_wp_security_threshold", "10"},
		{"alert_wp_security_window_hours", "24"},
	} {
		var got string
		if err := DB.QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", setting.key).Scan(&got); err != nil {
			t.Fatalf("upgraded install missing %s: %v", setting.key, err)
		}
		if got != setting.want {
			t.Fatalf("upgraded install %s = %q, want %q", setting.key, got, setting.want)
		}
	}

	var version string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != LatestVersion() {
		t.Fatalf("schema_version = %q, want %q", version, LatestVersion())
	}
}

func TestFreshInstallHasWPSecurityAlertThresholdSettings(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}

	for _, setting := range []struct {
		key  string
		want string
	}{
		{"alert_wp_security_threshold", "10"},
		{"alert_wp_security_window_hours", "24"},
	} {
		var got string
		if err := DB.QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", setting.key).Scan(&got); err != nil {
			t.Fatalf("fresh install missing %s: %v", setting.key, err)
		}
		if got != setting.want {
			t.Fatalf("fresh install %s = %q, want %q", setting.key, got, setting.want)
		}
	}
}

func TestUpgradeAddsFileBackupsTableToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.23')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("DROP TABLE IF EXISTS file_backups"); err != nil {
		t.Fatalf("drop file_backups: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var tableExists, indexExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'file_backups'").Scan(&tableExists); err != nil {
		t.Fatalf("query file_backups table: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_file_backups_site'").Scan(&indexExists); err != nil {
		t.Fatalf("query file_backups index: %v", err)
	}
	if tableExists != 1 || indexExists != 1 {
		t.Fatalf("file_backups table/index exists = %d/%d, want 1/1", tableExists, indexExists)
	}
}

func TestUpgradeChainReachesFileBackupsBackfillFromExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.24')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	insertMinimalWebsiteForBackfill(t, 1, "chain.example.com")

	// 走真实入口 RunUpgrades()，而不是直接调用 backfillFileBackupsFromRoot，
	// 确保 1.0.25 这一步真的会被执行到（生产环境正是通过这条链路触发回填）。
	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() from 1.0.24 error = %v", err)
	}

	var version string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != LatestVersion() {
		t.Fatalf("schema_version = %q, want %q (RunUpgrades must execute through the latest step)", version, LatestVersion())
	}

	// 生产环境的 backfillFileBackupsFromDisk 固定读取 config.DefaultBackupDir，测试环境里这个
	// 目录不存在，所以升级步骤应该安全跳过（不报错、不插入任何记录），而不是失败。
	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM file_backups`).Scan(&count); err != nil {
		t.Fatalf("count file_backups: %v", err)
	}
	if count != 0 {
		t.Fatalf("file_backups count = %d, want 0 (config.DefaultBackupDir does not exist in test env)", count)
	}
}

func TestFreshInstallHasFileBackupsTable(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}

	var tableExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'file_backups'").Scan(&tableExists); err != nil {
		t.Fatalf("query file_backups table: %v", err)
	}
	if tableExists != 1 {
		t.Fatalf("file_backups table exists = %d, want 1 on fresh install", tableExists)
	}
}

func insertMinimalWebsiteForBackfill(t *testing.T, id int, domain string) {
	t.Helper()
	if _, err := DB.Exec(`INSERT INTO websites (id, name, domain, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES (?, 'site', ?, 'u1', ?, ?, 'db1', 'u1', '/p', '/n')`,
		id, domain, "/www/wwwroot/"+domain, "/www/wwwlogs/"+domain); err != nil {
		t.Fatalf("insert website: %v", err)
	}
}

func TestBackfillFileBackupsFromRootInsertsUntrackedFiles(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	insertMinimalWebsiteForBackfill(t, 1, "backfill.example.com")

	root := t.TempDir()
	filesDir := filepath.Join(root, "backfill.example.com", "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatalf("mkdir files dir: %v", err)
	}
	full := "file_full_20260101_020000.tar.gz"
	inc := "file_inc_20260102_020000.tar.gz"
	if err := os.WriteFile(filepath.Join(filesDir, full), []byte("full-backup-bytes"), 0644); err != nil {
		t.Fatalf("write full backup fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, inc), []byte("inc"), 0644); err != nil {
		t.Fatalf("write incremental backup fixture: %v", err)
	}
	// 不相关的文件（命名不匹配）应该被忽略
	if err := os.WriteFile(filepath.Join(filesDir, "notes.txt"), []byte("irrelevant"), 0644); err != nil {
		t.Fatalf("write unrelated fixture: %v", err)
	}

	if err := backfillFileBackupsFromRoot(root); err != nil {
		t.Fatalf("backfillFileBackupsFromRoot() error = %v", err)
	}

	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM file_backups WHERE site_id = 1`).Scan(&count); err != nil {
		t.Fatalf("count file_backups: %v", err)
	}
	if count != 2 {
		t.Fatalf("file_backups count = %d, want 2 (unrelated file must be ignored)", count)
	}

	var mode, transportStatus string
	var fileSize int64
	var createdAt string
	if err := DB.QueryRow(`SELECT mode, file_size, transport_status, created_at FROM file_backups WHERE site_id = 1 AND filename = ?`, full).
		Scan(&mode, &fileSize, &transportStatus, &createdAt); err != nil {
		t.Fatalf("query backfilled full backup row: %v", err)
	}
	if mode != "full" {
		t.Fatalf("mode = %q, want %q", mode, "full")
	}
	if fileSize != int64(len("full-backup-bytes")) {
		t.Fatalf("file_size = %d, want %d", fileSize, len("full-backup-bytes"))
	}
	if transportStatus != "local" {
		t.Fatalf("transport_status = %q, want %q", transportStatus, "local")
	}
	if !strings.HasPrefix(createdAt, "2026-01-01") {
		t.Fatalf("created_at = %q, want parsed from filename timestamp (2026-01-01...)", createdAt)
	}

	var incMode string
	if err := DB.QueryRow(`SELECT mode FROM file_backups WHERE site_id = 1 AND filename = ?`, inc).Scan(&incMode); err != nil {
		t.Fatalf("query backfilled incremental backup row: %v", err)
	}
	if incMode != "incremental" {
		t.Fatalf("mode = %q, want %q", incMode, "incremental")
	}
}

func TestBackfillFileBackupsFromRootIsIdempotent(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	insertMinimalWebsiteForBackfill(t, 1, "idempotent.example.com")

	root := t.TempDir()
	filesDir := filepath.Join(root, "idempotent.example.com", "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatalf("mkdir files dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "file_full_20260101_020000.tar.gz"), []byte("x"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := backfillFileBackupsFromRoot(root); err != nil {
			t.Fatalf("backfillFileBackupsFromRoot() call %d error = %v", i, err)
		}
	}

	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM file_backups WHERE site_id = 1`).Scan(&count); err != nil {
		t.Fatalf("count file_backups: %v", err)
	}
	if count != 1 {
		t.Fatalf("file_backups count after running twice = %d, want 1 (must not duplicate)", count)
	}
}

func TestBackfillFileBackupsFromRootSkipsSitesWithoutBackupDir(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	insertMinimalWebsiteForBackfill(t, 1, "no-backups.example.com")

	root := t.TempDir()
	if err := backfillFileBackupsFromRoot(root); err != nil {
		t.Fatalf("backfillFileBackupsFromRoot() error = %v, want nil when a site has no backup directory at all", err)
	}

	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM file_backups`).Scan(&count); err != nil {
		t.Fatalf("count file_backups: %v", err)
	}
	if count != 0 {
		t.Fatalf("file_backups count = %d, want 0", count)
	}
}

func TestBackfillFileBackupsFromRootToleratesMissingTables(t *testing.T) {
	openTempDB(t)
	// 不建 websites / file_backups 表，模拟老得多的 schema_version 起点跑升级链的场景
	// （真实场景见 TestUpgradeAddsBotRateLimitSettingsToExistingSchema，从 1.0.12 开始跑）。
	if err := backfillFileBackupsFromRoot(t.TempDir()); err != nil {
		t.Fatalf("backfillFileBackupsFromRoot() error = %v, want nil when websites/file_backups tables don't exist yet", err)
	}
}

func TestUpgradeAddsDocumentRootSubdirColumnToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.16')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN document_root_subdir"); err != nil {
		t.Fatalf("drop document_root_subdir: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	var defaultValue string
	if err := DB.QueryRow("SELECT COUNT(*), COALESCE(MAX(dflt_value), '') FROM pragma_table_info('websites') WHERE name = 'document_root_subdir'").Scan(&exists, &defaultValue); err != nil {
		t.Fatalf("query document_root_subdir: %v", err)
	}
	if exists != 1 {
		t.Fatalf("document_root_subdir exists = %d, want 1", exists)
	}
	if defaultValue != "''" {
		t.Fatalf("document_root_subdir default = %q, want %q", defaultValue, "''")
	}
}

func TestUpgradeAddsAIMessagesTableToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.18')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("DROP TABLE IF EXISTS ai_messages"); err != nil {
		t.Fatalf("drop ai_messages: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var tableExists, indexExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ai_messages'").Scan(&tableExists); err != nil {
		t.Fatalf("query ai_messages table: %v", err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_ai_messages_session'").Scan(&indexExists); err != nil {
		t.Fatalf("query ai_messages index: %v", err)
	}
	if tableExists != 1 || indexExists != 1 {
		t.Fatalf("ai_messages table/index exists = %d/%d, want 1/1", tableExists, indexExists)
	}
}

func TestUpgradeAddsSSLExportEnabledColumnToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.15')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN ssl_export_enabled"); err != nil {
		t.Fatalf("drop ssl_export_enabled: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'ssl_export_enabled'").Scan(&exists); err != nil {
		t.Fatalf("query ssl_export_enabled: %v", err)
	}
	if exists != 1 {
		t.Fatalf("ssl_export_enabled exists = %d, want 1", exists)
	}
}

func TestUpgradeAddsSSLLastErrorColumnToExistingSchema(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.13')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN ssl_last_error"); err != nil {
		t.Fatalf("drop ssl_last_error: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'ssl_last_error'").Scan(&exists); err != nil {
		t.Fatalf("query ssl_last_error: %v", err)
	}
	if exists != 1 {
		t.Fatalf("ssl_last_error exists = %d, want 1", exists)
	}
}

func TestUpgradeRunnerAdvancesExistingVersion(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.9')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}

	var version string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != LatestVersion() {
		t.Fatalf("version = %q, want %q", version, LatestVersion())
	}
}

func TestUpgradeAddsCDNRealIPColumnToOldSchema(t *testing.T) {
	openTempDB(t)

	if _, err := DB.Exec(`CREATE TABLE websites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		domain TEXT NOT NULL UNIQUE
	)`); err != nil {
		t.Fatalf("create old websites table: %v", err)
	}
	if _, err := DB.Exec(`CREATE TABLE schema_version (
		version TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := DB.Exec(`CREATE TABLE security_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		skey TEXT NOT NULL UNIQUE,
		svalue TEXT NOT NULL DEFAULT '',
		description TEXT DEFAULT '',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create security_settings: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.11')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'cdn_realip_enabled'").Scan(&exists); err != nil {
		t.Fatalf("query cdn_realip_enabled: %v", err)
	}
	if exists != 1 {
		t.Fatalf("cdn_realip_enabled exists = %d, want 1", exists)
	}
}

func TestUpgradeAddsBotRateLimitSettingsToExistingSchema(t *testing.T) {
	openTempDB(t)

	if _, err := DB.Exec(`CREATE TABLE security_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		skey TEXT NOT NULL UNIQUE,
		svalue TEXT NOT NULL DEFAULT '',
		description TEXT DEFAULT '',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create security_settings: %v", err)
	}
	if _, err := DB.Exec(`CREATE TABLE schema_version (
		version TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.12')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	for _, setting := range []struct {
		key  string
		want string
	}{
		{"bot_limit_enabled", "false"},
		{"bot_limit_rpm", "30"},
		{"bot_limit_burst", "20"},
		{"googlebot_ips", ""},
		{"bingbot_ips", ""},
	} {
		var got string
		if err := DB.QueryRow("SELECT svalue FROM security_settings WHERE skey = ?", setting.key).Scan(&got); err != nil {
			t.Fatalf("query %s setting: %v", setting.key, err)
		}
		if got != setting.want {
			t.Fatalf("%s = %q, want %q", setting.key, got, setting.want)
		}
	}
}
