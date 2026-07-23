package executor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	wpPluginRestoreMaxEntries = 20_000
	wpPluginRestoreMaxBytes   = int64(1 << 30)
)

func defaultWPPluginFilesRestorer(ctx context.Context, execution wpPluginUpdateExecution) error {
	if !wpUpdateTaskIDPattern.MatchString(execution.Task.ID) || !validWPPluginComponentKey(execution.Task.ComponentKey) {
		return errors.New("invalid plugin restore request")
	}
	backupInfo, err := os.Lstat(execution.PluginBackup)
	if err != nil || !backupInfo.Mode().IsRegular() || backupInfo.Mode()&os.ModeSymlink != 0 || backupInfo.Size() == 0 {
		return errors.New("invalid plugin restore backup")
	}
	webRoot, err := safeSiteWebRoot(execution.WebRoot)
	if err != nil {
		return err
	}
	u, err := user.Lookup(execution.SystemUser)
	if err != nil {
		return errors.New("plugin restore user unavailable")
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || u.Username != execution.SystemUser {
		return errors.New("invalid plugin restore identity")
	}
	pluginsPath := filepath.Join(webRoot, "wp-content", "plugins")
	root, err := os.OpenRoot(pluginsPath)
	if err != nil {
		return err
	}
	defer root.Close()
	slug := strings.Split(execution.Task.ComponentKey, "/")[0]
	mainFile := strings.Split(execution.Task.ComponentKey, "/")[1]
	stageName := ".wp-panel-plugin-restore-stage-" + execution.Task.ID
	oldName := ".wp-panel-plugin-restore-old-" + execution.Task.ID
	if _, err := root.Lstat(stageName); !os.IsNotExist(err) {
		return errors.New("plugin restore staging already exists")
	}
	if _, err := root.Lstat(oldName); !os.IsNotExist(err) {
		return errors.New("plugin restore quarantine already exists")
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
	if err := extractPluginBackupTar(ctx, execution.PluginBackup, stage, slug, uid, gid); err != nil {
		stage.Close()
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	mainInfo, err := root.Lstat(filepath.Join(stageName, slug, mainFile))
	if err != nil || !mainInfo.Mode().IsRegular() || mainInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("plugin restore backup is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	hadOld := false
	if info, err := root.Lstat(slug); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid current plugin directory")
		}
		if err := root.Rename(slug, oldName); err != nil {
			return err
		}
		hadOld = true
	} else if !os.IsNotExist(err) {
		return err
	}
	rollback := func() {
		_ = root.RemoveAll(slug)
		if hadOld {
			_ = root.Rename(oldName, slug)
		}
	}
	if err := root.Rename(filepath.Join(stageName, slug), slug); err != nil {
		rollback()
		return err
	}
	if err := syncDirectory(pluginsPath); err != nil {
		rollback()
		return err
	}
	if err := root.RemoveAll(stageName); err != nil {
		rollback()
		return err
	}
	cleanupStage = false
	if hadOld {
		if err := root.RemoveAll(oldName); err != nil {
			return err
		}
	}
	return syncDirectory(pluginsPath)
}

func extractPluginBackupTar(ctx context.Context, backupPath string, root *os.Root, slug string, uid, gid int) error {
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
	seen := map[string]bool{}
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
		if entries > wpPluginRestoreMaxEntries {
			return errors.New("plugin restore archive has too many entries")
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if filepath.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || (name != slug && !strings.HasPrefix(name, slug+string(filepath.Separator))) || seen[name] {
			return errors.New("plugin restore archive path rejected")
		}
		seen[name] = true
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensurePluginRestoreDirectories(root, name, uid, gid); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > wpPluginRestoreMaxBytes-total {
				return errors.New("plugin restore archive too large")
			}
			if err := ensurePluginRestoreDirectories(root, filepath.Dir(name), uid, gid); err != nil {
				return err
			}
			dst, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			written, copyErr := copyWithContext(ctx, dst, io.LimitReader(tr, header.Size))
			chmodErr := dst.Chmod(0644)
			chownErr := dst.Chown(uid, gid)
			syncErr := dst.Sync()
			closeErr := dst.Close()
			if copyErr != nil || chmodErr != nil || chownErr != nil || syncErr != nil || closeErr != nil || written != header.Size {
				return errors.New("plugin restore extraction failed")
			}
			total += written
		default:
			return errors.New("plugin restore archive type rejected")
		}
	}
	return nil
}

func ensurePluginRestoreDirectories(root *os.Root, name string, uid, gid int) error {
	if name == "." || name == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid plugin restore directory")
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
			return errors.New("invalid plugin restore directory")
		}
		dir, err := root.Open(current)
		if err != nil {
			return err
		}
		chmodErr := dir.Chmod(0755)
		chownErr := dir.Chown(uid, gid)
		closeErr := dir.Close()
		if chmodErr != nil || chownErr != nil || closeErr != nil {
			return errors.New("plugin restore directory ownership failed")
		}
	}
	return nil
}
