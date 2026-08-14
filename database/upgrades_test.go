package database

import (
	"fmt"
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

	var oomTableExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'system_oom_events'").Scan(&oomTableExists); err != nil {
		t.Fatalf("query system_oom_events: %v", err)
	}
	if oomTableExists != 1 {
		t.Fatalf("system_oom_events exists = %d, want 1", oomTableExists)
	}
	var remoteStateTableExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'remote_backup_site_state'").Scan(&remoteStateTableExists); err != nil {
		t.Fatalf("query remote_backup_site_state: %v", err)
	}
	if remoteStateTableExists != 1 {
		t.Fatalf("remote_backup_site_state exists = %d, want 1", remoteStateTableExists)
	}
	var oomAlertEnabled string
	if err := DB.QueryRow("SELECT svalue FROM security_settings WHERE skey = 'alert_oom'").Scan(&oomAlertEnabled); err != nil {
		t.Fatalf("query alert_oom: %v", err)
	}
	if oomAlertEnabled != "true" {
		t.Fatalf("alert_oom = %q, want true", oomAlertEnabled)
	}

	for _, col := range []string{"php_pool_path", "nginx_conf_path", "wp_memory_limit", "file_lock_enabled", "file_lock_enabled_at", "file_lock_mode", "file_lock_apply_status", "cdn_realip_enabled", "ssl_last_error", "ssl_export_enabled", "document_root_subdir", "password_reset_mode"} {
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
	var aiTimeout int
	if err := DB.QueryRow("SELECT timeout_seconds FROM ai_settings WHERE id = 1").Scan(&aiTimeout); err != nil || aiTimeout != 180 {
		t.Fatalf("ai default timeout = %d, want 180, err=%v", aiTimeout, err)
	}
	for _, table := range []string{"ai_settings", "ai_sessions", "ai_messages", "ai_tool_events", "ai_ip_aliases"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&exists); err != nil {
			t.Fatalf("query %s table: %v", table, err)
		}
		if exists != 1 {
			t.Fatalf("%s exists = %d, want 1", table, exists)
		}
	}
	for _, column := range []string{"context_type", "context_id", "context_json", "focus_kind", "focus_value"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('ai_sessions') WHERE name = ?", column).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("ai_sessions.%s exists=%d err=%v", column, exists, err)
		}
	}
	var fileSecurityEventsTable int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'file_security_events'").Scan(&fileSecurityEventsTable); err != nil {
		t.Fatalf("query file_security_events table: %v", err)
	}
	if fileSecurityEventsTable != 1 {
		t.Fatalf("file_security_events exists = %d, want 1", fileSecurityEventsTable)
	}
	for _, table := range []string{
		"site_wp_inventory_state",
		"site_wp_components",
		"site_wp_component_updates",
		"site_wp_inventory_jobs",
		"site_wp_inventory_job_warnings",
		"wp_update_tasks",
		"wp_update_task_events",
		"wp_update_task_backups",
	} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&exists); err != nil {
			t.Fatalf("query %s table: %v", table, err)
		}
		if exists != 1 {
			t.Fatalf("%s exists = %d, want 1", table, exists)
		}
	}
	for _, index := range []string{"ix_wp_update_tasks_finished_at"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&exists); err != nil {
			t.Fatalf("query %s index: %v", index, err)
		}
		if exists != 1 {
			t.Fatalf("%s exists = %d, want 1", index, exists)
		}
	}
	for _, col := range []string{"backup_type", "s3_endpoint", "s3_bucket", "s3_region", "s3_access_key_id", "s3_secret_key", "s3_path_prefix", "connection_mode", "server_id", "remote_base_path", "s3_base_prefix", "isolate_path"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('remote_backup_settings') WHERE name = ?", col).Scan(&exists); err != nil {
			t.Fatalf("query remote_backup_settings column %s: %v", col, err)
		}
		if exists != 1 {
			t.Fatalf("remote_backup_settings.%s exists = %d, want 1", col, exists)
		}
	}
	var connectionMode string
	var isolatePath int
	if err := DB.QueryRow(`SELECT connection_mode, isolate_path FROM remote_backup_settings WHERE id=1`).Scan(&connectionMode, &isolatePath); err != nil {
		t.Fatal(err)
	}
	if connectionMode != "auto" || isolatePath != 1 {
		t.Fatalf("fresh remote backup defaults = mode %q isolate %d, want auto/1", connectionMode, isolatePath)
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

func TestUpgrade1046AddsUnifiedAIDiagnosticContext(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DROP TABLE ai_tool_events`); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"focus_value", "focus_kind", "context_json", "context_id", "context_type"} {
		if _, err := DB.Exec("ALTER TABLE ai_sessions DROP COLUMN " + column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	if _, err := DB.Exec(`DELETE FROM schema_version`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO schema_version(version) VALUES ('1.0.45')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"context_type", "context_id", "context_json", "focus_kind", "focus_value"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('ai_sessions') WHERE name=?", column).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("column %s exists=%d err=%v", column, exists, err)
		}
	}
	var tableExists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ai_tool_events'`).Scan(&tableExists); err != nil || tableExists != 1 {
		t.Fatalf("ai_tool_events exists=%d err=%v", tableExists, err)
	}
}

