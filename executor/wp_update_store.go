package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	ComponentKey   string
	CurrentVersion string
	TargetVersion  string
	PackageSource  string
	DownloadURL    string
}

func (s *wpUpdateStore) createPluginManualPlan(ctx context.Context, plan WPUpdatePlan, now time.Time) (WPUpdateTask, error) {
	if s == nil || s.db == nil || plan.SiteID <= 0 || !validWPPluginComponentKey(plan.ComponentKey) ||
		!wpComponentVersionPattern.MatchString(plan.CurrentVersion) || !wpComponentVersionPattern.MatchString(plan.TargetVersion) ||
		plan.CurrentVersion == plan.TargetVersion || plan.PackageSource != "wordpress.org" ||
		!validWPPluginDownloadURL(plan.DownloadURL, strings.Split(plan.ComponentKey, "/")[0], plan.TargetVersion) {
		return WPUpdateTask{}, errors.New("invalid plugin update plan")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WPUpdateTask{}, err
	}
	defer tx.Rollback()
	var siteType, siteStatus, inventoryStatus, collectionID, componentVersion string
	var multisite int
	err = tx.QueryRowContext(ctx, `SELECT w.site_type,w.status,COALESCE(i.status,''),COALESCE(i.collection_id,''),
		COALESCE(i.is_multisite,0),COALESCE(c.version,'') FROM websites w
		LEFT JOIN site_wp_inventory_state i ON i.site_id=w.id
		LEFT JOIN site_wp_components c ON c.site_id=w.id AND c.component_type='plugin'
			AND c.component_key=? AND c.collection_id=i.collection_id WHERE w.id=?`, plan.ComponentKey, plan.SiteID).
		Scan(&siteType, &siteStatus, &inventoryStatus, &collectionID, &multisite, &componentVersion)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if siteType != "wordpress" || siteStatus != "active" || inventoryStatus != "complete" ||
		multisite != 0 || collectionID == "" || componentVersion != plan.CurrentVersion {
		return WPUpdateTask{}, errors.New("site is not eligible for plugin update")
	}
	var candidate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_wp_component_updates
		WHERE site_id=? AND component_type='plugin' AND component_key=? AND target_version=? AND collection_id=?`,
		plan.SiteID, plan.ComponentKey, plan.TargetVersion, collectionID).Scan(&candidate); err != nil {
		return WPUpdateTask{}, err
	}
	if candidate == 0 {
		return WPUpdateTask{}, errors.New("plugin update candidate is not current")
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
		VALUES (?,?,'plugin',?,'update','manual','preparing','plan',?,?,?,?,?,?,?)`,
		id, plan.SiteID, plan.ComponentKey, plan.CurrentVersion, plan.TargetVersion, plan.PackageSource, plan.DownloadURL, stamp, stamp, stamp)
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

func (s *wpUpdateStore) failPreparingPlan(ctx context.Context, id, code string, now time.Time) error {
	if !wpUpdateTaskIDPattern.MatchString(id) || code == "" {
		return errors.New("invalid preparing update failure")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status='failed',stage='package_prepare',
		failure_stage='package_prepare',finished_at=?,updated_at=?
		WHERE id=? AND task_kind='update' AND status='preparing' AND plan_sealed_at IS NULL`, stamp, stamp, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("preparing update plan not failed")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "package_prepare", "failed", code, stamp); err != nil {
		return err
	}
	return tx.Commit()
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

func (s *wpUpdateStore) claimPluginUpdate(ctx context.Context, id, owner, observedVersion, observedSHA string, now time.Time) (WPUpdateTask, error) {
	if owner == "" || observedVersion == "" || !wpUpdateSHA256Pattern.MatchString(observedSHA) {
		return WPUpdateTask{}, errors.New("invalid plugin claim")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WPUpdateTask{}, err
	}
	defer tx.Rollback()
	var status, currentVersion, sha, verification, snapshot, siteType, siteStatus, componentKey string
	err = tx.QueryRowContext(ctx, `SELECT t.status,t.current_version,t.downloaded_sha256,t.verification_level,
		t.package_snapshot_path,w.site_type,w.status,t.component_key FROM wp_update_tasks t
		JOIN websites w ON w.id=t.site_id WHERE t.id=? AND t.task_kind='update' AND t.component_type='plugin'`, id).
		Scan(&status, &currentVersion, &sha, &verification, &snapshot, &siteType, &siteStatus, &componentKey)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if status != wpUpdateQueued || siteType != "wordpress" || siteStatus != "active" ||
		currentVersion != observedVersion || sha != observedSHA || verification != "structure_only" ||
		!filepath.IsAbs(snapshot) || !validWPPluginComponentKey(componentKey) {
		return s.failPluginClaimTx(ctx, tx, id, status, "precheck_failed", now)
	}
	stamp := wpUpdateDBTime(now)
	leaseUntil := wpUpdateDBTime(now.Add(wpUpdateLease))
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status='running',stage='claimed',lease_owner=?,
		lease_expires_at=?,started_at=?,updated_at=? WHERE id=? AND status='queued' AND component_type='plugin'`,
		owner, leaseUntil, stamp, stamp, id)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return WPUpdateTask{}, errors.New("plugin update task was not claimed")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "claimed", "success", "", stamp); err != nil {
		return WPUpdateTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return WPUpdateTask{}, err
	}
	return s.getTask(ctx, id)
}

