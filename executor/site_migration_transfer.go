package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const siteMigrationTransferManifestPage = 10

type siteMigrationTransferSource interface {
	ListManifest(context.Context, string, string, int64, int) ([]SiteMigrationManifestEntry, int64, bool, error)
	DatabaseArtifact(context.Context, string, string) (SiteMigrationManifestEntry, error)
	CertificateArtifacts(context.Context, string, string) ([]SiteMigrationCertificateArtifact, error)
	ReadFileChunk(context.Context, string, string, string, int64, int64) (*SiteMigrationFileChunk, error)
	WriteFileShard(context.Context, string, string, int, int, int64, io.Writer) error
	ReadDatabaseChunk(context.Context, string, string, int64, int64) (*SiteMigrationFileChunk, error)
	ReadCertificateChunk(context.Context, string, string, string, int64, int64) (*SiteMigrationFileChunk, error)
	RuntimeSettings(context.Context, string, string) (SiteMigrationRuntimeSettings, error)
}

type siteMigrationTransferReceiver interface {
	Prepare(context.Context, string, []SiteMigrationManifestEntry, SiteMigrationManifestEntry) error
	PrepareCertificates(context.Context, string, []SiteMigrationCertificateArtifact) error
	WriteChunk(context.Context, string, string, string, int64, []byte, string) error
	ReceiveFileShard(context.Context, string, int, []SiteMigrationManifestEntry, func(io.Writer) error) error
	Finalize(context.Context, string) error
}

type SiteMigrationTargetTransferService struct {
	db       *sql.DB
	source   siteMigrationTransferSource
	receiver siteMigrationTransferReceiver
	now      func() time.Time
	activeMu sync.Mutex
	active   map[string]struct{}
}

func NewSiteMigrationTargetTransferService(db *sql.DB, source siteMigrationTransferSource, receiver siteMigrationTransferReceiver) (*SiteMigrationTargetTransferService, error) {
	if db == nil || source == nil || receiver == nil {
		return nil, errors.New("site migration target transfer unavailable")
	}
	return &SiteMigrationTargetTransferService{db: db, source: source, receiver: receiver, now: time.Now, active: make(map[string]struct{})}, nil
}

func (s *SiteMigrationTargetTransferService) Transfer(ctx context.Context, migrationSiteID, leaseOwner string) error {
	if !s.enter(migrationSiteID) {
		return errors.New("target transfer already running")
	}
	defer s.leave(migrationSiteID)
	peerID, err := s.begin(ctx, migrationSiteID, leaseOwner)
	if err != nil {
		return err
	}
	if err := s.transfer(ctx, peerID, migrationSiteID); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET status='failed_retryable',error_code='target_transfer_failed',updated_at=? WHERE id=? AND status='running' AND stage IN ('transferring_files','transferring_database') AND lease_owner=?`, s.now().UTC(), migrationSiteID, strings.TrimSpace(leaseOwner))
		return err
	}
	return nil
}

func (s *SiteMigrationTargetTransferService) enter(taskID string) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if _, exists := s.active[taskID]; exists {
		return false
	}
	s.active[taskID] = struct{}{}
	return true
}

func (s *SiteMigrationTargetTransferService) leave(taskID string) {
	s.activeMu.Lock()
	delete(s.active, taskID)
	s.activeMu.Unlock()
}

func (s *SiteMigrationTargetTransferService) begin(ctx context.Context, taskID, leaseOwner string) (string, error) {
	leaseOwner = strings.TrimSpace(leaseOwner)
	if !validSiteMigrationID(taskID) || leaseOwner == "" || len(leaseOwner) > 128 {
		return "", errors.New("invalid target transfer task")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var peerID, direction, batchStatus, status, stage, domain, storedOwner string
	var leaseExpires sql.NullTime
	now := s.now().UTC()
	if err := tx.QueryRowContext(ctx, `SELECT mb.peer_id,mb.direction,mb.status,ms.status,ms.stage,ms.target_domain,ms.lease_owner,ms.lease_expires_at FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id WHERE ms.id=?`, taskID).Scan(&peerID, &direction, &batchStatus, &status, &stage, &domain, &storedOwner, &leaseExpires); err != nil || direction != "target" || batchStatus != "active" {
		return "", errors.New("target transfer scope unavailable")
	}
	if storedOwner != leaseOwner || !leaseExpires.Valid || !leaseExpires.Time.After(now) {
		return "", errors.New("target transfer lease unavailable")
	}
	if status == "failed_retryable" && (stage == "transferring_files" || stage == "transferring_database") {
		result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',error_code='',updated_at=? WHERE id=? AND status='failed_retryable' AND stage=? AND lease_owner=? AND lease_expires_at>?`, now, taskID, stage, leaseOwner, now)
		if err != nil {
			return "", err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return "", errors.New("target transfer retry state changed")
		}
	} else if status == "running" && stage == "preflight_passed" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_locks(domain,migration_site_id,direction,status,created_at,updated_at) VALUES (?,?,'target','active',?,?)`, domain, taskID, now, now); err != nil {
			return "", err
		}
		result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET stage='transferring_files',error_code='',updated_at=? WHERE id=? AND status='running' AND stage='preflight_passed' AND lease_owner=? AND lease_expires_at>?`, now, taskID, leaseOwner, now)
		if err != nil {
			return "", err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return "", errors.New("target transfer start state changed")
		}
	} else if status != "running" || stage != "transferring_files" && stage != "transferring_database" {
		return "", errors.New("target transfer is not retryable")
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return peerID, nil
}

