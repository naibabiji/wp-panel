package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func TestSiteMigrationBuildManifestAndReadVerifiedChunk(t *testing.T) {
	files, root := setupMigrationSourceFilesTest(t)
	if err := os.Mkdir(filepath.Join(root, "assets"), 0750); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello migration")
	if err := os.WriteFile(filepath.Join(root, "assets", "app.php"), content, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("assets/app.php", filepath.Join(root, "current.php")); err != nil {
		t.Fatal(err)
	}

	if err := files.BuildManifest(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	var stage string
	_ = database.GetDB().QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage)
	if stage != "manifest_ready" {
		t.Fatalf("stage=%q", stage)
	}
	var entries int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id='migration_0000001' AND artifact_type='file'`).Scan(&entries); err != nil || entries != 3 {
		t.Fatalf("entries=%d err=%v", entries, err)
	}
	chunk, err := files.ReadChunk(context.Background(), "migration_0000001", "assets/app.php", 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(chunk.Data) != "ello" || chunk.TotalSize != int64(len(content)) {
		t.Fatalf("chunk=%+v", chunk)
	}
	sum := sha256.Sum256([]byte("ello"))
	if chunk.ChunkSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("chunk hash=%q", chunk.ChunkSHA256)
	}
	if _, err := files.ReadChunk(context.Background(), "migration_0000001", "../secret", 0, 1); err == nil {
		t.Fatal("traversal path accepted")
	}
	if _, err := files.ReadChunk(context.Background(), "migration_0000001", "current.php", 0, 1); err == nil {
		t.Fatal("symlink chunk accepted")
	}
	credential := strings.Repeat("s", 48)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_peers SET inbound_credential_hash=? WHERE id='peer_00000000001'`, hashMigrationSecret(credential)); err != nil {
		t.Fatal(err)
	}
	source, err := NewSiteMigrationSourceService(database.GetDB(), &SiteMigrationPairingService{db: database.GetDB()})
	if err != nil {
		t.Fatal(err)
	}
	page, next, err := source.ListManifest(context.Background(), "peer_00000000001", "migration_0000001", credential, 0, 2)
	if err != nil || len(page) != 2 || next <= 0 {
		t.Fatalf("manifest page len=%d next=%d err=%v", len(page), next, err)
	}
	if _, _, err := source.ListManifest(context.Background(), "peer_00000000001", "migration_0000001", "wrong", 0, 2); err == nil {
		t.Fatal("wrong peer credential accepted")
	}
	if _, err := source.ReadFileChunk(context.Background(), "peer_00000000001", "migration_0000001", credential, "assets/app.php", 0, 5); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.ListManifest(context.Background(), "peer_00000000001", "migration_0000001", credential, -1, 2); err == nil {
		t.Fatal("negative cursor accepted")
	}
	if _, _, err := source.ListManifest(context.Background(), "peer_00000000001", "migration_0000001", credential, 0, 1001); err == nil {
		t.Fatal("oversized page accepted")
	}
	if _, err := source.ReadFileChunk(context.Background(), "peer_00000000001", "migration_0000001", credential, "assets/app.php", 0, SiteMigrationMaxChunkSize+1); err == nil {
		t.Fatal("oversized chunk accepted")
	}
	if _, err := database.GetDB().Exec(`INSERT INTO site_migration_peers(id,status,inbound_credential_hash,protocol_version) VALUES ('peer_00000000002','paired',?,?)`, hashMigrationSecret(credential), siteMigrationProtocolVersion); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.ListManifest(context.Background(), "peer_00000000002", "migration_0000001", credential, 0, 2); err == nil {
		t.Fatal("cross-peer manifest access accepted")
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET stage='transferring_database' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.ListManifest(context.Background(), "peer_00000000001", "migration_0000001", credential, 0, 2); err != nil {
		t.Fatalf("prepared frozen manifest unavailable: %v", err)
	}
	if _, err := source.ReadFileChunk(context.Background(), "peer_00000000001", "migration_0000001", credential, "assets/app.php", 0, 5); err != nil {
		t.Fatalf("prepared frozen file unavailable: %v", err)
	}
}

func TestSiteMigrationManifestRejectsFIFO(t *testing.T) {
	files, root := setupMigrationSourceFilesTest(t)
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := files.BuildManifest(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("FIFO accepted in manifest")
	}
}

func TestSiteMigrationManifestRejectsEscapingSymlinkWithoutPartialRows(t *testing.T) {
	files, root := setupMigrationSourceFilesTest(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := files.BuildManifest(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("escaping symlink accepted")
	}
	var stage string
	_ = database.GetDB().QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage)
	if stage != "source_frozen" {
		t.Fatalf("stage=%q", stage)
	}
	var entries int
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id='migration_0000001'`).Scan(&entries)
	if entries != 0 {
		t.Fatalf("partial manifest entries=%d", entries)
	}
}

func TestSiteMigrationChunkRejectsFileChangedOrReplacedAfterManifest(t *testing.T) {
	files, root := setupMigrationSourceFilesTest(t)
	path := filepath.Join(root, "data.bin")
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := files.BuildManifest(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed-size"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadChunk(context.Background(), "migration_0000001", "data.bin", 0, 4); err == nil {
		t.Fatal("changed file accepted")
	}

	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadChunk(context.Background(), "migration_0000001", "data.bin", 0, 4); err == nil {
		t.Fatal("replacement symlink accepted")
	}
}

func setupMigrationSourceFilesTest(t *testing.T) (*siteMigrationSourceFiles, string) {
	t.Helper()
	store, siteID := newSiteMigrationStoreTest(t)
	root := t.TempDir()
	if _, err := store.db.Exec(`UPDATE websites SET web_root=? WHERE id=?`, root, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET stage='source_frozen' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if err := store.acquireLock(context.Background(), "migration_0000001", &siteID, "example.com", "source", freezerTestTime()); err != nil {
		t.Fatal(err)
	}
	files, err := newSiteMigrationSourceFiles(store.db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files.now = freezerTestTime
	info, err := os.Stat(files.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("work dir permissions=%v", info.Mode().Perm())
	}
	return files, root
}

func TestSiteMigrationManifestDoesNotUseSystemTemp(t *testing.T) {
	files, root := setupMigrationSourceFilesTest(t)
	blockedTemp := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedTemp, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blockedTemp)
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("<?php echo 'ok';"), 0640); err != nil {
		t.Fatal(err)
	}

	if err := files.BuildManifest(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(files.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("manifest temporary files remain: %v", entries)
	}
	info, err := os.Stat(files.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("manifest work dir mode=%v", info.Mode().Perm())
	}
}

func TestSiteMigrationSourceServiceUsesPanelDataDirForManifest(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	dataDir := t.TempDir()
	previous := config.AppConfig
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: dataDir}}
	t.Cleanup(func() { config.AppConfig = previous })

	service, err := NewSiteMigrationSourceService(store.db, &SiteMigrationPairingService{db: store.db})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "site-migration", "manifests")
	if service.files.workDir != want {
		t.Fatalf("manifest work dir=%q want=%q", service.files.workDir, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("manifest work dir mode=%v", info.Mode().Perm())
	}
}
