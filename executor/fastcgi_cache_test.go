package executor

import (
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
	activeConf := filepath.Join(sitesAvailable, "active.example.com.conf")
	pausedEnabled := filepath.Join(sitesEnabled, "paused.example.com.conf")
	activeEnabled := filepath.Join(sitesEnabled, "active.example.com.conf")

	const oldPlaceholder = "# stale placeholder config\n"
	if err := os.WriteFile(pausedConf, []byte(oldPlaceholder), 0644); err != nil {
		t.Fatalf("seed paused conf: %v", err)
	}
	if err := os.WriteFile(activeConf, []byte(oldPlaceholder), 0644); err != nil {
		t.Fatalf("seed active conf: %v", err)
	}

	// Paused site: mirrors the state left behind by executePauseSite —
	// the enabled symlink has been removed.
	insertRegenTestWebsite(t, "paused.example.com", pausedConf, "paused")

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
