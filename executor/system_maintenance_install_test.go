//go:build linux

package executor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func maintenanceInstallPlanFixture(t *testing.T, cfg *config.Config, id int64) systemPackageUpdatePlan {
	t.Helper()
	p := systemPackageUpdatePlan{ID: "install-gate-fixture", StatusPath: SystemPackageUpdateStatusPath(cfg), PlanPath: filepath.Join(cfg.Panel.DataDir, "plan.json"), MaintenanceDBPath: cfg.SQLite.Path, MaintenanceRoundID: id}
	if err := writePanelDBRestoreJSON(p.PlanPath, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMaintenanceInstallerServiceBusyDefersAndResumes(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	round, err := createMaintenanceRound(database.GetDB(), nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	p := maintenanceInstallPlanFixture(t, cfg, round.ID)
	release, err := tryServiceControlLocks(cfg.Panel.DataDir, "mariadb")
	if err != nil {
		t.Fatal(err)
	}
	oldCommand := systemPackageUpdateCommand
	t.Cleanup(func() { systemPackageUpdateCommand = oldCommand })
	systemPackageUpdateCommand = func(context.Context, string, ...string) error {
		t.Fatal("installer ran while service was busy")
		return nil
	}
	if err := RunSystemPackageUpdatePlan(p.PlanPath); err != nil {
		t.Fatal(err)
	}
	release()
	status := ReadSystemPackageUpdateStatus(cfg)
	if status.Status != "deferred" {
		t.Fatalf("%+v", status)
	}
	var phase, reason string
	var overtime bool
	if err := database.GetDB().QueryRow(`SELECT phase,message_code,overtime_allowed FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&phase, &reason, &overtime); err != nil {
		t.Fatal(err)
	}
	if phase != "install" || reason != "maintenance_busy" || overtime {
		t.Fatalf("%s %s %v", phase, reason, overtime)
	}
	// Reuse the existing deferred-install continuation, not another worker.
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 1 {
		t.Fatalf("deferred installer lost daily opportunity: %d", f.installs)
	}
}

func TestMaintenanceAutomaticInstallerDefersBeforeAnyPackageCommand(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	db := database.GetDB()
	round, err := createMaintenanceRound(db, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	p := maintenanceInstallPlanFixture(t, cfg, round.ID)
	backup, err := fileBackupLockPath()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := tryMaintenanceFileLock(backup)
	if err != nil {
		t.Fatal(err)
	}
	oldCommand := systemPackageUpdateCommand
	t.Cleanup(func() { systemPackageUpdateCommand = oldCommand })
	systemPackageUpdateCommand = func(context.Context, string, ...string) error {
		t.Fatal("APT command ran through active backup")
		return nil
	}
	if err := RunSystemPackageUpdatePlan(p.PlanPath); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	status := ReadSystemPackageUpdateStatus(cfg)
	if status.Status != "deferred" {
		t.Fatalf("busy installation was not deferred: %+v", status)
	}
	var phase, reason string
	var overtime bool
	if err := db.QueryRow(`SELECT phase,message_code,overtime_allowed FROM system_maintenance_actions WHERE id=?`, round.ID).Scan(&phase, &reason, &overtime); err != nil {
		t.Fatal(err)
	}
	if phase != "install" || reason != "file_backup_busy" || overtime {
		t.Fatalf("deferred installation falsely advanced: %s %s %v", phase, reason, overtime)
	}
	// A denied install is still an install phase, not an empty runtime scope
	// that could be incorrectly marked complete outside its window.
	f.now = f.now.Add(5 * time.Hour)
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 0 {
		t.Fatal("deferred install restarted outside window")
	}
	f.now = f.now.Add(-5 * time.Hour)
	if err := runMaintenanceCycle(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	if f.installs != 1 {
		t.Fatalf("deferred install did not resume: %d", f.installs)
	}
}

func TestMaintenanceLifecycleAndMigrationAdmissionAreCrossProcessLocks(t *testing.T) {
	cfg, _ := setupMaintenanceRunner(t)
	db := database.GetDB()
	lock, err := acquireMaintenanceLifecycleFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if other, err := acquireMaintenanceLifecycleFile(cfg); err == nil {
		other.Close()
		t.Fatal("lifecycle lock did not exclude independent fd")
	}
	round, err := createMaintenanceRound(db, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	p := maintenanceInstallPlanFixture(t, cfg, round.ID)
	connection, release, reason, err := prepareMaintenancePackageRun(p)
	if connection != nil {
		connection.Close()
	}
	if release != nil {
		release()
		t.Fatal("installer entered active lifecycle operation")
	}
	if err != nil || reason != "maintenance_busy" {
		t.Fatalf("lifecycle admission: %s %v", reason, err)
	}
	store, _ := newSiteMigrationStore(db)
	siteID := 1
	if err := store.acquireLock(context.Background(), "migration_0000001", &siteID, "example.test", "source", time.Now()); !errors.Is(err, errSiteMigrationBusy) {
		t.Fatalf("migration lock admitted: %v", err)
	}
	source := &SiteMigrationSourcePreparationService{db: db, now: time.Now}
	if err := source.DeclareBatch(context.Background(), "peer_00000000001", "batch_0000000001", "admin", []SiteMigrationSourceDeclaration{{ID: "migration_0000001", SourceSiteID: 1, Domain: "example.test", SiteType: "wordpress"}}); !errors.Is(err, errSiteMigrationBusy) {
		t.Fatalf("source declaration admitted: %v", err)
	}
	target := &SiteMigrationTargetTransferService{db: db, now: time.Now}
	if _, err := target.begin(context.Background(), "migration_0000001", "worker-1"); !errors.Is(err, errSiteMigrationBusy) {
		t.Fatalf("target transfer admitted: %v", err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM site_migration_locks`).Scan(&count)
	if count != 0 {
		t.Fatal("blocked migration published active lock")
	}
}

func TestMaintenanceInstallerAllowsOnlyItsOwnQueuedPackageTask(t *testing.T) {
	cfg, f := setupMaintenanceRunner(t)
	db := database.GetDB()
	round, err := createMaintenanceRound(db, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	p := maintenanceInstallPlanFixture(t, cfg, round.ID)
	oldLive := systemPackageUpdateUnitLive
	t.Cleanup(func() { systemPackageUpdateUnitLive = oldLive })
	systemPackageUpdateUnitLive = func(string) bool { return true }
	status := SystemPackageUpdateStatus{ID: p.ID, Status: "running", StartedAt: f.now.Format(time.RFC3339), UpdatedAt: f.now.Format(time.RFC3339)}
	if err := writePanelDBRestoreJSON(p.StatusPath, status); err != nil {
		t.Fatal(err)
	}
	connection, release, reason, err := prepareMaintenancePackageRun(p)
	if err != nil || release == nil {
		if connection != nil {
			connection.Close()
		}
		t.Fatalf("own queued task blocked itself: %s %v", reason, err)
	}
	release()
	connection.Close()
	status.ID = "another-task"
	if err := writePanelDBRestoreJSON(p.StatusPath, status); err != nil {
		t.Fatal(err)
	}
	connection, release, reason, err = prepareMaintenancePackageRun(p)
	if connection != nil {
		connection.Close()
	}
	if release != nil {
		release()
		t.Fatal("installer ignored another task")
	}
	if err != nil || reason != "package_manager_busy" {
		t.Fatalf("other task: %s %v", reason, err)
	}
}
