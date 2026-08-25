package executor

import (
	"archive/tar"
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
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	SiteMigrationFileShardBytes int64 = 512 << 20
	siteMigrationShardMaxExtra  int64 = 16 << 20
)

type SiteMigrationFileShard struct {
	Index             int
	EntryCount        int
	UncompressedBytes int64
	WriteTo           func(io.Writer) error
}

type siteMigrationShardEntry struct {
	ID     int64
	Status string
	SiteMigrationManifestEntry
}

func partitionMigrationFileShards(entries []siteMigrationShardEntry) [][]siteMigrationShardEntry {
	var shards [][]siteMigrationShardEntry
	var current []siteMigrationShardEntry
	var size int64
	for _, entry := range entries {
		if entry.EntryType != "file" || entry.Size <= 0 || entry.Size > SiteMigrationFileShardBytes {
			continue
		}
		if len(current) != 0 && size > SiteMigrationFileShardBytes-entry.Size {
			shards = append(shards, current)
			current, size = nil, 0
		}
		current = append(current, entry)
		size += entry.Size
	}
	if len(current) != 0 {
		shards = append(shards, current)
	}
	return shards
}

func (s *siteMigrationSourceFiles) FileShard(ctx context.Context, migrationSiteID string, index int) (*SiteMigrationFileShard, error) {
	if index < 0 {
		return nil, errors.New("invalid source file shard")
	}
	root, stage, err := s.sourceRoot(ctx, migrationSiteID)
	if err != nil {
		return nil, err
	}
	if stage != "manifest_ready" && stage != "transferring_files" && stage != "transferring_database" {
		return nil, errors.New("source manifest is not available")
	}
	entries, err := loadMigrationShardEntries(ctx, s.db, migrationSiteID)
	if err != nil {
		return nil, err
	}
	shards := partitionMigrationFileShards(entries)
	if index >= len(shards) {
		return nil, errors.New("source file shard is unavailable")
	}
	selected := append([]siteMigrationShardEntry(nil), shards[index]...)
	var total int64
	for _, entry := range selected {
		total += entry.Size
	}
	return &SiteMigrationFileShard{
		Index: index, EntryCount: len(selected), UncompressedBytes: total,
		WriteTo: func(dst io.Writer) error { return writeMigrationFileShard(ctx, root, selected, dst) },
	}, nil
}

func loadMigrationShardEntries(ctx context.Context, db *sql.DB, taskID string) ([]siteMigrationShardEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,relative_path,entry_type,file_mode,file_size,sha256,link_target,modified_unix_ns,status
		FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type='file' ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []siteMigrationShardEntry
	for rows.Next() {
		var entry siteMigrationShardEntry
		if err := rows.Scan(&entry.ID, &entry.RelativePath, &entry.EntryType, &entry.Mode, &entry.Size, &entry.SHA256, &entry.LinkTarget, &entry.ModifiedUnixNS, &entry.Status); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func writeMigrationFileShard(ctx context.Context, root string, entries []siteMigrationShardEntry, dst io.Writer) error {
	encoder, err := zstd.NewWriter(dst, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)), zstd.WithEncoderConcurrency(4), zstd.WithEncoderCRC(true))
	if err != nil {
		return err
	}
	tarWriter := tar.NewWriter(encoder)
	closed := false
	defer func() {
		if !closed {
			_ = tarWriter.Close()
			_ = encoder.Close()
		}
	}()
	closeWriters := func() error {
		if err := tarWriter.Close(); err != nil {
			_ = encoder.Close()
			return err
		}
		if err := encoder.Close(); err != nil {
			return err
		}
		closed = true
		return nil
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			_ = tarWriter.Close()
			_ = encoder.Close()
			return err
		}
		if entry.EntryType != "file" || entry.Size <= 0 || entry.Size > SiteMigrationFileShardBytes {
			return errors.New("invalid source shard entry")
		}
		file, info, err := openMigrationSourceFile(root, entry.RelativePath)
		if err != nil {
			return err
		}
		if info.Size() != entry.Size || info.ModTime().UnixNano() != entry.ModifiedUnixNS {
			_ = file.Close()
			return errors.New("source file changed after manifest")
		}
		header := &tar.Header{Name: entry.RelativePath, Mode: int64(entry.Mode), Size: entry.Size, ModTime: time.Unix(0, entry.ModifiedUnixNS), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = file.Close()
			return err
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(tarWriter, hash), file, entry.Size)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil || written != entry.Size {
			return errors.New("source shard file read failed")
		}
		if statErr != nil || after.Size() != entry.Size || after.ModTime().UnixNano() != entry.ModifiedUnixNS {
			return errors.New("source file changed during shard read")
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return errors.New("source shard file hash changed")
		}
	}
	return closeWriters()
}

