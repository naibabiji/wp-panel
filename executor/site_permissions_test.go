package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(locked, wpPanelFileLockBegin) || !strings.Contains(locked, "define('DISALLOW_FILE_MODS', true);") {
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
