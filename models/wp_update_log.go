package models

import "time"

// WPUpdateLogEvent 是更新任务事件时间线中的一条记录，对应 wp_update_task_events 表。
type WPUpdateLogEvent struct {
	Stage     string    `json:"stage"`
	Result    string    `json:"result"`
	ErrorCode string    `json:"error_code"`
	CreatedAt time.Time `json:"created_at"`
}

// WPUpdateLogTask 是「更新日志」卡片中展示的一条更新任务，聚合其事件时间线。
// 仅返回未结束（finished_at IS NULL）或近 24 小时内结束的任务，超期事件由清理逻辑删除。
type WPUpdateLogTask struct {
	TaskID            string             `json:"task_id"`
	ComponentType     string             `json:"component_type"`
	ComponentKey      string             `json:"component_key"`
	TaskKind          string             `json:"task_kind"`
	Status            string             `json:"status"`
	Stage             string             `json:"stage"`
	FailureStage      string             `json:"failure_stage"`
	RollbackStatus    string             `json:"rollback_status"`
	RequiresAttention bool               `json:"requires_attention"`
	CurrentVersion    string             `json:"current_version"`
	TargetVersion     string             `json:"target_version"`
	StartedAt         *time.Time         `json:"started_at"`
	FinishedAt        *time.Time         `json:"finished_at"`
	CreatedAt         time.Time          `json:"created_at"`
	Events            []WPUpdateLogEvent `json:"events"`
}
