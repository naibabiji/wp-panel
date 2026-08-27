package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

type SiteMigrationWorkflowService struct {
	db      *sql.DB
	cfg     *config.Config
	pairing interface {
		RemoteCreateTargetBatch(context.Context, string, string, string, []SiteMigrationBatchSitePlan) (*SiteMigrationBatchPlanResult, error)
	}
	source interface {
		DeclareBatch(context.Context, string, string, string, []SiteMigrationSourceDeclaration) error
		QueueBatch(context.Context, string, string) error
	}
	databaseSizes func(*config.Config) (map[string]int64, error)
	prepareStart  func(context.Context, []int64) error
	deleteRemote  func(context.Context, string, string) error
}

type SiteMigrationEstimate struct {
	Sites                 []SiteMigrationBatchSitePlan `json:"sites"`
	TotalFileBytes        int64                        `json:"total_file_bytes"`
	TotalDatabaseBytes    int64                        `json:"total_database_bytes"`
	TotalReservationBytes int64                        `json:"total_reservation_bytes"`
}

func NewSiteMigrationWorkflowService(db *sql.DB, cfg *config.Config, pairing *SiteMigrationPairingService, source *SiteMigrationSourcePreparationService) (*SiteMigrationWorkflowService, error) {
	if db == nil || cfg == nil || pairing == nil || source == nil {
		return nil, errors.New("site migration workflow unavailable")
	}
	return &SiteMigrationWorkflowService{db: db, cfg: cfg, pairing: pairing, source: source, databaseSizes: GetMariaDBDatabaseSizes, deleteRemote: pairing.RemoteDeleteTargetTask}, nil
}

func (s *SiteMigrationWorkflowService) Start(ctx context.Context, peerID, batchID, requestedBy string, siteIDs []int64) (*SiteMigrationBatchPlanResult, error) {
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(batchID) || !validSiteMigrationRequester(requestedBy) || len(siteIDs) == 0 || len(siteIDs) > 500 {
		return nil, errors.New("invalid site migration start")
	}
	for _, siteID := range siteIDs {
		blocked, err := database.IsAIDevelopmentAccessBlocking(ctx, s.db, siteID)
		if err != nil {
			return nil, fmt.Errorf("check AI development access for site %d: %w", siteID, err)
		}
		if blocked {
			return nil, fmt.Errorf("site %d has active AI development access", siteID)
		}
	}
	plans, declarationsByDomain, err := s.buildPlans(ctx, siteIDs)
	if err != nil {
		return nil, err
	}
	if s.prepareStart != nil {
		if err := s.prepareStart(ctx, siteIDs); err != nil {
			return nil, err
		}
	}
	result, err := s.pairing.RemoteCreateTargetBatch(ctx, peerID, batchID, requestedBy, plans)
	if err != nil {
		return nil, err
	}
	declarations := make([]SiteMigrationSourceDeclaration, 0, len(result.Sites))
	for _, item := range result.Sites {
		if item.Status != "draft" {
			continue
		}
		declaration, ok := declarationsByDomain[strings.ToLower(item.Domain)]
		if !ok {
			return nil, errors.New("target batch returned unknown website")
		}
		declaration.ID = item.ID
		declarations = append(declarations, declaration)
	}
	if len(declarations) == 0 {
		s.discardRemoteResults(peerID, result.Sites, "")
		return nil, errors.New("target batch has no accepted websites")
	}
	if err := s.source.DeclareBatch(ctx, peerID, batchID, requestedBy, declarations); err != nil {
		s.discardRemoteResults(peerID, result.Sites, "")
		return nil, err
	}
	if err := s.source.QueueBatch(ctx, peerID, batchID); err != nil {
		if s.prepareStart != nil {
			_ = s.prepareStart(context.Background(), siteIDs)
		}
		s.discardRemoteResults(peerID, result.Sites, "")
		return nil, err
	}
	s.discardRemoteResults(peerID, result.Sites, "draft")
	return result, nil
}

func (s *SiteMigrationWorkflowService) discardRemoteResults(peerID string, items []SiteMigrationBatchPlanSiteResult, keepStatus string) {
	if s.deleteRemote == nil {
		return
	}
	for _, item := range items {
		if keepStatus != "" && item.Status == keepStatus {
			continue
		}
		_ = s.deleteRemote(context.Background(), peerID, item.ID)
	}
}

