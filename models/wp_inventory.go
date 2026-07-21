package models

import "time"

type WPInventorySummary struct {
	SiteID                 int                    `json:"site_id"`
	CollectionStatus       string                 `json:"collection_status"`
	HasSuccessfulInventory bool                   `json:"has_successful_inventory"`
	WordPress              WPInventoryWordPress   `json:"wordpress"`
	Counts                 WPInventoryCounts      `json:"counts"`
	CoreUpgradeAvailable   bool                   `json:"core_upgrade_available"`
	LastAttemptAt          *time.Time             `json:"last_attempt_at"`
	LastSuccessAt          *time.Time             `json:"last_success_at"`
	LastError              *WPInventoryStateError `json:"last_error"`
	ActiveTask             *WPInventoryTask       `json:"active_task"`
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