func TestUpgrade1047RaisesLegacyDefaultAITimeout(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`UPDATE ai_settings SET timeout_seconds=60 WHERE id=1; DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES ('1.0.46')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	var timeout int
	if err := DB.QueryRow(`SELECT timeout_seconds FROM ai_settings WHERE id=1`).Scan(&timeout); err != nil || timeout != 180 {
		t.Fatalf("upgraded timeout=%d, want 180, err=%v", timeout, err)
	}
}

func TestUpgrade1048AddsAIIPAliases(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DROP TABLE ai_ip_aliases; DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES ('1.0.47')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ai_ip_aliases'`).Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("ai_ip_aliases exists=%d err=%v", exists, err)
	}
}

func TestUpgradeAddsWPInventorySchemaFrom1030(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}

	if _, err := DB.Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES ('existing.example.com', 'existing.example.com', 'active', 'wp_existing', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf')`); err != nil {
		t.Fatalf("insert existing website: %v", err)
	}
	for _, table := range []string{
		"site_wp_inventory_job_warnings",
		"site_wp_inventory_jobs",
		"site_wp_component_updates",
		"site_wp_components",
		"site_wp_inventory_state",
	} {
		if _, err := DB.Exec("DROP TABLE " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.30')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var version string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query schema version: %v", err)
	}
	if version != LatestVersion() {
		t.Fatalf("schema version = %q, want %s", version, LatestVersion())
	}
	var sites int
	if err := DB.QueryRow("SELECT COUNT(*) FROM websites WHERE domain = 'existing.example.com'").Scan(&sites); err != nil {
		t.Fatalf("query existing website: %v", err)
	}
	if sites != 1 {
		t.Fatalf("existing websites = %d, want 1", sites)
	}
	for _, name := range []string{
		"ux_site_wp_inventory_jobs_active_site",
		"ix_site_wp_inventory_jobs_claim",
		"ix_site_wp_inventory_jobs_finished",
	} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&exists); err != nil {
			t.Fatalf("query %s index: %v", name, err)
		}
		if exists != 1 {
			t.Fatalf("%s exists = %d, want 1", name, exists)
		}
	}
}

