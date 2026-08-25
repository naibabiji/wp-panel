package executor

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

type siteMigrationSiteIdentity struct {
	PanelURL string `json:"panel_url"`
	APIKey   string `json:"api_key"`
}

type siteMigrationPublishedIdentity struct {
	SecretPath string
	CertPath   string
	KeyPath    string
	SSLEnabled bool
}

type siteMigrationTargetConfigureOps interface {
	PublishIdentity(string, string, string, bool) (siteMigrationPublishedIdentity, error)
	ResetIdentity(string, bool) error
	ApplyConfigs(siteMigrationPublishSpec, SiteMigrationRuntimeSettings, siteMigrationPublishedIdentity, int) error
	ResetRuntime(siteMigrationPublishSpec) error
	Health(context.Context, siteMigrationPublishSpec, siteMigrationPublishedIdentity) error
}

type productionSiteMigrationTargetConfigureOps struct{ cfg *config.Config }

func (o productionSiteMigrationTargetConfigureOps) PublishIdentity(siteRoot, domain, systemUser string, sslEnabled bool) (siteMigrationPublishedIdentity, error) {
	result := siteMigrationPublishedIdentity{}
	secretsRoot := "/var/wp-panel/site-secrets"
	secretDir, err := managedSubpath(secretsRoot, filepath.Join(secretsRoot, domain), "站点密钥目录")
	if err != nil {
		return result, err
	}
	if _, err := os.Lstat(secretDir); !os.IsNotExist(err) {
		return result, errors.New("target site secret directory already exists")
	}
	if err := os.MkdirAll(secretsRoot, 0711); err != nil {
		return result, err
	}
	if err := os.Chmod(secretsRoot, 0711); err != nil {
		return result, err
	}
	if err := os.Mkdir(secretDir, 0700); err != nil {
		return result, err
	}
	result.SecretPath = filepath.Join(secretDir, "wp-panel-config.json")
	if err := copyMigrationRegularFile(filepath.Join(siteRoot, "identity", "wp-panel-config.json"), result.SecretPath, 0600); err != nil {
		return result, err
	}
	if _, err := executeCommand("chown", "-R", siteOwner(systemUser), secretDir); err != nil {
		return result, err
	}
	if !sslEnabled {
		return result, nil
	}
	certRoot, err := managedSubpath(o.cfg.Paths.Certificates, filepath.Join(o.cfg.Paths.Certificates, domain), "证书目录")
	if err != nil {
		return result, err
	}
	if _, err := os.Lstat(certRoot); !os.IsNotExist(err) {
		return result, errors.New("target certificate directory already exists")
	}
	if err := os.Mkdir(certRoot, 0700); err != nil {
		return result, err
	}
	result.CertPath = filepath.Join(certRoot, "fullchain.pem")
	result.KeyPath = filepath.Join(certRoot, "privkey.pem")
	if err := copyMigrationRegularFile(filepath.Join(siteRoot, "certificates", "certificate.pem"), result.CertPath, 0644); err != nil {
		return result, err
	}
	if err := copyMigrationRegularFile(filepath.Join(siteRoot, "certificates", "private-key.pem"), result.KeyPath, 0600); err != nil {
		return result, err
	}
	result.SSLEnabled = true
	return result, nil
}

func (o productionSiteMigrationTargetConfigureOps) ResetIdentity(domain string, sslEnabled bool) error {
	secretDir, err := managedSubpath("/var/wp-panel/site-secrets", filepath.Join("/var/wp-panel/site-secrets", domain), "站点密钥目录")
	if err != nil {
		return err
	}
	if err := os.RemoveAll(secretDir); err != nil {
		return err
	}
	if !sslEnabled {
		return nil
	}
	certDir, err := managedSubpath(o.cfg.Paths.Certificates, filepath.Join(o.cfg.Paths.Certificates, domain), "证书目录")
	if err != nil {
		return err
	}
	return os.RemoveAll(certDir)
}

