package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	sitePluginConfigFileName = "wp-panel-config.json"
	sitePluginConfigEnvName  = "WP_PANEL_CONFIG_PATH"
)

var siteSecretsRoot = "/var/wp-panel/site-secrets"

type sitePluginIdentity struct {
	PanelURL                    string `json:"panel_url"`
	APIKey                      string `json:"api_key"`
	DisableApplicationPasswords bool   `json:"disable_application_passwords"`
}

func sitePluginSecretsDir(domain string) string {
	return filepath.Join(siteSecretsRoot, domain)
}

func sitePluginConfigPath(domain string) string {
	return filepath.Join(sitePluginSecretsDir(domain), sitePluginConfigFileName)
}

// WriteSitePluginIdentity creates or replaces the panel-issued identity for a site.
// WordPress never owns this file; it only receives read access through its PHP-FPM pool.
func WriteSitePluginIdentity(domain, systemUser, panelURL, apiKey string, disableApplicationPasswords bool) error {
	if !IsValidDomain(domain) || systemUser == "" || panelURL == "" || len(apiKey) < 32 {
		return errors.New("invalid site plugin identity")
	}
	content, err := json.Marshal(sitePluginIdentity{PanelURL: panelURL, APIKey: apiKey, DisableApplicationPasswords: disableApplicationPasswords})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(siteSecretsRoot, 0711); err != nil {
		return fmt.Errorf("create site secrets root: %w", err)
	}
	if err := os.Chmod(siteSecretsRoot, 0711); err != nil {
		return fmt.Errorf("set site secrets root permissions: %w", err)
	}
	dir := sitePluginSecretsDir(domain)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create site plugin identity directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".wp-panel-config-*")
	if err != nil {
		return fmt.Errorf("create temporary site plugin identity: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary site plugin identity permissions: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary site plugin identity: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary site plugin identity: %w", err)
	}
	if err := os.Rename(tmpPath, sitePluginConfigPath(domain)); err != nil {
		return fmt.Errorf("write site plugin identity: %w", err)
	}
	InstallPluginPermissions(domain, systemUser, "")
	return nil
}

func SitePluginIdentityAvailable(domain, expectedAPIKey string) bool {
	if !IsValidDomain(domain) || expectedAPIKey == "" {
		return false
	}
	content, err := os.ReadFile(sitePluginConfigPath(domain))
	if err != nil {
		return false
	}
	var identity sitePluginIdentity
	return json.Unmarshal(content, &identity) == nil && identity.PanelURL != "" && identity.APIKey == expectedAPIKey
}

// UpdateSitePluginApplicationPasswordPolicy refreshes an existing site identity.
// Sites without a companion identity are left alone; installation writes the
// current policy together with the new identity.
func UpdateSitePluginApplicationPasswordPolicy(domain, systemUser string, disabled bool) error {
	content, err := os.ReadFile(sitePluginConfigPath(domain))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var identity sitePluginIdentity
	if json.Unmarshal(content, &identity) != nil || identity.PanelURL == "" || len(identity.APIKey) < 32 {
		return errors.New("invalid site plugin identity")
	}
	return WriteSitePluginIdentity(domain, systemUser, identity.PanelURL, identity.APIKey, disabled)
}

// moveSitePluginIdentity moves a domain-keyed identity without ever replacing an
// existing target. The bool reports whether a source identity directory existed.
func moveSitePluginIdentity(oldDomain, newDomain string) (bool, error) {
	oldDir := sitePluginSecretsDir(oldDomain)
	newDir := sitePluginSecretsDir(newDomain)
	info, err := os.Lstat(oldDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect source site plugin identity: %w", err)
	}
	if !info.IsDir() {
		return false, errors.New("source site plugin identity is not a directory")
	}
	if _, err := os.Lstat(newDir); err == nil {
		return false, errors.New("target site plugin identity already exists")
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect target site plugin identity: %w", err)
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return false, fmt.Errorf("rename site plugin identity: %w", err)
	}
	return true, nil
}
