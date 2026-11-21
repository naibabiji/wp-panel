//go:build linux

package executor

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/naibabiji/wp-panel/config"
	"golang.org/x/sys/unix"
)

func tryMaintenanceFileLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || int(st.Uid) != os.Geteuid()) {
		err = errors.New("unsafe maintenance lock")
	}
	if err == nil {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func maintenancePackageBusy(cfg *config.Config) (bool, error) {
	if ReconcileSystemPackageUpdateStatus(cfg).Status == "running" {
		return true, nil
	}
	f, err := tryMaintenanceFileLock(systemPackageUpdateLockPath)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	return maintenanceDPKGQuery()
}

// Lifecycle operations use the same file gate as the detached installer; an
// in-process mutex alone cannot coordinate across those two processes.
func acquireMaintenanceLifecycleFile(cfg *config.Config) (*os.File, error) {
	if cfg == nil || cfg.Panel.DataDir == "" {
		return nil, errors.New("panel data directory unavailable")
	}
	dir := filepath.Join(cfg.Panel.DataDir, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe maintenance lock directory")
	}
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return nil, err
	}
	if int(st.Uid) != os.Geteuid() {
		return nil, errors.New("unsafe maintenance lock directory owner")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	return tryMaintenanceFileLock(filepath.Join(dir, "system-maintenance.lock"))
}

// Hold all acquired locks until the caller finishes the action (including the
// reboot grace period). Failed nonblocking acquisition releases the full set.
func acquireMaintenanceGate(cfg *config.Config, db *sql.DB) (func(), string, error) {
	return acquireMaintenanceGateForUpdate(cfg, db, "")
}

func acquireMaintenanceGateForUpdate(cfg *config.Config, db *sql.DB, updateID string) (func(), string, error) {
	if cfg == nil || db == nil {
		return nil, "unavailable", errors.New("maintenance unavailable")
	}
	if config.AppConfig == nil || filepath.Clean(config.AppConfig.Panel.DataDir) != filepath.Clean(cfg.Panel.DataDir) {
		return nil, "configuration_mismatch", errors.New("maintenance lock configuration mismatch")
	}
	if !panelLifecycleMu.TryLock() {
		return nil, "panel_lifecycle_busy", nil
	}
	var files []*os.File
	release := func() {
		for i := len(files) - 1; i >= 0; i-- {
			files[i].Close()
		}
		panelLifecycleMu.Unlock()
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()
	backupPath, err := fileBackupLockPath()
	if err != nil {
		return nil, "backup_lock_unavailable", err
	}
	for _, item := range []struct{ path, reason string }{
		{filepath.Join(filepath.Dir(backupPath), "system-maintenance.lock"), "maintenance_busy"},
		{systemPackageUpdateLockPath, "package_manager_busy"}, {backupPath, "file_backup_busy"},
		{filepath.Join(filepath.Dir(backupPath), "site-create.lock"), "site_creation_busy"},
	} {
		f, err := tryMaintenanceFileLock(item.path)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, item.reason, nil
		}
		if err != nil {
			return nil, item.reason, err
		}
		files = append(files, f)
	}
	rows, err := db.Query(`SELECT id FROM websites ORDER BY id`)
	if err != nil {
		return nil, "site_query_failed", err
	}
	var ids []int
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, "site_query_failed", err
	}
	for _, id := range ids {
		f, acquired := acquireSiteInterprocessLock(id)
		if !acquired {
			return nil, fmt.Sprintf("site_%d_busy", id), nil
		}
		files = append(files, f)
	}
	var migrations, updates int
	if err := db.QueryRow(`SELECT COUNT(*) FROM site_migration_locks WHERE status='active'`).Scan(&migrations); err != nil {
		return nil, "migration_query_failed", err
	}
	if migrations != 0 {
		return nil, "migration_busy", nil
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM wp_update_tasks WHERE status='running'`).Scan(&updates); err != nil {
		return nil, "wp_update_query_failed", err
	}
	if updates != 0 {
		return nil, "wp_update_busy", nil
	}
	if SnapshotPanelUpdateStatus().Running || ReconcilePanelDBRestoreStatus(cfg).Status == "running" {
		return nil, "panel_lifecycle_busy", nil
	}
	if _, err := os.Lstat(rollbackPlanPath(cfg)); err == nil {
		return nil, "panel_update_unconfirmed", nil
	} else if !os.IsNotExist(err) {
		return nil, "panel_update_unknown", err
	}
	if status := ReconcileSystemPackageUpdateStatus(cfg); status.Status == "running" && status.ID != updateID {
		return nil, "package_manager_busy", nil
	}
	busy, err := maintenanceDPKGQuery()
	if err != nil || busy {
		return nil, "package_manager_busy", err
	}
	ok = true
	return release, "", nil
}
