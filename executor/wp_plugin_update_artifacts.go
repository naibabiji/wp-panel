package executor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (s *wpUpdateArtifactService) snapshotValidateAndSealPluginPackage(ctx context.Context, taskID, sourcePath, expectedSHA string) (WPUpdateTask, WPComponentPackageReport, error) {
	task, err := s.store.getTask(ctx, taskID)
	if err != nil || task.Status != wpUpdatePreparing || task.TaskKind != "update" || task.ComponentType != "plugin" ||
		!validWPPluginComponentKey(task.ComponentKey) || !wpComponentVersionPattern.MatchString(task.TargetVersion) {
		return WPUpdateTask{}, WPComponentPackageReport{}, errors.New("plugin update plan is not snapshot-ready")
	}
	slug := strings.Split(task.ComponentKey, "/")[0]
	var report WPComponentPackageReport
	sealed, err := s.snapshotAndSealPackageChecked(ctx, taskID, sourcePath, expectedSHA, "structure_only", func(snapshot string) error {
		var validateErr error
		report, validateErr = ValidateWPComponentPackage(ctx, snapshot, WPComponentPackageExpectation{
			ComponentType: "plugin", ComponentKey: task.ComponentKey, OfficialSlug: slug, TargetVersion: task.TargetVersion,
		})
		return validateErr
	})
	if err != nil {
		return WPUpdateTask{}, WPComponentPackageReport{}, err
	}
	return sealed, report, nil
}

func (s *wpUpdateArtifactService) snapshotValidateAndSealThemePackage(ctx context.Context, taskID, sourcePath, expectedSHA, expectedTemplate string) (WPUpdateTask, WPComponentPackageReport, error) {
	task, err := s.store.getTask(ctx, taskID)
	if err != nil || task.Status != wpUpdatePreparing || task.TaskKind != "update" || task.ComponentType != "theme" ||
		!validWPThemeComponentKey(task.ComponentKey) || !wpComponentVersionPattern.MatchString(task.TargetVersion) ||
		(expectedTemplate != "" && !validWPThemeComponentKey(expectedTemplate)) {
		return WPUpdateTask{}, WPComponentPackageReport{}, errors.New("theme update plan is not snapshot-ready")
	}
	var report WPComponentPackageReport
	sealed, err := s.snapshotAndSealPackageChecked(ctx, taskID, sourcePath, expectedSHA, "structure_only", func(snapshot string) error {
		var validateErr error
		report, validateErr = ValidateWPComponentPackage(ctx, snapshot, WPComponentPackageExpectation{
			ComponentType: "theme", ComponentKey: task.ComponentKey, OfficialSlug: task.ComponentKey,
			TargetVersion: task.TargetVersion, Template: expectedTemplate,
		})
		return validateErr
	})
	if err != nil {
		return WPUpdateTask{}, WPComponentPackageReport{}, err
	}
	return sealed, report, nil
}

func (s *wpUpdateArtifactService) validateAndClaimPluginUpdate(ctx context.Context, taskID, owner, observedVersion string) (WPUpdateTask, error) {
	task, err := s.store.getTask(ctx, taskID)
	if err != nil || task.Status != wpUpdateQueued || task.ComponentType != "plugin" || task.TaskKind != "update" ||
		!validWPPluginComponentKey(task.ComponentKey) || !filepath.IsAbs(task.PackageSnapshotPath) {
		return WPUpdateTask{}, errors.New("plugin update task is not claimable")
	}
	sha, _, err := hashRegularFile(task.PackageSnapshotPath)
	if err != nil || sha != task.DownloadedSHA256 {
		return s.store.failPluginClaim(ctx, taskID, "package_digest_mismatch", s.now().UTC())
	}
	slug := strings.Split(task.ComponentKey, "/")[0]
	if _, err := ValidateWPComponentPackage(ctx, task.PackageSnapshotPath, WPComponentPackageExpectation{
		ComponentType: "plugin", ComponentKey: task.ComponentKey, OfficialSlug: slug, TargetVersion: task.TargetVersion,
	}); err != nil {
		return s.store.failPluginClaim(ctx, taskID, "package_validation_failed", s.now().UTC())
	}
	validatedSHA, _, err := hashRegularFile(task.PackageSnapshotPath)
	if err != nil || validatedSHA != sha {
		return s.store.failPluginClaim(ctx, taskID, "package_digest_mismatch", s.now().UTC())
	}
	return s.store.claimPluginUpdate(ctx, taskID, owner, observedVersion, validatedSHA, s.now().UTC())
}

