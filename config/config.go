package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type PanelConfig struct {
	Version      string `json:"version"`
	Port         int    `json:"port"`
	TLSPort      int    `json:"tls_port"`
	TLSCertPath  string `json:"tls_cert_path"`
	TLSKeyPath   string `json:"tls_key_path"`
	RandomSuffix string `json:"random_suffix"`
	DataDir      string `json:"data_dir"`
	BackupDir    string `json:"backup_dir"`
	LogDir       string `json:"log_dir"`
}

type SQLiteConfig struct {
	Path string `json:"path"`
}

type MariaDBConfig struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Socket       string `json:"socket"`
	RootUser     string `json:"root_user"`
	RootPassword string `json:"root_password"`
}

type AdminConfig struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

type BasicAuthConfig struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

type PathsConfig struct {
	WWWRoot             string `json:"www_root"`
	WWWLogs             string `json:"www_logs"`
	NginxSitesAvailable string `json:"nginx_sites_available"`
	NginxSitesEnabled   string `json:"nginx_sites_enabled"`
	PHPFPMPool          string `json:"php_fpm_pool"`
	PHPFPMSock          string `json:"php_fpm_sock"`
	Certificates        string `json:"certificates"`
	WordPressPackage    string `json:"wordpress_package"`
	CronFile            string `json:"cron_file"`
}

type SecurityConfig struct {
	BasicAuthEnabled     bool  `json:"basic_auth_enabled"`
	MaxLoginAttempts     int   `json:"max_login_attempts"`
	AttemptWindowMinutes int   `json:"attempt_window_minutes"`
	BanDurationHours     int   `json:"ban_duration_hours"`
	AutoWhitelistEnabled bool  `json:"auto_whitelist_enabled"`
	CorePorts            []int `json:"core_ports"`
}

type SystemdConfig struct {
	ServiceName string `json:"service_name"`
	ServicePath string `json:"service_path"`
	BinaryPath  string `json:"binary_path"`
}

type Config struct {
	Panel     PanelConfig     `json:"panel"`
	SQLite    SQLiteConfig    `json:"sqlite"`
	MariaDB   MariaDBConfig   `json:"mariadb"`
	Admin     AdminConfig     `json:"admin"`
	BasicAuth BasicAuthConfig `json:"basic_auth"`
	Paths     PathsConfig     `json:"paths"`
	Security  SecurityConfig  `json:"security"`
	Systemd   SystemdConfig   `json:"systemd"`
}

var AppConfig *Config

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	AppConfig = cfg
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Panel.Port <= 0 || c.Panel.Port > 65535 {
		return fmt.Errorf("invalid panel port: %d", c.Panel.Port)
	}
	if c.Panel.RandomSuffix == "" {
		return fmt.Errorf("random_suffix must not be empty")
	}
	if c.SQLite.Path == "" {
		return fmt.Errorf("sqlite path must not be empty")
	}
	if c.MariaDB.RootPassword == "" {
		return fmt.Errorf("mariadb root password must not be empty")
	}
	if c.Admin.Username == "" || c.Admin.PasswordHash == "" {
		return fmt.Errorf("admin credentials incomplete")
	}
	return nil
}