func (s *SiteMigrationTargetTransferService) transfer(ctx context.Context, peerID, taskID string) error {
	prepared, err := s.prepared(ctx, taskID)
	if err != nil {
		return err
	}
	if !prepared {
		files, err := s.loadManifest(ctx, peerID, taskID)
		if err != nil {
			return err
		}
		databaseArtifact, err := s.source.DatabaseArtifact(ctx, peerID, taskID)
		if err != nil {
			return err
		}
		if err := s.receiver.Prepare(ctx, taskID, files, databaseArtifact); err != nil {
			return err
		}
	}
	certPrepared, err := s.certificatesPrepared(ctx, taskID)
	if err != nil {
		return err
	}
	if !certPrepared {
		certificates, err := s.source.CertificateArtifacts(ctx, peerID, taskID)
		if err != nil {
			return err
		}
		if err := s.receiver.PrepareCertificates(ctx, taskID, certificates); err != nil {
			return err
		}
	}
	if err := s.transferPending(ctx, peerID, taskID); err != nil {
		return err
	}
	return s.receiver.Finalize(ctx, taskID)
}

func (s *SiteMigrationTargetTransferService) transferFileShards(ctx context.Context, peerID, taskID string) error {
	entries, err := loadMigrationShardEntries(ctx, s.db, taskID)
	if err != nil {
		return err
	}
	for index, shard := range partitionMigrationFileShards(entries) {
		pending := false
		manifest := make([]SiteMigrationManifestEntry, 0, len(shard))
		var bytes int64
		for _, entry := range shard {
			manifest = append(manifest, entry.SiteMigrationManifestEntry)
			bytes += entry.Size
			pending = pending || entry.Status != "verified"
		}
		if !pending {
			continue
		}
		entryCount := len(manifest)
		if err := s.receiver.ReceiveFileShard(ctx, taskID, index, manifest, func(dst io.Writer) error {
			return s.source.WriteFileShard(ctx, peerID, taskID, index, entryCount, bytes, dst)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *SiteMigrationTargetTransferService) loadManifest(ctx context.Context, peerID, taskID string) ([]SiteMigrationManifestEntry, error) {
	var all []SiteMigrationManifestEntry
	var after int64
	for {
		entries, next, more, err := s.source.ListManifest(ctx, peerID, taskID, after, siteMigrationTransferManifestPage)
		if err != nil {
			return nil, err
		}
		if len(entries) > siteMigrationTransferManifestPage || more != (len(entries) == siteMigrationTransferManifestPage) || next < after || len(entries) == 0 && more || next == after && len(entries) != 0 {
			return nil, errors.New("source manifest pagination changed")
		}
		all = append(all, entries...)
		if len(all) > 1_000_000 {
			return nil, errors.New("source manifest is too large")
		}
		if !more {
			return all, nil
		}
		after = next
	}
}

func (s *SiteMigrationTargetTransferService) prepared(ctx context.Context, taskID string) (bool, error) {
	var resources, databaseArtifacts int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, taskID).Scan(&resources); err != nil {
		return false, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type='database'`, taskID).Scan(&databaseArtifacts); err != nil {
		return false, err
	}
	if resources == 0 && databaseArtifacts == 0 {
		return false, nil
	}
	if resources != 1 || databaseArtifacts != 1 {
		return false, errors.New("target transfer manifest state is inconsistent")
	}
	return true, nil
}

func (s *SiteMigrationTargetTransferService) certificatesPrepared(ctx context.Context, taskID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type IN ('certificate','private_key')`, taskID).Scan(&count); err != nil {
		return false, err
	}
	if count != 0 && count != 2 {
		return false, errors.New("target certificate transfer state is inconsistent")
	}
	return count == 2, nil
}

func (s *SiteMigrationTargetTransferService) transferPending(ctx context.Context, peerID, taskID string) error {
	if err := s.transferFileShards(ctx, peerID, taskID); err != nil {
		return err
	}
	for {
		var artifactType, relativePath, expectedHash string
		var size, offset int64
		err := s.db.QueryRowContext(ctx, `SELECT artifact_type,relative_path,file_size,confirmed_offset,sha256 FROM site_migration_artifacts WHERE migration_site_id=? AND entry_type='file' AND status IN ('pending','transferring') AND (artifact_type!='file' OR file_size>?) ORDER BY id LIMIT 1`, taskID, SiteMigrationFileShardBytes).Scan(&artifactType, &relativePath, &size, &offset, &expectedHash)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		length := SiteMigrationMaxChunkSize
		if size-offset < length {
			length = size - offset
		}
		if length <= 0 {
			return errors.New("target transfer artifact offset is invalid")
		}
		var chunk *SiteMigrationFileChunk
		switch artifactType {
		case "file":
			chunk, err = s.source.ReadFileChunk(ctx, peerID, taskID, relativePath, offset, length)
		case "database":
			chunk, err = s.source.ReadDatabaseChunk(ctx, peerID, taskID, offset, length)
		case "certificate", "private_key":
			chunk, err = s.source.ReadCertificateChunk(ctx, peerID, taskID, artifactType, offset, length)
		default:
			return errors.New("target transfer artifact type is invalid")
		}
		if err != nil {
			return err
		}
		if chunk == nil || chunk.RelativePath != relativePath || chunk.Offset != offset || chunk.TotalSize != size || chunk.FileSHA256 != expectedHash || int64(len(chunk.Data)) != length {
			return fmt.Errorf("source %s chunk metadata changed", artifactType)
		}
		if err := s.receiver.WriteChunk(ctx, taskID, artifactType, relativePath, offset, chunk.Data, chunk.ChunkSHA256); err != nil {
			return err
		}
	}
}
