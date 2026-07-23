package executor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"
)

var wpUpdateTaskIDPattern = regexp.MustCompile(`^wpu_[0-9a-f]{32}$`)

var wpCoreRootFiles = []string{
	"index.php", "xmlrpc.php", "wp-activate.php", "wp-blog-header.php", "wp-comments-post.php",
	"wp-config-sample.php", "wp-cron.php", "wp-links-opml.php", "wp-load.php", "wp-login.php",
	"wp-mail.php", "wp-settings.php", "wp-signup.php", "wp-trackback.php",
}

type wpUpdateDatabaseDumper func(context.Context, string, string) error

type wpUpdateArtifactService struct {
	store  *wpUpdateStore
	root   string
	dumpDB wpUpdateDatabaseDumper
	now    func() time.Time
}

func newWPUpdateArtifactService(store *wpUpdateStore, root string, dumpDB wpUpdateDatabaseDumper) (*wpUpdateArtifactService, error) {
	if store == nil || store.db == nil || !filepath.IsAbs(root) || dumpDB == nil {
		return nil, errors.New("invalid update artifact service")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid update artifact root")
	}
	return &wpUpdateArtifactService{store: store, root: root, dumpDB: dumpDB, now: time.Now}, nil
}

func (s *wpUpdateArtifactService) snapshotAndSealPackage(ctx context.Context, taskID, sourcePath, expectedSHA, verification string) (WPUpdateTask, error) {
	return s.snapshotAndSealPackageChecked(ctx, taskID, sourcePath, expectedSHA, verification, nil)
}

func (s *wpUpdateArtifactService) snapshotAndSealPackageChecked(ctx context.Context, taskID, sourcePath, expectedSHA, verification string, validate func(string) error) (WPUpdateTask, error) {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || !filepath.IsAbs(sourcePath) || !wpUpdateSHA256Pattern.MatchString(expectedSHA) {
		return WPUpdateTask{}, errors.New("invalid package snapshot request")
	}
	task, err := s.store.getTask(ctx, taskID)
	if err != nil || task.Status != wpUpdatePreparing || task.TaskKind != "update" {
		return WPUpdateTask{}, errors.New("update plan is not snapshot-ready")
	}
	if task.ComponentType == "plugin" && validate == nil {
		return WPUpdateTask{}, errors.New("plugin package validation is required")
	}
	taskDir, err := s.createTaskDir(taskID)
	if err != nil {
		return WPUpdateTask{}, err
	}
	keepDir := false
	defer func() {
		if !keepDir {
			_ = os.RemoveAll(taskDir)
		}
	}()
	target := filepath.Join(taskDir, "package.zip")
	actual, _, err := copyFileAtomic(sourcePath, target, 0600)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if actual != expectedSHA {
		return WPUpdateTask{}, errors.New("package snapshot digest mismatch")
	}
	if validate != nil {
		if err := validate(target); err != nil {
			return WPUpdateTask{}, err
		}
		validatedSHA, _, err := hashRegularFile(target)
		if err != nil || validatedSHA != actual {
			return WPUpdateTask{}, errors.New("package snapshot changed during validation")
		}
	}
	sealed, err := s.store.sealPlan(ctx, taskID, actual, verification, target, s.now().UTC())
	if err != nil {
		current, lookupErr := s.store.getTask(ctx, taskID)
		if lookupErr == nil && current.Status == wpUpdateQueued && current.PackageSnapshotPath == target && current.DownloadedSHA256 == actual {
			keepDir = true
			return current, nil
		}
		if lookupErr != nil || current.Status != wpUpdatePreparing {
			// Preserve the task-owned directory when persistence is ambiguous. An
			// orphan is safer than a sealed plan that references a deleted package.
			keepDir = true
		}
		return WPUpdateTask{}, err
	}
	keepDir = true
	return sealed, nil
}

