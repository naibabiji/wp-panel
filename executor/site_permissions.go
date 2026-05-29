package executor

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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
