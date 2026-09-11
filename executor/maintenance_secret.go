package executor

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// The key lives beside the panel database, never in a website or the database.
// Publish a complete key atomically; never replace an existing key on failure.
func (m *MaintenanceManager) secretKey(create bool) ([]byte, error) {
	var seq int
	var name, file string
	if err := m.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil || file == "" {
		return nil, ErrMaintenanceUnknown
	}
	path := filepath.Join(filepath.Dir(file), "maintenance.key")
	if _, err := os.Lstat(path); os.IsNotExist(err) && create {
		var encrypted int
		if err := m.db.QueryRow(`SELECT COUNT(*) FROM websites WHERE maintenance_security LIKE '%"password_ciphertext"%'`).Scan(&encrypted); err != nil || encrypted > 0 {
			return nil, ErrMaintenanceUnknown // Restore the key; never silently invalidate other sites.
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".maintenance-key-")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())
		if _, err = tmp.Write(key); err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if err = os.Link(tmp.Name(), path); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrMaintenanceUnknown
	}
	key, err := os.ReadFile(path)
	if err != nil || len(key) != 32 {
		return nil, ErrMaintenanceUnknown
	}
	return key, nil
}

func (m *MaintenanceManager) sealPassword(id int, password string) (string, error) {
	key, err := m.secretKey(true)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(password), []byte(fmt.Sprintf("maintenance:v1:%d", id)))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// RevealPassword is for the session/CSRF-protected owner endpoint only.
// Status() and plugin APIs deliberately do not call it.
func (m *MaintenanceManager) RevealPassword(id int) (string, error) {
	_, state, _, err := m.load(id)
	if err != nil {
		return "", ErrMaintenanceUnknown
	}
	if state.Ciphertext == "" {
		return "", nil
	} // Legacy hash: must set a new password.
	key, err := m.secretKey(false)
	if err != nil {
		return "", ErrMaintenanceUnknown
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", ErrMaintenanceUnknown
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", ErrMaintenanceUnknown
	}
	sealed, err := base64.StdEncoding.DecodeString(state.Ciphertext)
	if err != nil || len(sealed) < gcm.NonceSize() {
		return "", ErrMaintenanceUnknown
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte(fmt.Sprintf("maintenance:v1:%d", id)))
	if err != nil {
		return "", ErrMaintenanceUnknown
	}
	return string(plain), nil
}
