package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	wpCoreRestoreMaxEntries = 20000
	wpCoreRestoreMaxBytes   = int64(512 << 20)
	wpCoreMySQLPath         = "/usr/bin/mysql"
)

func defaultWPCoreDatabaseRestorer(ctx context.Context, dbName, backupPath string) error {
	if !isValidMySQLIdentifier(dbName) {
		return errors.New("invalid update database")
	}
	if err := validateRestoreBackupFile(backupPath); err != nil {
		return errors.New("invalid update database backup")
	}
	password := readMariaDBPassword()
	if password == "" {
		return errors.New("database credentials unavailable")
	}
	mysqlPath, err := validateInventoryBinary(wpCoreMySQLPath, "/usr/bin", 0, 0)
	if err != nil {
		return errors.New("mysql client unavailable")
	}
	return restoreWPCoreDatabase(ctx, mysqlPath, dbName, password, backupPath)
}

func restoreWPCoreDatabase(ctx context.Context, mysqlPath, dbName, password, backupPath string) error {
	if !filepath.IsAbs(mysqlPath) || !isValidMySQLIdentifier(dbName) || password == "" {
		return errors.New("invalid update database restore")
	}
	if err := validateRestoreBackupFile(backupPath); err != nil {
		return errors.New("invalid update database backup")
	}
	listCmd := exec.CommandContext(ctx, mysqlPath, "-u", "root", "-B", "-N", "-e", "SELECT CONCAT(CASE WHEN TABLE_TYPE = 'VIEW' THEN 'DROP VIEW IF EXISTS `' ELSE 'DROP TABLE IF EXISTS `' END, REPLACE(TABLE_NAME, '`', '``'), '`;') FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = '"+dbName+"' ORDER BY CASE WHEN TABLE_TYPE = 'VIEW' THEN 0 ELSE 1 END")
	listCmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	dropSQL, err := listCmd.Output()
	if err != nil {
		return errors.New("update database table listing failed")
	}
	backup, err := os.Open(backupPath)
	if err != nil {
		return errors.New("update database backup open failed")
	}
	defer backup.Close()
	gz, err := gzip.NewReader(backup)
	if err != nil {
		return errors.New("update database backup gzip invalid")
	}
	defer gz.Close()
	cmd := exec.CommandContext(ctx, mysqlPath, "-u", "root", dbName)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.New("update database restore pipe failed")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return errors.New("update database restore start failed")
	}
	writeErr := error(nil)
	if _, err := io.WriteString(stdin, "SET FOREIGN_KEY_CHECKS=0;\n"+string(dropSQL)+"\n"); err != nil {
		writeErr = err
	}
	if writeErr == nil {
		writeErr = writeSanitizedRestoreSQL(stdin, gz)
	}
	if writeErr == nil {
		_, writeErr = io.WriteString(stdin, "\nSET FOREIGN_KEY_CHECKS=1;\n")
	}
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if writeErr != nil || closeErr != nil || waitErr != nil {
		return errors.New("update database restore failed")
	}
	return nil
}

