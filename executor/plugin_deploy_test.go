package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func listStagingLikeDirs(t *testing.T, pluginsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		t.Fatalf("read dir %s: %v", pluginsDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != pluginDirName {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestDeployPluginDirectoryFreshInstall(t *testing.T) {
	pluginsDir := t.TempDir()
	pluginDir := filepath.Join(pluginsDir, pluginDirName)
	src := map[string][]byte{
		"wp-panel-optimizer.php":   []byte("<?php // bootstrap\n"),
		"includes/trait-cache.php": []byte("<?php // cache\n"),
	}

	if err := deployPluginDirectory(pluginsDir, pluginDir, src); err != nil {
		t.Fatalf("deployPluginDirectory: %v", err)
	}

	if got := readFileString(t, filepath.Join(pluginDir, "wp-panel-optimizer.php")); got != "<?php // bootstrap\n" {
		t.Fatalf("bootstrap content = %q", got)
	}
	if got := readFileString(t, filepath.Join(pluginDir, "includes/trait-cache.php")); got != "<?php // cache\n" {
		t.Fatalf("trait content = %q", got)
	}
	if leftover := listStagingLikeDirs(t, pluginsDir); len(leftover) != 0 {
		t.Fatalf("expected no leftover staging/backup dirs, got %v", leftover)
	}
	if !pluginDirMatches(pluginDir, src) {
		t.Fatalf("pluginDirMatches should report match right after deploy")
	}
}

func TestDeployPluginDirectoryUpdateOverExisting(t *testing.T) {
	pluginsDir := t.TempDir()
	pluginDir := filepath.Join(pluginsDir, pluginDirName)

	// Simulate an old single-file install already living at the target path —
	// this is exactly the ENOTEMPTY scenario a single os.Rename can't handle.
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("seed old dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "wp-panel-optimizer.php"), []byte("<?php // old single-file version\n"), 0644); err != nil {
		t.Fatalf("seed old file: %v", err)
	}

	newSrc := map[string][]byte{
		"wp-panel-optimizer.php":   []byte("<?php // new bootstrap\n"),
		"includes/trait-cache.php": []byte("<?php // new cache module\n"),
	}

	if err := deployPluginDirectory(pluginsDir, pluginDir, newSrc); err != nil {
		t.Fatalf("deployPluginDirectory over existing dir: %v", err)
	}

	if got := readFileString(t, filepath.Join(pluginDir, "wp-panel-optimizer.php")); got != "<?php // new bootstrap\n" {
		t.Fatalf("expected new content, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "includes/trait-cache.php")); err != nil {
		t.Fatalf("expected new module file to exist: %v", err)
	}
	if leftover := listStagingLikeDirs(t, pluginsDir); len(leftover) != 0 {
		t.Fatalf("expected backup dir cleaned up after successful deploy, got %v", leftover)
	}
}

func TestDeployPluginDirectoryRemovesFilesDroppedFromNewerVersion(t *testing.T) {
	pluginsDir := t.TempDir()
	pluginDir := filepath.Join(pluginsDir, pluginDirName)

	oldSrc := map[string][]byte{
		"wp-panel-optimizer.php":    []byte("<?php // v1\n"),
		"includes/trait-legacy.php": []byte("<?php // module removed in v2\n"),
	}
	if err := deployPluginDirectory(pluginsDir, pluginDir, oldSrc); err != nil {
		t.Fatalf("seed v1 deploy: %v", err)
	}

	newSrc := map[string][]byte{
		"wp-panel-optimizer.php": []byte("<?php // v2\n"),
	}
	if err := deployPluginDirectory(pluginsDir, pluginDir, newSrc); err != nil {
		t.Fatalf("deploy v2: %v", err)
	}

	if _, err := os.Stat(filepath.Join(pluginDir, "includes/trait-legacy.php")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy module file to be removed, stat err = %v", err)
	}
	if !pluginDirMatches(pluginDir, newSrc) {
		t.Fatalf("pluginDirMatches should report match after dropping stale files")
	}
}

// TestDeployPluginDirectoryRecoversFromCrashBetweenSwapSteps reproduces the panel
// being killed right after the old directory has been renamed to its backup path
// but before the new directory has been swapped into place (between step ③ and
// step ④ of the design). The next deploy attempt must restore the still-good
// backup instead of discarding it as junk, and only then proceed with the update.
func TestDeployPluginDirectoryRecoversFromCrashBetweenSwapSteps(t *testing.T) {
	pluginsDir := t.TempDir()
	pluginDir := filepath.Join(pluginsDir, pluginDirName)

	oldSrc := map[string][]byte{
		"wp-panel-optimizer.php": []byte("<?php // still-good previous version\n"),
	}
	if err := deployPluginDirectory(pluginsDir, pluginDir, oldSrc); err != nil {
		t.Fatalf("seed initial deploy: %v", err)
	}

	// Simulate the crash: manually move the live dir to a backup path, mirroring
	// what step ③ does, then stop (as if the process died before step ④).
	backupDir := filepath.Join(pluginsDir, "."+pluginDirName+".backup-deadbeef")
	if err := os.Rename(pluginDir, backupDir); err != nil {
		t.Fatalf("simulate crash rename: %v", err)
	}
	if _, err := os.Stat(pluginDir); !os.IsNotExist(err) {
		t.Fatalf("plugin dir should be missing to reproduce the crash window")
	}

	newSrc := map[string][]byte{
		"wp-panel-optimizer.php": []byte("<?php // new version after recovery\n"),
	}
	if err := deployPluginDirectory(pluginsDir, pluginDir, newSrc); err != nil {
		t.Fatalf("deploy after simulated crash: %v", err)
	}

	if got := readFileString(t, filepath.Join(pluginDir, "wp-panel-optimizer.php")); got != "<?php // new version after recovery\n" {
		t.Fatalf("expected deploy to succeed and land the new version, got %q", got)
	}
	if leftover := listStagingLikeDirs(t, pluginsDir); len(leftover) != 0 {
		t.Fatalf("expected no leftover staging/backup dirs after recovery, got %v", leftover)
	}
}

func TestCleanupStalePluginStagingDirsRemovesLeftovers(t *testing.T) {
	pluginsDir := t.TempDir()
	pluginDir := filepath.Join(pluginsDir, pluginDirName)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("seed live dir: %v", err)
	}

	staleStaging := filepath.Join(pluginsDir, "."+pluginDirName+".staging-abc123")
	staleBackup := filepath.Join(pluginsDir, "."+pluginDirName+".backup-abc123")
	for _, dir := range []string{staleStaging, staleBackup} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("seed stale dir %s: %v", dir, err)
		}
	}

	recoverOrCleanupStalePluginDirs(pluginsDir, pluginDir)

	for _, dir := range []string{staleStaging, staleBackup} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected stale dir %s to be removed, stat err = %v", dir, err)
		}
	}
	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("live plugin dir should be untouched: %v", err)
	}
}

func TestPluginDirMatchesDetectsExtraFile(t *testing.T) {
	pluginsDir := t.TempDir()
	pluginDir := filepath.Join(pluginsDir, pluginDirName)
	src := map[string][]byte{
		"wp-panel-optimizer.php": []byte("<?php // v\n"),
	}
	if err := deployPluginDirectory(pluginsDir, pluginDir, src); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !pluginDirMatches(pluginDir, src) {
		t.Fatalf("expected match right after deploy")
	}

	// A leftover file not present in the current source set (e.g. a module
	// removed in a later version) must make the comparison report "not matching"
	// so AutoDeployPluginUpdates re-deploys and cleans it up.
	if err := os.WriteFile(filepath.Join(pluginDir, "includes/stale-module.php"), []byte("<?php\n"), 0644); err == nil {
		t.Fatalf("expected write to fail because includes/ doesn't exist yet")
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, "includes"), 0755); err != nil {
		t.Fatalf("mkdir includes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "includes/stale-module.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatalf("write stale module: %v", err)
	}
	if pluginDirMatches(pluginDir, src) {
		t.Fatalf("expected mismatch once an extra file not in src is present")
	}
}
