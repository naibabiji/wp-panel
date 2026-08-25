package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const siteMigrationMaxCertificateSize int64 = 16 << 20

type SiteMigrationCertificateArtifact struct {
	ArtifactType string `json:"artifact_type"`
	RelativePath string `json:"relative_path"`
	Mode         uint32 `json:"mode"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}

type siteMigrationSourceCertificates struct {
	db   *sql.DB
	root string
	now  func() time.Time
}

func newSiteMigrationSourceCertificates(db *sql.DB, root string) (*siteMigrationSourceCertificates, error) {
	if db == nil || root == "" {
		return nil, errors.New("site migration certificate source unavailable")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	return &siteMigrationSourceCertificates{db: db, root: root, now: time.Now}, nil
}

func (s *siteMigrationSourceCertificates) Build(ctx context.Context, migrationSiteID string) error {
	var enabled int
	var certPath, keyPath, stage string
	err := s.db.QueryRowContext(ctx, `SELECT w.ssl_enabled,w.ssl_cert_path,w.ssl_key_path,ms.stage FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source' AND mb.status='active'
		JOIN websites w ON w.id=ms.source_site_id
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.site_id=w.id AND ml.direction='source' AND ml.status='active'
		WHERE ms.id=?`, migrationSiteID).Scan(&enabled, &certPath, &keyPath, &stage)
	if err != nil || stage != "transferring_database" {
		return errors.New("source certificate scope unavailable")
	}
	if enabled == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type IN ('certificate','private_key')`, migrationSiteID)
		return err
	}
	if !filepath.IsAbs(certPath) || !filepath.IsAbs(keyPath) {
		return errors.New("source certificate paths unavailable")
	}
	siteRoot := filepath.Join(s.root, migrationSiteID)
	if err := os.RemoveAll(siteRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(siteRoot, 0700); err != nil {
		return err
	}
	type source struct {
		kind, name, path string
		mode             os.FileMode
	}
	items := []source{
		{kind: "certificate", name: "certificate.pem", path: certPath, mode: 0644},
		{kind: "private_key", name: "private-key.pem", path: keyPath, mode: 0600},
	}
	type builtArtifact struct {
		source
		modified int64
		size     int64
		hash     string
		target   string
	}
	built := make([]builtArtifact, 0, len(items))
	for _, item := range items {
		target := filepath.Join(siteRoot, item.name)
		if err := copyMigrationCertificate(item.path, target, item.mode); err != nil {
			_ = os.RemoveAll(siteRoot)
			return err
		}
		info, hash, err := hashMigrationArtifact(target)
		if err != nil {
			_ = os.RemoveAll(siteRoot)
			return err
		}
		built = append(built, builtArtifact{source: item, modified: info.ModTime().UnixNano(), size: info.Size(), hash: hash, target: target})
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type IN ('certificate','private_key')`, migrationSiteID); err != nil {
		return err
	}
	for _, item := range built {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_artifacts
			(migration_site_id,relative_path,artifact_type,entry_type,file_mode,modified_unix_ns,file_size,sha256,staging_key,status)
			VALUES (?,? ,?,'file',?,?,?,?,?,'verified')`, migrationSiteID, item.name, item.kind, uint32(item.mode.Perm()), item.modified, item.size, item.hash, item.target); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func copyMigrationCertificate(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > siteMigrationMaxCertificateSize {
		return errors.New("source certificate file unavailable")
	}
	tmp := target + ".partial"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = out.Close()
		if !keep {
			_ = os.Remove(tmp)
		}
	}()
	written, err := io.Copy(out, io.LimitReader(in, siteMigrationMaxCertificateSize+1))
	if err != nil || written != info.Size() {
		return errors.New("copy source certificate failed")
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	if err := syncMigrationDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	keep = true
	return nil
}

func (s *siteMigrationSourceCertificates) List(ctx context.Context, migrationSiteID string) ([]SiteMigrationCertificateArtifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT artifact_type,relative_path,file_mode,file_size,sha256 FROM site_migration_artifacts
		WHERE migration_site_id=? AND artifact_type IN ('certificate','private_key') AND status='verified' ORDER BY artifact_type`, migrationSiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SiteMigrationCertificateArtifact
	for rows.Next() {
		var item SiteMigrationCertificateArtifact
		if err := rows.Scan(&item.ArtifactType, &item.RelativePath, &item.Mode, &item.Size, &item.SHA256); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *siteMigrationSourceCertificates) ReadChunk(ctx context.Context, migrationSiteID, artifactType string, offset, length int64) (*SiteMigrationFileChunk, error) {
	if artifactType != "certificate" && artifactType != "private_key" || offset < 0 || length <= 0 || length > SiteMigrationMaxChunkSize {
		return nil, errors.New("invalid certificate chunk")
	}
	var path, relativePath, hash string
	var size, modified int64
	err := s.db.QueryRowContext(ctx, `SELECT staging_key,relative_path,file_size,modified_unix_ns,sha256 FROM site_migration_artifacts
		WHERE migration_site_id=? AND artifact_type=? AND status='verified'`, migrationSiteID, artifactType).
		Scan(&path, &relativePath, &size, &modified, &hash)
	if err != nil || offset > size || length > size-offset {
		return nil, errors.New("verified certificate artifact unavailable")
	}
	rel := filepath.ToSlash(filepath.Join(migrationSiteID, relativePath))
	file, info, err := openMigrationSourceFile(s.root, rel)
	if err != nil || filepath.Clean(path) != filepath.Join(s.root, migrationSiteID, relativePath) {
		return nil, errors.New("certificate artifact path invalid")
	}
	defer file.Close()
	if info.Size() != size || info.ModTime().UnixNano() != modified {
		return nil, errors.New("certificate artifact changed")
	}
	data := make([]byte, length)
	n, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if int64(n) != length {
		return nil, errors.New("short certificate artifact read")
	}
	after, err := file.Stat()
	if err != nil || after.Size() != size || after.ModTime().UnixNano() != modified {
		return nil, errors.New("certificate artifact changed during read")
	}
	sum := sha256.Sum256(data)
	return &SiteMigrationFileChunk{RelativePath: relativePath, Offset: offset, TotalSize: size, FileSHA256: hash, ChunkSHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}

func (s *SiteMigrationSourceService) BuildCertificateArtifacts(ctx context.Context, migrationSiteID string) error {
	return s.certificates.Build(ctx, migrationSiteID)
}

func (s *SiteMigrationSourceService) ListCertificateArtifacts(ctx context.Context, peerID, migrationSiteID, bearer string) ([]SiteMigrationCertificateArtifact, error) {
	if err := s.authorizeCertificate(ctx, peerID, migrationSiteID, bearer); err != nil {
		return nil, err
	}
	return s.certificates.List(ctx, migrationSiteID)
}

func (s *SiteMigrationSourceService) ReadCertificateChunk(ctx context.Context, peerID, migrationSiteID, bearer, artifactType string, offset, length int64) (*SiteMigrationFileChunk, error) {
	if err := s.authorizeCertificate(ctx, peerID, migrationSiteID, bearer); err != nil {
		return nil, err
	}
	return s.certificates.ReadChunk(ctx, migrationSiteID, artifactType, offset, length)
}

func (s *SiteMigrationSourceService) authorizeCertificate(ctx context.Context, peerID, migrationSiteID, bearer string) error {
	if err := s.pairing.AuthorizePeer(ctx, peerID, bearer); err != nil {
		return err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='source' AND ml.status='active'
		WHERE ms.id=? AND mb.peer_id=? AND ms.stage='transferring_database'`, migrationSiteID, peerID).Scan(&count)
	if err != nil || count != 1 {
		return fmt.Errorf("certificate authorization rejected: %w", ErrSiteMigrationPairRejected)
	}
	return nil
}