func (o productionSiteMigrationTargetConfigureOps) ApplyConfigs(spec siteMigrationPublishSpec, settings SiteMigrationRuntimeSettings, identity siteMigrationPublishedIdentity, maxChildren int) error {
	engine := NewTemplateEngine(o.cfg.Panel.BackupDir)
	configBase := strings.TrimSuffix(filepath.Base(spec.PHPPoolPath), ".conf")
	phpConfig, err := engine.RenderPHPFPMPool(&PHPFPMPoolData{Domain: spec.Domain, PoolName: configBase, SystemUser: spec.SystemUser, WebRoot: spec.WebRoot, SocketPath: o.cfg.Paths.PHPFPMSock, SocketName: configBase, MaxChildren: strconv.Itoa(maxChildren)})
	if err != nil {
		return err
	}
	if err := engine.ApplyPHPFPMPool(phpConfig, spec.PHPPoolPath, spec.LogDir); err != nil {
		return err
	}
	cdnGroups := make([]models.CDNRealIPGroup, 0, len(settings.CDNGroups))
	for _, group := range settings.CDNGroups {
		cdnGroups = append(cdnGroups, models.CDNRealIPGroup{ID: int(group.TargetID), Name: group.Name, Provider: group.Provider, HeaderName: group.HeaderName, IPRanges: group.IPRanges, Builtin: group.Builtin, Enabled: group.Enabled, Description: group.Description})
	}
	cdnRuntime, err := ResolveCDNRealIPRuntime(&models.Website{CDNRealIPEnabled: settings.CDNRealIPEnabled, CDNRealIPGroups: cdnGroups})
	if err != nil {
		return err
	}
	data := &NginxSiteData{Domain: spec.Domain, Aliases: settings.Aliases, ServerNames: buildServerNames(spec.Domain, settings.Aliases), WebRoot: EffectiveDocumentRoot(spec.WebRoot, spec.SiteType, spec.DocumentRootSubdir), LogDir: spec.LogDir, SystemUser: spec.SystemUser, UseSSL: identity.SSLEnabled, SSLCertPath: identity.CertPath, SSLKeyPath: identity.KeyPath, PHPProxy: "unix:" + spec.PHPSocketPath, TemplateVer: settings.TemplateVersion, AccessLogMode: settings.AccessLogMode, FCacheEnabled: settings.FastCGICacheEnabled, FCacheTTL: settings.FastCGICacheTTL, FCacheKey: NewCacheKey(), SiteType: spec.SiteType, XMLRPCEnabled: settings.XMLRPCEnabled, CDNRealIPEnabled: settings.CDNRealIPEnabled, CDNRealIPHeader: cdnRuntime.HeaderName, CDNRealIPRanges: cdnRuntime.IPRanges, CDNRealIPCompat: cdnRuntime.Compatible}
	nginxConfig, err := engine.RenderNginxConfig(data)
	if err != nil {
		return err
	}
	return engine.ApplyNginxConfig(nginxConfig, spec.NginxConfPath, spec.NginxEnabledPath)
}

func (o productionSiteMigrationTargetConfigureOps) ResetRuntime(spec siteMigrationPublishSpec) error {
	for _, path := range []string{spec.NginxEnabledPath, spec.NginxConfPath, spec.PHPPoolPath} {
		if err := removeMigrationPath(path); err != nil {
			return err
		}
	}
	return nil
}

func (o productionSiteMigrationTargetConfigureOps) Health(ctx context.Context, spec siteMigrationPublishSpec, identity siteMigrationPublishedIdentity) error {
	if spec.SiteType == "wordpress" {
		if err := runMigrationWordPressHealth(ctx, spec); err != nil {
			return err
		}
	}
	return runMigrationLocalHTTPHealth(ctx, spec.Domain, identity.SSLEnabled)
}

