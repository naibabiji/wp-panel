package executor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

const SiteMigrationMaxChunkSize int64 = 4 << 20

type SiteMigrationManifestEntry struct {
	RelativePath   string `json:"relative_path"`
	EntryType      string `json:"entry_type"`
	Mode           uint32 `json:"mode"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256,omitempty"`
	LinkTarget     string `json:"link_target,omitempty"`
	ModifiedUnixNS int64  `json:"modified_unix_ns"`
}

type SiteMigrationFileChunk struct {
	RelativePath string
	Offset       int64
	TotalSize    int64
	FileSHA256   string
	ChunkSHA256  string
	Data         []byte
}

type siteMigrationSourceFiles struct {
	db      *sql.DB
	now     func() time.Time
	workDir string
}

type SiteMigrationSourceService struct {
	db           *sql.DB
	pairing      *SiteMigrationPairingService
	files        *siteMigrationSourceFiles
	database     *siteMigrationSourceDatabase
	certificates *siteMigrationSourceCertificates
	settings     *SiteMigrationSettingsService
}

func NewSiteMigrationSourceService(db *sql.DB, pairing *SiteMigrationPairingService) (*SiteMigrationSourceService, error) {
	if pairing == nil {
		return nil, errors.New("site migration pairing unavailable")
	}
	manifestRoot := filepath.Join(os.TempDir(), "wp-panel-site-migration", "manifests")
	if config.AppConfig != nil && config.AppConfig.Panel.DataDir != "" {
		manifestRoot = filepath.Join(config.AppConfig.Panel.DataDir, "site-migration", "manifests")
	}
	files, err := newSiteMigrationSourceFiles(db, manifestRoot)
	if err != nil {
		return nil, err
	}
	databaseRoot := filepath.Join(os.TempDir(), "wp-panel-site-migration-database")
	if config.AppConfig != nil && config.AppConfig.Panel.DataDir != "" {
		databaseRoot = filepath.Join(config.AppConfig.Panel.DataDir, "site-migration", "database")
	}
	databaseSource, err := newSiteMigrationSourceDatabase(db, databaseRoot, defaultSiteMigrationDatabaseDumper)
	if err != nil {
		return nil, err
	}
	certificateRoot := filepath.Join(os.TempDir(), "wp-panel-site-migration-certificates")
	if config.AppConfig != nil && config.AppConfig.Panel.DataDir != "" {
		certificateRoot = filepath.Join(config.AppConfig.Panel.DataDir, "site-migration", "certificates")
	}
	certificates, err := newSiteMigrationSourceCertificates(db, certificateRoot)
	if err != nil {
		return nil, err
	}
	settings, err := NewSiteMigrationSettingsService(db)
	if err != nil {
		return nil, err
	}
	return &SiteMigrationSourceService{db: db, pairing: pairing, files: files, database: databaseSource, certificates: certificates, settings: settings}, nil
}

