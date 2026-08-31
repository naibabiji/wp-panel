package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

type SiteMigrationTargetSpec struct {
	Domain             string
	Aliases            []string
	SiteType           string
	DocumentRootSubdir string
}

type siteMigrationTargetPlan struct {
	Domain, SystemUser, WebRoot, LogDir string
	DBName, DBUser                      string
	PHPPoolPath, NginxConfPath          string
	NginxEnabledPath, PHPSockPath       string
}

type siteMigrationTargetResourceOps interface {
	EnsureAvailable(siteMigrationTargetPlan) error
	CreateUser(string) error
	RemoveUser(string) error
	CreateDirectory(string, os.FileMode) error
	RemoveDirectory(string) error
	SetOwner(string, string) error
	CreateDatabase(string, string, string) error
	DropDatabase(string, string) error
}

type productionSiteMigrationTargetResourceOps struct{ cfg *config.Config }

func (o productionSiteMigrationTargetResourceOps) EnsureAvailable(plan siteMigrationTargetPlan) error {
	return ensureCreateSiteResourcesAvailable(plan.SystemUser, plan.WebRoot, plan.LogDir, plan.DBName, plan.DBUser, plan.PHPPoolPath, plan.NginxConfPath, plan.NginxEnabledPath, plan.PHPSockPath)
}
func (o productionSiteMigrationTargetResourceOps) CreateUser(name string) error {
	if _, err := executeCommand("useradd", "-r", "-U", "-s", "/usr/sbin/nologin", "-M", "-d", "/nonexistent", name); err != nil {
		return err
	}
	return ensureSitePrimaryGroup(name)
}
func (o productionSiteMigrationTargetResourceOps) RemoveUser(name string) error {
	return removeMigrationSystemUser(name)
}

func removeMigrationSystemUser(name string) error {
	return removeMigrationSystemUserWith(name, user.Lookup, executeCommand)
}

func removeMigrationSystemUserWith(name string, lookup func(string) (*user.User, error), run func(string, ...string) (string, error)) error {
	name = strings.TrimSpace(name)
	if !wpInventoryUserPattern.MatchString(name) {
		return errors.New("invalid migration system user")
	}
	if _, err := lookup(name); err == nil {
		if _, err := run("userdel", "-r", "-f", name); err != nil {
			return err
		}
	} else {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return err
		}
	}
	if _, err := run("getent", "group", name); err != nil {
		return nil
	}
	_, err := run("groupdel", name)
	return err
}
func (o productionSiteMigrationTargetResourceOps) CreateDirectory(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode.Perm()); err != nil {
		return err
	}
	return os.Chmod(path, mode.Perm())
}
func (o productionSiteMigrationTargetResourceOps) RemoveDirectory(path string) error {
	return os.RemoveAll(path)
}
func (o productionSiteMigrationTargetResourceOps) SetOwner(path, owner string) error {
	_, err := executeCommand("chown", "-R", siteOwner(owner), path)
	return err
}
func (o productionSiteMigrationTargetResourceOps) CreateDatabase(name, user, password string) error {
	return createMigrationMariaDBDatabase(name, user, password, o.cfg)
}
func (o productionSiteMigrationTargetResourceOps) DropDatabase(name, user string) error {
	return dropMariaDBDatabase(name, user, o.cfg)
}

type SiteMigrationTargetResourceService struct {
	db          *sql.DB
	cfg         *config.Config
	stagingRoot string
	ops         siteMigrationTargetResourceOps
	now         func() time.Time
	password    func(int) string
	apiKey      func() string
}

func NewSiteMigrationTargetResourceService(db *sql.DB, cfg *config.Config, stagingRoot string) (*SiteMigrationTargetResourceService, error) {
	if db == nil || cfg == nil || !filepath.IsAbs(stagingRoot) {
		return nil, errors.New("site migration target resource service unavailable")
	}
	return &SiteMigrationTargetResourceService{db: db, cfg: cfg, stagingRoot: filepath.Clean(stagingRoot), ops: productionSiteMigrationTargetResourceOps{cfg: cfg}, now: time.Now, password: generatePassword, apiKey: NewAPIKey}, nil
}

