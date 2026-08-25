package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/database"
)

func TestSiteMigrationCertificatesCopyOriginalBytesWithoutCertificateValidation(t *testing.T) {
	files, _ := setupMigrationSourceFilesTest(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "expired-or-origin-cert.pem")
	keyPath := filepath.Join(dir, "private.key")
	certData := []byte("not parsed: expired, self-signed, or Cloudflare origin certificate bytes")
	keyData := []byte("private key bytes kept exactly")
	if err := os.WriteFile(certPath, certData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE websites SET ssl_enabled=1,ssl_cert_path=?,ssl_key_path=? WHERE domain='example.com'`, certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET stage='transferring_database' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	certificates, err := newSiteMigrationSourceCertificates(database.GetDB(), filepath.Join(t.TempDir(), "certificates"))
	if err != nil {
		t.Fatal(err)
	}
	if err := certificates.Build(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	credential := strings.Repeat("c", 48)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_peers SET inbound_credential_hash=? WHERE id='peer_00000000001'`, hashMigrationSecret(credential)); err != nil {
		t.Fatal(err)
	}
	service := &SiteMigrationSourceService{db: database.GetDB(), pairing: &SiteMigrationPairingService{db: database.GetDB()}, files: files, certificates: certificates}
	artifacts, err := service.ListCertificateArtifacts(context.Background(), "peer_00000000001", "migration_0000001", credential)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
	chunk, err := service.ReadCertificateChunk(context.Background(), "peer_00000000001", "migration_0000001", credential, "private_key", 0, int64(len(keyData)))
	if err != nil || string(chunk.Data) != string(keyData) {
		t.Fatalf("chunk=%+v err=%v", chunk, err)
	}
	var stagedKey string
	if err := database.GetDB().QueryRow(`SELECT staging_key FROM site_migration_artifacts WHERE migration_site_id='migration_0000001' AND artifact_type='private_key'`).Scan(&stagedKey); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(stagedKey)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private key mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestSiteMigrationCertificatesDisabledProducesNoArtifacts(t *testing.T) {
	setupMigrationSourceFilesTest(t)
	if _, err := database.GetDB().Exec(`UPDATE site_migration_sites SET stage='transferring_database' WHERE id='migration_0000001'`); err != nil {
		t.Fatal(err)
	}
	certificates, err := newSiteMigrationSourceCertificates(database.GetDB(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := certificates.Build(context.Background(), "migration_0000001"); err != nil {
		t.Fatal(err)
	}
	items, err := certificates.List(context.Background(), "migration_0000001")
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}
