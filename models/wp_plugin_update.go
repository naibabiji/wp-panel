package models

import "time"

type WPPluginUpdatePreview struct {
	Available            bool       `json:"available"`
	SiteID               int        `json:"site_id"`
	Domain               string     `json:"domain"`
	ComponentKey         string     `json:"component_key"`
	Name                 string     `json:"name"`
	CurrentVersion       string     `json:"current_version"`
	TargetVersion        string     `json:"target_version"`
	PackageSource        string     `json:"package_source"`
	VerificationRequired string     `json:"verification_required"`
	DatabaseBackup       bool       `json:"database_backup"`
	PluginFilesBackup    bool       `json:"plugin_files_backup"`
	ConfirmationToken    string     `json:"confirmation_token,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
}

type WPPluginUpdateTask struct {
	ID                string                  `json:"task_id"`
	SiteID            int                     `json:"site_id"`
	ComponentType     string                  `json:"component_type"`
	ComponentKey      string                  `json:"component_key"`
	TaskKind          string                  `json:"task_kind"`
	Status            string                  `json:"status"`
	Stage             string                  `json:"stage"`
	FailureStage      string                  `json:"failure_stage,omitempty"`
	RollbackStatus    string                  `json:"rollback_status"`
	RequiresAttention bool                    `json:"requires_attention"`
	ManualDisposition string                  `json:"manual_disposition,omitempty"`
	CurrentVersion    string                  `json:"current_version"`
	TargetVersion     string                  `json:"target_version"`
	VerificationLevel string                  `json:"verification_level,omitempty"`
	RequestedAt       time.Time               `json:"requested_at"`
	StartedAt         *time.Time              `json:"started_at,omitempty"`
	FinishedAt        *time.Time              `json:"finished_at,omitempty"`
	Events            []WPCoreUpdateTaskEvent `json:"events,omitempty"`
}