func (p *SiteMigrationTargetPublisher) ConfigureAndHealth(ctx context.Context, migrationSiteID string, ops siteMigrationTargetConfigureOps) error {
	if ops == nil {
		ops = productionSiteMigrationTargetConfigureOps{cfg: p.cfg}
	}
	if err := p.resumeFailedStage(ctx, migrationSiteID, "configuring_target"); err != nil {
		return err
	}
	spec, settings, siteIdentity, sslAvailable, err := p.loadConfigureScope(ctx, migrationSiteID)
	if err != nil {
		return err
	}
	maxChildren := RecommendPHPFPMMaxChildren(CollectSystemFacts())
	if maxChildren <= 0 {
		return p.configureFailed(migrationSiteID, "php_capacity_unavailable", errors.New("target PHP-FPM capacity unavailable"))
	}
	siteRoot := filepath.Join(p.stagingRoot, migrationSiteID)
	if err := p.resolveTargetCDNGroups(ctx, migrationSiteID, &settings); err != nil {
		return p.configureFailed(migrationSiteID, "cdn_group_conflict", err)
	}
	publishSSL := sslAvailable && settings.SSLEnabled
	secretStatus, secretExisting, err := p.beginPublishStep(ctx, migrationSiteID, "site_secret_publish", filepath.Join("/var/wp-panel/site-secrets", spec.Domain))
	if err != nil {
		return err
	}
	certificateStatus := "published"
	certificateExisting := false
	if publishSSL {
		certificateStatus, certificateExisting, err = p.beginPublishStep(ctx, migrationSiteID, "certificate_publish", filepath.Join(p.cfg.Paths.Certificates, spec.Domain))
		if err != nil {
			return err
		}
	}
	published := migrationPublishedIdentity(spec.Domain, p.cfg.Paths.Certificates, publishSSL)
	if secretStatus != "published" || certificateStatus != "published" {
		if secretExisting || certificateExisting {
			if err := ops.ResetIdentity(spec.Domain, publishSSL); err != nil {
				return p.configureFailed(migrationSiteID, "identity_reset_failed", err)
			}
		}
		published, err = ops.PublishIdentity(siteRoot, spec.Domain, spec.SystemUser, publishSSL)
		if err != nil {
			return p.configureFailed(migrationSiteID, "identity_publish_failed", err)
		}
		if secretStatus != "published" {
			if err := p.completePublishStep(ctx, migrationSiteID, "site_secret_publish", filepath.Join("/var/wp-panel/site-secrets", spec.Domain)); err != nil {
				return p.publishUnknown(migrationSiteID, err)
			}
		}
		if publishSSL && certificateStatus != "published" {
			if err := p.completePublishStep(ctx, migrationSiteID, "certificate_publish", filepath.Join(p.cfg.Paths.Certificates, spec.Domain)); err != nil {
				return p.publishUnknown(migrationSiteID, err)
			}
		}
	}
	runtimeStatus, runtimeExisting, err := p.beginPublishStep(ctx, migrationSiteID, "runtime_config_publish", spec.NginxConfPath)
	if err != nil {
		return err
	}
	if runtimeStatus != "published" {
		if runtimeExisting {
			if err := ops.ResetRuntime(spec); err != nil {
				return p.configureFailed(migrationSiteID, "runtime_config_reset_failed", err)
			}
		}
		if err := ops.ApplyConfigs(spec, settings, published, maxChildren); err != nil {
			return p.configureFailed(migrationSiteID, "runtime_config_failed", err)
		}
		if err := p.completePublishStep(ctx, migrationSiteID, "runtime_config_publish", spec.NginxConfPath); err != nil {
			return p.publishUnknown(migrationSiteID, err)
		}
	}
	siteID, exists, err := p.loadPublishedWebsite(ctx, migrationSiteID, spec.Domain)
	if err != nil {
		return p.configureFailed(migrationSiteID, "website_record_state_failed", err)
	}
	if !exists {
		siteID, err = p.insertTargetWebsite(ctx, migrationSiteID, spec, settings, siteIdentity, published, maxChildren)
		if err != nil {
			return p.configureFailed(migrationSiteID, "website_record_failed", err)
		}
	}
	if err := ops.Health(ctx, spec, published); err != nil {
		return p.configureFailed(migrationSiteID, "target_health_failed", err)
	}
	result, err := p.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='awaiting_cutover',stage='awaiting_cutover',target_site_id=?,error_code='',updated_at=? WHERE id=? AND status='running' AND stage='configuring_target'`, siteID, p.now().UTC(), migrationSiteID)
	if err != nil {
		return p.publishUnknown(migrationSiteID, err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return p.publishUnknown(migrationSiteID, errors.New("target health commit state changed"))
	}
	return nil
}

func migrationPublishedIdentity(domain, certificateRoot string, sslEnabled bool) siteMigrationPublishedIdentity {
	identity := siteMigrationPublishedIdentity{SecretPath: filepath.Join("/var/wp-panel/site-secrets", domain, "wp-panel-config.json"), SSLEnabled: sslEnabled}
	if sslEnabled {
		identity.CertPath = filepath.Join(certificateRoot, domain, "fullchain.pem")
		identity.KeyPath = filepath.Join(certificateRoot, domain, "privkey.pem")
	}
	return identity
}

func (p *SiteMigrationTargetPublisher) loadPublishedWebsite(ctx context.Context, taskID, domain string) (int64, bool, error) {
	var identifier, owner, status string
	err := p.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag,status FROM site_migration_resources WHERE migration_site_id=? AND resource_type='website_record'`, taskID).Scan(&identifier, &owner, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil || owner != taskID || status != "published" {
		return 0, false, errors.New("target website resource state invalid")
	}
	siteID, err := strconv.ParseInt(identifier, 10, 64)
	if err != nil || siteID <= 0 {
		return 0, false, errors.New("target website resource identity invalid")
	}
	var actualDomain string
	if err := p.db.QueryRowContext(ctx, `SELECT domain FROM websites WHERE id=?`, siteID).Scan(&actualDomain); err != nil || actualDomain != domain {
		return 0, false, errors.New("target website record identity changed")
	}
	return siteID, true, nil
}