func TestUpgradeAddsWPUpdateSchemaFrom1031(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"wp_update_task_events", "wp_update_task_backups", "wp_update_tasks"} {
		if _, err := DB.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version(version) VALUES ('1.0.31')"); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("repeated upgrade: %v", err)
	}
	for _, table := range []string{"wp_update_tasks", "wp_update_task_events", "wp_update_task_backups"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("table %s exists=%d err=%v", table, exists, err)
		}
	}
	if got := LatestVersion(); got != "1.0.48" {
		t.Fatalf("LatestVersion=%q", got)
	}
	for _, column := range []string{"database_backup_mode", "database_backup_source_id", "auto_rollback", "batch_id"} {
		var exists int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('wp_update_tasks') WHERE name=?`, column).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("column %s exists=%d err=%v", column, exists, err)
		}
	}
}

func TestUpgradeAddsRemoteBackupIsolationWithoutChangingLegacyTarget(t *testing.T) {
	openTempDB(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}
	if _, err := DB.Exec(`UPDATE remote_backup_settings SET username='wpbackup', auth_type='password', password='secret', remote_path='/mnt/backup/old', s3_path_prefix='legacy-prefix' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"connection_mode", "server_id", "remote_base_path", "s3_base_prefix", "isolate_path"} {
		if _, err := DB.Exec("ALTER TABLE remote_backup_settings DROP COLUMN " + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version(version) VALUES ('1.0.41')"); err != nil {
		t.Fatal(err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}

	var mode, username, authType, password, remotePath, s3Prefix string
	if err := DB.QueryRow(`SELECT connection_mode, username, auth_type, password, remote_path, s3_path_prefix FROM remote_backup_settings WHERE id=1`).Scan(&mode, &username, &authType, &password, &remotePath, &s3Prefix); err != nil {
		t.Fatal(err)
	}
	if mode != "legacy" || username != "wpbackup" || authType != "password" || password != "secret" || remotePath != "/mnt/backup/old" || s3Prefix != "legacy-prefix" {
		t.Fatalf("legacy remote backup changed after upgrade: mode=%q username=%q auth=%q password=%q path=%q s3=%q", mode, username, authType, password, remotePath, s3Prefix)
	}
}

func TestUpgrade1043AddsRemoteBackupSiteState(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DROP TABLE remote_backup_site_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DELETE FROM schema_version`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO schema_version(version) VALUES ('1.0.42')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() from 1.0.42: %v", err)
	}
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='remote_backup_site_state'`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 1 {
		t.Fatalf("remote_backup_site_state exists=%d, want 1", exists)
	}
}

func TestUpgrade1041BackfillsInventoryJobPriorities(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DROP INDEX ix_site_wp_inventory_jobs_claim`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`ALTER TABLE site_wp_inventory_jobs DROP COLUMN priority`); err != nil {
		t.Fatal(err)
	}
	for id, trigger := range []string{"update_followup", "site_created", "manual", "scheduled"} {
		siteID := id + 1
		if _, err := DB.Exec(`INSERT INTO websites (id,name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES (?,?,?,'active',?,'/tmp/www','/tmp/log','db','user','/tmp/php','/tmp/nginx')`, siteID, trigger, trigger+".example", "wp_"+trigger); err != nil {
			t.Fatal(err)
		}
		if _, err := DB.Exec(`INSERT INTO site_wp_inventory_jobs (id,site_id,trigger_type,status,requested_at,not_before) VALUES (?,?,?,'queued','2026-08-01 00:00:00','2026-08-01 00:00:00')`, fmt.Sprintf("%032d", siteID), siteID, trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DB.Exec(`DELETE FROM schema_version`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO schema_version(version) VALUES ('1.0.40')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	wants := map[string]int{"update_followup": 0, "site_created": 10, "manual": 20, "scheduled": 30}
	for trigger, want := range wants {
		var got int
		if err := DB.QueryRow(`SELECT priority FROM site_wp_inventory_jobs WHERE trigger_type=?`, trigger).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s priority=%d want=%d", trigger, got, want)
		}
	}
	var indexSQL string
	if err := DB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='ix_site_wp_inventory_jobs_claim'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexSQL, "status, priority, not_before, requested_at") {
		t.Fatalf("claim index=%s", indexSQL)
	}
}

// revertWPUpdateTasksToPre1036Schema 把 wp_update_tasks 还原成 1.0.36（批量插件更新）
// 之前的旧表结构（不含 auto_rollback/batch_id）。auto_rollback/batch_id 是表级 CHECK
// 约束涉及的列，SQLite 的 DROP COLUMN 不支持这种情况，因此用整表重建的方式还原。
func revertWPUpdateTasksToPre1036Schema(t *testing.T) {
	t.Helper()
	for _, table := range []string{"wp_update_batches", "wp_update_batch_items"} {
		if _, err := DB.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS trg_wp_update_tasks_sealed_auto_rollback_immutable`,
		`DROP TRIGGER IF EXISTS trg_wp_update_tasks_sealed_immutable`,
		`DROP TRIGGER IF EXISTS trg_wp_update_tasks_sealed_backup_mode_immutable`,
		`ALTER TABLE wp_update_tasks RENAME TO wp_update_tasks_new`,
		`CREATE TABLE wp_update_tasks (
			id                    TEXT PRIMARY KEY,
			site_id               INTEGER NOT NULL,
			component_type        TEXT NOT NULL,
			component_key         TEXT NOT NULL DEFAULT 'core',
			task_kind             TEXT NOT NULL DEFAULT 'update',
			parent_task_id         TEXT,
			trigger_type          TEXT NOT NULL DEFAULT 'manual',
			status                TEXT NOT NULL DEFAULT 'preparing',
			stage                 TEXT NOT NULL DEFAULT 'created',
			failure_stage         TEXT NOT NULL DEFAULT '',
			rollback_status       TEXT NOT NULL DEFAULT 'not_required',
			requires_attention    INTEGER NOT NULL DEFAULT 0,
			manual_disposition    TEXT NOT NULL DEFAULT '',
			current_version       TEXT NOT NULL,
			target_version        TEXT NOT NULL,
			package_source        TEXT NOT NULL,
			download_url          TEXT NOT NULL,
			downloaded_sha256     TEXT NOT NULL DEFAULT '',
			verification_level    TEXT NOT NULL DEFAULT '',
			package_snapshot_path TEXT NOT NULL DEFAULT '',
			backup_ready          INTEGER NOT NULL DEFAULT 0,
			database_backup_mode  TEXT NOT NULL DEFAULT 'fresh',
			database_backup_source_id INTEGER,
			plan_sealed_at        DATETIME,
			lease_owner           TEXT NOT NULL DEFAULT '',
			lease_expires_at      DATETIME,
			requested_at          DATETIME NOT NULL,
			started_at            DATETIME,
			finished_at           DATETIME,
			created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK (component_type IN ('core','plugin','theme')),
			CHECK (task_kind IN ('update','rollback')),
			CHECK (trigger_type = 'manual'),
			CHECK (status IN ('preparing','queued','running','success','failed','interrupted_unknown')),
			CHECK (rollback_status IN ('not_required','pending','success','failed')),
			CHECK (requires_attention IN (0,1)),
			CHECK (manual_disposition IN ('','confirmed_target_version','manually_rolled_back','marked_failed_no_action','escalated')),
			CHECK (verification_level IN ('','structure_only','official_verified')),
			CHECK (database_backup_mode IN ('fresh','reuse')),
			CHECK ((task_kind = 'update' AND parent_task_id IS NULL) OR (task_kind = 'rollback' AND parent_task_id IS NOT NULL))
		)`,
		`INSERT INTO wp_update_tasks SELECT id,site_id,component_type,component_key,task_kind,parent_task_id,
			trigger_type,status,stage,failure_stage,rollback_status,requires_attention,manual_disposition,
			current_version,target_version,package_source,download_url,downloaded_sha256,verification_level,
			package_snapshot_path,backup_ready,database_backup_mode,database_backup_source_id,
			plan_sealed_at,lease_owner,lease_expires_at,requested_at,started_at,finished_at,created_at,updated_at
			FROM wp_update_tasks_new`,
		`DROP TABLE wp_update_tasks_new`,
	} {
		if _, err := DB.Exec(stmt); err != nil {
			t.Fatalf("revert to pre-1.0.36 schema (%s): %v", stmt, err)
		}
	}
}

func assertWPUpdateBatchSchemaComplete(t *testing.T) {
	t.Helper()
	for _, column := range []string{"auto_rollback", "batch_id"} {
		var exists int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('wp_update_tasks') WHERE name=?`, column).Scan(&exists); err != nil {
			t.Fatalf("query %s: %v", column, err)
		}
		if exists != 1 {
			t.Fatalf("column %s exists = %d, want 1", column, exists)
		}
	}
	var def string
	if err := DB.QueryRow(`SELECT COALESCE(dflt_value, '') FROM pragma_table_info('wp_update_tasks') WHERE name = 'auto_rollback'`).Scan(&def); err != nil {
		t.Fatalf("query auto_rollback default: %v", err)
	}
	if def != "1" {
		t.Fatalf("auto_rollback default = %q, want %q (existing single-update tasks must keep automatic rollback)", def, "1")
	}
	for _, table := range []string{"wp_update_batches", "wp_update_batch_items"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("table %s exists=%d err=%v", table, exists, err)
		}
	}
	for _, name := range []string{"ix_wp_update_tasks_batch", "trg_wp_update_tasks_sealed_auto_rollback_immutable"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name=?", name).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("object %s exists=%d err=%v", name, exists, err)
		}
	}
}

func TestUpgradeAddsWPUpdateBatchSchemaFrom1035(t *testing.T) {
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
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.35')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	revertWPUpdateTasksToPre1036Schema(t)

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}
	assertWPUpdateBatchSchemaComplete(t)
}

// TestRunMigrationsAloneDoesNotCrashOnPre1036WPUpdateTasksSchema 复现了一次真实的线上
// 事故：面板升级到新二进制后，进程启动时 RunMigrations() 先于 RunUpgrades() 执行；如果
// wp_update_tasks 表是旧版本建的（没有 auto_rollback/batch_id 列），而新增的索引/触发器
// 又被错放进 RunMigrations() 无条件执行的语句列表里，会在字段还没被 RunUpgrades() 的
// ALTER TABLE 补上之前就报 "no such column" 直接崩溃退出，导致面板服务起不来。
// 这里只调用 RunMigrations()，完全不调用 RunUpgrades()，验证它自己就能把
// auto_rollback/batch_id 字段和相关索引/触发器补齐，不会因为顺序问题崩溃。
func TestRunMigrationsAloneDoesNotCrashOnPre1036WPUpdateTasksSchema(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("initial RunMigrations() error = %v", err)
	}
	revertWPUpdateTasksToPre1036Schema(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() must not fail when wp_update_tasks predates auto_rollback/batch_id: %v", err)
	}
	// 幂等性：再跑一次也不能出错（模拟进程反复重启）。
	if err := RunMigrations(); err != nil {
		t.Fatalf("second RunMigrations() error = %v", err)
	}
	assertWPUpdateBatchSchemaComplete(t)
}

func revertWPUpdateBatchItemsToPre1037Schema(t *testing.T) {
	t.Helper()
	for _, stmt := range []string{
		`ALTER TABLE wp_update_batch_items RENAME TO wp_update_batch_items_new`,
		`CREATE TABLE wp_update_batch_items (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			batch_id      TEXT NOT NULL,
			position      INTEGER NOT NULL,
			component_key TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'pending',
			message       TEXT NOT NULL DEFAULT '',
			task_id       TEXT,
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (batch_id) REFERENCES wp_update_batches(id) ON DELETE CASCADE,
			FOREIGN KEY (task_id) REFERENCES wp_update_tasks(id) ON DELETE SET NULL,
			CHECK (status IN ('pending','dispatched','failed')),
			UNIQUE (batch_id, position),
			UNIQUE (batch_id, component_key)
		)`,
		`INSERT INTO wp_update_batch_items SELECT id,batch_id,position,component_key,status,message,task_id,created_at,updated_at
			FROM wp_update_batch_items_new`,
		`DROP TABLE wp_update_batch_items_new`,
		`CREATE INDEX IF NOT EXISTS ix_wp_update_batch_items_batch ON wp_update_batch_items(batch_id, position)`,
	} {
		if _, err := DB.Exec(stmt); err != nil {
			t.Fatalf("revert to pre-1.0.37 schema (%s): %v", stmt, err)
		}
	}
}

func assertWPUpdateBatchRetrySchemaComplete(t *testing.T) {
	t.Helper()
	for _, column := range []string{"retry_count", "next_retry_at"} {
		var exists int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('wp_update_batch_items') WHERE name=?`, column).Scan(&exists); err != nil {
			t.Fatalf("query %s: %v", column, err)
		}
		if exists != 1 {
			t.Fatalf("column %s exists = %d, want 1", column, exists)
		}
	}
	var def string
	if err := DB.QueryRow(`SELECT COALESCE(dflt_value, '') FROM pragma_table_info('wp_update_batch_items') WHERE name = 'retry_count'`).Scan(&def); err != nil {
		t.Fatalf("query retry_count default: %v", err)
	}
	if def != "0" {
		t.Fatalf("retry_count default = %q, want %q", def, "0")
	}
}

