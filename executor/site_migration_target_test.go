package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestSiteMigrationTargetReceiverWritesAndFinalizesIsolatedArtifacts(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	fileData := []byte("hello target")
	dbData := []byte("database dump")
	files := []SiteMigrationManifestEntry{
		{RelativePath: "assets", EntryType: "directory", Mode: 0750},
		{RelativePath: "assets/app.php", EntryType: "file", Mode: 0640, Size: int64(len(fileData)), SHA256: migrationTestHash(fileData)},
		{RelativePath: "empty.txt", EntryType: "file", Mode: 0644, Size: 0, SHA256: migrationTestHash(nil)},
		{RelativePath: "current.php", EntryType: "symlink", Mode: 0777, Size: int64(len("assets/app.php")), LinkTarget: "assets/app.php", SHA256: migrationTestHash([]byte("assets/app.php"))},
	}
	databaseArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Mode: 0600, Size: int64(len(dbData)), SHA256: migrationTestHash(dbData)}
	if err := receiver.Prepare(context.Background(), "migration_0000001", files, databaseArtifact); err != nil {
		t.Fatal(err)
	}
	certData := []byte("certificate bytes")
	keyData := []byte("private key bytes")
	certificates := []SiteMigrationCertificateArtifact{
		{ArtifactType: "certificate", RelativePath: "certificate.pem", Mode: 0644, Size: int64(len(certData)), SHA256: migrationTestHash(certData)},
		{ArtifactType: "private_key", RelativePath: "private-key.pem", Mode: 0600, Size: int64(len(keyData)), SHA256: migrationTestHash(keyData)},
	}
	if err := receiver.PrepareCertificates(context.Background(), "migration_0000001", certificates); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Prepare(context.Background(), "migration_0000001", files, databaseArtifact); err == nil {
		t.Fatal("prepared target manifest was overwritten")
	}
	if _, err := os.Lstat(filepath.Join(root, "migration_0000001", "files", "current.php")); !os.IsNotExist(err) {
		t.Fatalf("symlink published before finalization: %v", err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "assets/app.php", 1, fileData[1:], migrationTestHash(fileData[1:])); err == nil {
		t.Fatal("out-of-order chunk accepted")
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "assets/app.php", 0, fileData[:5], migrationTestHash(fileData[:5])); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "assets/app.php", 5, fileData[5:], migrationTestHash(fileData[5:])); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "database", siteMigrationDatabaseArtifactName, 0, dbData, migrationTestHash(dbData)); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "certificate", "certificate.pem", 0, certData, migrationTestHash(certData)); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "private_key", "private-key.pem", 0, keyData, migrationTestHash(keyData)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("assets/app.php", filepath.Join(root, "migration_0000001", "files", "current.php")); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Finalize(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(root, "migration_0000001", "files", "assets", "app.php"))
	if err != nil || string(written) != string(fileData) {
		t.Fatalf("written=%q err=%v", written, err)
	}
	if info, err := os.Stat(filepath.Join(root, "migration_0000001", "files", "assets", "app.php")); err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("file mode=%v err=%v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Join(root, "migration_0000001", "files", "empty.txt")); err != nil || info.Size() != 0 {
		t.Fatalf("empty file info=%v err=%v", info, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "migration_0000001", "certificates", "private-key.pem")); err != nil || string(got) != string(keyData) {
		t.Fatalf("private key=%q err=%v", got, err)
	}
	if info, err := os.Stat(filepath.Join(root, "migration_0000001", "certificates", "private-key.pem")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private key mode=%v err=%v", info.Mode().Perm(), err)
	}
	target, err := os.Readlink(filepath.Join(root, "migration_0000001", "files", "current.php"))
	if err != nil || target != "assets/app.php" {
		t.Fatalf("symlink=%q err=%v", target, err)
	}
	var stage string
	_ = database.GetDB().QueryRow(`SELECT stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&stage)
	if stage != "preparing_target" {
		t.Fatalf("stage=%q", stage)
	}
}

func TestSiteMigrationTargetReceiverKeepsManifestPartialSuffixFile(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	mainData := []byte("main")
	partialData := []byte("legitimate partial file")
	dbData := []byte("db")
	files := []SiteMigrationManifestEntry{
		{RelativePath: "asset.js", EntryType: "file", Mode: 0644, Size: int64(len(mainData)), SHA256: migrationTestHash(mainData)},
		{RelativePath: "asset.js.partial", EntryType: "file", Mode: 0644, Size: int64(len(partialData)), SHA256: migrationTestHash(partialData)},
		{RelativePath: "empty-case.js", EntryType: "file", Mode: 0644, Size: int64(len(mainData)), SHA256: migrationTestHash(mainData)},
		{RelativePath: "empty-case.js.partial", EntryType: "file", Mode: 0644, Size: 0, SHA256: migrationTestHash(nil)},
	}
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash(dbData)}
	if err := receiver.Prepare(context.Background(), "migration_0000001", files, dbArtifact); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{"asset.js": mainData, "asset.js.partial": partialData, "empty-case.js": mainData} {
		if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", path, 0, data, migrationTestHash(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "database", siteMigrationDatabaseArtifactName, 0, dbData, migrationTestHash(dbData)); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Finalize(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{"asset.js": mainData, "asset.js.partial": partialData, "empty-case.js": mainData, "empty-case.js.partial": nil} {
		got, err := os.ReadFile(filepath.Join(root, "migration_0000001", "files", path))
		if err != nil || string(got) != string(want) {
			t.Fatalf("path=%s got=%q err=%v", path, got, err)
		}
	}
}

func TestSiteMigrationTargetFinalizeRejectsDifferentExistingSymlink(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	dbData := []byte("db")
	files := []SiteMigrationManifestEntry{
		{RelativePath: "empty", EntryType: "file", Mode: 0644, Size: 0, SHA256: migrationTestHash(nil)},
		{RelativePath: "link", EntryType: "symlink", Mode: 0777, LinkTarget: "empty", Size: 5, SHA256: migrationTestHash([]byte("empty"))},
	}
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash(dbData)}
	if err := receiver.Prepare(context.Background(), "migration_0000001", files, dbArtifact); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "database", siteMigrationDatabaseArtifactName, 0, dbData, migrationTestHash(dbData)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("different", filepath.Join(root, "migration_0000001", "files", "link")); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Finalize(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("different existing symlink accepted")
	}
}

func TestSiteMigrationTargetRejectsNonPrivateKeyMode(t *testing.T) {
	receiver, _ := setupMigrationTargetReceiverTest(t)
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash([]byte("db"))}
	if err := receiver.Prepare(context.Background(), "migration_0000001", nil, dbArtifact); err != nil {
		t.Fatal(err)
	}
	certificates := []SiteMigrationCertificateArtifact{
		{ArtifactType: "certificate", RelativePath: "certificate.pem", Mode: 0644, Size: 4, SHA256: migrationTestHash([]byte("cert"))},
		{ArtifactType: "private_key", RelativePath: "private-key.pem", Mode: 0644, Size: 3, SHA256: migrationTestHash([]byte("key"))},
	}
	if err := receiver.PrepareCertificates(context.Background(), "migration_0000001", certificates); err == nil {
		t.Fatal("non-0600 private key mode accepted")
	}
	var count int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id='migration_0000001' AND artifact_type IN ('certificate','private_key')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("certificate artifacts persisted after rejected mode: %d", count)
	}
}

func TestSiteMigrationTargetReceiverRejectsManifestLeafWithChildren(t *testing.T) {
	for _, parentType := range []string{"file", "symlink"} {
		t.Run(parentType, func(t *testing.T) {
			receiver, _ := setupMigrationTargetReceiverTest(t)
			parent := SiteMigrationManifestEntry{RelativePath: "node", EntryType: parentType, Size: 1, SHA256: migrationTestHash([]byte("x"))}
			if parentType == "symlink" {
				parent.LinkTarget = "target"
				parent.Size = int64(len(parent.LinkTarget))
				parent.SHA256 = migrationTestHash([]byte(parent.LinkTarget))
			}
			files := []SiteMigrationManifestEntry{parent, {RelativePath: "node/child", EntryType: "file", Size: 1, SHA256: migrationTestHash([]byte("y"))}}
			dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash([]byte("db"))}
			if err := receiver.Prepare(context.Background(), "migration_0000001", files, dbArtifact); err == nil {
				t.Fatal("manifest leaf with child accepted")
			}
		})
	}
}

func TestSiteMigrationTargetReceiverRecoversRenameBeforeStateCommit(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	data := []byte("complete")
	dbData := []byte("db")
	files := []SiteMigrationManifestEntry{{RelativePath: "index.php", EntryType: "file", Mode: 0644, Size: int64(len(data)), SHA256: migrationTestHash(data)}}
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash(dbData)}
	if err := receiver.Prepare(context.Background(), "migration_0000001", files, dbArtifact); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(root, "migration_0000001", "files", "index.php")
	if err := os.WriteFile(finalPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_artifacts SET confirmed_offset=3,status='transferring' WHERE migration_site_id='migration_0000001' AND artifact_type='file'`); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "index.php", 3, data[3:], migrationTestHash(data[3:])); err != nil {
		t.Fatal(err)
	}
	var status string
	var confirmed int64
	if err := database.GetDB().QueryRow(`SELECT status,confirmed_offset FROM site_migration_artifacts WHERE migration_site_id='migration_0000001' AND artifact_type='file'`).Scan(&status, &confirmed); err != nil {
		t.Fatal(err)
	}
	if status != "verified" || confirmed != int64(len(data)) {
		t.Fatalf("status=%q confirmed=%d", status, confirmed)
	}
}

