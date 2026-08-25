package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

const siteMigrationDatabaseArtifactName = "database.sql.gz"

type siteMigrationDatabaseDumper func(context.Context, string, string) error

type siteMigrationSourceDatabase struct {
	db   *sql.DB
	root string
	now  func() time.Time
	dump siteMigrationDatabaseDumper
}

func newSiteMigrationSourceDatabase(db *sql.DB, root string, dump siteMigrationDatabaseDumper) (*siteMigrationSourceDatabase, error) {
	if db == nil || root == "" || dump == nil {
		return nil, errors.New("site migration database export unavailable")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	return &siteMigrationSourceDatabase{db: db, root: root, now: time.Now, dump: dump}, nil
}

func (s *siteMigrationSourceDatabase) BuildExport(ctx context.Context, migrationSiteID string) error {
	var dbName, stage string
	err := s.db.QueryRowContext(ctx, `SELECT w.db_name,ms.stage FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source' AND mb.status='active'
		JOIN websites w ON w.id=ms.source_site_id
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.site_id=w.id AND ml.direction='source' AND ml.status='active'
		WHERE ms.id=?`, migrationSiteID).Scan(&dbName, &stage)
	if err != nil || !isValidMySQLIdentifier(dbName) {
		return errors.New("invalid source database scope")
	}
	if stage != "manifest_ready" && stage != "transferring_files" && stage != "transferring_database" {
		return errors.New("source database export stage unavailable")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET stage='transferring_database',error_code='',updated_at=? WHERE id=?`, s.now().UTC(), migrationSiteID); err != nil {
		return err
	}
	siteDir := filepath.Join(s.root, migrationSiteID)
	if err := os.MkdirAll(siteDir, 0700); err != nil {
		return s.exportFailed(migrationSiteID, err)
	}
	if err := os.Chmod(siteDir, 0700); err != nil {
		return s.exportFailed(migrationSiteID, err)
	}
	target := filepath.Join(siteDir, siteMigrationDatabaseArtifactName)
	_ = os.Remove(target)
	if err := s.dump(ctx, dbName, target); err != nil {
		_ = os.Remove(target)
		return s.exportFailed(migrationSiteID, err)
	}
	info, hash, err := hashMigrationArtifact(target)
	if err != nil {
		_ = os.Remove(target)
		return s.exportFailed(migrationSiteID, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.exportFailed(migrationSiteID, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type='database'`, migrationSiteID); err != nil {
		return s.exportFailed(migrationSiteID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_artifacts
		(migration_site_id,relative_path,artifact_type,entry_type,file_mode,modified_unix_ns,file_size,sha256,staging_key,status)
		VALUES (?,?,'database','file',?,?,?,?,?,'verified')`, migrationSiteID, siteMigrationDatabaseArtifactName, uint32(0600), info.ModTime().UnixNano(), info.Size(), hash, target); err != nil {
		return s.exportFailed(migrationSiteID, err)
	}
	if err := tx.Commit(); err != nil {
		return s.exportFailed(migrationSiteID, err)
	}
	return nil
}

func (s *siteMigrationSourceDatabase) exportFailed(migrationSiteID string, cause error) error {
	_, _ = s.db.Exec(`UPDATE site_migration_sites SET error_code='database_export_failed',updated_at=? WHERE id=? AND stage='transferring_database'`, s.now().UTC(), migrationSiteID)
	return fmt.Errorf("source database export failed: %w", cause)
}

func (s *siteMigrationSourceDatabase) ReadChunk(ctx context.Context, migrationSiteID string, offset, length int64) (*SiteMigrationFileChunk, error) {
	if offset < 0 || length <= 0 || length > SiteMigrationMaxChunkSize {
		return nil, errors.New("invalid database chunk range")
	}
	var path, hash string
	var size, modified int64
	err := s.db.QueryRowContext(ctx, `SELECT staging_key,file_size,modified_unix_ns,sha256 FROM site_migration_artifacts
		WHERE migration_site_id=? AND artifact_type='database' AND relative_path=? AND status='verified'`, migrationSiteID, siteMigrationDatabaseArtifactName).
		Scan(&path, &size, &modified, &hash)
	if err != nil || offset > size || length > size-offset {
		return nil, errors.New("verified database export unavailable")
	}
	file, info, err := openMigrationSourceFile(s.root, filepath.ToSlash(migrationSiteID+"/"+siteMigrationDatabaseArtifactName))
	if err != nil || filepath.Clean(path) != filepath.Join(s.root, migrationSiteID, siteMigrationDatabaseArtifactName) {
		return nil, errors.New("database export path invalid")
	}
	defer file.Close()
	if info.Size() != size || info.ModTime().UnixNano() != modified {
		return nil, errors.New("database export changed")
	}
	data := make([]byte, length)
	n, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if int64(n) != length {
		return nil, errors.New("short database export read")
	}
	after, err := file.Stat()
	if err != nil || after.Size() != size || after.ModTime().UnixNano() != modified {
		return nil, errors.New("database export changed during read")
	}
	sum := sha256.Sum256(data)
	return &SiteMigrationFileChunk{RelativePath: siteMigrationDatabaseArtifactName, Offset: offset, TotalSize: size, FileSHA256: hash, ChunkSHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}

func hashMigrationArtifact(path string) (os.FileInfo, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return nil, "", errors.New("migration artifact is not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, "", err
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, "", errors.New("migration artifact changed while hashing")
	}
	return after, hex.EncodeToString(hash.Sum(nil)), nil
}

func defaultSiteMigrationDatabaseDumper(ctx context.Context, dbName, target string) error {
	if !isValidMySQLIdentifier(dbName) {
		return errors.New("invalid database name")
	}
	if config.AppConfig == nil {
		return errors.New("database configuration unavailable")
	}
	cfg := config.AppConfig.MariaDB
	tmp := target + ".partial"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
	args := siteMigrationDumpArgs(cfg, dbName)
	cmd := exec.CommandContext(ctx, "mariadb-dump", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.RootPassword)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, stdout)
	closeGzipErr := gz.Close()
	closeFileErr := out.Close()
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	if closeGzipErr != nil {
		return closeGzipErr
	}
	if closeFileErr != nil {
		return closeFileErr
	}
	if waitErr != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("mariadb-dump failed: %s", stderr.String())
		}
		return waitErr
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	keep = true
	return nil
}

func siteMigrationDumpArgs(cfg config.MariaDBConfig, dbName string) []string {
	args := []string{"--user=" + cfg.RootUser, "--single-transaction", "--quick", "--routines", "--events", "--triggers", "--hex-blob", "--default-character-set=utf8mb4"}
	if cfg.Socket != "" {
		args = append(args, "--socket="+cfg.Socket)
	} else {
		args = append(args, "--host="+cfg.Host, "--port="+strconv.Itoa(cfg.Port))
	}
	args = append(args, "--", dbName)
	return args
}