func (s *SiteMigrationTargetResourceService) Create(ctx context.Context, migrationSiteID string, spec SiteMigrationTargetSpec) error {
	var err error
	spec, err = normalizeSiteMigrationTargetSpec(spec)
	if err != nil {
		return err
	}
	stage, err := s.prepareCreationRetry(ctx, migrationSiteID, spec)
	if err != nil {
		return err
	}
	plan, err := s.plan(ctx, migrationSiteID, spec, stage)
	if err != nil {
		return err
	}
	if stage == "preparing_target" {
		if err := s.ops.EnsureAvailable(plan); err != nil {
			return err
		}
		if err := s.beginCreation(ctx, migrationSiteID, spec, plan); err != nil {
			return err
		}
	}
	if err := s.createAndRecord(ctx, migrationSiteID, "system_user", plan.SystemUser, func() error { return s.ops.CreateUser(plan.SystemUser) }, func() error { return s.ops.RemoveUser(plan.SystemUser) }); err != nil {
		return s.handleCreationError(migrationSiteID, err)
	}
	for _, directory := range []struct {
		kind, path string
		mode       os.FileMode
	}{
		{kind: "web_root", path: plan.WebRoot, mode: 0755},
		{kind: "log_dir", path: plan.LogDir, mode: 0755},
	} {
		item := directory
		if err := s.createAndRecord(ctx, migrationSiteID, item.kind, item.path, func() error { return s.ops.CreateDirectory(item.path, item.mode) }, func() error { return s.ops.RemoveDirectory(item.path) }); err != nil {
			return s.handleCreationError(migrationSiteID, err)
		}
		if err := s.ops.SetOwner(item.path, plan.SystemUser); err != nil {
			return s.handleCreationError(migrationSiteID, err)
		}
	}
	identityPath := filepath.Join(s.stagingRoot, migrationSiteID, "identity", "database.json")
	dbIdentity, identityExists, err := readMigrationDatabaseIdentity(identityPath, plan.DBName, plan.DBUser)
	if err != nil {
		return s.handleCreationError(migrationSiteID, err)
	}
	if !identityExists {
		dbIdentity = siteMigrationDatabaseIdentity{Database: plan.DBName, User: plan.DBUser, Password: s.password(32)}
		if len(dbIdentity.Password) < 24 {
			return s.creationFailed(migrationSiteID, errors.New("generated database credential unavailable"))
		}
	}
	if err := s.createAndRecord(ctx, migrationSiteID, "database_identity", identityPath, func() error {
		return writeMigrationDatabaseIdentity(identityPath, plan.DBName, plan.DBUser, dbIdentity.Password)
	}, func() error { return os.Remove(identityPath) }); err != nil {
		return s.handleCreationError(migrationSiteID, err)
	}
	dbIdentity, identityExists, err = readMigrationDatabaseIdentity(identityPath, plan.DBName, plan.DBUser)
	if err != nil || !identityExists {
		if err == nil {
			err = errors.New("target database identity unavailable")
		}
		return s.handleCreationError(migrationSiteID, err)
	}
	if err := s.createAndRecord(ctx, migrationSiteID, "database", plan.DBName, func() error { return s.ops.CreateDatabase(plan.DBName, plan.DBUser, dbIdentity.Password) }, func() error { return s.ops.DropDatabase(plan.DBName, plan.DBUser) }); err != nil {
		return s.handleCreationError(migrationSiteID, err)
	}
	if err := s.createAndRecord(ctx, migrationSiteID, "database_user", plan.DBUser, func() error { return nil }, func() error { return nil }); err != nil {
		return s.handleCreationError(migrationSiteID, err)
	}
	siteIdentityPath := filepath.Join(s.stagingRoot, migrationSiteID, "identity", sitePluginConfigFileName)
	panelURL := fmt.Sprintf("https://127.0.0.1:%d/%s", s.cfg.Panel.TLSPort, s.cfg.Panel.RandomSuffix)
	if err := s.createAndRecord(ctx, migrationSiteID, "site_identity", siteIdentityPath, func() error {
		apiKey := s.apiKey()
		if len(apiKey) < 32 {
			return errors.New("generated site API credential unavailable")
		}
		return writeMigrationSiteIdentity(siteIdentityPath, panelURL, apiKey)
	}, func() error { return os.Remove(siteIdentityPath) }); err != nil {
		return s.handleCreationError(migrationSiteID, err)
	}
	if info, err := os.Stat(siteIdentityPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return s.handleCreationError(migrationSiteID, errors.New("target site identity unavailable"))
	}
	return nil
}

