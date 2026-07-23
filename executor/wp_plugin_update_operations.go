package executor

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

type wpPluginRunnerSessionAPI interface {
	Observe(context.Context) (bool, error)
	Update(context.Context) error
	Reactivate(context.Context) error
	Check(context.Context, string, bool) error
	Journal() (wpPluginUpdateJournalReport, error)
	Close() error
}

type wpPluginRunnerPreparer func(context.Context, wpPluginUpdateExecution) (wpPluginRunnerSessionAPI, error)
type wpPluginFilesRestorer func(context.Context, wpPluginUpdateExecution) error
type wpPluginLinter func(context.Context, wpPluginUpdateExecution) error
type wpPluginSpaceChecker func(context.Context, wpPluginUpdateExecution, ZIPInspection) error

type wpPluginSystemOperations struct {
	store         *wpUpdateStore
	componentType string
	prepare       wpPluginRunnerPreparer
	restoreDB     wpCoreDatabaseRestorer
	restoreFiles  wpPluginFilesRestorer
	probe         wpCoreHomeProber
	lint          wpPluginLinter
	space         wpPluginSpaceChecker
	supervisor    wpPluginUpdateSupervisor
	mu            sync.Mutex
	sessions      map[string]*wpPluginPreparedSession
}

type wpPluginPreparedSession struct {
	runner    wpPluginRunnerSessionAPI
	wasActive bool
}

func newWPPluginSystemOperations(store *wpUpdateStore, prepare wpPluginRunnerPreparer, restoreDB wpCoreDatabaseRestorer, restoreFiles wpPluginFilesRestorer, probe wpCoreHomeProber, lint wpPluginLinter, space wpPluginSpaceChecker) (*wpPluginSystemOperations, error) {
	if store == nil || store.db == nil || prepare == nil || restoreDB == nil || restoreFiles == nil || probe == nil || lint == nil || space == nil {
		return nil, errors.New("invalid plugin update operations")
	}
	return &wpPluginSystemOperations{store: store, componentType: "plugin", prepare: prepare, restoreDB: restoreDB, restoreFiles: restoreFiles, probe: probe, lint: lint, space: space, sessions: map[string]*wpPluginPreparedSession{}}, nil
}

func newDefaultWPPluginSystemOperations(store *wpUpdateStore, wwwRoot string) (*wpPluginSystemOperations, error) {
	runner, err := newDefaultWPPluginPHPRunner(wwwRoot)
	if err != nil {
		return nil, err
	}
	operations, err := newWPPluginSystemOperations(store,
		func(ctx context.Context, execution wpPluginUpdateExecution) (wpPluginRunnerSessionAPI, error) {
			return runner.Prepare(ctx, execution)
		},
		defaultWPCoreDatabaseRestorer, defaultWPPluginFilesRestorer, defaultWPCoreHomeProber(nil), defaultWPPluginLinter, defaultWPPluginSpaceChecker)
	if err != nil {
		return nil, err
	}
	supervisor, ok := runner.opts.scope.(*wpPluginUpdateScope)
	if !ok {
		return nil, errors.New("plugin update supervisor unavailable")
	}
	operations.supervisor = supervisor
	return operations, nil
}

func newDefaultWPThemeSystemOperations(store *wpUpdateStore, wwwRoot string) (*wpPluginSystemOperations, error) {
	runner, err := newDefaultWPThemePHPRunner(wwwRoot)
	if err != nil {
		return nil, err
	}
	operations, err := newWPPluginSystemOperations(store,
		func(ctx context.Context, execution wpPluginUpdateExecution) (wpPluginRunnerSessionAPI, error) {
			return runner.Prepare(ctx, execution)
		},
		defaultWPCoreDatabaseRestorer, defaultWPThemeFilesRestorer, defaultWPCoreHomeProber(nil), defaultWPPluginLinter, defaultWPPluginSpaceChecker)
	if err != nil {
		return nil, err
	}
	operations.componentType = "theme"
	supervisor, ok := runner.opts.scope.(*wpPluginUpdateScope)
	if !ok {
		return nil, errors.New("theme update supervisor unavailable")
	}
	operations.supervisor = supervisor
	return operations, nil
}