func TestUpgradeAddsWPUpdateBatchRetrySchemaFrom1036(t *testing.T) {
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
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.36')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	revertWPUpdateBatchItemsToPre1037Schema(t)

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}
	assertWPUpdateBatchRetrySchemaComplete(t)
}

// TestRunMigrationsAloneDoesNotCrashOnPre1037WPUpdateBatchItemsSchema 验证若
// wp_update_batch_items 表是升级到 1.0.37 之前建的（没有 retry_count/next_retry_at
// 列），单独调用 RunMigrations()（不调用 RunUpgrades()）也不会因为字段缺失而崩溃——
// migrations.go 里的 CREATE TABLE IF NOT EXISTS 对已存在的旧表是空操作，缺失的列
// 需要等 RunUpgrades() 的版本化 ALTER TABLE 补上，这里只确认 RunMigrations() 本身
// 在这种过渡状态下依然能正常启动、不报错。
func TestRunMigrationsAloneDoesNotCrashOnPre1037WPUpdateBatchItemsSchema(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("initial RunMigrations() error = %v", err)
	}
	revertWPUpdateBatchItemsToPre1037Schema(t)

	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() must not fail when wp_update_batch_items predates retry_count/next_retry_at: %v", err)
	}
	if err := RunMigrations(); err != nil {
		t.Fatalf("second RunMigrations() error = %v", err)
	}
}

