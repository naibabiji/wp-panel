package executor

import (
	"github.com/naibabiji/wp-panel/models"
	"time"
)

type TaskType string

const (
	TaskCreateSite       TaskType = "create_site"
	TaskDeleteSite       TaskType = "delete_site"
	TaskPauseSite        TaskType = "pause_site"
	TaskEnableSite       TaskType = "enable_site"
	TaskUpgradeTemplate  TaskType = "upgrade_template"
	TaskRefreshWhitelist TaskType = "refresh_whitelist"
	TaskBanIP            TaskType = "ban_ip"
	TaskUnbanIP          TaskType = "unban_ip"
	TaskEnableSSL        TaskType = "enable_ssl"
	TaskRemoveSSL        TaskType = "remove_ssl"
	TaskChangeDBPassword TaskType = "change_db_password"
	TaskUpdateDomains     TaskType = "update_domains"
	TaskSaveNginxCustom    TaskType = "save_nginx_custom"
	TaskSetAccessLogMode   TaskType = "set_access_log_mode"
	TaskRenewSSL           TaskType = "renew_ssl"
	TaskRenderCron       TaskType = "render_cron"
	TaskRunCron          TaskType = "run_cron"
	TaskManualBan        TaskType = "manual_ban"
	TaskCreateBackup     TaskType = "create_backup"
	TaskRestoreBackup    TaskType = "restore_backup"
)

type TaskStatus string

const (
	TaskStatusWaiting  TaskStatus = "waiting"
	TaskStatusRunning  TaskStatus = "running"
	TaskStatusSuccess  TaskStatus = "success"
	TaskStatusFailed   TaskStatus = "failed"
)

type Task struct {
	ID        string
	Type      TaskType
	Payload   interface{}
	Status    TaskStatus
	CreatedAt time.Time
	ResultCh  chan TaskResult
}

type TaskResult struct {
	Success bool
	Message string
	Data    interface{}
}

type CreateSitePayload struct {
	Domain     string
	Aliases    []string
	SSLEnabled bool
	DBPassword string
	ExpiresAt  string
	SiteType   string
}

type DeleteSitePayload struct {
	Site *models.Website
}

type PauseSitePayload struct {
	Site *models.Website
}

type EnableSitePayload struct {
	Site *models.Website
}

type EnableSSLPayload struct {
	Site        *models.Website `json:"-"`
	Mode        string          `json:"mode"`
	Certificate string          `json:"certificate"`
	PrivateKey  string          `json:"private_key"`
}

type RemoveSSLPayload struct {
	Site *models.Website `json:"-"`
}

type ChangeDBPasswordPayload struct {
	Site        *models.Website `json:"-"`
	NewPassword string          `json:"new_password"`
}

type UpdateDomainsPayload struct {
	Site      *models.Website `json:"-"`
	NewDomain string          `json:"new_domain"`
	Aliases   []string        `json:"aliases"`
}

type SaveNginxCustomPayload struct {
	Site       *models.Website `json:"-"`
	PreContent string          `json:"pre_content"`
	Content    string          `json:"content"`
}

type SetAccessLogModePayload struct {
	Site    *models.Website `json:"-"`
	Mode   string          `json:"mode"`
}

type RunCronPayload struct {
	JobID int `json:"job_id"`
}

type ManualBanPayload struct {
	IP       string `json:"ip"`
	Duration int    `json:"duration"`
}

type CreateBackupPayload struct {
	Site *models.Website `json:"-"`
	Auto bool            `json:"auto"`
}

type RestoreBackupPayload struct {
	Site     *models.Website `json:"-"`
	Filename string          `json:"filename"`
	FilePath string          `json:"file_path"`
}