func (s *SiteMigrationSourceService) GetRuntimeSettings(ctx context.Context, peerID, migrationSiteID, bearer string) (SiteMigrationRuntimeSettings, error) {
	if err := s.pairing.AuthorizePeer(ctx, peerID, bearer); err != nil {
		return SiteMigrationRuntimeSettings{}, err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='source' AND ml.status='active' WHERE ms.id=? AND mb.peer_id=? AND mb.direction='source' AND mb.status='active' AND ms.status='awaiting_cutover' AND ms.stage='transferring_database'`, migrationSiteID, peerID).Scan(&count)
	if err != nil || count != 1 {
		return SiteMigrationRuntimeSettings{}, ErrSiteMigrationPairRejected
	}
	return s.settings.SourceSettings(ctx, migrationSiteID)
}

func (s *SiteMigrationSourceService) BuildFileManifest(ctx context.Context, migrationSiteID string) error {
	return s.files.BuildManifest(ctx, migrationSiteID)
}

func (s *SiteMigrationSourceService) BuildDatabaseExport(ctx context.Context, migrationSiteID string) error {
	return s.database.BuildExport(ctx, migrationSiteID)
}

func (s *SiteMigrationSourceService) ReadDatabaseChunk(ctx context.Context, peerID, migrationSiteID, bearer string, offset, length int64) (*SiteMigrationFileChunk, error) {
	if err := s.authorizeDatabase(ctx, peerID, migrationSiteID, bearer); err != nil {
		return nil, err
	}
	return s.database.ReadChunk(ctx, migrationSiteID, offset, length)
}

func (s *SiteMigrationSourceService) GetDatabaseArtifact(ctx context.Context, peerID, migrationSiteID, bearer string) (SiteMigrationManifestEntry, error) {
	if err := s.authorizeDatabase(ctx, peerID, migrationSiteID, bearer); err != nil {
		return SiteMigrationManifestEntry{}, err
	}
	var item SiteMigrationManifestEntry
	err := s.db.QueryRowContext(ctx, `SELECT relative_path,entry_type,file_mode,file_size,sha256,modified_unix_ns
		FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type='database' AND status='verified'`, migrationSiteID).
		Scan(&item.RelativePath, &item.EntryType, &item.Mode, &item.Size, &item.SHA256, &item.ModifiedUnixNS)
	return item, err
}

func (s *SiteMigrationSourceService) authorizeDatabase(ctx context.Context, peerID, migrationSiteID, bearer string) error {
	if err := s.pairing.AuthorizePeer(ctx, peerID, bearer); err != nil {
		return err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.status='active' AND ml.direction='source'
		JOIN site_migration_artifacts ma ON ma.migration_site_id=ms.id AND ma.artifact_type='database' AND ma.status='verified'
		WHERE ms.id=? AND mb.peer_id=? AND mb.direction='source' AND mb.status='active' AND ms.stage='transferring_database'`, migrationSiteID, peerID).Scan(&count)
	if err != nil || count != 1 {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationSourceService) authorize(ctx context.Context, peerID, migrationSiteID, bearer string) error {
	if err := s.pairing.AuthorizePeer(ctx, peerID, bearer); err != nil {
		return err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.status='active' AND ml.direction='source'
		WHERE ms.id=? AND mb.peer_id=? AND mb.direction='source' AND mb.status='active'
		AND ms.stage IN ('manifest_ready','transferring_files','transferring_database')`, migrationSiteID, peerID).Scan(&count)
	if err != nil || count != 1 {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationSourceService) ListManifest(ctx context.Context, peerID, migrationSiteID, bearer string, afterID int64, limit int) ([]SiteMigrationManifestEntry, int64, error) {
	if afterID < 0 || limit <= 0 || limit > 1000 || !validSiteMigrationID(migrationSiteID) {
		return nil, 0, errors.New("invalid manifest page")
	}
	if err := s.authorize(ctx, peerID, migrationSiteID, bearer); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,relative_path,entry_type,file_mode,file_size,sha256,link_target,modified_unix_ns
		FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type='file' AND status='verified' AND id>?
		ORDER BY id LIMIT ?`, migrationSiteID, afterID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := make([]SiteMigrationManifestEntry, 0, limit)
	next := afterID
	for rows.Next() {
		var id int64
		var item SiteMigrationManifestEntry
		if err := rows.Scan(&id, &item.RelativePath, &item.EntryType, &item.Mode, &item.Size, &item.SHA256, &item.LinkTarget, &item.ModifiedUnixNS); err != nil {
			return nil, 0, err
		}
		entries, next = append(entries, item), id
	}
	return entries, next, rows.Err()
}

func (s *SiteMigrationSourceService) ReadFileChunk(ctx context.Context, peerID, migrationSiteID, bearer, relativePath string, offset, length int64) (*SiteMigrationFileChunk, error) {
	if err := s.authorize(ctx, peerID, migrationSiteID, bearer); err != nil {
		return nil, err
	}
	return s.files.ReadChunk(ctx, migrationSiteID, relativePath, offset, length)
}

func (s *SiteMigrationSourceService) FileShard(ctx context.Context, peerID, migrationSiteID, bearer string, index int) (*SiteMigrationFileShard, error) {
	if err := s.authorize(ctx, peerID, migrationSiteID, bearer); err != nil {
		return nil, err
	}
	return s.files.FileShard(ctx, migrationSiteID, index)
}

func newSiteMigrationSourceFiles(db *sql.DB, workDir string) (*siteMigrationSourceFiles, error) {
	if db == nil {
		return nil, errors.New("site migration database unavailable")
	}
	if !filepath.IsAbs(workDir) {
		return nil, errors.New("site migration manifest directory must be absolute")
	}
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(workDir, 0700); err != nil {
		return nil, err
	}
	return &siteMigrationSourceFiles{db: db, now: time.Now, workDir: workDir}, nil
}

func (s *siteMigrationSourceFiles) BuildManifest(ctx context.Context, migrationSiteID string) error {
	root, stage, err := s.sourceRoot(ctx, migrationSiteID)
	if err != nil {
		return err
	}
	if stage != "source_frozen" && stage != "manifesting" {
		return errors.New("source site is not frozen for manifest")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve source root: %w", err)
	}
	info, err := os.Stat(realRoot)
	if err != nil || !info.IsDir() {
		return errors.New("source root is not a directory")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET stage='manifesting',updated_at=? WHERE id=? AND stage IN ('source_frozen','manifesting')`, s.now().UTC(), migrationSiteID); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(s.workDir, "manifest-*.jsonl")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	walkErr := filepath.WalkDir(realRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == realRoot {
			return nil
		}
		rel, err := filepath.Rel(realRoot, path)
		if err != nil {
			return err
		}
		rel, err = normalizeMigrationRelativePath(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		item, err := manifestEntryForPath(realRoot, path, rel, entry)
		if err != nil {
			return err
		}
		return encoder.Encode(item)
	})
	closeErr := tmp.Close()
	if walkErr != nil || closeErr != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET stage='source_frozen',error_code='manifest_failed',updated_at=? WHERE id=? AND stage='manifesting'`, s.now().UTC(), migrationSiteID)
		if walkErr != nil {
			return walkErr
		}
		return closeErr
	}
	if err := s.persistManifest(ctx, migrationSiteID, tmpPath); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET stage='source_frozen',error_code='manifest_failed',updated_at=? WHERE id=? AND stage='manifesting'`, s.now().UTC(), migrationSiteID)
		return err
	}
	return nil
}

func (s *siteMigrationSourceFiles) sourceRoot(ctx context.Context, migrationSiteID string) (string, string, error) {
	if !validSiteMigrationID(migrationSiteID) {
		return "", "", errors.New("invalid migration site ID")
	}
	var root, stage string
	err := s.db.QueryRowContext(ctx, `SELECT w.web_root,ms.stage FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source' AND mb.status='active'
		JOIN websites w ON w.id=ms.source_site_id
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.site_id=w.id AND ml.status='active'
		WHERE ms.id=?`, migrationSiteID).Scan(&root, &stage)
	if err != nil {
		return "", "", fmt.Errorf("load source file scope: %w", err)
	}
	if !filepath.IsAbs(root) {
		return "", "", errors.New("source root must be absolute")
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", "", fmt.Errorf("resolve source root: %w", err)
	}
	return realRoot, stage, nil
}

func manifestEntryForPath(root, path, rel string, dirEntry os.DirEntry) (SiteMigrationManifestEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return SiteMigrationManifestEntry{}, err
	}
	item := SiteMigrationManifestEntry{RelativePath: rel, Mode: uint32(info.Mode().Perm()), Size: info.Size(), ModifiedUnixNS: info.ModTime().UnixNano()}
	switch {
	case info.Mode().IsDir():
		item.EntryType = "directory"
	case info.Mode().IsRegular():
		item.EntryType = "file"
		file, openedInfo, err := openMigrationSourceFile(root, rel)
		if err != nil {
			return item, err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		finalInfo, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil {
			return item, copyErr
		}
		if statErr != nil {
			return item, statErr
		}
		if closeErr != nil {
			return item, closeErr
		}
		if openedInfo.Size() != finalInfo.Size() || openedInfo.ModTime() != finalInfo.ModTime() {
			return item, errors.New("source file changed while hashing")
		}
		item.Size, item.ModifiedUnixNS, item.SHA256 = finalInfo.Size(), finalInfo.ModTime().UnixNano(), hex.EncodeToString(hash.Sum(nil))
	case info.Mode()&os.ModeSymlink != 0:
		item.EntryType = "symlink"
		target, err := os.Readlink(path)
		if err != nil {
			return item, err
		}
		if filepath.IsAbs(target) {
			return item, errors.New("absolute symlink is not allowed")
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
		if !migrationPathWithin(root, resolved) {
			return item, errors.New("symlink escapes source root")
		}
		evaluated, err := filepath.EvalSymlinks(resolved)
		if err != nil {
			return item, fmt.Errorf("resolve source symlink: %w", err)
		}
		if !migrationPathWithin(root, evaluated) {
			return item, errors.New("symlink resolves outside source root")
		}
		item.LinkTarget, item.Size = filepath.ToSlash(target), int64(len(target))
		sum := sha256.Sum256([]byte(item.LinkTarget))
		item.SHA256 = hex.EncodeToString(sum[:])
	default:
		return item, fmt.Errorf("unsupported source file type: %s", rel)
	}
	_ = dirEntry
	return item, nil
}

func normalizeMigrationRelativePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") || filepath.IsAbs(value) {
		return "", errors.New("invalid migration relative path")
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", errors.New("invalid migration relative path")
	}
	return clean, nil
}

func migrationPathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func openMigrationSourceFile(root, relativePath string) (*os.File, os.FileInfo, error) {
	rel, err := normalizeMigrationRelativePath(relativePath)
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	closeOnError := func(err error) (*os.File, os.FileInfo, error) { _ = file.Close(); return nil, nil, err }
	info, err := file.Stat()
	if err != nil {
		return closeOnError(err)
	}
	if !info.Mode().IsRegular() {
		return closeOnError(errors.New("source chunk path is not a regular file"))
	}
	actual, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return closeOnError(fmt.Errorf("verify opened source file: %w", err))
	}
	if !migrationPathWithin(root, strings.TrimSuffix(actual, " (deleted)")) {
		return closeOnError(errors.New("opened source file escaped root"))
	}
	return file, info, nil
}

func (s *siteMigrationSourceFiles) persistManifest(ctx context.Context, migrationSiteID, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type='file'`, migrationSiteID); err != nil {
		return err
	}
	decoder := json.NewDecoder(bufio.NewReader(file))
	const batchSize = 500
	var tx *sql.Tx
	count := 0
	begin := func() error {
		var err error
		tx, err = s.db.BeginTx(ctx, nil)
		return err
	}
	if err := begin(); err != nil {
		return err
	}
	for {
		var item SiteMigrationManifestEntry
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_artifacts
			(migration_site_id,relative_path,artifact_type,entry_type,file_mode,link_target,modified_unix_ns,file_size,sha256,status)
			VALUES (?,?,'file',?,?,?,?,?,?,'verified')`, migrationSiteID, item.RelativePath, item.EntryType, item.Mode, item.LinkTarget, item.ModifiedUnixNS, item.Size, item.SHA256); err != nil {
			_ = tx.Rollback()
			return err
		}
		count++
		if count%batchSize == 0 {
			if err := tx.Commit(); err != nil {
				return err
			}
			if err := begin(); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET stage='manifest_ready',error_code='',updated_at=? WHERE id=? AND stage='manifesting'`, s.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("source manifest state changed")
	}
	return nil
}