func (s *wpUpdateStore) failPluginClaim(ctx context.Context, id, code string, now time.Time) (WPUpdateTask, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WPUpdateTask{}, err
	}
	defer tx.Rollback()
	return s.failPluginClaimTx(ctx, tx, id, wpUpdateQueued, code, now)
}

func (s *wpUpdateStore) failPluginClaimTx(ctx context.Context, tx *sql.Tx, id, status, code string, now time.Time) (WPUpdateTask, error) {
	if status != wpUpdateQueued || code == "" {
		return WPUpdateTask{}, errors.New("plugin update claim precheck failed")
	}
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET status='failed',stage='precheck',failure_stage='precheck',
		finished_at=?,updated_at=? WHERE id=? AND status='queued' AND component_type='plugin'`, stamp, stamp, id)
	if err != nil {
		return WPUpdateTask{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return WPUpdateTask{}, errors.New("plugin update claim precheck failed")
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "precheck", "failed", code, stamp); err != nil {
		return WPUpdateTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return WPUpdateTask{}, err
	}
	return WPUpdateTask{}, errors.New("plugin update claim precheck failed")
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

func (s *wpUpdateStore) nextQueuedUpdateCandidates(ctx context.Context) ([]WPUpdateTask, error) {
	rows, err := s.db.QueryContext(ctx, `WITH ranked AS (
		SELECT id,requested_at,component_type,
			ROW_NUMBER() OVER (PARTITION BY component_type ORDER BY requested_at,id) AS candidate_rank
		FROM wp_update_tasks
		WHERE status='queued' AND task_kind='update' AND component_type IN ('core','plugin')
	)
	SELECT id FROM ranked WHERE candidate_rank=1 ORDER BY requested_at,id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	tasks := make([]WPUpdateTask, 0, len(ids))
	for _, id := range ids {
		task, err := s.getTask(ctx, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
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
	return s.recoverRunningComponent(ctx, code, now, allRunning, "")
}

func (s *wpUpdateStore) recoverCoreExpired(ctx context.Context, now time.Time) (int64, error) {
	return s.recoverRunningComponent(ctx, "lease_expired", now, false, "core")
}

func (s *wpUpdateStore) recoverCoreAfterRestart(ctx context.Context, now time.Time) (int64, error) {
	return s.recoverRunningComponent(ctx, "worker_restarted", now, true, "core")
}

func (s *wpUpdateStore) recoverRunningComponent(ctx context.Context, code string, now time.Time, allRunning bool, component string) (int64, error) {
	if code == "" {
		return 0, errors.New("invalid update recovery")
	}
	if component != "" && component != "core" {
		return 0, errors.New("invalid update recovery component")
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
	if component == "core" {
		query = `UPDATE wp_update_tasks SET status='interrupted_unknown',stage='interrupted',
			requires_attention=1,lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=?
			WHERE status='running' AND component_type='core' AND lease_expires_at<=? RETURNING id`
	}
	if allRunning {
		query = `UPDATE wp_update_tasks SET status='interrupted_unknown',stage='interrupted',
			requires_attention=1,lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=?
			WHERE status='running' RETURNING id`
		queryArgs = []any{stamp, stamp}
		if component == "core" {
			query = `UPDATE wp_update_tasks SET status='interrupted_unknown',stage='interrupted',
				requires_attention=1,lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=?
				WHERE status='running' AND component_type='core' RETURNING id`
		}
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

func (s *wpUpdateStore) runningPluginTasks(ctx context.Context, now time.Time, expiredOnly bool) ([]WPUpdateTask, error) {
	query := `SELECT id FROM wp_update_tasks WHERE status='running' AND component_type='plugin'`
	args := []any{}
	if expiredOnly {
		query += ` AND lease_expires_at<=?`
		args = append(args, wpUpdateDBTime(now))
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	tasks := make([]WPUpdateTask, 0, len(ids))
	for _, id := range ids {
		task, err := s.getTask(ctx, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
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

func (s *wpUpdateStore) recordPluginPrepared(ctx context.Context, id, owner string, wasActive bool, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := wpUpdateDBTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE wp_update_tasks SET stage='unlocking',updated_at=?
		WHERE id=? AND component_type='plugin' AND lease_owner=? AND status='running' AND stage='backups_ready'`, stamp, id, owner)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("update task ownership lost")
	}
	code := "plugin_observed_inactive"
	if wasActive {
		code = "plugin_observed_active"
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "prepare", "info", code, stamp); err != nil {
		return err
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "unlocking", "info", "", stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpUpdateStore) recordPluginRunnerJournal(ctx context.Context, id, owner string, report wpPluginUpdateJournalReport, now time.Time) error {
	if len(report.Checkpoints) > len(wpPluginJournalCheckpoints) {
		return errors.New("invalid plugin runner journal report")
	}
	for i, checkpoint := range report.Checkpoints {
		if checkpoint != wpPluginJournalCheckpoints[i] {
			return errors.New("invalid plugin runner journal report")
		}
	}
	last := "none"
	if len(report.Checkpoints) != 0 {
		last = report.Checkpoints[len(report.Checkpoints)-1]
	}
	code := fmt.Sprintf("runner_journal_%d_%s_complete", len(report.Checkpoints), last)
	if report.Truncated {
		code = fmt.Sprintf("runner_journal_%d_%s_truncated", len(report.Checkpoints), last)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wp_update_tasks WHERE id=? AND lease_owner=? AND status='running')`, id, owner).Scan(&owned); err != nil || owned != 1 {
		return errors.New("update task ownership lost")
	}
	var recorded int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wp_update_task_events
		WHERE task_id=? AND stage='runner_journal' AND result='info' AND error_code=?)`, id, code).Scan(&recorded); err != nil {
		return err
	}
	if recorded == 1 {
		return tx.Commit()
	}
	if err := insertWPUpdateEvent(ctx, tx, id, "runner_journal", "info", code, wpUpdateDBTime(now)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpUpdateStore) ownsRunningTask(ctx context.Context, id, owner string) (bool, error) {
	var owned int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wp_update_tasks
		WHERE id=? AND lease_owner=? AND status='running')`, id, owner).Scan(&owned)
	return owned == 1, err
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

func (s *wpUpdateStore) latestCoreUpdateTask(ctx context.Context, siteID int) (WPUpdateTask, error) {
	if s == nil || s.db == nil || siteID <= 0 {
		return WPUpdateTask{}, errors.New("invalid update task lookup")
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT t.id FROM wp_update_tasks t
		JOIN websites w ON w.id=t.site_id
		WHERE t.site_id=? AND t.task_kind='update' AND t.component_type='core'
		ORDER BY t.created_at DESC,t.rowid DESC LIMIT 1`, siteID).Scan(&id)
	if err != nil {
		return WPUpdateTask{}, err
	}
	return s.getTask(ctx, id)
}

type wpUpdateBackupRecord struct {
	Kind     string
	FilePath string
	FileSize int64
	SHA256   string
}

func (s *wpUpdateStore) markBackupsReady(ctx context.Context, id, owner string, records []wpUpdateBackupRecord, now time.Time) error {
	return s.markComponentBackupsReady(ctx, id, owner, "core", "core_files", records, now)
}

func (s *wpUpdateStore) markPluginBackupsReady(ctx context.Context, id, owner string, records []wpUpdateBackupRecord, now time.Time) error {
	return s.markComponentBackupsReady(ctx, id, owner, "plugin", "plugin_files", records, now)
}

func (s *wpUpdateStore) markComponentBackupsReady(ctx context.Context, id, owner, componentType, fileKind string, records []wpUpdateBackupRecord, now time.Time) error {
	validPair := (componentType == "core" && fileKind == "core_files") || (componentType == "plugin" && fileKind == "plugin_files")
	if !validPair || len(records) != 2 || records[0].Kind != "database" || records[1].Kind != fileKind {
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
		WHERE id=? AND task_kind='update' AND component_type=? AND status='running' AND lease_owner=? AND backup_ready=0`, stamp, id, componentType, owner)
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

func validWPPluginComponentKey(key string) bool {
	if strings.Contains(key, `\`) || strings.Count(key, "/") != 1 {
		return false
	}
	parts := strings.Split(key, "/")
	return wpComponentSlugPattern.MatchString(parts[0]) && wpComponentPHPFilePattern.MatchString(parts[1])
}

func validWPPluginDownloadURL(raw, slug, targetVersion string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() == "downloads.wordpress.org" &&
		(u.Port() == "" || u.Port() == "443") && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		u.EscapedPath() == "/plugin/"+slug+"."+targetVersion+".zip"
}
