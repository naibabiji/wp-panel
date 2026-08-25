package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type siteMigrationStageProcessor struct {
	db     *sql.DB
	source interface {
		Prepare(context.Context, string, string, string, string) (SiteMigrationRuntimeSettings, error)
	}
	remote interface {
		RuntimeSettings(context.Context, string, string) (SiteMigrationRuntimeSettings, error)
	}
	transfer interface {
		Transfer(context.Context, string, string) error
	}
	settings interface {
		StoreTargetSettings(context.Context, string, SiteMigrationRuntimeSettings) error
	}
	resources interface {
		Create(context.Context, string, SiteMigrationTargetSpec) error
	}
	publisher interface {
		PublishData(context.Context, string) error
		ConfigureAndHealth(context.Context, string, siteMigrationTargetConfigureOps) error
	}
	cutover interface {
		ActivateAfterHealth(context.Context, string) error
	}
	coordinator interface {
		RemoteQueueTargetSite(context.Context, string, string, string) error
	}
	now     func() time.Time
	cleanup *SiteMigrationControlService
}

func newSiteMigrationStageProcessor(db *sql.DB, cfg *config.Config, pairing *SiteMigrationPairingService) (*siteMigrationStageProcessor, error) {
	if db == nil || cfg == nil || pairing == nil || cfg.Panel.DataDir == "" {
		return nil, errors.New("site migration stage processor unavailable")
	}
	root := filepath.Join(cfg.Panel.DataDir, "site-migration", "target")
	receiver, err := NewSiteMigrationTargetReceiver(db, root)
	if err != nil {
		return nil, err
	}
	remote, err := NewSiteMigrationRemoteSource(pairing)
	if err != nil {
		return nil, err
	}
	transfer, err := NewSiteMigrationTargetTransferService(db, remote, receiver)
	if err != nil {
		return nil, err
	}
	source, err := NewSiteMigrationSourcePreparationService(db, cfg, pairing)
	if err != nil {
		return nil, err
	}
	settings, err := NewSiteMigrationSettingsService(db)
	if err != nil {
		return nil, err
	}
	resources, err := NewSiteMigrationTargetResourceService(db, cfg, root)
	if err != nil {
		return nil, err
	}
	publisher, err := NewSiteMigrationTargetPublisher(db, cfg, root)
	if err != nil {
		return nil, err
	}
	cutover, err := NewSiteMigrationCutoverService(db)
	if err != nil {
		return nil, err
	}
	rollback, err := NewSiteMigrationTargetRollbackService(db, cfg, root)
	if err != nil {
		return nil, err
	}
	freezer, err := newSiteMigrationFreezer(db, cfg, productionSiteMigrationNginxRunner{})
	if err != nil {
		return nil, err
	}
	cleanup := &SiteMigrationControlService{db: db, pairing: pairing, rollback: rollback, freezer: freezer, stagingRoot: root, now: time.Now}
	return &siteMigrationStageProcessor{db: db, source: source, remote: remote, transfer: transfer, settings: settings, resources: resources, publisher: publisher, cutover: cutover, coordinator: pairing, now: time.Now, cleanup: cleanup}, nil
}

func (p *siteMigrationStageProcessor) HandleFailure(ctx context.Context, job *siteMigrationSite, _ error) error {
	if p.cleanup == nil || job == nil {
		return errors.New("site migration failure cleanup unavailable")
	}
	scope, err := p.cleanup.loadTaskCleanupScope(ctx, job.ID)
	if err != nil {
		return err
	}
	if scope.Direction == "source" {
		return p.cleanup.deleteSourceTask(ctx, job.ID, true)
	}
	// Activation has already made this an ordinary website. Keep the retryable
	// task so ResumeActivation can finish runtime synchronization.
	if scope.TargetActivated {
		return nil
	}
	if err := p.cleanup.deleteTargetTask(ctx, scope); err != nil {
		return err
	}
	if p.cleanup.pairing != nil {
		_ = p.cleanup.pairing.RemoteDeleteSourceTask(context.Background(), scope.PeerID, job.ID)
	}
	return nil
}

func (p *siteMigrationStageProcessor) Process(ctx context.Context, job *siteMigrationSite) error {
	if job == nil || job.LeaseOwner == "" {
		return errors.New("site migration job lease unavailable")
	}
	var direction, peerID string
	if err := p.db.QueryRowContext(ctx, `SELECT mb.direction,mb.peer_id FROM site_migration_batches mb JOIN site_migration_sites ms ON ms.batch_id=mb.id WHERE ms.id=? AND mb.status='active'`, job.ID).Scan(&direction, &peerID); err != nil {
		return err
	}
	if direction == "source" {
		return p.processSource(ctx, peerID, job)
	}
	if direction != "target" {
		return errors.New("site migration job direction unavailable")
	}
	return p.processTarget(ctx, peerID, job)
}

