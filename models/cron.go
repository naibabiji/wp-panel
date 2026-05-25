package models

import "time"

type CronJob struct {
	ID             int        `json:"id"`
	Name           string     `json:"name"`
	CronExpression string     `json:"cron_expression"`
	Command        string     `json:"command"`
	TaskType       string     `json:"task_type"`
	BackupMode     string     `json:"backup_mode"`
	KeepCount      int        `json:"keep_count"`
	NotifyFail     bool       `json:"notify_fail"`
	SiteID         *int       `json:"site_id"`
	RunAsUser      string     `json:"run_as_user"`
	Enabled        bool       `json:"enabled"`
	Running        bool       `json:"running"`
	LastRunAt      *time.Time `json:"last_run_at"`
	LastStatus     string     `json:"last_status"`
	LastOutput     string     `json:"last_output"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type CreateCronRequest struct {
	Name           string `json:"name" binding:"required"`
	CronExpression string `json:"cron_expression" binding:"required"`
	Command        string `json:"command"`
	TaskType       string `json:"task_type"`
	BackupMode     string `json:"backup_mode"`
	KeepCount      int    `json:"keep_count"`
	NotifyFail     bool   `json:"notify_fail"`
	SiteID         *int   `json:"site_id"`
	RunAsUser      string `json:"run_as_user"`
}

type UpdateCronRequest struct {
	Name           string `json:"name" binding:"required"`
	CronExpression string `json:"cron_expression" binding:"required"`
	Command        string `json:"command"`
	TaskType       string `json:"task_type"`
	BackupMode     string `json:"backup_mode"`
	KeepCount      *int   `json:"keep_count"`
	NotifyFail     *bool  `json:"notify_fail"`
	SiteID         *int   `json:"site_id"`
	RunAsUser      string `json:"run_as_user"`
	Enabled        *bool  `json:"enabled"`
}
