package models

import "time"

type DBBackup struct {
	ID               int       `json:"id"`
	SiteID           int       `json:"site_id"`
	Filename         string    `json:"filename"`
	FileSize         int64     `json:"file_size"`
	DBName           string    `json:"db_name"`
	Auto             bool      `json:"auto"`
	TransportStatus  string    `json:"transport_status"`
	TransportMessage string    `json:"transport_message"`
	CreatedAt        time.Time `json:"created_at"`
}

type BackupSettings struct {
	Enabled   bool `json:"enabled"`
	KeepCount int  `json:"keep_count"`
}

type FileBackup struct {
	ID               int       `json:"id"`
	SiteID           int       `json:"site_id"`
	Filename         string    `json:"filename"`
	FileSize         int64     `json:"file_size"`
	Mode             string    `json:"mode"`
	TransportStatus  string    `json:"transport_status"`
	TransportMessage string    `json:"transport_message"`
	CreatedAt        time.Time `json:"created_at"`
}

// BackupCronJobRef 是删除网站前提醒用的关联备份计划任务简要信息。
type BackupCronJobRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// WebsiteBackupUsage 汇总一个网站当前的备份数据规模，用于删除网站前提醒管理员。
type WebsiteBackupUsage struct {
	DBBackupCount     int                `json:"db_backup_count"`
	FileBackupCount   int                `json:"file_backup_count"`
	AutoBackupEnabled bool               `json:"auto_backup_enabled"`
	CronJobs          []BackupCronJobRef `json:"cron_jobs"`
}

// HasBackupData 判断是否存在任何需要管理员关注的备份数据或配置。
func (u WebsiteBackupUsage) HasBackupData() bool {
	return u.DBBackupCount > 0 || u.FileBackupCount > 0 || u.AutoBackupEnabled || len(u.CronJobs) > 0
}
