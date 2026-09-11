package main

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

// Explicit root-only test, confined to temporary files and SQLite. No service,
// system user/group, production WebRoot or WordPress database is modified.
func TestLockedCompanionUpgradeRealPermissions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root; run the compiled test against temporary fixtures")
	}
	u, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal(err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"standard", "strict"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "webroot")
			plugin := filepath.Join(root, "wp-content", "plugins", "wp-panel-optimizer")
			if err := os.MkdirAll(plugin, 0755); err != nil {
				t.Fatal(err)
			}
			old := []byte("<?php\n/* Plugin Name: WP Panel Optimizer\nVersion: 1.1.12\n*/\n")
			if err := os.WriteFile(filepath.Join(plugin, "wp-panel-optimizer.php"), old, 0444); err != nil {
				t.Fatal(err)
			}
			cfg := []byte("<?php\ndefine('DISALLOW_FILE_MODS', true);\n")
			configPath := filepath.Join(root, "wp-config.php")
			if err := os.WriteFile(configPath, cfg, 0440); err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if err := os.Chown(path, 0, gid); err != nil {
					return err
				}
				if d.IsDir() {
					return os.Chmod(path, 0555)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(configPath)
			if err := database.Open(filepath.Join(base, "panel.db")); err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := database.RunMigrations(); err != nil {
				t.Fatal(err)
			}
			key := strings.Repeat("a", 64)
			_, err := database.GetDB().Exec(`INSERT INTO websites(name,domain,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,plugin_api_key,file_lock_enabled,file_lock_mode,file_lock_apply_status) VALUES ('locked','locked.example','nobody',?,'','','','','',?,1,?,'ready')`, root, key, mode)
			if err != nil {
				t.Fatal(err)
			}
			executor.AutoDeployPluginUpdates(PluginFS)
			header, err := os.ReadFile(filepath.Join(plugin, "wp-panel-optimizer.php"))
			expected, sourceErr := PluginFS.ReadFile("wp-panel-optimizer/wp-panel-optimizer.php")
			if err != nil || sourceErr != nil || string(header) != string(expected) {
				t.Fatalf("old locked site did not upgrade: %v", err)
			}
			for _, rel := range []string{"includes/trait-maintenance.php", "assets/maintenance.js", "assets/maintenance.css"} {
				if _, err := os.Stat(filepath.Join(plugin, rel)); err != nil {
					t.Fatal(err)
				}
			}
			if err := filepath.WalkDir(plugin, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				stat := info.Sys().(*syscall.Stat_t)
				want := os.FileMode(0444)
				if d.IsDir() {
					want = 0555
				}
				if info.Mode().Perm() != want || stat.Uid != 0 || int(stat.Gid) != gid {
					t.Errorf("unsafe published file %s: mode=%o uid=%d gid=%d", path, info.Mode().Perm(), stat.Uid, stat.Gid)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(configPath)
			content, _ := os.ReadFile(configPath)
			if string(content) != string(cfg) || !os.SameFile(before, after) || after.Mode() != before.Mode() {
				t.Fatal("whole-site lock/config changed")
			}
			var enabled bool
			var gotMode, gotKey string
			if err := database.GetDB().QueryRow(`SELECT file_lock_enabled,file_lock_mode,plugin_api_key FROM websites WHERE id=1`).Scan(&enabled, &gotMode, &gotKey); err != nil {
				t.Fatal(err)
			}
			if !enabled || gotMode != mode || gotKey != key {
				t.Fatal("lock mode or identity changed")
			}
			if err := executor.VerifySiteFileLockMode(&models.Website{WebRoot: root, SystemUser: "nobody"}, mode); err != nil {
				t.Fatalf("locked site verification failed: %v", err)
			}
			first, _ := os.Stat(plugin)
			executor.AutoDeployPluginUpdates(PluginFS)
			second, _ := os.Stat(plugin)
			if !os.SameFile(first, second) {
				t.Fatal("unchanged version redeployed")
			}
			// Even trusted deployment must not bypass an unfinished window.
			if err := os.WriteFile(filepath.Join(plugin, ".wpp-version"), []byte("old"), 0444); err != nil {
				t.Fatal(err)
			}
			if _, err := database.GetDB().Exec(`UPDATE websites SET maintenance_security='{"window":{"id":"unfinished","state":"relocking"}}'`); err != nil {
				t.Fatal(err)
			}
			executor.AutoDeployPluginUpdates(PluginFS)
			third, _ := os.Stat(plugin)
			if !os.SameFile(second, third) {
				t.Fatal("deployment bypassed maintenance window")
			}
		})
	}
}
