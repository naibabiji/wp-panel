package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	wpUpdatePreparing   = "preparing"
	wpUpdateQueued      = "queued"
	wpUpdateRunning     = "running"
	wpUpdateSuccess     = "success"
	wpUpdateFailed      = "failed"
	wpUpdateInterrupted = "interrupted_unknown"
	wpUpdateLease       = 10 * time.Minute
)

var wpUpdateSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type WPUpdateTask struct {
	ID                  string
	SiteID              int
	ComponentType       string
	ComponentKey        string
	TaskKind            string
	ParentTaskID        string
	TriggerType         string
	Status              string
	Stage               string
	FailureStage        string
	RollbackStatus      string
	RequiresAttention   bool
	ManualDisposition   string
	CurrentVersion      string
	TargetVersion       string
	PackageSource       string
	DownloadURL         string
	DownloadedSHA256    string
	VerificationLevel   string
	PackageSnapshotPath string
	BackupReady         bool
	PlanSealedAt        string
	LeaseOwner          string
	LeaseExpiresAt      string
	RequestedAt         string
	StartedAt           string
	FinishedAt          string
}

type WPUpdatePlan struct {
	SiteID         int
	CurrentVersion string
	TargetVersion  string
	PackageSource  string
	DownloadURL    string
}

type wpUpdateStore struct{ db *sql.DB }

func newWPUpdateStore(db *sql.DB) *wpUpdateStore { return &wpUpdateStore{db: db} }

func (s *wpUpdateStore) createCoreManualPlan(ctx context.Context, plan WPUpdatePlan, now time.Time) (WPUpdateTask, error) {
	if s == nil || s.db == nil || plan.SiteID <= 0 || strings.TrimSpace(plan.CurrentVersion) == "" ||
		strings.TrimSpace(plan.TargetVersion) == "" || plan.CurrentVersion == plan.TargetVersion ||
		plan.PackageSource != "wordpress.org" || !validWPUpdateDownloadURL(plan.DownloadURL) {
		return WPUpdateTask{}, errors.New("invalid update plan")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WPUpdateTask{}, err
	}
	defer tx.Rollback()
	var siteType, siteStatus, inventoryStatus, inventoryVersion string
	var multisite int
	err = tx.QueryRowContext(ctx, `SELECT w.site_type, w.status,
		COALESCE(i.status,''), COALESCE(i.wordpress_version,''), COALESCE(i.is_multisite,0)
		FROM websites w LEFT JOIN site_wp_inventory_state i ON i.site_id=w.id WHERE w.id=?`, plan.SiteID).
		Scan(&siteType, &siteStatus, &inventoryStatus, &inventoryVersion, &multisite)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if siteType != "wordpress" || siteStatus != "active" || inventoryStatus != "complete" || multisite != 0 || inventoryVersion != plan.CurrentVersion {
		return WPUpdateTask{}, errors.New("site is not eligible for core update")
	}
	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wp_update_tasks WHERE site_id=? AND (
		status IN ('preparing','queued','running') OR
		(status='interrupted_unknown' AND manual_disposition=''))`, plan.SiteID).Scan(&blocked); err != nil {
		return WPUpdateTask{}, err
	}
	if blocked != 0 {
		return WPUpdateTask{}, errors.New("site has blocking update task")
	}
	id, err := newWPUpdateTaskID()
	if err != nil {
		return WPUpdateTask{}, err
	}
	stamp := wpUpdateDBTime(now)
	_, err = tx.ExecContext(ctx, `INSERT INTO wp_update_tasks
		(id,site_id,component_type,component_key,task_kind,trigger_type,status,stage,
		 current_version,target_version,package_source,download_url,requested_at,created_at,updated_at)
		VALUES (?,?,'core','core','update','manual','preparing','plan',?,?,?,?,?,?,?)`,
		id, plan.SiteID, plan.CurrentVersion, plan.TargetVersion, plan.PackageSource, plan.DownloadURL, stamp, stamp, stamp)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "plan", "info", "", stamp); err != nil {
		return WPUpdateTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return WPUpdateTask{}, err
	}
	return s.getTask(ctx, id)
}

func (s *wpUpdateStore) sealPlan(ctx context.Context, id, sha256, verification, snapshotPath string, now time.Time) (WPUpdateTask, error) {
	if !wpUpdateSHA256Pattern.MatchString(sha256) || (verification != "structure_only" && verification != "official_verified") || !filepath.IsAbs(snapshotPath) {
		return WPUpdateTask{}, errors.New("invalid sealed plan")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WPUpdateTask{}, err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET downloaded_sha256=?, verification_level=?,
		package_snapshot_path=?, plan_sealed_at=?, status='queued', stage='queued', updated_at=?
		WHERE id=? AND task_kind='update' AND status='preparing' AND plan_sealed_at IS NULL`,
		sha256, verification, filepath.Clean(snapshotPath), stamp, stamp, id)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return WPUpdateTask{}, errors.New("update plan is not sealable")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "queued", "success", "", stamp); err != nil {
		return WPUpdateTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return WPUpdateTask{}, err
	}
	return s.getTask(ctx, id)
}

