package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

type siteOpLease struct {
	reason string
	file   *os.File
}

var (
	wpSiteOpMu   sync.Mutex
	wpSiteOpBusy = map[int]siteOpLease{}
)

func siteOpLockPath(siteID int) (string, error) {
	if siteID <= 0 {
		return "", fmt.Errorf("invalid site id %d", siteID)
	}
	if config.AppConfig == nil || config.AppConfig.Panel.DataDir == "" {
		return "", errors.New("panel data directory is not configured")
	}
	dataDir := filepath.Clean(config.AppConfig.Panel.DataDir)
	lockDir := filepath.Join(dataDir, "locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(lockDir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("site lock directory is not a real directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("site lock directory has unexpected owner")
	}
	if err := os.Chmod(lockDir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(lockDir, fmt.Sprintf("site-%d.lock", siteID)), nil
}

// acquireSiteInterprocessLock is deliberately only a mutual-exclusion
// primitive. Maintenance windows and other business gates remain at their
// existing call sites so companion deployment and file-operation exceptions
// retain their documented semantics.
func acquireSiteInterprocessLock(siteID int) (*os.File, bool) {
	path, err := siteOpLockPath(siteID)
	if err != nil {
		return nil, false
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, false
	}
	file := os.NewFile(uintptr(fd), path)
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, false
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 {
		_ = file.Close()
		return nil, false
	}
	if sys, ok := stat.Sys().(*syscall.Stat_t); !ok || sys.Nlink != 1 || int(sys.Uid) != os.Geteuid() {
		_ = file.Close()
		return nil, false
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, false
	}
	return file, true
}

func tryClaimSiteOpLock(siteID int, reason string) bool {
	if _, busy := wpSiteOpBusy[siteID]; busy {
		return false
	}
	file, ok := acquireSiteInterprocessLock(siteID)
	if !ok {
		return false
	}
	wpSiteOpBusy[siteID] = siteOpLease{reason: reason, file: file}
	return true
}

// TryAcquireSiteOpLock claims an exclusive "site busy" slot so a manual
// update-backup database restore and a WordPress core/plugin/theme update
// cannot run against the same site at the same time. The caller must call
// ReleaseSiteOpLock once the operation finishes (success or failure).
func TryAcquireSiteOpLock(siteID int, reason string) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	if db := database.GetDB(); db != nil {
		active, err := database.MaintenanceWindowActive(db, siteID)
		if err != nil || active {
			return false
		}
	}
	return tryClaimSiteOpLock(siteID, reason)
}

// TryAcquireCompanionDeployLock reserves the in-process site slot without
// treating a persisted maintenance window as a blocker. WP Panel owns the
// companion plugin and may replace it while AI access or temporary maintenance
// is active; real in-process writers remain mutually exclusive.
func TryAcquireCompanionDeployLock(siteID int) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	return tryClaimSiteOpLock(siteID, "companion_upgrade")
}

// TryAcquireSiteFileOpLock serializes a long file operation with other site
// mutations while allowing an already-authorized maintenance window to remain
// active. File operations perform their own path-level maintenance checks.
func TryAcquireSiteFileOpLock(siteID int, reason string) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	return tryClaimSiteOpLock(siteID, reason)
}

// Only the maintenance executor can enter its own persisted window.
func tryAcquireMaintenanceOp(siteID int) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	return tryClaimSiteOpLock(siteID, "maintenance")
}

// ReleaseSiteOpLock releases a lock acquired by TryAcquireSiteOpLock. It is
// a no-op if the site currently holds no lock.
func ReleaseSiteOpLock(siteID int) {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	lease, ok := wpSiteOpBusy[siteID]
	if !ok {
		return
	}
	delete(wpSiteOpBusy, siteID)
	if lease.file != nil {
		_ = syscall.Flock(int(lease.file.Fd()), syscall.LOCK_UN)
		_ = lease.file.Close()
	}
}

// SiteOpLocked reports whether siteID currently holds a lock.
func SiteOpLocked(siteID int) bool {
	wpSiteOpMu.Lock()
	defer wpSiteOpMu.Unlock()
	_, busy := wpSiteOpBusy[siteID]
	return busy
}
