package executor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func deployWordPress(packagePath, webRoot, tmpDir string) error {
	os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, "wordpress.zip")

	if err := downloadWP(packagePath, zipPath); err != nil {
		return err
	}

	extractDir := filepath.Join(tmpDir, "wp_extract")
	if _, err := executeCommand("unzip", "-q", "-o", zipPath, "-d", extractDir); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}

	srcDir := extractDir
	if info, err := os.Stat(filepath.Join(extractDir, "wordpress")); err == nil && info.IsDir() {
		srcDir = filepath.Join(extractDir, "wordpress")
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("读取WordPress文件失败: %w", err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(webRoot, entry.Name())
		if err := copyPath(srcPath, dstPath); err != nil {
			return fmt.Errorf("移动文件 %s 失败: %w", entry.Name(), err)
		}
	}

	return nil
}

func copyPath(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()

	if _, err := io.Copy(d, s); err != nil {
		return err
	}

	return os.Chmod(dst, 0644)
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if err := copyPath(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func downloadWP(localPath, destPath string) error {
	if _, err := executeCommand("wget", "-q", "-T", "30", "-t", "3", "-O", destPath,
		"https://wordpress.org/latest.zip"); err == nil {
		return nil
	}

	if _, err := executeCommand("cp", "-f", localPath, destPath); err != nil {
		return fmt.Errorf("下载WordPress失败且本地备用包不可用: %w", err)
	}

	return nil
}

func removeDefaultPlugins(webRoot string) {
	os.RemoveAll(filepath.Join(webRoot, "wp-content", "plugins", "akismet"))
	os.Remove(filepath.Join(webRoot, "wp-content", "plugins", "hello.php"))
}

func removeUnusedThemes(webRoot string) {
	for _, slug := range []string{"twentytwentyfour", "twentytwentythree"} {
		os.RemoveAll(filepath.Join(webRoot, "wp-content", "themes", slug))
	}
}

func installExtensions(webRoot, systemUser string, themes, plugins []string) {
	for _, slug := range themes {
		installZip(filepath.Join(webRoot, "wp-content", "themes"), slug, "theme")
	}
	for _, slug := range plugins {
		installZip(filepath.Join(webRoot, "wp-content", "plugins"), slug, "plugin")
	}
	if len(themes) > 0 || len(plugins) > 0 {
		executeCommand("chown", "-R", siteOwner(systemUser),
			filepath.Join(webRoot, "wp-content", "themes"),
			filepath.Join(webRoot, "wp-content", "plugins"))
	}
}

func installZip(destDir, slug, etype string) {
	url := fmt.Sprintf("https://downloads.wordpress.org/%s/%s.latest-stable.zip", etype, slug)
	zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("wp_ext_%s_%s.zip", etype, slug))
	defer os.Remove(zipPath)

	if _, err := executeCommand("wget", "-q", "-T", "30", "-O", zipPath, url); err != nil {
		return
	}
	os.MkdirAll(destDir, 0755)
	executeCommand("unzip", "-q", "-o", zipPath, "-d", destDir)
}
