package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

func TestIsPathWithinRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "wp-content")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}

	if !isPathWithinRoot(root, inside) {
		t.Fatal("inside path should be allowed")
	}
	if isPathWithinRoot(root, outside) {
		t.Fatal("outside path should be rejected")
	}
}

func TestChownSitePathRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "wp-content")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		path       string
		root       string
		systemUser string
	}{
		{name: "empty path", path: "", root: root, systemUser: "wp_site"},
		{name: "root path", path: string(filepath.Separator), root: root, systemUser: "wp_site"},
		{name: "empty allowed root", path: inside, root: "", systemUser: "wp_site"},
		{name: "unsafe allowed root", path: inside, root: string(filepath.Separator), systemUser: "wp_site"},
		{name: "outside allowed root", path: outside, root: root, systemUser: "wp_site"},
		{name: "empty system user", path: inside, root: root, systemUser: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ChownSitePath(tt.path, tt.root, tt.systemUser); err == nil {
				t.Fatal("ChownSitePath error = nil, want rejection")
			}
		})
	}
}

func TestApplyWPFileModsLockBlockAddsAndRemovesManagedBlock(t *testing.T) {
	content := "<?php\n" +
		"define('DB_NAME', 'wordpress');\n" +
		"/* That's all, stop editing! Happy publishing. */\n" +
		"require_once ABSPATH . 'wp-settings.php';\n"

	locked, err := applyWPFileModsLockBlock(content, true)
	if err != nil {
		t.Fatalf("apply lock: %v", err)
	}
	if !strings.Contains(locked, wpPanelFileLockBegin) || !strings.Contains(locked, "define('DISALLOW_FILE_MODS', true);") || !strings.Contains(locked, "define('FS_METHOD', 'direct');") {
		t.Fatalf("managed lock block missing:\n%s", locked)
	}
	if strings.Index(locked, wpPanelFileLockBegin) > strings.Index(locked, "/* That's all, stop editing!") {
		t.Fatal("managed lock block should be inserted before wp-config marker")
	}

	unlocked, err := applyWPFileModsLockBlock(locked, false)
	if err != nil {
		t.Fatalf("remove lock: %v", err)
	}
	if strings.Contains(unlocked, wpPanelFileLockBegin) || strings.Contains(unlocked, "DISALLOW_FILE_MODS") {
		t.Fatalf("managed lock block was not removed:\n%s", unlocked)
	}
}

func TestApplyWPFileModsLockBlockAddsFSMethodForUserDefinedDisallow(t *testing.T) {
	content := "<?php\n" +
		"define('DISALLOW_FILE_MODS', true);\n"
	locked, err := applyWPFileModsLockBlock(content, true)
	if err != nil {
		t.Fatalf("apply lock: %v", err)
	}
	if strings.Count(locked, "define('DISALLOW_FILE_MODS', true);") != 1 {
		t.Fatalf("managed block should not add duplicate DISALLOW_FILE_MODS: %s", locked)
	}
	if !strings.Contains(locked, "define('FS_METHOD', 'direct');") {
		t.Fatalf("managed block should ensure FS_METHOD direct: %s", locked)
	}
}

func TestApplyWPFileModsLockBlockClearsManagedFSMethodOnUnlock(t *testing.T) {
	content := "<?php\n" +
		"define('DISALLOW_FILE_MODS', true);\n" +
		"define('FS_METHOD', 'ftp');\n" +
		"/* That's all, stop editing! Happy publishing. */\n"
	locked, err := applyWPFileModsLockBlock(content, true)
	if err != nil {
		t.Fatalf("apply lock: %v", err)
	}
	if !strings.Contains(locked, wpPanelFileLockBegin) {
		t.Fatalf("managed block should be present while locked: %s", locked)
	}

	unlocked, err := applyWPFileModsLockBlock(locked, false)
	if err != nil {
		t.Fatalf("remove lock: %v", err)
	}
	if strings.Contains(unlocked, wpPanelFileLockBegin) {
		t.Fatalf("managed block should be removed after unlock: %s", unlocked)
	}
	if strings.Contains(unlocked, "define('FS_METHOD', 'direct');") {
		t.Fatal("managed FS_METHOD direct should be removed after unlock")
	}
}

func TestApplyWPFileModsLockBlockRejectsExistingFalseConstant(t *testing.T) {
	content := "<?php\n" +
		"define('DISALLOW_FILE_MODS', false);\n" +
		"/* That's all, stop editing! Happy publishing. */\n"

	if _, err := applyWPFileModsLockBlock(content, true); err == nil {
		t.Fatal("apply lock error = nil, want rejection for existing false constant")
	}
}

