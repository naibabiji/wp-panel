package database

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeIssuerCertificate(t *testing.T, organization string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com", Organization: []string{organization}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fullchain.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCertificateIssuedByLetsEncrypt(t *testing.T) {
	if !certificateIssuedByLetsEncrypt(writeIssuerCertificate(t, "Let's Encrypt")) {
		t.Fatal("Let's Encrypt certificate was not classified as automatic")
	}
	if certificateIssuedByLetsEncrypt(writeIssuerCertificate(t, "Example Commercial CA")) {
		t.Fatal("non-Let's Encrypt certificate was classified as automatic")
	}
	if certificateIssuedByLetsEncrypt(filepath.Join(t.TempDir(), "missing.pem")) {
		t.Fatal("missing certificate was classified as automatic")
	}
}

func TestUpgradeBackfillsExistingSSLCertificateSources(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	autoPath := writeIssuerCertificate(t, "Let's Encrypt")
	manualPath := writeIssuerCertificate(t, "Example Commercial CA")
	for id, item := range []struct {
		domain string
		path   string
	}{{"auto.example", autoPath}, {"manual.example", manualPath}} {
		if _, err := DB.Exec(`INSERT INTO websites (id,name,domain,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,ssl_enabled,ssl_cert_path) VALUES (?,?,?,?,?,?,?,?,?,?,1,?)`, id+1, item.domain, item.domain, "wp_test", "/tmp/www", "/tmp/log", "db", "user", "/tmp/php", "/tmp/nginx", item.path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DB.Exec(`ALTER TABLE websites DROP COLUMN ssl_cert_source`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DELETE FROM schema_version`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO schema_version(version) VALUES ('1.0.64')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]string{"auto.example": "auto", "manual.example": "manual"} {
		var got string
		if err := DB.QueryRow(`SELECT ssl_cert_source FROM websites WHERE domain=?`, domain).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s source=%q, want %q", domain, got, want)
		}
	}
}
