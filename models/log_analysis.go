package models

import "time"

const (
	LogAnalysisPending   = "pending"
	LogAnalysisRunning   = "running"
	LogAnalysisCompleted = "completed"
	LogAnalysisFailed    = "failed"
)

type LogAnalysisRequest struct {
	SiteID  int       `json:"site_id"`
	StartAt time.Time `json:"start_at"`
	EndAt   time.Time `json:"end_at"`
	UseAI   bool      `json:"use_ai"`
}

type LogAnalysisCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type LogAnalysisBotCount struct {
	Name         string `json:"name"`
	Verification string `json:"verification"`
	Count        int    `json:"count"`
}

type LogAnalysisFinding struct {
	Severity string   `json:"severity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Evidence []string `json:"evidence,omitempty"`
}

type LogAnalysisDetail struct {
	Kind                       string                `json:"kind"`
	Value                      string                `json:"value"`
	Total                      int                   `json:"total"`
	UniqueIPs                  int                   `json:"unique_ips"`
	UniquePaths                int                   `json:"unique_paths"`
	Page                       int                   `json:"page"`
	PageSize                   int                   `json:"page_size"`
	Lines                      []string              `json:"lines"`
	TopIPs                     []LogAnalysisIPDetail `json:"top_ips"`
	TopPaths                   []LogAnalysisCount    `json:"top_paths"`
	StatusCodes                []LogAnalysisCount    `json:"status_codes"`
	UserAgents                 []LogAnalysisCount    `json:"user_agents"`
	Methods                    []LogAnalysisCount    `json:"methods"`
	Hourly                     []LogAnalysisCount    `json:"hourly"`
	IPPathPairs                []LogAnalysisCount    `json:"ip_path_pairs"`
	SecurityEventRetentionDays int                   `json:"security_event_retention_days"`
	BanHistoryLimit            int                   `json:"ban_history_limit"`
}

type LogAnalysisIPDetail struct {
	IPAddress          string   `json:"ip_address"`
	Count              int      `json:"count"`
	CurrentlyBanned    bool     `json:"currently_banned"`
	BanReason          string   `json:"ban_reason,omitempty"`
	BanSource          string   `json:"ban_source,omitempty"`
	BanExpiresAt       string   `json:"ban_expires_at,omitempty"`
	RetainedEventCount int      `json:"retained_event_count"`
	SecurityEventTypes []string `json:"security_event_types"`
	LastSecurityEvent  string   `json:"last_security_event,omitempty"`
}

type LogAnalysisDetailAIRequest struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type LogAnalysisReport struct {
	SiteID                 int                   `json:"site_id"`
	Domain                 string                `json:"domain"`
	StartAt                time.Time             `json:"start_at"`
	EndAt                  time.Time             `json:"end_at"`
	GeneratedAt            time.Time             `json:"generated_at"`
	FilesScanned           int                   `json:"files_scanned"`
	BytesScanned           int64                 `json:"bytes_scanned"`
	LinesScanned           int                   `json:"lines_scanned"`
	LinesInRange           int                   `json:"lines_in_range"`
	Truncated              bool                  `json:"truncated"`
	AccessRequests         int                   `json:"access_requests"`
	UniqueIPs              int                   `json:"unique_ips"`
	StatusCodes            []LogAnalysisCount    `json:"status_codes"`
	HourlyRequests         []LogAnalysisCount    `json:"hourly_requests"`
	TopPaths               []LogAnalysisCount    `json:"top_paths"`
	TopIPs                 []LogAnalysisCount    `json:"top_ips"`
	Bots                   []LogAnalysisBotCount `json:"bots"`
	PHPFatalCount          int                   `json:"php_fatal_count"`
	PHPWarningCount        int                   `json:"php_warning_count"`
	NginxErrorCount        int                   `json:"nginx_error_count"`
	SlowRequestCount       int                   `json:"slow_request_count"`
	SecurityRequestCount   int                   `json:"security_request_count"`
	FakeSearchBotCount     int                   `json:"fake_search_bot_count"`
	UnverifiedBotCount     int                   `json:"unverified_bot_count"`
	VerifiedSearchCount    int                   `json:"verified_search_count"`
	Findings               []LogAnalysisFinding  `json:"findings"`
	Samples                []string              `json:"samples"`
	CrawlerRangesUpdatedAt string                `json:"crawler_ranges_updated_at,omitempty"`
}

type LogAnalysisJob struct {
	ID           int                `json:"id"`
	SiteID       int                `json:"site_id"`
	Domain       string             `json:"domain"`
	Status       string             `json:"status"`
	StartAt      time.Time          `json:"start_at"`
	EndAt        time.Time          `json:"end_at"`
	UseAI        bool               `json:"use_ai"`
	LocalReport  *LogAnalysisReport `json:"local_report,omitempty"`
	AIAnalysis   string             `json:"ai_analysis,omitempty"`
	ErrorMessage string             `json:"error_message,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}
