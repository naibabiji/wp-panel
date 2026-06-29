package models

type FileSecurityEvent struct {
	ID            int     `json:"id"`
	SiteID        int     `json:"site_id"`
	Domain        string  `json:"domain"`
	EventType     string  `json:"event_type"`
	Source        string  `json:"source"`
	RiskLevel     string  `json:"risk_level"`
	Path          string  `json:"path"`
	RequestMethod string  `json:"request_method"`
	IPAddress     string  `json:"ip_address"`
	UserAgent     string  `json:"user_agent"`
	Status        int     `json:"status"`
	FileSize      int64   `json:"file_size"`
	FileMTime     *string `json:"file_mtime"`
	Message       string  `json:"message"`
	FirstSeen     string  `json:"first_seen"`
	LastSeen      string  `json:"last_seen"`
	EventCount    int     `json:"event_count"`
	ResolvedAt    *string `json:"resolved_at"`
}

type FileSecurityRefreshSummary struct {
	SitesScanned int `json:"sites_scanned"`
	FileEvents   int `json:"file_events"`
	AccessEvents int `json:"access_events"`
}