func (s *SiteMigrationTargetResourceService) prepareCreationRetry(ctx context.Context, taskID string, spec SiteMigrationTargetSpec) (string, error) {
	var status, stage, raw string
	if err := s.db.QueryRowContext(ctx, `SELECT status,stage,settings_snapshot FROM site_migration_sites WHERE id=?`, taskID).Scan(&status, &stage, &raw); err != nil {
		return "", errors.New("target resource creation state unavailable")
	}
	if stage == "preparing_target" {
		return stage, nil
	}
	if stage != "publishing" || status != "failed_retryable" {
		return "", errors.New("target resource creation is not retryable")
	}
	var snapshot struct {
		TargetSpec siteMigrationPublishSpec `json:"target_spec"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil || snapshot.TargetSpec.Domain != spec.Domain || snapshot.TargetSpec.SiteType != spec.SiteType || snapshot.TargetSpec.DocumentRootSubdir != spec.DocumentRootSubdir || strings.Join(snapshot.TargetSpec.Aliases, "\n") != strings.Join(spec.Aliases, "\n") {
		return "", errors.New("target resource retry specification changed")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',error_code='',updated_at=? WHERE id=? AND status='failed_retryable' AND stage='publishing'`, s.now().UTC(), taskID)
	if err != nil {
		return "", err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return "", errors.New("target resource retry state changed")
	}
	return stage, nil
}

func (s *SiteMigrationTargetResourceService) beginCreation(ctx context.Context, migrationSiteID string, spec SiteMigrationTargetSpec, plan siteMigrationTargetPlan) error {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT settings_snapshot FROM site_migration_sites WHERE id=? AND stage='preparing_target'`, migrationSiteID).Scan(&raw); err != nil {
		return errors.New("target resource creation state changed")
	}
	snapshot := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return fmt.Errorf("decode target settings snapshot: %w", err)
		}
	}
	normalizedAliases := make([]string, 0, len(spec.Aliases))
	for _, alias := range spec.Aliases {
		normalizedAliases = append(normalizedAliases, strings.ToLower(strings.TrimSpace(alias)))
	}
	snapshot["target_spec"] = map[string]any{
		"domain": spec.Domain, "aliases": normalizedAliases, "site_type": spec.SiteType,
		"document_root_subdir": spec.DocumentRootSubdir, "system_user": plan.SystemUser,
		"web_root": plan.WebRoot, "log_dir": plan.LogDir, "db_name": plan.DBName, "db_user": plan.DBUser,
		"php_pool_path": plan.PHPPoolPath, "nginx_conf_path": plan.NginxConfPath,
		"nginx_enabled_path": plan.NginxEnabledPath, "php_socket_path": plan.PHPSockPath,
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',stage='publishing',settings_snapshot=?,updated_at=? WHERE id=? AND stage='preparing_target'`, string(encoded), s.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("target resource creation state changed")
	}
	return nil
}

