package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type SiteMigrationRuntimeSettings struct {
	Aliases                 []string                       `json:"aliases"`
	DocumentRootSubdir      string                         `json:"document_root_subdir"`
	SSLEnabled              bool                           `json:"ssl_enabled"`
	TemplateVersion         string                         `json:"template_version"`
	AccessLogMode           string                         `json:"access_log_mode"`
	FastCGICacheEnabled     bool                           `json:"fastcgi_cache_enabled"`
	FastCGICacheTTL         int                            `json:"fastcgi_cache_ttl"`
	MonitoringEnabled       bool                           `json:"monitoring_enabled"`
	MonitoringInterval      int                            `json:"monitoring_interval"`
	DisableWPUpdates        bool                           `json:"disable_wp_updates"`
	DisableFileEditing      bool                           `json:"disable_file_editing"`
	XMLRPCEnabled           bool                           `json:"xmlrpc_enabled"`
	WPDebugEnabled          bool                           `json:"wp_debug_enabled"`
	WPPostRevisions         int                            `json:"wp_post_revisions"`
	WPMemoryLimit           string                         `json:"wp_memory_limit"`
	FileLockEnabled         bool                           `json:"file_lock_enabled"`
	FileLockMode            string                         `json:"file_lock_mode"`
	PasswordResetMode       string                         `json:"password_reset_mode"`
	LogRetentionDays        int                            `json:"log_retention_days"`
	CDNRealIPEnabled        bool                           `json:"cdn_realip_enabled"`
	PHPFPMMaxChildren       int                            `json:"php_fpm_max_children"`
	ExpiresAt               string                         `json:"expires_at"`
	RemoteBackupReconfigure bool                           `json:"remote_backup_reconfigure"`
	CronJobs                []SiteMigrationCronSetting     `json:"cron_jobs,omitempty"`
	SkippedCustomCommands   int                            `json:"skipped_custom_commands"`
	CDNGroups               []SiteMigrationCDNGroupSetting `json:"cdn_groups,omitempty"`
	MarkerToken             string                         `json:"marker_token"`
}