func TestSiteMigrationTargetReceiverRecoversDataWriteBeforeOffsetCommit(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	data := []byte("complete")
	dbData := []byte("db")
	files := []SiteMigrationManifestEntry{{RelativePath: "index.php", EntryType: "file", Mode: 0644, Size: int64(len(data)), SHA256: migrationTestHash(data)}}
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash(dbData)}
	if err := receiver.Prepare(context.Background(), "migration_0000001", files, dbArtifact); err != nil {
		t.Fatal(err)
	}
	var artifactID int64
	if err := database.GetDB().QueryRow(`SELECT id FROM site_migration_artifacts WHERE migration_site_id='migration_0000001' AND artifact_type='file'`).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, "migration_0000001", "partials", "file", fmt.Sprintf("%d.partial", artifactID))
	if err := os.MkdirAll(filepath.Dir(partial), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("stale bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "index.php", 0, data, migrationTestHash(data)); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(root, "migration_0000001", "files", "index.php"))
	if err != nil || string(written) != string(data) {
		t.Fatalf("written=%q err=%v", written, err)
	}
}

func TestSiteMigrationTargetReceiverRejectsUnsafeManifestAndBadChunk(t *testing.T) {
	receiver, _ := setupMigrationTargetReceiverTest(t)
	dbData := []byte("db")
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash(dbData)}
	unsafe := []SiteMigrationManifestEntry{{RelativePath: "escape", EntryType: "symlink", LinkTarget: "../outside", SHA256: migrationTestHash([]byte("../outside"))}}
	if err := receiver.Prepare(context.Background(), "migration_0000001", unsafe, dbArtifact); err == nil {
		t.Fatal("escaping symlink accepted")
	}
	badEmpty := []SiteMigrationManifestEntry{{RelativePath: "empty", EntryType: "file", Size: 0, SHA256: migrationTestHash([]byte("not empty"))}}
	if err := receiver.Prepare(context.Background(), "migration_0000001", badEmpty, dbArtifact); err == nil {
		t.Fatal("empty file with wrong hash accepted")
	}
	data := []byte("safe")
	files := []SiteMigrationManifestEntry{{RelativePath: "index.php", EntryType: "file", Size: 4, SHA256: migrationTestHash(data)}}
	if err := receiver.Prepare(context.Background(), "migration_0000001", files, dbArtifact); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "../index.php", 0, data, migrationTestHash(data)); err == nil {
		t.Fatal("traversal chunk accepted")
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "index.php", 0, data, migrationTestHash([]byte("wrong"))); err == nil {
		t.Fatal("bad chunk hash accepted")
	}
	if err := receiver.WriteChunk(context.Background(), "migration_0000001", "file", "index.php", 0, []byte("evil"), migrationTestHash([]byte("evil"))); err == nil {
		t.Fatal("bad complete file hash accepted")
	}
	if err := receiver.Finalize(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("unverified artifacts finalized")
	}
}