func (p *SiteMigrationTargetPublisher) loadConfigureScope(ctx context.Context, migrationSiteID string) (siteMigrationPublishSpec, SiteMigrationRuntimeSettings, siteMigrationSiteIdentity, bool, error) {
	var raw string
	err := p.db.QueryRowContext(ctx, `SELECT ms.settings_snapshot FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='target' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='target' AND ml.status='active'
		WHERE ms.id=? AND ms.status='running' AND ms.stage='configuring_target'`, migrationSiteID).Scan(&raw)
	if err != nil {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target configure scope unavailable")
	}
	var snapshot struct {
		TargetSpec      siteMigrationPublishSpec     `json:"target_spec"`
		RuntimeSettings SiteMigrationRuntimeSettings `json:"runtime_settings"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || validateSiteMigrationRuntimeSettings(&snapshot.RuntimeSettings) != nil {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target runtime settings unavailable")
	}
	spec := snapshot.TargetSpec
	normalized, normalizeErr := normalizeSiteMigrationTargetSpec(SiteMigrationTargetSpec{Domain: spec.Domain, Aliases: append([]string(nil), spec.Aliases...), SiteType: spec.SiteType, DocumentRootSubdir: spec.DocumentRootSubdir})
	if normalizeErr != nil || normalized.Domain != spec.Domain || normalized.DocumentRootSubdir != spec.DocumentRootSubdir || strings.Join(normalized.Aliases, "\n") != strings.Join(spec.Aliases, "\n") {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target configure specification invalid")
	}
	plan, err := p.expectedPlan(spec.Domain, spec.SiteType)
	if err != nil || spec.WebRoot != plan.WebRoot || spec.SystemUser != plan.SystemUser || spec.DBName != plan.DBName || spec.DBUser != plan.DBUser || spec.PHPPoolPath != plan.PHPPoolPath || spec.NginxConfPath != plan.NginxConfPath || spec.NginxEnabledPath != plan.NginxEnabledPath || spec.PHPSocketPath != plan.PHPSockPath {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target configure plan mismatch")
	}
	if strings.Join(snapshot.RuntimeSettings.Aliases, "\n") != strings.Join(spec.Aliases, "\n") || snapshot.RuntimeSettings.DocumentRootSubdir != spec.DocumentRootSubdir {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target settings identity mismatch")
	}
	siteIdentityPath := filepath.Join(p.stagingRoot, migrationSiteID, "identity", "wp-panel-config.json")
	var stagingIdentifier, stagingOwner, identityIdentifier, identityOwner string
	if err := p.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag FROM site_migration_resources WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, migrationSiteID).Scan(&stagingIdentifier, &stagingOwner); err != nil || filepath.Clean(stagingIdentifier) != filepath.Join(p.stagingRoot, migrationSiteID) || stagingOwner != migrationSiteID {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target configure staging ownership unavailable")
	}
	if err := p.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag FROM site_migration_resources WHERE migration_site_id=? AND resource_type='site_identity' AND status IN ('created','published')`, migrationSiteID).Scan(&identityIdentifier, &identityOwner); err != nil || filepath.Clean(identityIdentifier) != siteIdentityPath || identityOwner != migrationSiteID {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target site identity ownership unavailable")
	}
	content, err := os.ReadFile(siteIdentityPath)
	var siteIdentity siteMigrationSiteIdentity
	if err != nil || json.Unmarshal(content, &siteIdentity) != nil || len(siteIdentity.APIKey) < 32 || !strings.HasPrefix(siteIdentity.PanelURL, "https://127.0.0.1:") {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target site identity unavailable")
	}
	var certCount int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_artifacts WHERE migration_site_id=? AND artifact_type IN ('certificate','private_key') AND status='verified'`, migrationSiteID).Scan(&certCount); err != nil || certCount != 0 && certCount != 2 {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target certificate state unavailable")
	}
	if snapshot.RuntimeSettings.SSLEnabled && certCount != 2 {
		return siteMigrationPublishSpec{}, SiteMigrationRuntimeSettings{}, siteMigrationSiteIdentity{}, false, errors.New("target SSL certificate pair unavailable")
	}
	return spec, snapshot.RuntimeSettings, siteIdentity, certCount == 2, nil
}

func (p *SiteMigrationTargetPublisher) insertTargetWebsite(ctx context.Context, migrationSiteID string, spec siteMigrationPublishSpec, settings SiteMigrationRuntimeSettings, identity siteMigrationSiteIdentity, published siteMigrationPublishedIdentity, maxChildren int) (int64, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO websites (name,domain,aliases,status,system_user,web_root,document_root_subdir,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,site_type,ssl_enabled,ssl_cert_path,ssl_key_path,template_version,access_log_mode,fastcgi_cache_enabled,fastcgi_cache_ttl,fastcgi_cache_key,plugin_api_key,monitoring_enabled,monitoring_interval,disable_wp_updates,disable_file_editing,xmlrpc_enabled,wp_debug_enabled,wp_post_revisions,wp_memory_limit,file_lock_enabled,file_lock_mode,password_reset_mode,log_retention_days,cdn_realip_enabled,php_fpm_max_children,expires_at)
		VALUES (?,?,?,'active',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		buildSiteName(spec.Domain), spec.Domain, strings.Join(settings.Aliases, "\n"), spec.SystemUser, spec.WebRoot, spec.DocumentRootSubdir, spec.LogDir, spec.DBName, spec.DBUser, spec.PHPPoolPath, spec.NginxConfPath, spec.SiteType, boolInt(published.SSLEnabled), published.CertPath, published.KeyPath, settings.TemplateVersion, settings.AccessLogMode, boolInt(settings.FastCGICacheEnabled), settings.FastCGICacheTTL, NewCacheKey(), identity.APIKey, 0, settings.MonitoringInterval, boolInt(settings.DisableWPUpdates), boolInt(settings.DisableFileEditing), boolInt(settings.XMLRPCEnabled), boolInt(settings.WPDebugEnabled), settings.WPPostRevisions, settings.WPMemoryLimit, boolInt(settings.FileLockEnabled), settings.FileLockMode, settings.PasswordResetMode, settings.LogRetentionDays, boolInt(settings.CDNRealIPEnabled), maxChildren, nilIfEmpty(settings.ExpiresAt))
	if err != nil {
		return 0, err
	}
	siteID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_resources (migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at) VALUES (?,'website_record',?,?, 'published',?,?)`, migrationSiteID, strconv.FormatInt(siteID, 10), migrationSiteID, p.now().UTC(), p.now().UTC()); err != nil {
		return 0, err
	}
	lockResult, err := tx.ExecContext(ctx, `UPDATE site_migration_locks SET site_id=?,updated_at=? WHERE migration_site_id=? AND direction='target' AND status='active' AND site_id IS NULL`, siteID, p.now().UTC(), migrationSiteID)
	if err != nil {
		return 0, err
	}
	changed, _ := lockResult.RowsAffected()
	if changed != 1 {
		return 0, errors.New("target migration lock identity changed")
	}
	for _, cron := range settings.CronJobs {
		command := ""
		if cron.TaskType == "wp_cron" {
			command = spec.Domain
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO cron_jobs (name,cron_expression,command,site_id,run_as_user,task_type,backup_mode,keep_count,notify_fail,enabled) VALUES (?,?,?,?,?,?,?,?,?,0)`, cron.Name, cron.CronExpression, command, siteID, spec.SystemUser, cron.TaskType, cron.BackupMode, cron.KeepCount, boolInt(cron.NotifyFail))
		if err != nil {
			return 0, err
		}
		cronID, err := result.LastInsertId()
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_resources (migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at) VALUES (?,'cron_job',?,?,'published',?,?)`, migrationSiteID, strconv.FormatInt(cronID, 10), migrationSiteID, p.now().UTC(), p.now().UTC()); err != nil {
			return 0, err
		}
	}
	for _, group := range settings.CDNGroups {
		if group.TargetID <= 0 {
			return 0, errors.New("target CDN group identity unavailable")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO website_cdn_realip_groups (website_id,group_id) VALUES (?,?)`, siteID, group.TargetID); err != nil {
			return 0, err
		}
	}
	return siteID, tx.Commit()
}

func (p *SiteMigrationTargetPublisher) resolveTargetCDNGroups(ctx context.Context, migrationSiteID string, settings *SiteMigrationRuntimeSettings) error {
	for index := range settings.CDNGroups {
		group := &settings.CDNGroups[index]
		var id int64
		var provider, header, ranges, description string
		var builtin, enabled int
		err := p.db.QueryRowContext(ctx, `SELECT id,provider,header_name,ip_ranges,builtin,enabled,description FROM cdn_realip_groups WHERE name=?`, group.Name).Scan(&id, &provider, &header, &ranges, &builtin, &enabled, &description)
		if errors.Is(err, sql.ErrNoRows) {
			if group.Builtin {
				return errors.New("target builtin CDN group unavailable")
			}
			id, err = p.createOwnedTargetCDNGroup(ctx, migrationSiteID, *group)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if group.Builtin {
			if builtin != 1 {
				return errors.New("target builtin CDN group unavailable")
			}
			group.Provider = provider
			group.HeaderName = header
			group.IPRanges = ranges
			group.Enabled = enabled == 1
			group.Description = description
		} else if builtin == 1 || provider != group.Provider || header != group.HeaderName || ranges != group.IPRanges || (enabled == 1) != group.Enabled || description != group.Description {
			return errors.New("target custom CDN group definition conflicts")
		}
		group.TargetID = id
	}
	return nil
}

func (p *SiteMigrationTargetPublisher) createOwnedTargetCDNGroup(ctx context.Context, migrationSiteID string, group SiteMigrationCDNGroupSetting) (int64, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_resources (migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at) VALUES (?,'cdn_custom_group',?,?,'created',?,?)`, migrationSiteID, group.Name, migrationSiteID, p.now().UTC(), p.now().UTC()); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO cdn_realip_groups (name,provider,header_name,ip_ranges,builtin,enabled,description) VALUES (?,?,?,?,0,?,?)`, group.Name, group.Provider, group.HeaderName, group.IPRanges, boolInt(group.Enabled), group.Description)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (p *SiteMigrationTargetPublisher) configureFailed(taskID, code string, cause error) error {
	_, _ = p.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',error_code=?,updated_at=? WHERE id=? AND stage='configuring_target'`, code, p.now().UTC(), taskID)
	return fmt.Errorf("target configuration failed: %w", cause)
}

func copyMigrationRegularFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("migration identity source is not a regular file")
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func runMigrationWordPressHealth(ctx context.Context, spec siteMigrationPublishSpec) error {
	allowedHosts, _ := json.Marshal(append([]string{spec.Domain}, spec.Aliases...))
	code := `$wp_panel_result=['ok'=>false];try{define('WP_USE_THEMES',false);define('DISABLE_WP_CRON',true);require ` + strconv.Quote(filepath.Join(spec.WebRoot, "wp-load.php")) + `;global $wpdb;$tables=[$wpdb->options,$wpdb->posts,$wpdb->users];foreach($tables as $table){$wpdb->get_var("SELECT 1 FROM {$table} LIMIT 1");if($wpdb->last_error){throw new Exception('core_table_unavailable');}}$allowed=json_decode(` + strconv.Quote(string(allowedHosts)) + `,true);$home=get_option('home');$siteurl=get_option('siteurl');foreach([$home,$siteurl] as $url){$host=strtolower((string)parse_url($url,PHP_URL_HOST));if(!$host||!in_array($host,$allowed,true)){throw new Exception('site_url_mismatch');}}$wp_panel_result=['ok'=>true];}catch(Throwable $e){$wp_panel_result=['ok'=>false,'error'=>'wordpress_bootstrap_failed'];}echo json_encode($wp_panel_result);exit($wp_panel_result['ok']?0:1);`
	cmd := exec.CommandContext(ctx, "runuser", "-u", spec.SystemUser, "--", "php", "-r", code)
	output, err := cmd.Output()
	if err != nil || !strings.Contains(string(output), `"ok":true`) {
		return errors.New("WordPress migration health failed")
	}
	return nil
}

func runMigrationLocalHTTPHealth(ctx context.Context, domain string, sslEnabled bool) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		port := "80"
		if strings.HasSuffix(address, ":443") {
			port = "443"
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, "127.0.0.1:"+port)
	}, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !strings.EqualFold(req.URL.Hostname(), domain) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	schemes := []string{"http"}
	if sslEnabled {
		schemes = append(schemes, "https")
	}
	for _, scheme := range schemes {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+domain+"/?wp_hc="+NewCacheKey(), nil)
		req.Host = domain
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("local migration HTTP health failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		closeErr := resp.Body.Close()
		if readErr != nil || closeErr != nil {
			return errors.New("local migration HTTP health response unavailable")
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return errors.New("local migration HTTP health rejected redirect target")
		}
		text := strings.ToLower(string(body))
		if resp.StatusCode >= 500 || strings.Contains(text, "fatal error") || strings.Contains(text, "uncaught error") {
			return errors.New("local migration HTTP health detected fatal response")
		}
	}
	return nil
}
