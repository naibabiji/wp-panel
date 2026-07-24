package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const (
	wpCoreUpdateWorkerPollInterval      = time.Second
	wpCoreUpdateWorkerHeartbeatInterval = 30 * time.Second
	wpCoreUpdateWorkerSweepInterval     = 30 * time.Second
)

var wpCoreUpdateWorkerSlot = make(chan struct{}, 1)
var wpCoreUpdateWorkerInstance struct {
	sync.Mutex
	active bool
}

type wpCoreUpdateBackupPreparer interface {
	prepareCoreBackups(context.Context, string, string) error
}

type wpCoreUpdateTaskExecutor interface {
	Execute(context.Context, string, string) error
}

type wpCoreUpdateVersionObserver func(context.Context, int) (string, error)

type wpPluginUpdateTaskService interface {
	validateAndClaimPluginUpdate(context.Context, string, string, string) (WPUpdateTask, error)
	preparePluginBackups(context.Context, string, string) error
}

type wpPluginUpdateVersionObserver func(context.Context, WPUpdateTask) (string, error)

type wpPluginUpdateSupervisor interface {
	Inspect(context.Context, string) (wpPluginScopeState, error)
}

type wpThemeUpdateTaskService interface {
	validateAndClaimThemeUpdate(context.Context, string, string, string, string) (WPUpdateTask, error)
	prepareThemeBackups(context.Context, string, string) error
}

type wpThemeUpdateIdentity struct {
	Version, Template string
}

type wpThemeUpdateObserver func(context.Context, WPUpdateTask) (wpThemeUpdateIdentity, error)