func (s *wpUpdateArtifactService) prepareCoreBackups(ctx context.Context, taskID, owner string) error {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || owner == "" {
		return errors.New("invalid core backup request")
	}
	var webRoot, dbName, status, kind, component, leaseOwner, dbMode string
	var siteID int
	var dbSource sql.NullInt64
	var backupReady int
	err := s.store.db.QueryRowContext(ctx, `SELECT w.web_root,w.db_name,t.status,t.task_kind,t.component_type,t.lease_owner,t.backup_ready,
		t.database_backup_mode,t.site_id,t.database_backup_source_id
		FROM wp_update_tasks t JOIN websites w ON w.id=t.site_id WHERE t.id=?`, taskID).
		Scan(&webRoot, &dbName, &status, &kind, &component, &leaseOwner, &backupReady, &dbMode, &siteID, &dbSource)
	if err != nil || status != wpUpdateRunning || kind != "update" || component != "core" || leaseOwner != owner || backupReady != 0 || !filepath.IsAbs(webRoot) {
		return errors.New("update task is not backup-ready")
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
	corePath := filepath.Join(taskDir, "core-files.tar.gz")
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
	if err := archiveWordPressCore(webRoot, corePath); err != nil {
		_ = os.Remove(corePath)
		return fmt.Errorf("update core backup failed: %w", err)
	}
	created = append(created, corePath)
	coreSHA, coreSize, err := hashRegularFile(corePath)
	if err != nil || coreSize == 0 {
		return errors.New("invalid update core backup")
	}
	records = append(records, wpUpdateBackupRecord{"core_files", corePath, coreSize, coreSHA})
	if err := s.store.markBackupsReady(ctx, taskID, owner, records, s.now().UTC()); err != nil {
		committed, checkErr := s.backupsAreCommitted(ctx, taskID, records)
		if checkErr != nil {
			// Keep files if the database outcome cannot be established. Cleanup can
			// safely reconcile an orphan later; deleting a referenced backup cannot.
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

func (s *wpUpdateArtifactService) backupsAreCommitted(ctx context.Context, taskID string, records []wpUpdateBackupRecord) (bool, error) {
	var ready int
	if err := s.store.db.QueryRowContext(ctx, `SELECT backup_ready FROM wp_update_tasks WHERE id=?`, taskID).Scan(&ready); err != nil {
		return false, err
	}
	for _, record := range records {
		var count int
		if err := s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wp_update_task_backups
			WHERE task_id=? AND protected=1 AND deleted_at IS NULL AND kind=? AND file_path=? AND file_size=? AND sha256=?`,
			taskID, record.Kind, record.FilePath, record.FileSize, record.SHA256).Scan(&count); err != nil || count != 1 {
			return false, err
		}
	}
	return ready == 1, nil
}

func (s *wpUpdateArtifactService) prepareDatabaseBackupRecord(ctx context.Context, mode, dbName, dbPath string, created *[]string) ([]wpUpdateBackupRecord, error) {
	if mode == "reuse" {
		return nil, nil
	}
	if mode != "fresh" {
		return nil, errors.New("invalid database backup mode")
	}
	if err := s.dumpDB(ctx, dbName, dbPath); err != nil {
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("update database backup failed: %w", err)
	}
	*created = append(*created, dbPath)
	if err := syncRegularFile(dbPath); err != nil {
		return nil, errors.New("invalid update database backup")
	}
	dbSHA, dbSize, err := hashRegularFile(dbPath)
	if err != nil || dbSize == 0 {
		return nil, errors.New("invalid update database backup")
	}
	return []wpUpdateBackupRecord{{"database", dbPath, dbSize, dbSHA}}, nil
}

func (s *wpUpdateArtifactService) createTaskDir(taskID string) (string, error) {
	taskDir := filepath.Join(s.root, taskID)
	if err := os.Mkdir(taskDir, 0700); err != nil {
		return "", err
	}
	return taskDir, nil
}

func copyFileAtomic(source, target string, mode fs.FileMode) (string, int64, error) {
	pathInfo, err := os.Lstat(source)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("package source is not a regular file")
	}
	src, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.New("package source is not a regular file")
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".snapshot-*.tmp")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), src)
	if err != nil {
		return "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", 0, err
	}
	keep = true
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return "", 0, err
		}
	} else {
		return "", 0, err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), size, nil
}

func hashRegularFile(name string) (string, int64, error) {
	pathInfo, err := os.Lstat(name)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("artifact is not a regular file")
	}
	f, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.New("artifact is not a regular file")
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), size, nil
}

func syncRegularFile(name string) error {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("artifact is not a regular file")
	}
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func archiveWordPressCore(webRoot, target string) error {
	root, err := os.OpenRoot(webRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".core-*.tmp")
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
	names := append([]string{"wp-admin", "wp-includes"}, wpCoreRootFiles...)
	for _, name := range names {
		err := fs.WalkDir(root.FS(), name, func(rel string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errors.New("unsupported core backup entry")
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
			if info.Mode().IsRegular() {
				f, err := root.Open(rel)
				if err != nil {
					return err
				}
				openedInfo, err := f.Stat()
				if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
					_ = f.Close()
					return errors.New("core backup entry changed during archive")
				}
				_, copyErr := io.Copy(tw, f)
				closeErr := f.Close()
				if copyErr != nil {
					return copyErr
				}
				return closeErr
			}
			return nil
		})
		if err != nil {
			return err
		}
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

func defaultWPUpdateDatabaseDumper(_ context.Context, dbName, target string) error {
	password := readMariaDBPassword()
	if password == "" {
		return errors.New("database credentials unavailable")
	}
	return dumpDatabaseToGzip(dbName, password, target)
}
