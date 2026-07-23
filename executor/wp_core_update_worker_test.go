package executor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

func TestNewWPCoreUpdateWorkerBuildsInertProductionGraph(t *testing.T) {
	store, _ := newWPUpdateStoreTest(t)
	backupRoot := t.TempDir()
	wwwRoot := t.TempDir()
	cfg := &config.Config{Panel: config.PanelConfig{BackupDir: backupRoot}, Paths: config.PathsConfig{WWWRoot: wwwRoot}}
	worker, err := NewWPCoreUpdateWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	artifact, ok := worker.backups.(*wpUpdateArtifactService)
	wantRoot := filepath.Join(backupRoot, "wp-updates")
	if !ok || worker.store.db != store.db || artifact.root != wantRoot {
		t.Fatalf("production graph store/root mismatch: artifact=%T root=%q", worker.backups, artifact.root)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	stopWPCoreUpdateWorker(t, worker)
	entries, err := os.ReadDir(wantRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty worker created task artifacts: entries=%v err=%v", entries, err)
	}
}

func TestNewWPCoreUpdateWorkerRejectsUnsafeBackupRoots(t *testing.T) {
	newWPUpdateStoreTest(t)
	wwwRoot := t.TempDir()
	for _, root := range []string{"relative", string(filepath.Separator)} {
		cfg := &config.Config{Panel: config.PanelConfig{BackupDir: root}, Paths: config.PathsConfig{WWWRoot: wwwRoot}}
		if _, err := NewWPCoreUpdateWorker(cfg); err == nil {
			t.Fatalf("accepted unsafe backup root %q", root)
		}
	}
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "backup-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Panel: config.PanelConfig{BackupDir: link}, Paths: config.PathsConfig{WWWRoot: wwwRoot}}
	if _, err := NewWPCoreUpdateWorker(cfg); err == nil {
		t.Fatal("accepted symlink backup root")
	}
}

func TestObserveWPCoreUpdateVersionReadsActiveWordPressOnly(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	root := filepath.Join(t.TempDir(), "site")
	writeVersionFixture(t, root, "7.0.2", "zh_CN")
	if _, err := store.db.Exec(`UPDATE websites SET web_root=? WHERE id=?`, root, siteID); err != nil {
		t.Fatal(err)
	}
	version, err := observeWPCoreUpdateVersion(context.Background(), store, siteID)
	if err != nil || version != "7.0.2" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET status='paused' WHERE id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := observeWPCoreUpdateVersion(context.Background(), store, siteID); err == nil {
		t.Fatal("observed paused site")
	}
}

type fakeWPCoreWorkerBackups struct {
	store    *wpUpdateStore
	mu       sync.Mutex
	calls    []string
	block    bool
	panicNow bool
	started  chan struct{}
	release  <-chan struct{}
}

func (f *fakeWPCoreWorkerBackups) prepareCoreBackups(ctx context.Context, id, owner string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "backup")
	f.mu.Unlock()
	if f.started != nil {
		close(f.started)
	}
	if f.panicNow {
		panic("secret panic payload")
	}
	if f.release != nil {
		<-f.release
		return ctx.Err()
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.store.advanceOwnedStage(ctx, id, owner, "claimed", "backups_ready", time.Now().UTC())
}

type fakeWPCoreWorkerExecutor struct {
	store *wpUpdateStore
	mu    sync.Mutex
	calls []string
}

func (f *fakeWPCoreWorkerExecutor) Execute(ctx context.Context, id, owner string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "execute")
	f.mu.Unlock()
	return f.store.markSuccess(ctx, id, owner, time.Now().UTC())
}

type unusedWPPluginWorkerTasks struct{}

func (unusedWPPluginWorkerTasks) validateAndClaimPluginUpdate(context.Context, string, string, string) (WPUpdateTask, error) {
	return WPUpdateTask{}, context.Canceled
}
func (unusedWPPluginWorkerTasks) preparePluginBackups(context.Context, string, string) error {
	return context.Canceled
}

type fakeWPPluginWorkerSupervisor struct {
	state wpPluginScopeState
	err   error
}

func (f fakeWPPluginWorkerSupervisor) Inspect(context.Context, string) (wpPluginScopeState, error) {
	return f.state, f.err
}

