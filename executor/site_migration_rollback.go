package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type siteMigrationTargetRollbackOps interface {
	StopRuntime(siteMigrationPublishSpec) error
	RemovePath(string) error
	DropDatabase(string, string) error
	RemoveUser(string) error
	ReloadCron() error
}

type productionSiteMigrationTargetRollbackOps struct{ cfg *config.Config }

func (o productionSiteMigrationTargetRollbackOps) StopRuntime(spec siteMigrationPublishSpec) error {
	if err := removeMigrationPath(spec.NginxEnabledPath); err != nil {
		return err
	}
	if output, err := executeCommand("nginx", "-s", "reload"); err != nil {
		return fmt.Errorf("reload Nginx after migration rollback: %s", strings.TrimSpace(output))
	}
	for _, path := range []string{spec.NginxConfPath, spec.PHPPoolPath} {
		if err := removeMigrationPath(path); err != nil {
			return err
		}
	}
	if output, err := executeCommand("systemctl", "reload", "php8.3-fpm"); err != nil {
		return fmt.Errorf("reload PHP-FPM after migration rollback: %s", strings.TrimSpace(output))
	}
	return nil
}

func (productionSiteMigrationTargetRollbackOps) RemovePath(path string) error {
	return os.RemoveAll(path)
}

func (o productionSiteMigrationTargetRollbackOps) DropDatabase(name, dbUser string) error {
	return dropMariaDBDatabase(name, dbUser, o.cfg)
}

func (productionSiteMigrationTargetRollbackOps) RemoveUser(name string) error {
	return removeMigrationSystemUser(name)
}

func (productionSiteMigrationTargetRollbackOps) ReloadCron() error {
	result := renderCronConfig()
	if !result.Success {
		return errors.New(result.Message)
	}
	return nil
}

