package executor

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type fakeMigrationStageOps struct {
	db       *sql.DB
	events   []string
	settings SiteMigrationRuntimeSettings
}

func (f *fakeMigrationStageOps) Transfer(_ context.Context, id, _ string) error {
	f.events = append(f.events, "transfer")
	_, err := f.db.Exec(`UPDATE site_migration_sites SET stage='preparing_target' WHERE id=?`, id)
	return err
}
func (f *fakeMigrationStageOps) RuntimeSettings(context.Context, string, string) (SiteMigrationRuntimeSettings, error) {
	f.events = append(f.events, "settings_remote")
	return f.settings, nil
}
func (f *fakeMigrationStageOps) StoreTargetSettings(_ context.Context, id string, _ SiteMigrationRuntimeSettings) error {
	f.events = append(f.events, "settings_store")
	return nil
}
func (f *fakeMigrationStageOps) Create(_ context.Context, id string, _ SiteMigrationTargetSpec) error {
	f.events = append(f.events, "create")
	_, err := f.db.Exec(`UPDATE site_migration_sites SET stage='publishing' WHERE id=?`, id)
	return err
}
func (f *fakeMigrationStageOps) PublishData(_ context.Context, id string) error {
	f.events = append(f.events, "publish")
	_, err := f.db.Exec(`UPDATE site_migration_sites SET stage='configuring_target' WHERE id=?`, id)
	return err
}
func (f *fakeMigrationStageOps) ConfigureAndHealth(_ context.Context, id string, _ siteMigrationTargetConfigureOps) error {
	f.events = append(f.events, "configure")
	_, err := f.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='awaiting_cutover' WHERE id=?`, id)
	return err
}
func (f *fakeMigrationStageOps) ActivateAfterHealth(_ context.Context, id string) error {
	f.events = append(f.events, "activate")
	_, err := f.db.Exec(`UPDATE site_migration_sites SET status='completed',stage='completed' WHERE id=?`, id)
	return err
}

func TestSiteMigrationStageProcessorRunsTargetLifecycleUnderLease(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET direction='target',status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	expires := freezerTestTime().Add(time.Minute)
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET status='running',stage='preflight_passed',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, expires); err != nil {
		t.Fatal(err)
	}
	ops := &fakeMigrationStageOps{db: database.GetDB(), settings: SiteMigrationRuntimeSettings{Aliases: []string{"www.example.com"}, MarkerToken: strings.Repeat("m", 48), FastCGICacheTTL: 60, MonitoringInterval: 5, LogRetentionDays: 7, PHPFPMMaxChildren: 2}}
	p := &siteMigrationStageProcessor{db: database.GetDB(), remote: ops, transfer: ops, settings: ops, resources: ops, publisher: ops, cutover: ops, now: freezerTestTime}
	job, err := getSiteMigrationSite(context.Background(), store.db, "migration_0000001")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	want := "transfer,settings_remote,settings_store,create,publish,configure,activate"
	if got := strings.Join(ops.events, ","); got != want {
		t.Fatalf("events=%q want=%q", got, want)
	}
	var status, stage string
	_ = store.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage)
	if status != "completed" || stage != "completed" {
		t.Fatalf("status=%q stage=%q", status, stage)
	}
}

func TestSiteMigrationStageProcessorCompletesRecoveredAutomaticActivation(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	now := freezerTestTime()
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET direction='target',status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET status='awaiting_cutover',stage='awaiting_cutover',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.recoverExpired(context.Background(), now); err != nil || changed != 1 {
		t.Fatalf("recover changed=%d err=%v", changed, err)
	}
	job, err := store.claimNext(context.Background(), "worker_0000000002", now, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	ops := &fakeMigrationStageOps{db: store.db}
	processor := &siteMigrationStageProcessor{db: store.db, cutover: ops, now: func() time.Time { return now }}
	if err := processor.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ops.events, ","); got != "activate" {
		t.Fatalf("events=%q", got)
	}
	var status, stage string
	if err := store.db.QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id=?`, job.ID).Scan(&status, &stage); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || stage != "completed" {
		t.Fatalf("status=%q stage=%q", status, stage)
	}
}
