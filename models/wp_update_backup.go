package models

import "time"

type WPUpdateRecentDatabaseBackup struct {
	BackupID  int64     `json:"backup_id"`
	TaskID    string    `json:"task_id"`
	FileSize  int64     `json:"file_size"`
	CreatedAt time.Time `json:"created_at"`
}
