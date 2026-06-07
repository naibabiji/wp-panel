package executor

import (
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/database"
)

func siteOwner(systemUser string) string {
	return systemUser + ":" + systemUser
}

func ensureSitePrimaryGroup(systemUser string) error {
	systemUser = strings.TrimSpace(systemUser)
	if systemUser == "" {
		return fmt.Errorf("system user is empty")
	}

	if _, err := executeCommand("getent", "group", systemUser); err != nil {
		if _, err := executeCommand("groupadd", "-r", systemUser); err != nil {
			if _, checkErr := executeCommand("getent", "group", systemUser); checkErr != nil {
				return fmt.Errorf("create site group %s: %w", systemUser, err)
			}
		}
	}

	if _, err := executeCommand("usermod", "-g", systemUser, systemUser); err != nil {
		return fmt.Errorf("set primary group for %s: %w", systemUser, err)
	}
	return nil
}

func HardenSiteSensitivePermissions(domain, webRoot, systemUser string) error {
	if err := ensureSitePrimaryGroup(systemUser); err != nil {
		return err
	}

	if webRoot != "" {
		if _, err := executeCommand("chown", "-R", siteOwner(systemUser), webRoot); err != nil {
			return err
		}
		configPath := filepath.Join(webRoot, "wp-config.php")
		if _, err := os.Stat(configPath); err == nil {
			if err := os.Chmod(configPath, 0600); err != nil {
				return err
			}
			if _, err := executeCommand("chown", siteOwner(systemUser), configPath); err != nil {
				return err
			}
		}
	}

	if domain != "" {
		secretsDir := filepath.Join("/var/wp-panel/site-secrets", domain)
		if _, err := os.Stat(secretsDir); err == nil {
			if err := os.Chmod(secretsDir, 0700); err != nil {
				return err
			}
			cfgPath := filepath.Join(secretsDir, "wp-panel-config.json")
			if _, err := os.Stat(cfgPath); err == nil {
				if err := os.Chmod(cfgPath, 0600); err != nil {
					return err
				}
			}
			if _, err := executeCommand("chown", "-R", siteOwner(systemUser), secretsDir); err != nil {
				return err
			}
		}
	}

	return nil
}

func isPathWithinRoot(rootPath, targetPath string) bool {
	cleanExistingPath := func(path string) (string, error) {
		cleanPath := filepath.Clean(path)
		resolved, err := filepath.EvalSymlinks(cleanPath)
		if err == nil {
			return resolved, nil
		}
		if runtime.GOOS == "windows" {
			return filepath.Abs(cleanPath)
		}
		return "", err
	}

	root, err := cleanExistingPath(rootPath)
	if err != nil {
		return false
	}
	target, err := cleanExistingPath(targetPath)
	if err != nil {
		return false
	}
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func ChownSitePath(path, allowedRoot, systemUser string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	allowedRoot = filepath.Clean(strings.TrimSpace(allowedRoot))
	systemUser = strings.TrimSpace(systemUser)
	if path == "" || path == "." || path == string(filepath.Separator) {
		return fmt.Errorf("path is unsafe")
	}
	if allowedRoot == "" || allowedRoot == "." || allowedRoot == string(filepath.Separator) {
		return fmt.Errorf("allowed root is unsafe")
	}
	if !isPathWithinRoot(allowedRoot, path) {
		return fmt.Errorf("path outside allowed root")
	}
	if systemUser == "" {
		return fmt.Errorf("system user is empty")
	}

	u, err := user.Lookup(systemUser)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return os.Chown(path, uid, gid)
	}
	return filepath.Walk(path, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, uid, gid)
	})
}

func init() {
	database.RegisterUpgrade("1.0.4", HardenSiteUnixIsolation)
}

// HardenSiteUnixIsolation 对所有已有站点执行 Unix 用户组隔离和敏感文件权限加固（升级迁移用）。
func HardenSiteUnixIsolation() error {
	db := database.GetDB()
	rows, err := db.Query("SELECT domain, web_root, system_user FROM websites")
	if err != nil {
		return fmt.Errorf("查询网站列表失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var domain, webRoot, systemUser string
		if err := rows.Scan(&domain, &webRoot, &systemUser); err != nil {
			log.Printf("[权限加固] 读取网站数据失败: %v", err)
			continue
		}
		if err := HardenSiteSensitivePermissions(domain, webRoot, systemUser); err != nil {
			log.Printf("[权限加固] %s: 安全权限设置失败: %v", domain, err)
		}
	}

	return rows.Err()
}

// InstallPluginPermissions 安装插件时设置插件目录和密钥目录权限。
// 与 HardenSiteSensitivePermissions 不同，此函数不 chown 整站，且所有错误静默忽略（不阻断插件安装）。
func InstallPluginPermissions(domain, systemUser, pluginDir string) {
	systemUser = strings.TrimSpace(systemUser)
	if systemUser == "" {
		return
	}

	ensureSitePrimaryGroup(systemUser)
	owner := siteOwner(systemUser)

	if pluginDir != "" {
		executeCommand("chown", "-R", owner, pluginDir)
	}

	if domain != "" {
		secretsDir := filepath.Join("/var/wp-panel/site-secrets", domain)
		if _, err := os.Stat(secretsDir); err == nil {
			os.Chmod(secretsDir, 0700)
			cfgPath := filepath.Join(secretsDir, "wp-panel-config.json")
			if _, err := os.Stat(cfgPath); err == nil {
				os.Chmod(cfgPath, 0600)
			}
			executeCommand("chown", "-R", owner, secretsDir)
		}
	}
}
