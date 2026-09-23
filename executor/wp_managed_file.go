package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/naibabiji/wp-panel/config"
)

var lintManagedPHPFile = func(path string) error {
	out, err := exec.Command("/usr/bin/php", "-l", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("PHP 语法检查失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func lintManagedPHPData(data []byte) error {
	tmp, err := os.CreateTemp("", ".wp-panel-php-lint-*")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return lintManagedPHPFile(path)
}

func managedWordPressPath(webRoot string, parts ...string) (string, error) {
	webRoot = filepath.Clean(strings.TrimSpace(webRoot))
	if webRoot == "" || webRoot == "." || webRoot == string(filepath.Separator) || !filepath.IsAbs(webRoot) {
		return "", fmt.Errorf("网站目录不安全")
	}
	if cfg := config.AppConfig; cfg != nil && strings.TrimSpace(cfg.Paths.WWWRoot) != "" {
		if _, err := managedSubpath(cfg.Paths.WWWRoot, webRoot, "网站目录"); err != nil {
			return "", err
		}
	}
	rootInfo, err := os.Lstat(webRoot)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("网站目录不是普通目录")
	}
	target := filepath.Join(append([]string{webRoot}, parts...)...)
	rel, err := filepath.Rel(webRoot, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("目标路径不在网站目录内")
	}
	current := webRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("目标路径包含符号链接: %s", current)
		}
	}
	return target, nil
}

func writeManagedPHPFile(webRoot, path string, data []byte, fallbackMode os.FileMode) error {
	info, err := os.Lstat(path)
	mode := fallbackMode
	uid, gid := -1, -1
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("目标不是普通文件")
		}
		mode = info.Mode().Perm()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(stat.Uid), int(stat.Gid)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wp-panel-php-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if uid >= 0 && gid >= 0 {
		if err := os.Chown(tmpPath, uid, gid); err != nil {
			return err
		}
	}
	if err := lintManagedPHPFile(tmpPath); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
