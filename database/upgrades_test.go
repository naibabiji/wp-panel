package database

import (
	"path/filepath"
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

	for _, col := range []string{"php_pool_path", "nginx_conf_path", "wp_memory_limit", "cdn_realip_enabled", "ssl_last_error", "ssl_export_enabled", "document_root_subdir"} {
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
