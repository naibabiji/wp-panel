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
	if err := m.Configure(id, true, 5, "new1"); err != nil {
		t.Fatalf("reset after missing key: %v", err)
	}
	if got, err := m.RevealPassword(id); err != nil || got != "new1" {
		t.Fatalf("reveal reset password: %q %v", got, err)
	}
}

func TestMaintenanceSecretRekeyKeepsOtherSiteHashValid(t *testing.T) {
	m, firstID, _, _, _ := maintenanceFixture(t)
	if err := m.Configure(firstID, true, 5, "site-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(`INSERT INTO websites(id,name,domain,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,file_lock_enabled,file_lock_mode,file_lock_apply_status) VALUES(999,'other','other.test','active','wordpress','wp_other','/www/wwwroot/other.test','','','','','',1,'standard','ready')`); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(999, true, 5, "site-two"); err != nil {
		t.Fatal(err)
	}

	var seq int
	var name, file string
	if err := m.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(filepath.Dir(file), "maintenance.key")
	if err := os.WriteFile(keyPath, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RevealPassword(999); err == nil {
		t.Fatal("damaged key accepted")
	}
	if err := m.Configure(firstID, true, 5, "site-one-new"); err != nil {
		t.Fatalf("reset after damaged key: %v", err)
	}
	if got, err := m.RevealPassword(firstID); err != nil || got != "site-one-new" {
		t.Fatalf("reveal reset password: %q %v", got, err)
	}
	if _, err := m.RevealPassword(999); err == nil {
		t.Fatal("old ciphertext unexpectedly decrypted with replacement key")
	}
	_, state, raw, err := m.load(999)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.password(999, &state, &raw, "site-two", "test", "other-site-password-check"); err != nil {
		t.Fatalf("other site bcrypt password stopped validating: %v", err)
	}
}

func TestMaintenanceSecretRejectsUnsafeKeyPathsAndRepairsPermissionsOnSave(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			m, id, _, _, _ := maintenanceFixture(t)
			var seq int
			var name, file string
			if err := m.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(filepath.Dir(file), "maintenance.key")
			if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if kind == "symlink" {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, make([]byte, 32), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, keyPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(keyPath, 0700); err != nil {
				t.Fatal(err)
			}
			if err := m.Configure(id, true, 5, "safe-password"); err == nil {
				t.Fatalf("%s key path accepted", kind)
			}
			info, err := os.Lstat(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" && info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("symlink key path was replaced")
			}
			if kind == "directory" && !info.IsDir() {
				t.Fatal("directory key path was replaced")
			}
		})
	}

	t.Run("wide permissions", func(t *testing.T) {
		m, id, _, _, _ := maintenanceFixture(t)
		if err := m.Configure(id, true, 5, "first-password"); err != nil {
			t.Fatal(err)
		}
		var seq int
		var name, file string
		if err := m.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(filepath.Dir(file), "maintenance.key")
		if err := os.Chmod(keyPath, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := m.RevealPassword(id); err == nil {
			t.Fatal("wide-permission key accepted for reveal")
		}
		if err := m.Configure(id, true, 5, "second-password"); err != nil {
			t.Fatalf("save did not repair key permissions: %v", err)
		}
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("key permissions = %v", info.Mode().Perm())
		}
		if got, err := m.RevealPassword(id); err != nil || got != "second-password" {
			t.Fatalf("reveal after permission repair: %q %v", got, err)
		}
	})
}