func TestWPConfigHasUserFileModsLockIgnoresManagedBlock(t *testing.T) {
	webRoot := t.TempDir()
	configPath := filepath.Join(webRoot, "wp-config.php")
	managedOnly := "<?php\n" +
		wpPanelFileLockBegin + "\n" +
		"define('DISALLOW_FILE_MODS', true);\n" +
		wpPanelFileLockEnd + "\n" +
		"/* That's all, stop editing! Happy publishing. */\n"
	if err := os.WriteFile(configPath, []byte(managedOnly), 0600); err != nil {
		t.Fatal(err)
	}
	if wpConfigHasUserFileModsLock(webRoot) {
		t.Fatal("managed lock block should not be treated as a user lock")
	}

	userDefined := "<?php\n" +
		"define(\"DISALLOW_FILE_MODS\", true);\n" +
		"/* That's all, stop editing! Happy publishing. */\n"
	if err := os.WriteFile(configPath, []byte(userDefined), 0600); err != nil {
		t.Fatal(err)
	}
	if !wpConfigHasUserFileModsLock(webRoot) {
		t.Fatal("user-defined DISALLOW_FILE_MODS=true should be reported")
	}
}

func TestWPFileLockRuntimeWritablePathPolicy(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "wp-content", "uploads", "2026", "photo.jpg")
	cache := filepath.Join(root, "wp-content", "cache", "page.html")
	language := filepath.Join(root, "wp-content", "languages", "zh_CN.mo")
	wflog := filepath.Join(root, "wp-content", "wflogs", "rules.php.json")
	unknown := filepath.Join(root, "wp-content", "plugin-data", "state.json")

	for _, tt := range []struct {
		mode    string
		allowed []string
		blocked []string
	}{
		{FileLockModeLegacy, []string{upload, cache, language, wflog, unknown}, nil},
		{FileLockModeStandard, []string{upload, cache, wflog}, []string{language, unknown}},
		{FileLockModeStrict, []string{upload}, []string{cache, language, wflog, unknown}},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			for _, path := range tt.allowed {
				if !IsWPFileLockRuntimeWritablePath(tt.mode, root, path, false, false) {
					t.Fatalf("%s should be writable in %s mode", path, tt.mode)
				}
			}
			for _, path := range tt.blocked {
				if IsWPFileLockRuntimeWritablePath(tt.mode, root, path, false, false) {
					t.Fatalf("%s should be read-only in %s mode", path, tt.mode)
				}
			}
		})
	}

	for _, path := range []string{
		filepath.Join(root, "index.php"),
		filepath.Join(root, "wp-config.php"),
		filepath.Join(root, "wordfence-waf.php"),
		filepath.Join(root, "wp-content"),
		filepath.Join(root, "wp-content", "advanced-cache.php"),
		filepath.Join(root, "wp-content", ".user.ini"),
		filepath.Join(root, "wp-content", "cache", ".user.ini"),
		filepath.Join(root, "wp-content", "upgrade", "wordpress.zip"),
		filepath.Join(root, "wp-content", "upgrade", "update.php"),
		filepath.Join(root, "wp-content", "upgrade-temp-backup", "plugins", "plugin.zip"),
		filepath.Join(root, "wp-content", "uploads", "shell.php"),
		filepath.Join(root, "wp-content", "plugins", "plugin.php"),
		filepath.Join(root, "wp-content", "themes", "theme", "functions.php"),
		filepath.Join(root, "wp-content", "mu-plugins", "loader.php"),
	} {
		for _, mode := range []string{FileLockModeLegacy, FileLockModeStandard, FileLockModeStrict} {
			if IsWPFileLockRuntimeWritablePath(mode, root, path, false, false) {
				t.Fatalf("%s should be blocked in %s mode", path, mode)
			}
		}
	}

	if !IsWPFileLockRuntimeWritablePath(FileLockModeStrict, root, filepath.Join(root, "wp-content", "uploads", "shell.php"), false, true) {
		t.Fatal("runtime PHP cleanup should be allowed when explicitly requested")
	}
	if IsWPFileLockRuntimeWritablePath(FileLockModeLegacy, root, filepath.Join(root, "wp-content", "advanced-cache.php"), false, true) {
		t.Fatal("drop-in PHP should stay blocked even during cleanup")
	}
	if wpFileLockPermissionWritablePath(FileLockModeStandard, root, filepath.Join(root, "wp-content"), true) {
		t.Fatal("wp-content root should not be writable")
	}
	if !wpFileLockPermissionWritablePath(FileLockModeStandard, root, filepath.Join(root, "wp-content", "cache"), true) {
		t.Fatal("cache should be writable in standard mode")
	}
	if wpFileLockPermissionWritablePath(FileLockModeStrict, root, filepath.Join(root, "wp-content", "cache"), true) {
		t.Fatal("cache should be read-only in strict mode")
	}
	if wpFileLockPermissionWritablePath(FileLockModeLegacy, root, filepath.Join(root, "wp-content", "upgrade"), true) {
		t.Fatal("upgrade directory should stay locked")
	}
	if wpFileLockPermissionWritablePath(FileLockModeLegacy, root, filepath.Join(root, "wp-content", "upgrade-temp-backup"), true) {
		t.Fatal("upgrade-temp-backup directory should stay locked")
	}
}

