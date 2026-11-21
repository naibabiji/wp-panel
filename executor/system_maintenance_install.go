package executor

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/naibabiji/wp-panel/config"
)

// Publish migration-active records while holding the same brief admission
// gate. Once committed, the persistent migration lock blocks maintenance.
func acquireMaintenanceMigrationAdmission() (*os.File, error) {
	file, err := acquireMaintenanceLifecycleFile(config.AppConfig)
	if err != nil {
		return nil, fmt.Errorf("%w: system maintenance busy or unavailable", errSiteMigrationBusy)
	}
	return file, nil
}

// The existing detached APT runner owns these locks, not its short-lived
// caller. Package maintainer scripts can restart services too, so automatic
// installation must not run through an active backup or site write.
func prepareMaintenancePackageRun(plan systemPackageUpdatePlan) (*sql.DB, func(), string, error) {
	db, err := sql.Open("sqlite", plan.MaintenanceDBPath+"?mode=rw&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, nil, "database_unavailable", err
	}
	check := func() (string, error) {
		var phase, status string
		if err := db.QueryRow(`SELECT phase,status FROM system_maintenance_actions WHERE id=? AND kind='round'`, plan.MaintenanceRoundID).Scan(&phase, &status); err != nil {
			return "round_unavailable", err
		}
		if phase != "install" || status != "pending" {
			return "round_unavailable", errors.New("maintenance round is not awaiting installation")
		}
		p, err := ReadSystemMaintenancePolicy(db)
		if err != nil {
			return "policy_unavailable", err
		}
		if !p.Enabled {
			return "authorization_revoked", nil
		}
		location, err := currentMaintenanceLocation()
		if err != nil {
			return "timezone_unknown", err
		}
		allowed, err := p.AllowsStart(maintenanceClock(), location)
		if err != nil {
			return "policy_unavailable", err
		}
		if !allowed {
			return "outside_window", nil
		}
		return "", nil
	}
	if reason, err := check(); reason != "" || err != nil {
		return db, nil, reason, err
	}
	cfg := &config.Config{Panel: config.PanelConfig{DataDir: filepath.Dir(plan.StatusPath)}, SQLite: config.SQLiteConfig{Path: plan.MaintenanceDBPath}}
	oldCfg := config.AppConfig
	config.AppConfig = cfg
	release, reason, err := acquireMaintenanceGateForUpdate(cfg, db, plan.ID)
	if release == nil {
		config.AppConfig = oldCfg
		return db, nil, reason, err
	}
	if reason, err := check(); reason != "" || err != nil {
		release()
		config.AppConfig = oldCfg
		return db, nil, reason, err
	}
	return db, func() { release(); config.AppConfig = oldCfg }, "", nil
}
