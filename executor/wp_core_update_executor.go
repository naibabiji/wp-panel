package executor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type wpCoreUpdateExecution struct {
	Task           WPUpdateTask
	WebRoot        string
	SystemUser     string
	Domain         string
	DatabaseName   string
	PackagePath    string
	DatabaseBackup string
	CoreBackup     string
	FileLockMode   string
	FileLockActive bool
}

type wpCoreUpdateOperations interface {
	Prepare(context.Context, wpCoreUpdateExecution) error
	Unlock(context.Context, wpCoreUpdateExecution) error
	ApplyCoreUpdate(context.Context, wpCoreUpdateExecution) error
	CheckTargetHealth(context.Context, wpCoreUpdateExecution) error
	SetMaintenance(context.Context, wpCoreUpdateExecution, bool) error
	RestoreDatabase(context.Context, wpCoreUpdateExecution) error
	RestoreCoreFiles(context.Context, wpCoreUpdateExecution) error
	CheckRollbackHealth(context.Context, wpCoreUpdateExecution) error
	RestoreFileLock(context.Context, wpCoreUpdateExecution) error
}

type wpCoreUpdateExecutor struct {
	store *wpUpdateStore
	root  string
	ops   wpCoreUpdateOperations
	now   func() time.Time
}

func newWPCoreUpdateExecutor(store *wpUpdateStore, artifactRoot string, ops wpCoreUpdateOperations) (*wpCoreUpdateExecutor, error) {
	if store == nil || store.db == nil || !filepath.IsAbs(artifactRoot) || ops == nil {
		return nil, errors.New("invalid core update executor")
	}
	return &wpCoreUpdateExecutor{store: store, root: filepath.Clean(artifactRoot), ops: ops, now: time.Now}, nil
}

func (e *wpCoreUpdateExecutor) Execute(ctx context.Context, taskID, owner string) error {
	execution, err := e.loadExecution(ctx, taskID, owner)
	if err != nil {
		return err
	}
	if err := e.ops.Prepare(ctx, execution); err != nil {
		if finishErr := e.store.markFailure(ctx, taskID, owner, "precheck", false, e.now().UTC()); finishErr != nil {
			return finishErr
		}
		return errors.New("core update operations precheck failed")
	}
	if err := e.stage(ctx, taskID, owner, "backups_ready", "unlocking"); err != nil {
		return err
	}
	if err := e.ops.Unlock(ctx, execution); err != nil {
		return e.failBeforeCoreWrite(ctx, execution, owner, "unlock")
	}
	if err := e.stage(ctx, taskID, owner, "unlocking", "updating_core"); err != nil {
		e.bestEffortRestoreFileLock(ctx, execution)
		return err
	}
	if err := e.ops.ApplyCoreUpdate(ctx, execution); err != nil {
		return e.rollback(ctx, execution, owner, "core_update")
	}
	if err := e.stage(ctx, taskID, owner, "updating_core", "health_check"); err != nil {
		e.bestEffortRestoreFileLock(ctx, execution)
		return err
	}
	if err := e.ops.CheckTargetHealth(ctx, execution); err != nil {
		return e.rollback(ctx, execution, owner, "health_check")
	}
	if err := e.stage(ctx, taskID, owner, "health_check", "restoring_file_lock"); err != nil {
		e.bestEffortRestoreFileLock(ctx, execution)
		return err
	}
	if err := e.ops.RestoreFileLock(ctx, execution); err != nil {
		return e.rollback(ctx, execution, owner, "file_lock_restore")
	}
	if err := e.store.markSuccess(ctx, taskID, owner, e.now().UTC()); err != nil {
		return err
	}
	return nil
}

func (e *wpCoreUpdateExecutor) failBeforeCoreWrite(ctx context.Context, execution wpCoreUpdateExecution, owner, failureStage string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wpUpdateLease)
	defer cancel()
	restoreErr := e.ops.RestoreFileLock(cleanupCtx, execution)
	if err := e.store.markFailure(ctx, execution.Task.ID, owner, failureStage, restoreErr != nil, e.now().UTC()); err != nil {
		return err
	}
	if restoreErr != nil {
		return errors.New("core update pre-write failure and file lock restoration failed")
	}
	return fmt.Errorf("core update failed at %s before core write", failureStage)
}