func TestPreviewSiteFileLockUsesModePolicy(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"uploads", "cache", "languages", "wflogs", "plugin-data", "plugins"} {
		if err := os.MkdirAll(filepath.Join(root, "wp-content", dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "wp-content", "languages", "zh_CN.l10n.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wp-content", "plugin-data", "drop.phtml"), []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wp-content", "drop.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wp-content", "plugins", "legit.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "wp-content", "plugins", "linked-code")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{WebRoot: root, SiteType: "wordpress"}

	standard, err := PreviewSiteFileLock(site, FileLockModeStandard)
	if err != nil {
		t.Fatalf("standard preview: %v", err)
	}
	if got := strings.Join(standard.WritableDirs, ","); got != "wp-content/cache,wp-content/uploads,wp-content/wflogs" {
		t.Fatalf("standard writable dirs = %q", got)
	}
	if got := strings.Join(standard.ExecutableFiles, ","); got != "wp-content/drop.php,wp-content/languages/zh_CN.l10n.php,wp-content/plugin-data/drop.phtml" {
		t.Fatalf("standard executable files = %q", got)
	}
	if !containsPreviewString(standard.ReadOnlyDirs, "wp-content/languages") || !containsPreviewString(standard.ReadOnlyDirs, "wp-content/plugin-data") {
		t.Fatalf("standard read-only dirs = %#v", standard.ReadOnlyDirs)
	}
	if runtime.GOOS != "windows" && !containsPreviewString(standard.SymlinkPaths, "wp-content/plugins/linked-code") {
		t.Fatalf("standard symlink paths = %#v", standard.SymlinkPaths)
	}

	strict, err := PreviewSiteFileLock(site, FileLockModeStrict)
	if err != nil {
		t.Fatalf("strict preview: %v", err)
	}
	if got := strings.Join(strict.WritableDirs, ","); got != "wp-content/uploads" {
		t.Fatalf("strict writable dirs = %q", got)
	}
	if !containsPreviewString(strict.ReadOnlyDirs, "wp-content/cache") || !containsPreviewString(strict.ReadOnlyDirs, "wp-content/wflogs") {
		t.Fatalf("strict read-only dirs = %#v", strict.ReadOnlyDirs)
	}
}

func TestExecuteSetFileLockMarksIncompleteApply(t *testing.T) {
	openTestDB(t)
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (1, 'demo', 'example.com', 'active', 'wp_demo', ?, '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress')`, missingRoot); err != nil {
		t.Fatalf("insert website: %v", err)
	}
	site := &models.Website{ID: 1, Domain: "example.com", WebRoot: missingRoot, SystemUser: "wp_demo", SiteType: "wordpress"}
	result := executeSetFileLock(&Task{Payload: &SetFileLockPayload{Site: site, Enabled: true, Mode: FileLockModeStandard}})
	if result.Success {
		t.Fatal("file lock should fail for a missing web root")
	}
	var enabled int
	var mode, status string
	if err := database.GetDB().QueryRow("SELECT file_lock_enabled, file_lock_mode, file_lock_apply_status FROM websites WHERE id = 1").Scan(&enabled, &mode, &status); err != nil {
		t.Fatalf("query website: %v", err)
	}
	if enabled != 0 || mode != "" || status != FileLockApplyStatusFailed {
		t.Fatalf("enabled/mode/status = %d/%q/%q, want 0/empty/failed", enabled, mode, status)
	}
}

func containsPreviewString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestApplyWPFileModsLockBlockFallbackForNonstandardWPConfig(t *testing.T) {
	content := "<?php\n" +
		"define('DB_NAME', 'wordpress');\n" +
		"define('DB_USER', 'admin');\n"
	locked, err := applyWPFileModsLockBlock(content, true)
	if err != nil {
		t.Fatalf("apply lock: %v", err)
	}
	if !strings.Contains(locked, wpPanelFileLockBegin) {
		t.Fatal("managed lock block should be injected for nonstandard wp-config")
	}
	if !strings.Contains(locked, "define('DISALLOW_FILE_MODS', true);") {
		t.Fatalf("managed lock constant should be present: %s", locked)
	}
	if !strings.Contains(locked, "<?php") {
		t.Fatal("original PHP open tag should remain")
	}

	unlocked, err := applyWPFileModsLockBlock(locked, false)
	if err != nil {
		t.Fatalf("remove lock: %v", err)
	}
	if strings.Contains(unlocked, wpPanelFileLockBegin) || strings.Contains(unlocked, "DISALLOW_FILE_MODS") {
		t.Fatalf("managed lock block should be removed: %s", unlocked)
	}

	configWithClose := "<?php\n" +
		"define('DB_NAME', 'wordpress');\n" +
		"?>\n"
	got := insertBeforeMarker(configWithClose, wpPanelFileLockBegin+"\n"+"define('DISALLOW_FILE_MODS', true);\n"+wpPanelFileLockEnd+"\n")
	if !strings.Contains(got, wpPanelFileLockBegin) {
		t.Fatal("should inject before closing PHP tag when marker is missing")
	}
	if idxTag := strings.Index(got, wpPanelFileLockBegin); idxTag >= strings.Index(got, "?>") {
		t.Fatal("managed block should be before ?> when inserted by fallback")
	}
}
