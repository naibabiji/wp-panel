package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckRepairConfigPreservesValidFile(t *testing.T) {
	path, original := writeRepairConfig(t, "")

	result, err := CheckRepairConfig(path)
	if err != nil {
		t.Fatalf("CheckRepairConfig() error = %v", err)
	}
	if result.Status != "valid" || result.TLSAction != "generate" {
		t.Fatalf("result = %+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatal("read-only repair check changed config bytes")
	}
}

func TestCheckRepairConfigPreservesExistingTLSPair(t *testing.T) {
	path, original := writeRepairConfig(t, "")
	certPath := filepath.Join(filepath.Dir(path), "certs", "panel.crt")
	keyPath := filepath.Join(filepath.Dir(path), "certs", "panel.key")
	writeRepairTLSFixture(t, certPath, keyPath, time.Now().Add(90*24*time.Hour))

	result, err := CheckRepairConfig(path)
	if err != nil {
		t.Fatalf("CheckRepairConfig() error = %v", err)
	}
	if result.TLSAction != "preserve" {
		t.Fatalf("TLSAction = %q", result.TLSAction)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("Warnings = %v", result.Warnings)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != original {
		t.Fatal("TLS validation changed config")
	}
}

func TestCheckRepairConfigWarnsForExpiringAndExpiredTLS(t *testing.T) {
	tests := []struct {
		name     string
		notAfter time.Time
		warning  string
	}{
		{name: "expires soon", notAfter: time.Now().Add(10 * 24 * time.Hour), warning: "tls_certificate_expires_soon"},
		{name: "expired", notAfter: time.Now().Add(-time.Hour), warning: "tls_certificate_expired"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := writeRepairConfig(t, "")
			writeRepairTLSFixture(t, filepath.Join(filepath.Dir(path), "certs", "panel.crt"), filepath.Join(filepath.Dir(path), "certs", "panel.key"), tc.notAfter)
			result, err := CheckRepairConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if result.TLSAction != "preserve" || len(result.Warnings) != 1 || result.Warnings[0] != tc.warning {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestCheckRepairConfigRejectsDuplicateAndMissingIdentity(t *testing.T) {
	path, original := writeRepairConfig(t, "")
	duplicate := strings.Replace(original, `"version":"legacy",`, `"version":"legacy","version":"other",`, 1)
	if err := os.WriteFile(path, []byte(duplicate), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRepairConfig(path); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate key error = %v", err)
	}

	missing := strings.Replace(original, `"random_suffix":"entry",`, `"random_suffix":"",`, 1)
	if err := os.WriteFile(path, []byte(missing), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRepairConfig(path); err == nil || !strings.Contains(err.Error(), "random_suffix") {
		t.Fatalf("missing identity error = %v", err)
	}
}

func TestCheckRepairConfigRejectsUnsafeFileAndCustomMissingTLS(t *testing.T) {
	path, _ := writeRepairConfig(t, "")
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRepairConfig(path); err == nil || !strings.Contains(err.Error(), "permissions_unsafe") {
		t.Fatalf("unsafe permissions error = %v", err)
	}

	path, original := writeRepairConfig(t, "")
	custom := strings.Replace(original, filepath.Dir(path)+`/certs/panel.crt`, `/custom/panel.crt`, 1)
	custom = strings.Replace(custom, filepath.Dir(path)+`/certs/panel.key`, `/custom/panel.key`, 1)
	if err := os.WriteFile(path, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckRepairConfig(path); err == nil || !strings.Contains(err.Error(), "tls_custom_pair_missing") {
		t.Fatalf("custom missing TLS error = %v", err)
	}
}

func writeRepairConfig(t *testing.T, panelOverrides string) (string, string) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	content := `{
"panel":{"version":"legacy","port":8888,"tls_port":8443,"tls_cert_path":"` + filepath.Join(root, "certs", "panel.crt") + `","tls_key_path":"` + filepath.Join(root, "certs", "panel.key") + `","random_suffix":"entry","data_dir":"` + root + `","backup_dir":"` + filepath.Join(root, "backups") + `","log_dir":"` + filepath.Join(root, "logs") + `"` + panelOverrides + `},
"sqlite":{"path":"` + filepath.Join(root, "panel.db") + `"},
"mariadb":{"host":"localhost","port":3306,"socket":"/run/mysqld/mysqld.sock","root_user":"root","root_password":"secret"},
"admin":{"username":"wpadmin","password_hash":"$2a$12$admin"},
"basic_auth":{"username":"admin","password_hash":"$2a$12$basic"},
"paths":{"www_root":"/www/wwwroot","www_logs":"/www/wwwlogs","nginx_sites_available":"/etc/nginx/sites-available","nginx_sites_enabled":"/etc/nginx/sites-enabled","php_fpm_pool":"/etc/php/8.3/fpm/pool.d","php_fpm_sock":"/run/php","certificates":"/www/server/certificates","wordpress_package":"` + filepath.Join(root, "packages", "wordpress.zip") + `","cron_file":"/etc/cron.d/wp_panel_cron"},
"security":{"basic_auth_enabled":true,"max_login_attempts":5,"attempt_window_minutes":5,"ban_duration_hours":24,"auto_whitelist_enabled":true,"core_ports":[22,80,443,8443]},
"systemd":{"service_name":"wp-panel","service_path":"/etc/systemd/system/wp-panel.service","binary_path":"/usr/local/bin/wp-panel"},
"future":{"preserve":true}
}`
	if panelOverrides != "" {
		t.Fatal("panel overrides are not supported")
	}
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE repair_fixture (id INTEGER PRIMARY KEY)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return configPath, content
}

func writeRepairTLSFixture(t *testing.T, certPath, keyPath string, notAfter time.Time) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "repair.test"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0700); err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, cert, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, privateKey, 0600); err != nil {
		t.Fatal(err)
	}
}
