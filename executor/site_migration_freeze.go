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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type siteMigrationNginxRunner interface {
	Test(context.Context) error
	Reload(context.Context) error
	Verify(context.Context, string, bool, string) error
}

type productionSiteMigrationNginxRunner struct{}

func (productionSiteMigrationNginxRunner) Test(context.Context) error {
	_, err := Execute("nginx", "-t")
	return err
}

func (productionSiteMigrationNginxRunner) Reload(context.Context) error {
	_, err := Execute("nginx", "-s", "reload")
	return err
}

func (productionSiteMigrationNginxRunner) Verify(ctx context.Context, domain string, useSSL bool, markerToken string) error {
	return verifySiteMigrationMaintenance(ctx, domain, useSSL, markerToken)
}

type siteMigrationFreezer struct {
	db        *sql.DB
	cfg       *config.Config
	runner    siteMigrationNginxRunner
	now       func() time.Time
	removeAll func(string) error
}

type siteMigrationFreezeSite struct {
	SiteID                                   int
	Domain, Aliases, WebRoot, NginxConfPath  string
	SiteType, DocumentRootSubdir             string
	SSLEnabled                               bool
	SSLCertPath, SSLKeyPath, Stage, Snapshot string
	OriginalTarget                           string
}

// ReconcileMigratedWebsiteStatuses recognizes source sites completed by older
// builds that already removed their one-time task but left the managed
// migration maintenance symlink in place.
func ReconcileMigratedWebsiteStatuses(ctx context.Context, db *sql.DB, cfg *config.Config) (int64, error) {
	if db == nil || cfg == nil || cfg.Paths.NginxSitesAvailable == "" || cfg.Paths.NginxSitesEnabled == "" {
		return 0, errors.New("invalid migrated website reconciliation configuration")
	}
	rows, err := db.QueryContext(ctx, `SELECT w.id,w.domain,w.nginx_conf_path FROM websites w
		WHERE w.status='active' AND NOT EXISTS (
			SELECT 1 FROM site_migration_locks ml WHERE ml.status='active' AND (ml.site_id=w.id OR ml.domain=w.domain)
		) ORDER BY w.id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type candidate struct {
		id                int
		domain, nginxConf string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.domain, &item.nginxConf); err != nil {
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var changed int64
	for _, item := range candidates {
		nginxConf, err := managedSubpath(cfg.Paths.NginxSitesAvailable, item.nginxConf, "Nginx配置")
		if err != nil {
			continue
		}
		enabledPath, err := managedSubpath(cfg.Paths.NginxSitesEnabled, nginxEnabledPath(cfg, nginxConf, item.domain), "Nginx启用链接")
		if err != nil {
			continue
		}
		target, err := os.Readlink(enabledPath)
		if err != nil || !strings.HasPrefix(filepath.Base(target), ".wp-panel-migration-") {
			continue
		}
		if _, err := managedSubpath(cfg.Paths.NginxSitesAvailable, target, "迁移维护配置"); err != nil {
			continue
		}
		result, err := db.ExecContext(ctx, `UPDATE websites SET status='migrated',updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND status='active' AND NOT EXISTS (
				SELECT 1 FROM site_migration_locks ml WHERE ml.status='active' AND (ml.site_id=websites.id OR ml.domain=websites.domain)
			)`, item.id)
		if err != nil {
			return changed, err
		}
		affected, _ := result.RowsAffected()
		changed += affected
	}
	return changed, nil
}

var siteMigrationMarkerPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{40,128}$`)

func newSiteMigrationFreezer(db *sql.DB, cfg *config.Config, runner siteMigrationNginxRunner) (*siteMigrationFreezer, error) {
	if db == nil || cfg == nil || runner == nil || cfg.Paths.NginxSitesAvailable == "" || cfg.Paths.NginxSitesEnabled == "" {
		return nil, errors.New("invalid site migration freezer configuration")
	}
	return &siteMigrationFreezer{db: db, cfg: cfg, runner: runner, now: time.Now, removeAll: os.RemoveAll}, nil
}

func atomicWriteMigrationFile(path string, content []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".wp-panel-migration-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func atomicReplaceSymlink(path, target string) error {
	tmp := path + ".migration-swap"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func renderSiteMigrationMaintenance(site *siteMigrationFreezeSite, migrationSiteID, markerToken string) (string, error) {
	if site == nil || !IsValidDomain(site.Domain) || !validSiteMigrationID(migrationSiteID) || !siteMigrationMarkerPattern.MatchString(markerToken) {
		return "", errors.New("invalid migration maintenance data")
	}
	serverNames := []string{site.Domain}
	for _, alias := range splitAliases(site.Aliases) {
		if !IsValidDomain(alias) {
			return "", errors.New("invalid source site alias")
		}
		serverNames = append(serverNames, alias)
	}
	markerPath := "/.well-known/wp-panel-migration/" + markerToken
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"robots\" content=\"noindex,nofollow\"><title>Site maintenance</title></head><body><h1>Site migration in progress</h1><p>Please try again later.</p></body></html>"
	block := func(listen, tls string) string {
		return "server {\n" + listen + "    server_name " + strings.Join(serverNames, " ") + ";\n" + tls +
			"    root " + EffectiveDocumentRoot(site.WebRoot, site.SiteType, site.DocumentRootSubdir) + ";\n" +
			"    location ^~ /.well-known/acme-challenge/ { try_files $uri =404; }\n" +
			"    location = " + markerPath + " { access_log off; default_type application/json; add_header Cache-Control \"no-store\" always; return 200 '{\"role\":\"source\",\"task\":\"" + migrationSiteID + "\"}'; }\n" +
			"    location / { default_type text/html; add_header Cache-Control \"no-store, no-cache, max-age=0, must-revalidate\" always; add_header Retry-After \"300\" always; return 503 '" + body + "'; }\n}\n"
	}
	content := "# WP Panel migration maintenance: " + migrationSiteID + "\n" + block("    listen 80;\n    listen [::]:80;\n", "")
	if site.SSLEnabled {
		if site.SSLCertPath == "" || site.SSLKeyPath == "" {
			return "", errors.New("source SSL paths unavailable")
		}
		content += block("    listen 443 ssl;\n    listen [::]:443 ssl;\n", "    ssl_certificate "+site.SSLCertPath+";\n    ssl_certificate_key "+site.SSLKeyPath+";\n")
	}
	return content, nil
}

func (f *siteMigrationFreezer) load(ctx context.Context, migrationSiteID string) (*siteMigrationFreezeSite, error) {
	var site siteMigrationFreezeSite
	var ssl int
	err := f.db.QueryRowContext(ctx, `SELECT w.id,w.domain,w.aliases,w.web_root,w.nginx_conf_path,w.site_type,w.document_root_subdir,
		w.ssl_enabled,w.ssl_cert_path,w.ssl_key_path,ms.stage,ms.settings_snapshot,
		COALESCE((SELECT ownership_tag FROM site_migration_resources WHERE migration_site_id=ms.id AND resource_type='source_maintenance_config' LIMIT 1),'')
		FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source'
		JOIN websites w ON w.id=ms.source_site_id JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.site_id=w.id AND ml.status='active'
		WHERE ms.id=?`, migrationSiteID).Scan(&site.SiteID, &site.Domain, &site.Aliases, &site.WebRoot, &site.NginxConfPath,
		&site.SiteType, &site.DocumentRootSubdir, &ssl, &site.SSLCertPath, &site.SSLKeyPath, &site.Stage, &site.Snapshot, &site.OriginalTarget)
	if err != nil {
		return nil, fmt.Errorf("load source migration site: %w", err)
	}
	site.SSLEnabled = ssl == 1
	return &site, nil
}

func (f *siteMigrationFreezer) FreezeSource(ctx context.Context, migrationSiteID, markerToken string) error {
	if !validSiteMigrationID(migrationSiteID) || !siteMigrationMarkerPattern.MatchString(markerToken) {
		return errors.New("invalid site migration freeze request")
	}
	site, err := f.load(ctx, migrationSiteID)
	if err != nil {
		return err
	}
	if site.Stage != "preflight_passed" && site.Stage != "source_freezing" {
		return errors.New("site migration is not ready to freeze")
	}
	if !TryAcquireSiteOpLock(site.SiteID, "site_migration_freeze") {
		return errors.New("site has an active write operation")
	}
	defer ReleaseSiteOpLock(site.SiteID)

	originalPath, err := managedSubpath(f.cfg.Paths.NginxSitesAvailable, site.NginxConfPath, "Nginx配置")
	if err != nil {
		return err
	}
	enabledPath, err := managedSubpath(f.cfg.Paths.NginxSitesEnabled, nginxEnabledPath(f.cfg, originalPath, site.Domain), "Nginx启用链接")
	if err != nil {
		return err
	}
	maintenancePath, err := managedSubpath(f.cfg.Paths.NginxSitesAvailable,
		filepath.Join(f.cfg.Paths.NginxSitesAvailable, ".wp-panel-migration-"+migrationSiteID+".conf"), "迁移维护配置")
	if err != nil {
		return err
	}
	originalTarget, err := os.Readlink(enabledPath)
	if err != nil {
		return fmt.Errorf("source site must have an enabled Nginx symlink: %w", err)
	}
	if filepath.Clean(originalTarget) == filepath.Clean(maintenancePath) && site.Stage == "source_freezing" {
		if site.OriginalTarget == "" {
			return errors.New("source migration recovery target unavailable")
		}
		if err := f.runner.Test(ctx); err != nil {
			return fmt.Errorf("test recovered migration maintenance config: %w", err)
		}
		if err := f.runner.Reload(ctx); err != nil {
			return fmt.Errorf("reload recovered migration maintenance config: %w", err)
		}
		if err := f.runner.Verify(ctx, site.Domain, site.SSLEnabled, markerToken); err != nil {
			if restoreErr := atomicReplaceSymlink(enabledPath, site.OriginalTarget); restoreErr != nil {
				return errors.Join(err, restoreErr)
			}
			if reloadErr := f.runner.Reload(context.Background()); reloadErr != nil {
				_ = atomicReplaceSymlink(enabledPath, maintenancePath)
				return errors.Join(err, fmt.Errorf("restore source Nginx runtime: %w", reloadErr))
			}
			return fmt.Errorf("verify recovered migration maintenance response: %w", err)
		}
		return f.markFrozen(ctx, migrationSiteID)
	}
	if filepath.Clean(originalTarget) != filepath.Clean(originalPath) {
		return errors.New("source Nginx symlink does not point to the managed config")
	}
	content, err := renderSiteMigrationMaintenance(site, migrationSiteID, markerToken)
	if err != nil {
		return err
	}
	if err := f.recordIntent(ctx, migrationSiteID, maintenancePath, originalTarget, markerToken, site.Snapshot); err != nil {
		return err
	}
	switched := false
	activated := false
	rollback := func() error {
		if switched {
			if err := atomicReplaceSymlink(enabledPath, originalTarget); err != nil {
				return fmt.Errorf("restore source Nginx symlink: %w", err)
			}
			if activated {
				if err := f.runner.Reload(context.Background()); err != nil {
					_ = atomicReplaceSymlink(enabledPath, maintenancePath)
					return fmt.Errorf("restore source Nginx runtime: %w", err)
				}
			}
		}
		_ = os.Remove(maintenancePath)
		_, _ = f.db.ExecContext(context.Background(), `DELETE FROM site_migration_resources WHERE migration_site_id=? AND resource_type='source_maintenance_config'`, migrationSiteID)
		_, _ = f.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET stage=?,settings_snapshot=?,updated_at=? WHERE id=? AND stage='source_freezing'`, site.Stage, site.Snapshot, f.now().UTC(), migrationSiteID)
		return nil
	}
	if err := atomicWriteMigrationFile(maintenancePath, []byte(content), 0644); err != nil {
		_ = rollback()
		return err
	}
	if err := atomicReplaceSymlink(enabledPath, maintenancePath); err != nil {
		_ = rollback()
		return err
	}
	switched = true
	if err := f.runner.Test(ctx); err != nil {
		_ = rollback()
		return fmt.Errorf("test migration maintenance config: %w", err)
	}
	if err := f.runner.Reload(ctx); err != nil {
		_ = rollback()
		return fmt.Errorf("reload migration maintenance config: %w", err)
	}
	activated = true
	if err := f.runner.Verify(ctx, site.Domain, site.SSLEnabled, markerToken); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(fmt.Errorf("verify migration maintenance response: %w", err), rollbackErr)
		}
		return fmt.Errorf("verify migration maintenance response: %w", err)
	}
	if err := f.markFrozen(ctx, migrationSiteID); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

func (f *siteMigrationFreezer) recordIntent(ctx context.Context, migrationSiteID, maintenancePath, originalTarget, markerToken, oldSnapshot string) error {
	snapshot := map[string]any{}
	if strings.TrimSpace(oldSnapshot) != "" {
		if err := json.Unmarshal([]byte(oldSnapshot), &snapshot); err != nil {
			return fmt.Errorf("decode site migration settings snapshot: %w", err)
		}
	}
	snapshot["source_marker_token"] = markerToken
	snapshot["original_nginx_target"] = originalTarget
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	now := f.now().UTC()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET stage='source_freezing',settings_snapshot=?,updated_at=? WHERE id=? AND stage IN ('preflight_passed','source_freezing')`, string(encoded), now, migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("site migration freeze state changed")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO site_migration_resources (migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at)
		VALUES (?,'source_maintenance_config',?,?,'created',?,?)
		ON CONFLICT(migration_site_id,resource_type,identifier) DO UPDATE SET ownership_tag=excluded.ownership_tag,status='created',updated_at=excluded.updated_at`,
		migrationSiteID, maintenancePath, originalTarget, now, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (f *siteMigrationFreezer) markFrozen(ctx context.Context, migrationSiteID string) error {
	result, err := f.db.ExecContext(ctx, `UPDATE site_migration_sites SET stage='source_frozen',updated_at=? WHERE id=? AND stage='source_freezing'`, f.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("site migration freeze state changed")
	}
	return nil
}

func (f *siteMigrationFreezer) AbandonSource(ctx context.Context, migrationSiteID string) error {
	if !validSiteMigrationID(migrationSiteID) {
		return errors.New("invalid source migration abandon request")
	}
	site, err := f.load(ctx, migrationSiteID)
	if err != nil {
		return err
	}
	var maintenancePath string
	err = f.db.QueryRowContext(ctx, `SELECT identifier FROM site_migration_resources
		WHERE migration_site_id=? AND resource_type='source_maintenance_config' AND status='created'`, migrationSiteID).Scan(&maintenancePath)
	if err != nil || site.OriginalTarget == "" {
		return errors.New("source migration maintenance ownership unavailable")
	}
	maintenancePath, err = managedSubpath(f.cfg.Paths.NginxSitesAvailable, maintenancePath, "迁移维护配置")
	if err != nil {
		return err
	}
	originalTarget, err := managedSubpath(f.cfg.Paths.NginxSitesAvailable, site.OriginalTarget, "原 Nginx 配置")
	if err != nil {
		return err
	}
	enabledPath, err := managedSubpath(f.cfg.Paths.NginxSitesEnabled, nginxEnabledPath(f.cfg, originalTarget, site.Domain), "Nginx启用链接")
	if err != nil {
		return err
	}
	current, err := os.Readlink(enabledPath)
	if err != nil {
		return errors.New("source maintenance link ownership changed")
	}
	alreadyRestored := filepath.Clean(current) == filepath.Clean(originalTarget) && site.Stage == "cancelling"
	if filepath.Clean(current) != filepath.Clean(maintenancePath) && !alreadyRestored {
		return errors.New("source maintenance link ownership changed")
	}
	now := f.now().UTC()
	result, err := f.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='cancelling',stage='cancelling',cleanup_status='running',updated_at=?
		WHERE id=? AND (lease_owner='' OR lease_expires_at IS NULL OR lease_expires_at<=?)`, now, migrationSiteID, now)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("source migration task is still running")
	}
	if f.cfg.Panel.DataDir != "" {
		for _, root := range []string{"database", "certificates"} {
			path := filepath.Join(f.cfg.Panel.DataDir, "site-migration", root, migrationSiteID)
			if err := f.removeAll(path); err != nil {
				_, _ = f.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET status='cleanup_failed',cleanup_status='failed',error_code='source_cleanup_failed',updated_at=? WHERE id=?`, f.now().UTC(), migrationSiteID)
				return fmt.Errorf("remove source migration artifact %s: %w", root, err)
			}
		}
	}
	if !alreadyRestored {
		if err := atomicReplaceSymlink(enabledPath, originalTarget); err != nil {
			return err
		}
	}
	rollback := func(cause error) error {
		if replaceErr := atomicReplaceSymlink(enabledPath, maintenancePath); replaceErr != nil {
			return errors.Join(cause, replaceErr)
		}
		_ = f.runner.Reload(context.Background())
		return cause
	}
	if err := f.runner.Test(ctx); err != nil {
		return rollback(fmt.Errorf("test restored source config: %w", err))
	}
	if err := f.runner.Reload(ctx); err != nil {
		return rollback(fmt.Errorf("reload restored source config: %w", err))
	}
	if err := os.Remove(maintenancePath); err != nil && !os.IsNotExist(err) {
		return rollback(fmt.Errorf("remove source maintenance config: %w", err))
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_artifacts WHERE migration_site_id=?`, migrationSiteID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_migration_resources SET status='removed',updated_at=? WHERE migration_site_id=? AND resource_type='source_maintenance_config' AND status='created'`, f.now().UTC(), migrationSiteID); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE site_migration_locks SET status='released',site_id=NULL,released_at=?,updated_at=? WHERE migration_site_id=? AND direction='source' AND status='active'`, f.now().UTC(), f.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("source migration lock state changed")
	}
	result, err = tx.ExecContext(ctx, `UPDATE site_migration_sites SET source_site_id=NULL,status='abandoned',stage='abandoned',cleanup_status='complete',error_code='',updated_at=?,finished_at=? WHERE id=? AND status='cancelling'`, f.now().UTC(), f.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("source migration abandon state changed")
	}
	return tx.Commit()
}

// CompleteSource records the user's decision that the migrated target is now
// authoritative. The source website remains behind the maintenance
// configuration and is marked migrated until the user restores or deletes it.
func (f *siteMigrationFreezer) CompleteSource(ctx context.Context, migrationSiteID string) error {
	if !validSiteMigrationID(migrationSiteID) {
		return errors.New("invalid source migration completion request")
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f.completeSourceTx(ctx, tx, migrationSiteID); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *siteMigrationFreezer) completeSourceTx(ctx context.Context, tx *sql.Tx, migrationSiteID string) error {
	var status, stage string
	var siteID int
	if err := tx.QueryRowContext(ctx, `SELECT ms.status,ms.stage,ms.source_site_id FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='source' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.site_id=ms.source_site_id AND ml.direction='source' AND ml.status='active'
		JOIN site_migration_resources r ON r.migration_site_id=ms.id AND r.resource_type='source_maintenance_config' AND r.status='created'
		WHERE ms.id=?`, migrationSiteID).Scan(&status, &stage, &siteID); err != nil {
		return errors.New("source migration completion unavailable")
	}
	if status == "completed" && stage == "completed" {
		var websiteStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM websites WHERE id=?`, siteID).Scan(&websiteStatus); err != nil || websiteStatus != "migrated" {
			return errors.New("source migration completion state changed")
		}
		return nil
	}
	if status != "awaiting_cutover" || stage != "transferring_database" {
		return errors.New("source migration completion unavailable")
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='completed',stage='completed',cleanup_status='not_needed',error_code='',updated_at=?,finished_at=?
		WHERE id=? AND status='awaiting_cutover' AND stage='transferring_database'`, f.now().UTC(), f.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("source migration completion state changed")
	}
	result, err = tx.ExecContext(ctx, `UPDATE websites SET status='migrated',updated_at=? WHERE id=? AND status='active'`, f.now().UTC(), siteID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("source website migration state changed")
	}
	return nil
}

func verifySiteMigrationMaintenance(ctx context.Context, domain string, useSSL bool, markerToken string) error {
	verifyEndpoint := func(scheme, port string) error {
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
			},
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, // local loopback response check, not pairing trust.
		}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		check := func(path string, status int, contains string) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+domain+path, nil)
			if err != nil {
				return err
			}
			req.Host = domain
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			if err != nil {
				return err
			}
			if resp.StatusCode != status || !strings.Contains(string(body), contains) || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
				return fmt.Errorf("unexpected local maintenance response status=%d", resp.StatusCode)
			}
			return nil
		}
		if err := check("/", http.StatusServiceUnavailable, "Site migration in progress"); err != nil {
			return err
		}
		return check("/.well-known/wp-panel-migration/"+markerToken, http.StatusOK, `"role":"source"`)
	}
	return retrySiteMigrationVerification(ctx, 5*time.Second, func() error {
		if err := verifyEndpoint("http", "80"); err != nil {
			return err
		}
		if useSSL {
			return verifyEndpoint("https", "443")
		}
		return nil
	})
}

func retrySiteMigrationVerification(ctx context.Context, timeout time.Duration, verify func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := verify(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return lastErr
		}
		delay := 100 * time.Millisecond
		if remaining < delay {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