func (s *SiteMigrationTargetResourceService) plan(ctx context.Context, migrationSiteID string, spec SiteMigrationTargetSpec, stage string) (siteMigrationTargetPlan, error) {
	if !validSiteMigrationID(migrationSiteID) || !IsValidDomain(spec.Domain) || spec.SiteType != "wordpress" && spec.SiteType != "php" {
		return siteMigrationTargetPlan{}, errors.New("invalid target resource specification")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='target' AND mb.status='active'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='target' AND ml.status='active'
		WHERE ms.id=? AND ms.target_domain=? AND ms.site_type=? AND ms.stage=? AND (?='preparing_target' OR ms.status='running')`, migrationSiteID, spec.Domain, spec.SiteType, stage, stage).Scan(&count)
	if err != nil || count != 1 {
		return siteMigrationTargetPlan{}, errors.New("target resource scope unavailable")
	}
	wantStagingRoot := filepath.Join(s.stagingRoot, migrationSiteID)
	var stagingRoot, ownership string
	if err := s.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag FROM site_migration_resources
		WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, migrationSiteID).Scan(&stagingRoot, &ownership); err != nil || filepath.Clean(stagingRoot) != wantStagingRoot || ownership != migrationSiteID {
		return siteMigrationTargetPlan{}, errors.New("target staging ownership unavailable")
	}
	siteName := buildSiteName(spec.Domain)
	systemUser := "wp_" + siteName
	if spec.SiteType == "php" {
		systemUser = "php_" + siteName
	}
	configBase := siteConfigBaseName(siteName)
	plan := siteMigrationTargetPlan{
		Domain: spec.Domain, SystemUser: systemUser,
		WebRoot: filepath.Join(s.cfg.Paths.WWWRoot, spec.Domain), LogDir: filepath.Join(s.cfg.Paths.WWWLogs, spec.Domain),
		DBName: "db_" + siteName, DBUser: "user_" + siteName,
		PHPPoolPath: filepath.Join(s.cfg.Paths.PHPFPMPool, configBase+".conf"), NginxConfPath: filepath.Join(s.cfg.Paths.NginxSitesAvailable, configBase+".conf"),
		NginxEnabledPath: filepath.Join(s.cfg.Paths.NginxSitesEnabled, configBase+".conf"), PHPSockPath: filepath.Join(s.cfg.Paths.PHPFPMSock, configBase+".sock"),
	}
	if err := validateUnixSocketPath(plan.PHPSockPath); err != nil {
		return siteMigrationTargetPlan{}, err
	}
	return plan, nil
}

func normalizeSiteMigrationTargetSpec(spec SiteMigrationTargetSpec) (SiteMigrationTargetSpec, error) {
	spec.Domain = strings.ToLower(strings.TrimSpace(spec.Domain))
	if !IsValidDomain(spec.Domain) || spec.SiteType != "wordpress" && spec.SiteType != "php" {
		return SiteMigrationTargetSpec{}, errors.New("invalid target resource specification")
	}
	normalizedSubdir, err := NormalizeDocumentRootSubdir(spec.SiteType, spec.DocumentRootSubdir)
	if err != nil {
		return SiteMigrationTargetSpec{}, err
	}
	spec.DocumentRootSubdir = normalizedSubdir
	seen := map[string]struct{}{spec.Domain: {}}
	for i, alias := range spec.Aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if !IsValidDomain(alias) {
			return SiteMigrationTargetSpec{}, errors.New("invalid target alias")
		}
		if _, exists := seen[alias]; exists {
			return SiteMigrationTargetSpec{}, errors.New("duplicate target domain or alias")
		}
		seen[alias] = struct{}{}
		spec.Aliases[i] = alias
	}
	return spec, nil
}

func (s *SiteMigrationTargetResourceService) createAndRecord(ctx context.Context, taskID, kind, identifier string, create, undo func() error) error {
	status, err := s.ensureResourceIntent(ctx, taskID, kind, identifier)
	if err != nil {
		return err
	}
	if status == "published" {
		return nil
	}
	if err := create(); err != nil {
		if undoErr := undo(); undoErr != nil {
			return errors.Join(errSiteMigrationResourceUnknown, fmt.Errorf("create %s: %w", kind, err), fmt.Errorf("undo failed %s: %w", kind, undoErr))
		}
		_, markErr := s.db.ExecContext(ctx, `UPDATE site_migration_resources SET status='removed',updated_at=? WHERE migration_site_id=? AND resource_type=? AND identifier=? AND status='created'`, s.now().UTC(), taskID, kind, identifier)
		if markErr != nil {
			return errors.Join(fmt.Errorf("create %s: %w", kind, err), markErr)
		}
		return fmt.Errorf("create %s: %w", kind, err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_resources SET status='published',updated_at=? WHERE migration_site_id=? AND resource_type=? AND identifier=? AND ownership_tag=? AND status='created'`, s.now().UTC(), taskID, kind, identifier, taskID)
	if err != nil {
		return errors.Join(errSiteMigrationResourceUnknown, err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.Join(errSiteMigrationResourceUnknown, errors.New("target resource completion state changed"))
	}
	return nil
}

var errSiteMigrationResourceUnknown = errors.New("target resource result unknown")

func (s *SiteMigrationTargetResourceService) ensureResourceIntent(ctx context.Context, taskID, kind, identifier string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var owner, status string
	err = tx.QueryRowContext(ctx, `SELECT ownership_tag,status FROM site_migration_resources WHERE migration_site_id=? AND resource_type=? AND identifier=?`, taskID, kind, identifier).Scan(&owner, &status)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_resources (migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at) VALUES (?,?,?,?,'created',?,?)`, taskID, kind, identifier, taskID, s.now().UTC(), s.now().UTC()); err != nil {
			return "", err
		}
		status = "created"
	} else if err != nil {
		return "", err
	} else if owner != taskID {
		return "", errors.New("target resource intent ownership changed")
	} else if status == "removed" {
		result, err := tx.ExecContext(ctx, `UPDATE site_migration_resources SET status='created',updated_at=? WHERE migration_site_id=? AND resource_type=? AND identifier=? AND ownership_tag=? AND status='removed'`, s.now().UTC(), taskID, kind, identifier, taskID)
		if err != nil {
			return "", err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return "", errors.New("target resource retry state changed")
		}
		status = "created"
	} else if status == "created" || status == "remove_failed" {
		return "", errors.Join(errSiteMigrationResourceUnknown, errors.New("target resource intent unresolved"))
	} else if status != "published" {
		return "", errors.New("target resource intent state invalid")
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return status, nil
}

func (s *SiteMigrationTargetResourceService) creationFailed(migrationSiteID string, cause error) error {
	_, _ = s.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',error_code='target_resource_creation_failed',updated_at=? WHERE id=? AND stage='publishing'`, s.now().UTC(), migrationSiteID)
	return fmt.Errorf("target resource creation failed: %w", cause)
}

