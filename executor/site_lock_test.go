package executor

import "testing"

func TestSiteOpLockPreventsConcurrentAcquire(t *testing.T) {
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
	siteID := 900004
	ReleaseSiteOpLock(siteID)
	if SiteOpLocked(siteID) {
		t.Fatal("releasing an unlocked site should stay unlocked")
	}
}
