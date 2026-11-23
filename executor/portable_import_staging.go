package executor

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (s *PortableImports) portableTreeRoot() (string, error) {
	if !filepath.IsAbs(s.cfg.Paths.WWWRoot) {
		return "", os.ErrNotExist
	}
	parent, err := filepath.EvalSymlinks(s.cfg.Paths.WWWRoot)
	if err != nil {
		return "", err
	}
	var stat unix.Stat_t
	if err := unix.Stat(parent, &stat); err != nil {
		return "", err
	}
	if err := validateSecureSiteParent(&stat); err != nil {
		return "", err
	}
	return filepath.Join(parent, ".wp-panel-restore"), nil
}

func (s *PortableImports) createPortableTree(id string) (string, error) {
	if !portableID.MatchString(id) {
		return "", ErrPortableMissing
	}
	root, err := s.portableTreeRoot()
	if err != nil {
		return "", fmt.Errorf("无法检查网站磁盘：%w", err)
	}
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := requirePrivateDir(root); err != nil {
		return "", err
	}
	var stageStat, parentStat unix.Stat_t
	if err := unix.Stat(root, &stageStat); err != nil {
		return "", err
	}
	if err := unix.Stat(filepath.Dir(root), &parentStat); err != nil {
		return "", err
	}
	if stageStat.Dev != parentStat.Dev {
		return "", errors.New("恢复暂存和网站根目录必须位于同一文件系统")
	}
	work := filepath.Join(root, id)
	if err := os.Mkdir(work, 0700); err != nil {
		return "", err
	}
	return filepath.Join(work, "website"), nil
}

func removePortableTree(root, id string) error {
	if !portableID.MatchString(id) {
		return ErrPortableMissing
	}
	work := filepath.Join(root, id)
	if err := requirePrivateDir(work); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(work)
}

func (s *PortableImports) removePortableTree(id string) error {
	root, err := s.portableTreeRoot()
	if err == nil {
		err = requirePrivateDir(root)
	}
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return removePortableTree(root, id)
}

func logPortableTreeCleanup(s *PortableImports, id string) {
	if err := s.removePortableTree(id); err != nil {
		log.Printf("clean portable file staging %s: %v", id, err)
	}
}

func (s *PortableImports) cleanupPortableTrees() error {
	root, err := s.portableTreeRoot()
	if err == nil {
		err = requirePrivateDir(root)
	}
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, entry := range entries {
		if entry.Name() != s.runningID && portableID.MatchString(entry.Name()) {
			cleanupErr = errors.Join(cleanupErr, removePortableTree(root, entry.Name()))
		}
	}
	return cleanupErr
}

// publishRestoredTree finalizes a verified tree inside private staging and
// installs it with a same-filesystem rename that cannot replace another site.
func publishRestoredTree(source, target string) error {
	if err := os.Chmod(source, 0755); err != nil {
		return err
	}
	if err := syncRestoredTree(source); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EXDEV) {
			return errors.New("恢复暂存和网站根目录必须位于同一文件系统")
		}
		return err
	}
	return errors.Join(syncRestorePath(filepath.Dir(target)), syncRestorePath(filepath.Dir(source)))
}

func syncRestoredTree(tree string) error {
	const workers = 4
	paths := make(chan string, workers)
	results := make(chan error, workers)
	for range workers {
		go func() {
			var firstErr error
			for path := range paths {
				if firstErr == nil {
					firstErr = syncRestorePath(path)
				}
			}
			results <- firstErr
		}()
	}
	walkErr := filepath.WalkDir(tree, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("恢复目录包含非普通文件：%s", entry.Name())
		}
		paths <- path
		return nil
	})
	close(paths)
	for range workers {
		walkErr = errors.Join(walkErr, <-results)
	}
	return walkErr
}

func syncRestorePath(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
