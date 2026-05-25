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