func (s *SiteMigrationWorkflowService) Estimate(ctx context.Context, siteIDs []int64) (SiteMigrationEstimate, error) {
	plans, _, err := s.buildPlans(ctx, siteIDs)
	if err != nil {
		return SiteMigrationEstimate{}, err
	}
	estimate := SiteMigrationEstimate{Sites: plans}
	for _, plan := range plans {
		reserved, err := migrationReservationBytes(plan.FileBytes, plan.DatabaseBytes)
		if err != nil || estimate.TotalFileBytes > int64(^uint64(0)>>1)-plan.FileBytes || estimate.TotalDatabaseBytes > int64(^uint64(0)>>1)-plan.DatabaseBytes || estimate.TotalReservationBytes > int64(^uint64(0)>>1)-reserved {
			return SiteMigrationEstimate{}, errors.New("site migration estimate overflow")
		}
		estimate.TotalFileBytes += plan.FileBytes
		estimate.TotalDatabaseBytes += plan.DatabaseBytes
		estimate.TotalReservationBytes += reserved
	}
	return estimate, nil
}

func (s *SiteMigrationWorkflowService) buildPlans(ctx context.Context, siteIDs []int64) ([]SiteMigrationBatchSitePlan, map[string]SiteMigrationSourceDeclaration, error) {
	if len(siteIDs) == 0 || len(siteIDs) > 500 {
		return nil, nil, errors.New("invalid source website selection")
	}
	dbSizes, err := s.databaseSizes(s.cfg)
	if err != nil {
		return nil, nil, err
	}
	plans := make([]SiteMigrationBatchSitePlan, 0, len(siteIDs))
	declarationsByDomain := make(map[string]SiteMigrationSourceDeclaration, len(siteIDs))
	seen := map[int64]struct{}{}
	for _, id := range siteIDs {
		if id <= 0 {
			return nil, nil, errors.New("invalid source website")
		}
		if _, ok := seen[id]; ok {
			return nil, nil, errors.New("duplicate source website")
		}
		seen[id] = struct{}{}
		var domain, aliases, siteType, root, dbName, status string
		if err := s.db.QueryRowContext(ctx, `SELECT lower(domain),lower(aliases),site_type,web_root,db_name,status FROM websites WHERE id=?`, id).Scan(&domain, &aliases, &siteType, &root, &dbName, &status); err != nil {
			return nil, nil, errors.New("source website unavailable")
		}
		if status != "active" {
			return nil, nil, errors.New("source website must be active before migration")
		}
		fileBytes, err := migrationDirectoryBytes(root)
		if err != nil {
			return nil, nil, err
		}
		plan := SiteMigrationBatchSitePlan{Domain: domain, Aliases: splitMigrationAliases(aliases), SiteType: siteType, FileBytes: fileBytes, DatabaseBytes: dbSizes[dbName]}
		plans = append(plans, plan)
		declarationsByDomain[domain] = SiteMigrationSourceDeclaration{SourceSiteID: id, Domain: domain, Aliases: plan.Aliases, SiteType: siteType, FileBytes: plan.FileBytes, DatabaseBytes: plan.DatabaseBytes}
	}
	return plans, declarationsByDomain, nil
}

// Retry queues only a task whose stage service explicitly classified its last
// failure as retryable. Unknown external outcomes must be abandoned instead.
func (s *SiteMigrationWorkflowService) Retry(ctx context.Context, taskID string) error {
	if !validSiteMigrationID(taskID) {
		return errors.New("invalid site migration retry")
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='queued',error_code='',lease_owner='',lease_expires_at=NULL,updated_at=?
		WHERE id=? AND status='failed_retryable' AND lease_owner='' AND lease_expires_at IS NULL
		AND EXISTS (SELECT 1 FROM site_migration_batches mb WHERE mb.id=site_migration_sites.batch_id AND mb.status='active')`, now, taskID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("site migration task is not safely retryable")
	}
	return nil
}

func migrationDirectoryBytes(root string) (int64, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return 0, errors.New("source website root unavailable")
	}
	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Size() > 0 && total > int64(^uint64(0)>>1)-info.Size() {
				return errors.New("source website size overflow")
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
