package handlers

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

// wpUpdateLogRetention 与「更新备份」保持一致的日志保留时长；超期事件由
// executor.cleanupExpiredUpdateLogs 删除，本接口只返回该窗口内（或尚未结束）的任务。
const wpUpdateLogRetention = 24 * time.Hour

type WPUpdateLogHandler struct{}

// List 返回某站点近 24 小时内（或尚未结束）的更新任务及其完整事件时间线。
// 用于「更新日志」卡片，用户更新失败时可一键复制结构化日志反馈，免去 SSH 排查。
func (h *WPUpdateLogHandler) List(c *gin.Context) {
	siteID, ok := wpUpdateBackupSiteID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	cutoff := executor.WPUpdateDBTime(time.Now().UTC().Add(-wpUpdateLogRetention))

	taskRows, err := database.GetDB().QueryContext(ctx, `SELECT id,component_type,component_key,task_kind,status,stage,failure_stage,
		rollback_status,requires_attention,current_version,target_version,started_at,finished_at,created_at
		FROM wp_update_tasks
		WHERE site_id=? AND (finished_at IS NULL OR finished_at>=?)
		ORDER BY created_at DESC
		LIMIT 100`, siteID, cutoff)
	if err != nil {
		wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_log.load_failed")
		return
	}
	tasks := make([]models.WPUpdateLogTask, 0)
	taskIDs := make([]string, 0)
	for taskRows.Next() {
		var t models.WPUpdateLogTask
		var attention int
		var startedAt, finishedAt sql.NullTime
		if err := taskRows.Scan(&t.TaskID, &t.ComponentType, &t.ComponentKey, &t.TaskKind, &t.Status,
			&t.Stage, &t.FailureStage, &t.RollbackStatus, &attention, &t.CurrentVersion, &t.TargetVersion,
			&startedAt, &finishedAt, &t.CreatedAt); err != nil {
			taskRows.Close()
			wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_log.load_failed")
			return
		}
		t.RequiresAttention = attention == 1
		t.StartedAt = nullTimeToPointer(startedAt)
		t.FinishedAt = nullTimeToPointer(finishedAt)
		t.Events = []models.WPUpdateLogEvent{}
		tasks = append(tasks, t)
		taskIDs = append(taskIDs, t.TaskID)
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_log.load_failed")
		return
	}
	taskRows.Close()

	if len(taskIDs) > 0 {
		placeholders := wpUpdateLogPlaceholders(len(taskIDs))
		args := make([]any, 0, len(taskIDs))
		for _, id := range taskIDs {
			args = append(args, id)
		}
		eventRows, err := database.GetDB().QueryContext(ctx, `SELECT task_id,stage,result,error_code,created_at
			FROM wp_update_task_events
			WHERE task_id IN (`+placeholders+`)
			ORDER BY task_id,id`, args...)
		if err != nil {
			wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_log.load_failed")
			return
		}
		byID := make(map[string]*models.WPUpdateLogTask, len(tasks))
		for i := range tasks {
			byID[tasks[i].TaskID] = &tasks[i]
		}
		for eventRows.Next() {
			var taskID string
			var e models.WPUpdateLogEvent
			if err := eventRows.Scan(&taskID, &e.Stage, &e.Result, &e.ErrorCode, &e.CreatedAt); err != nil {
				eventRows.Close()
				wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_log.load_failed")
				return
			}
			if t := byID[taskID]; t != nil {
				t.Events = append(t.Events, e)
			}
		}
		if err := eventRows.Err(); err != nil {
			eventRows.Close()
			wpUpdateBackupError(c, http.StatusInternalServerError, "wp_update_log.load_failed")
			return
		}
		eventRows.Close()
	}

	c.JSON(http.StatusOK, models.SuccessResponse(tasks))
}

func nullTimeToPointer(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time.UTC()
	return &t
}

// wpUpdateLogPlaceholders 生成 n 个问号占位符，用于事件查询的 IN (...) 子句。
func wpUpdateLogPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
