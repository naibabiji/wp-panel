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
	"sort"
	"syscall"
	"time"
)

type SiteMigrationTargetReceiver struct {
	db        *sql.DB
	root      string
	now       func() time.Time
	removeAll func(string) error
	available func(string) (uint64, error)
}

func NewSiteMigrationTargetReceiver(db *sql.DB, root string) (*SiteMigrationTargetReceiver, error) {
	if db == nil || root == "" {
		return nil, errors.New("site migration target receiver unavailable")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	return &SiteMigrationTargetReceiver{db: db, root: root, now: time.Now, removeAll: os.RemoveAll, available: migrationAvailableBytes}, nil
}

func (r *SiteMigrationTargetReceiver) Prepare(ctx context.Context, migrationSiteID string, files []SiteMigrationManifestEntry, databaseArtifact SiteMigrationManifestEntry) error {
	if err := r.authorizeTarget(ctx, migrationSiteID, "transferring_files"); err != nil {
		return err
	}
	if len(files) > 1_000_000 || databaseArtifact.RelativePath != siteMigrationDatabaseArtifactName || databaseArtifact.EntryType != "file" || databaseArtifact.Size <= 0 || databaseArtifact.Mode&^0777 != 0 || !validMigrationSHA256(databaseArtifact.SHA256) {
		return errors.New("invalid target migration manifest")
	}
	seen := make(map[string]struct{}, len(files))
	declaredSize := databaseArtifact.Size
	for _, item := range files {
		if err := validateTargetManifestEntry(item); err != nil {
			return err
		}
		if _, exists := seen[item.RelativePath]; exists {
			return errors.New("duplicate target manifest path")
		}
		seen[item.RelativePath] = struct{}{}
		if item.Size > 0 && declaredSize > int64(^uint64(0)>>1)-item.Size {
			return errors.New("target migration size overflow")
		}
		declaredSize += item.Size
	}
	if err := validateTargetManifestStructure(files); err != nil {
		return err
	}
	siteRoot := filepath.Join(r.root, migrationSiteID)
	available, err := r.available(r.root)
	if err != nil || uint64(declaredSize) > available {
		return errors.New("insufficient target staging space")
	}
	var existing int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id=?`, migrationSiteID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return errors.New("target migration manifest already prepared")
	}
	if err := os.RemoveAll(siteRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(siteRoot, "files"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(siteRoot, "database"), 0700); err != nil {
		return err
	}
	for _, item := range files {
		if item.EntryType != "file" || item.Size != 0 {
			continue
		}
		path := filepath.Join(siteRoot, "files", filepath.FromSlash(item.RelativePath))
		if err := ensureMigrationTargetParents(filepath.Join(siteRoot, "files"), filepath.Dir(path)); err != nil {
			return err
		}
		if err := os.WriteFile(path, nil, os.FileMode(item.Mode).Perm()); err != nil {
			return err
		}
		if err := os.Chmod(path, os.FileMode(item.Mode).Perm()); err != nil {
			return err
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_resources
		(migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at)
		VALUES (?,'target_staging_root',?,?,'created',?,?)`, migrationSiteID, siteRoot, migrationSiteID, r.now().UTC(), r.now().UTC()); err != nil {
		return err
	}
	for _, item := range files {
		status := "pending"
		if item.EntryType == "directory" || item.EntryType == "symlink" || item.EntryType == "file" && item.Size == 0 {
			status = "verified"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_artifacts
			(migration_site_id,relative_path,artifact_type,entry_type,file_mode,link_target,modified_unix_ns,file_size,sha256,status)
			VALUES (?,?,'file',?,?,?,?,?,?,?)`, migrationSiteID, item.RelativePath, item.EntryType, item.Mode, item.LinkTarget, item.ModifiedUnixNS, item.Size, item.SHA256, status); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_artifacts
		(migration_site_id,relative_path,artifact_type,entry_type,file_mode,modified_unix_ns,file_size,sha256,status)
		VALUES (?,?,'database','file',?,?,?,?,'pending')`, migrationSiteID, databaseArtifact.RelativePath, databaseArtifact.Mode, databaseArtifact.ModifiedUnixNS, databaseArtifact.Size, databaseArtifact.SHA256); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SiteMigrationTargetReceiver) Cleanup(ctx context.Context, migrationSiteID string) error {
	if err := r.authorizeTarget(ctx, migrationSiteID, "transferring_files", "transferring_database", "preparing_target", "cancelling"); err != nil {
		return err
	}
	siteRoot := filepath.Join(r.root, migrationSiteID)
	var identifier, ownership string
	err := r.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag FROM site_migration_resources
		WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, migrationSiteID).Scan(&identifier, &ownership)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (filepath.Clean(identifier) != siteRoot || ownership != migrationSiteID) {
		return errors.New("target staging ownership mismatch")
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='cancelling',stage='cancelling',cleanup_status='running',updated_at=? WHERE id=?`, r.now().UTC(), migrationSiteID); err != nil {
		return err
	}
	if err := r.removeAll(siteRoot); err != nil {
		_, _ = r.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET status='cleanup_failed',cleanup_status='failed',error_code='target_cleanup_failed',updated_at=? WHERE id=?`, r.now().UTC(), migrationSiteID)
		return fmt.Errorf("remove target staging root: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_artifacts WHERE migration_site_id=?`, migrationSiteID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_migration_resources SET status='removed',updated_at=? WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, r.now().UTC(), migrationSiteID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_locks SET status='released',released_at=?,updated_at=? WHERE migration_site_id=? AND direction='target' AND status='active'`, r.now().UTC(), r.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("target cleanup lock state changed")
	}
	result, err = tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='abandoned',stage='abandoned',cleanup_status='complete',error_code='',updated_at=?,finished_at=? WHERE id=? AND status='cancelling'`, r.now().UTC(), r.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("target cleanup state changed")
	}
	return tx.Commit()
}

func (r *SiteMigrationTargetReceiver) PrepareCertificates(ctx context.Context, migrationSiteID string, items []SiteMigrationCertificateArtifact) error {
	if err := r.authorizeTarget(ctx, migrationSiteID, "transferring_files", "transferring_database"); err != nil {
		return err
	}
	if len(items) != 0 && len(items) != 2 {
		return errors.New("invalid target certificate manifest")
	}
	var prepared int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, migrationSiteID).Scan(&prepared); err != nil || prepared != 1 {
		return errors.New("target staging manifest is not prepared")
	}
	seen := make(map[string]struct{}, len(items))
	var declaredSize uint64
	for _, item := range items {
		if item.ArtifactType != "certificate" && item.ArtifactType != "private_key" || item.Size <= 0 || item.Size > siteMigrationMaxCertificateSize || item.Mode&^0777 != 0 || !validMigrationSHA256(item.SHA256) {
			return errors.New("invalid target certificate artifact")
		}
		wantPath := "certificate.pem"
		wantMode := uint32(0644)
		if item.ArtifactType == "private_key" {
			wantPath = "private-key.pem"
			wantMode = 0600
		}
		if item.RelativePath != wantPath || item.Mode != wantMode {
			return errors.New("invalid target certificate path or mode")
		}
		if _, exists := seen[item.ArtifactType]; exists {
			return errors.New("duplicate target certificate artifact")
		}
		seen[item.ArtifactType] = struct{}{}
		declaredSize += uint64(item.Size)
	}
	if len(items) == 2 {
		if _, ok := seen["certificate"]; !ok {
			return errors.New("target certificate pair incomplete")
		}
		if _, ok := seen["private_key"]; !ok {
			return errors.New("target certificate pair incomplete")
		}
	}
	certRoot := filepath.Join(r.root, migrationSiteID, "certificates")
	available, err := r.available(r.root)
	if err != nil || declaredSize > available {
		return errors.New("insufficient target certificate staging space")
	}
	if err := os.MkdirAll(certRoot, 0700); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_artifacts
			(migration_site_id,relative_path,artifact_type,entry_type,file_mode,file_size,sha256,status)
			VALUES (?, ?, ?, 'file', ?, ?, ?, 'pending')`, migrationSiteID, item.RelativePath, item.ArtifactType, item.Mode, item.Size, item.SHA256); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrationAvailableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func validateTargetManifestEntry(item SiteMigrationManifestEntry) error {
	if _, err := normalizeMigrationRelativePath(item.RelativePath); err != nil || item.Size < 0 || item.Mode&^0777 != 0 {
		return errors.New("invalid target manifest entry")
	}
	switch item.EntryType {
	case "file":
		if !validMigrationSHA256(item.SHA256) || item.LinkTarget != "" {
			return errors.New("invalid target file entry")
		}
		if item.Size == 0 {
			empty := sha256.Sum256(nil)
			if item.SHA256 != hex.EncodeToString(empty[:]) {
				return errors.New("invalid target empty file hash")
			}
		}
	case "directory":
		if item.LinkTarget != "" {
			return errors.New("invalid target directory entry")
		}
	case "symlink":
		if !validMigrationSHA256(item.SHA256) || item.LinkTarget == "" || filepath.IsAbs(item.LinkTarget) {
			return errors.New("invalid target symlink entry")
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(item.RelativePath)), filepath.FromSlash(item.LinkTarget)))
		if resolved == ".." || filepath.IsAbs(resolved) || len(resolved) >= 3 && resolved[:3] == "../" {
			return errors.New("target symlink escapes staging root")
		}
		sum := sha256.Sum256([]byte(filepath.ToSlash(item.LinkTarget)))
		if hex.EncodeToString(sum[:]) != item.SHA256 {
			return errors.New("target symlink hash mismatch")
		}
	default:
		return errors.New("invalid target entry type")
	}
	return nil
}