type WPCoreUpdateWorker struct {
	store             *wpUpdateStore
	backups           wpCoreUpdateBackupPreparer
	executor          wpCoreUpdateTaskExecutor
	observeVersion    wpCoreUpdateVersionObserver
	pluginTasks       wpPluginUpdateTaskService
	pluginExecutor    wpCoreUpdateTaskExecutor
	observePlugin     wpPluginUpdateVersionObserver
	pluginSupervisor  wpPluginUpdateSupervisor
	themeTasks        wpThemeUpdateTaskService
	themeExecutor     wpCoreUpdateTaskExecutor
	observeTheme      wpThemeUpdateObserver
	themeSupervisor   wpPluginUpdateSupervisor
	batchOrchestrator *wpPluginBatchOrchestrator
	owner             string
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	sweepInterval     time.Duration
	now               func() time.Time

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

type wpCoreUpdateWorkerOptions struct {
	store             *wpUpdateStore
	backups           wpCoreUpdateBackupPreparer
	executor          wpCoreUpdateTaskExecutor
	observeVersion    wpCoreUpdateVersionObserver
	pluginTasks       wpPluginUpdateTaskService
	pluginExecutor    wpCoreUpdateTaskExecutor
	observePlugin     wpPluginUpdateVersionObserver
	pluginSupervisor  wpPluginUpdateSupervisor
	themeTasks        wpThemeUpdateTaskService
	themeExecutor     wpCoreUpdateTaskExecutor
	observeTheme      wpThemeUpdateObserver
	themeSupervisor   wpPluginUpdateSupervisor
	batchOrchestrator *wpPluginBatchOrchestrator
	owner             string
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	sweepInterval     time.Duration
	now               func() time.Time
}

func NewWPCoreUpdateWorker(cfg *config.Config) (*WPCoreUpdateWorker, error) {
	if cfg == nil || database.GetDB() == nil || !validWPCoreUpdateRoot(cfg.Panel.BackupDir) || !filepath.IsAbs(cfg.Paths.WWWRoot) {
		return nil, errors.New("invalid core update worker configuration")
	}
	root := filepath.Join(filepath.Clean(cfg.Panel.BackupDir), "wp-updates")
	store := newWPUpdateStore(database.GetDB())
	backups, err := newWPUpdateArtifactService(store, root, defaultWPUpdateDatabaseDumper)
	if err != nil {
		return nil, errors.New("core update backup service unavailable")
	}
	ops, err := newDefaultWPCoreSystemOperations(filepath.Clean(cfg.Paths.WWWRoot))
	if err != nil {
		return nil, errors.New("core update operations unavailable")
	}
	executor, err := newWPCoreUpdateExecutor(store, root, ops)
	if err != nil {
		return nil, errors.New("core update executor unavailable")
	}
	pluginOps, err := newDefaultWPPluginSystemOperations(store, filepath.Clean(cfg.Paths.WWWRoot))
	if err != nil {
		return nil, errors.New("plugin update operations unavailable")
	}
	pluginExecutor, err := newWPPluginUpdateExecutor(store, root, pluginOps)
	if err != nil {
		return nil, errors.New("plugin update executor unavailable")
	}
	themeOps, err := newDefaultWPThemeSystemOperations(store, filepath.Clean(cfg.Paths.WWWRoot))
	if err != nil {
		return nil, errors.New("theme update operations unavailable")
	}
	themeExecutor, err := newWPThemeUpdateExecutor(store, root, themeOps)
	if err != nil {
		return nil, errors.New("theme update executor unavailable")
	}
	pluginService, err := NewWPPluginUpdateService(database.GetDB(), cfg.Panel.BackupDir)
	if err != nil {
		return nil, errors.New("plugin update service unavailable")
	}
	batchOrchestrator, err := newWPPluginBatchOrchestrator(store, pluginService)
	if err != nil {
		return nil, errors.New("plugin batch orchestrator unavailable")
	}
	owner, err := newWPCoreUpdateWorkerOwner()
	if err != nil {
		return nil, errors.New("core update worker identity unavailable")
	}
	return newWPCoreUpdateWorker(wpCoreUpdateWorkerOptions{
		store: store, backups: backups, executor: executor,
		observeVersion: func(ctx context.Context, siteID int) (string, error) {
			return observeWPCoreUpdateVersion(ctx, store, siteID)
		},
		pluginTasks:      backups,
		pluginExecutor:   pluginExecutor,
		pluginSupervisor: pluginOps.supervisor,
		observePlugin: func(ctx context.Context, task WPUpdateTask) (string, error) {
			return observeWPPluginUpdateVersion(ctx, store, task)
		},
		themeTasks:      backups,
		themeExecutor:   themeExecutor,
		themeSupervisor: themeOps.supervisor,
		observeTheme: func(ctx context.Context, task WPUpdateTask) (wpThemeUpdateIdentity, error) {
			return observeWPThemeUpdateIdentity(ctx, store, task)
		},
		batchOrchestrator: batchOrchestrator,
		owner:             owner, pollInterval: wpCoreUpdateWorkerPollInterval,
		heartbeatInterval: wpCoreUpdateWorkerHeartbeatInterval,
		sweepInterval:     wpCoreUpdateWorkerSweepInterval, now: time.Now,
	})
}

func observeWPThemeUpdateIdentity(ctx context.Context, store *wpUpdateStore, task WPUpdateTask) (wpThemeUpdateIdentity, error) {
	if store == nil || store.db == nil || task.SiteID <= 0 || task.ComponentType != "theme" || !validWPThemeComponentKey(task.ComponentKey) {
		return wpThemeUpdateIdentity{}, errors.New("invalid theme update identity observation")
	}
	var webRoot string
	if err := store.db.QueryRowContext(ctx, `SELECT web_root FROM websites
		WHERE id=? AND site_type='wordpress' AND status='active'`, task.SiteID).Scan(&webRoot); err != nil {
		return wpThemeUpdateIdentity{}, errors.New("theme update site unavailable")
	}
	webRoot, err := safeSiteWebRoot(webRoot)
	if err != nil {
		return wpThemeUpdateIdentity{}, errors.New("theme update site unavailable")
	}
	version, template, err := readInstalledWPThemeIdentity(webRoot, task.ComponentKey)
	if err != nil {
		return wpThemeUpdateIdentity{}, err
	}
	return wpThemeUpdateIdentity{Version: version, Template: template}, nil
}

func validWPCoreUpdateRoot(backupDir string) bool {
	if !filepath.IsAbs(backupDir) {
		return false
	}
	clean := filepath.Clean(backupDir)
	if clean == string(filepath.Separator) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(clean)
	return err == nil && resolved == clean
}

func observeWPCoreUpdateVersion(ctx context.Context, store *wpUpdateStore, siteID int) (string, error) {
	if store == nil || store.db == nil || siteID <= 0 {
		return "", errors.New("invalid core update version observation")
	}
	var webRoot string
	if err := store.db.QueryRowContext(ctx, `SELECT web_root FROM websites
		WHERE id=? AND site_type='wordpress' AND status='active'`, siteID).Scan(&webRoot); err != nil || !filepath.IsAbs(webRoot) {
		return "", errors.New("core update site unavailable")
	}
	identity, err := readInstalledWordPressIdentity(filepath.Clean(webRoot))
	if err != nil {
		return "", errors.New("installed WordPress version unavailable")
	}
	return identity.Version, nil
}

func observeWPPluginUpdateVersion(ctx context.Context, store *wpUpdateStore, task WPUpdateTask) (string, error) {
	if store == nil || store.db == nil || task.SiteID <= 0 || task.ComponentType != "plugin" || !validWPPluginComponentKey(task.ComponentKey) {
		return "", errors.New("invalid plugin update version observation")
	}
	var webRoot string
	if err := store.db.QueryRowContext(ctx, `SELECT web_root FROM websites
		WHERE id=? AND site_type='wordpress' AND status='active'`, task.SiteID).Scan(&webRoot); err != nil {
		return "", errors.New("plugin update site unavailable")
	}
	webRoot, err := safeSiteWebRoot(webRoot)
	if err != nil {
		return "", errors.New("plugin update site unavailable")
	}
	parts := strings.Split(task.ComponentKey, "/")
	pluginRoot, err := os.OpenRoot(filepath.Join(webRoot, "wp-content", "plugins", parts[0]))
	if err != nil {
		return "", errors.New("installed plugin unavailable")
	}
	defer pluginRoot.Close()
	info, err := pluginRoot.Lstat(parts[1])
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("installed plugin unavailable")
	}
	f, err := pluginRoot.Open(parts[1])
	if err != nil {
		return "", errors.New("installed plugin unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", errors.New("installed plugin changed")
	}
	headers, err := readWPComponentHeadersFromReader(io.LimitReader(f, wpComponentHeaderBytes), "Plugin Name", "Version")
	if err != nil || headers["Plugin Name"] == "" || !wpComponentVersionPattern.MatchString(headers["Version"]) {
		return "", errors.New("installed plugin version unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return headers["Version"], nil
}

func newWPCoreUpdateWorker(opts wpCoreUpdateWorkerOptions) (*WPCoreUpdateWorker, error) {
	if opts.store == nil || opts.store.db == nil || opts.backups == nil || opts.executor == nil || opts.observeVersion == nil ||
		opts.pluginTasks == nil || opts.pluginExecutor == nil || opts.observePlugin == nil || opts.pluginSupervisor == nil ||
		opts.owner == "" || opts.pollInterval <= 0 || opts.heartbeatInterval <= 0 || opts.sweepInterval <= 0 || opts.now == nil {
		return nil, errors.New("invalid core update worker options")
	}
	return &WPCoreUpdateWorker{
		store: opts.store, backups: opts.backups, executor: opts.executor, observeVersion: opts.observeVersion,
		pluginTasks: opts.pluginTasks, pluginExecutor: opts.pluginExecutor, observePlugin: opts.observePlugin,
		pluginSupervisor:  opts.pluginSupervisor,
		themeTasks:        opts.themeTasks,
		themeExecutor:     opts.themeExecutor,
		observeTheme:      opts.observeTheme,
		themeSupervisor:   opts.themeSupervisor,
		batchOrchestrator: opts.batchOrchestrator,
		owner:             opts.owner, pollInterval: opts.pollInterval, heartbeatInterval: opts.heartbeatInterval,
		sweepInterval: opts.sweepInterval, now: opts.now,
	}, nil
}

func newWPCoreUpdateWorkerOwner() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "core-worker-" + hex.EncodeToString(raw[:]), nil
}

func (w *WPCoreUpdateWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("core update worker already started")
	}
	wpCoreUpdateWorkerInstance.Lock()
	defer wpCoreUpdateWorkerInstance.Unlock()
	if wpCoreUpdateWorkerInstance.active {
		return errors.New("core update worker already active")
	}
	if _, err := w.store.recoverCoreAfterRestart(context.Background(), w.now().UTC()); err != nil {
		return err
	}
	if err := w.recoverPluginTasks(context.Background(), false, "worker_restarted"); err != nil {
		return err
	}
	if err := w.recoverThemeTasks(context.Background(), false, "worker_restarted"); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	wpCoreUpdateWorkerInstance.active = true
	go w.run(ctx, w.done)
	return nil
}

func (w *WPCoreUpdateWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	w.cancel()
	done := w.done
	w.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *WPCoreUpdateWorker) run(ctx context.Context, done chan struct{}) {
	defer func() {
		w.mu.Lock()
		w.started = false
		w.cancel = nil
		w.mu.Unlock()
		wpCoreUpdateWorkerInstance.Lock()
		wpCoreUpdateWorkerInstance.active = false
		wpCoreUpdateWorkerInstance.Unlock()
		close(done)
	}()
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		w.sweep(ctx)
	}()
	defer func() { <-sweepDone }()
	for {
		if ctx.Err() != nil {
			return
		}
		if w.processOne(ctx) {
			// task 刚完成（站点名额释放），立即推进批量派发下一项，不必等 30s sweep。
			w.tickBatchOrchestrator()
			continue
		}
		// 空闲轮询时也推进一次，保证批量首项在 pollInterval 内被派发。
		w.tickBatchOrchestrator()
		timer := time.NewTimer(w.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (w *WPCoreUpdateWorker) sweep(ctx context.Context) {
	ticker := time.NewTicker(w.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = w.store.recoverCoreExpired(context.Background(), w.now().UTC())
			_ = w.recoverPluginTasks(context.Background(), true, "lease_expired")
			_ = w.recoverThemeTasks(context.Background(), true, "lease_expired")
			if cleaner, ok := w.backups.(interface {
				cleanupExpiredArtifacts(context.Context, time.Time) error
			}); ok {
				_ = cleaner.cleanupExpiredArtifacts(context.Background(), w.now().UTC())
			}
			if logCleaner, ok := w.backups.(interface {
				cleanupExpiredUpdateLogs(context.Context, time.Time) error
			}); ok {
				_ = logCleaner.cleanupExpiredUpdateLogs(context.Background(), w.now().UTC())
			}
			w.tickBatchOrchestrator()
		}
	}
}

// tickBatchOrchestrator 推进批量派发。worker 主循环在每次 processOne 后调用（1s 节奏 +
// task 完成即触发），sweep 作为 30s 兜底；batchOrchestrator.Tick 内部有互斥锁防并发。
func (w *WPCoreUpdateWorker) tickBatchOrchestrator() {
	if w.batchOrchestrator != nil {
		w.batchOrchestrator.Tick(context.Background())
	}
}

func (w *WPCoreUpdateWorker) recoverThemeTasks(ctx context.Context, expiredOnly bool, code string) error {
	if w.themeTasks == nil || w.themeSupervisor == nil {
		return nil
	}
	now := w.now().UTC()
	tasks, err := w.store.runningThemeTasks(ctx, now, expiredOnly)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wpPluginUpdateControlTimeout)
		state, inspectErr := w.themeSupervisor.Inspect(inspectCtx, task.ID)
		cancel()
		if inspectErr != nil {
			continue
		}
		if state.ActiveState == "active" || state.ActiveState == "activating" || state.ActiveState == "reloading" {
			continue
		}
		if state.LoadState != "not-found" && state.ActiveState != "inactive" && state.ActiveState != "failed" {
			continue
		}
		if _, err := w.store.interruptOwned(ctx, task.ID, task.LeaseOwner, code, now); err != nil {
			return err
		}
	}
	return nil
}

