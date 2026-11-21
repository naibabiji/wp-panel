//go:build linux

package executor

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"golang.org/x/sys/unix"
)

func TestMaintenanceGateHoldsSiteAndCreationLocksAndReleasesPartialSet(t *testing.T) {
	cfg, _ := setupMaintenanceRunner(t)
	db := database.GetDB()
	if _, err := db.Exec(`INSERT INTO websites(id,name,domain,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES(1,'site','example.test','wp_site','/unused','/unused','site','site','/unused/pool.conf','/unused/nginx.conf')`); err != nil {
		t.Fatal(err)
	}
	release, reason, err := acquireMaintenanceGate(cfg, db)
	if err != nil || release == nil {
		t.Fatalf("gate: %s %v", reason, err)
	}
	if file, ok := acquireSiteInterprocessLock(1); ok {
		file.Close()
		t.Fatal("site write started while maintenance held gate")
	}
	if lock, err := acquireSiteCreationLock(); err == nil {
		lock.Close()
		t.Fatal("new site creation passed held gate")
	}
	if panelLifecycleMu.TryLock() {
		panelLifecycleMu.Unlock()
		t.Fatal("panel lifecycle was not held")
	}
	release()
	file, ok := acquireSiteInterprocessLock(1)
	if !ok {
		t.Fatal("gate did not release site lock")
	}
	file.Close()
	// If a later lock is busy, earlier locks must be released too.
	backup, err := fileBackupLockPath()
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := tryMaintenanceFileLock(backup)
	if err != nil {
		t.Fatal(err)
	}
	release, reason, err = acquireMaintenanceGate(cfg, db)
	if err != nil || release != nil || reason != "file_backup_busy" {
		t.Fatalf("busy backup: %s %v", reason, err)
	}
	blocked.Close()
	if !panelLifecycleMu.TryLock() {
		t.Fatal("partial acquisition leaked lifecycle lock")
	}
	panelLifecycleMu.Unlock()
	apt, err := tryMaintenanceFileLock(systemPackageUpdateLockPath)
	if err != nil {
		t.Fatalf("partial acquisition leaked APT lock: %v", err)
	}
	apt.Close()
	// The regular package observation query sees the panel lock even when
	// the detached task status says idle.
	apt, err = tryMaintenanceFileLock(systemPackageUpdateLockPath)
	if err != nil {
		t.Fatal(err)
	}
	busy, err := maintenancePackageBusy(cfg)
	apt.Close()
	if err != nil || !busy {
		t.Fatalf("panel APT lock not included: %v %v", busy, err)
	}
	// An unsafe lock is an error rather than idle permission.
	_, err = tryMaintenanceFileLock(filepath.Join(t.TempDir(), "absent-parent", "lock"))
	if err == nil || errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("unsafe lock treated as idle: %v", err)
	}
}
