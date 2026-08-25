package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func TestSiteMigrationDatabaseExportAndAuthorizedChunk(t *testing.T) {
	files, _ := setupMigrationSourceFilesTest(t)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET stage='manifest_ready' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	databaseSource, err := newSiteMigrationSourceDatabase(database.GetDB(), root, func(_ context.Context, dbName, target string) error {
		if dbName != "db_example" {
			t.Fatalf("dbName=%q", dbName)
		}
		return os.WriteFile(target, []byte("gzip-database"), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	databaseSource.now = freezerTestTime
	if err := databaseSource.BuildExport(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	var stage, artifactPath, hash string
	var size int64
	if err := database.GetDB().QueryRow(`SELECT ms.stage,ma.staging_key,ma.file_size,ma.sha256
		FROM site_migration_sites ms JOIN site_migration_artifacts ma ON ma.migration_site_id=ms.id AND ma.artifact_type='database'
		WHERE ms.id='migration_0000001'`).Scan(&stage, &artifactPath, &size, &hash); err != nil {
		t.Fatal(err)
	}
	if stage != "transferring_database" || artifactPath != filepath.Join(root, "migration_0000001", siteMigrationDatabaseArtifactName) || size != 13 || len(hash) != 64 {
		t.Fatalf("stage=%q path=%q size=%d hash=%q", stage, artifactPath, size, hash)
	}

	credential := strings.Repeat("d", 48)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_peers SET inbound_credential_hash=? WHERE id='peer_00000000001'`, hashMigrationSecret(credential)); err != nil {
		t.Fatal(err)
	}
	service := &SiteMigrationSourceService{
		db: database.GetDB(), pairing: &SiteMigrationPairingService{db: database.GetDB()}, files: files, database: databaseSource,
	}
	chunk, err := service.ReadDatabaseChunk(context.Background(), "peer_00000000001", "migration_0000001", credential, 5, 8)
	if err != nil {
		t.Fatal(err)
	}
	if string(chunk.Data) != "database" || chunk.FileSHA256 != hash {
		t.Fatalf("chunk=%+v", chunk)
	}
	if err := os.WriteFile(artifactPath, []byte("tampered-export"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDatabaseChunk(context.Background(), "peer_00000000001", "migration_0000001", credential, 0, 1); err == nil {
		t.Fatal("changed database export accepted")
	}
	if err := os.WriteFile(artifactPath, []byte("gzip-database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDatabaseChunk(context.Background(), "peer_00000000001", "migration_0000001", "wrong", 0, 1); err == nil {
		t.Fatal("invalid credential accepted")
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_batches SET status='failed' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadDatabaseChunk(context.Background(), "peer_00000000001", "migration_0000001", credential, 0, 1); err == nil {
		t.Fatal("inactive batch accepted")
	}
}

func TestSiteMigrationDatabaseExportFailureIsNotReadable(t *testing.T) {
	setupMigrationSourceFilesTest(t)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET stage='manifest_ready' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	databaseSource, err := newSiteMigrationSourceDatabase(database.GetDB(), t.TempDir(), func(_ context.Context, _, target string) error {
		if err := os.WriteFile(target, []byte("partial"), 0600); err != nil {
			return err
		}
		return context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := databaseSource.BuildExport(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("failed dump accepted")
	}
	var errorCode string
	_ = database.GetDB().QueryRow(`SELECT error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&errorCode)
	if errorCode != "database_export_failed" {
		t.Fatalf("error_code=%q", errorCode)
	}
	if _, err := os.Stat(filepath.Join(databaseSource.root, "migration_0000001", siteMigrationDatabaseArtifactName)); !os.IsNotExist(err) {
		t.Fatalf("partial export remains: %v", err)
	}
	if _, err := databaseSource.ReadChunk(context.Background(), "migration_0000001", 0, 1); err == nil {
		t.Fatal("failed export became readable")
	}
}

func TestSiteMigrationDumpArgsUseConsistentSnapshotAndArgumentBoundary(t *testing.T) {
	args := siteMigrationDumpArgs(config.MariaDBConfig{Host: "127.0.0.1", Port: 3306, RootUser: "root"}, "safe_db")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--single-transaction", "--quick", "--routines", "--events", "--triggers", "--hex-blob", "-- safe_db"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %q", required, joined)
		}
	}
}