func TestWPCoreUpdateWorkerClaimsBacksUpAndExecutes(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	backups := &fakeWPCoreWorkerBackups{store: store}
	executor := &fakeWPCoreWorkerExecutor{store: store}
	worker := newTestWPCoreUpdateWorker(t, store, backups, executor, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateSuccess)
	stopWPCoreUpdateWorker(t, worker)
	backups.mu.Lock()
	backupCalls := append([]string(nil), backups.calls...)
	backups.mu.Unlock()
	executor.mu.Lock()
	executorCalls := append([]string(nil), executor.calls...)
	executor.mu.Unlock()
	if len(backupCalls) != 1 || len(executorCalls) != 1 {
		t.Fatalf("backup calls=%v executor calls=%v", backupCalls, executorCalls)
	}
}

func TestWPCoreUpdateWorkerClaimsBacksUpAndExecutesPlugin(t *testing.T) {
	service, store, task := preparePluginSnapshotPlan(t)
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	pluginRoot := filepath.Join(webRoot, "wp-content", "plugins", "sample")
	if err := os.MkdirAll(pluginRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "sample.php"), []byte("<?php\n/*\nPlugin Name: Sample\nVersion: 1.0.0\n*/"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=?,db_name='wordpress_db' WHERE id=?`, webRoot, task.SiteID); err != nil {
		t.Fatal(err)
	}
	source := writePluginPackageFixture(t, "sample", "sample.php", "1.1.0")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = service.snapshotValidateAndSealPluginPackage(context.Background(), task.ID, source, digest)
	if err != nil {
		t.Fatal(err)
	}
	coreBackups := &fakeWPCoreWorkerBackups{store: store}
	coreExecutor := &fakeWPCoreWorkerExecutor{store: store}
	pluginExecutor := &fakeWPCoreWorkerExecutor{store: store}
	worker, err := newWPCoreUpdateWorker(wpCoreUpdateWorkerOptions{
		store: store, backups: coreBackups, executor: coreExecutor,
		observeVersion: func(context.Context, int) (string, error) { return "7.0.1", nil },
		pluginTasks:    service, pluginExecutor: pluginExecutor,
		pluginSupervisor: fakeWPPluginWorkerSupervisor{state: wpPluginScopeState{
			LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
		}},
		observePlugin: func(ctx context.Context, task WPUpdateTask) (string, error) {
			return observeWPPluginUpdateVersion(ctx, store, task)
		},
		owner: "test-plugin-worker", pollInterval: 5 * time.Millisecond,
		heartbeatInterval: 20 * time.Millisecond, sweepInterval: 20 * time.Millisecond, now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateSuccess)
	stopWPCoreUpdateWorker(t, worker)
	if len(coreBackups.calls) != 0 || len(coreExecutor.calls) != 0 || len(pluginExecutor.calls) != 1 {
		t.Fatalf("core backup=%v core executor=%v plugin executor=%v", coreBackups.calls, coreExecutor.calls, pluginExecutor.calls)
	}
	var backups int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups WHERE task_id=?`, task.ID).Scan(&backups); err != nil || backups != 2 {
		t.Fatalf("backups=%d err=%v", backups, err)
	}
}

