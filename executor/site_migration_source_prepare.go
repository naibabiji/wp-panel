package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type SiteMigrationSourceDeclaration struct {
	ID            string   `json:"id"`
	SourceSiteID  int64    `json:"source_site_id"`
	Domain        string   `json:"domain"`
	Aliases       []string `json:"aliases"`
	SiteType      string   `json:"site_type"`
	FileBytes     int64    `json:"file_bytes"`
	DatabaseBytes int64    `json:"database_bytes"`
}

type siteMigrationPreparedSourceDeclaration struct {
	SiteMigrationSourceDeclaration
	snapshot string
}

type siteMigrationSourcePrepareOps interface {
	Freeze(context.Context, string, string) error
	BuildManifest(context.Context, string) error
	BuildDatabase(context.Context, string) error
	BuildCertificates(context.Context, string) error
	Settings(context.Context, string) (SiteMigrationRuntimeSettings, error)
}

type SiteMigrationSourcePreparationService struct {
	db             *sql.DB
	ops            siteMigrationSourcePrepareOps
	now            func() time.Time
	dataRoot       string
	availableBytes func(string) (uint64, error)
}

type productionSiteMigrationSourcePrepareOps struct {
	freezer  *siteMigrationFreezer
	source   *SiteMigrationSourceService
	settings *SiteMigrationSettingsService
}

func (o productionSiteMigrationSourcePrepareOps) Freeze(ctx context.Context, id, marker string) error {
	return o.freezer.FreezeSource(ctx, id, marker)
}
func (o productionSiteMigrationSourcePrepareOps) BuildManifest(ctx context.Context, id string) error {
	return o.source.BuildFileManifest(ctx, id)
}
func (o productionSiteMigrationSourcePrepareOps) BuildDatabase(ctx context.Context, id string) error {
	return o.source.BuildDatabaseExport(ctx, id)
}
func (o productionSiteMigrationSourcePrepareOps) BuildCertificates(ctx context.Context, id string) error {
	return o.source.BuildCertificateArtifacts(ctx, id)
}
func (o productionSiteMigrationSourcePrepareOps) Settings(ctx context.Context, id string) (SiteMigrationRuntimeSettings, error) {
	return o.settings.SourceSettings(ctx, id)
}

func NewSiteMigrationSourcePreparationService(db *sql.DB, cfg *config.Config, pairing *SiteMigrationPairingService) (*SiteMigrationSourcePreparationService, error) {
	freezer, err := newSiteMigrationFreezer(db, cfg, productionSiteMigrationNginxRunner{})
	if err != nil {
		return nil, err
	}
	source, err := NewSiteMigrationSourceService(db, pairing)
	if err != nil {
		return nil, err
	}
	settings, err := NewSiteMigrationSettingsService(db)
	if err != nil {
		return nil, err
	}
	service, err := newSiteMigrationSourcePreparationService(db, productionSiteMigrationSourcePrepareOps{freezer: freezer, source: source, settings: settings})
	if err != nil {
		return nil, err
	}
	if cfg != nil && cfg.Panel.DataDir != "" {
		service.dataRoot = cfg.Panel.DataDir
		service.availableBytes = migrationAvailableBytes
	}
	return service, nil
}

func newSiteMigrationSourcePreparationService(db *sql.DB, ops siteMigrationSourcePrepareOps) (*SiteMigrationSourcePreparationService, error) {
	if db == nil || ops == nil {
		return nil, errors.New("site migration source preparation unavailable")
	}
	return &SiteMigrationSourcePreparationService{db: db, ops: ops, now: time.Now}, nil
}

