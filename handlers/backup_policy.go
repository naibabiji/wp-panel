package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

const backupPolicyCommand = "wp-panel file backup"

var (
	backupPolicyMu         sync.Mutex
	renderBackupPolicyCron = func() error {
		task := executor.GlobalQueue.Enqueue(executor.TaskRenderCron, nil)
		result := <-task.ResultCh
		if !result.Success {
			return errors.New(result.Message)
		}
		return nil
	}
)

type fileBackupPolicyState struct {
	Enabled        bool   `json:"enabled"`
	TaskCount      int    `json:"task_count"`
	Conflict       bool   `json:"conflict"`
	Cycle          string `json:"cycle"`
	CronExpression string `json:"cron_expression"`
}

type backupPolicySiteState struct {
	SiteID      int                   `json:"site_id"`
	Domain      string                `json:"domain"`
	Status      string                `json:"status"`
	DBEnabled   bool                  `json:"db_enabled"`
	DBKeepCount int                   `json:"db_keep_count"`
	Incremental fileBackupPolicyState `json:"incremental"`
	Full        fileBackupPolicyState `json:"full"`
}

type backupPolicySiteRequest struct {
	SiteID             int    `json:"site_id"`
	DBEnabled          bool   `json:"db_enabled"`
	DBKeepCount        int    `json:"db_keep_count"`
	IncrementalEnabled bool   `json:"incremental_enabled"`
	IncrementalCycle   string `json:"incremental_cycle"`
	IncrementalRebuild bool   `json:"incremental_rebuild"`
	FullEnabled        bool   `json:"full_enabled"`
	FullCycle          string `json:"full_cycle"`
	FullRebuild        bool   `json:"full_rebuild"`
}

type backupPolicyRequest struct {
	Sites []backupPolicySiteRequest `json:"sites"`
}

type cronJobSnapshot struct {
	ID                                                             int
	Name, CronExpression, Command, RunAsUser, TaskType, BackupMode string
	KeepCount, NotifyFail, Enabled, Running                        int
	SiteID                                                         sql.NullInt64
	LastRunAt                                                      sql.NullTime
	LastStatus, LastOutput                                         string
	CreatedAt, UpdatedAt                                           sql.NullTime
}

type backupSettingSnapshot struct {
	SiteID, Enabled, KeepCount int
	Exists                     bool
}

type backupPolicySnapshot struct {
	Settings []backupSettingSnapshot
	Jobs     []cronJobSnapshot
}

func GetBackupPolicy(c *gin.Context) {
	sites, remoteEnabled, err := loadBackupPolicy(database.GetDB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "backups.policy_load_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"sites": sites, "remote_backup_enabled": remoteEnabled}))
}

func SaveBackupPolicy(c *gin.Context) {
	var req backupPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Sites) == 0 || len(req.Sites) > 1000 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "backups.policy_invalid_request")))
		return
	}
	if err := validateBackupPolicyRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, err.Error())))
		return
	}

	backupPolicyMu.Lock()
	defer backupPolicyMu.Unlock()
	db := database.GetDB()
	snapshot, err := snapshotBackupPolicy(db, req.Sites)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "backups.policy_snapshot_failed")))
		return
	}
	changed, skipped, err := applyBackupPolicy(db, req.Sites)
	if err != nil {
		var userErr backupPolicyUserError
		if errors.As(err, &userErr) {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, err.Error())))
			return
		}
		log.Printf("保存批量备份设置失败: %v", err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "backups.policy_save_failed")))
		return
	}
	if changed {
		if err := renderBackupPolicyCron(); err != nil {
			log.Printf("生成批量备份 Cron 配置失败: %v", err)
			if restoreErr := restoreBackupPolicy(db, req.Sites, snapshot); restoreErr != nil {
				log.Printf("回滚批量备份设置失败: %v", restoreErr)
				c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "backups.policy_cron_and_rollback_failed")))
				return
			}
			if restoreCronErr := renderBackupPolicyCron(); restoreCronErr != nil {
				log.Printf("恢复原 Cron 配置失败: %v", restoreCronErr)
				c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "backups.policy_cron_restore_failed")))
				return
			}
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "backups.policy_cron_rolled_back")))
			return
		}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"changed": changed, "skipped_conflicts": skipped}))
}

type backupPolicyUserError string

func (err backupPolicyUserError) Error() string { return string(err) }