func TestWPCoreUpdateWorkerClaimsBacksUpAndExecutesTheme(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	seedThemeUpdateCandidate(t, store, siteID, "sample-theme", "1.0.0", "1.1.0", "collection-theme-worker")
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	themeRoot := filepath.Join(webRoot, "wp-content", "themes", "sample-theme")
	if err := os.MkdirAll(themeRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeRoot, "style.css"), []byte("/*\nTheme Name: Sample\nVersion: 1.0.0\n*/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=?,db_name='wordpress_db' WHERE id=?`, webRoot, siteID); err != nil {
		t.Fatal(err)
	}
	task, err := store.createThemeManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample-theme", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/theme/sample-theme.1.1.0.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	service, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	source := writeThemePackageFixture(t, "sample-theme", "1.1.0", "")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = service.snapshotValidateAndSealThemePackage(context.Background(), task.ID, source, digest, "")
	if err != nil {
		t.Fatal(err)
	}
	coreBackups := &fakeWPCoreWorkerBackups{store: store}
	coreExecutor := &fakeWPCoreWorkerExecutor{store: store}
	themeExecutor := &fakeWPCoreWorkerExecutor{store: store}
	worker, err := newWPCoreUpdateWorker(wpCoreUpdateWorkerOptions{
		store: store, backups: coreBackups, executor: coreExecutor,
		observeVersion: func(context.Context, int) (string, error) { return "7.0.1", nil },
		pluginTasks:    unusedWPPluginWorkerTasks{}, pluginExecutor: &fakeWPCoreWorkerExecutor{store: store},
		observePlugin: func(context.Context, WPUpdateTask) (string, error) { return "1.0.0", nil },
		pluginSupervisor: fakeWPPluginWorkerSupervisor{state: wpPluginScopeState{
			LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
		}},
		themeTasks: service, themeExecutor: themeExecutor,
		themeSupervisor: fakeWPPluginWorkerSupervisor{state: wpPluginScopeState{
			LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
		}},
		observeTheme: func(ctx context.Context, task WPUpdateTask) (wpThemeUpdateIdentity, error) {
			return observeWPThemeUpdateIdentity(ctx, store, task)
		},
		owner: "test-theme-worker", pollInterval: 5 * time.Millisecond,
		heartbeatInterval: 20 * time.Millisecond, sweepInterval: 20 * time.Millisecond, now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateSuccess)
	stopWPCoreUpdateWorker(t, worker)
	if len(coreBackups.calls) != 0 || len(coreExecutor.calls) != 0 || len(themeExecutor.calls) != 1 {
		t.Fatalf("core backup=%v core executor=%v theme executor=%v", coreBackups.calls, coreExecutor.calls, themeExecutor.calls)
	}
	var backups int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_backups
		WHERE task_id=? AND kind IN ('database','theme_files')`, task.ID).Scan(&backups); err != nil || backups != 2 {
		t.Fatalf("backups=%d err=%v", backups, err)
	}
}

func TestWPCoreUpdateWorkerThemeRecoveryUsesSupervisorState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scope      wpPluginScopeState
		wantStatus string
	}{
		{
			name: "active scope remains owned",
			scope: wpPluginScopeState{
				LoadState: "loaded", ActiveState: "active", SubState: "running", MainPID: 123,
			},
			wantStatus: wpUpdateRunning,
		},
		{
			name: "stopped scope becomes interrupted",
			scope: wpPluginScopeState{
				LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
			},
			wantStatus: wpUpdateInterrupted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, store, task := prepareRunningThemeWorkerTask(t)
			worker, err := newWPCoreUpdateWorker(wpCoreUpdateWorkerOptions{
				store: store, backups: &fakeWPCoreWorkerBackups{store: store}, executor: &fakeWPCoreWorkerExecutor{store: store},
				observeVersion: func(context.Context, int) (string, error) { return "7.0.1", nil },
				pluginTasks:    unusedWPPluginWorkerTasks{}, pluginExecutor: &fakeWPCoreWorkerExecutor{store: store},
				observePlugin: func(context.Context, WPUpdateTask) (string, error) { return "1.0.0", nil },
				pluginSupervisor: fakeWPPluginWorkerSupervisor{state: wpPluginScopeState{
					LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
				}},
				themeTasks: service, themeExecutor: &fakeWPCoreWorkerExecutor{store: store},
				observeTheme: func(context.Context, WPUpdateTask) (wpThemeUpdateIdentity, error) {
					return wpThemeUpdateIdentity{Version: "1.0.0"}, nil
				},
				themeSupervisor: fakeWPPluginWorkerSupervisor{state: tc.scope},
				owner:           "test-theme-recovery", pollInterval: 5 * time.Millisecond,
				heartbeatInterval: 20 * time.Millisecond, sweepInterval: 20 * time.Millisecond, now: time.Now,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Start(); err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus == wpUpdateInterrupted {
				waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateInterrupted)
			} else {
				time.Sleep(40 * time.Millisecond)
				current, err := store.getTask(context.Background(), task.ID)
				if err != nil || current.Status != wpUpdateRunning || current.LeaseOwner != "worker-theme-recovery" {
					t.Fatalf("active theme task was recovered: task=%+v err=%v", current, err)
				}
			}
			stopWPCoreUpdateWorker(t, worker)
		})
	}
}

func prepareRunningThemeWorkerTask(t *testing.T) (*wpUpdateArtifactService, *wpUpdateStore, WPUpdateTask) {
	t.Helper()
	store, siteID := newWPUpdateStoreTest(t)
	seedThemeUpdateCandidate(t, store, siteID, "sample-theme", "1.0.0", "1.1.0", "collection-theme-recovery")
	task, err := store.createThemeManualPlan(context.Background(), WPUpdatePlan{
		SiteID: siteID, ComponentKey: "sample-theme", CurrentVersion: "1.0.0", TargetVersion: "1.1.0",
		PackageSource: "wordpress.org", DownloadURL: "https://downloads.wordpress.org/theme/sample-theme.1.1.0.zip",
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	service, err := newWPUpdateArtifactService(store, filepath.Join(t.TempDir(), "artifacts"), fakeUpdateDump)
	if err != nil {
		t.Fatal(err)
	}
	source := writeThemePackageFixture(t, "sample-theme", "1.1.0", "")
	digest, _, err := hashRegularFile(source)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = service.snapshotValidateAndSealThemePackage(context.Background(), task.ID, source, digest, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err = service.validateAndClaimThemeUpdate(context.Background(), task.ID, "worker-theme-recovery", "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	return service, store, task
}

func TestWPCoreUpdateWorkerRestartDefersActivePluginScopeRecovery(t *testing.T) {
	service, store, task, _ := prepareRunningPluginArtifactTask(t, fakeUpdateDump)
	worker := newPluginRecoveryTestWorker(t, store, service, fakeWPPluginWorkerSupervisor{state: wpPluginScopeState{
		LoadState: "loaded", ActiveState: "active", SubState: "running", MainPID: 123,
	}})
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	stopWPCoreUpdateWorker(t, worker)
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateRunning || current.LeaseOwner != "worker-plugin" {
		t.Fatalf("active supervised task was recovered: task=%+v err=%v", current, err)
	}
}

func TestWPCoreUpdateWorkerRestartRecoversStoppedPluginScope(t *testing.T) {
	service, store, task, _ := prepareRunningPluginArtifactTask(t, fakeUpdateDump)
	worker := newPluginRecoveryTestWorker(t, store, service, fakeWPPluginWorkerSupervisor{state: wpPluginScopeState{
		LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
	}})
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateInterrupted)
	stopWPCoreUpdateWorker(t, worker)
}

func newPluginRecoveryTestWorker(t *testing.T, store *wpUpdateStore, service wpPluginUpdateTaskService, supervisor wpPluginUpdateSupervisor) *WPCoreUpdateWorker {
	t.Helper()
	worker, err := newWPCoreUpdateWorker(wpCoreUpdateWorkerOptions{
		store:   store,
		backups: &fakeWPCoreWorkerBackups{store: store}, executor: &fakeWPCoreWorkerExecutor{store: store},
		observeVersion: func(context.Context, int) (string, error) { return "7.0.1", nil },
		pluginTasks:    service, pluginExecutor: &fakeWPCoreWorkerExecutor{store: store},
		observePlugin:    func(context.Context, WPUpdateTask) (string, error) { return "1.0.0", nil },
		pluginSupervisor: supervisor,
		owner:            "test-restart-worker", pollInterval: 5 * time.Millisecond,
		heartbeatInterval: 20 * time.Millisecond, sweepInterval: 20 * time.Millisecond, now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestObserveWPPluginUpdateVersionRejectsSymlinkAndReadsHeader(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	pluginRoot := filepath.Join(webRoot, "wp-content", "plugins", "sample")
	if err := os.MkdirAll(pluginRoot, 0755); err != nil {
		t.Fatal(err)
	}
	mainFile := filepath.Join(pluginRoot, "sample.php")
	if err := os.WriteFile(mainFile, []byte("<?php\n/* Plugin Name: Sample\nVersion: 1.2.3 */"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE websites SET web_root=? WHERE id=?`, webRoot, siteID); err != nil {
		t.Fatal(err)
	}
	task := WPUpdateTask{SiteID: siteID, ComponentType: "plugin", ComponentKey: "sample/sample.php"}
	version, err := observeWPPluginUpdateVersion(context.Background(), store, task)
	if err != nil || version != "1.2.3" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	target := filepath.Join(t.TempDir(), "outside.php")
	if err := os.WriteFile(target, []byte("<?php /* Plugin Name: Outside Version: 9.9.9 */"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mainFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, mainFile); err != nil {
		t.Fatal(err)
	}
	if _, err := observeWPPluginUpdateVersion(context.Background(), store, task); err == nil {
		t.Fatal("symlink plugin main file was observed")
	}
}

func TestWPCoreUpdateWorkerRestartNeverResumesRunningStage(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Hour))
	old := time.Now()
	if _, err := store.claimCoreUpdate(context.Background(), task.ID, "dead-worker", "7.0.1", old); err != nil {
		t.Fatal(err)
	}
	if err := store.advanceOwnedStage(context.Background(), task.ID, "dead-worker", "claimed", "updating_core", old); err != nil {
		t.Fatal(err)
	}
	backups := &fakeWPCoreWorkerBackups{store: store}
	executor := &fakeWPCoreWorkerExecutor{store: store}
	worker := newTestWPCoreUpdateWorker(t, store, backups, executor, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateInterrupted)
	stopWPCoreUpdateWorker(t, worker)
	if len(backups.calls) != 0 || len(executor.calls) != 0 {
		t.Fatalf("expired task was resumed: backup=%v executor=%v", backups.calls, executor.calls)
	}
}

