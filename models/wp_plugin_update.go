package models

import "time"

type WPPluginUpdatePreview struct {
	Available            bool                          `json:"available"`
	SiteID               int                           `json:"site_id"`
	Domain               string                        `json:"domain"`
	ComponentKey         string                        `json:"component_key"`
	Name                 string                        `json:"name"`
	CurrentVersion       string                        `json:"current_version"`
	TargetVersion        string                        `json:"target_version"`
	PackageSource        string                        `json:"package_source"`
	VerificationRequired string                        `json:"verification_required"`
	DatabaseBackup       bool                          `json:"database_backup"`
	RecentDatabaseBackup *WPUpdateRecentDatabaseBackup `json:"recent_database_backup,omitempty"`
	PluginFilesBackup    bool                          `json:"plugin_files_backup"`
	ConfirmationToken    string                        `json:"confirmation_token,omitempty"`
	ExpiresAt            *time.Time                    `json:"expires_at,omitempty"`
}

type WPPluginUpdateTask struct {
	ID                 string                  `json:"task_id"`
	SiteID             int                     `json:"site_id"`
	ComponentType      string                  `json:"component_type"`
	ComponentKey       string                  `json:"component_key"`
	TaskKind           string                  `json:"task_kind"`
	Status             string                  `json:"status"`
	Stage              string                  `json:"stage"`
	FailureStage       string                  `json:"failure_stage,omitempty"`
	RollbackStatus     string                  `json:"rollback_status"`
	RequiresAttention  bool                    `json:"requires_attention"`
	ManualDisposition  string                  `json:"manual_disposition,omitempty"`
	CurrentVersion     string                  `json:"current_version"`
	TargetVersion      string                  `json:"target_version"`
	VerificationLevel  string                  `json:"verification_level,omitempty"`
	DatabaseBackupMode string                  `json:"database_backup_mode"`
	AutoRollback       bool                    `json:"auto_rollback"`
	BatchID            string                  `json:"batch_id,omitempty"`
	RequestedAt        time.Time               `json:"requested_at"`
	StartedAt          *time.Time              `json:"started_at,omitempty"`
	FinishedAt         *time.Time              `json:"finished_at,omitempty"`
	Events             []WPCoreUpdateTaskEvent `json:"events,omitempty"`
}

// WPPluginBatchItem 是批量更新中的一项（一个插件）。TaskStatus/TaskRollbackStatus/
// TaskRequiresAttention/CurrentVersion/TargetVersion 只在 TaskID 非空（已派发）时有意义。
type WPPluginBatchItem struct {
	Position              int    `json:"position"`
	ComponentKey          string `json:"component_key"`
	Status                string `json:"status"`
	Message               string `json:"message,omitempty"`
	TaskID                string `json:"task_id,omitempty"`
	TaskStatus            string `json:"task_status,omitempty"`
	TaskRollbackStatus    string `json:"task_rollback_status,omitempty"`
	TaskRequiresAttention bool   `json:"task_requires_attention,omitempty"`
	TaskManualDisposition string `json:"task_manual_disposition,omitempty"`
	CurrentVersion        string `json:"current_version,omitempty"`
	TargetVersion         string `json:"target_version,omitempty"`
}

// WPPluginBatch 是一次批量插件更新，Items 按 position 顺序排列。
type WPPluginBatch struct {
	ID         string              `json:"batch_id"`
	SiteID     int                 `json:"site_id"`
	Status     string              `json:"status"`
	TotalCount int                 `json:"total_count"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Items      []WPPluginBatchItem `json:"items,omitempty"`
}