func (s *wpUpdateArtifactService) validateAndClaimThemeUpdate(ctx context.Context, taskID, owner, observedVersion, observedTemplate string) (WPUpdateTask, error) {
	task, err := s.store.getTask(ctx, taskID)
	if err != nil || task.Status != wpUpdateQueued || task.ComponentType != "theme" || task.TaskKind != "update" ||
		!validWPThemeComponentKey(task.ComponentKey) || !filepath.IsAbs(task.PackageSnapshotPath) ||
		(observedTemplate != "" && !validWPThemeComponentKey(observedTemplate)) {
		return WPUpdateTask{}, errors.New("theme update task is not claimable")
	}
	sha, _, err := hashRegularFile(task.PackageSnapshotPath)
	if err != nil || sha != task.DownloadedSHA256 {
		return s.store.failThemeClaim(ctx, taskID, "package_digest_mismatch", s.now().UTC())
	}
	if _, err := ValidateWPComponentPackage(ctx, task.PackageSnapshotPath, WPComponentPackageExpectation{
		ComponentType: "theme", ComponentKey: task.ComponentKey, OfficialSlug: task.ComponentKey,
		TargetVersion: task.TargetVersion, Template: observedTemplate,
	}); err != nil {
		return s.store.failThemeClaim(ctx, taskID, "package_validation_failed", s.now().UTC())
	}
	validatedSHA, _, err := hashRegularFile(task.PackageSnapshotPath)
	if err != nil || validatedSHA != sha {
		return s.store.failThemeClaim(ctx, taskID, "package_digest_mismatch", s.now().UTC())
	}
	return s.store.claimThemeUpdate(ctx, taskID, owner, observedVersion, validatedSHA, s.now().UTC())
}

func (s *wpUpdateArtifactService) preparePluginBackups(ctx context.Context, taskID, owner string) error {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || owner == "" {
		return errors.New("invalid plugin backup request")
	}
	var webRoot, dbName, status, kind, component, componentKey, leaseOwner, dbMode string
	var siteID int
	var dbSource sql.NullInt64
	var backupReady int
	err := s.store.db.QueryRowContext(ctx, `SELECT w.web_root,w.db_name,t.status,t.task_kind,t.component_type,
		t.component_key,t.lease_owner,t.backup_ready,t.database_backup_mode,t.site_id,t.database_backup_source_id FROM wp_update_tasks t
		JOIN websites w ON w.id=t.site_id WHERE t.id=?`, taskID).
		Scan(&webRoot, &dbName, &status, &kind, &component, &componentKey, &leaseOwner, &backupReady, &dbMode, &siteID, &dbSource)
	if err != nil || status != wpUpdateRunning || kind != "update" || component != "plugin" ||
		leaseOwner != owner || backupReady != 0 || !filepath.IsAbs(webRoot) || !validWPPluginComponentKey(componentKey) {
		return errors.New("update task is not plugin-backup-ready")
	}
	if dbMode == "reuse" &&
		(!dbSource.Valid || s.store.validateDatabaseBackupChoice(ctx, siteID, dbMode, dbSource.Int64, s.root, s.now().UTC()) != nil) {
		return errors.New("reused database backup unavailable")
	}
	taskDir := filepath.Join(s.root, taskID)
	if info, err := os.Lstat(taskDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid update task directory")
	}
	dbPath := filepath.Join(taskDir, "database.sql.gz")
	pluginPath := filepath.Join(taskDir, "plugin-files.tar.gz")
	created := []string{}
	defer func() {
		for _, name := range created {
			_ = os.Remove(name)
		}
	}()
	records, err := s.prepareDatabaseBackupRecord(ctx, dbMode, dbName, dbPath, &created)
	if err != nil {
		return err
	}
	if err := archiveWordPressPlugin(webRoot, componentKey, pluginPath); err != nil {
		_ = os.Remove(pluginPath)
		return fmt.Errorf("update plugin backup failed: %w", err)
	}
	created = append(created, pluginPath)
	pluginSHA, pluginSize, err := hashRegularFile(pluginPath)
	if err != nil || pluginSize == 0 {
		return errors.New("invalid update plugin backup")
	}
	records = append(records, wpUpdateBackupRecord{"plugin_files", pluginPath, pluginSize, pluginSHA})
	if err := s.store.markPluginBackupsReady(ctx, taskID, owner, records, s.now().UTC()); err != nil {
		committed, checkErr := s.backupsAreCommitted(ctx, taskID, records)
		if checkErr != nil {
			created = nil
			return err
		}
		if committed {
			created = nil
			return nil
		}
		return err
	}
	created = nil
	return nil
}

