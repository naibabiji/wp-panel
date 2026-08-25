package executor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type siteMigrationTaskCleanupScope struct {
	ID, BatchID, PeerID, Direction, Domain, Status, Stage string
	SourceSiteID, TargetSiteID                            sql.NullInt64
	HasMaintenance, HasRuntimeResources, TargetActivated  bool
}

func (s *SiteMigrationControlService) loadTaskCleanupScope(ctx context.Context, taskID string) (siteMigrationTaskCleanupScope, error) {
	var scope siteMigrationTaskCleanupScope
	var maintenance, runtimeResources int
	err := s.db.QueryRowContext(ctx, `SELECT ms.id,ms.batch_id,mb.peer_id,mb.direction,ms.source_domain,ms.status,ms.stage,
		ms.source_site_id,ms.target_site_id,
		EXISTS(SELECT 1 FROM site_migration_resources r WHERE r.migration_site_id=ms.id AND r.resource_type='source_maintenance_config' AND r.status='created'),
		EXISTS(SELECT 1 FROM site_migration_resources r WHERE r.migration_site_id=ms.id AND r.resource_type NOT IN ('target_staging_root','database_identity','site_identity') AND r.status IN ('created','published','remove_failed')),
		EXISTS(SELECT 1 FROM site_migration_locks ml WHERE ml.migration_site_id=ms.id AND ml.direction='target' AND ml.status='released')
		FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id
		WHERE ms.id=?`, taskID).Scan(&scope.ID, &scope.BatchID, &scope.PeerID, &scope.Direction, &scope.Domain, &scope.Status, &scope.Stage,
		&scope.SourceSiteID, &scope.TargetSiteID, &maintenance, &runtimeResources, &scope.TargetActivated)
	if err != nil || !validSiteMigrationID(scope.ID) || !validSiteMigrationID(scope.BatchID) || !validSiteMigrationID(scope.PeerID) {
		return siteMigrationTaskCleanupScope{}, errors.New("site migration task unavailable")
	}
	scope.HasMaintenance, scope.HasRuntimeResources = maintenance != 0, runtimeResources != 0
	scope.TargetActivated = scope.TargetActivated || scope.Stage == "activation_runtime_sync" || scope.Stage == "completed"
	return scope, nil
}

func (s *SiteMigrationControlService) prepareStart(ctx context.Context, siteIDs []int64) error {
	if len(siteIDs) == 0 {
		return errors.New("invalid source website selection")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(siteIDs)), ",")
	args := make([]any, len(siteIDs))
	for i, id := range siteIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ms.id FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source'
		WHERE ms.source_site_id IN (`+placeholders+`) ORDER BY ms.created_at`, args...)
	if err != nil {
		return err
	}
	var taskIDs []string
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			rows.Close()
			return err
		}
		taskIDs = append(taskIDs, taskID)
	}
	err = rows.Close()
	if err != nil {
		return err
	}
	for _, taskID := range taskIDs {
		if err := s.deleteSourceTask(ctx, taskID, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *SiteMigrationControlService) DeleteTask(ctx context.Context, taskID string) error {
	scope, err := s.loadTaskCleanupScope(ctx, taskID)
	if err != nil {
		return err
	}
	if scope.Direction == "source" {
		return s.deleteSourceTask(ctx, taskID, true)
	}
	if scope.Direction != "target" {
		return errors.New("site migration task unavailable")
	}
	return s.deleteTargetTask(ctx, scope)
}

func (s *SiteMigrationControlService) DeleteTargetTask(ctx context.Context, taskID string) error {
	scope, err := s.loadTaskCleanupScope(ctx, taskID)
	if err != nil || scope.Direction != "target" {
		return errors.New("target migration task unavailable")
	}
	return s.deleteTargetTask(ctx, scope)
}

func (s *SiteMigrationControlService) DeleteSourceTask(ctx context.Context, taskID string) error {
	return s.deleteSourceTask(ctx, taskID, false)
}

func (s *SiteMigrationControlService) deleteSourceTask(ctx context.Context, taskID string, notifyTarget bool) error {
	scope, err := s.loadTaskCleanupScope(ctx, taskID)
	if err != nil || scope.Direction != "source" {
		return errors.New("source migration task unavailable")
	}
	if scope.HasMaintenance {
		if s.freezer == nil {
			return errors.New("source migration restore unavailable")
		}
		if err := s.freezer.AbandonSource(ctx, taskID); err != nil {
			return err
		}
	} else if err := releaseSiteMigrationTaskReferences(ctx, s.db, taskID); err != nil {
		return err
	}
	if err := s.removeTaskRuntimeFiles(scope.Direction, taskID); err != nil {
		return err
	}
	if err := forgetSiteMigrationTask(ctx, s.db, taskID); err != nil {
		return err
	}
	if notifyTarget && s.pairing != nil {
		_ = s.pairing.RemoteDeleteTargetTask(context.Background(), scope.PeerID, taskID)
	}
	return nil
}

func (s *SiteMigrationControlService) deleteTargetTask(ctx context.Context, scope siteMigrationTaskCleanupScope) error {
	// A completed target website is already an ordinary local website. Forget
	// only the migration bookkeeping and never delete that website here.
	if !scope.TargetActivated && scope.Status != "completed" && (scope.HasRuntimeResources || scope.TargetSiteID.Valid) {
		if s.rollback == nil {
			return errors.New("target migration cleanup unavailable")
		}
		if err := s.rollback.Abandon(ctx, scope.ID, scope.Domain, "migration-task-delete", "discard unfinished migration", true); err != nil {
			return err
		}
	} else if err := releaseSiteMigrationTaskReferences(ctx, s.db, scope.ID); err != nil {
		return err
	}
	if err := s.removeTaskRuntimeFiles(scope.Direction, scope.ID); err != nil {
		return err
	}
	return forgetSiteMigrationTask(ctx, s.db, scope.ID)
}

func (s *SiteMigrationControlService) removeTaskRuntimeFiles(direction, taskID string) error {
	if direction == "target" && s.stagingRoot != "" {
		return os.RemoveAll(filepath.Join(s.stagingRoot, taskID))
	}
	if direction == "source" && s.freezer != nil && s.freezer.cfg.Panel.DataDir != "" {
		for _, name := range []string{"database", "certificates"} {
			if err := os.RemoveAll(filepath.Join(s.freezer.cfg.Panel.DataDir, "site-migration", name, taskID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func releaseSiteMigrationTaskReferences(ctx context.Context, db *sql.DB, taskID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := releaseSiteMigrationTaskReferencesTx(ctx, tx, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func releaseSiteMigrationTaskReferencesTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET source_site_id=NULL,target_site_id=NULL,lease_owner='',lease_expires_at=NULL
		WHERE id=? AND (lease_owner='' OR lease_expires_at IS NULL OR lease_expires_at<=CURRENT_TIMESTAMP)`, taskID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("site migration task is still running")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_migration_locks SET status='released',site_id=NULL,released_at=COALESCE(released_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE migration_site_id=?`, taskID); err != nil {
		return err
	}
	return nil
}

func forgetSiteMigrationTask(ctx context.Context, db *sql.DB, taskID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := forgetSiteMigrationTaskTx(ctx, tx, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func forgetSiteMigrationTaskTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	var batchID string
	if err := tx.QueryRowContext(ctx, `SELECT batch_id FROM site_migration_sites WHERE id=?`, taskID).Scan(&batchID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_sites WHERE id=?`, taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_batches WHERE id=? AND NOT EXISTS (SELECT 1 FROM site_migration_sites WHERE batch_id=?)`, batchID, batchID); err != nil {
		return err
	}
	return nil
}
