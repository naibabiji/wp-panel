package executor

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type siteMigrationDatabaseIdentity struct {
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

type siteMigrationPublishSpec struct {
	Domain             string   `json:"domain"`
	Aliases            []string `json:"aliases"`
	SiteType           string   `json:"site_type"`
	DocumentRootSubdir string   `json:"document_root_subdir"`
	SystemUser         string   `json:"system_user"`
	WebRoot            string   `json:"web_root"`
	LogDir             string   `json:"log_dir"`
	DBName             string   `json:"db_name"`
	DBUser             string   `json:"db_user"`
	PHPPoolPath        string   `json:"php_pool_path"`
	NginxConfPath      string   `json:"nginx_conf_path"`
	NginxEnabledPath   string   `json:"nginx_enabled_path"`
	PHPSocketPath      string   `json:"php_socket_path"`
}

type siteMigrationTargetPublishOps interface {
	ImportDatabase(context.Context, string, string) error
	ResetDatabase(string, string, string) error
	CopyTree(string, string) error
	ResetTree(string) error
	RewriteWordPressConfig(string, string, siteMigrationDatabaseIdentity) error
	SetOwner(string, string) error
}

type productionSiteMigrationTargetPublishOps struct{ cfg *config.Config }

func (o productionSiteMigrationTargetPublishOps) ImportDatabase(ctx context.Context, archivePath, databaseName string) error {
	if !isValidMySQLIdentifier(databaseName) {
		return errors.New("invalid migration database name")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer compressed.Close()
	cmd := exec.CommandContext(ctx, "mysql", "-u", o.cfg.MariaDB.RootUser, databaseName)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+o.cfg.MariaDB.RootPassword)
	cmd.Stdin = compressed
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("import migration database: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (o productionSiteMigrationTargetPublishOps) ResetDatabase(name, dbUser, password string) error {
	if err := dropMariaDBDatabase(name, dbUser, o.cfg); err != nil {
		return err
	}
	return createMigrationMariaDBDatabase(name, dbUser, password, o.cfg)
}

func (productionSiteMigrationTargetPublishOps) CopyTree(source, target string) error {
	return copyMigrationTree(source, target)
}

func (productionSiteMigrationTargetPublishOps) ResetTree(target string) error {
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return os.Mkdir(target, 0755)
}

func (productionSiteMigrationTargetPublishOps) RewriteWordPressConfig(webRoot, domain string, identity siteMigrationDatabaseIdentity) error {
	return fixMigrationWPConfigCredentials(webRoot, domain, identity)
}

func (productionSiteMigrationTargetPublishOps) SetOwner(path, owner string) error {
	_, err := executeCommand("chown", "-R", siteOwner(owner), path)
	return err
}

type SiteMigrationTargetPublisher struct {
	db          *sql.DB
	cfg         *config.Config
	stagingRoot string
	ops         siteMigrationTargetPublishOps
	now         func() time.Time
}

func NewSiteMigrationTargetPublisher(db *sql.DB, cfg *config.Config, stagingRoot string) (*SiteMigrationTargetPublisher, error) {
	if db == nil || cfg == nil || !filepath.IsAbs(stagingRoot) {
		return nil, errors.New("site migration target publisher unavailable")
	}
	return &SiteMigrationTargetPublisher{db: db, cfg: cfg, stagingRoot: filepath.Clean(stagingRoot), ops: productionSiteMigrationTargetPublishOps{cfg: cfg}, now: time.Now}, nil
}

func (p *SiteMigrationTargetPublisher) PublishData(ctx context.Context, migrationSiteID string) error {
	if err := p.resumeFailedStage(ctx, migrationSiteID, "publishing"); err != nil {
		return err
	}
	spec, identity, err := p.loadPublishScope(ctx, migrationSiteID)
	if err != nil {
		return err
	}
	siteRoot := filepath.Join(p.stagingRoot, migrationSiteID)
	databaseArchive := filepath.Join(siteRoot, "database", siteMigrationDatabaseArtifactName)
	databaseStatus, databaseExisting, err := p.beginPublishStep(ctx, migrationSiteID, "database_import", identity.Database)
	if err != nil {
		return err
	}
	if databaseStatus != "published" {
		if databaseExisting {
			if err := p.ops.ResetDatabase(identity.Database, identity.User, identity.Password); err != nil {
				return p.publishFailed(migrationSiteID, "database_reset_failed", err)
			}
		}
		if err := p.ops.ImportDatabase(ctx, databaseArchive, identity.Database); err != nil {
			return p.publishFailed(migrationSiteID, "database_import_failed", err)
		}
		if err := p.completePublishStep(ctx, migrationSiteID, "database_import", identity.Database); err != nil {
			return p.publishUnknown(migrationSiteID, err)
		}
	}
	fileStatus, fileExisting, err := p.beginPublishStep(ctx, migrationSiteID, "file_publish", spec.WebRoot)
	if err != nil {
		return err
	}
	if fileStatus != "published" {
		if fileExisting {
			if err := p.ops.ResetTree(spec.WebRoot); err != nil {
				return p.publishFailed(migrationSiteID, "file_reset_failed", err)
			}
		}
		if err := p.ops.CopyTree(filepath.Join(siteRoot, "files"), spec.WebRoot); err != nil {
			return p.publishFailed(migrationSiteID, "file_publish_failed", err)
		}
		if spec.SiteType == "wordpress" {
			if err := p.ops.RewriteWordPressConfig(spec.WebRoot, spec.Domain, identity); err != nil {
				return p.publishFailed(migrationSiteID, "wp_config_rewrite_failed", err)
			}
		}
		if err := p.ops.SetOwner(spec.WebRoot, spec.SystemUser); err != nil {
			return p.publishFailed(migrationSiteID, "file_owner_failed", err)
		}
		if err := p.completePublishStep(ctx, migrationSiteID, "file_publish", spec.WebRoot); err != nil {
			return p.publishUnknown(migrationSiteID, err)
		}
	}
	result, err := p.db.ExecContext(ctx, `UPDATE site_migration_sites SET stage='configuring_target',error_code='',updated_at=? WHERE id=? AND status='running' AND stage='publishing'`, p.now().UTC(), migrationSiteID)
	if err != nil {
		return p.publishUnknown(migrationSiteID, err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return p.publishUnknown(migrationSiteID, errors.New("target publish state changed"))
	}
	return nil
}

func (p *SiteMigrationTargetPublisher) loadPublishScope(ctx context.Context, migrationSiteID string) (siteMigrationPublishSpec, siteMigrationDatabaseIdentity, error) {
	if !validSiteMigrationID(migrationSiteID) {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, errors.New("invalid target publish task")
	}
	var raw string
	var count int
	err := p.db.QueryRowContext(ctx, `SELECT ms.settings_snapshot,COUNT(ml.id) FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='target' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='target' AND ml.status='active'
		WHERE ms.id=? AND ms.status='running' AND ms.stage='publishing' GROUP BY ms.id`, migrationSiteID).Scan(&raw, &count)
	if err != nil || count != 1 {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, errors.New("target publish scope unavailable")
	}
	var snapshot struct {
		TargetSpec siteMigrationPublishSpec `json:"target_spec"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, err
	}
	spec := snapshot.TargetSpec
	normalized, err := normalizeSiteMigrationTargetSpec(SiteMigrationTargetSpec{Domain: spec.Domain, Aliases: spec.Aliases, SiteType: spec.SiteType, DocumentRootSubdir: spec.DocumentRootSubdir})
	if err != nil || normalized.Domain != spec.Domain || normalized.DocumentRootSubdir != spec.DocumentRootSubdir {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, errors.New("target publish specification invalid")
	}
	plan, err := p.expectedPlan(spec.Domain, spec.SiteType)
	if err != nil || spec.SystemUser != plan.SystemUser || spec.WebRoot != plan.WebRoot || spec.LogDir != plan.LogDir || spec.DBName != plan.DBName || spec.DBUser != plan.DBUser || spec.PHPPoolPath != plan.PHPPoolPath || spec.NginxConfPath != plan.NginxConfPath || spec.NginxEnabledPath != plan.NginxEnabledPath || spec.PHPSocketPath != plan.PHPSockPath {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, errors.New("target publish plan mismatch")
	}
	siteRoot := filepath.Join(p.stagingRoot, migrationSiteID)
	var stagingIdentifier, stagingOwner string
	if err := p.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag FROM site_migration_resources WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, migrationSiteID).Scan(&stagingIdentifier, &stagingOwner); err != nil || filepath.Clean(stagingIdentifier) != siteRoot || stagingOwner != migrationSiteID {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, errors.New("target publish staging ownership unavailable")
	}
	identityPath := filepath.Join(siteRoot, "identity", "database.json")
	var identityIdentifier, identityOwner string
	if err := p.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag FROM site_migration_resources WHERE migration_site_id=? AND resource_type='database_identity' AND status IN ('created','published')`, migrationSiteID).Scan(&identityIdentifier, &identityOwner); err != nil || filepath.Clean(identityIdentifier) != identityPath || identityOwner != migrationSiteID {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, errors.New("target database identity ownership unavailable")
	}
	content, err := os.ReadFile(identityPath)
	var identity siteMigrationDatabaseIdentity
	if err != nil || json.Unmarshal(content, &identity) != nil || identity.Database != spec.DBName || identity.User != spec.DBUser || len(identity.Password) < 24 {
		return siteMigrationPublishSpec{}, siteMigrationDatabaseIdentity{}, errors.New("target database identity invalid")
	}
	return spec, identity, nil
}

func (p *SiteMigrationTargetPublisher) expectedPlan(domain, siteType string) (siteMigrationTargetPlan, error) {
	service := &SiteMigrationTargetResourceService{cfg: p.cfg}
	spec, err := normalizeSiteMigrationTargetSpec(SiteMigrationTargetSpec{Domain: domain, SiteType: siteType})
	if err != nil {
		return siteMigrationTargetPlan{}, err
	}
	siteName := buildSiteName(spec.Domain)
	systemUser := "wp_" + siteName
	if siteType == "php" {
		systemUser = "php_" + siteName
	}
	configBase := siteConfigBaseName(siteName)
	plan := siteMigrationTargetPlan{Domain: domain, SystemUser: systemUser, WebRoot: filepath.Join(service.cfg.Paths.WWWRoot, domain), LogDir: filepath.Join(service.cfg.Paths.WWWLogs, domain), DBName: "db_" + siteName, DBUser: "user_" + siteName, PHPPoolPath: filepath.Join(service.cfg.Paths.PHPFPMPool, configBase+".conf"), NginxConfPath: filepath.Join(service.cfg.Paths.NginxSitesAvailable, configBase+".conf"), NginxEnabledPath: filepath.Join(service.cfg.Paths.NginxSitesEnabled, configBase+".conf"), PHPSockPath: filepath.Join(service.cfg.Paths.PHPFPMSock, configBase+".sock")}
	return plan, validateUnixSocketPath(plan.PHPSockPath)
}

func (p *SiteMigrationTargetPublisher) beginPublishStep(ctx context.Context, taskID, kind, identifier string) (string, bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var owner, status string
	err = tx.QueryRowContext(ctx, `SELECT ownership_tag,status FROM site_migration_resources WHERE migration_site_id=? AND resource_type=? AND identifier=?`, taskID, kind, identifier).Scan(&owner, &status)
	existing := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_resources (migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at) VALUES (?,?,?,?,'created',?,?)`, taskID, kind, identifier, taskID, p.now().UTC(), p.now().UTC()); err != nil {
			return "", false, err
		}
		status = "created"
	} else if err != nil {
		return "", false, err
	} else if owner != taskID || status != "created" && status != "published" {
		return "", false, errors.New("target publish intent conflicts")
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return status, existing, nil
}

func (p *SiteMigrationTargetPublisher) resumeFailedStage(ctx context.Context, taskID, stage string) error {
	result, err := p.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',error_code='',updated_at=? WHERE id=? AND stage=? AND status='failed_retryable'`, p.now().UTC(), taskID, stage)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	var status string
	if err := p.db.QueryRowContext(ctx, `SELECT status FROM site_migration_sites WHERE id=? AND stage=?`, taskID, stage).Scan(&status); err != nil || status != "running" {
		return errors.New("target migration stage is not retryable")
	}
	return nil
}

func (p *SiteMigrationTargetPublisher) completePublishStep(ctx context.Context, taskID, kind, identifier string) error {
	result, err := p.db.ExecContext(ctx, `UPDATE site_migration_resources SET status='published',updated_at=? WHERE migration_site_id=? AND resource_type=? AND identifier=? AND ownership_tag=? AND status='created'`, p.now().UTC(), taskID, kind, identifier, taskID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("target publish ownership state changed")
	}
	return nil
}

func (p *SiteMigrationTargetPublisher) publishFailed(taskID, code string, cause error) error {
	_, _ = p.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',error_code=?,updated_at=? WHERE id=? AND stage='publishing'`, code, p.now().UTC(), taskID)
	return fmt.Errorf("target data publish failed: %w", cause)
}

func (p *SiteMigrationTargetPublisher) publishUnknown(taskID string, cause error) error {
	_, _ = p.db.Exec(`UPDATE site_migration_sites SET status='interrupted_unknown',stage='interrupted_unknown',error_code='publish_commit_unknown',updated_at=? WHERE id=?`, p.now().UTC(), taskID)
	return fmt.Errorf("target data publish state unknown: %w", cause)
}

func copyMigrationTree(source, target string) error {
	source = filepath.Clean(source)
	target = filepath.Clean(target)
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		destination := filepath.Join(target, rel)
		if !migrationPathWithin(target, destination) {
			return errors.New("migration publish path escaped target")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, destination)
		}
		if entry.IsDir() {
			if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return errors.New("unsupported migration publish entry")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	})
}