func (p *siteMigrationStageProcessor) processSource(ctx context.Context, peerID string, job *siteMigrationSite) error {
	marker, err := p.sourceMarker(ctx, job.ID)
	if err != nil {
		return err
	}
	settings, err := p.source.Prepare(ctx, peerID, job.ID, marker, job.LeaseOwner)
	if err != nil {
		return err
	}
	if err := p.storeSourceWarnings(ctx, job.ID, settings); err != nil {
		return p.stageFailed(job.ID, job.LeaseOwner, "source_warning_store_failed", err)
	}
	if p.coordinator != nil {
		if err := p.coordinator.RemoteQueueTargetSite(ctx, peerID, job.BatchID, job.ID); err != nil {
			return p.stageFailed(job.ID, job.LeaseOwner, "target_queue_failed", err)
		}
	}
	return nil
}

func (p *siteMigrationStageProcessor) storeSourceWarnings(ctx context.Context, taskID string, settings SiteMigrationRuntimeSettings) error {
	var raw string
	if err := p.db.QueryRowContext(ctx, `SELECT settings_snapshot FROM site_migration_sites WHERE id=? AND status='awaiting_cutover'`, taskID).Scan(&raw); err != nil {
		return err
	}
	var snapshot map[string]any
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		return errors.New("source migration settings snapshot invalid")
	}
	snapshot["migration_warnings"] = map[string]any{"remote_backup_reconfigure": settings.RemoteBackupReconfigure, "skipped_custom_commands": settings.SkippedCustomCommands}
	encoded, _ := json.Marshal(snapshot)
	result, err := p.db.ExecContext(ctx, `UPDATE site_migration_sites SET settings_snapshot=?,updated_at=? WHERE id=? AND status='awaiting_cutover'`, string(encoded), p.now().UTC(), taskID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("source migration warning state changed")
	}
	return nil
}

func (p *siteMigrationStageProcessor) sourceMarker(ctx context.Context, taskID string) (string, error) {
	var raw string
	if err := p.db.QueryRowContext(ctx, `SELECT settings_snapshot FROM site_migration_sites WHERE id=?`, taskID).Scan(&raw); err != nil {
		return "", err
	}
	var snapshot struct {
		Marker string `json:"source_marker_token"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) == nil && siteMigrationMarkerPattern.MatchString(snapshot.Marker) {
		return snapshot.Marker, nil
	}
	return randomMigrationValue("marker_", 32)
}

func (p *siteMigrationStageProcessor) processTarget(ctx context.Context, peerID string, job *siteMigrationSite) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.requireLease(ctx, job.ID, job.LeaseOwner); err != nil {
			return err
		}
		current, err := getSiteMigrationSite(ctx, p.db, job.ID)
		if err != nil {
			return err
		}
		switch current.Stage {
		case "preflight_passed", "transferring_files", "transferring_database":
			if err := p.transfer.Transfer(ctx, job.ID, job.LeaseOwner); err != nil {
				return err
			}
		case "preparing_target":
			settings, err := p.remote.RuntimeSettings(ctx, peerID, job.ID)
			if err != nil {
				return p.stageFailed(job.ID, job.LeaseOwner, "source_settings_failed", err)
			}
			if err := p.settings.StoreTargetSettings(ctx, job.ID, settings); err != nil {
				return p.stageFailed(job.ID, job.LeaseOwner, "target_settings_failed", err)
			}
			spec := SiteMigrationTargetSpec{Domain: current.TargetDomain, Aliases: settings.Aliases, SiteType: current.SiteType, DocumentRootSubdir: settings.DocumentRootSubdir}
			if err := p.resources.Create(ctx, job.ID, spec); err != nil {
				return err
			}
		case "publishing":
			if err := p.publisher.PublishData(ctx, job.ID); err != nil {
				return err
			}
		case "configuring_target":
			if err := p.publisher.ConfigureAndHealth(ctx, job.ID, nil); err != nil {
				return err
			}
		case "awaiting_cutover":
			if current.Status == "running" {
				result, err := p.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='awaiting_cutover',updated_at=?
					WHERE id=? AND status='running' AND stage='awaiting_cutover' AND lease_owner=? AND lease_expires_at>?`, p.now().UTC(), job.ID, job.LeaseOwner, p.now().UTC())
				if err != nil {
					return err
				}
				if changed, _ := result.RowsAffected(); changed != 1 {
					return errSiteMigrationNotClaimed
				}
			}
			return p.cutover.ActivateAfterHealth(ctx, job.ID)
		default:
			return errors.New("site migration target stage is not executable")
		}
	}
}

func (p *siteMigrationStageProcessor) requireLease(ctx context.Context, taskID, owner string) error {
	var count int
	err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites WHERE id=? AND status IN ('running','awaiting_cutover') AND lease_owner=? AND lease_expires_at>?`, taskID, owner, p.now().UTC()).Scan(&count)
	if err != nil || count != 1 {
		return errSiteMigrationNotClaimed
	}
	return nil
}

func (p *siteMigrationStageProcessor) stageFailed(taskID, owner, code string, cause error) error {
	_, _ = p.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',error_code=?,updated_at=? WHERE id=? AND status IN ('running','awaiting_cutover') AND lease_owner=?`, code, p.now().UTC(), taskID, owner)
	return cause
}