func (w *WPCoreUpdateWorker) recoverPluginTasks(ctx context.Context, expiredOnly bool, code string) error {
	now := w.now().UTC()
	tasks, err := w.store.runningPluginTasks(ctx, now, expiredOnly)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wpPluginUpdateControlTimeout)
		state, inspectErr := w.pluginSupervisor.Inspect(inspectCtx, task.ID)
		cancel()
		if inspectErr != nil {
			continue
		}
		if state.ActiveState == "active" || state.ActiveState == "activating" || state.ActiveState == "reloading" {
			continue
		}
		if state.LoadState != "not-found" && state.ActiveState != "inactive" && state.ActiveState != "failed" {
			continue
		}
		if _, err := w.store.interruptOwned(ctx, task.ID, task.LeaseOwner, code, now); err != nil {
			return err
		}
	}
	return nil
}

func (w *WPCoreUpdateWorker) processOne(ctx context.Context) bool {
	select {
	case wpCoreUpdateWorkerSlot <- struct{}{}:
		defer func() { <-wpCoreUpdateWorkerSlot }()
	case <-ctx.Done():
		return false
	}
	tasks, err := w.store.nextQueuedUpdateCandidates(ctx)
	if err != nil {
		return false
	}
	for _, task := range tasks {
		if w.processCandidate(ctx, task) {
			return true
		}
	}
	return false
}

