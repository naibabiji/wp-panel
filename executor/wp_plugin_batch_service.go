package executor

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

// WPPluginBatchService 是批量插件更新的 HTTP 层入口：创建批量、查询批量/单项状态、
// 对失败挂起的项执行「回滚」或「忽略」。真正的派发（Preview+Confirm 下一项）由
// wpPluginBatchOrchestrator 在后台 worker 里完成，本服务不参与派发。
type WPPluginBatchService struct {
	store    *wpUpdateStore
	executor *wpPluginUpdateExecutor
	now      func() time.Time
}

func NewWPPluginBatchService(db *sql.DB, backupDir, wwwRoot string) (*WPPluginBatchService, error) {
	if db == nil || !validWPCoreUpdateRoot(backupDir) || !filepath.IsAbs(wwwRoot) {
		return nil, ErrWPPluginUpdateInvalid
	}
	store := newWPUpdateStore(db)
	root := filepath.Join(filepath.Clean(backupDir), "wp-updates")
	pluginOps, err := newDefaultWPPluginSystemOperations(store, filepath.Clean(wwwRoot))
	if err != nil {
		return nil, ErrWPPluginUpdateUnavailable
	}
	executor, err := newWPPluginUpdateExecutor(store, root, pluginOps)
	if err != nil {
		return nil, ErrWPPluginUpdateUnavailable
	}
	return &WPPluginBatchService{store: store, executor: executor, now: time.Now}, nil
}

// Create 校验站点当前没有阻塞任务后，创建一个批量插件更新分组；实际派发由后台编排器完成。
func (s *WPPluginBatchService) Create(ctx context.Context, siteID int, username string, componentKeys []string) (models.WPPluginBatch, error) {
	if s == nil || siteID <= 0 || username == "" || len(componentKeys) == 0 {
		return models.WPPluginBatch{}, ErrWPPluginUpdateInvalid
	}
	batch, err := s.store.createPluginBatch(ctx, siteID, username, componentKeys, s.now().UTC())
	if err != nil {
		return models.WPPluginBatch{}, ErrWPPluginUpdateConflict
	}
	return s.batchModel(ctx, batch, true)
}

