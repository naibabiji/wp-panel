package models

import "time"

type WPFleetOverview struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Counts      WPFleetOverviewCounts `json:"counts"`
	Sites       []WPFleetSite         `json:"sites"`
}

type WPFleetOverviewCounts struct {
	TotalSites              int `json:"total_sites"`
	WordPressSites          int `json:"wordpress_sites"`
	CriticalSites           int `json:"critical_sites"`
	WarningSites            int `json:"warning_sites"`
	UnknownSites            int `json:"unknown_sites"`
	HealthySites            int `json:"healthy_sites"`
	UpdateSites             int `json:"update_sites"`
	FailedInventorySites    int `json:"failed_inventory_sites"`
	StaleInventorySites     int `json:"stale_inventory_sites"`
	InventoryAttentionSites int `json:"inventory_attention_sites"`
	UncollectedSites        int `json:"uncollected_sites"`
}

type WPFleetSite struct {
	ID                   int               `json:"id"`
	Name                 string            `json:"name"`
	Domain               string            `json:"domain"`
	SiteType             string            `json:"site_type"`
	Status               string            `json:"status"`
	CreatedAt            time.Time         `json:"created_at"`
	ExpiresAt            *time.Time        `json:"expires_at"`
	SSLEnabled           bool              `json:"ssl_enabled"`
	SSLExpiresAt         *time.Time        `json:"ssl_expires_at"`
	SSLState             string            `json:"ssl_state"`
	MonitoringEnabled    bool              `json:"monitoring_enabled"`
	BackupEnabled        bool              `json:"backup_enabled"`
	FileLockEnabled      bool              `json:"file_lock_enabled"`
	FastCGICacheEnabled  bool              `json:"fastcgi_cache_enabled"`
	AccessLogMode        string            `json:"access_log_mode"`
	UpdateChecksDisabled bool              `json:"update_checks_disabled"`
	Health               WPFleetHealth     `json:"health"`
	Inventory            *WPFleetInventory `json:"inventory"`
}

type WPFleetInventory struct {
	Status                 string     `json:"status"`
	HasSuccessfulInventory bool       `json:"has_successful_inventory"`
	WordPressVersion       string     `json:"wordpress_version"`
	PluginUpdates          int        `json:"plugin_updates"`
	ThemeUpdates           int        `json:"theme_updates"`
	CoreUpgradeAvailable   bool       `json:"core_upgrade_available"`
	UpdateTotal            int        `json:"update_total"`
	LastAttemptAt          *time.Time `json:"last_attempt_at"`
	LastSuccessAt          *time.Time `json:"last_success_at"`
	Stale                  bool       `json:"stale"`
}

type WPFleetHealth struct {
	Level  string   `json:"level"`
	Issues []string `json:"issues"`
}