func TestSiteMigrationTargetCleanupRemovesOnlyOwnedStagingAndReleasesLock(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash([]byte("db"))}
	if err := receiver.Prepare(context.Background(), "migration_0000001", nil, dbArtifact); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "unrelated-task")
	if err := os.MkdirAll(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	if err := receiver.Cleanup(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "migration_0000001")); !os.IsNotExist(err) {
		t.Fatalf("owned staging remains: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated staging removed: %v", err)
	}
	var status, stage, cleanup, lockStatus string
	if err := database.GetDB().QueryRow(`SELECT status,stage,cleanup_status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage, &cleanup); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001'`).Scan(&lockStatus); err != nil {
		t.Fatal(err)
	}
	if status != "abandoned" || stage != "abandoned" || cleanup != "complete" || lockStatus != "released" {
		t.Fatalf("status=%q stage=%q cleanup=%q lock=%q", status, stage, cleanup, lockStatus)
	}
}

func TestSiteMigrationTargetCleanupFailureKeepsLockAndEvidence(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash([]byte("db"))}
	if err := receiver.Prepare(context.Background(), "migration_0000001", nil, dbArtifact); err != nil {
		t.Fatal(err)
	}
	receiver.removeAll = func(path string) error {
		if path != filepath.Join(root, "migration_0000001") {
			t.Fatalf("unexpected cleanup path=%q", path)
		}
		return errors.New("injected cleanup failure")
	}
	if err := receiver.Cleanup(context.Background(), "migration_0000001"); err == nil {
		t.Fatal("cleanup failure reported success")
	}
	var status, cleanup, lockStatus string
	_ = database.GetDB().QueryRow(`SELECT status,cleanup_status FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &cleanup)
	_ = database.GetDB().QueryRow(`SELECT status FROM site_migration_locks WHERE migration_site_id='migration_0000001'`).Scan(&lockStatus)
	if status != "cleanup_failed" || cleanup != "failed" || lockStatus != "active" {
		t.Fatalf("status=%q cleanup=%q lock=%q", status, cleanup, lockStatus)
	}
}

func TestSiteMigrationTargetPrepareRejectsInsufficientSpaceWithoutArtifacts(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	receiver.available = func(path string) (uint64, error) {
		if path != root {
			t.Fatalf("space checked unexpected path=%q", path)
		}
		return 1, nil
	}
	dbArtifact := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Size: 2, SHA256: migrationTestHash([]byte("db"))}
	if err := receiver.Prepare(context.Background(), "migration_0000001", nil, dbArtifact); err == nil {
		t.Fatal("insufficient staging space accepted")
	}
	var artifacts, resources int
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id='migration_0000001'`).Scan(&artifacts)
	_ = database.GetDB().QueryRow(`SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id='migration_0000001'`).Scan(&resources)
	if artifacts != 0 || resources != 0 {
		t.Fatalf("artifacts=%d resources=%d", artifacts, resources)
	}
}

func setupMigrationTargetReceiverTest(t *testing.T) (*SiteMigrationTargetReceiver, string) {
	t.Helper()
	store, _ := newSiteMigrationStoreTest(t)
	if _, err := store.db.Exec(`UPDATE site_migration_batches SET direction='target',status='active' WHERE id='batch_0000000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET stage='transferring_files' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status) VALUES ('example.com',NULL,'migration_0000001','target','active')`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	receiver, err := NewSiteMigrationTargetReceiver(store.db, root)
	if err != nil {
		t.Fatal(err)
	}
	receiver.now = freezerTestTime
	return receiver, root
}

func migrationTestHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