func fixMigrationWPConfigCredentials(webRoot, domain string, identity siteMigrationDatabaseIdentity) error {
	content, err := readWPConfigSecure(webRoot)
	if err != nil {
		return err
	}
	result := string(content)
	for name, value := range map[string]string{"DB_NAME": identity.Database, "DB_USER": identity.User, "DB_PASSWORD": identity.Password} {
		single := regexpMustCompileWPDefine(name, true)
		double := regexpMustCompileWPDefine(name, false)
		replacement := fmt.Sprintf("define('%s', '%s')", name, phpSingleQuoteEscape(value))
		if single.MatchString(result) {
			result = single.ReplaceAllLiteralString(result, replacement)
		} else if double.MatchString(result) {
			result = double.ReplaceAllLiteralString(result, replacement)
		} else {
			return fmt.Errorf("WordPress database constant unavailable: %s", name)
		}
	}
	result, _ = ensureWPConfigCachePrefixes(result, wpCacheKeySalt(domain))
	result = freezeMigrationWPConfigCron(result)
	return writeWPConfigSecure(webRoot, []byte(result))
}

func freezeMigrationWPConfigCron(content string) string {
	re := regexp.MustCompile(`(?m)define\(\s*['"]DISABLE_WP_CRON['"]\s*,\s*(?:true|false)\s*\)\s*;?`)
	managed := "define('DISABLE_WP_CRON', true); // WP Panel migration freeze"
	if re.MatchString(content) {
		return re.ReplaceAllLiteralString(content, managed)
	}
	for _, marker := range []string{"/* That's all, stop editing!", "/* 以上信息设置完毕！"} {
		if index := strings.Index(content, marker); index >= 0 {
			return content[:index] + managed + "\n\n" + content[index:]
		}
	}
	return content + "\n" + managed + "\n"
}

func regexpMustCompileWPDefine(name string, single bool) *regexp.Regexp {
	quote := `"`
	value := `[^\"]*`
	if single {
		quote = `'`
		value = `[^']*`
	}
	return regexp.MustCompile(`define\(\s*` + quote + name + quote + `\s*,\s*` + quote + value + quote + `\s*\)`)
}