func TestWPCoreUpdateWorkerPanicBecomesInterruptedWithoutPayload(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	backups := &fakeWPCoreWorkerBackups{store: store, panicNow: true}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	waitWPUpdateTaskStatus(t, store, task.ID, wpUpdateInterrupted)
	stopWPCoreUpdateWorker(t, worker)
	var leaked int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM wp_update_task_events WHERE task_id=? AND (summary LIKE '%secret%' OR error_code LIKE '%secret%')`, task.ID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("panic payload leaked: count=%d err=%v", leaked, err)
	}
}

func TestWPCoreUpdateWorkerStopMarksOwnedTaskInterrupted(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, block: true, started: started}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	stopWPCoreUpdateWorker(t, worker)
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateInterrupted || !current.RequiresAttention || current.LeaseOwner != "" {
		t.Fatalf("task=%+v err=%v", current, err)
	}
}

func TestWPCoreUpdateWorkerHeartbeatOwnershipLossStopsWithoutOverwrite(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, block: true, started: started}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	worker.heartbeatInterval = 5 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	if _, err := store.db.Exec(`UPDATE wp_update_tasks SET lease_owner='replacement-worker' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	current, err := store.getTask(context.Background(), task.ID)
	if err != nil || current.Status != wpUpdateRunning || current.LeaseOwner != "replacement-worker" {
		t.Fatalf("task=%+v err=%v", current, err)
	}
	stopWPCoreUpdateWorker(t, worker)
}

