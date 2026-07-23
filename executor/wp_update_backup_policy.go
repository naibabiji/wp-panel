package executor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

const wpUpdateBackupReuseWindow = 6 * time.Hour
const wpUpdateArtifactRetention = 24 * time.Hour

func (s *wpUpdateStore) recentDatabaseBackup(ctx context.Context, siteID int, now time.Time) (models.WPUpdateRecentDatabaseBackup, error) {
	var result models.WPUpdateRecentDatabaseBackup
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT b.id,b.task_id,b.file_size,b.created_at
		FROM wp_update_task_backups b JOIN wp_update_tasks t ON t.id=b.task_id
		WHERE t.site_id=? AND b.kind='database' AND b.protected=1 AND b.deleted_at IS NULL
		  AND t.backup_ready=1 AND t.requires_attention=0 AND t.rollback_status!='failed'
		  AND b.created_at>=?
		ORDER BY b.created_at DESC,b.id DESC LIMIT 1`,
		siteID, wpUpdateDBTime(now.Add(-wpUpdateBackupReuseWindow))).Scan(
		&result.BackupID, &result.TaskID, &result.FileSize, &created)
	if err != nil {
		return result, err
	}
	parsed, err := parseRequiredWPInventoryTime(created)
	if err != nil {
		return result, err
	}
	result.CreatedAt = parsed
	return result, nil
}

func (s *wpUpdateStore) validateDatabaseBackupChoice(ctx context.Context, siteID int, mode string, sourceID int64, root string, now time.Time) error {
	if mode == "fresh" && sourceID == 0 {
		return nil
	}
	if mode != "reuse" || sourceID <= 0 {
		return errors.New("invalid database backup choice")
	}
	var path, sha string
	var created string
	var size int64
	err := s.db.QueryRowContext(ctx, `SELECT b.file_path,b.file_size,b.sha256,b.created_at
		FROM wp_update_task_backups b JOIN wp_update_tasks t ON t.id=b.task_id
		WHERE b.id=? AND t.site_id=? AND b.kind='database' AND b.protected=1 AND b.deleted_at IS NULL
		  AND t.backup_ready=1 AND t.requires_attention=0 AND t.rollback_status!='failed'`,
		sourceID, siteID).Scan(&path, &size, &sha, &created)
	if err != nil || size <= 0 || !wpUpdateSHA256Pattern.MatchString(sha) {
		return errors.New("database backup unavailable")
	}
	parsed, err := parseRequiredWPInventoryTime(created)
	if err != nil || parsed.Before(now.Add(-wpUpdateBackupReuseWindow)) ||
		!pathWithin(filepath.Clean(root), filepath.Clean(path), false) {
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

func recentBackupOrNil(store *wpUpdateStore, ctx context.Context, siteID int, now time.Time) *models.WPUpdateRecentDatabaseBackup {
	backup, err := store.recentDatabaseBackup(ctx, siteID, now)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return nil
	}
	return &backup
}

func (s *wpUpdateArtifactService) cleanupExpiredArtifacts(ctx context.Context, now time.Time) error {
	if s == nil || s.store == nil || !filepath.IsAbs(s.root) {
		return errors.New("update artifact cleanup unavailable")
	}
	cutoff := wpUpdateDBTime(now.Add(-wpUpdateArtifactRetention))
	rows, err := s.store.db.QueryContext(ctx, `SELECT b.id,b.file_path FROM wp_update_task_backups b
		JOIN wp_update_tasks t ON t.id=b.task_id
		WHERE b.protected=1 AND b.deleted_at IS NULL
		  AND t.status IN ('success','failed')
		  AND t.requires_attention=0 AND t.rollback_status!='failed'
		  AND t.finished_at IS NOT NULL AND t.finished_at<=?
		  AND (b.kind!='database' OR NOT EXISTS (
			SELECT 1 FROM wp_update_tasks r
			WHERE r.database_backup_source_id=b.id AND (
			  r.status NOT IN ('success','failed') OR r.requires_attention=1 OR
			  r.rollback_status='failed' OR r.finished_at IS NULL OR r.finished_at>?
			)
		  ))
		ORDER BY b.id`, cutoff, cutoff)
	if err != nil {
		return err
	}
	type candidate struct {
		id   int64
		path string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.path); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range candidates {
		clean := filepath.Clean(item.path)
		if !pathWithin(s.root, clean, false) {
			continue
		}
		if info, err := os.Lstat(clean); err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if err := os.Remove(clean); err != nil {
				continue
			}
		} else if !os.IsNotExist(err) {
			continue
		}
		_, _ = s.store.db.ExecContext(ctx, `UPDATE wp_update_task_backups
			SET protected=0,deleted_at=?,cleanup_result='deleted'
			WHERE id=? AND protected=1 AND deleted_at IS NULL`,
			wpUpdateDBTime(now), item.id)
	}
	taskRows, err := s.store.db.QueryContext(ctx, `SELECT t.id FROM wp_update_tasks t
		WHERE t.status IN ('success','failed') AND t.requires_attention=0
		  AND t.rollback_status!='failed' AND t.finished_at IS NOT NULL AND t.finished_at<=?
		  AND NOT EXISTS (SELECT 1 FROM wp_update_task_backups b
			WHERE b.task_id=t.id AND b.protected=1 AND b.deleted_at IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM wp_update_tasks r
			WHERE r.database_backup_source_id IN (
			  SELECT b.id FROM wp_update_task_backups b WHERE b.task_id=t.id AND b.kind='database'
			) AND (
			  r.status NOT IN ('success','failed') OR r.requires_attention=1 OR
			  r.rollback_status='failed' OR r.finished_at IS NULL OR r.finished_at>?
			)
		  )`, cutoff, cutoff)
	if err != nil {
		return err
	}
	var taskIDs []string
	for taskRows.Next() {
		var id string
		if err := taskRows.Scan(&id); err != nil {
			taskRows.Close()
			return err
		}
		taskIDs = append(taskIDs, id)
	}
	if err := taskRows.Close(); err != nil {
		return err
	}
	for _, id := range taskIDs {
		if !wpUpdateTaskIDPattern.MatchString(id) {
			continue
		}
		dir := filepath.Join(s.root, id)
		if info, err := os.Lstat(dir); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}