func (s *siteMigrationSourceFiles) ReadChunk(ctx context.Context, migrationSiteID, relativePath string, offset, length int64) (*SiteMigrationFileChunk, error) {
	if offset < 0 || length <= 0 || length > SiteMigrationMaxChunkSize {
		return nil, errors.New("invalid source chunk range")
	}
	rel, err := normalizeMigrationRelativePath(relativePath)
	if err != nil {
		return nil, err
	}
	root, stage, err := s.sourceRoot(ctx, migrationSiteID)
	if err != nil {
		return nil, err
	}
	if stage != "manifest_ready" && stage != "transferring_files" && stage != "transferring_database" {
		return nil, errors.New("source manifest is not available")
	}
	var size, modified int64
	var hash, entryType string
	if err := s.db.QueryRowContext(ctx, `SELECT file_size,modified_unix_ns,sha256,entry_type FROM site_migration_artifacts
		WHERE migration_site_id=? AND artifact_type='file' AND relative_path=? AND status='verified'`, migrationSiteID, rel).Scan(&size, &modified, &hash, &entryType); err != nil {
		return nil, errors.New("source file is not in the verified manifest")
	}
	if entryType != "file" || offset > size || length > size-offset {
		return nil, errors.New("invalid source chunk range")
	}
	file, info, err := openMigrationSourceFile(root, rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() != size || info.ModTime().UnixNano() != modified {
		return nil, errors.New("source file changed after manifest")
	}
	data := make([]byte, length)
	if _, err := file.ReadAt(data, offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || after.Size() != size || after.ModTime().UnixNano() != modified {
		return nil, errors.New("source file changed during chunk read")
	}
	sum := sha256.Sum256(data)
	return &SiteMigrationFileChunk{RelativePath: rel, Offset: offset, TotalSize: size, FileSHA256: hash, ChunkSHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}
