package models

import "time"

type WPThemeUpdatePreview struct {
	Available            bool                          `json:"available"`
	SiteID               int                           `json:"site_id"`
	Domain               string                        `json:"domain"`
	ComponentKey         string                        `json:"component_key"`
	Name                 string                        `json:"name"`
	CurrentVersion       string                        `json:"current_version"`
	TargetVersion        string                        `json:"target_version"`
	Template             string                        `json:"template"`
	CurrentTheme         bool                          `json:"current_theme"`
	PackageSource        string                        `json:"package_source"`
	VerificationRequired string                        `json:"verification_required"`
	DatabaseBackup       bool                          `json:"database_backup"`
	RecentDatabaseBackup *WPUpdateRecentDatabaseBackup `json:"recent_database_backup,omitempty"`
	ThemeFilesBackup     bool                          `json:"theme_files_backup"`
	ConfirmationToken    string                        `json:"confirmation_token,omitempty"`
	RiskToken            string                        `json:"risk_token,omitempty"`
	ExpiresAt            *time.Time                    `json:"expires_at,omitempty"`
}

type WPThemeUpdateTask = WPPluginUpdateTask
