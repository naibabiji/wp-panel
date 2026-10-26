package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/config"
)

func setupSiteLockTest(t *testing.T) {
	t.Helper()
	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: t.TempDir()}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
}

func TestSiteOpLockPreventsConcurrentAcquire(t *testing.T) {
	setupSiteLockTest(t)
	siteID := 900001
	t.Cleanup(func() { ReleaseSiteOpLock(siteID) })

	if !TryAcquireSiteOpLock(siteID, "restore") {
		t.Fatal("first acquire should succeed")
	}
	if TryAcquireSiteOpLock(siteID, "update") {
		t.Fatal("second acquire should fail while lock is held")
	}
	if !SiteOpLocked(siteID) {
		t.Fatal("site should report as locked")
	}
	ReleaseSiteOpLock(siteID)
	if SiteOpLocked(siteID) {
		t.Fatal("site should be unlocked after release")
	}
	if !TryAcquireSiteOpLock(siteID, "update") {
		t.Fatal("acquire after release should succeed")
	}
}

func TestSiteOpLockIsPerSite(t *testing.T) {
	setupSiteLockTest(t)
	siteA, siteB := 900002, 900003
	t.Cleanup(func() { ReleaseSiteOpLock(siteA); ReleaseSiteOpLock(siteB) })

	if !TryAcquireSiteOpLock(siteA, "restore") {
		t.Fatal("acquire for site A should succeed")
	}
	if !TryAcquireSiteOpLock(siteB, "restore") {
		t.Fatal("acquire for unrelated site B should not be blocked by site A's lock")
	}
}

func TestSiteOpLockReleaseIsNoOpWhenNotLocked(t *testing.T) {
	setupSiteLockTest(t)
	siteID := 900004
	ReleaseSiteOpLock(siteID)
	if SiteOpLocked(siteID) {
		t.Fatal("releasing an unlocked site should stay unlocked")
	}
}

func TestSiteFileOpLockSharesTheSiteMutationSlot(t *testing.T) {
	setupSiteLockTest(t)
	siteID := 900005
	t.Cleanup(func() { ReleaseSiteOpLock(siteID) })

	if !TryAcquireSiteFileOpLock(siteID, "archive_extract") {
		t.Fatal("file operation acquire should succeed")
	}
	if TryAcquireSiteOpLock(siteID, "migration") {
		t.Fatal("migration acquire should fail while file operation is active")
	}
}

func TestSiteInterprocessLockRejectsSecondFileDescriptor(t *testing.T) {
	setupSiteLockTest(t)
	first, ok := acquireSiteInterprocessLock(900006)
	if !ok {
		t.Fatal("first interprocess acquire should succeed")
	}
	t.Cleanup(func() {
		_ = first.Close()
	})
	if second, ok := acquireSiteInterprocessLock(900006); ok {
		_ = second.Close()
		t.Fatal("second independent file descriptor should not acquire the lock")
	}
	other, ok := acquireSiteInterprocessLock(900007)
	if !ok {
		t.Fatal("a different site should acquire independently")
	}
	_ = other.Close()
}

func TestSiteInterprocessLockFailsClosedForUnsafeLockDirectory(t *testing.T) {
	setupSiteLockTest(t)
	dataDir := config.AppConfig.Panel.DataDir
	lockPath := filepath.Join(dataDir, "locks")
	if err := os.WriteFile(lockPath, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := acquireSiteInterprocessLock(900008); ok {
		t.Fatal("lock acquisition should fail when the lock directory is not a directory")
	}
}
