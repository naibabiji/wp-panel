package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type wpThemeUpdateExecutor struct {
	inner *wpPluginUpdateExecutor
}

func newWPThemeUpdateExecutor(store *wpUpdateStore, artifactRoot string, ops wpPluginUpdateOperations) (*wpThemeUpdateExecutor, error) {
	inner, err := newWPPluginUpdateExecutor(store, artifactRoot, ops)
	if err != nil {
		return nil, errors.New("invalid theme update executor")
	}
	return &wpThemeUpdateExecutor{inner: inner}, nil
}

func (e *wpThemeUpdateExecutor) Execute(ctx context.Context, taskID, owner string) error {
	if e == nil || e.inner == nil {
		return errors.New("theme update executor unavailable")
	}
	execution, err := e.loadExecution(ctx, taskID, owner)
	if err != nil {
		return err
	}
	return e.inner.execute(ctx, execution, owner)
}

func (e *wpThemeUpdateExecutor) loadExecution(ctx context.Context, taskID, owner string) (wpPluginUpdateExecution, error) {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || owner == "" {
		return wpPluginUpdateExecution{}, errors.New("invalid theme update execution")
	}
	task, err := e.inner.store.getTask(ctx, taskID)
	if err != nil || task.Status != wpUpdateRunning || task.TaskKind != "update" || task.ComponentType != "theme" ||
		task.LeaseOwner != owner || !task.BackupReady || !validWPThemeComponentKey(task.ComponentKey) {
		return wpPluginUpdateExecution{}, errors.New("theme update task is not executable")
	}
	taskDir := filepath.Join(e.inner.root, taskID)
	packagePath := filepath.Join(taskDir, "package.zip")
	if task.PackageSnapshotPath != packagePath {
		return wpPluginUpdateExecution{}, errors.New("theme update package path is not controlled")
	}
	sha, _, err := hashRegularFile(packagePath)
	if err != nil || sha != task.DownloadedSHA256 {
		return wpPluginUpdateExecution{}, errors.New("theme update package digest mismatch")
	}
	var execution wpPluginUpdateExecution
	execution.Task, execution.PackagePath = task, packagePath
	var lockEnabled int
	err = e.inner.store.db.QueryRowContext(ctx, `SELECT domain,system_user,web_root,db_name,file_lock_mode,file_lock_enabled
		FROM websites WHERE id=? AND site_type='wordpress' AND status='active'`, task.SiteID).
		Scan(&execution.Domain, &execution.SystemUser, &execution.WebRoot, &execution.DatabaseName, &execution.FileLockMode, &lockEnabled)
	if err != nil || !filepath.IsAbs(execution.WebRoot) {
		return wpPluginUpdateExecution{}, errors.New("theme update site is not executable")
	}
	_, template, err := readInstalledWPThemeIdentity(execution.WebRoot, task.ComponentKey)
	if err != nil {
		return wpPluginUpdateExecution{}, errors.New("installed theme identity unavailable")
	}
	if _, err := ValidateWPComponentPackage(ctx, packagePath, WPComponentPackageExpectation{
		ComponentType: "theme", ComponentKey: task.ComponentKey, OfficialSlug: task.ComponentKey,
		TargetVersion: task.TargetVersion, Template: template,
	}); err != nil {
		return wpPluginUpdateExecution{}, errors.New("theme update package validation failed")
	}
	validatedSHA, _, err := hashRegularFile(packagePath)
	if err != nil || validatedSHA != sha {
		return wpPluginUpdateExecution{}, errors.New("theme update package changed during validation")
	}
	execution.FileLockActive = lockEnabled != 0
	rows, err := e.inner.store.db.QueryContext(ctx, `SELECT kind,file_path,sha256 FROM wp_update_task_backups
		WHERE task_id=? AND protected=1 AND deleted_at IS NULL AND kind IN ('database','theme_files')`, taskID)
	if err != nil {
		return wpPluginUpdateExecution{}, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var kind, name, digest string
		if err := rows.Scan(&kind, &name, &digest); err != nil {
			return wpPluginUpdateExecution{}, err
		}
		expected := filepath.Join(taskDir, map[string]string{"database": "database.sql.gz", "theme_files": "theme-files.tar.gz"}[kind])
		if name != expected || seen[kind] {
			return wpPluginUpdateExecution{}, errors.New("theme update backup path is not controlled")
		}
		actual, _, err := hashRegularFile(name)
		if err != nil || actual != digest {
			return wpPluginUpdateExecution{}, errors.New("theme update backup digest mismatch")
		}
		if kind == "database" {
			execution.DatabaseBackup = name
		} else {
			execution.PluginBackup = name
		}
		seen[kind] = true
	}
	if err := rows.Err(); err != nil {
		return wpPluginUpdateExecution{}, err
	}
	if !seen["database"] || !seen["theme_files"] {
		return wpPluginUpdateExecution{}, errors.New("theme update backups are incomplete")
	}
	return execution, nil
}

func readInstalledWPThemeIdentity(webRoot, componentKey string) (string, string, error) {
	if !validWPThemeComponentKey(componentKey) {
		return "", "", errors.New("invalid theme component key")
	}
	root, err := os.OpenRoot(filepath.Join(webRoot, "wp-content", "themes", componentKey))
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	info, err := root.Lstat("style.css")
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("invalid theme stylesheet")
	}
	f, err := root.Open("style.css")
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", "", errors.New("theme stylesheet changed")
	}
	headers, err := readWPComponentHeadersFromReader(io.LimitReader(f, wpComponentHeaderBytes), "Theme Name", "Version", "Template")
	if err != nil || headers["Theme Name"] == "" || !wpComponentVersionPattern.MatchString(headers["Version"]) ||
		(headers["Template"] != "" && !validWPThemeComponentKey(headers["Template"])) {
		return "", "", errors.New("invalid theme headers")
	}
	return headers["Version"], headers["Template"], nil
}