func (e *wpCoreUpdateExecutor) rollback(ctx context.Context, execution wpCoreUpdateExecution, owner, failureStage string) error {
	if err := e.store.beginAutomaticRollback(ctx, execution.Task.ID, owner, failureStage, e.now().UTC()); err != nil {
		return err
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wpUpdateLease)
	defer cancel()
	rollbackErr := e.ops.SetMaintenance(rollbackCtx, execution, true)
	if err := e.ops.RestoreDatabase(rollbackCtx, execution); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	if err := e.ops.RestoreCoreFiles(rollbackCtx, execution); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	if rollbackErr == nil {
		if err := e.ops.SetMaintenance(rollbackCtx, execution, false); err != nil {
			rollbackErr = err
		} else if err := e.ops.CheckRollbackHealth(rollbackCtx, execution); err != nil {
			rollbackErr = err
		}
	}
	if err := e.ops.RestoreFileLock(rollbackCtx, execution); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	succeeded := rollbackErr == nil
	if err := e.store.finishAutomaticRollback(ctx, execution.Task.ID, owner, succeeded, e.now().UTC()); err != nil {
		return err
	}
	if rollbackErr != nil {
		return errors.New("core update failed and automatic rollback failed")
	}
	return fmt.Errorf("core update failed at %s and was rolled back", failureStage)
}

func (e *wpCoreUpdateExecutor) bestEffortRestoreFileLock(ctx context.Context, execution wpCoreUpdateExecution) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	_ = e.ops.RestoreFileLock(cleanupCtx, execution)
}

func (e *wpCoreUpdateExecutor) stage(ctx context.Context, taskID, owner, expectedStage, nextStage string) error {
	return e.store.advanceOwnedStage(ctx, taskID, owner, expectedStage, nextStage, e.now().UTC())
}

func (e *wpCoreUpdateExecutor) loadExecution(ctx context.Context, taskID, owner string) (wpCoreUpdateExecution, error) {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || owner == "" {
		return wpCoreUpdateExecution{}, errors.New("invalid core update execution")
	}
	task, err := e.store.getTask(ctx, taskID)
	if err != nil || task.Status != wpUpdateRunning || task.TaskKind != "update" || task.ComponentType != "core" || task.LeaseOwner != owner || !task.BackupReady {
		return wpCoreUpdateExecution{}, errors.New("core update task is not executable")
	}
	taskDir := filepath.Join(e.root, taskID)
	packagePath := filepath.Join(taskDir, "package.zip")
	if task.PackageSnapshotPath != packagePath {
		return wpCoreUpdateExecution{}, errors.New("core update package path is not controlled")
	}
	sha, _, err := hashRegularFile(packagePath)
	if err != nil || sha != task.DownloadedSHA256 {
		return wpCoreUpdateExecution{}, errors.New("core update package digest mismatch")
	}
	var execution wpCoreUpdateExecution
	execution.Task, execution.PackagePath = task, packagePath
	var lockEnabled int
	err = e.store.db.QueryRowContext(ctx, `SELECT domain,system_user,web_root,db_name,file_lock_mode,file_lock_enabled
		FROM websites WHERE id=? AND site_type='wordpress' AND status='active'`, task.SiteID).
		Scan(&execution.Domain, &execution.SystemUser, &execution.WebRoot, &execution.DatabaseName, &execution.FileLockMode, &lockEnabled)
	if err != nil || !filepath.IsAbs(execution.WebRoot) {
		return wpCoreUpdateExecution{}, errors.New("core update site is not executable")
	}
	execution.FileLockActive = lockEnabled != 0
	rows, err := e.store.db.QueryContext(ctx, `SELECT kind,file_path,sha256 FROM wp_update_task_backups
		WHERE task_id=? AND protected=1 AND deleted_at IS NULL AND kind IN ('database','core_files')`, taskID)
	if err != nil {
		return wpCoreUpdateExecution{}, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var kind, name, digest string
		if err := rows.Scan(&kind, &name, &digest); err != nil {
			return wpCoreUpdateExecution{}, err
		}
		expected := filepath.Join(taskDir, map[string]string{"database": "database.sql.gz", "core_files": "core-files.tar.gz"}[kind])
		if name != expected || seen[kind] {
			return wpCoreUpdateExecution{}, errors.New("core update backup path is not controlled")
		}
		actual, _, err := hashRegularFile(name)
		if err != nil || actual != digest {
			return wpCoreUpdateExecution{}, errors.New("core update backup digest mismatch")
		}
		if kind == "database" {
			execution.DatabaseBackup = name
		} else {
			execution.CoreBackup = name
		}
		seen[kind] = true
	}
	if err := rows.Err(); err != nil {
		return wpCoreUpdateExecution{}, err
	}
	if !seen["database"] || !seen["core_files"] {
		return wpCoreUpdateExecution{}, errors.New("core update backups are incomplete")
	}
	return execution, nil
}