func (w *WPCoreUpdateWorker) processCandidate(ctx context.Context, task WPUpdateTask) bool {
	var claimed WPUpdateTask
	switch task.ComponentType {
	case "core":
		observed, err := w.observeVersion(ctx, task.SiteID)
		if err != nil {
			return false
		}
		claimed, err = w.store.claimCoreUpdate(ctx, task.ID, w.owner, observed, w.now().UTC())
		if err != nil {
			return false
		}
	case "plugin":
		observed, err := w.observePlugin(ctx, task)
		if err != nil {
			return false
		}
		claimed, err = w.pluginTasks.validateAndClaimPluginUpdate(ctx, task.ID, w.owner, observed)
		if err != nil {
			return false
		}
	case "theme":
		if w.themeTasks == nil || w.observeTheme == nil || w.themeExecutor == nil || w.themeSupervisor == nil {
			return false
		}
		observed, err := w.observeTheme(ctx, task)
		if err != nil {
			return false
		}
		claimed, err = w.themeTasks.validateAndClaimThemeUpdate(ctx, task.ID, w.owner, observed.Version, observed.Template)
		if err != nil {
			return false
		}
	default:
		return false
	}
	w.runOwned(ctx, claimed)
	return true
}

func (w *WPCoreUpdateWorker) runOwned(parent context.Context, task WPUpdateTask) {
	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- errors.New("core update worker execution panic")
			}
		}()
		var err error
		switch task.ComponentType {
		case "core":
			err = w.backups.prepareCoreBackups(runCtx, task.ID, w.owner)
		case "plugin":
			err = w.pluginTasks.preparePluginBackups(runCtx, task.ID, w.owner)
		case "theme":
			err = w.themeTasks.prepareThemeBackups(runCtx, task.ID, w.owner)
		default:
			err = errors.New("unsupported update component")
		}
		if err != nil {
			if runCtx.Err() == nil {
				_ = w.store.markFailure(context.Background(), task.ID, w.owner, "backup", false, w.now().UTC())
			}
			done <- err
			return
		}
		if task.ComponentType == "plugin" {
			done <- w.pluginExecutor.Execute(runCtx, task.ID, w.owner)
		} else if task.ComponentType == "theme" {
			done <- w.themeExecutor.Execute(runCtx, task.ID, w.owner)
		} else {
			done <- w.executor.Execute(runCtx, task.ID, w.owner)
		}
	}()
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			cancel()
			<-done
			w.interruptIfOwned(task.ID, "worker_stopped")
			return
		case <-ticker.C:
			owned, err := w.store.heartbeat(context.Background(), task.ID, w.owner, w.now().UTC())
			if err != nil || !owned {
				cancel()
				<-done
				if err != nil {
					w.resolveUncertainHeartbeat(parent, task.ID)
				}
				return
			}
		case <-done:
			w.interruptIfOwned(task.ID, "execution_result_unknown")
			return
		}
	}
}

func (w *WPCoreUpdateWorker) resolveUncertainHeartbeat(ctx context.Context, taskID string) {
	deadline := time.Now().Add(wpUpdateLease)
	retry := w.heartbeatInterval
	if retry > time.Second {
		retry = time.Second
	}
	for time.Now().Before(deadline) {
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		owned, err := w.store.heartbeat(context.Background(), taskID, w.owner, w.now().UTC())
		if err != nil {
			continue
		}
		if owned {
			w.interruptIfOwned(taskID, "heartbeat_uncertain")
		}
		return
	}
}

func (w *WPCoreUpdateWorker) interruptIfOwned(taskID, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = w.store.interruptOwned(ctx, taskID, w.owner, code, w.now().UTC())
}