func (s *wpUpdateStore) claimCoreUpdate(ctx context.Context, id, owner, observedVersion string, now time.Time) (WPUpdateTask, error) {
	if owner == "" || observedVersion == "" {
		return WPUpdateTask{}, errors.New("invalid claim")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WPUpdateTask{}, err
	}
	defer tx.Rollback()
	var status, currentVersion, sha, verification, snapshot, siteType, siteStatus string
	err = tx.QueryRowContext(ctx, `SELECT t.status,t.current_version,t.downloaded_sha256,t.verification_level,
		t.package_snapshot_path,w.site_type,w.status FROM wp_update_tasks t
		JOIN websites w ON w.id=t.site_id WHERE t.id=? AND t.task_kind='update' AND t.component_type='core'`, id).
		Scan(&status, &currentVersion, &sha, &verification, &snapshot, &siteType, &siteStatus)
	if err != nil {
		return WPUpdateTask{}, err
	}
	stamp := wpUpdateDBTime(now)
	if status != wpUpdateQueued || siteType != "wordpress" || siteStatus != "active" || currentVersion != observedVersion ||
		!wpUpdateSHA256Pattern.MatchString(sha) || verification == "" || !filepath.IsAbs(snapshot) {
		if status == wpUpdateQueued {
			_, err = tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status='failed',stage='precheck',failure_stage='precheck',
				finished_at=?,updated_at=? WHERE id=? AND status='queued'`, stamp, stamp, id)
			if err == nil {
				err = insertWPUpdateEvent(ctx, tx, id, "precheck", "failed", "precheck_failed", stamp)
			}
			if err == nil {
				err = tx.Commit()
			}
		}
		return WPUpdateTask{}, errors.New("update claim precheck failed")
	}
	leaseUntil := wpUpdateDBTime(now.Add(wpUpdateLease))
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status='running',stage='claimed',lease_owner=?,
		lease_expires_at=?,started_at=?,updated_at=? WHERE id=? AND status='queued'`, owner, leaseUntil, stamp, stamp, id)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return WPUpdateTask{}, errors.New("update task was not claimed")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "claimed", "success", "", stamp); err != nil {
		return WPUpdateTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return WPUpdateTask{}, err
	}
	return s.getTask(ctx, id)
}

func (s *wpUpdateStore) heartbeat(ctx context.Context, id, owner string, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE wp_update_tasks SET lease_expires_at=?,updated_at=?
		WHERE id=? AND lease_owner=? AND status='running'`, wpUpdateDBTime(now.Add(wpUpdateLease)), wpUpdateDBTime(now), id, owner)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *wpUpdateStore) nextQueuedCoreUpdate(ctx context.Context) (WPUpdateTask, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM wp_update_tasks
		WHERE status='queued' AND task_kind='update' AND component_type='core'
		ORDER BY requested_at,id LIMIT 1`).Scan(&id)
	if err != nil {
		return WPUpdateTask{}, err
	}
	return s.getTask(ctx, id)
}

