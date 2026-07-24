package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

// WPUpdateBatch 是一次批量插件更新的分组记录：一次批量对应若干个串行派发的
// wp_update_tasks（每个任务 auto_rollback=0，batch_id 指向本记录）。
type WPUpdateBatch struct {
	ID                     string
	SiteID                 int
	CreatedBy              string
	Status                 string
	TotalCount             int
	DatabaseBackupSourceID int64
	CreatedAt              string
	UpdatedAt              string
}

// WPUpdateBatchItem 是批量中的一项（一个插件），在被派发前只是一个待处理的
// component_key；派发成功后 TaskID 指向真正的 wp_update_tasks 行，Task*/CurrentVersion/
// TargetVersion 是从该任务行冗余读取的快照，供前端不必再单独查询任务详情即可展示进度。
type WPUpdateBatchItem struct {
	ID                    int64
	BatchID               string
	Position              int
	ComponentKey          string
	Status                string
	Message               string
	TaskID                string
	TaskStatus            string
	TaskRollbackStatus    string
	TaskRequiresAttention bool
	TaskManualDisposition string
	CurrentVersion        string
	TargetVersion         string
	CreatedAt             string
	UpdatedAt             string
}

func newWPUpdateBatchID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "wpub_" + hex.EncodeToString(raw[:]), nil
}

