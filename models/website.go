package models

import "time"

type WebsiteStatus string

const (
	StatusActive   WebsiteStatus = "active"
	StatusPaused   WebsiteStatus = "paused"
	StatusError    WebsiteStatus = "error"
	StatusCreating WebsiteStatus = "creating"
	StatusDeleting WebsiteStatus = "deleting"
)

type Website struct {
	ID              int           `json:"id"`
	Name            string        `json:"name"`
	Domain          string        `json:"domain"`
	Aliases         string        `json:"aliases"`
	Status          WebsiteStatus `json:"status"`
	SystemUser      string        `json:"system_user"`
	WebRoot         string        `json:"web_root"`
	LogDir          string        `json:"log_dir"`
	DBName          string        `json:"db_name"`
	DBUser          string        `json:"db_user"`
	PHPPoolPath     string        `json:"php_pool_path"`
	NginxConfPath   string        `json:"nginx_conf_path"`
	SiteType        string        `json:"site_type"`
	SSLEnabled      bool          `json:"ssl_enabled"`
	SSLCertPath     string        `json:"ssl_cert_path"`
	SSLKeyPath      string        `json:"ssl_key_path"`
	TemplateVersion string        `json:"template_version"`
	AccessLogMode   string        `json:"access_log_mode"`
	SSLExpiresAt      *time.Time    `json:"ssl_expires_at"`
	FCacheEnabled     bool          `json:"fastcgi_cache_enabled"`
	FCacheTTL         int           `json:"fastcgi_cache_ttl"`
	FCacheKey            string        `json:"fastcgi_cache_key"`
	MonitoringEnabled   bool          `json:"monitoring_enabled"`
	MonitoringInterval  int           `json:"monitoring_interval"`
	ExpiresAt           *time.Time    `json:"expires_at"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

type CreateWebsiteRequest struct {
	Domain     string   `json:"domain" binding:"required"`
	Aliases    []string `json:"aliases"`
	SSLEnabled bool     `json:"ssl_enabled"`
	DBPassword string   `json:"db_password"`
	ExpiresAt  string   `json:"expires_at"`
	SiteType   string   `json:"site_type"`
}

type UpdateWebsiteStatusRequest struct {
	Action string `json:"action" binding:"required,oneof=pause enable"`
}