func (r *SiteMigrationTargetReceiver) ReceiveFileShard(ctx context.Context, migrationSiteID string, index int, entries []SiteMigrationManifestEntry, download func(io.Writer) error) error {
	if index < 0 || len(entries) == 0 || download == nil {
		return errors.New("invalid target file shard")
	}
	if err := r.authorizeTarget(ctx, migrationSiteID, "transferring_files", "transferring_database"); err != nil {
		return err
	}
	var expectedBytes int64
	for _, entry := range entries {
		if err := validateTargetManifestEntry(entry); err != nil || entry.EntryType != "file" || entry.Size <= 0 || entry.Size > SiteMigrationFileShardBytes {
			return errors.New("invalid target file shard entry")
		}
		if expectedBytes > SiteMigrationFileShardBytes-entry.Size {
			return errors.New("target file shard exceeds limit")
		}
		expectedBytes += entry.Size
	}
	siteRoot := filepath.Join(r.root, migrationSiteID)
	shardRoot := filepath.Join(siteRoot, "shards")
	if err := os.MkdirAll(shardRoot, 0700); err != nil {
		return err
	}
	if err := os.Chmod(shardRoot, 0700); err != nil {
		return err
	}
	archivePath := filepath.Join(shardRoot, fmt.Sprintf("shard-%06d.tar.zst.partial", index))
	extractRoot := filepath.Join(shardRoot, fmt.Sprintf("extract-%06d", index))
	if err := os.RemoveAll(extractRoot); err != nil {
		return err
	}
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	limit := expectedBytes + expectedBytes/100 + siteMigrationShardMaxExtra
	counter := &limitedCountingWriter{dst: archive, limit: limit}
	downloadErr := download(counter)
	if downloadErr == nil {
		downloadErr = archive.Sync()
	}
	closeErr := archive.Close()
	if downloadErr != nil {
		_ = os.Remove(archivePath)
		return downloadErr
	}
	if closeErr != nil {
		_ = os.Remove(archivePath)
		return closeErr
	}
	if counter.written <= 0 || counter.written > limit {
		_ = os.Remove(archivePath)
		return errors.New("target compressed shard size is invalid")
	}
	if err := r.extractAndCommitFileShard(ctx, migrationSiteID, index, entries, archivePath); err != nil {
		_ = os.Remove(archivePath)
		return err
	}
	return nil
}

type limitedCountingWriter struct {
	dst     io.Writer
	limit   int64
	written int64
}

func (w *limitedCountingWriter) Write(p []byte) (int, error) {
	if w.written+int64(len(p)) > w.limit {
		return 0, errors.New("remote file shard exceeded limit")
	}
	n, err := w.dst.Write(p)
	w.written += int64(n)
	return n, err
}

func (r *SiteMigrationTargetReceiver) extractAndCommitFileShard(ctx context.Context, taskID string, index int, entries []SiteMigrationManifestEntry, archivePath string) error {
	extractRoot := filepath.Join(r.root, taskID, "shards", fmt.Sprintf("extract-%06d", index))
	if err := os.RemoveAll(extractRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(extractRoot, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(extractRoot)
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	decoder, err := zstd.NewReader(archive, zstd.WithDecoderConcurrency(1))
	if err != nil {
		_ = archive.Close()
		return err
	}
	tarReader := tar.NewReader(decoder)
	expected := make(map[string]SiteMigrationManifestEntry, len(entries))
	for _, entry := range entries {
		expected[entry.RelativePath] = entry
	}
	seen := make(map[string]struct{}, len(entries))
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			decoder.Close()
			_ = archive.Close()
			return err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			decoder.Close()
			_ = archive.Close()
			return errors.New("target shard contains unsupported tar entry")
		}
		rel, err := normalizeMigrationRelativePath(header.Name)
		entry, ok := expected[rel]
		if err != nil || !ok || header.Size != entry.Size || header.Mode&0777 != int64(entry.Mode) {
			decoder.Close()
			_ = archive.Close()
			return errors.New("target shard entry differs from manifest")
		}
		if _, duplicate := seen[rel]; duplicate {
			decoder.Close()
			_ = archive.Close()
			return errors.New("target shard contains duplicate entry")
		}
		seen[rel] = struct{}{}
		path := filepath.Join(extractRoot, filepath.FromSlash(rel))
		if err := ensureMigrationTargetParents(extractRoot, filepath.Dir(path)); err != nil {
			decoder.Close()
			_ = archive.Close()
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(entry.Mode).Perm())
		if err != nil {
			decoder.Close()
			_ = archive.Close()
			return err
		}
		hash := sha256.New()
		copied, copyErr := io.CopyN(io.MultiWriter(file, hash), tarReader, entry.Size)
		syncErr, closeErr := file.Sync(), file.Close()
		if copyErr != nil || copied != entry.Size || syncErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			decoder.Close()
			_ = archive.Close()
			return errors.New("target shard file integrity check failed")
		}
	}
	decoder.Close()
	if err := archive.Close(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("target shard is incomplete")
	}
	base := filepath.Join(r.root, taskID, "files")
	sorted := append([]SiteMigrationManifestEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelativePath < sorted[j].RelativePath })
	for _, entry := range sorted {
		finalPath := filepath.Join(base, filepath.FromSlash(entry.RelativePath))
		if err := ensureMigrationTargetParents(base, filepath.Dir(finalPath)); err != nil {
			return err
		}
		if info, err := os.Stat(finalPath); err == nil {
			_, hash, hashErr := hashMigrationArtifact(finalPath)
			if !info.Mode().IsRegular() || info.Size() != entry.Size || hashErr != nil || hash != entry.SHA256 {
				return errors.New("existing target shard file differs from manifest")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(filepath.Join(extractRoot, filepath.FromSlash(entry.RelativePath)), finalPath); err != nil {
			return err
		}
		if err := os.Chmod(finalPath, os.FileMode(entry.Mode).Perm()); err != nil {
			return err
		}
		if err := syncMigrationDirectory(filepath.Dir(finalPath)); err != nil {
			return err
		}
	}
	if err := os.Remove(archivePath); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, entry := range entries {
		result, err := tx.ExecContext(ctx, `UPDATE site_migration_artifacts SET confirmed_offset=file_size,status='verified',staging_key=?,updated_at=?
			WHERE migration_site_id=? AND artifact_type='file' AND relative_path=? AND file_size=? AND sha256=? AND status IN ('pending','transferring','verified')`,
			filepath.Join(base, filepath.FromSlash(entry.RelativePath)), r.now().UTC(), taskID, entry.RelativePath, entry.Size, entry.SHA256)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("target shard artifact state changed")
		}
	}
	return tx.Commit()
}