func (s *SiteMigrationSourcePreparationService) QueueBatch(ctx context.Context, peerID, batchID string) error {
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(batchID) {
		return errors.New("invalid source migration batch queue")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_batches WHERE id=? AND peer_id=? AND direction='source' AND status='active'`, batchID, peerID).Scan(&count); err != nil || count != 1 {
		return errors.New("source migration batch unavailable")
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='queued',updated_at=? WHERE batch_id=? AND status='draft' AND stage='preflight_passed'`, s.now().UTC(), batchID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites WHERE batch_id=?`, batchID).Scan(&existing); err != nil || existing == 0 {
			return errors.New("source migration batch has no ready sites")
		}
	}
	return tx.Commit()
}

func (s *SiteMigrationSourcePreparationService) DeclareBatch(ctx context.Context, peerID, batchID, requestedBy string, declarations []SiteMigrationSourceDeclaration) error {
	requestedBy = strings.TrimSpace(requestedBy)
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(batchID) || !validSiteMigrationRequester(requestedBy) || len(declarations) == 0 || len(declarations) > 500 {
		return errors.New("invalid source migration batch declaration")
	}
	items := make([]siteMigrationPreparedSourceDeclaration, 0, len(declarations))
	seenIDs, seenDomains := make(map[string]struct{}), make(map[string]struct{})
	for _, declaration := range declarations {
		declaration.Domain = strings.ToLower(strings.TrimSpace(declaration.Domain))
		if !validSiteMigrationID(declaration.ID) || declaration.SourceSiteID <= 0 || !IsValidDomain(declaration.Domain) || (declaration.SiteType != "wordpress" && declaration.SiteType != "php") || declaration.FileBytes < 0 || declaration.DatabaseBytes < 0 {
			return errors.New("invalid source migration site declaration")
		}
		if _, exists := seenIDs[declaration.ID]; exists {
			return errors.New("duplicate source migration task")
		}
		seenIDs[declaration.ID] = struct{}{}
		all := append([]string{declaration.Domain}, declaration.Aliases...)
		for i := range all {
			all[i] = strings.ToLower(strings.TrimSpace(all[i]))
			if !IsValidDomain(all[i]) {
				return errors.New("invalid source migration alias")
			}
			if _, exists := seenDomains[all[i]]; exists {
				return errors.New("duplicate source migration domain")
			}
			seenDomains[all[i]] = struct{}{}
		}
		declaration.Aliases = append([]string(nil), all[1:]...)
		raw, err := json.Marshal(map[string]any{"source_aliases": declaration.Aliases})
		if err != nil {
			return err
		}
		items = append(items, siteMigrationPreparedSourceDeclaration{SiteMigrationSourceDeclaration: declaration, snapshot: string(raw)})
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var peerStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM site_migration_peers WHERE id=?`, peerID).Scan(&peerStatus); err != nil || peerStatus != "paired" {
		return errors.New("source migration peer unavailable")
	}
	var existingPeer, direction, batchStatus, existingRequester string
	err = tx.QueryRowContext(ctx, `SELECT peer_id,direction,status,requested_by FROM site_migration_batches WHERE id=?`, batchID).Scan(&existingPeer, &direction, &batchStatus, &existingRequester)
	if err == nil {
		if existingPeer != peerID || direction != "source" || batchStatus != "active" || existingRequester != requestedBy {
			return errors.New("source migration batch declaration conflicts")
		}
		return s.verifyDeclaredBatch(ctx, tx, batchID, items)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_batches(id,peer_id,direction,status,requested_by,created_at,updated_at) VALUES (?,?,'source','active',?,?,?)`, batchID, peerID, requestedBy, now, now); err != nil {
		return err
	}
	for _, item := range items {
		var domain, aliases, siteType string
		if err := tx.QueryRowContext(ctx, `SELECT lower(domain),lower(aliases),site_type FROM websites WHERE id=?`, item.SourceSiteID).Scan(&domain, &aliases, &siteType); err != nil || domain != item.Domain || siteType != item.SiteType || !sameMigrationAliases(aliases, item.Aliases) {
			return errors.New("source website declaration changed")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_sites(id,batch_id,source_site_id,source_domain,target_domain,site_type,status,stage,settings_snapshot,estimated_file_bytes,estimated_database_bytes,created_at,updated_at) VALUES (?,?,?,?,?,?,'draft','preflight_passed',?,?,?,?,?)`, item.ID, batchID, item.SourceSiteID, item.Domain, item.Domain, item.SiteType, item.snapshot, item.FileBytes, item.DatabaseBytes, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status,created_at,updated_at) VALUES (?,?,?,'source','active',?,?)`, item.Domain, item.SourceSiteID, item.ID, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SiteMigrationSourcePreparationService) verifyDeclaredBatch(ctx context.Context, tx *sql.Tx, batchID string, items []siteMigrationPreparedSourceDeclaration) error {
	for _, item := range items {
		var siteID int64
		var domain, siteType, snapshot, lockStatus string
		var fileBytes, databaseBytes int64
		if err := tx.QueryRowContext(ctx, `SELECT ms.source_site_id,ms.source_domain,ms.site_type,ms.settings_snapshot,ms.estimated_file_bytes,ms.estimated_database_bytes,ml.status FROM site_migration_sites ms JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='source' WHERE ms.id=? AND ms.batch_id=?`, item.ID, batchID).Scan(&siteID, &domain, &siteType, &snapshot, &fileBytes, &databaseBytes, &lockStatus); err != nil || siteID != item.SourceSiteID || domain != item.Domain || siteType != item.SiteType || fileBytes != item.FileBytes || databaseBytes != item.DatabaseBytes || !sourceSnapshotAliasesMatch(snapshot, item.Aliases) || lockStatus != "active" {
			return errors.New("source migration batch replay conflicts")
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites WHERE batch_id=?`, batchID).Scan(&count); err != nil || count != len(items) {
		return errors.New("source migration batch replay conflicts")
	}
	return tx.Commit()
}