// Get 返回一个批量及其所有项的当前状态，用于前端轮询展示进度。
func (s *WPPluginBatchService) Get(ctx context.Context, siteID int, batchID string) (models.WPPluginBatch, error) {
	if s == nil || siteID <= 0 || batchID == "" {
		return models.WPPluginBatch{}, ErrWPPluginUpdateInvalid
	}
	batch, err := s.store.getBatch(ctx, batchID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && batch.SiteID != siteID {
		return models.WPPluginBatch{}, ErrWPPluginUpdateNotFound
	}
	if err != nil {
		return models.WPPluginBatch{}, err
	}
	return s.batchModel(ctx, batch, true)
}

// ListForSite 返回一个站点最近的批量历史（不含每项明细），用于列表页展示。
func (s *WPPluginBatchService) ListForSite(ctx context.Context, siteID int) ([]models.WPPluginBatch, error) {
	if s == nil || siteID <= 0 {
		return nil, ErrWPPluginUpdateInvalid
	}
	batches, err := s.store.listBatchesForSite(ctx, siteID)
	if err != nil {
		return nil, err
	}
	result := make([]models.WPPluginBatch, 0, len(batches))
	for _, b := range batches {
		model, err := s.batchModel(ctx, b, false)
		if err != nil {
			return nil, err
		}
		result = append(result, model)
	}
	return result, nil
}

// Rollback 对批量中一个失败挂起（status='failed', requires_attention=1）的任务执行用户
// 触发的回滚，哪怕上一次回滚本身失败过（rollback_status='failed'）也允许重试。
// interrupted_unknown 状态的任务不允许回滚——执行结果本身不确定，贸然恢复文件/数据库
// 可能覆盖掉一次其实已经成功的更新，只能通过 Ignore acknowledgment 掉。
func (s *WPPluginBatchService) Rollback(ctx context.Context, siteID int, taskID string) error {
	task, err := s.loadBatchTaskForDecision(ctx, siteID, taskID)
	if err != nil {
		return err
	}
	if task.Status != wpUpdateFailed || !task.RequiresAttention {
		return ErrWPPluginUpdateConflict
	}
	if err := s.executor.ManualRollback(ctx, task.ID); err != nil {
		return ErrWPPluginUpdateConflict
	}
	return nil
}

// Ignore 让用户对批量中一个卡住的任务选择「忽略，我自己去后台检查」，覆盖两种情况：
//   - status='failed' 且 requires_attention=1：更新失败挂起，或人工回滚本身也失败了。
//   - status='interrupted_unknown' 且 manual_disposition 为空字符串：runner 执行结果不确定
//     （比如进程被杀但无法确认是否已经开始/完成写入）。这类任务本来就会被
//     siteHasBlockingTask 当作阻塞任务，批量必须有办法处置掉它，否则会卡住整个批量
//     后续的插件，违反"某一项失败不阻塞其它项"的设计目标。
func (s *WPPluginBatchService) Ignore(ctx context.Context, siteID int, taskID string) error {
	task, err := s.loadBatchTaskForDecision(ctx, siteID, taskID)
	if err != nil {
		return err
	}
	switch {
	case task.Status == wpUpdateFailed && task.RequiresAttention:
		if err := s.store.disposeFailedTaskIgnored(ctx, task.ID, s.now().UTC()); err != nil {
			return ErrWPPluginUpdateConflict
		}
	case task.Status == wpUpdateInterrupted && task.ManualDisposition == "":
		if err := s.store.disposeInterrupted(ctx, task.ID, "escalated", s.now().UTC()); err != nil {
			return ErrWPPluginUpdateConflict
		}
	default:
		return ErrWPPluginUpdateConflict
	}
	return nil
}

func (s *WPPluginBatchService) loadBatchTaskForDecision(ctx context.Context, siteID int, taskID string) (WPUpdateTask, error) {
	if s == nil || siteID <= 0 || !wpUpdateTaskIDPattern.MatchString(taskID) {
		return WPUpdateTask{}, ErrWPPluginUpdateInvalid
	}
	task, err := s.store.getTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (task.SiteID != siteID || task.BatchID == "" ||
		task.TaskKind != "update" || task.ComponentType != "plugin") {
		return WPUpdateTask{}, ErrWPPluginUpdateNotFound
	}
	if err != nil {
		return WPUpdateTask{}, err
	}
	if task.Status != wpUpdateFailed && task.Status != wpUpdateInterrupted {
		return WPUpdateTask{}, ErrWPPluginUpdateConflict
	}
	return task, nil
}

func (s *WPPluginBatchService) batchModel(ctx context.Context, batch WPUpdateBatch, includeItems bool) (models.WPPluginBatch, error) {
	created, err := parseRequiredWPInventoryTime(batch.CreatedAt)
	if err != nil {
		return models.WPPluginBatch{}, err
	}
	updated, err := parseRequiredWPInventoryTime(batch.UpdatedAt)
	if err != nil {
		return models.WPPluginBatch{}, err
	}
	model := models.WPPluginBatch{
		ID: batch.ID, SiteID: batch.SiteID, Status: batch.Status, TotalCount: batch.TotalCount,
		CreatedAt: created, UpdatedAt: updated,
	}
	if !includeItems {
		return model, nil
	}
	items, err := s.store.listBatchItems(ctx, batch.ID)
	if err != nil {
		return models.WPPluginBatch{}, err
	}
	for _, it := range items {
		model.Items = append(model.Items, models.WPPluginBatchItem{
			Position: it.Position, ComponentKey: it.ComponentKey, Status: it.Status, Message: it.Message, TaskID: it.TaskID,
			TaskStatus: it.TaskStatus, TaskRollbackStatus: it.TaskRollbackStatus, TaskRequiresAttention: it.TaskRequiresAttention,
			TaskManualDisposition: it.TaskManualDisposition, CurrentVersion: it.CurrentVersion, TargetVersion: it.TargetVersion,
		})
	}
	return model, nil
}
