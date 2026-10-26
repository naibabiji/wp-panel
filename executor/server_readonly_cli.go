package executor

import (
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type ReadOnlyCLIError struct {
	Code    string
	Message string
}

func (e *ReadOnlyCLIError) Error() string { return e.Message }

func readOnlyCLIError(code, message string) error {
	return &ReadOnlyCLIError{Code: code, Message: message}
}

type ReadOnlyPanelStatus struct {
	OK             bool                    `json:"ok"`
	PanelVersion   string                  `json:"panel_version"`
	CheckedAt      time.Time               `json:"checked_at"`
	CapabilitySync string                  `json:"capability_sync"`
	Database       ReadOnlyDependencyState `json:"database"`
	Services       map[string]string       `json:"services"`
	WebsiteCount   int                     `json:"website_count"`
}

type ReadOnlyDependencyState struct {
	State string `json:"state"`
}

type ReadOnlySiteSummary struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Domain     string     `json:"domain"`
	Aliases    string     `json:"aliases,omitempty"`
	SiteType   string     `json:"site_type"`
	Status     string     `json:"status"`
	SSLEnabled bool       `json:"ssl_enabled"`
	SSLExpires *time.Time `json:"ssl_expires_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type ReadOnlySiteList struct {
	OK           bool                  `json:"ok"`
	PanelVersion string                `json:"panel_version"`
	CheckedAt    time.Time             `json:"checked_at"`
	Sites        []ReadOnlySiteSummary `json:"sites"`
}

type ReadOnlySiteDetail struct {
	OK           bool                `json:"ok"`
	PanelVersion string              `json:"panel_version"`
	CheckedAt    time.Time           `json:"checked_at"`
	Site         ReadOnlySiteSummary `json:"site"`
}

type ReadOnlySSLStatus struct {
	OK           bool       `json:"ok"`
	PanelVersion string     `json:"panel_version"`
	CheckedAt    time.Time  `json:"checked_at"`
	Domain       string     `json:"domain"`
	Enabled      bool       `json:"enabled"`
	Source       string     `json:"source,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

type ReadOnlyBackupRecord struct {
	ID              int       `json:"id"`
	Kind            string    `json:"kind"`
	Filename        string    `json:"filename"`
	Size            int64     `json:"size"`
	Automatic       bool      `json:"automatic,omitempty"`
	Mode            string    `json:"mode,omitempty"`
	TransportStatus string    `json:"transport_status"`
	CreatedAt       time.Time `json:"created_at"`
}

type ReadOnlyBackupList struct {
	OK           bool                   `json:"ok"`
	PanelVersion string                 `json:"panel_version"`
	CheckedAt    time.Time              `json:"checked_at"`
	Domain       string                 `json:"domain"`
	Backups      []ReadOnlyBackupRecord `json:"backups"`
}

func GetReadOnlyPanelStatus(panelVersion string) (ReadOnlyPanelStatus, error) {
	db := database.GetDB()
	if db == nil {
		return ReadOnlyPanelStatus{}, readOnlyCLIError("database_unavailable", "面板数据库未打开")
	}
	if err := db.Ping(); err != nil {
		return ReadOnlyPanelStatus{}, readOnlyCLIError("database_unavailable", "面板数据库不可用")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM websites").Scan(&count); err != nil {
		return ReadOnlyPanelStatus{}, readOnlyCLIError("status_unavailable", "无法读取网站数量")
	}
	return ReadOnlyPanelStatus{
		OK:             true,
		PanelVersion:   panelVersion,
		CheckedAt:      time.Now().UTC(),
		CapabilitySync: PanelCapabilitySyncStatus(panelVersion),
		Database:       ReadOnlyDependencyState{State: "ok"},
		Services: map[string]string{
			"wp-panel": readOnlyServiceState("wp-panel"),
			"nginx":    readOnlyServiceState("nginx"),
			"mariadb":  readOnlyServiceState("mariadb"),
		},
		WebsiteCount: count,
	}, nil
}

func GetReadOnlySiteList(panelVersion string) (ReadOnlySiteList, error) {
	db := database.GetDB()
	if db == nil {
		return ReadOnlySiteList{}, readOnlyCLIError("database_unavailable", "面板数据库未打开")
	}
	rows, err := db.Query(readOnlySiteQuery + " ORDER BY created_at DESC, id DESC")
	if err != nil {
		return ReadOnlySiteList{}, readOnlyCLIError("sites_unavailable", "无法读取网站列表")
	}
	defer rows.Close()
	sites, err := scanReadOnlySites(rows)
	if err != nil {
		return ReadOnlySiteList{}, readOnlyCLIError("sites_unavailable", "读取网站列表失败")
	}
	return ReadOnlySiteList{OK: true, PanelVersion: panelVersion, CheckedAt: time.Now().UTC(), Sites: sites}, nil
}

func ReadOnlySiteShow(panelVersion, domain string) (ReadOnlySiteDetail, error) {
	domain = normalizeReadOnlyDomain(domain)
	if domain == "" {
		return ReadOnlySiteDetail{}, readOnlyCLIError("invalid_domain", "必须提供网站域名")
	}
	db := database.GetDB()
	if db == nil {
		return ReadOnlySiteDetail{}, readOnlyCLIError("database_unavailable", "面板数据库未打开")
	}
	row := db.QueryRow(readOnlySiteQuery+" WHERE lower(domain)=?", domain)
	var site ReadOnlySiteSummary
	if err := scanReadOnlySite(row.Scan, &site); errors.Is(err, sql.ErrNoRows) {
		return ReadOnlySiteDetail{}, readOnlyCLIError("site_not_found", "网站不存在")
	} else if err != nil {
		return ReadOnlySiteDetail{}, readOnlyCLIError("site_unavailable", "读取网站详情失败")
	}
	return ReadOnlySiteDetail{OK: true, PanelVersion: panelVersion, CheckedAt: time.Now().UTC(), Site: site}, nil
}

func ReadOnlySSLStatusByDomain(panelVersion, domain string) (ReadOnlySSLStatus, error) {
	domain = normalizeReadOnlyDomain(domain)
	if domain == "" {
		return ReadOnlySSLStatus{}, readOnlyCLIError("invalid_domain", "必须提供网站域名")
	}
	db := database.GetDB()
	if db == nil {
		return ReadOnlySSLStatus{}, readOnlyCLIError("database_unavailable", "面板数据库未打开")
	}
	var status ReadOnlySSLStatus
	status.Domain = domain
	var enabled int
	var expiry sql.NullTime
	var source, lastError string
	err := db.QueryRow(`SELECT ssl_enabled, ssl_expires_at, COALESCE(ssl_cert_source,''), COALESCE(ssl_last_error,'') FROM websites WHERE lower(domain)=?`, domain).
		Scan(&enabled, &expiry, &source, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return ReadOnlySSLStatus{}, readOnlyCLIError("site_not_found", "网站不存在")
	}
	if err != nil {
		return ReadOnlySSLStatus{}, readOnlyCLIError("ssl_unavailable", "读取 SSL 状态失败")
	}
	status.OK = true
	status.PanelVersion = panelVersion
	status.CheckedAt = time.Now().UTC()
	status.Enabled = enabled == 1
	status.Source = source
	status.LastError = summarizeReadOnlyError(lastError)
	if expiry.Valid {
		status.ExpiresAt = &expiry.Time
	}
	return status, nil
}

func ReadOnlyBackupListByDomain(panelVersion, domain string) (ReadOnlyBackupList, error) {
	domain = normalizeReadOnlyDomain(domain)
	if domain == "" {
		return ReadOnlyBackupList{}, readOnlyCLIError("invalid_domain", "必须提供网站域名")
	}
	db := database.GetDB()
	if db == nil {
		return ReadOnlyBackupList{}, readOnlyCLIError("database_unavailable", "面板数据库未打开")
	}
	var siteID int
	if err := db.QueryRow("SELECT id FROM websites WHERE lower(domain)=?", domain).Scan(&siteID); errors.Is(err, sql.ErrNoRows) {
		return ReadOnlyBackupList{}, readOnlyCLIError("site_not_found", "网站不存在")
	} else if err != nil {
		return ReadOnlyBackupList{}, readOnlyCLIError("backup_unavailable", "读取网站信息失败")
	}
	backups := make([]ReadOnlyBackupRecord, 0)
	dbRows, err := db.Query(`SELECT id, filename, file_size, auto, transport_status, created_at FROM db_backups WHERE site_id=? ORDER BY created_at DESC, id DESC`, siteID)
	if err != nil {
		return ReadOnlyBackupList{}, readOnlyCLIError("backup_unavailable", "读取数据库备份失败")
	}
	for dbRows.Next() {
		var item ReadOnlyBackupRecord
		var auto int
		var filename string
		if err := dbRows.Scan(&item.ID, &filename, &item.Size, &auto, &item.TransportStatus, &item.CreatedAt); err != nil {
			dbRows.Close()
			return ReadOnlyBackupList{}, readOnlyCLIError("backup_unavailable", "读取数据库备份失败")
		}
		item.Kind = "database"
		item.Filename = filepath.Base(filename)
		item.Automatic = auto == 1
		backups = append(backups, item)
	}
	if err := dbRows.Err(); err != nil {
		dbRows.Close()
		return ReadOnlyBackupList{}, readOnlyCLIError("backup_unavailable", "读取数据库备份失败")
	}
	dbRows.Close()
	fileRows, err := db.Query(`SELECT id, filename, file_size, mode, transport_status, created_at FROM file_backups WHERE site_id=? ORDER BY created_at DESC, id DESC`, siteID)
	if err != nil {
		return ReadOnlyBackupList{}, readOnlyCLIError("backup_unavailable", "读取文件备份失败")
	}
	for fileRows.Next() {
		var item ReadOnlyBackupRecord
		var filename string
		if err := fileRows.Scan(&item.ID, &filename, &item.Size, &item.Mode, &item.TransportStatus, &item.CreatedAt); err != nil {
			fileRows.Close()
			return ReadOnlyBackupList{}, readOnlyCLIError("backup_unavailable", "读取文件备份失败")
		}
		item.Kind = "files"
		item.Filename = filepath.Base(filename)
		backups = append(backups, item)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return ReadOnlyBackupList{}, readOnlyCLIError("backup_unavailable", "读取文件备份失败")
	}
	fileRows.Close()
	sort.SliceStable(backups, func(i, j int) bool {
		if backups[i].CreatedAt.Equal(backups[j].CreatedAt) {
			return backups[i].ID > backups[j].ID
		}
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})
	return ReadOnlyBackupList{OK: true, PanelVersion: panelVersion, CheckedAt: time.Now().UTC(), Domain: domain, Backups: backups}, nil
}

const readOnlySiteQuery = `SELECT id, name, domain, COALESCE(aliases,''), site_type, status, ssl_enabled, ssl_expires_at, expires_at, created_at, updated_at FROM websites`

type readOnlyScanner func(...any) error

func scanReadOnlySite(scanner readOnlyScanner, site *ReadOnlySiteSummary) error {
	var sslEnabled int
	var sslExpiry, expiry sql.NullTime
	if err := scanner(&site.ID, &site.Name, &site.Domain, &site.Aliases, &site.SiteType, &site.Status, &sslEnabled, &sslExpiry, &expiry, &site.CreatedAt, &site.UpdatedAt); err != nil {
		return err
	}
	site.SSLEnabled = sslEnabled == 1
	if sslExpiry.Valid {
		site.SSLExpires = &sslExpiry.Time
	}
	if expiry.Valid {
		site.ExpiresAt = &expiry.Time
	}
	return nil
}

func scanReadOnlySites(rows *sql.Rows) ([]ReadOnlySiteSummary, error) {
	sites := make([]ReadOnlySiteSummary, 0)
	for rows.Next() {
		var site ReadOnlySiteSummary
		if err := scanReadOnlySite(rows.Scan, &site); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

func normalizeReadOnlyDomain(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

var readOnlySystemctlState = func(service string) ([]byte, error) {
	return exec.Command("systemctl", "is-active", service).Output()
}

func readOnlyServiceState(service string) string {
	output, err := readOnlySystemctlState(service)
	state := strings.TrimSpace(string(output))
	if state == "" {
		if err != nil {
			return "unknown"
		}
		return "unknown"
	}
	return state
}

func summarizeReadOnlyError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Fields(value)
	for i, part := range parts {
		if strings.Contains(part, "/") || strings.Contains(part, "\\") {
			parts[i] = "[path]"
		}
	}
	value = strings.Join(parts, " ")
	if len(value) > 240 {
		return value[:240] + "..."
	}
	return value
}

func ReadOnlyCLIErrorCode(err error) string {
	var cliErr *ReadOnlyCLIError
	if errors.As(err, &cliErr) {
		return cliErr.Code
	}
	return "readonly_command_failed"
}

func ReadOnlyCLIErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