func sourceSnapshotAliasesMatch(raw string, expected []string) bool {
	var snapshot struct {
		Aliases []string `json:"source_aliases"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil || len(snapshot.Aliases) != len(expected) {
		return false
	}
	for i := range expected {
		if snapshot.Aliases[i] != expected[i] {
			return false
		}
	}
	return true
}

func sameMigrationAliases(stored string, expected []string) bool {
	actual := splitMigrationAliases(stored)
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}

func (s *SiteMigrationSourcePreparationService) Prepare(ctx context.Context, peerID, migrationSiteID, markerToken, leaseOwner string) (SiteMigrationRuntimeSettings, error) {
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(migrationSiteID) || !siteMigrationMarkerPattern.MatchString(markerToken) {
		return SiteMigrationRuntimeSettings{}, errors.New("invalid source migration preparation")
	}
	var stage, status, snapshotRaw, storedLeaseOwner string
	var leaseExpires sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT ms.stage,ms.status,ms.settings_snapshot,ms.lease_owner,ms.lease_expires_at FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.peer_id=? AND mb.direction='source' AND mb.status='active' JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='source' AND ml.status='active' WHERE ms.id=?`, peerID, migrationSiteID).Scan(&stage, &status, &snapshotRaw, &storedLeaseOwner, &leaseExpires); err != nil {
		return SiteMigrationRuntimeSettings{}, errors.New("source migration preparation scope unavailable")
	}
	if status != "draft" && status != "failed_retryable" && status != "running" && status != "awaiting_cutover" {
		return SiteMigrationRuntimeSettings{}, errors.New("source migration preparation state unavailable")
	}
	allowedStages := map[string]bool{"preflight_passed": true, "source_freezing": true, "source_frozen": true, "manifesting": true, "manifest_ready": true, "transferring_files": true, "transferring_database": true}
	if !allowedStages[stage] {
		return SiteMigrationRuntimeSettings{}, errors.New("source migration preparation stage unavailable")
	}
	if stage != "preflight_passed" && stage != "source_freezing" {
		var snapshot struct {
			MarkerToken string `json:"source_marker_token"`
		}
		if json.Unmarshal([]byte(snapshotRaw), &snapshot) != nil || snapshot.MarkerToken != markerToken {
			return SiteMigrationRuntimeSettings{}, errors.New("source migration marker changed")
		}
	}
	databaseReady, certificatesReady, err := s.preparedArtifacts(ctx, migrationSiteID)
	if err != nil {
		return SiteMigrationRuntimeSettings{}, err
	}
	if status == "awaiting_cutover" {
		if stage != "transferring_database" || !databaseReady || !certificatesReady {
			return SiteMigrationRuntimeSettings{}, errors.New("source migration prepared state is incomplete")
		}
		return s.ops.Settings(ctx, migrationSiteID)
	}
	if status == "running" {
		if strings.TrimSpace(leaseOwner) == "" || storedLeaseOwner != leaseOwner || !leaseExpires.Valid || !leaseExpires.Time.After(s.now().UTC()) {
			return SiteMigrationRuntimeSettings{}, errors.New("source migration preparation lease unavailable")
		}
	} else {
		result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',error_code='',updated_at=? WHERE id=? AND status IN ('draft','failed_retryable')`, s.now().UTC(), migrationSiteID)
		if err != nil {
			return SiteMigrationRuntimeSettings{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return SiteMigrationRuntimeSettings{}, errors.New("source migration preparation state changed")
		}
	}
	if (stage == "preflight_passed" || stage == "source_freezing") && s.dataRoot != "" && s.availableBytes != nil {
		if err := s.requireSourcePreparationSpace(ctx, migrationSiteID); err != nil {
			return SiteMigrationRuntimeSettings{}, s.prepareFailed(migrationSiteID, err)
		}
	}
	steps := []struct {
		name   string
		stages map[string]bool
		run    func() error
	}{
		{"freeze", map[string]bool{"preflight_passed": true, "source_freezing": true}, func() error { return s.ops.Freeze(ctx, migrationSiteID, markerToken) }},
		{"manifest", map[string]bool{"preflight_passed": true, "source_freezing": true, "source_frozen": true, "manifesting": true}, func() error { return s.ops.BuildManifest(ctx, migrationSiteID) }},
		{"database", map[string]bool{"preflight_passed": true, "source_freezing": true, "source_frozen": true, "manifesting": true, "manifest_ready": true, "transferring_files": true, "transferring_database": true}, func() error { return s.ops.BuildDatabase(ctx, migrationSiteID) }},
	}
	for _, step := range steps {
		if step.stages[stage] {
			if step.name == "database" && databaseReady {
				continue
			}
			if err := step.run(); err != nil {
				return SiteMigrationRuntimeSettings{}, s.prepareFailed(migrationSiteID, err)
			}
		}
	}
	if err := s.ops.BuildCertificates(ctx, migrationSiteID); err != nil {
		return SiteMigrationRuntimeSettings{}, s.prepareFailed(migrationSiteID, err)
	}
	settings, err := s.ops.Settings(ctx, migrationSiteID)
	if err != nil {
		return SiteMigrationRuntimeSettings{}, s.prepareFailed(migrationSiteID, err)
	}
	if err := s.markPrepared(ctx, migrationSiteID); err != nil {
		return SiteMigrationRuntimeSettings{}, s.prepareFailed(migrationSiteID, err)
	}
	return settings, nil
}

func (s *SiteMigrationSourcePreparationService) requireSourcePreparationSpace(ctx context.Context, taskID string) error {
	var databaseBytes int64
	if err := s.db.QueryRowContext(ctx, `SELECT estimated_database_bytes FROM site_migration_sites WHERE id=?`, taskID).Scan(&databaseBytes); err != nil || databaseBytes < 0 {
		return errors.New("source migration space estimate unavailable")
	}
	const manifestAndWorkMargin int64 = 64 << 20
	if databaseBytes > int64(^uint64(0)>>1)-manifestAndWorkMargin {
		return errors.New("source migration space estimate overflow")
	}
	available, err := s.availableBytes(s.dataRoot)
	if err != nil || uint64(databaseBytes+manifestAndWorkMargin) > available {
		return errors.New("insufficient source migration preparation space")
	}
	return nil
}

func (s *SiteMigrationSourcePreparationService) markPrepared(ctx context.Context, taskID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='awaiting_cutover',error_code='',updated_at=? WHERE id=? AND status='running' AND stage='transferring_database'`, s.now().UTC(), taskID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("source migration preparation completion changed")
	}
	return nil
}

func (s *SiteMigrationSourcePreparationService) preparedArtifacts(ctx context.Context, taskID string) (bool, bool, error) {
	var databaseCount, certificateCount, sslEnabled int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id=ms.id AND artifact_type='database' AND status='verified'),
		(SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id=ms.id AND artifact_type IN ('certificate','private_key') AND status='verified'),
		w.ssl_enabled
		FROM site_migration_sites ms JOIN websites w ON w.id=ms.source_site_id WHERE ms.id=?`, taskID).Scan(&databaseCount, &certificateCount, &sslEnabled)
	if err != nil {
		return false, false, err
	}
	return databaseCount == 1, (sslEnabled == 0 && certificateCount == 0) || (sslEnabled == 1 && certificateCount == 2), nil
}

func (s *SiteMigrationSourcePreparationService) prepareFailed(taskID string, cause error) error {
	_, _ = s.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',error_code='source_prepare_failed',updated_at=? WHERE id=? AND status='running'`, s.now().UTC(), taskID)
	return cause
}
