package executor

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/naibabiji/wp-panel/database"
)

type fakeSiteMigrationTransferSource struct {
	files        []SiteMigrationManifestEntry
	data         map[string][]byte
	database     SiteMigrationManifestEntry
	databaseData []byte
	certificates []SiteMigrationCertificateArtifact
	certData     map[string][]byte
	failPath     string
	reads        map[string]int
	block        chan struct{}
	release      chan struct{}
	blockOnce    sync.Once
}

func (f *fakeSiteMigrationTransferSource) ListManifest(_ context.Context, _, _ string, after int64, _ int) ([]SiteMigrationManifestEntry, int64, bool, error) {
	if f.block != nil {
		f.blockOnce.Do(func() {
			close(f.block)
			<-f.release
		})
	}
	if after != 0 {
		return nil, after, false, nil
	}
	return f.files, int64(len(f.files)), false, nil
}
func (f *fakeSiteMigrationTransferSource) DatabaseArtifact(context.Context, string, string) (SiteMigrationManifestEntry, error) {
	return f.database, nil
}
func (f *fakeSiteMigrationTransferSource) CertificateArtifacts(context.Context, string, string) ([]SiteMigrationCertificateArtifact, error) {
	return f.certificates, nil
}
func (f *fakeSiteMigrationTransferSource) ReadFileChunk(_ context.Context, _, _, path string, offset, length int64) (*SiteMigrationFileChunk, error) {
	f.reads[path]++
	if f.failPath == path {
		f.failPath = ""
		return nil, errors.New("injected source read failure")
	}
	return migrationTransferTestChunk(path, f.data[path], offset, length), nil
}
func (f *fakeSiteMigrationTransferSource) WriteFileShard(_ context.Context, _, _ string, index, expectedEntries int, expectedBytes int64, dst io.Writer) error {
	var small []SiteMigrationManifestEntry
	for _, entry := range f.files {
		if entry.EntryType == "file" && entry.Size > 0 && entry.Size <= SiteMigrationFileShardBytes {
			small = append(small, entry)
		}
	}
	var shards [][]SiteMigrationManifestEntry
	var current []SiteMigrationManifestEntry
	var size int64
	for _, entry := range small {
		if len(current) > 0 && size > SiteMigrationFileShardBytes-entry.Size {
			shards = append(shards, current)
			current, size = nil, 0
		}
		current, size = append(current, entry), size+entry.Size
	}
	if len(current) > 0 {
		shards = append(shards, current)
	}
	if index < 0 || index >= len(shards) || len(shards[index]) != expectedEntries {
		return errors.New("unexpected shard")
	}
	encoder, err := zstd.NewWriter(dst, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	tw := tar.NewWriter(encoder)
	var total int64
	for _, entry := range shards[index] {
		data := f.data[entry.RelativePath]
		f.reads[entry.RelativePath]++
		if f.failPath == entry.RelativePath {
			f.failPath = ""
			return errors.New("injected source read failure")
		}
		if err := tw.WriteHeader(&tar.Header{Name: entry.RelativePath, Mode: int64(entry.Mode), Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
		total += int64(len(data))
	}
	if total != expectedBytes {
		return errors.New("unexpected shard bytes")
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return encoder.Close()
}
func (f *fakeSiteMigrationTransferSource) ReadDatabaseChunk(_ context.Context, _, _ string, offset, length int64) (*SiteMigrationFileChunk, error) {
	f.reads[siteMigrationDatabaseArtifactName]++
	return migrationTransferTestChunk(siteMigrationDatabaseArtifactName, f.databaseData, offset, length), nil
}
func (f *fakeSiteMigrationTransferSource) ReadCertificateChunk(_ context.Context, _, _, artifactType string, offset, length int64) (*SiteMigrationFileChunk, error) {
	f.reads[artifactType]++
	path := "certificate.pem"
	if artifactType == "private_key" {
		path = "private-key.pem"
	}
	return migrationTransferTestChunk(path, f.certData[artifactType], offset, length), nil
}
func (f *fakeSiteMigrationTransferSource) RuntimeSettings(context.Context, string, string) (SiteMigrationRuntimeSettings, error) {
	return SiteMigrationRuntimeSettings{}, nil
}

func migrationTransferTestChunk(path string, data []byte, offset, length int64) *SiteMigrationFileChunk {
	part := append([]byte(nil), data[offset:offset+length]...)
	return &SiteMigrationFileChunk{RelativePath: path, Offset: offset, TotalSize: int64(len(data)), FileSHA256: migrationTestHash(data), ChunkSHA256: migrationTestHash(part), Data: part}
}

func TestSiteMigrationTargetTransferRetriesFromConfirmedArtifacts(t *testing.T) {
	receiver, root := setupMigrationTargetReceiverTest(t)
	if _, err := database.GetDB().Exec(`DELETE FROM site_migration_locks WHERE migration_site_id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET status='running',stage='preflight_passed' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, freezerTestTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	one, two, dump := []byte("first"), []byte("second"), []byte("database")
	certificate, privateKey := []byte("certificate bytes"), []byte("private key bytes")
	source := &fakeSiteMigrationTransferSource{
		files: []SiteMigrationManifestEntry{
			{RelativePath: "one.php", EntryType: "file", Mode: 0644, Size: int64(len(one)), SHA256: migrationTestHash(one)},
			{RelativePath: "two.php", EntryType: "file", Mode: 0644, Size: int64(len(two)), SHA256: migrationTestHash(two)},
		},
		data:         map[string][]byte{"one.php": one, "two.php": two},
		database:     SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Mode: 0600, Size: int64(len(dump)), SHA256: migrationTestHash(dump)},
		databaseData: dump,
		certificates: []SiteMigrationCertificateArtifact{
			{ArtifactType: "certificate", RelativePath: "certificate.pem", Mode: 0644, Size: int64(len(certificate)), SHA256: migrationTestHash(certificate)},
			{ArtifactType: "private_key", RelativePath: "private-key.pem", Mode: 0600, Size: int64(len(privateKey)), SHA256: migrationTestHash(privateKey)},
		},
		certData: map[string][]byte{"certificate": certificate, "private_key": privateKey},
		failPath: "two.php",
		reads:    make(map[string]int),
	}
	service, err := NewSiteMigrationTargetTransferService(database.GetDB(), source, receiver)
	if err != nil {
		t.Fatal(err)
	}
	service.now = freezerTestTime
	if err := service.Transfer(context.Background(), "migration_0000001", "worker_0000000001"); err == nil {
		t.Fatal("injected transfer failure reported success")
	}
	var status, stage, code string
	_ = database.GetDB().QueryRow(`SELECT status,stage,error_code FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage, &code)
	if status != "failed_retryable" || stage != "transferring_files" || code != "target_transfer_failed" {
		t.Fatalf("status=%q stage=%q code=%q", status, stage, code)
	}
	if err := service.Transfer(context.Background(), "migration_0000001", "worker_0000000001"); err != nil {
		t.Fatal(err)
	}
	_ = database.GetDB().QueryRow(`SELECT status,stage FROM site_migration_sites WHERE id='migration_0000001'`).Scan(&status, &stage)
	if status != "running" || stage != "preparing_target" {
		t.Fatalf("status=%q stage=%q", status, stage)
	}
	if source.reads["one.php"] != 2 || source.reads["two.php"] != 2 || source.reads[siteMigrationDatabaseArtifactName] != 1 || source.reads["certificate"] != 1 || source.reads["private_key"] != 1 {
		t.Fatalf("reads=%v", source.reads)
	}
	for _, path := range []string{"files/one.php", "files/two.php", "database/" + siteMigrationDatabaseArtifactName, "certificates/certificate.pem", "certificates/private-key.pem"} {
		if _, err := receiverFileStat(root, "migration_0000001", path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
}

func TestSiteMigrationTargetTransferRejectsConcurrentCaller(t *testing.T) {
	receiver, _ := setupMigrationTargetReceiverTest(t)
	if _, err := database.GetDB().Exec(`DELETE FROM site_migration_locks WHERE migration_site_id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET status='running',stage='preflight_passed',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, freezerTestTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	dump := []byte("database")
	source := &fakeSiteMigrationTransferSource{
		database:     SiteMigrationManifestEntry{RelativePath: siteMigrationDatabaseArtifactName, EntryType: "file", Mode: 0600, Size: int64(len(dump)), SHA256: migrationTestHash(dump)},
		databaseData: dump,
		data:         make(map[string][]byte),
		certData:     make(map[string][]byte),
		reads:        make(map[string]int),
		block:        make(chan struct{}),
		release:      make(chan struct{}),
	}
	service, err := NewSiteMigrationTargetTransferService(database.GetDB(), source, receiver)
	if err != nil {
		t.Fatal(err)
	}
	service.now = freezerTestTime
	first := make(chan error, 1)
	go func() { first <- service.Transfer(context.Background(), "migration_0000001", "worker_0000000001") }()
	<-source.block
	if err := service.Transfer(context.Background(), "migration_0000001", "worker_0000000001"); err == nil {
		t.Fatal("concurrent target transfer was accepted")
	}
	close(source.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestSiteMigrationTargetTransferRejectsWrongOrExpiredLease(t *testing.T) {
	receiver, _ := setupMigrationTargetReceiverTest(t)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET status='running',stage='transferring_files',lease_owner='worker_0000000001',lease_expires_at=? WHERE id='migration_0000001'`, freezerTestTime().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	source := &fakeSiteMigrationTransferSource{reads: make(map[string]int)}
	service, _ := NewSiteMigrationTargetTransferService(database.GetDB(), source, receiver)
	service.now = freezerTestTime
	if err := service.Transfer(context.Background(), "migration_0000001", "worker_0000000001"); err == nil {
		t.Fatal("expired transfer lease was accepted")
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET lease_expires_at=? WHERE id='migration_0000001'`, freezerTestTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := service.Transfer(context.Background(), "migration_0000001", "worker_0000000002"); err == nil {
		t.Fatal("wrong transfer lease owner was accepted")
	}
}

func receiverFileStat(root, taskID, path string) (int64, error) {
	info, err := os.Stat(filepath.Join(root, taskID, filepath.FromSlash(path)))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
