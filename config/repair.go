package config

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type RepairConfigCheck struct {
	Status    string   `json:"status"`
	TLSAction string   `json:"tls_action"`
	Warnings  []string `json:"warnings,omitempty"`
}

// CheckRepairConfig validates an existing config without changing files,
// opening SQLite, contacting MariaDB, or starting services.
func CheckRepairConfig(path string) (*RepairConfigCheck, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("config_unreadable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("config_not_regular")
	}
	if info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("config_permissions_unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.New("config_ownership_unsafe")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config_unreadable: %w", err)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("config_json_invalid: %w", err)
	}

	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config_json_invalid: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("config_json_invalid: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config_required_field_invalid: %w", err)
	}
	if err := validateRepairFields(&cfg); err != nil {
		return nil, err
	}
	if err := checkRepairSQLite(cfg.SQLite.Path); err != nil {
		return nil, err
	}

	tlsAction, tlsWarning, err := repairTLSAction(&cfg, time.Now())
	if err != nil {
		return nil, err
	}
	result := &RepairConfigCheck{Status: "valid", TLSAction: tlsAction}
	if tlsWarning != "" {
		result.Warnings = []string{tlsWarning}
	}
	return result, nil
}

func checkRepairSQLite(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("sqlite_invalid")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return errors.New("sqlite_invalid")
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		return errors.New("sqlite_integrity_failed")
	}
	return nil
}

func validateRepairFields(cfg *Config) error {
	requiredStrings := map[string]string{
		"panel.version":               cfg.Panel.Version,
		"panel.data_dir":              cfg.Panel.DataDir,
		"panel.backup_dir":            cfg.Panel.BackupDir,
		"panel.log_dir":               cfg.Panel.LogDir,
		"panel.tls_cert_path":         cfg.Panel.TLSCertPath,
		"panel.tls_key_path":          cfg.Panel.TLSKeyPath,
		"mariadb.host":                cfg.MariaDB.Host,
		"mariadb.socket":              cfg.MariaDB.Socket,
		"mariadb.root_user":           cfg.MariaDB.RootUser,
		"basic_auth.username":         cfg.BasicAuth.Username,
		"basic_auth.password_hash":    cfg.BasicAuth.PasswordHash,
		"paths.www_root":              cfg.Paths.WWWRoot,
		"paths.www_logs":              cfg.Paths.WWWLogs,
		"paths.nginx_sites_available": cfg.Paths.NginxSitesAvailable,
		"paths.nginx_sites_enabled":   cfg.Paths.NginxSitesEnabled,
		"paths.php_fpm_pool":          cfg.Paths.PHPFPMPool,
		"paths.php_fpm_sock":          cfg.Paths.PHPFPMSock,
		"paths.certificates":          cfg.Paths.Certificates,
		"paths.wordpress_package":     cfg.Paths.WordPressPackage,
		"paths.cron_file":             cfg.Paths.CronFile,
		"systemd.service_name":        cfg.Systemd.ServiceName,
		"systemd.service_path":        cfg.Systemd.ServicePath,
		"systemd.binary_path":         cfg.Systemd.BinaryPath,
	}
	for field, value := range requiredStrings {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("config_required_field_invalid: %s", field)
		}
	}
	if cfg.Panel.TLSPort <= 0 || cfg.Panel.TLSPort > 65535 || cfg.MariaDB.Port <= 0 || cfg.MariaDB.Port > 65535 {
		return errors.New("config_required_field_invalid: port")
	}
	if !strings.HasPrefix(cfg.Admin.PasswordHash, "$2") || !strings.HasPrefix(cfg.BasicAuth.PasswordHash, "$2") {
		return errors.New("config_required_field_invalid: password_hash")
	}

	absolutePaths := map[string]string{
		"panel.data_dir": cfg.Panel.DataDir, "panel.backup_dir": cfg.Panel.BackupDir,
		"panel.log_dir": cfg.Panel.LogDir, "panel.tls_cert_path": cfg.Panel.TLSCertPath,
		"panel.tls_key_path": cfg.Panel.TLSKeyPath, "sqlite.path": cfg.SQLite.Path,
		"mariadb.socket": cfg.MariaDB.Socket, "paths.www_root": cfg.Paths.WWWRoot,
		"paths.www_logs": cfg.Paths.WWWLogs, "paths.nginx_sites_available": cfg.Paths.NginxSitesAvailable,
		"paths.nginx_sites_enabled": cfg.Paths.NginxSitesEnabled, "paths.php_fpm_pool": cfg.Paths.PHPFPMPool,
		"paths.php_fpm_sock": cfg.Paths.PHPFPMSock, "paths.certificates": cfg.Paths.Certificates,
		"paths.wordpress_package": cfg.Paths.WordPressPackage, "paths.cron_file": cfg.Paths.CronFile,
		"systemd.service_path": cfg.Systemd.ServicePath, "systemd.binary_path": cfg.Systemd.BinaryPath,
	}
	for field, value := range absolutePaths {
		if !filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("config_required_field_invalid: %s", field)
		}
	}
	return nil
}

func repairTLSAction(cfg *Config, now time.Time) (string, string, error) {
	certInfo, certErr := os.Lstat(cfg.Panel.TLSCertPath)
	keyInfo, keyErr := os.Lstat(cfg.Panel.TLSKeyPath)
	certMissing := errors.Is(certErr, os.ErrNotExist)
	keyMissing := errors.Is(keyErr, os.ErrNotExist)

	if certMissing && keyMissing {
		controlledDir := filepath.Join(filepath.Clean(cfg.Panel.DataDir), "certs")
		if filepath.Dir(filepath.Clean(cfg.Panel.TLSCertPath)) != controlledDir || filepath.Dir(filepath.Clean(cfg.Panel.TLSKeyPath)) != controlledDir {
			return "", "", errors.New("tls_custom_pair_missing")
		}
		return "generate", "", nil
	}
	if certMissing != keyMissing {
		return "", "", errors.New("tls_pair_incomplete")
	}
	if certErr != nil || keyErr != nil {
		return "", "", errors.New("tls_pair_unreadable")
	}
	if !certInfo.Mode().IsRegular() || !keyInfo.Mode().IsRegular() || certInfo.Mode()&os.ModeSymlink != 0 || keyInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("tls_pair_not_regular")
	}
	if keyInfo.Mode().Perm()&0077 != 0 || certInfo.Mode().Perm()&0022 != 0 {
		return "", "", errors.New("tls_pair_permissions_unsafe")
	}
	pair, err := tls.LoadX509KeyPair(cfg.Panel.TLSCertPath, cfg.Panel.TLSKeyPath)
	if err != nil {
		return "", "", errors.New("tls_pair_invalid")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return "", "", errors.New("tls_pair_invalid")
	}
	switch {
	case !certificate.NotAfter.After(now):
		return "preserve", "tls_certificate_expired", nil
	case certificate.NotAfter.Before(now.Add(30 * 24 * time.Hour)):
		return "preserve", "tls_certificate_expires_soon", nil
	default:
		return "preserve", "", nil
	}
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}
