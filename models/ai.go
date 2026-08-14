package models

import "time"

const (
	AIDiagnosisSite500      = "site_500"
	AIDiagnosisWPAdminDown  = "wp_admin_down"
	AIDiagnosisSSLFailure   = "ssl_failure"
	AIDiagnosisDBConnection = "db_connection"
	AIDiagnosisCacheIssue   = "cache_issue"
	AIDiagnosisPerformance  = "performance"
	AIDiagnosisLogAnalysis  = "log_analysis"

	AISessionPending   = "pending"
	AISessionRunning   = "running"
	AISessionCompleted = "completed"
	AISessionFailed    = "failed"
)

type AISettings struct {
	Enabled        bool      `json:"enabled"`
	Provider       string    `json:"provider"`
	BaseURL        string    `json:"base_url"`
	Model          string    `json:"model"`
	APIKey         string    `json:"api_key,omitempty"`
	APIKeyMasked   string    `json:"api_key_masked,omitempty"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type AISettingsRequest struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	APIKey         string `json:"api_key"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type AIDiagnoseRequest struct {
	Symptom string `json:"symptom"`
}

type AIDiagnosticContextRef struct {
	Type       string `json:"type"`
	ID         int    `json:"id"`
	FocusKind  string `json:"focus_kind,omitempty"`
	FocusValue string `json:"focus_value,omitempty"`
}

type AIMessageRequest struct {
	Content string `json:"content"`
}

type AITestRequest struct {
	Provider       string `json:"provider"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	APIKey         string `json:"api_key"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type AIDiagnosticReport struct {
	Summary                 string            `json:"summary"`
	RiskLevel               string            `json:"risk_level"`
	LikelyCauses            []AILikelyCause   `json:"likely_causes"`
	RecommendedActions      []AIAction        `json:"recommended_actions"`
	NeedsMoreInfo           bool              `json:"needs_more_info"`
	UserFriendlyExplanation string            `json:"user_friendly_explanation"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

type AILikelyCause struct {
	Title      string   `json:"title"`
	Confidence string   `json:"confidence"`
	Evidence   []string `json:"evidence"`
}

type AIAction struct {
	Label           string   `json:"label"`
	Description     string   `json:"description"`
	Risk            string   `json:"risk"`
	ManualSteps     []string `json:"manual_steps"`
	PanelActionHint string   `json:"panel_action_hint"`
}

type AISessionSummary struct {
	ID             int       `json:"id"`
	SiteID         int       `json:"site_id"`
	Symptom        string    `json:"symptom"`
	Status         string    `json:"status"`
	RiskLevel      string    `json:"risk_level"`
	SummaryExcerpt string    `json:"summary_excerpt"`
	ContextType    string    `json:"context_type,omitempty"`
	ContextID      int       `json:"context_id,omitempty"`
	FocusKind      string    `json:"focus_kind,omitempty"`
	FocusValue     string    `json:"focus_value,omitempty"`
	ErrorMessage   string    `json:"error_message"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AISessionDetail struct {
	ID            int                 `json:"id"`
	SiteID        int                 `json:"site_id"`
	Symptom       string              `json:"symptom"`
	Status        string              `json:"status"`
	RiskLevel     string              `json:"risk_level"`
	Summary       string              `json:"summary"`
	Report        *AIDiagnosticReport `json:"report,omitempty"`
	RawText       string              `json:"raw_text,omitempty"`
	ErrorMessage  string              `json:"error_message"`
	PromptChars   int                 `json:"prompt_chars"`
	ResponseChars int                 `json:"response_chars"`
	ContextType   string              `json:"context_type,omitempty"`
	ContextID     int                 `json:"context_id,omitempty"`
	ContextJSON   string              `json:"-"`
	FocusKind     string              `json:"focus_kind,omitempty"`
	FocusValue    string              `json:"focus_value,omitempty"`
	ToolEvents    []AIToolEvent       `json:"tool_events,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

type AIToolEvent struct {
	ID            int       `json:"id"`
	SessionID     int       `json:"session_id"`
	ToolName      string    `json:"tool_name"`
	ResultSummary string    `json:"result_summary"`
	CreatedAt     time.Time `json:"created_at"`
}

type AIMessage struct {
	ID            int       `json:"id"`
	SessionID     int       `json:"session_id"`
	Role          string    `json:"role"`
	Content       string    `json:"content"`
	PromptChars   int       `json:"prompt_chars,omitempty"`
	ResponseChars int       `json:"response_chars,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func IsValidAIDiagnosisSymptom(symptom string) bool {
	switch symptom {
	case AIDiagnosisSite500, AIDiagnosisWPAdminDown, AIDiagnosisSSLFailure,
		AIDiagnosisDBConnection, AIDiagnosisCacheIssue, AIDiagnosisPerformance, AIDiagnosisLogAnalysis:
		return true
	default:
		return false
	}
}
