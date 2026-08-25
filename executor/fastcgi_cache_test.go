package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

// installStubNginx puts a fake "nginx" binary at the front of PATH so
// RegenerateAllSitesNginx can run "nginx -t" / "nginx -s reload" without a
// real Nginx install. It always succeeds and ignores its arguments.
func installStubNginx(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "nginx")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write stub nginx: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func insertRegenTestWebsite(t *testing.T, domain, nginxConfPath, status string) int {
	t.Helper()
	res, err := database.GetDB().Exec(
		`INSERT INTO websites (name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		domain, domain, status, "wp_"+domain, "/www/wwwroot/"+domain, "/www/wwwlogs/"+domain,
		"db_"+domain, "dbuser_"+domain, "/www/server/php/83/etc/php-fpm.d/"+domain+".conf", nginxConfPath,
	)
	if err != nil {
		t.Fatalf("insert website %s: %v", domain, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return int(id)
}

// TestRegenerateAllSitesNginxKeepsPausedSitesDisabled reproduces the bug where
// restarting the panel (e.g. after a self-update) ran RegenerateAllSitesNginx
// for every site regardless of status, which recreated the Nginx
// sites-enabled symlink for paused sites and silently made them reachable
// again while the DB/UI still showed them as paused.
func TestRegenerateAllSitesNginxKeepsPausedSitesDisabled(t *testing.T) {
	openTestDB(t)
	installStubNginx(t)

	baseDir := t.TempDir()
	sitesAvailable := filepath.Join(baseDir, "sites-available")
	sitesEnabled := filepath.Join(baseDir, "sites-enabled")
	backupDir := filepath.Join(baseDir, "backups")
	for _, dir := range []string{sitesAvailable, sitesEnabled, backupDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{
		Panel: config.PanelConfig{BackupDir: backupDir},
		Paths: config.PathsConfig{
			NginxSitesAvailable: sitesAvailable,
			NginxSitesEnabled:   sitesEnabled,
		},
	}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	pausedConf := filepath.Join(sitesAvailable, "paused.example.com.conf")
	migratedConf := filepath.Join(sitesAvailable, "migrated.example.com.conf")
	activeConf := filepath.Join(sitesAvailable, "active.example.com.conf")
	pausedEnabled := filepath.Join(sitesEnabled, "paused.example.com.conf")
	migratedEnabled := filepath.Join(sitesEnabled, "migrated.example.com.conf")
	migrationMaintenance := filepath.Join(sitesAvailable, ".wp-panel-migration-finished.conf")
	activeEnabled := filepath.Join(sitesEnabled, "active.example.com.conf")

	const oldPlaceholder = "# stale placeholder config\n"
	if err := os.WriteFile(pausedConf, []byte(oldPlaceholder), 0644); err != nil {
		t.Fatalf("seed paused conf: %v", err)
	}
	if err := os.WriteFile(activeConf, []byte(oldPlaceholder), 0644); err != nil {
		t.Fatalf("seed active conf: %v", err)
	}
	if err := os.WriteFile(migratedConf, []byte(oldPlaceholder), 0644); err != nil {
		t.Fatalf("seed migrated conf: %v", err)
	}
	if err := os.WriteFile(migrationMaintenance, []byte("return 503;"), 0600); err != nil {
		t.Fatalf("seed migration maintenance: %v", err)
	}

	// Paused site: mirrors the state left behind by executePauseSite —
	// the enabled symlink has been removed.
	insertRegenTestWebsite(t, "paused.example.com", pausedConf, "paused")
	if err := os.Symlink(migrationMaintenance, migratedEnabled); err != nil {
		t.Fatalf("seed migrated enabled symlink: %v", err)
	}
	insertRegenTestWebsite(t, "migrated.example.com", migratedConf, "migrated")

	// Active site: enabled symlink present, as a normal running site would be.
	if err := os.Symlink(activeConf, activeEnabled); err != nil {
		t.Fatalf("seed active enabled symlink: %v", err)
	}
	insertRegenTestWebsite(t, "active.example.com", activeConf, "active")

	if err := RegenerateAllSitesNginx(); err != nil {
		t.Fatalf("RegenerateAllSitesNginx: %v", err)
	}

	// The paused site must stay disabled: no sites-enabled symlink...
	if _, err := os.Lstat(pausedEnabled); !os.IsNotExist(err) {
		t.Fatalf("expected paused site to remain without an enabled symlink, lstat err = %v", err)
	}
	// ...but its on-disk config should still have been refreshed with the
	// latest template, so re-enabling later serves the current rules.
	pausedContent, err := os.ReadFile(pausedConf)
	if err != nil {
		t.Fatalf("read paused conf: %v", err)
	}
	if strings.Contains(string(pausedContent), oldPlaceholder) || !strings.Contains(string(pausedContent), "paused.example.com") {
		t.Fatalf("expected paused site config to be refreshed with rendered template, got:\n%s", pausedContent)
	}
	migratedTarget, err := os.Readlink(migratedEnabled)
	if err != nil || filepath.Clean(migratedTarget) != filepath.Clean(migrationMaintenance) {
		t.Fatalf("migrated maintenance target=%q err=%v", migratedTarget, err)
	}
	migratedContent, err := os.ReadFile(migratedConf)
	if err != nil || strings.Contains(string(migratedContent), oldPlaceholder) || !strings.Contains(string(migratedContent), "migrated.example.com") {
		t.Fatalf("migrated inactive config was not refreshed: err=%v content=%s", err, migratedContent)
	}

	// The active site must remain enabled and pointing at its config.
	target, err := os.Readlink(activeEnabled)
	if err != nil {
		t.Fatalf("expected active site to keep an enabled symlink: %v", err)
	}
	if target != activeConf {
		t.Fatalf("active enabled symlink target = %q, want %q", target, activeConf)
	}
	activeContent, err := os.ReadFile(activeConf)
	if err != nil {
		t.Fatalf("read active conf: %v", err)
	}
	if strings.Contains(string(activeContent), oldPlaceholder) || !strings.Contains(string(activeContent), "active.example.com") {
		t.Fatalf("expected active site config to be refreshed with rendered template, got:\n%s", activeContent)
	}
}

func TestRegenerateSiteNginxPreservesActiveTargetMigrationMarker(t *testing.T) {
	openTestDB(t)
	installStubNginx(t)

	baseDir := t.TempDir()
	sitesAvailable := filepath.Join(baseDir, "sites-available")
	sitesEnabled := filepath.Join(baseDir, "sites-enabled")
	backupDir := filepath.Join(baseDir, "backups")
	for _, dir := range []string{sitesAvailable, sitesEnabled, backupDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{
		Panel: config.PanelConfig{BackupDir: backupDir},
		Paths: config.PathsConfig{
			NginxSitesAvailable: sitesAvailable,
			NginxSitesEnabled:   sitesEnabled,
		},
	}
	t.Cleanup(func() { config.AppConfig = oldConfig })

	const domain = "target-migration.example.com"
	nginxConf := filepath.Join(sitesAvailable, domain+".conf")
	enabledPath := filepath.Join(sitesEnabled, domain+".conf")
	const markerToken = "marker_0000000000000000000000000000000000000000"
	const outboundCredential = "credential_0000000000000000000000000000000000000000"
	marker := SiteMigrationMarkerResponse{Task: "migration_0000001", Role: "target", IssuedAt: 123}
	marker.Signature = signSiteMigrationMarker(hashMigrationSecret(outboundCredential), markerToken, marker)
	markerPayload, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	markerBlock := renderSiteMigrationTargetMarkerBlock(markerToken, string(markerPayload))
	markerConfig, err := injectSiteMigrationTargetMarker("server {\n}\n", markerBlock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nginxConf, []byte(markerConfig), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nginxConf, enabledPath); err != nil {
		t.Fatal(err)
	}
	siteID := insertRegenTestWebsite(t, domain, nginxConf, "active")
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO site_migration_peers(id,status,outbound_credential,protocol_version) VALUES ('peer_00000000001','paired',?,?)`, outboundCredential, siteMigrationProtocolVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_batches(id,peer_id,direction,status) VALUES ('batch_0000000001','peer_00000000001','target','active')`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(map[string]any{
		"runtime_settings": map[string]any{"marker_token": markerToken},
		"target_spec": map[string]any{
			"nginx_conf_path": nginxConf, "nginx_enabled_path": enabledPath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_sites(id,batch_id,target_site_id,source_domain,target_domain,site_type,status,stage,settings_snapshot)
		VALUES ('migration_0000001','batch_0000000001',?,?,?,'wordpress','awaiting_cutover','awaiting_cutover',?)`, siteID, domain, domain, string(snapshot)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status)
		VALUES (?,?,'migration_0000001','target','active')`, domain, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_resources(migration_site_id,resource_type,identifier,ownership_tag,status)
		VALUES ('migration_0000001','target_staging_root',?,'migration_0000001','created'),
		('migration_0000001','target_marker_config',?,'migration_0000001','created')`, filepath.Join(baseDir, "staging"), nginxConf); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_migration_events(migration_site_id,stage,result,message)
		VALUES ('migration_0000001','target_marker','info',?)`, string(markerPayload)); err != nil {
		t.Fatal(err)
	}

	if err := RegenerateSiteNginx(siteID); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(nginxConf)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) == markerConfig || !strings.Contains(string(content), markerBlock) {
		t.Fatalf("active target migration config was overwritten:\n%s", content)
	}
	target, err := os.Readlink(enabledPath)
	if err != nil || target != nginxConf {
		t.Fatalf("enabled target=%q err=%v", target, err)
	}
	if _, err := db.Exec(`UPDATE site_migration_sites SET status='cleanup_failed',stage='cancelling' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if err := RegenerateSiteNginx(siteID); err == nil {
		t.Fatal("cancelling target migration marker was restored")
	}
	afterCancelling, err := os.ReadFile(nginxConf)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCancelling) != string(content) {
		t.Fatal("rejected cancelling marker recovery changed the active config")
	}
	if _, err := db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='awaiting_cutover' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE site_migration_events SET message='{"task":"migration_0000001","role":"target","issued_at":123,"signature":"tampered"}'
		WHERE migration_site_id='migration_0000001' AND stage='target_marker'`); err != nil {
		t.Fatal(err)
	}
	if err := RegenerateSiteNginx(siteID); err == nil {
		t.Fatal("tampered target marker intent was accepted")
	}
	afterRejected, err := os.ReadFile(nginxConf)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRejected) != string(content) {
		t.Fatal("failed target marker recovery changed the active config")
	}
}