type SiteMigrationCDNGroupSetting struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	HeaderName  string `json:"header_name"`
	IPRanges    string `json:"ip_ranges"`
	Builtin     bool   `json:"builtin"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	TargetID    int64  `json:"-"`
}

type SiteMigrationCronSetting struct {
	Name           string `json:"name"`
	CronExpression string `json:"cron_expression"`
	TaskType       string `json:"task_type"`
	BackupMode     string `json:"backup_mode"`
	KeepCount      int    `json:"keep_count"`
	NotifyFail     bool   `json:"notify_fail"`
	Enabled        bool   `json:"enabled"`
}

type SiteMigrationSettingsService struct {
	db  *sql.DB
	now func() time.Time
}

var siteMigrationCronFieldPattern = regexp.MustCompile(`^[0-9*/,\-]+$`)

func NewSiteMigrationSettingsService(db *sql.DB) (*SiteMigrationSettingsService, error) {
	if db == nil {
		return nil, errors.New("site migration settings service unavailable")
	}
	return &SiteMigrationSettingsService{db: db, now: time.Now}, nil
}

func (s *SiteMigrationSettingsService) SourceSettings(ctx context.Context, migrationSiteID string) (SiteMigrationRuntimeSettings, error) {
	if !validSiteMigrationID(migrationSiteID) {
		return SiteMigrationRuntimeSettings{}, errors.New("invalid migration settings task")
	}
	var settings SiteMigrationRuntimeSettings
	var aliases, expires, snapshotRaw string
	var ssl, fastcgi, monitoring, disableUpdates, disableEditing, xmlrpc, debug, fileLock, cdn int
	err := s.db.QueryRowContext(ctx, `SELECT w.aliases,w.document_root_subdir,w.ssl_enabled,w.template_version,w.access_log_mode,
		w.fastcgi_cache_enabled,w.fastcgi_cache_ttl,w.monitoring_enabled,w.monitoring_interval,w.disable_wp_updates,
		w.disable_file_editing,w.xmlrpc_enabled,w.wp_debug_enabled,w.wp_post_revisions,w.wp_memory_limit,
		w.file_lock_enabled,w.file_lock_mode,w.password_reset_mode,w.log_retention_days,w.cdn_realip_enabled,
		w.php_fpm_max_children,COALESCE(CAST(w.expires_at AS TEXT),''),ms.settings_snapshot
		FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.site_id=ms.source_site_id AND ml.direction='source' AND ml.status='active'
		JOIN websites w ON w.id=ms.source_site_id
		WHERE ms.id=? AND ms.stage IN ('source_frozen','manifest_ready','transferring_files','transferring_database')`, migrationSiteID).Scan(
		&aliases, &settings.DocumentRootSubdir, &ssl, &settings.TemplateVersion, &settings.AccessLogMode,
		&fastcgi, &settings.FastCGICacheTTL, &monitoring, &settings.MonitoringInterval, &disableUpdates,
		&disableEditing, &xmlrpc, &debug, &settings.WPPostRevisions, &settings.WPMemoryLimit,
		&fileLock, &settings.FileLockMode, &settings.PasswordResetMode, &settings.LogRetentionDays, &cdn,
		&settings.PHPFPMMaxChildren, &expires, &snapshotRaw)
	if err != nil {
		return SiteMigrationRuntimeSettings{}, errors.New("source migration settings unavailable")
	}
	settings.Aliases = splitMigrationAliases(aliases)
	settings.SSLEnabled = ssl == 1
	settings.FastCGICacheEnabled = fastcgi == 1
	settings.MonitoringEnabled = monitoring == 1
	settings.DisableWPUpdates = disableUpdates == 1
	settings.DisableFileEditing = disableEditing == 1
	settings.XMLRPCEnabled = xmlrpc == 1
	settings.WPDebugEnabled = debug == 1
	settings.FileLockEnabled = fileLock == 1
	settings.CDNRealIPEnabled = cdn == 1
	settings.ExpiresAt = expires
	var sourceSnapshot struct {
		MarkerToken string `json:"source_marker_token"`
	}
	if json.Unmarshal([]byte(snapshotRaw), &sourceSnapshot) != nil || !siteMigrationMarkerPattern.MatchString(sourceSnapshot.MarkerToken) {
		return SiteMigrationRuntimeSettings{}, errors.New("source migration marker unavailable")
	}
	settings.MarkerToken = sourceSnapshot.MarkerToken
	var remoteBackupEnabled int
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM remote_backup_settings WHERE id=1`).Scan(&remoteBackupEnabled); err == nil && remoteBackupEnabled == 1 {
		settings.RemoteBackupReconfigure = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name,cron_expression,task_type,backup_mode,keep_count,notify_fail,enabled
		FROM cron_jobs WHERE site_id=(SELECT source_site_id FROM site_migration_sites WHERE id=?) ORDER BY id`, migrationSiteID)
	if err != nil {
		return SiteMigrationRuntimeSettings{}, errors.New("source migration cron settings unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var item SiteMigrationCronSetting
		var notify, enabled int
		if err := rows.Scan(&item.Name, &item.CronExpression, &item.TaskType, &item.BackupMode, &item.KeepCount, &notify, &enabled); err != nil {
			return SiteMigrationRuntimeSettings{}, err
		}
		if item.TaskType == "command" {
			settings.SkippedCustomCommands++
			continue
		}
		if item.TaskType == "file_backup" && settings.RemoteBackupReconfigure {
			continue
		}
		if item.TaskType != "file_backup" && item.TaskType != "wp_cron" {
			continue
		}
		item.NotifyFail = notify == 1
		item.Enabled = enabled == 1
		settings.CronJobs = append(settings.CronJobs, item)
	}
	groupRows, err := s.db.QueryContext(ctx, `SELECT g.name,g.provider,g.header_name,g.ip_ranges,g.builtin,g.enabled,g.description
		FROM cdn_realip_groups g JOIN website_cdn_realip_groups wg ON wg.group_id=g.id
		WHERE wg.website_id=(SELECT source_site_id FROM site_migration_sites WHERE id=?) ORDER BY g.builtin DESC,g.name`, migrationSiteID)
	if err != nil {
		return SiteMigrationRuntimeSettings{}, errors.New("source CDN Real IP settings unavailable")
	}
	defer groupRows.Close()
	for groupRows.Next() {
		var group SiteMigrationCDNGroupSetting
		var builtin, enabled int
		if err := groupRows.Scan(&group.Name, &group.Provider, &group.HeaderName, &group.IPRanges, &builtin, &enabled, &group.Description); err != nil {
			return SiteMigrationRuntimeSettings{}, err
		}
		group.Builtin = builtin == 1
		group.Enabled = enabled == 1
		settings.CDNGroups = append(settings.CDNGroups, group)
	}
	if err := validateSiteMigrationRuntimeSettings(&settings); err != nil {
		return SiteMigrationRuntimeSettings{}, err
	}
	return settings, nil
}

func (s *SiteMigrationSettingsService) StoreTargetSettings(ctx context.Context, migrationSiteID string, settings SiteMigrationRuntimeSettings) error {
	if !validSiteMigrationID(migrationSiteID) || validateSiteMigrationRuntimeSettings(&settings) != nil {
		return errors.New("invalid target migration settings")
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT settings_snapshot FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='target' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='target' AND ml.status='active'
		WHERE ms.id=? AND ms.stage='preparing_target'`, migrationSiteID).Scan(&raw); err != nil {
		return errors.New("target migration settings scope unavailable")
	}
	snapshot := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return err
		}
	}
	snapshot["runtime_settings"] = settings
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET settings_snapshot=?,updated_at=? WHERE id=? AND stage='preparing_target'`, string(encoded), s.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("target migration settings state changed")
	}
	return nil
}

func splitMigrationAliases(raw string) []string {
	var aliases []string
	for _, alias := range strings.Split(raw, "\n") {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias != "" {
			aliases = append(aliases, alias)
		}
	}
	return aliases
}

func validateSiteMigrationRuntimeSettings(settings *SiteMigrationRuntimeSettings) error {
	if settings == nil || !siteMigrationMarkerPattern.MatchString(settings.MarkerToken) || settings.FastCGICacheTTL < 10 || settings.FastCGICacheTTL > 86400 || settings.MonitoringInterval <= 0 || settings.LogRetentionDays < 0 || settings.PHPFPMMaxChildren <= 0 {
		return errors.New("invalid migration runtime settings")
	}
	if settings.TemplateVersion == "" {
		settings.TemplateVersion = "v1.0"
	}
	if settings.AccessLogMode == "" {
		settings.AccessLogMode = "error_only"
	}
	if settings.AccessLogMode != "off" && settings.AccessLogMode != "error_only" && settings.AccessLogMode != "full" {
		return errors.New("invalid migration access log mode")
	}
	if settings.PasswordResetMode == "" {
		settings.PasswordResetMode = "allow"
	}
	if _, err := ValidatePasswordResetMode(settings.PasswordResetMode); err != nil {
		return err
	}
	if settings.FileLockEnabled && settings.FileLockMode != FileLockModeStandard && settings.FileLockMode != FileLockModeStrict {
		return errors.New("invalid migration file lock mode")
	}
	seen := map[string]struct{}{}
	for index, alias := range settings.Aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if !IsValidDomain(alias) {
			return errors.New("invalid migration runtime alias")
		}
		if _, exists := seen[alias]; exists {
			return errors.New("duplicate migration runtime alias")
		}
		seen[alias] = struct{}{}
		settings.Aliases[index] = alias
	}
	for _, item := range settings.CronJobs {
		if strings.TrimSpace(item.Name) == "" || strings.ContainsAny(item.Name, "\r\n\x00") || !validMigrationCronExpression(item.CronExpression) || item.TaskType != "wp_cron" && item.TaskType != "file_backup" || item.KeepCount < 0 {
			return errors.New("invalid migration cron setting")
		}
		if item.TaskType == "file_backup" && item.BackupMode != "full" && item.BackupMode != "incremental" {
			return errors.New("invalid migration backup cron setting")
		}
	}
	groupNames := map[string]struct{}{}
	for _, group := range settings.CDNGroups {
		if strings.TrimSpace(group.Name) == "" || strings.ContainsAny(group.Name, "\r\n\x00") || strings.TrimSpace(group.Provider) == "" || strings.TrimSpace(group.IPRanges) == "" {
			return errors.New("invalid migration CDN group")
		}
		if _, err := NormalizeCDNRealIPHeader(group.HeaderName); err != nil {
			return err
		}
		if _, exists := groupNames[group.Name]; exists {
			return errors.New("duplicate migration CDN group")
		}
		groupNames[group.Name] = struct{}{}
	}
	return nil
}

func validMigrationCronExpression(value string) bool {
	fields := strings.Fields(value)
	if len(fields) != 5 {
		return false
	}
	for _, field := range fields {
		if !siteMigrationCronFieldPattern.MatchString(field) {
			return false
		}
	}
	return true
}
