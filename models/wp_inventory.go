package models

import "time"

type WPInventorySummary struct {
	SiteID                 int                     `json:"site_id"`
	CollectionStatus       string                  `json:"collection_status"`
	HasSuccessfulInventory bool                    `json:"has_successful_inventory"`
	WordPress              WPInventoryWordPress    `json:"wordpress"`
	Counts                 WPInventoryCounts       `json:"counts"`
	CoreUpgradeAvailable   bool                    `json:"core_upgrade_available"`
	UpdateChecks           WPInventoryUpdateChecks `json:"update_checks"`
	LastAttemptAt          *time.Time              `json:"last_attempt_at"`
	LastSuccessAt          *time.Time              `json:"last_success_at"`
	LastError              *WPInventoryStateError  `json:"last_error"`
	ActiveTask             *WPInventoryTask        `json:"active_task"`
}

// WPInventoryUpdateChecks reports whether WordPress's own update-check cache
// (the "transient") was actually present for each component group during
// the last successful scan. When a group has zero available updates AND its
// transient was never populated, that means the update check itself never
// ran or was blocked (e.g. an "disable updates" plugin, or the site cannot
// reach WordPress.org) — the frontend uses this to say "unable to confirm"
// instead of incorrectly implying "already up to date".
type WPInventoryUpdateChecks struct {
	Core    bool `json:"core"`
	Plugins bool `json:"plugins"`
	Themes  bool `json:"themes"`
}

type WPInventoryWordPress struct {
	Version         string `json:"version"`
	Locale          string `json:"locale"`
	Multisite       bool   `json:"multisite"`
	CurrentThemeKey string `json:"current_theme_key"`
}

type WPInventoryCounts struct {
	Plugins       int `json:"plugins"`
	ActivePlugins int `json:"active_plugins"`
	Themes        int `json:"themes"`
	PluginUpdates int `json:"plugin_updates"`
	ThemeUpdates  int `json:"theme_updates"`
}

type WPInventoryStateError struct {
	Code  string `json:"code"`
	Stage string `json:"stage"`
}

type WPInventoryTask struct {
	ID           string                `json:"id"`
	SiteID       int                   `json:"site_id"`
	Status       string                `json:"status"`
	RequestedAt  time.Time             `json:"requested_at"`
	StartedAt    *time.Time            `json:"started_at"`
	FinishedAt   *time.Time            `json:"finished_at"`
	AttemptCount int                   `json:"attempt_count"`
	Error        *WPInventoryTaskError `json:"error"`
}

type WPInventoryTaskError struct {
	Code     string `json:"code"`
	Stage    string `json:"stage"`
	TimedOut bool   `json:"timed_out"`
}

type WPInventoryRefreshResult struct {
	Task    WPInventoryTask `json:"task"`
	Created bool            `json:"created"`
}

type WPInventoryComponent struct {
	Type          string    `json:"type"`
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Active        bool      `json:"active"`
	NetworkActive bool      `json:"network_active"`
	CurrentTheme  bool      `json:"current_theme"`
	CollectedAt   time.Time `json:"collected_at"`
}

type WPInventoryUpdate struct {
	Type           string    `json:"type"`
	Key            string    `json:"key"`
	CurrentVersion string    `json:"current_version"`
	TargetVersion  string    `json:"target_version"`
	Locale         string    `json:"locale"`
	CollectedAt    time.Time `json:"collected_at"`
}