func defaultWPCoreFilesRestorer(ctx context.Context, webRoot, backupPath, taskID, systemUser string) error {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || !filepath.IsAbs(backupPath) {
		return errors.New("invalid core restore request")
	}
	u, err := user.Lookup(systemUser)
	if err != nil {
		return errors.New("core restore user unavailable")
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || u.Username != systemUser {
		return errors.New("invalid core restore identity")
	}
	rootPath, err := safeSiteWebRoot(webRoot)
	if err != nil {
		return err
	}
	stageName := ".wp-panel-core-restore-stage-" + taskID
	oldName := ".wp-panel-core-restore-old-" + taskID
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(stageName); !os.IsNotExist(err) {
		return errors.New("core restore staging already exists")
	}
	if _, err := root.Lstat(oldName); !os.IsNotExist(err) {
		return errors.New("core restore rollback directory already exists")
	}
	if err := root.Mkdir(stageName, 0700); err != nil {
		return err
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = root.RemoveAll(stageName)
		}
	}()
	stage, err := root.OpenRoot(stageName)
	if err != nil {
		return err
	}
	if err := extractCoreBackupTar(ctx, backupPath, stage, uid, gid); err != nil {
		stage.Close()
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	targets := append([]string{"wp-admin", "wp-includes"}, wpCoreRootFiles...)
	for _, target := range targets {
		if info, err := root.Lstat(filepath.Join(stageName, target)); err != nil || (target == "wp-admin" || target == "wp-includes") && !info.IsDir() || target != "wp-admin" && target != "wp-includes" && !info.Mode().IsRegular() {
			return errors.New("core restore backup is incomplete")
		}
	}
	if err := root.Mkdir(oldName, 0700); err != nil {
		return err
	}
	cleanupOld := true
	defer func() {
		if cleanupOld {
			_ = root.RemoveAll(oldName)
		}
	}()
	type moveState struct {
		target            string
		hadOld, installed bool
	}
	states := make([]moveState, 0, len(targets))
	rollbackMoves := func() {
		for i := len(states) - 1; i >= 0; i-- {
			state := states[i]
			if state.installed {
				_ = root.RemoveAll(state.target)
			}
			if state.hadOld {
				_ = root.Rename(filepath.Join(oldName, state.target), state.target)
			}
		}
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			rollbackMoves()
			return err
		}
		state := moveState{target: target}
		if _, err := root.Lstat(target); err == nil {
			if err := root.Rename(target, filepath.Join(oldName, target)); err != nil {
				rollbackMoves()
				return err
			}
			state.hadOld = true
		} else if !os.IsNotExist(err) {
			rollbackMoves()
			return err
		}
		states = append(states, state)
		if err := root.Rename(filepath.Join(stageName, target), target); err != nil {
			rollbackMoves()
			return err
		}
		states[len(states)-1].installed = true
	}
	if err := root.RemoveAll(stageName); err != nil {
		return err
	}
	cleanupStage = false
	if err := root.RemoveAll(oldName); err != nil {
		return err
	}
	cleanupOld = false
	dir, err := os.Open(rootPath)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func extractCoreBackupTar(ctx context.Context, backupPath string, root *os.Root, uid, gid int) error {
	f, err := os.Open(backupPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := 0
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > wpCoreRestoreMaxEntries {
			return errors.New("core restore archive has too many entries")
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if !allowedCoreBackupPath(name) || filepath.IsAbs(name) || name == "." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return errors.New("core restore archive path rejected")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureCoreRestoreDirectories(root, name, uid, gid); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > wpCoreRestoreMaxBytes-total {
				return errors.New("core restore archive too large")
			}
			if err := ensureCoreRestoreDirectories(root, filepath.Dir(name), uid, gid); err != nil {
				return err
			}
			dst, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			written, copyErr := copyWithContext(ctx, dst, io.LimitReader(tr, header.Size))
			chmodErr := dst.Chmod(0644)
			chownErr := dst.Chown(uid, gid)
			closeErr := dst.Close()
			if copyErr != nil || chmodErr != nil || chownErr != nil || closeErr != nil || written != header.Size {
				return errors.New("core restore archive extraction failed")
			}
			total += written
		default:
			return errors.New("core restore archive type rejected")
		}
	}
	return nil
}

// ensureCoreRestoreDirectories mirrors ensurePluginRestoreDirectories: it
// creates (or reuses) each path component and chowns it to the site's
// system user. The shared ensureRootDirectories helper (used by generic ZIP
// extraction elsewhere) does not chown, which previously left restored
// wp-admin/wp-includes owned by root — the process running the restore —
// instead of the site user, breaking every subsequent core update attempt.
func ensureCoreRestoreDirectories(root *os.Root, name string, uid, gid int) error {
	if name == "." || name == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid core restore directory")
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, 0755); err != nil {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid core restore directory")
		}
		dir, err := root.Open(current)
		if err != nil {
			return errors.New("invalid core restore directory")
		}
		chmodErr := dir.Chmod(0755)
		chownErr := dir.Chown(uid, gid)
		closeErr := dir.Close()
		if chmodErr != nil || chownErr != nil || closeErr != nil {
			return errors.New("core restore directory ownership failed")
		}
	}
	return nil
}

func allowedCoreBackupPath(name string) bool {
	slash := filepath.ToSlash(name)
	if slash == "wp-admin" || strings.HasPrefix(slash, "wp-admin/") || slash == "wp-includes" || strings.HasPrefix(slash, "wp-includes/") {
		return true
	}
	for _, rootFile := range wpCoreRootFiles {
		if slash == rootFile {
			return true
		}
	}
	return false
}
