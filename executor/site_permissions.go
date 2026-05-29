package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
