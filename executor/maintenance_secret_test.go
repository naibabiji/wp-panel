package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaintenanceSecretEncryptionAndLegacy(t *testing.T) {
	m, id, _, _, _ := maintenanceFixture(t)
	for _, password := range []string{"abc", "中文", strings.Repeat("x", 73)} {
		if err := m.Configure(id, true, 5, password); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if err := m.Configure(id, true, 5, "1234"); err != nil {
		t.Fatal(err)
	}
	got, err := NewMaintenanceManager(m.db).RevealPassword(id)
	if err != nil || got != "1234" {
		t.Fatalf("reveal: %q %v", got, err)
	}
	_, state, raw, err := m.load(id)
	if err != nil || strings.Contains(raw, "1234") || state.Ciphertext == "" {
		t.Fatal("plaintext persisted or missing encryption")
	}
	public, _ := m.Status(id)
	b, _ := json.Marshal(public)
	if strings.Contains(string(b), "1234") || strings.Contains(string(b), "ciphertext") {
		t.Fatal("public status leaked secret")
	}
	// Copying ciphertext to a different site must fail authentication.
	_, err = m.db.Exec(`INSERT INTO websites(id,name,domain,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,maintenance_security) VALUES(999,'other','other.test','wordpress','wp_other','/www/wwwroot/other.test','','','','','',?)`, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.RevealPassword(999); err == nil {
		t.Fatal("cross-site ciphertext accepted")
	}
	if _, err := m.db.Exec(`UPDATE websites SET maintenance_security=json_remove(maintenance_security,'$.password_ciphertext') WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if got, err := m.RevealPassword(id); err != nil || got != "" {
		t.Fatal("legacy must require reset")
	}
	if err := m.Configure(id, true, 5, "abcd"); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, file string
	if err := m.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(filepath.Dir(file), "maintenance.key")
	info, err := os.Stat(key)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key permissions")
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RevealPassword(id); err == nil {
		t.Fatal("missing key accepted")
	}
	if err := m.Configure(id, true, 5, "new1"); err == nil {
		t.Fatal("missing key silently replaced")
	}
}