func loadBackupPolicy(db *sql.DB) ([]backupPolicySiteState, bool, error) {
	rows, err := db.Query(`SELECT w.id,w.domain,w.status,COALESCE(bs.enabled,0),COALESCE(bs.keep_count,7)
		FROM websites w LEFT JOIN backup_settings bs ON bs.site_id=w.id ORDER BY w.domain`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	sites := []backupPolicySiteState{}
	index := map[int]int{}
	for rows.Next() {
		var site backupPolicySiteState
		var enabled int
		if err := rows.Scan(&site.SiteID, &site.Domain, &site.Status, &enabled, &site.DBKeepCount); err != nil {
			return nil, false, err
		}
		site.DBEnabled = enabled == 1
		site.Incremental.Cycle = "weekly"
		site.Full.Cycle = "monthly"
		index[site.SiteID] = len(sites)
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	jobs, err := db.Query(`SELECT site_id,backup_mode,cron_expression,enabled FROM cron_jobs
		WHERE task_type='file_backup' AND site_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, false, err
	}
	defer jobs.Close()
	for jobs.Next() {
		var siteID, enabled int
		var mode, expression string
		if err := jobs.Scan(&siteID, &mode, &expression, &enabled); err != nil {
			return nil, false, err
		}
		i, ok := index[siteID]
		if !ok || (mode != "incremental" && mode != "full") {
			continue
		}
		state := &sites[i].Incremental
		if mode == "full" {
			state = &sites[i].Full
		}
		state.TaskCount++
		state.Enabled = state.Enabled || enabled == 1
		if state.TaskCount == 1 {
			state.CronExpression = expression
			state.Cycle = backupCycleFromCron(expression)
		}
		state.Conflict = state.TaskCount > 1
	}
	if err := jobs.Err(); err != nil {
		return nil, false, err
	}
	var remoteEnabled int
	_ = db.QueryRow(`SELECT enabled FROM remote_backup_settings WHERE id=1`).Scan(&remoteEnabled)
	return sites, remoteEnabled == 1, nil
}

func validateBackupPolicyRequest(req backupPolicyRequest) error {
	seen := map[int]bool{}
	for _, site := range req.Sites {
		if site.SiteID <= 0 || seen[site.SiteID] {
			return backupPolicyUserError("backups.policy_invalid_sites")
		}
		seen[site.SiteID] = true
		if site.DBKeepCount < 1 || site.DBKeepCount > 30 {
			return backupPolicyUserError("backups.policy_invalid_keep_count")
		}
		if site.IncrementalEnabled && !validBackupCycle("incremental", site.IncrementalCycle) {
			return backupPolicyUserError("backups.policy_invalid_incremental_cycle")
		}
		if site.FullEnabled && !validBackupCycle("full", site.FullCycle) {
			return backupPolicyUserError("backups.policy_invalid_full_cycle")
		}
	}
	return nil
}

func applyBackupPolicy(db *sql.DB, requests []backupPolicySiteRequest) (bool, int, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	occupied, err := occupiedBackupCronExpressions(tx)
	if err != nil {
		return false, 0, err
	}
	changed, skipped := false, 0
	for _, req := range requests {
		var domain string
		if err := tx.QueryRow(`SELECT domain FROM websites WHERE id=?`, req.SiteID).Scan(&domain); err != nil {
			return false, 0, backupPolicyUserError("backups.policy_site_changed")
		}
		enabled := 0
		if req.DBEnabled {
			enabled = 1
		}
		result, err := tx.Exec(`INSERT INTO backup_settings(site_id,enabled,keep_count) VALUES(?,?,?)
			ON CONFLICT(site_id) DO UPDATE SET enabled=excluded.enabled,keep_count=excluded.keep_count
			WHERE enabled!=excluded.enabled OR keep_count!=excluded.keep_count`, req.SiteID, enabled, req.DBKeepCount)
		if err != nil {
			return false, 0, err
		}
		if n, _ := result.RowsAffected(); n > 0 {
			changed = true
		}

		modeRequests := []struct {
			mode, cycle      string
			enabled, rebuild bool
		}{{"incremental", req.IncrementalCycle, req.IncrementalEnabled, req.IncrementalRebuild}, {"full", req.FullCycle, req.FullEnabled, req.FullRebuild}}
		for _, item := range modeRequests {
			tasks, err := fileBackupTasks(tx, req.SiteID, item.mode)
			if err != nil {
				return false, 0, err
			}
			if len(tasks) > 1 && !item.rebuild {
				skipped++
				continue
			}
			if !item.enabled {
				if len(tasks) > 0 {
					if _, err := tx.Exec(`DELETE FROM cron_jobs WHERE site_id=? AND task_type='file_backup' AND backup_mode=?`, req.SiteID, item.mode); err != nil {
						return false, 0, err
					}
					changed = true
				}
				continue
			}
			if len(tasks) > 1 {
				if _, err := tx.Exec(`DELETE FROM cron_jobs WHERE site_id=? AND task_type='file_backup' AND backup_mode=?`, req.SiteID, item.mode); err != nil {
					return false, 0, err
				}
				tasks = nil
				changed = true
			}
			if len(tasks) == 1 {
				currentCycle := backupCycleFromCron(tasks[0].CronExpression)
				newExpression := tasks[0].CronExpression
				if item.cycle != "custom" && item.cycle != currentCycle {
					expression, err := allocateBackupCron(item.cycle, occupied)
					if err != nil {
						return false, 0, err
					}
					delete(occupied, tasks[0].CronExpression)
					occupied[expression] = true
					newExpression = expression
				}
				result, err := tx.Exec(`UPDATE cron_jobs SET cron_expression=?,enabled=1,updated_at=CURRENT_TIMESTAMP
					WHERE id=? AND (cron_expression!=? OR enabled!=1)`, newExpression, tasks[0].ID, newExpression)
				if err != nil {
					return false, 0, err
				}
				if n, _ := result.RowsAffected(); n > 0 {
					changed = true
				}
				continue
			}
			expression, err := allocateBackupCron(item.cycle, occupied)
			if err != nil {
				return false, 0, err
			}
			occupied[expression] = true
			name := fmt.Sprintf("[file_backup:%s] %s", item.mode, domain)
			if _, err := tx.Exec(`INSERT INTO cron_jobs(name,cron_expression,command,task_type,backup_mode,keep_count,notify_fail,site_id,run_as_user,enabled)
				VALUES(?,?,?,'file_backup',?,3,1,?,'',1)`, name, expression, backupPolicyCommand, item.mode, req.SiteID); err != nil {
				return false, 0, err
			}
			changed = true
		}
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return changed, skipped, nil
}

func fileBackupTaskExists(db interface{ QueryRow(string, ...any) *sql.Row }, siteID int, mode string, excludeID int) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cron_jobs WHERE site_id=? AND task_type='file_backup' AND backup_mode=? AND id!=?`, siteID, mode, excludeID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func fileBackupTasks(tx *sql.Tx, siteID int, mode string) ([]cronJobSnapshot, error) {
	rows, err := tx.Query(`SELECT id,cron_expression FROM cron_jobs WHERE site_id=? AND task_type='file_backup' AND backup_mode=? ORDER BY id`, siteID, mode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []cronJobSnapshot
	for rows.Next() {
		var task cronJobSnapshot
		if err := rows.Scan(&task.ID, &task.CronExpression); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func validBackupCycle(mode, cycle string) bool {
	if cycle == "custom" {
		return true
	}
	if mode == "full" {
		return cycle == "monthly" || cycle == "quarterly"
	}
	return cycle == "daily" || cycle == "weekly" || cycle == "fortnightly" || cycle == "monthly"
}

func backupCycleFromCron(expression string) string {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return "custom"
	}
	minute, minuteErr := strconv.Atoi(fields[0])
	hour, hourErr := strconv.Atoi(fields[1])
	if minuteErr != nil || hourErr != nil || minute < 0 || minute > 59 || hour < 0 || hour > 23 {
		return "custom"
	}
	if fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
		return "daily"
	}
	if fields[2] == "*" && fields[3] == "*" {
		if day, err := strconv.Atoi(fields[4]); err == nil && day >= 0 && day <= 7 {
			return "weekly"
		}
	}
	if fields[3] == "*" && fields[4] == "*" {
		parts := strings.Split(fields[2], ",")
		if len(parts) == 2 {
			first, err1 := strconv.Atoi(parts[0])
			second, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil && first >= 1 && second == first+15 && second <= 31 {
				return "fortnightly"
			}
		}
		if day, err := strconv.Atoi(fields[2]); err == nil && day >= 1 && day <= 31 {
			return "monthly"
		}
	}
	if fields[3] == "1,4,7,10" && fields[4] == "*" {
		if day, err := strconv.Atoi(fields[2]); err == nil && day >= 1 && day <= 31 {
			return "quarterly"
		}
	}
	return "custom"
}

func occupiedBackupCronExpressions(tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.Query(`SELECT cron_expression FROM cron_jobs WHERE task_type='file_backup' AND enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	occupied := map[string]bool{}
	for rows.Next() {
		var expression string
		if err := rows.Scan(&expression); err != nil {
			return nil, err
		}
		occupied[expression] = true
	}
	return occupied, rows.Err()
}

func allocateBackupCron(cycle string, occupied map[string]bool) (string, error) {
	hours := []int{0, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23}
	for slot := 0; slot < 672; slot++ {
		hour := hours[slot%len(hours)]
		dayOffset := slot / len(hours)
		var expression string
		switch cycle {
		case "daily":
			minute := dayOffset
			if minute > 59 {
				return "", backupPolicyUserError("backups.policy_daily_slots_exhausted")
			}
			expression = fmt.Sprintf("%d %d * * *", minute, hour)
		case "weekly":
			expression = fmt.Sprintf("0 %d * * %d", hour, dayOffset%7)
		case "fortnightly":
			day := 1 + dayOffset
			if day > 15 {
				return "", backupPolicyUserError("backups.policy_fortnightly_slots_exhausted")
			}
			expression = fmt.Sprintf("0 %d %d,%d * *", hour, day, day+15)
		case "monthly":
			day := 1 + dayOffset
			if day > 28 {
				return "", backupPolicyUserError("backups.policy_monthly_slots_exhausted")
			}
			expression = fmt.Sprintf("0 %d %d * *", hour, day)
		case "quarterly":
			day := 1 + dayOffset
			if day > 28 {
				return "", backupPolicyUserError("backups.policy_quarterly_slots_exhausted")
			}
			expression = fmt.Sprintf("0 %d %d 1,4,7,10 *", hour, day)
		default:
			return "", backupPolicyUserError("backups.policy_invalid_file_cycle")
		}
		if !occupied[expression] {
			return expression, nil
		}
	}
	return "", backupPolicyUserError("backups.policy_slots_exhausted")
}

func snapshotBackupPolicy(db *sql.DB, requests []backupPolicySiteRequest) (backupPolicySnapshot, error) {
	ids := make([]int, 0, len(requests))
	for _, req := range requests {
		ids = append(ids, req.SiteID)
	}
	sort.Ints(ids)
	snapshot := backupPolicySnapshot{}
	for _, id := range ids {
		setting := backupSettingSnapshot{SiteID: id}
		err := db.QueryRow(`SELECT enabled,keep_count FROM backup_settings WHERE site_id=?`, id).Scan(&setting.Enabled, &setting.KeepCount)
		if err != nil && err != sql.ErrNoRows {
			return snapshot, err
		}
		setting.Exists = err == nil
		snapshot.Settings = append(snapshot.Settings, setting)
		rows, err := db.Query(`SELECT id,name,cron_expression,command,site_id,run_as_user,task_type,backup_mode,keep_count,notify_fail,enabled,running,last_run_at,last_status,last_output,created_at,updated_at
			FROM cron_jobs WHERE site_id=? AND task_type='file_backup' ORDER BY id`, id)
		if err != nil {
			return snapshot, err
		}
		for rows.Next() {
			var job cronJobSnapshot
			if err := rows.Scan(&job.ID, &job.Name, &job.CronExpression, &job.Command, &job.SiteID, &job.RunAsUser, &job.TaskType, &job.BackupMode, &job.KeepCount, &job.NotifyFail, &job.Enabled, &job.Running, &job.LastRunAt, &job.LastStatus, &job.LastOutput, &job.CreatedAt, &job.UpdatedAt); err != nil {
				rows.Close()
				return snapshot, err
			}
			snapshot.Jobs = append(snapshot.Jobs, job)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return snapshot, err
		}
		rows.Close()
	}
	return snapshot, nil
}

func restoreBackupPolicy(db *sql.DB, requests []backupPolicySiteRequest, snapshot backupPolicySnapshot) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, req := range requests {
		if _, err := tx.Exec(`DELETE FROM backup_settings WHERE site_id=?`, req.SiteID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM cron_jobs WHERE site_id=? AND task_type='file_backup'`, req.SiteID); err != nil {
			return err
		}
	}
	for _, setting := range snapshot.Settings {
		if setting.Exists {
			if _, err := tx.Exec(`INSERT INTO backup_settings(site_id,enabled,keep_count) VALUES(?,?,?)`, setting.SiteID, setting.Enabled, setting.KeepCount); err != nil {
				return err
			}
		}
	}
	for _, job := range snapshot.Jobs {
		if _, err := tx.Exec(`INSERT INTO cron_jobs(id,name,cron_expression,command,site_id,run_as_user,task_type,backup_mode,keep_count,notify_fail,enabled,running,last_run_at,last_status,last_output,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, job.ID, job.Name, job.CronExpression, job.Command, job.SiteID, job.RunAsUser, job.TaskType, job.BackupMode, job.KeepCount, job.NotifyFail, job.Enabled, job.Running, job.LastRunAt, job.LastStatus, job.LastOutput, job.CreatedAt, job.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