func TestUpgradeAddsPasswordResetModeColumnFrom1033(t *testing.T) {
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
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.33')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("ALTER TABLE websites DROP COLUMN password_reset_mode"); err != nil {
		t.Fatalf("drop password_reset_mode: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'password_reset_mode'").Scan(&exists); err != nil {
		t.Fatalf("query password_reset_mode: %v", err)
	}
	if exists != 1 {
		t.Fatalf("password_reset_mode exists = %d, want 1 after upgrade from 1.0.33", exists)
	}

	// 旧站点默认值必须与新装迁移一致，否则既有站点升级后行为漂移。
	var def string
	if err := DB.QueryRow("SELECT COALESCE(dflt_value, '') FROM pragma_table_info('websites') WHERE name = 'password_reset_mode'").Scan(&def); err != nil {
		t.Fatalf("query password_reset_mode default: %v", err)
	}
	if def != "'allow'" {
		t.Fatalf("password_reset_mode default = %q, want %q", def, "'allow'")
	}

	var version string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != LatestVersion() {
		t.Fatalf("schema_version = %q, want %q", version, LatestVersion())
	}
}

func TestUpgradeAddsWPUpdateTasksFinishedAtIndexFrom1034(t *testing.T) {
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
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.34')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}
	if _, err := DB.Exec("DROP INDEX IF EXISTS ix_wp_update_tasks_finished_at"); err != nil {
		t.Fatalf("drop index: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	var exists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'ix_wp_update_tasks_finished_at'").Scan(&exists); err != nil {
		t.Fatalf("query index: %v", err)
	}
	if exists != 1 {
		t.Fatalf("ix_wp_update_tasks_finished_at exists = %d, want 1 after upgrade from 1.0.34", exists)
	}

	var version string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != LatestVersion() {
		t.Fatalf("schema_version = %q, want %q", version, LatestVersion())
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