func (s *wpUpdateStore) interruptOwned(ctx context.Context, id, owner, code string, now time.Time) (bool, error) {
	if owner == "" || code == "" {
		return false, errors.New("invalid update interruption")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status='interrupted_unknown',stage='interrupted',
		requires_attention=1,lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=?
		WHERE id=? AND lease_owner=? AND status='running'`, stamp, stamp, id, owner)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "interrupted", "interrupted", code, stamp); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *wpUpdateStore) recoverExpired(ctx context.Context, now time.Time) (int64, error) {
	return s.recoverRunning(ctx, "lease_expired", now, false)
}

func (s *wpUpdateStore) recoverAfterRestart(ctx context.Context, now time.Time) (int64, error) {
	return s.recoverRunning(ctx, "worker_restarted", now, true)
}

func (s *wpUpdateStore) recoverRunning(ctx context.Context, code string, now time.Time, allRunning bool) (int64, error) {
	if code == "" {
		return 0, errors.New("invalid update recovery")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	query := `UPDATE wp_update_tasks SET status='interrupted_unknown',stage='interrupted',
		requires_attention=1,lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=?
		WHERE status='running' AND lease_expires_at<=? RETURNING id`
	queryArgs := []any{stamp, stamp, stamp}
	if allRunning {
		query = `UPDATE wp_update_tasks SET status='interrupted_unknown',stage='interrupted',
			requires_attention=1,lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=?
			WHERE status='running' RETURNING id`
		queryArgs = []any{stamp, stamp}
	}
	rows, err := tx.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := insertWPUpdateEvent(ctx, tx, id, "interrupted", "interrupted", code, stamp); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

func (s *wpUpdateStore) markSuccess(ctx context.Context, id, owner string, now time.Time) error {
	return s.finishOwned(ctx, id, owner, wpUpdateSuccess, "complete", "", false, now)
}

func (s *wpUpdateStore) markFailure(ctx context.Context, id, owner, failureStage string, attention bool, now time.Time) error {
	if failureStage == "" {
		return errors.New("failure stage is required")
	}
	return s.finishOwned(ctx, id, owner, wpUpdateFailed, failureStage, failureStage, attention, now)
}

func (s *wpUpdateStore) advanceOwnedStage(ctx context.Context, id, owner, expectedStage, nextStage string, now time.Time) error {
	if expectedStage == "" || nextStage == "" || expectedStage == nextStage {
		return errors.New("update stage is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET stage=?,updated_at=?
		WHERE id=? AND lease_owner=? AND status='running' AND stage=?`, nextStage, stamp, id, owner, expectedStage)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("update task ownership lost")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, nextStage, "info", "", stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpUpdateStore) beginAutomaticRollback(ctx context.Context, id, owner, failureStage string, now time.Time) error {
	if failureStage == "" {
		return errors.New("failure stage is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET stage='rollback',failure_stage=?,rollback_status='pending',updated_at=?
		WHERE id=? AND lease_owner=? AND status='running' AND rollback_status='not_required'`, failureStage, stamp, id, owner)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("update task ownership lost")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "rollback", "info", "automatic_rollback_started", stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpUpdateStore) finishAutomaticRollback(ctx context.Context, id, owner string, succeeded bool, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	rollbackStatus, attention, resultType, code := "success", 0, "success", "automatic_rollback_succeeded"
	if !succeeded {
		rollbackStatus, attention, resultType, code = "failed", 1, "failed", "automatic_rollback_failed"
	}
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status='failed',stage='rollback',rollback_status=?,requires_attention=?,
		lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=? WHERE id=? AND lease_owner=? AND status='running' AND rollback_status='pending'`,
		rollbackStatus, attention, stamp, stamp, id, owner)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("update task ownership lost")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "rollback", resultType, code, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpUpdateStore) finishOwned(ctx context.Context, id, owner, status, stage, failureStage string, attention bool, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status=?,stage=?,failure_stage=?,requires_attention=?,
		lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=? WHERE id=? AND lease_owner=? AND status='running'`,
		status, stage, failureStage, boolInt(attention), stamp, stamp, id, owner)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("update task ownership lost")
	}
	resultType := "success"
	code := ""
	if status == wpUpdateFailed {
		resultType, code = "failed", "update_failed"
	}
	if err := insertWPUpdateEvent(ctx, tx, id, stage, resultType, code, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpUpdateStore) disposeInterrupted(ctx context.Context, id, disposition string, now time.Time) error {
	if disposition != "confirmed_target_version" && disposition != "marked_failed_no_action" && disposition != "escalated" {
		return errors.New("invalid manual disposition")
	}
	attention := disposition != "confirmed_target_version"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET manual_disposition=?,requires_attention=?,updated_at=?
		WHERE id=? AND task_kind='update' AND status='interrupted_unknown' AND manual_disposition=''`,
		disposition, boolInt(attention), stamp, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("interrupted task is not disposable")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "manual_disposition", "manual", disposition, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpUpdateStore) hasEffectiveManualSuccess(ctx context.Context, siteID int) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wp_update_tasks WHERE site_id=? AND task_kind='update'
		AND trigger_type='manual' AND component_type='core' AND rollback_status='not_required' AND (
		status='success' OR (status='interrupted_unknown' AND manual_disposition='confirmed_target_version'))`, siteID).Scan(&count)
	return count > 0, err
}

func (s *wpUpdateStore) getTask(ctx context.Context, id string) (WPUpdateTask, error) {
	var task WPUpdateTask
	var attention, backupReady int
	err := s.db.QueryRowContext(ctx, `SELECT id,site_id,component_type,component_key,task_kind,COALESCE(parent_task_id,''),
		trigger_type,status,stage,failure_stage,rollback_status,requires_attention,manual_disposition,current_version,target_version,
		package_source,download_url,downloaded_sha256,verification_level,package_snapshot_path,backup_ready,COALESCE(plan_sealed_at,''),
		lease_owner,COALESCE(lease_expires_at,''),requested_at,COALESCE(started_at,''),COALESCE(finished_at,'')
		FROM wp_update_tasks WHERE id=?`, id).Scan(&task.ID, &task.SiteID, &task.ComponentType, &task.ComponentKey,
		&task.TaskKind, &task.ParentTaskID, &task.TriggerType, &task.Status, &task.Stage, &task.FailureStage,
		&task.RollbackStatus, &attention, &task.ManualDisposition, &task.CurrentVersion, &task.TargetVersion,
		&task.PackageSource, &task.DownloadURL, &task.DownloadedSHA256, &task.VerificationLevel,
		&task.PackageSnapshotPath, &backupReady, &task.PlanSealedAt, &task.LeaseOwner, &task.LeaseExpiresAt,
		&task.RequestedAt, &task.StartedAt, &task.FinishedAt)
	task.RequiresAttention = attention != 0
	task.BackupReady = backupReady != 0
	return task, err
}

type wpUpdateBackupRecord struct {
	Kind     string
	FilePath string
	FileSize int64
	SHA256   string
}

func (s *wpUpdateStore) markBackupsReady(ctx context.Context, id, owner string, records []wpUpdateBackupRecord, now time.Time) error {
	if len(records) != 2 || records[0].Kind != "database" || records[1].Kind != "core_files" {
		return errors.New("invalid update backup records")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	for _, record := range records {
		if !filepath.IsAbs(record.FilePath) || record.FileSize <= 0 || !wpUpdateSHA256Pattern.MatchString(record.SHA256) {
			return errors.New("invalid update backup record")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO wp_update_task_backups
			(task_id,kind,file_path,file_size,sha256,protected,created_at) VALUES (?,?,?,?,?,1,?)`,
			id, record.Kind, filepath.Clean(record.FilePath), record.FileSize, record.SHA256, stamp); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET backup_ready=1,stage='backups_ready',updated_at=?
		WHERE id=? AND task_kind='update' AND component_type='core' AND status='running' AND lease_owner=? AND backup_ready=0`, stamp, id, owner)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("update task ownership lost")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "backups_ready", "success", "", stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func insertWPUpdateEvent(ctx context.Context, tx *sql.Tx, id, stage, result, code, stamp string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO wp_update_task_events(task_id,stage,result,error_code,summary,created_at)
		VALUES (?,?,?,?,?,?)`, id, stage, result, code, "", stamp)
	return err
}

func newWPUpdateTaskID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "wpu_" + hex.EncodeToString(raw[:]), nil
}

func wpUpdateDBTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000") }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validWPUpdateDownloadURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && allowedWordPressURL(u)
}