// createPluginBatch 为一个站点创建一次批量插件更新分组，componentKeys 决定派发顺序。
// 站点当前存在阻塞任务，或者已经有一个 running 状态的批量时会失败
// （ux_wp_update_batches_active_site 唯一索引兜底）。
func (s *wpUpdateStore) createPluginBatch(ctx context.Context, siteID int, createdBy string, componentKeys []string, now time.Time) (WPUpdateBatch, error) {
	if s == nil || s.db == nil || siteID <= 0 || createdBy == "" || len(componentKeys) == 0 {
		return WPUpdateBatch{}, errors.New("invalid plugin batch request")
	}
	seen := map[string]bool{}
	for _, key := range componentKeys {
		if !validWPPluginComponentKey(key) || seen[key] {
			return WPUpdateBatch{}, errors.New("invalid plugin batch component list")
		}
		seen[key] = true
	}
	blocked, err := s.siteHasBlockingTask(ctx, siteID)
	if err != nil {
		return WPUpdateBatch{}, err
	}
	if blocked {
		return WPUpdateBatch{}, errors.New("site has blocking update task")
	}
	id, err := newWPUpdateBatchID()
	if err != nil {
		return WPUpdateBatch{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WPUpdateBatch{}, err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO wp_update_batches(id,site_id,created_by,status,total_count,created_at,updated_at)
		VALUES (?,?,?,'running',?,?,?)`, id, siteID, createdBy, len(componentKeys), stamp, stamp); err != nil {
		return WPUpdateBatch{}, err
	}
	for i, key := range componentKeys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO wp_update_batch_items(batch_id,position,component_key,status,created_at,updated_at)
			VALUES (?,?,?,'pending',?,?)`, id, i+1, key, stamp, stamp); err != nil {
			return WPUpdateBatch{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return WPUpdateBatch{}, err
	}
	return s.getBatch(ctx, id)
}

func (s *wpUpdateStore) getBatch(ctx context.Context, id string) (WPUpdateBatch, error) {
	var b WPUpdateBatch
	var backupSource sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,site_id,created_by,status,total_count,database_backup_source_id,created_at,updated_at
		FROM wp_update_batches WHERE id=?`, id).
		Scan(&b.ID, &b.SiteID, &b.CreatedBy, &b.Status, &b.TotalCount, &backupSource, &b.CreatedAt, &b.UpdatedAt)
	if backupSource.Valid {
		b.DatabaseBackupSourceID = backupSource.Int64
	}
	return b, err
}

func (s *wpUpdateStore) listBatchesForSite(ctx context.Context, siteID int) ([]WPUpdateBatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,created_by,status,total_count,created_at,updated_at
		FROM wp_update_batches WHERE site_id=? ORDER BY created_at DESC,rowid DESC LIMIT 50`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []WPUpdateBatch
	for rows.Next() {
		var b WPUpdateBatch
		if err := rows.Scan(&b.ID, &b.SiteID, &b.CreatedBy, &b.Status, &b.TotalCount, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

func (s *wpUpdateStore) listBatchItems(ctx context.Context, batchID string) ([]WPUpdateBatchItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.batch_id,i.position,i.component_key,i.status,i.message,COALESCE(i.task_id,''),
		COALESCE(t.status,''),COALESCE(t.rollback_status,''),COALESCE(t.requires_attention,0),COALESCE(t.manual_disposition,''),
		COALESCE(t.current_version,''),COALESCE(t.target_version,''),i.created_at,i.updated_at
		FROM wp_update_batch_items i LEFT JOIN wp_update_tasks t ON t.id=i.task_id
		WHERE i.batch_id=? ORDER BY i.position`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WPUpdateBatchItem
	for rows.Next() {
		var it WPUpdateBatchItem
		var attention int
		if err := rows.Scan(&it.ID, &it.BatchID, &it.Position, &it.ComponentKey, &it.Status, &it.Message, &it.TaskID,
			&it.TaskStatus, &it.TaskRollbackStatus, &attention, &it.TaskManualDisposition,
			&it.CurrentVersion, &it.TargetVersion, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		it.TaskRequiresAttention = attention != 0
		items = append(items, it)
	}
	return items, rows.Err()
}

// listRunningBatches 返回所有仍在进行中的批量，供编排器每次 tick 扫描。
func (s *wpUpdateStore) listRunningBatches(ctx context.Context) ([]WPUpdateBatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,created_by,status,total_count,database_backup_source_id,created_at,updated_at
		FROM wp_update_batches WHERE status='running' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []WPUpdateBatch
	for rows.Next() {
		var b WPUpdateBatch
		var backupSource sql.NullInt64
		if err := rows.Scan(&b.ID, &b.SiteID, &b.CreatedBy, &b.Status, &b.TotalCount, &backupSource, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		if backupSource.Valid {
			b.DatabaseBackupSourceID = backupSource.Int64
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// siteHasBlockingTask 与 create*ManualPlan 中内联的阻塞检查一致：站点当前存在
// preparing/queued/running 任务，或存在尚未处置的 interrupted_unknown 任务时视为阻塞。
func (s *wpUpdateStore) siteHasBlockingTask(ctx context.Context, siteID int) (bool, error) {
	var blocked int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wp_update_tasks WHERE site_id=? AND (
		status IN ('preparing','queued','running') OR
		(status='interrupted_unknown' AND manual_disposition=''))`, siteID).Scan(&blocked)
	return blocked != 0, err
}

// nextPendingBatchItem 取批量中位置最靠前的待派发项；没有剩余待处理项时返回 sql.ErrNoRows。
func (s *wpUpdateStore) nextPendingBatchItem(ctx context.Context, batchID string) (WPUpdateBatchItem, error) {
	var it WPUpdateBatchItem
	err := s.db.QueryRowContext(ctx, `SELECT id,batch_id,position,component_key,status,message,COALESCE(task_id,''),created_at,updated_at
		FROM wp_update_batch_items WHERE batch_id=? AND status='pending' ORDER BY position LIMIT 1`, batchID).
		Scan(&it.ID, &it.BatchID, &it.Position, &it.ComponentKey, &it.Status, &it.Message, &it.TaskID, &it.CreatedAt, &it.UpdatedAt)
	return it, err
}

func (s *wpUpdateStore) markBatchItemDispatched(ctx context.Context, itemID int64, taskID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE wp_update_batch_items SET status='dispatched',task_id=?,updated_at=?
		WHERE id=? AND status='pending'`, taskID, wpUpdateDBTime(now), itemID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("batch item is not dispatchable")
	}
	return nil
}

func (s *wpUpdateStore) markBatchItemFailed(ctx context.Context, itemID int64, message string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE wp_update_batch_items SET status='failed',message=?,updated_at=?
		WHERE id=? AND status='pending'`, message, wpUpdateDBTime(now), itemID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("batch item is not markable as failed")
	}
	return nil
}

// completeBatchIfExhausted 只有在批量里没有 pending 项、且所有已派发任务都已经走到
// 「真正终态」时才标记为 completed：
//   - failed 状态用 requires_attention 判断是否还在等待决定（回滚成功或用户忽略都会
//     把它清零；哪怕人工回滚本身失败过、又被再次忽略，disposeFailedTaskIgnored 同样
//     会清零，所以不需要再看 rollback_status）。
//   - interrupted_unknown 状态用 manual_disposition 判断（disposeInterrupted 对
//     'escalated'/'marked_failed_no_action' 这两种处置结果故意保留
//     requires_attention=1 以便在单任务的更新日志里继续高亮，所以这里不能用
//     requires_attention 判断，必须看 manual_disposition 是否已经写入）。
//
// 哪怕只是最后一项停在等待人工决定，也必须保持 running，否则批量会在用户还没来得及
// 处理时就“提前完成”，导致刷新页面/关闭进度框后再也找不到入口去做「回滚」或「忽略」。
func (s *wpUpdateStore) completeBatchIfExhausted(ctx context.Context, batchID string, now time.Time) error {
	var unresolved int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wp_update_batch_items i
		LEFT JOIN wp_update_tasks t ON t.id=i.task_id
		WHERE i.batch_id=? AND (
			i.status='pending' OR
			(i.status='dispatched' AND (
				t.id IS NULL OR
				t.status IN ('preparing','queued','running') OR
				(t.status='interrupted_unknown' AND t.manual_disposition='') OR
				(t.status='failed' AND t.requires_attention=1)
			))
		)`, batchID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE wp_update_batches SET status='completed',updated_at=? WHERE id=? AND status='running'`,
		wpUpdateDBTime(now), batchID)
	return err
}

// ensureBatchDatabaseBackupSource 在批量的共享数据库备份来源尚未确定时，从批量里已经
// 派发过的项目（按 position 顺序）中找到第一个已经就绪的数据库备份，并把它固定写回
// wp_update_batches——一旦写入就不再改变，即使这一项后来失败、被标记 requires_attention。
// 这样后续所有项都能稳定复用同一份备份，而不是像 recentDatabaseBackup 那样一旦某个任务
// 进入 requires_attention 状态就查不到、从而被迫为后续每一项都重新备份一次数据库。
// 返回 0 且 err 为 nil 表示目前还没有任何可用的备份（比如第一项还没跑到 backups_ready）。
func (s *wpUpdateStore) ensureBatchDatabaseBackupSource(ctx context.Context, batchID string, now time.Time) (int64, error) {
	var existing sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT database_backup_source_id FROM wp_update_batches WHERE id=?`, batchID).Scan(&existing); err != nil {
		return 0, err
	}
	if existing.Valid && existing.Int64 > 0 {
		return existing.Int64, nil
	}
	var backupID int64
	err := s.db.QueryRowContext(ctx, `SELECT b.id FROM wp_update_batch_items i
		JOIN wp_update_task_backups b ON b.task_id=i.task_id
		WHERE i.batch_id=? AND b.kind='database' AND b.protected=1 AND b.deleted_at IS NULL
		ORDER BY i.position LIMIT 1`, batchID).Scan(&backupID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE wp_update_batches SET database_backup_source_id=?,updated_at=?
		WHERE id=? AND database_backup_source_id IS NULL`, backupID, wpUpdateDBTime(now), batchID); err != nil {
		return 0, err
	}
	return backupID, nil
}

// validateBatchDatabaseBackup 与 validateDatabaseBackupChoice 校验同样的备份文件完整性
// （站点归属、受保护未删除、路径受控、大小与 sha256 一致），但故意不检查备份所属任务
// 当前的 requires_attention/rollback_status——批量复用的备份来源一旦由
// ensureBatchDatabaseBackupSource 固定下来，就不应该因为那一项后来失败/需要人工介入而
// 失效：备份本身在拍摄那一刻就已经是完整且正确的，与该任务后续的健康检查结果无关。
// 同样故意不做 6 小时新鲜度窗口限制：批量的持续时间不该被这个为“临时复用”设计的窗口卡住。
func (s *wpUpdateStore) validateBatchDatabaseBackup(ctx context.Context, siteID int, mode string, sourceID int64, root string) error {
	if mode == "fresh" && sourceID == 0 {
		return nil
	}
	if mode != "reuse" || sourceID <= 0 {
		return errors.New("invalid database backup choice")
	}
	var path, sha string
	var size int64
	err := s.db.QueryRowContext(ctx, `SELECT b.file_path,b.file_size,b.sha256
		FROM wp_update_task_backups b JOIN wp_update_tasks t ON t.id=b.task_id
		WHERE b.id=? AND t.site_id=? AND b.kind='database' AND b.protected=1 AND b.deleted_at IS NULL`,
		sourceID, siteID).Scan(&path, &size, &sha)
	if err != nil || size <= 0 || !wpUpdateSHA256Pattern.MatchString(sha) {
		return errors.New("database backup unavailable")
	}
	if !pathWithin(filepath.Clean(root), filepath.Clean(path), false) {
		return errors.New("database backup unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("database backup unavailable")
	}
	actual, _, err := hashRegularFile(path)
	if err != nil || actual != sha {
		return errors.New("database backup unavailable")
	}
	return nil
}

// findBatchItemExistingTask 供派发前的幂等恢复使用：如果上一次派发已经通过
// ConfirmForBatch 建出了真实任务，但紧接着的 markBatchItemDispatched 因为进程崩溃/
// 数据库错误没有跑完，这里能找到那个「孤儿」任务并回填，而不是重新创建第二个任务。
func (s *wpUpdateStore) findBatchItemExistingTask(ctx context.Context, batchID, componentKey string) (string, error) {
	var taskID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM wp_update_tasks
		WHERE batch_id=? AND component_key=? ORDER BY created_at DESC LIMIT 1`, batchID, componentKey).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return taskID, err
}

// wpPluginBatchConfirmer 是编排器派发下一项时需要的最小接口，由 WPPluginUpdateService 实现。
type wpPluginBatchConfirmer interface {
	Preview(ctx context.Context, siteID int, username, componentKey string) (models.WPPluginUpdatePreview, error)
	ConfirmForBatch(ctx context.Context, siteID int, username, componentKey, token, target, backupMode, batchID string, databaseBackupSourceID int64) (models.WPPluginUpdateTask, error)
}

// wpPluginBatchOrchestrator 挂在现有更新 worker 的 sweep 节奏上，每次 tick 为每个仍在进行
// 中的批量派发「下一项」——同一时刻每个站点只有一个活跃任务，所以批量天然是串行推进的。
type wpPluginBatchOrchestrator struct {
	store     *wpUpdateStore
	confirmer wpPluginBatchConfirmer
	now       func() time.Time
}

func newWPPluginBatchOrchestrator(store *wpUpdateStore, confirmer wpPluginBatchConfirmer) (*wpPluginBatchOrchestrator, error) {
	if store == nil || store.db == nil || confirmer == nil {
		return nil, errors.New("invalid plugin batch orchestrator")
	}
	return &wpPluginBatchOrchestrator{store: store, confirmer: confirmer, now: time.Now}, nil
}

// Tick 扫描所有 running 批量，为每个「站点当前没有阻塞任务」的批量派发一项。
// 单个批量在一次 tick 内最多派发一项，派发失败的项标记为 failed 后立即继续下一项，
// 避免一次 tick 里因为连续多个坏插件而做无界的工作量。
func (o *wpPluginBatchOrchestrator) Tick(ctx context.Context) {
	batches, err := o.store.listRunningBatches(ctx)
	if err != nil {
		log.Printf("批量更新编排器读取进行中批量失败: %v", err)
		return
	}
	for _, batch := range batches {
		if err := o.advance(ctx, batch); err != nil {
			log.Printf("批量更新编排器推进 batch=%s site=%d 失败: %v", batch.ID, batch.SiteID, err)
		}
	}
}

func (o *wpPluginBatchOrchestrator) advance(ctx context.Context, batch WPUpdateBatch) error {
	now := o.now().UTC()
	for {
		item, err := o.store.nextPendingBatchItem(ctx, batch.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return o.store.completeBatchIfExhausted(ctx, batch.ID, now)
		}
		if err != nil {
			return err
		}
		// 幂等恢复必须先于「站点是否被阻塞」判断：如果这一项此前已经通过 ConfirmForBatch
		// 建出了真实任务（只是紧接着的 markBatchItemDispatched 没跑完），那个孤儿任务本身
		// 就是站点被占用的原因——如果先判断 blocked 就直接因为它而 return，这个孤儿任务
		// 永远没有机会被发现和回填，批量会卡死在这一项上。
		if existingTaskID, err := o.store.findBatchItemExistingTask(ctx, batch.ID, item.ComponentKey); err != nil {
			return err
		} else if existingTaskID != "" {
			if err := o.store.markBatchItemDispatched(ctx, item.ID, existingTaskID, now); err != nil {
				return err
			}
			return nil
		}
		blocked, err := o.store.siteHasBlockingTask(ctx, batch.SiteID)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
		dispatched, dispatchErr := o.dispatch(ctx, batch, item, now)
		if dispatchErr != nil {
			return dispatchErr
		}
		if dispatched {
			// 已经占用了站点唯一活跃任务名额，本次 tick 到此为止。
			return nil
		}
		// 这一项在派发前就被拒绝（已标记 failed），同一 tick 内继续尝试下一项，
		// 但仍然只会占用最多一个真正的任务名额。
	}
}

// dispatch 尝试把 item 变成一个真正的 wp_update_tasks 行。调用方已经确认站点当前没有
// 阻塞任务、且这一项不存在可回填的孤儿任务。返回 true 表示成功派发（占用了站点唯一
// 活跃任务名额，本次 tick 应该停止）；返回 false 且 err 为 nil 表示该项在成为任务之前
// 就被拒绝，已标记为 failed，调用方应该继续尝试批量中的下一项。
func (o *wpPluginBatchOrchestrator) dispatch(ctx context.Context, batch WPUpdateBatch, item WPUpdateBatchItem, now time.Time) (bool, error) {
	preview, err := o.confirmer.Preview(ctx, batch.SiteID, batch.CreatedBy, item.ComponentKey)
	if err != nil || !preview.Available {
		message := "preview_unavailable"
		if err != nil {
			message = err.Error()
		}
		return false, o.store.markBatchItemFailed(ctx, item.ID, message, now)
	}
	// 批量共享的数据库备份来源一旦确定就固定不变（见 ensureBatchDatabaseBackupSource
	// 的注释），不使用 Preview 响应里那个会被 requires_attention 状态污染的
	// RecentDatabaseBackup 字段。
	backupSourceID, err := o.store.ensureBatchDatabaseBackupSource(ctx, batch.ID, now)
	if err != nil {
		return false, err
	}
	backupMode := "fresh"
	if backupSourceID > 0 {
		backupMode = "reuse"
	}
	task, err := o.confirmer.ConfirmForBatch(ctx, batch.SiteID, batch.CreatedBy, item.ComponentKey,
		preview.ConfirmationToken, preview.TargetVersion, backupMode, batch.ID, backupSourceID)
	if err != nil {
		return false, o.store.markBatchItemFailed(ctx, item.ID, "confirm_failed: "+err.Error(), now)
	}
	if err := o.store.markBatchItemDispatched(ctx, item.ID, task.ID, now); err != nil {
		return false, err
	}
	return true, nil
}