func validateTargetManifestStructure(items []SiteMigrationManifestEntry) error {
	types := make(map[string]string, len(items))
	for _, item := range items {
		types[item.RelativePath] = item.EntryType
	}
	for _, item := range items {
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(item.RelativePath)))
		for parent != "." {
			if kind, exists := types[parent]; exists && kind != "directory" {
				return errors.New("target manifest leaf path has children")
			}
			parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent)))
		}
	}
	return nil
}

func validMigrationSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (r *SiteMigrationTargetReceiver) WriteChunk(ctx context.Context, migrationSiteID, artifactType, relativePath string, offset int64, data []byte, chunkSHA256 string) error {
	if len(data) == 0 || int64(len(data)) > SiteMigrationMaxChunkSize || offset < 0 || !validMigrationSHA256(chunkSHA256) {
		return errors.New("invalid target chunk")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != chunkSHA256 {
		return errors.New("target chunk hash mismatch")
	}
	if artifactType != "file" && artifactType != "database" && artifactType != "certificate" && artifactType != "private_key" {
		return errors.New("invalid target artifact type")
	}
	rel, err := normalizeMigrationRelativePath(relativePath)
	if err != nil {
		return err
	}
	if err := r.authorizeTarget(ctx, migrationSiteID, "transferring_files", "transferring_database"); err != nil {
		return err
	}
	base := filepath.Join(r.root, migrationSiteID, "files")
	if artifactType == "database" {
		base = filepath.Join(r.root, migrationSiteID, "database")
	} else if artifactType == "certificate" || artifactType == "private_key" {
		base = filepath.Join(r.root, migrationSiteID, "certificates")
	}
	finalPath := filepath.Join(base, filepath.FromSlash(rel))
	if !migrationPathWithin(base, finalPath) {
		return errors.New("target chunk path escaped staging root")
	}
	if err := ensureMigrationTargetParents(base, filepath.Dir(finalPath)); err != nil {
		return err
	}
	var artifactID int64
	var expectedSize, confirmed int64
	var expectedMode uint32
	var expectedHash, entryType, status string
	err = r.db.QueryRowContext(ctx, `SELECT id,file_size,confirmed_offset,sha256,entry_type,status,file_mode FROM site_migration_artifacts
		WHERE migration_site_id=? AND artifact_type=? AND relative_path=?`, migrationSiteID, artifactType, rel).
		Scan(&artifactID, &expectedSize, &confirmed, &expectedHash, &entryType, &status, &expectedMode)
	if err != nil || entryType != "file" || status == "verified" || confirmed != offset || int64(len(data)) > expectedSize-offset {
		return errors.New("target chunk offset unavailable")
	}
	partialRoot := filepath.Join(r.root, migrationSiteID, "partials", artifactType)
	if err := os.MkdirAll(partialRoot, 0700); err != nil {
		return err
	}
	if err := os.Chmod(partialRoot, 0700); err != nil {
		return err
	}
	partialPath := filepath.Join(partialRoot, fmt.Sprintf("%d.partial", artifactID))
	file, err := os.OpenFile(partialPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	err = r.db.QueryRowContext(ctx, `SELECT file_size,confirmed_offset,sha256,entry_type,status,file_mode FROM site_migration_artifacts WHERE id=?`, artifactID).
		Scan(&expectedSize, &confirmed, &expectedHash, &entryType, &status, &expectedMode)
	if err != nil || entryType != "file" || status == "verified" || confirmed != offset || int64(len(data)) > expectedSize-offset {
		return errors.New("target chunk state unavailable")
	}
	if finalInfo, finalErr := os.Stat(finalPath); finalErr == nil && finalInfo.Mode().IsRegular() {
		_, finalHash, hashErr := hashMigrationArtifact(finalPath)
		if hashErr != nil || finalInfo.Size() != expectedSize || finalHash != expectedHash {
			return errors.New("recovered target file failed integrity check")
		}
		if err := os.Chmod(finalPath, os.FileMode(expectedMode).Perm()); err != nil {
			return err
		}
		if err := syncMigrationFileAndParent(finalPath); err != nil {
			return err
		}
		_ = os.Remove(partialPath)
		result, err := r.db.ExecContext(ctx, `UPDATE site_migration_artifacts SET confirmed_offset=file_size,status='verified',staging_key=?,updated_at=?
			WHERE migration_site_id=? AND artifact_type=? AND relative_path=? AND confirmed_offset=? AND status IN ('pending','transferring')`,
			finalPath, r.now().UTC(), migrationSiteID, artifactType, rel, confirmed)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return errors.New("recovered target file state changed")
		}
		return nil
	} else if finalErr != nil && !os.IsNotExist(finalErr) {
		return finalErr
	}
	info, err := file.Stat()
	if err != nil || info.Size() < offset {
		return errors.New("target partial file offset mismatch")
	}
	if info.Size() > offset {
		if err := file.Truncate(offset); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
	}
	if _, err := file.WriteAt(data, offset); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	next := offset + int64(len(data))
	if next == expectedSize {
		if err := verifyAndPublishTargetFile(file, partialPath, finalPath, expectedHash, os.FileMode(expectedMode)); err != nil {
			return err
		}
		status = "verified"
	}
	result, err := r.db.ExecContext(ctx, `UPDATE site_migration_artifacts SET confirmed_offset=?,status=?,staging_key=?,updated_at=?
		WHERE migration_site_id=? AND artifact_type=? AND relative_path=? AND confirmed_offset=? AND status IN ('pending','transferring')`,
		next, status, finalPath, r.now().UTC(), migrationSiteID, artifactType, rel, offset)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("target chunk state changed")
	}
	return nil
}