func (o *wpPluginSystemOperations) Prepare(ctx context.Context, execution wpPluginUpdateExecution) (bool, error) {
	if execution.Task.ComponentType != o.componentType {
		return false, errors.New("component update operations mismatch")
	}
	slug, template := execution.Task.ComponentKey, ""
	var err error
	if o.componentType == "plugin" {
		slug = strings.Split(execution.Task.ComponentKey, "/")[0]
	} else {
		_, template, err = readInstalledWPThemeIdentity(execution.WebRoot, execution.Task.ComponentKey)
		if err != nil {
			return false, errors.New("theme identity unavailable")
		}
	}
	report, err := ValidateWPComponentPackage(ctx, execution.PackagePath, WPComponentPackageExpectation{ComponentType: o.componentType, ComponentKey: execution.Task.ComponentKey, OfficialSlug: slug, TargetVersion: execution.Task.TargetVersion, Template: template})
	if err != nil {
		return false, errors.New("plugin package identity mismatch")
	}
	if err := o.space(ctx, execution, report.Inspection); err != nil {
		return false, err
	}
	o.mu.Lock()
	if _, exists := o.sessions[execution.Task.ID]; exists {
		o.mu.Unlock()
		return false, errors.New("plugin runner session already exists")
	}
	o.sessions[execution.Task.ID] = nil
	o.mu.Unlock()
	session, err := o.prepare(ctx, execution)
	if err != nil {
		o.deleteReservation(execution.Task.ID)
		return false, err
	}
	wasActive, err := session.Observe(ctx)
	if err != nil {
		if errors.Is(err, errWPPluginScopeSupervisionUncertain) {
			o.setSession(execution.Task.ID, &wpPluginPreparedSession{runner: session})
		} else {
			_ = session.Close()
			o.deleteReservation(execution.Task.ID)
		}
		return false, err
	}
	o.setSession(execution.Task.ID, &wpPluginPreparedSession{runner: session, wasActive: wasActive})
	if o.componentType == "theme" {
		return false, nil
	}
	return wasActive, nil
}

func (o *wpPluginSystemOperations) Unlock(_ context.Context, execution wpPluginUpdateExecution) error {
	if wpConfigHasUserFileModsLock(execution.WebRoot) {
		return errors.New("user file modifications lock is enabled")
	}
	if !execution.FileLockActive {
		return nil
	}
	return ApplySiteUnlockedPermissions(pluginExecutionWebsite(execution))
}

func (o *wpPluginSystemOperations) ApplyPluginUpdate(ctx context.Context, execution wpPluginUpdateExecution) error {
	session, err := o.session(execution.Task.ID)
	if err != nil {
		return err
	}
	return session.runner.Update(ctx)
}

func (o *wpPluginSystemOperations) ReactivatePlugin(ctx context.Context, execution wpPluginUpdateExecution) error {
	if o.componentType != "plugin" {
		return errors.New("theme reactivation is not allowed")
	}
	session, err := o.session(execution.Task.ID)
	if err != nil || !session.wasActive {
		return errors.New("plugin reactivation is not allowed")
	}
	return session.runner.Reactivate(ctx)
}

func (o *wpPluginSystemOperations) CheckTargetHealth(ctx context.Context, execution wpPluginUpdateExecution, active bool) error {
	session, err := o.session(execution.Task.ID)
	if err != nil || o.componentType == "plugin" && session.wasActive != active {
		return errors.New("plugin health plan mismatch")
	}
	expectedActive := active
	if o.componentType == "theme" {
		expectedActive = session.wasActive
	}
	if err := session.runner.Check(ctx, execution.Task.TargetVersion, expectedActive); err != nil {
		return err
	}
	if !expectedActive {
		if err := o.lint(ctx, execution); err != nil {
			return err
		}
	}
	return o.probe(ctx, execution.Domain)
}

func (o *wpPluginSystemOperations) SetMaintenance(_ context.Context, execution wpPluginUpdateExecution, enabled bool) error {
	return setWPUpdateMaintenance(execution.WebRoot, enabled)
}

