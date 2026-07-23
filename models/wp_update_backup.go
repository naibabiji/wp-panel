package models

import "time"

type WPUpdateRecentDatabaseBackup struct {
	BackupID  int64     `json:"backup_id"`
	TaskID    string    `json:"task_id"`
	FileSize  int64     `json:"file_size"`
	CreatedAt time.Time `json:"created_at"`
}

type WPUpdateBackup struct {
	BackupID          int64     `json:"backup_id"`
	TaskID            string    `json:"task_id"`
	ComponentType     string    `json:"component_type"`
	ComponentKey      string    `json:"component_key"`
	CurrentVersion    string    `json:"current_version"`
	TargetVersion     string    `json:"target_version"`
	TaskStatus        string    `json:"task_status"`
	RollbackStatus    string    `json:"rollback_status"`
	Kind              string    `json:"kind"`
	FileSize          int64     `json:"file_size"`
	CreatedAt         time.Time `json:"created_at"`
	RestoreAllowed    bool      `json:"restore_allowed"`
	RequiresAttention bool      `json:"requires_attention"`
}