func TestWPCoreUpdateWorkerHeartbeatRenewsLease(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	task := createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, block: true, started: started}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	worker.heartbeatInterval = 5 * time.Millisecond
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	before, err := store.getTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, err := store.getTask(context.Background(), task.ID)
		if err == nil && current.LeaseExpiresAt > before.LeaseExpiresAt {
			stopWPCoreUpdateWorker(t, worker)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopWPCoreUpdateWorker(t, worker)
	t.Fatal("lease was not renewed")
}

func TestWPCoreUpdateWorkerStopTimeoutReleasesInstanceAfterRunExits(t *testing.T) {
	store, siteID := newWPUpdateStoreTest(t)
	createAndSealUpdateTask(t, store, siteID, time.Now().Add(-time.Minute))
	started := make(chan struct{})
	release := make(chan struct{})
	backups := &fakeWPCoreWorkerBackups{store: store, started: started, release: release}
	worker := newTestWPCoreUpdateWorker(t, store, backups, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := worker.Stop(stopCtx)
	cancel()
	if err == nil {
		t.Fatal("expected Stop timeout")
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		next := newTestWPCoreUpdateWorker(t, store, &fakeWPCoreWorkerBackups{store: store}, &fakeWPCoreWorkerExecutor{store: store}, "7.0.1")
		if err := next.Start(); err == nil {
			stopWPCoreUpdateWorker(t, next)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("worker instance remained permanently active")
}

func newTestWPCoreUpdateWorker(t *testing.T, store *wpUpdateStore, backups wpCoreUpdateBackupPreparer, executor wpCoreUpdateTaskExecutor, version string) *WPCoreUpdateWorker {
	t.Helper()
	worker, err := newWPCoreUpdateWorker(wpCoreUpdateWorkerOptions{
		store: store, backups: backups, executor: executor,
		observeVersion: func(context.Context, int) (string, error) { return version, nil },
		pluginTasks:    unusedWPPluginWorkerTasks{},
		pluginExecutor: &fakeWPCoreWorkerExecutor{store: store},
		observePlugin:  func(context.Context, WPUpdateTask) (string, error) { return "", context.Canceled },
		pluginSupervisor: fakeWPPluginWorkerSupervisor{state: wpPluginScopeState{
			LoadState: "not-found", ActiveState: "inactive", SubState: "dead",
		}},
		owner: "test-core-worker", pollInterval: 5 * time.Millisecond,
		heartbeatInterval: 20 * time.Millisecond, sweepInterval: 20 * time.Millisecond, now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func stopWPCoreUpdateWorker(t *testing.T, worker *WPCoreUpdateWorker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitWPUpdateTaskStatus(t *testing.T, store *wpUpdateStore, id, status string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.getTask(context.Background(), id)
		if err == nil && task.Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	task, err := store.getTask(context.Background(), id)
	t.Fatalf("task status=%s err=%v want=%s", task.Status, err, status)
}