func (o *wpPluginSystemOperations) RestoreDatabase(ctx context.Context, execution wpPluginUpdateExecution) error {
	return o.restoreDB(ctx, execution.DatabaseName, execution.DatabaseBackup)
}

func (o *wpPluginSystemOperations) RestorePluginFiles(ctx context.Context, execution wpPluginUpdateExecution) error {
	return o.restoreFiles(ctx, execution)
}

func (o *wpPluginSystemOperations) CheckRollbackHealth(ctx context.Context, execution wpPluginUpdateExecution) error {
	session, err := o.session(execution.Task.ID)
	if err != nil {
		return err
	}
	if err := session.runner.Check(ctx, execution.Task.CurrentVersion, session.wasActive); err != nil {
		return err
	}
	if !session.wasActive {
		if err := o.lint(ctx, execution); err != nil {
			return err
		}
	}
	return o.probe(ctx, execution.Domain)
}

func (o *wpPluginSystemOperations) RestoreFileLock(_ context.Context, execution wpPluginUpdateExecution) error {
	if !execution.FileLockActive {
		return nil
	}
	return ApplySiteFileLockMode(pluginExecutionWebsite(execution), execution.FileLockMode)
}

func (o *wpPluginSystemOperations) Finalize(ctx context.Context, execution wpPluginUpdateExecution, preserve bool) error {
	session, err := o.session(execution.Task.ID)
	if err != nil {
		return err
	}
	report, err := session.runner.Journal()
	if err != nil {
		return err
	}
	if err := o.store.recordPluginRunnerJournal(ctx, execution.Task.ID, execution.Task.LeaseOwner, report, time.Now().UTC()); err != nil {
		return err
	}
	if preserve {
		return nil
	}
	if err := session.runner.Close(); err != nil {
		return err
	}
	o.deleteReservation(execution.Task.ID)
	return nil
}

func (o *wpPluginSystemOperations) session(taskID string) (*wpPluginPreparedSession, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	session := o.sessions[taskID]
	if session == nil || session.runner == nil {
		return nil, errors.New("plugin runner session is unavailable")
	}
	return session, nil
}

func (o *wpPluginSystemOperations) setSession(taskID string, session *wpPluginPreparedSession) {
	o.mu.Lock()
	o.sessions[taskID] = session
	o.mu.Unlock()
}
func (o *wpPluginSystemOperations) deleteReservation(taskID string) {
	o.mu.Lock()
	delete(o.sessions, taskID)
	o.mu.Unlock()
}

func pluginExecutionWebsite(execution wpPluginUpdateExecution) *models.Website {
	return &models.Website{ID: execution.Task.SiteID, Domain: execution.Domain, SystemUser: execution.SystemUser, WebRoot: execution.WebRoot, DBName: execution.DatabaseName, SiteType: "wordpress", Status: models.StatusActive, FileLockEnabled: execution.FileLockActive, FileLockMode: execution.FileLockMode}
}

func defaultWPPluginLinter(ctx context.Context, execution wpPluginUpdateExecution) error {
	componentDir, slug := "plugins", ""
	switch execution.Task.ComponentType {
	case "plugin":
		parts := strings.Split(execution.Task.ComponentKey, "/")
		if len(parts) != 2 {
			return errors.New("invalid plugin lint target")
		}
		slug = parts[0]
	case "theme":
		if !validWPThemeComponentKey(execution.Task.ComponentKey) {
			return errors.New("invalid theme lint target")
		}
		componentDir, slug = "themes", execution.Task.ComponentKey
	default:
		return errors.New("invalid component lint target")
	}
	u, err := user.Lookup(execution.SystemUser)
	if err != nil {
		return errors.New("plugin lint user unavailable")
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || u.Username != execution.SystemUser {
		return errors.New("invalid plugin lint identity")
	}
	php, err := validateInventoryBinary(wpInventoryPHPPath, "/usr/bin", 0, 0)
	if err != nil {
		return err
	}
	runuser, err := validateInventoryBinary(wpInventoryRunuserPath, "/usr/sbin", 0, 0)
	if err != nil {
		return err
	}
	rootPath := filepath.Join(execution.WebRoot, "wp-content", componentDir, slug)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if len(filepath.ToSlash(name)) > wpComponentMaxPathBytes {
			return errors.New("plugin lint path budget exceeded")
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("invalid plugin lint entry")
		}
		if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".php") {
			return nil
		}
		count++
		if count > wpComponentMaxPHPFiles || info.Size() > wpComponentMaxPHPBytes {
			return errors.New("plugin lint budget exceeded")
		}
		return lintWPPluginFile(ctx, root, name, info, runuser, php, u)
	})
	return err
}