func ensureMigrationTargetParents(root, parent string) error {
	if !migrationPathWithin(root, parent) {
		return errors.New("target parent escaped staging root")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	for path := parent; path != root; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("target parent is not a real directory")
		}
	}
	return nil
}

func verifyAndPublishTargetFile(file *os.File, partialPath, finalPath, expectedHash string, mode os.FileMode) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return errors.New("target file hash mismatch")
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		return err
	}
	if err := os.Chmod(finalPath, mode.Perm()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return syncMigrationDirectory(filepath.Dir(finalPath))
}

func syncMigrationFileAndParent(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncMigrationDirectory(filepath.Dir(path))
}

func syncMigrationDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (r *SiteMigrationTargetReceiver) Finalize(ctx context.Context, migrationSiteID string) error {
	if err := r.authorizeTarget(ctx, migrationSiteID, "transferring_files", "transferring_database"); err != nil {
		return err
	}
	var pending int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id=? AND entry_type='file' AND status!='verified'`, migrationSiteID).Scan(&pending); err != nil || pending != 0 {
		return errors.New("target artifacts are not verified")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT relative_path,entry_type,file_mode,link_target FROM site_migration_artifacts
		WHERE migration_site_id=? AND artifact_type='file' AND entry_type IN ('directory','symlink') ORDER BY length(relative_path),relative_path`, migrationSiteID)
	if err != nil {
		return err
	}
	type entry struct {
		path, kind, target string
		mode               uint32
	}
	var entries []entry
	for rows.Next() {
		var item entry
		if err := rows.Scan(&item.path, &item.kind, &item.mode, &item.target); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].kind == "directory" && entries[j].kind != "directory" })
	base := filepath.Join(r.root, migrationSiteID, "files")
	for _, item := range entries {
		path := filepath.Join(base, filepath.FromSlash(item.path))
		if err := ensureMigrationTargetParents(base, filepath.Dir(path)); err != nil {
			return err
		}
		if item.kind == "directory" {
			if err := os.MkdirAll(path, os.FileMode(item.mode)); err != nil {
				return err
			}
			if err := os.Chmod(path, os.FileMode(item.mode).Perm()); err != nil {
				return err
			}
			continue
		}
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return errors.New("target symlink path already exists")
			}
			target, readErr := os.Readlink(path)
			if readErr != nil || filepath.ToSlash(target) != item.target {
				return errors.New("target symlink differs from manifest")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(item.target), path); err != nil {
			return err
		}
	}
	result, err := r.db.ExecContext(ctx, `UPDATE site_migration_sites SET stage='preparing_target',updated_at=? WHERE id=? AND stage IN ('transferring_files','transferring_database')`, r.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("target finalization state changed")
	}
	return nil
}

func (r *SiteMigrationTargetReceiver) authorizeTarget(ctx context.Context, migrationSiteID string, stages ...string) error {
	if !validSiteMigrationID(migrationSiteID) {
		return errors.New("invalid target migration ID")
	}
	var direction, batchStatus, lockDirection, lockStatus, stage string
	err := r.db.QueryRowContext(ctx, `SELECT mb.direction,mb.status,ml.direction,ml.status,ms.stage FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='target' AND ml.status='active'
		WHERE ms.id=?`, migrationSiteID).Scan(&direction, &batchStatus, &lockDirection, &lockStatus, &stage)
	if err != nil || direction != "target" || batchStatus != "active" || lockDirection != "target" || lockStatus != "active" {
		return errors.New("target migration scope unavailable")
	}
	for _, allowed := range stages {
		if stage == allowed {
			return nil
		}
	}
	return fmt.Errorf("target migration stage unavailable: %s", stage)
}
