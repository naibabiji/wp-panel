package executor

import (
	"context"
	"strings"
	"testing"
)

func TestSiteMigrationSettingsSourceCapturesRuntimeWithoutSecrets(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET stage='source_frozen',settings_snapshot=? WHERE id='migration_0000001'`, `{"source_marker_token":"`+strings.Repeat("m", 48)+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status) VALUES ('example.com',1,'migration_0000001','source','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET aliases='WWW.example.com',ssl_enabled=1,fastcgi_cache_enabled=1,fastcgi_cache_ttl=900,monitoring_enabled=1,log_retention_days=0,php_fpm_max_children=17 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO cron_jobs (name,cron_expression,command,site_id,run_as_user,task_type,enabled) VALUES ('WP Cron','*/5 * * * *','example.com',1,'wp_example','wp_cron',1),('Custom','0 * * * *','echo unsafe',1,'wp_example','command',1)`); err != nil {
		t.Fatal(err)
	}
	service, _ := NewSiteMigrationSettingsService(store.db)
	settings, err := service.SourceSettings(context.Background(), "migration_0000001")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(settings.Aliases, ",") != "www.example.com" || !settings.SSLEnabled || !settings.FastCGICacheEnabled || settings.FastCGICacheTTL != 900 || !settings.MonitoringEnabled || settings.LogRetentionDays != 0 || settings.PHPFPMMaxChildren != 17 || len(settings.CronJobs) != 1 || settings.CronJobs[0].TaskType != "wp_cron" || settings.SkippedCustomCommands != 1 {
		t.Fatalf("settings=%+v", settings)
	}
}

func TestSiteMigrationRuntimeSettingsRejectsNegativeLogRetention(t *testing.T) {
	settings := SiteMigrationRuntimeSettings{MarkerToken: strings.Repeat("m", 48), FastCGICacheTTL: 300, MonitoringInterval: 5, LogRetentionDays: -1, PHPFPMMaxChildren: 2}
	if err := validateSiteMigrationRuntimeSettings(&settings); err == nil {
		t.Fatal("negative log retention must be rejected")
	}
}

func TestSiteMigrationSettingsTargetNormalizesAndPreservesSnapshot(t *testing.T) {
	service, _, _ := setupMigrationTargetResourceServiceTest(t)
	settingsService, _ := NewSiteMigrationSettingsService(service.db)
	settingsService.now = freezerTestTime
	settings := SiteMigrationRuntimeSettings{Aliases: []string{"WWW.example.com"}, TemplateVersion: "v1.0", AccessLogMode: "error_only", FastCGICacheTTL: 300, MonitoringInterval: 5, WPPostRevisions: -1, PasswordResetMode: "allow", LogRetentionDays: 7, PHPFPMMaxChildren: 10, MarkerToken: strings.Repeat("m", 48)}
	if err := settingsService.StoreTargetSettings(context.Background(), "migration_0000001", settings); err != nil {
		t.Fatal(err)
	}
	var snapshot string
	_ = service.db.QueryRow(`SELECT settings_snapshot FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&snapshot)
	if !strings.Contains(snapshot, `"existing":"kept"`) || !strings.Contains(snapshot, `"aliases":["www.example.com"]`) || strings.Contains(snapshot, `"password":`) || strings.Contains(snapshot, `"api_key":`) {
		t.Fatalf("snapshot=%s", snapshot)
	}
}