func removeMigrationPath(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

type SiteMigrationTargetRollbackService struct {
	db          *sql.DB
	cfg         *config.Config
	stagingRoot string
	ops         siteMigrationTargetRollbackOps
	now         func() time.Time
}

func NewSiteMigrationTargetRollbackService(db *sql.DB, cfg *config.Config, stagingRoot string) (*SiteMigrationTargetRollbackService, error) {
	if db == nil || cfg == nil || !filepath.IsAbs(stagingRoot) {
		return nil, errors.New("site migration target rollback service unavailable")
	}
	return &SiteMigrationTargetRollbackService{db: db, cfg: cfg, stagingRoot: filepath.Clean(stagingRoot), ops: productionSiteMigrationTargetRollbackOps{cfg: cfg}, now: time.Now}, nil
}

type siteMigrationRollbackScope struct {
	Spec      siteMigrationPublishSpec
	Settings  SiteMigrationRuntimeSettings
	SiteID    int64
	Activated bool
	Resources map[string][]string
}

func (s *SiteMigrationTargetRollbackService) Abandon(ctx context.Context, taskID, typedDomain, operator, reason string, allowActivated bool) error {
	complete, err := s.rollbackAlreadyComplete(ctx, taskID, typedDomain, operator)
	if err != nil || complete {
		return err
	}
	scope, err := s.loadScope(ctx, taskID)
	if err != nil {
		return err
	}
	if typedDomain != scope.Spec.Domain || strings.TrimSpace(operator) == "" {
		return errors.New("target rollback confirmation mismatch")
	}
	if scope.Activated && (!allowActivated || strings.TrimSpace(reason) == "") {
		return errors.New("activated target rollback requires explicit risk acceptance")
	}
	if err := s.validateWebsiteOwnership(ctx, taskID, scope); err != nil {
		return err
	}
	if err := s.beginRollback(ctx, taskID, scope, operator, reason); err != nil {
		return err
	}
	if err := s.ops.ReloadCron(); err != nil {
		return s.rollbackFailed(taskID, "target_rollback_freeze_failed", err)
	}
	if hasMigrationResource(scope.Resources, "runtime_config_publish") || scope.SiteID > 0 {
		if err := s.ops.StopRuntime(scope.Spec); err != nil {
			return s.rollbackFailed(taskID, "target_rollback_runtime_failed", err)
		}
		if err := s.markRemoved(ctx, taskID, "runtime_config_publish", "target_marker_config"); err != nil {
			return s.rollbackFailed(taskID, "target_rollback_state_failed", err)
		}
	}
	if err := s.removeWebsiteRecords(ctx, taskID, scope); err != nil {
		return s.rollbackFailed(taskID, "target_rollback_website_failed", err)
	}
	for _, item := range []struct {
		kind  string
		path  string
		marks []string
	}{
		{"certificate_publish", filepath.Join(s.cfg.Paths.Certificates, scope.Spec.Domain), []string{"certificate_publish"}},
		{"site_secret_publish", filepath.Join("/var/wp-panel/site-secrets", scope.Spec.Domain), []string{"site_secret_publish"}},
		{"web_root", scope.Spec.WebRoot, []string{"web_root", "file_publish"}},
		{"log_dir", scope.Spec.LogDir, []string{"log_dir"}},
		{"target_staging_root", filepath.Join(s.stagingRoot, taskID), []string{"target_staging_root", "database_identity", "site_identity"}},
	} {
		if !hasAnyMigrationResource(scope.Resources, item.marks...) {
			continue
		}
		if err := s.ops.RemovePath(item.path); err != nil {
			return s.resourceRemoveFailed(taskID, item.kind, err)
		}
		if err := s.markRemoved(ctx, taskID, item.marks...); err != nil {
			return s.rollbackFailed(taskID, "target_rollback_state_failed", err)
		}
	}
	if hasAnyMigrationResource(scope.Resources, "database", "database_user", "database_import") {
		if err := s.ops.DropDatabase(scope.Spec.DBName, scope.Spec.DBUser); err != nil {
			return s.resourceRemoveFailed(taskID, "database", err)
		}
		if err := s.markRemoved(ctx, taskID, "database", "database_user", "database_import"); err != nil {
			return s.rollbackFailed(taskID, "target_rollback_state_failed", err)
		}
	}
	if hasMigrationResource(scope.Resources, "system_user") {
		if err := s.ops.RemoveUser(scope.Spec.SystemUser); err != nil {
			return s.resourceRemoveFailed(taskID, "system_user", err)
		}
		if err := s.markRemoved(ctx, taskID, "system_user"); err != nil {
			return s.rollbackFailed(taskID, "target_rollback_state_failed", err)
		}
	}
	return s.finishRollback(ctx, taskID)
}

func (s *SiteMigrationTargetRollbackService) validateWebsiteOwnership(ctx context.Context, taskID string, scope siteMigrationRollbackScope) error {
	if (scope.SiteID > 0) != hasMigrationResource(scope.Resources, "website_record") {
		return errors.New("target rollback website resource mismatch")
	}
	if scope.SiteID <= 0 {
		return nil
	}
	var domain string
	if err := s.db.QueryRowContext(ctx, `SELECT domain FROM websites WHERE id=?`, scope.SiteID).Scan(&domain); err != nil || domain != scope.Spec.Domain {
		return errors.New("target rollback website identity changed")
	}
	var totalCron, ownedCron int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_jobs WHERE site_id=?`, scope.SiteID).Scan(&totalCron); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_jobs cj JOIN site_migration_resources r ON r.migration_site_id=? AND r.resource_type='cron_job' AND r.identifier=CAST(cj.id AS TEXT) AND r.ownership_tag=? AND r.status IN ('created','published','remove_failed') WHERE cj.site_id=?`, taskID, taskID, scope.SiteID).Scan(&ownedCron); err != nil {
		return err
	}
	if totalCron != len(scope.Resources["cron_job"]) || ownedCron != totalCron {
		return errors.New("target rollback website Cron ownership changed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT g.name FROM website_cdn_realip_groups wg JOIN cdn_realip_groups g ON g.id=wg.group_id WHERE wg.website_id=?`, scope.SiteID)
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		actual[name] = struct{}{}
	}
	if len(actual) != len(scope.Settings.CDNGroups) {
		return errors.New("target rollback CDN bindings changed")
	}
	for _, group := range scope.Settings.CDNGroups {
		if _, ok := actual[group.Name]; !ok {
			return errors.New("target rollback CDN binding identity changed")
		}
	}
	return rows.Err()
}

func (s *SiteMigrationTargetRollbackService) rollbackAlreadyComplete(ctx context.Context, taskID, typedDomain, operator string) (bool, error) {
	if !validSiteMigrationID(taskID) {
		return false, errors.New("invalid target rollback task")
	}
	var domain, status, cleanup, lockStatus string
	var remaining int
	err := s.db.QueryRowContext(ctx, `SELECT ms.target_domain,ms.status,ms.cleanup_status,ml.status,
		(SELECT COUNT(*) FROM site_migration_resources r WHERE r.migration_site_id=ms.id AND r.status!='removed')
		FROM site_migration_sites ms JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='target' WHERE ms.id=?`, taskID).Scan(&domain, &status, &cleanup, &lockStatus, &remaining)
	if err != nil {
		return false, nil
	}
	if status != "abandoned" {
		return false, nil
	}
	if typedDomain != domain || strings.TrimSpace(operator) == "" {
		return false, errors.New("target rollback confirmation mismatch")
	}
	if cleanup != "complete" || lockStatus != "released" || remaining != 0 {
		return false, errors.New("target rollback completion evidence invalid")
	}
	return true, nil
}

func (s *SiteMigrationTargetRollbackService) loadScope(ctx context.Context, taskID string) (siteMigrationRollbackScope, error) {
	if !validSiteMigrationID(taskID) {
		return siteMigrationRollbackScope{}, errors.New("invalid target rollback task")
	}
	var raw, stage, status, lockStatus string
	var siteID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT ms.settings_snapshot,ms.stage,ms.status,ms.target_site_id,ml.status
		FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='target' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='target'
		WHERE ms.id=?`, taskID).Scan(&raw, &stage, &status, &siteID, &lockStatus)
	if err != nil {
		return siteMigrationRollbackScope{}, errors.New("target rollback scope unavailable")
	}
	if stage == "abandoned" {
		return siteMigrationRollbackScope{}, errors.New("target rollback already complete")
	}
	if status == "cancelling" || status == "cleanup_failed" {
		// Retrying an interrupted cleanup is allowed; the resource ledger remains authoritative.
	} else if stage != "publishing" && stage != "configuring_target" && stage != "awaiting_cutover" && stage != "activating_target" && stage != "activation_runtime_sync" && stage != "completed" && stage != "interrupted_unknown" {
		return siteMigrationRollbackScope{}, errors.New("target rollback stage unavailable")
	}
	var snapshot struct {
		TargetSpec      siteMigrationPublishSpec     `json:"target_spec"`
		RuntimeSettings SiteMigrationRuntimeSettings `json:"runtime_settings"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil || !IsValidDomain(snapshot.TargetSpec.Domain) {
		return siteMigrationRollbackScope{}, errors.New("target rollback snapshot unavailable")
	}
	plan, err := (&SiteMigrationTargetPublisher{cfg: s.cfg}).expectedPlan(snapshot.TargetSpec.Domain, snapshot.TargetSpec.SiteType)
	if err != nil || snapshot.TargetSpec.SystemUser != plan.SystemUser || snapshot.TargetSpec.WebRoot != plan.WebRoot || snapshot.TargetSpec.LogDir != plan.LogDir || snapshot.TargetSpec.DBName != plan.DBName || snapshot.TargetSpec.DBUser != plan.DBUser || snapshot.TargetSpec.PHPPoolPath != plan.PHPPoolPath || snapshot.TargetSpec.NginxConfPath != plan.NginxConfPath || snapshot.TargetSpec.NginxEnabledPath != plan.NginxEnabledPath {
		return siteMigrationRollbackScope{}, errors.New("target rollback plan mismatch")
	}
	resources, err := s.loadResources(ctx, taskID, snapshot.TargetSpec)
	if err != nil {
		return siteMigrationRollbackScope{}, err
	}
	return siteMigrationRollbackScope{Spec: snapshot.TargetSpec, Settings: snapshot.RuntimeSettings, SiteID: siteID.Int64, Activated: stage == "completed" || stage == "activation_runtime_sync" || lockStatus == "released", Resources: resources}, nil
}

func (s *SiteMigrationTargetRollbackService) loadResources(ctx context.Context, taskID string, spec siteMigrationPublishSpec) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT resource_type,identifier,ownership_tag,status FROM site_migration_resources WHERE migration_site_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resources := map[string][]string{}
	allowed := map[string]bool{"target_staging_root": true, "system_user": true, "web_root": true, "log_dir": true, "database": true, "database_user": true, "database_identity": true, "site_identity": true, "database_import": true, "file_publish": true, "site_secret_publish": true, "certificate_publish": true, "runtime_config_publish": true, "website_record": true, "cron_job": true, "cdn_custom_group": true, "target_marker_config": true}
	for rows.Next() {
		var kind, identifier, owner, status string
		if err := rows.Scan(&kind, &identifier, &owner, &status); err != nil {
			return nil, err
		}
		if !allowed[kind] || owner != taskID {
			return nil, errors.New("target rollback resource ownership mismatch")
		}
		if status == "removed" {
			continue
		}
		if status != "created" && status != "published" && status != "remove_failed" {
			return nil, errors.New("target rollback resource state invalid")
		}
		resources[kind] = append(resources[kind], identifier)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	want := map[string]string{
		"target_staging_root": filepath.Join(s.stagingRoot, taskID), "system_user": spec.SystemUser, "web_root": spec.WebRoot, "log_dir": spec.LogDir,
		"database": spec.DBName, "database_user": spec.DBUser, "database_identity": filepath.Join(s.stagingRoot, taskID, "identity", "database.json"),
		"site_identity": filepath.Join(s.stagingRoot, taskID, "identity", "wp-panel-config.json"), "database_import": spec.DBName, "file_publish": spec.WebRoot,
		"site_secret_publish": filepath.Join("/var/wp-panel/site-secrets", spec.Domain), "certificate_publish": filepath.Join(s.cfg.Paths.Certificates, spec.Domain),
		"runtime_config_publish": spec.NginxConfPath, "target_marker_config": spec.NginxConfPath,
	}
	for kind, identifiers := range resources {
		if expected, fixed := want[kind]; fixed {
			if len(identifiers) != 1 || filepath.Clean(identifiers[0]) != filepath.Clean(expected) {
				return nil, errors.New("target rollback resource identity mismatch")
			}
		}
	}
	return resources, nil
}

func (s *SiteMigrationTargetRollbackService) beginRollback(ctx context.Context, taskID string, scope siteMigrationRollbackScope, operator, reason string) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if scope.SiteID > 0 {
		var domain string
		if err := tx.QueryRowContext(ctx, `SELECT domain FROM websites WHERE id=?`, scope.SiteID).Scan(&domain); err != nil || domain != scope.Spec.Domain {
			return errors.New("target rollback website identity changed")
		}
		rows, err := tx.QueryContext(ctx, `SELECT g.name FROM website_cdn_realip_groups wg JOIN cdn_realip_groups g ON g.id=wg.group_id WHERE wg.website_id=?`, scope.SiteID)
		if err != nil {
			return err
		}
		actualGroups := map[string]struct{}{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			actualGroups[name] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(actualGroups) != len(scope.Settings.CDNGroups) {
			return errors.New("target rollback CDN bindings changed")
		}
		for _, group := range scope.Settings.CDNGroups {
			if _, ok := actualGroups[group.Name]; !ok {
				return errors.New("target rollback CDN binding identity changed")
			}
		}
		for _, name := range scope.Resources["cdn_custom_group"] {
			var matching int
			for _, group := range scope.Settings.CDNGroups {
				if group.Name != name || group.Builtin {
					continue
				}
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cdn_realip_groups WHERE name=? AND builtin=0 AND provider=? AND header_name=? AND ip_ranges=? AND enabled=? AND COALESCE(description,'')=?`, name, group.Provider, group.HeaderName, group.IPRanges, boolInt(group.Enabled), group.Description).Scan(&matching); err != nil {
					return err
				}
			}
			if matching != 1 {
				return errors.New("target rollback custom CDN definition changed")
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE websites SET monitoring_enabled=0,updated_at=? WHERE id=? AND domain=?`, s.now().UTC(), scope.SiteID, scope.Spec.Domain)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return errors.New("target rollback website state changed")
		}
		var totalCron, ownedCron int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_jobs WHERE site_id=?`, scope.SiteID).Scan(&totalCron); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_jobs cj JOIN site_migration_resources r ON r.migration_site_id=? AND r.resource_type='cron_job' AND r.identifier=CAST(cj.id AS TEXT) AND r.ownership_tag=? AND r.status IN ('created','published','remove_failed') WHERE cj.site_id=?`, taskID, taskID, scope.SiteID).Scan(&ownedCron); err != nil {
			return err
		}
		if totalCron != ownedCron || ownedCron != len(scope.Resources["cron_job"]) {
			return errors.New("target rollback website Cron ownership changed")
		}
		result, err = tx.ExecContext(ctx, `UPDATE cron_jobs SET enabled=0,updated_at=? WHERE site_id=? AND EXISTS (SELECT 1 FROM site_migration_resources r WHERE r.migration_site_id=? AND r.resource_type='cron_job' AND r.identifier=CAST(cron_jobs.id AS TEXT) AND r.ownership_tag=? AND r.status IN ('created','published','remove_failed'))`, s.now().UTC(), scope.SiteID, taskID, taskID)
		if err != nil {
			return err
		}
		changed, _ = result.RowsAffected()
		if changed != int64(ownedCron) {
			return errors.New("target rollback Cron state changed")
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_locks SET status='active',site_id=CASE WHEN ? > 0 THEN ? ELSE site_id END,released_at=NULL,updated_at=? WHERE migration_site_id=? AND direction='target'`, scope.SiteID, scope.SiteID, now, taskID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("target rollback lock state changed")
	}
	result, err = tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='cancelling',stage='cancelling',cleanup_status='running',error_code='',updated_at=?
		WHERE id=? AND (lease_owner='' OR lease_expires_at IS NULL OR lease_expires_at<=?)`, now, taskID, now)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("target migration task is still running")
	}
	payload, _ := json.Marshal(map[string]string{"operator": strings.TrimSpace(operator), "reason": strings.TrimSpace(reason)})
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_events (migration_site_id,stage,result,message,created_at) VALUES (?,'target_rollback','warning',?,?)`, taskID, string(payload), s.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SiteMigrationTargetRollbackService) removeWebsiteRecords(ctx context.Context, taskID string, scope siteMigrationRollbackScope) error {
	if (scope.SiteID > 0) != hasMigrationResource(scope.Resources, "website_record") {
		return errors.New("target rollback website resource mismatch")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if scope.SiteID > 0 {
		var siteDomain string
		if err := tx.QueryRowContext(ctx, `SELECT domain FROM websites WHERE id=?`, scope.SiteID).Scan(&siteDomain); err != nil || siteDomain != scope.Spec.Domain {
			return errors.New("target rollback website identity changed")
		}
		var totalCron int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_jobs WHERE site_id=?`, scope.SiteID).Scan(&totalCron); err != nil || totalCron != len(scope.Resources["cron_job"]) {
			return errors.New("target rollback website Cron ownership changed")
		}
		for _, identifier := range scope.Resources["cron_job"] {
			id, err := strconv.ParseInt(identifier, 10, 64)
			if err != nil {
				return errors.New("target rollback Cron identity invalid")
			}
			result, err := tx.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id=? AND site_id=?`, id, scope.SiteID)
			if err != nil {
				return err
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return errors.New("target rollback Cron identity changed")
			}
		}
		var bindingCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM website_cdn_realip_groups WHERE website_id=?`, scope.SiteID).Scan(&bindingCount); err != nil || bindingCount != len(scope.Settings.CDNGroups) {
			return errors.New("target rollback CDN bindings changed")
		}
		rows, err := tx.QueryContext(ctx, `SELECT g.name FROM website_cdn_realip_groups wg JOIN cdn_realip_groups g ON g.id=wg.group_id WHERE wg.website_id=? ORDER BY g.name`, scope.SiteID)
		if err != nil {
			return err
		}
		actualGroups := make(map[string]struct{}, bindingCount)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			actualGroups[name] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, group := range scope.Settings.CDNGroups {
			if _, ok := actualGroups[group.Name]; !ok {
				return errors.New("target rollback CDN binding identity changed")
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM website_cdn_realip_groups WHERE website_id=?`, scope.SiteID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET target_site_id=NULL WHERE id=? AND target_site_id=?`, taskID, scope.SiteID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE site_migration_locks SET site_id=NULL WHERE migration_site_id=? AND direction='target' AND site_id=?`, taskID, scope.SiteID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM websites WHERE id=? AND domain=?`, scope.SiteID, scope.Spec.Domain)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return errors.New("target rollback website state changed")
		}
		for _, kind := range []string{"website_record", "cron_job"} {
			if _, err := tx.ExecContext(ctx, `UPDATE site_migration_resources SET status='removed',updated_at=? WHERE migration_site_id=? AND resource_type=? AND status IN ('created','published','remove_failed')`, s.now().UTC(), taskID, kind); err != nil {
				return err
			}
		}
	}
	for _, name := range scope.Resources["cdn_custom_group"] {
		var expected *SiteMigrationCDNGroupSetting
		for index := range scope.Settings.CDNGroups {
			if scope.Settings.CDNGroups[index].Name == name && !scope.Settings.CDNGroups[index].Builtin {
				expected = &scope.Settings.CDNGroups[index]
				break
			}
		}
		if expected == nil {
			return errors.New("target rollback custom CDN definition unavailable")
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM cdn_realip_groups WHERE name=? AND builtin=0 AND provider=? AND header_name=? AND ip_ranges=? AND enabled=? AND COALESCE(description,'')=? AND NOT EXISTS (SELECT 1 FROM website_cdn_realip_groups WHERE group_id=cdn_realip_groups.id)`, name, expected.Provider, expected.HeaderName, expected.IPRanges, boolInt(expected.Enabled), expected.Description)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return errors.New("target rollback custom CDN ownership changed")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_migration_resources SET status='removed',updated_at=? WHERE migration_site_id=? AND resource_type='cdn_custom_group' AND status IN ('created','published','remove_failed')`, s.now().UTC(), taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SiteMigrationTargetRollbackService) markRemoved(ctx context.Context, taskID string, kinds ...string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, kind := range kinds {
		if _, err := tx.ExecContext(ctx, `UPDATE site_migration_resources SET status='removed',updated_at=? WHERE migration_site_id=? AND resource_type=? AND status IN ('created','published','remove_failed')`, s.now().UTC(), taskID, kind); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SiteMigrationTargetRollbackService) resourceRemoveFailed(taskID, kind string, cause error) error {
	_, _ = s.db.Exec(`UPDATE site_migration_resources SET status='remove_failed',updated_at=? WHERE migration_site_id=? AND resource_type=? AND status IN ('created','published')`, s.now().UTC(), taskID, kind)
	return s.rollbackFailed(taskID, "target_rollback_resource_failed", cause)
}

func (s *SiteMigrationTargetRollbackService) rollbackFailed(taskID, code string, cause error) error {
	_, _ = s.db.Exec(`UPDATE site_migration_sites SET status='cleanup_failed',cleanup_status='failed',error_code=?,updated_at=? WHERE id=? AND stage='cancelling'`, code, s.now().UTC(), taskID)
	return fmt.Errorf("target migration rollback failed: %w", cause)
}

func (s *SiteMigrationTargetRollbackService) finishRollback(ctx context.Context, taskID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.rollbackFailed(taskID, "target_rollback_finalize_failed", err)
	}
	defer tx.Rollback()
	fail := func(cause error) error {
		_ = tx.Rollback()
		return s.rollbackFailed(taskID, "target_rollback_finalize_failed", cause)
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_resources WHERE migration_site_id=? AND status!='removed'`, taskID).Scan(&remaining); err != nil {
		return fail(err)
	}
	if remaining != 0 {
		return fail(errors.New("target rollback resources remain"))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_artifacts WHERE migration_site_id=?`, taskID); err != nil {
		return fail(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_locks SET status='released',site_id=NULL,released_at=?,updated_at=? WHERE migration_site_id=? AND direction='target' AND status='active'`, s.now().UTC(), s.now().UTC(), taskID)
	if err != nil {
		return fail(err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return fail(errors.New("target rollback lock state changed"))
	}
	result, err = tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='abandoned',stage='abandoned',cleanup_status='complete',error_code='',finished_at=?,updated_at=? WHERE id=? AND status='cancelling'`, s.now().UTC(), s.now().UTC(), taskID)
	if err != nil {
		return fail(err)
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return fail(errors.New("target rollback completion state changed"))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_events (migration_site_id,stage,result,message,created_at) VALUES (?,'target_rollback','success','target resources removed',?)`, taskID, s.now().UTC()); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return s.rollbackFailed(taskID, "target_rollback_finalize_failed", err)
	}
	return nil
}

func hasMigrationResource(resources map[string][]string, kind string) bool {
	return len(resources[kind]) > 0
}

func hasAnyMigrationResource(resources map[string][]string, kinds ...string) bool {
	for _, kind := range kinds {
		if hasMigrationResource(resources, kind) {
			return true
		}
	}
	return false
}

type siteMigrationTargetAbandoner interface {
	Abandon(context.Context, string, string, string, string, bool) error
}

type siteMigrationSourceRestorer interface {
	AbandonSource(context.Context, string) error
}

// AbandonSiteMigration restores A only after B has proved its task-owned
// resources are gone. Callers may safely retry after B completed but A failed.
func AbandonSiteMigration(ctx context.Context, target siteMigrationTargetAbandoner, source siteMigrationSourceRestorer, targetTaskID, sourceTaskID, domain, operator, reason string, allowActivated bool) error {
	if target == nil || source == nil || !validSiteMigrationID(targetTaskID) || !validSiteMigrationID(sourceTaskID) {
		return errors.New("site migration abandon coordination unavailable")
	}
	if err := target.Abandon(ctx, targetTaskID, domain, operator, reason, allowActivated); err != nil {
		return err
	}
	if err := source.AbandonSource(ctx, sourceTaskID); err != nil {
		return fmt.Errorf("target cleaned but source restore failed: %w", err)
	}
	return nil
}
