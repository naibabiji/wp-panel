package executor

import (
	"sync"

	"github.com/naibabiji/wp-panel/database"
)

var (
	wpSiteOpMu   sync.Mutex
	wpSiteOpBusy = map[int]string{}
)

// TryAcquireSiteOpLock claims an exclusive "site busy" slot so a manual
// update-backup database restore and a WordPress core/plugin/theme update
// cannot run against the same site at the same time. The caller must call
// ReleaseSiteOpLock once the operation finishes (success or failure).
func TryAcquireSiteOpLock(siteID int, reason string) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	if _, busy := wpSiteOpBusy[siteID]; busy {
		return false
	}
	if db := database.GetDB(); db != nil {
		active, err := database.MaintenanceWindowActive(db, siteID)
		if err != nil || active {
			return false
		}
	}
	wpSiteOpBusy[siteID] = reason
	return true
}

// Only the maintenance executor can enter its own persisted window.
func tryAcquireMaintenanceOp(siteID int) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	if _, busy := wpSiteOpBusy[siteID]; busy {
		return false
	}
	wpSiteOpBusy[siteID] = "maintenance"
	return true
}

// ReleaseSiteOpLock releases a lock acquired by TryAcquireSiteOpLock. It is
// a no-op if the site currently holds no lock.
func ReleaseSiteOpLock(siteID int) {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	delete(wpSiteOpBusy, siteID)
}

// SiteOpLocked reports whether siteID currently holds a lock.
func SiteOpLocked(siteID int) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	_, busy := wpSiteOpBusy[siteID]
	return busy
}
