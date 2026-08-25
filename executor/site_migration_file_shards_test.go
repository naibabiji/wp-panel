package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestSiteMigrationFileShardRoundTrip(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	sourceRoot := t.TempDir()
	files := map[string][]byte{"assets/one.php": []byte("first file"), "assets/two.php": []byte("second file")}
	entries := make([]SiteMigrationManifestEntry, 0, len(files))
	var shardEntries []siteMigrationShardEntry
	var id int64
	for _, name := range []string{"assets/one.php", "assets/two.php"} {
		data := files[name]
		path := filepath.Join(sourceRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0640); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		entry := SiteMigrationManifestEntry{RelativePath: name, EntryType: "file", Mode: 0640, Size: int64(len(data)), SHA256: migrationTestHash(data), ModifiedUnixNS: info.ModTime().UnixNano()}
		entries = append(entries, entry)
		id++
		shardEntries = append(shardEntries, siteMigrationShardEntry{ID: id, SiteMigrationManifestEntry: entry, Status: "verified"})
	}
	databaseData := []byte("database")
	databaseEntry := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Mode: 0600, Size: int64(len(databaseData)), SHA256: migrationTestHash(databaseData)}
	if err := receiver.Prepare(context.Background(), "migration_0000001", entries, databaseEntry); err != nil {
		t.Fatal(err)
	}
	staleExtractRoot := filepath.Join(root, "migration_0000001", "shards", "extract-000000")
	if err := os.MkdirAll(staleExtractRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := receiver.ReceiveFileShard(context.Background(), "migration_0000001", 0, entries, func(dst io.Writer) error {
		if _, err := os.Stat(staleExtractRoot); !os.IsNotExist(err) {
			return errors.New("stale extraction directory was not removed before download")
		}
		return writeMigrationFileShard(context.Background(), sourceRoot, shardEntries, dst)
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(root, "migration_0000001", "files", filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("file %s=%q err=%v", name, got, err)
		}
	}
	var verified int
	if err := receiver.db.QueryRow(`SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id='migration_0000001' AND artifact_type='file' AND status='verified'`).Scan(&verified); err != nil || verified != 2 {
		t.Fatalf("verified=%d err=%v", verified, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "migration_0000001", "shards", "*.partial")); len(matches) != 0 {
		t.Fatalf("compressed shards remain: %v", matches)
	}
}

func TestSiteMigrationFileShardRejectsHardLinkBeforePublishing(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	data := []byte("content")
	entry := SiteMigrationManifestEntry{RelativePath: "index.php", EntryType: "file", Mode: 0644, Size: int64(len(data)), SHA256: migrationTestHash(data)}
	databaseEntry := SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Mode: 0600, Size: 1, SHA256: migrationTestHash([]byte("d"))}
	if err := receiver.Prepare(context.Background(), "migration_0000001", []SiteMigrationManifestEntry{entry}, databaseEntry); err != nil {
		t.Fatal(err)
	}
	err := receiver.ReceiveFileShard(context.Background(), "migration_0000001", 0, []SiteMigrationManifestEntry{entry}, func(dst io.Writer) error {
		encoder, err := zstd.NewWriter(dst)
		if err != nil {
			return err
		}
		tw := tar.NewWriter(encoder)
		if err := tw.WriteHeader(&tar.Header{Name: "index.php", Linkname: "outside", Typeflag: tar.TypeLink}); err != nil {
			return err
		}
		if err := tw.Close(); err != nil {
			return err
		}
		return encoder.Close()
	})
	if err == nil {
		t.Fatal("hard link shard accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "migration_0000001", "files", "index.php")); !os.IsNotExist(err) {
		t.Fatalf("hard link failure published a file: %v", err)
	}
}