func (s *wpUpdateArtifactService) prepareThemeBackups(ctx context.Context, taskID, owner string) error {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || owner == "" {
		return errors.New("invalid theme backup request")
	}
	var webRoot, dbName, status, kind, component, componentKey, leaseOwner, dbMode string
	var siteID int
	var dbSource sql.NullInt64
	var backupReady int
	err := s.store.db.QueryRowContext(ctx, `SELECT w.web_root,w.db_name,t.status,t.task_kind,t.component_type,
		t.component_key,t.lease_owner,t.backup_ready,t.database_backup_mode,t.site_id,t.database_backup_source_id FROM wp_update_tasks t
		JOIN websites w ON w.id=t.site_id WHERE t.id=?`, taskID).
		Scan(&webRoot, &dbName, &status, &kind, &component, &componentKey, &leaseOwner, &backupReady, &dbMode, &siteID, &dbSource)
	if err != nil || status != wpUpdateRunning || kind != "update" || component != "theme" ||
		leaseOwner != owner || backupReady != 0 || !filepath.IsAbs(webRoot) || !validWPThemeComponentKey(componentKey) {
		return errors.New("update task is not theme-backup-ready")
	}
	if dbMode == "reuse" &&
		(!dbSource.Valid || s.store.validateDatabaseBackupChoice(ctx, siteID, dbMode, dbSource.Int64, s.root, s.now().UTC()) != nil) {
		return errors.New("reused database backup unavailable")
	}
	taskDir := filepath.Join(s.root, taskID)
	if info, err := os.Lstat(taskDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid update task directory")
	}
	dbPath := filepath.Join(taskDir, "database.sql.gz")
	themePath := filepath.Join(taskDir, "theme-files.tar.gz")
	created := []string{}
	defer func() {
		for _, name := range created {
			_ = os.Remove(name)
		}
	}()
	records, err := s.prepareDatabaseBackupRecord(ctx, dbMode, dbName, dbPath, &created)
	if err != nil {
		return err
	}
	if err := archiveWordPressTheme(webRoot, componentKey, themePath); err != nil {
		_ = os.Remove(themePath)
		return fmt.Errorf("update theme backup failed: %w", err)
	}
	created = append(created, themePath)
	themeSHA, themeSize, err := hashRegularFile(themePath)
	if err != nil || themeSize == 0 {
		return errors.New("invalid update theme backup")
	}
	records = append(records, wpUpdateBackupRecord{"theme_files", themePath, themeSize, themeSHA})
	if err := s.store.markThemeBackupsReady(ctx, taskID, owner, records, s.now().UTC()); err != nil {
		committed, checkErr := s.backupsAreCommitted(ctx, taskID, records)
		if checkErr != nil {
			created = nil
			return err
		}
		if committed {
			created = nil
			return nil
		}
		return err
	}
	created = nil
	return nil
}

func archiveWordPressPlugin(webRoot, componentKey, target string) error {
	if !validWPPluginComponentKey(componentKey) {
		return errors.New("invalid plugin component key")
	}
	parts := strings.Split(componentKey, "/")
	return archiveWordPressComponentDirectory(webRoot, "wp-content/plugins", parts[0], componentKey, target)
}

func archiveWordPressTheme(webRoot, componentKey, target string) error {
	if !validWPThemeComponentKey(componentKey) {
		return errors.New("invalid theme component key")
	}
	return archiveWordPressComponentDirectory(webRoot, "wp-content/themes", componentKey, path.Join(componentKey, "style.css"), target)
}

func archiveWordPressComponentDirectory(webRoot, contentRoot, componentDir, identityFile, target string) error {
	web, err := os.OpenRoot(webRoot)
	if err != nil {
		return err
	}
	defer web.Close()
	root, err := web.OpenRoot(contentRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	mainInfo, err := root.Lstat(identityFile)
	if err != nil || !mainInfo.Mode().IsRegular() || mainInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid component identity file")
	}
	componentInfo, err := root.Lstat(componentDir)
	if err != nil || !componentInfo.IsDir() || componentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid component directory")
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".component-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	err = fs.WalkDir(root.FS(), componentDir, func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("unsupported component backup entry")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = path.Clean(filepath.ToSlash(rel))
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		if info.IsDir() {
			header.Mode = 0755
		} else {
			header.Mode = 0644
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := root.Open(rel)
		if err != nil {
			return err
		}
		openedInfo, err := f.Stat()
		if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
			_ = f.Close()
			return errors.New("component backup entry changed during archive")
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	keep = true
	return nil
}