func (s *SiteMigrationTargetResourceService) handleCreationError(taskID string, cause error) error {
	if errors.Is(cause, errSiteMigrationResourceUnknown) {
		_, _ = s.db.Exec(`UPDATE site_migration_sites SET status='interrupted_unknown',stage='interrupted_unknown',error_code='target_resource_commit_unknown',updated_at=? WHERE id=?`, s.now().UTC(), taskID)
		return fmt.Errorf("target resource creation state unknown: %w", cause)
	}
	return s.creationFailed(taskID, cause)
}

func readMigrationDatabaseIdentity(path, name, dbUser string) (siteMigrationDatabaseIdentity, bool, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return siteMigrationDatabaseIdentity{}, false, nil
	}
	if err != nil {
		return siteMigrationDatabaseIdentity{}, false, err
	}
	var identity siteMigrationDatabaseIdentity
	if json.Unmarshal(content, &identity) != nil || identity.Database != name || identity.User != dbUser || len(identity.Password) < 24 {
		return siteMigrationDatabaseIdentity{}, false, errors.New("target database identity invalid")
	}
	return identity, true, nil
}

func writeMigrationDatabaseIdentity(path, name, user, password string) error {
	if !isValidMySQLIdentifier(name) || !isValidMySQLIdentifier(user) || len(password) < 24 {
		return errors.New("invalid migration database identity")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	content, err := json.Marshal(map[string]string{"database": name, "user": user, "password": password})
	if err != nil {
		return err
	}
	return atomicWriteMigrationFile(path, content, 0600)
}

func writeMigrationSiteIdentity(path, panelURL, apiKey string) error {
	if !strings.HasPrefix(panelURL, "https://127.0.0.1:") || len(apiKey) < 32 {
		return errors.New("invalid migration site identity")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	content, err := json.Marshal(map[string]string{"panel_url": panelURL, "api_key": apiKey})
	if err != nil {
		return err
	}
	return atomicWriteMigrationFile(path, content, 0600)
}