func lintWPPluginFile(ctx context.Context, root *os.Root, name string, expected os.FileInfo, runuser, php string, u *user.User) error {
	src, err := root.Open(name)
	if err != nil {
		return errors.New("plugin lint file unavailable")
	}
	opened, statErr := src.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		_ = src.Close()
		return errors.New("plugin lint file changed")
	}
	tmp, err := os.CreateTemp("", ".wp-panel-plugin-lint-*.php")
	if err != nil {
		src.Close()
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, copyErr := io.Copy(tmp, io.LimitReader(src, wpComponentMaxPHPBytes+1))
	closeSrcErr := src.Close()
	chmodErr := tmp.Chmod(0444)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil || closeSrcErr != nil || chmodErr != nil || syncErr != nil || closeErr != nil || written != expected.Size() {
		return errors.New("plugin lint copy failed")
	}
	cmd := exec.CommandContext(ctx, runuser, "-u", u.Username, "--", php, "-d", "display_errors=0", "-d", "allow_url_include=0", "-d", "open_basedir="+filepath.Dir(tmpName), "-l", tmpName)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username}
	out := newCountingSink(64<<10, false)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return errors.New("plugin PHP syntax check failed")
	}
	_, exceeded, _ := out.snapshot()
	if exceeded {
		return errors.New("plugin PHP syntax output exceeded")
	}
	return nil
}

func defaultWPPluginSpaceChecker(ctx context.Context, execution wpPluginUpdateExecution, inspection ZIPInspection) error {
	if inspection.DeclaredUncompressed == 0 {
		return errors.New("invalid plugin update space inputs")
	}
	db, err := wpCoreDatabaseBytes(ctx, execution.DatabaseName)
	if err != nil {
		return errors.New("plugin database size unavailable")
	}
	componentDir, slug := "plugins", strings.Split(execution.Task.ComponentKey, "/")[0]
	if execution.Task.ComponentType == "theme" {
		componentDir, slug = "themes", execution.Task.ComponentKey
	}
	target, err := directoryRegularBytes(filepath.Join(execution.WebRoot, "wp-content", componentDir, slug))
	if err != nil {
		return errors.New("plugin target size unavailable")
	}
	working, err := wpCoreWorkingBytes(target, db, inspection.DeclaredUncompressed)
	if err != nil {
		return err
	}
	checked := map[uint64]bool{}
	for _, path := range []string{execution.WebRoot, filepath.Dir(execution.PackagePath), "/var/lib/mysql", "/var"} {
		info, err := os.Stat(path)
		if err != nil {
			return errors.New("plugin filesystem unavailable")
		}
		fileStat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("plugin filesystem unavailable")
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(path, &stat); err != nil {
			return errors.New("plugin filesystem unavailable")
		}
		device := uint64(fileStat.Dev)
		if checked[device] {
			continue
		}
		checked[device] = true
		if !wpCoreHasAvailableSpace(working, stat.Blocks*uint64(stat.Bsize), stat.Bavail*uint64(stat.Bsize)) {
			return errors.New("plugin update disk space insufficient")
		}
	}
	return nil
}

func directoryRegularBytes(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid plugin directory")
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("invalid plugin directory entry")
		}
		if info.Mode().IsRegular() {
			size := uint64(info.Size())
			if total > ^uint64(0)-size {
				return errors.New("plugin size overflow")
			}
			total += size
		}
		return nil
	})
	return total, err
}
