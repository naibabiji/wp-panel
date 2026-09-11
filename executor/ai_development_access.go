package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const aiDevelopmentHomeRoot = "/var/lib/wp-panel/ai-homes"

const (
	aiDevelopmentUsermodRetryDelay = 200 * time.Millisecond
	aiDevelopmentUsermodAttempts   = 76
)

var aiDevelopmentUserPattern = regexp.MustCompile(`^(wp|php)_[a-z0-9_]{1,28}$`)
var aiDevelopmentSessionPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ErrAIDevelopmentSiteBusy means usermod could not change the site user because
// its PHP-FPM workers never had a fully idle moment during the retry window. The
// caller may retry with force to terminate those workers before retrying.
var ErrAIDevelopmentSiteBusy = errors.New("site has active PHP processes")

type AIDevelopmentSite struct {
	ID         int64
	Domain     string
	SystemUser string
	WebRoot    string
	DBName     string
	DBUser     string
}

type aiDevelopmentPasswd struct {
	Home  string
	Shell string
	UID   int
	GID   int
}

type aiDevelopmentSystem interface {
	LookupPasswd(context.Context, string) (aiDevelopmentPasswd, error)
	Configure(ctx context.Context, site AIDevelopmentSite, publicKey, fingerprint string, force bool) error
	UpdateHandoff(context.Context, AIDevelopmentSite, string) error
	RevokeKey(context.Context, string) error
	InstallKey(context.Context, string, string) error
	TerminateSessions(context.Context, string) error
	Restore(context.Context, string, aiDevelopmentPasswd) error
	RemoveHome(string) error
}

type productionAIDevelopmentSystem struct{}

type AIDevelopmentAccessService struct {
	db     *sql.DB
	system aiDevelopmentSystem
	verify func(context.Context, AIDevelopmentSite) error
}

func NewAIDevelopmentAccessService(db *sql.DB) *AIDevelopmentAccessService {
	return &AIDevelopmentAccessService{
		db:     db,
		system: productionAIDevelopmentSystem{},
		verify: VerifyAIDevelopmentDatabaseIsolation,
	}
}

// ReconcilePending fails closed after a panel interruption. An enabling record
// cannot have delivered its private key yet, so it is safely rolled back; a
// disabling record resumes the same idempotent shutdown path.
func (s *AIDevelopmentAccessService) ReconcilePending(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT site_id FROM website_ai_development_access WHERE status IN ('enabling','disabling') ORDER BY site_id`)
	if err != nil {
		return err
	}
	var siteIDs []int64
	for rows.Next() {
		var siteID int64
		if err := rows.Scan(&siteID); err != nil {
			rows.Close()
			return err
		}
		siteIDs = append(siteIDs, siteID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, siteID := range siteIDs {
		if err := s.disable(ctx, siteID, true); err != nil {
			return fmt.Errorf("reconcile AI development access for site %d: %w", siteID, err)
		}
	}
	return nil
}

func (s *AIDevelopmentAccessService) Enable(ctx context.Context, site AIDevelopmentSite, publicKey, fingerprint, requestedBy string, force bool) error {
	if !TryAcquireSiteOpLock(int(site.ID), "ai_development") {
		return ErrMaintenanceBusy
	}
	defer ReleaseSiteOpLock(int(site.ID))
	if err := validateAIDevelopmentSite(site); err != nil {
		return err
	}
	if strings.TrimSpace(publicKey) == "" || strings.TrimSpace(fingerprint) == "" {
		return errors.New("SSH key is incomplete")
	}
	if _, err := database.GetAIDevelopmentAccess(ctx, s.db, site.ID); err == nil {
		return errors.New("AI development access already exists")
	} else if !errors.Is(err, database.ErrAIDevelopmentAccessNotFound) {
		return err
	}
	passwd, err := s.system.LookupPasswd(ctx, site.SystemUser)
	if err != nil {
		return fmt.Errorf("inspect site user: %w", err)
	}
	if passwd.Shell != "/usr/sbin/nologin" && passwd.Shell != "/bin/false" {
		return errors.New("site user already has an interactive shell")
	}
	if err := s.verify(ctx, site); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint,requested_by)
		VALUES (?,'enabling',?,?,?,?,?,?,?)`, site.ID, site.SystemUser, site.WebRoot, passwd.Shell, passwd.Home,
		strings.TrimSpace(publicKey), fingerprint, strings.TrimSpace(requestedBy)); err != nil {
		return err
	}
	if err := s.system.Configure(ctx, site, publicKey, fingerprint, force); err != nil {
		if rollbackErr := s.system.Restore(ctx, site.SystemUser, passwd); rollbackErr == nil {
			_ = s.system.RemoveHome(aiDevelopmentHome(site.SystemUser))
			_, _ = s.db.ExecContext(ctx, `DELETE FROM website_ai_development_access WHERE site_id=?`, site.ID)
		} else {
			_, _ = s.db.ExecContext(ctx, `UPDATE website_ai_development_access SET status='error',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE site_id=?`, safeAIDevelopmentError(err), site.ID)
		}
		return fmt.Errorf("configure AI development access: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE website_ai_development_access SET status='enabled',operation='',last_error='',enabled_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE site_id=? AND status='enabling'`, site.ID)
	if err != nil {
		return err
	}
	return requireAIDevelopmentStateChange(result, "AI development access changed while enabling")
}

func (s *AIDevelopmentAccessService) Rotate(ctx context.Context, site AIDevelopmentSite, publicKey, fingerprint string) error {
	siteID := site.ID
	item, err := database.GetAIDevelopmentAccess(ctx, s.db, siteID)
	if err != nil {
		return err
	}
	if item.Status != "enabled" && item.Status != "error" {
		return errors.New("AI development access is busy")
	}
	if site.Domain == "" || site.SystemUser != item.SystemUser || site.WebRoot != item.WebRoot {
		return errors.New("AI development site identity changed")
	}
	claim, err := s.db.ExecContext(ctx, `UPDATE website_ai_development_access
		SET status='enabling',operation='rotate',last_error='',updated_at=CURRENT_TIMESTAMP
		WHERE site_id=? AND ((status='enabled' AND operation='') OR status='error')`, siteID)
	if err != nil {
		return err
	}
	if err := requireAIDevelopmentStateChange(claim, "AI development access is busy"); err != nil {
		return err
	}
	failed := func(cause error) error {
		_, _ = s.db.ExecContext(ctx, `UPDATE website_ai_development_access SET status='error',operation='rotate',public_key='',key_fingerprint='',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE site_id=?`, safeAIDevelopmentError(cause), siteID)
		return cause
	}
	if err := s.system.RevokeKey(ctx, item.SystemUser); err != nil {
		return failed(fmt.Errorf("revoke old SSH key: %w", err))
	}
	if err := s.system.TerminateSessions(ctx, item.SystemUser); err != nil {
		return failed(fmt.Errorf("terminate old SSH sessions: %w", err))
	}
	if err := s.system.UpdateHandoff(ctx, site, fingerprint); err != nil {
		return failed(fmt.Errorf("update AI handoff: %w", err))
	}
	if err := s.system.InstallKey(ctx, item.SystemUser, publicKey); err != nil {
		return failed(fmt.Errorf("install new SSH key: %w", err))
	}
	result, err := s.db.ExecContext(ctx, `UPDATE website_ai_development_access SET status='enabled',operation='',public_key=?,key_fingerprint=?,last_error='',updated_at=CURRENT_TIMESTAMP WHERE site_id=? AND status='enabling' AND operation='rotate'`, strings.TrimSpace(publicKey), fingerprint, siteID)
	if err != nil {
		return err
	}
	return requireAIDevelopmentStateChange(result, "AI development access changed during rotation")
}

func (s *AIDevelopmentAccessService) Disable(ctx context.Context, siteID int64) error {
	return s.disable(ctx, siteID, false)
}

func (s *AIDevelopmentAccessService) disable(ctx context.Context, siteID int64, resumePending bool) error {
	item, err := database.GetAIDevelopmentAccess(ctx, s.db, siteID)
	if err != nil {
		return err
	}
	condition := `((status='enabled' AND operation='') OR (status='enabling' AND operation='') OR status='error')`
	if resumePending {
		condition = `status IN ('enabling','disabling')`
	}
	claim, err := s.db.ExecContext(ctx, `UPDATE website_ai_development_access
		SET status='disabling',operation='',updated_at=CURRENT_TIMESTAMP
		WHERE site_id=? AND `+condition, siteID)
	if err != nil {
		return err
	}
	if err := requireAIDevelopmentStateChange(claim, "AI development access is busy"); err != nil {
		return err
	}
	fail := func(cause error) error {
		_, _ = s.db.ExecContext(ctx, `UPDATE website_ai_development_access SET status='error',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE site_id=?`, safeAIDevelopmentError(cause), siteID)
		return cause
	}
	if err := s.system.RevokeKey(ctx, item.SystemUser); err != nil {
		return fail(fmt.Errorf("revoke SSH key: %w", err))
	}
	if err := s.system.TerminateSessions(ctx, item.SystemUser); err != nil {
		return fail(fmt.Errorf("terminate SSH sessions: %w", err))
	}
	if err := s.system.Restore(ctx, item.SystemUser, aiDevelopmentPasswd{Home: item.OriginalHome, Shell: item.OriginalShell}); err != nil {
		return fail(fmt.Errorf("restore site user: %w", err))
	}
	if err := s.system.RemoveHome(aiDevelopmentHome(item.SystemUser)); err != nil {
		return fail(fmt.Errorf("remove AI home: %w", err))
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM website_ai_development_access WHERE site_id=?`, siteID)
	return err
}

func requireAIDevelopmentStateChange(result sql.Result, message string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New(message)
	}
	return nil
}

func validateAIDevelopmentSite(site AIDevelopmentSite) error {
	if site.ID <= 0 || !aiDevelopmentUserPattern.MatchString(site.SystemUser) || !isValidMySQLIdentifier(site.DBName) || !isValidMySQLIdentifier(site.DBUser) {
		return errors.New("invalid AI development site identity")
	}
	root, err := safeSiteWebRoot(site.WebRoot)
	if err != nil || root != filepath.Clean(site.WebRoot) {
		return errors.New("invalid AI development web root")
	}
	return nil
}

func safeAIDevelopmentError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}

func aiDevelopmentHome(systemUser string) string {
	return filepath.Join(aiDevelopmentHomeRoot, systemUser)
}

func (productionAIDevelopmentSystem) LookupPasswd(ctx context.Context, name string) (aiDevelopmentPasswd, error) {
	if !aiDevelopmentUserPattern.MatchString(name) {
		return aiDevelopmentPasswd{}, errors.New("invalid site user")
	}
	out, err := exec.CommandContext(ctx, "getent", "passwd", name).Output()
	if err != nil {
		return aiDevelopmentPasswd{}, err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(parts) != 7 || parts[0] != name || !filepath.IsAbs(parts[5]) || !filepath.IsAbs(parts[6]) {
		return aiDevelopmentPasswd{}, errors.New("invalid passwd entry")
	}
	uid, uidErr := strconv.Atoi(parts[2])
	gid, gidErr := strconv.Atoi(parts[3])
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 {
		return aiDevelopmentPasswd{}, errors.New("invalid passwd uid or gid")
	}
	return aiDevelopmentPasswd{Home: parts[5], Shell: parts[6], UID: uid, GID: gid}, nil
}

func (p productionAIDevelopmentSystem) Configure(ctx context.Context, site AIDevelopmentSite, publicKey, fingerprint string, force bool) error {
	home := aiDevelopmentHome(site.SystemUser)
	passwd, err := p.LookupPasswd(ctx, site.SystemUser)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0755); err != nil {
		return err
	}
	if err := ensureAIDevelopmentHomeTraversal(); err != nil {
		return err
	}
	for _, path := range []string{home, filepath.Join(home, ".ssh")} {
		if err := os.Chown(path, 0, 0); err != nil {
			return err
		}
	}
	if err := os.Chmod(home, 0755); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(home, ".ssh"), 0755); err != nil {
		return err
	}
	// Remote-SSH clients need writable per-user state, while the home itself,
	// its profile and authorized_keys remain root-controlled.
	for _, name := range []string{".cache", ".npm", ".vscode-server", ".cursor-server"} {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err := os.Chown(path, passwd.UID, passwd.GID); err != nil {
			return err
		}
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	if err := p.InstallKey(ctx, site.SystemUser, publicKey); err != nil {
		return err
	}
	profile := "# Managed by WP Panel AI development access.\n" +
		"if [ -d " + shellSingleQuote(site.WebRoot) + " ]; then\n  cd -- " + shellSingleQuote(site.WebRoot) + "\nfi\n" +
		"printf '%s\\n' 'WP Panel AI development access: " + site.Domain + "' 'Instructions: ~/WP-PANEL-AI-HANDOFF.md'\n"
	if err := writeAIDevelopmentFile(filepath.Join(home, ".profile"), []byte(profile), 0444); err != nil {
		return err
	}
	if err := p.UpdateHandoff(ctx, site, fingerprint); err != nil {
		return err
	}
	if force {
		if err := p.terminateSitePHPProcesses(ctx, site.SystemUser); err != nil {
			return err
		}
	}
	if err := retryAIDevelopmentUsermod(ctx, aiDevelopmentUsermodRetryDelay, aiDevelopmentUsermodAttempts, func() error {
		if force {
			if err := p.killSitePHPProcesses(ctx, site.SystemUser); err != nil {
				return err
			}
		}
		return exec.CommandContext(ctx, "usermod", "-d", home, site.SystemUser).Run()
	}); err != nil {
		return wrapAIDevelopmentUsermodBusy(err)
	}
	if err := retryAIDevelopmentUsermod(ctx, aiDevelopmentUsermodRetryDelay, aiDevelopmentUsermodAttempts, func() error {
		if force {
			if err := p.killSitePHPProcesses(ctx, site.SystemUser); err != nil {
				return err
			}
		}
		return exec.CommandContext(ctx, "usermod", "-s", "/bin/bash", site.SystemUser).Run()
	}); err != nil {
		return wrapAIDevelopmentUsermodBusy(err)
	}
	return nil
}

// killSitePHPProcesses closes the race where PHP-FPM immediately replaces a
// gracefully terminated worker while forced usermod is waiting to run.
func (productionAIDevelopmentSystem) killSitePHPProcesses(ctx context.Context, systemUser string) error {
	if !aiDevelopmentUserPattern.MatchString(systemUser) {
		return errors.New("invalid site user")
	}
	err := exec.CommandContext(ctx, "pkill", aiDevelopmentPHPKillArgs("KILL", systemUser)...).Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil
	}
	return fmt.Errorf("force terminate site PHP processes: %w", err)
}

// terminateSitePHPProcesses ends the site's own PHP-FPM workers so a following
// usermod attempt does not have to wait for them to go idle on their own. Each
// site runs under its own dedicated system user with its own PHP-FPM pool, so
// this only interrupts in-flight requests for this one site; the pool is
// pm=ondemand and simply forks a fresh worker on the next request.
func (productionAIDevelopmentSystem) terminateSitePHPProcesses(ctx context.Context, systemUser string) error {
	if !aiDevelopmentUserPattern.MatchString(systemUser) {
		return errors.New("invalid site user")
	}
	err := exec.CommandContext(ctx, "pkill", aiDevelopmentPHPKillArgs("TERM", systemUser)...).Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil
	}
	return fmt.Errorf("terminate site PHP processes: %w", err)
}

func aiDevelopmentPHPKillArgs(signal, systemUser string) []string {
	return []string{"-" + signal, "-u", systemUser, "-f", `^php-fpm: pool `}
}

func wrapAIDevelopmentUsermodBusy(err error) error {
	if isAIDevelopmentUsermodBusy(err) {
		return fmt.Errorf("%w: %v", ErrAIDevelopmentSiteBusy, err)
	}
	return err
}

func (productionAIDevelopmentSystem) UpdateHandoff(_ context.Context, site AIDevelopmentSite, fingerprint string) error {
	handoff := buildAIDevelopmentHandoff(site, fingerprint)
	return writeAIDevelopmentFile(filepath.Join(aiDevelopmentHome(site.SystemUser), "WP-PANEL-AI-HANDOFF.md"), []byte(handoff), 0444)
}

func (productionAIDevelopmentSystem) InstallKey(_ context.Context, systemUser, publicKey string) error {
	if !aiDevelopmentUserPattern.MatchString(systemUser) {
		return errors.New("invalid site user")
	}
	key := strings.TrimSpace(publicKey)
	if strings.ContainsAny(key, "\r\n") || !strings.HasPrefix(key, "ssh-ed25519 ") {
		return errors.New("invalid SSH public key")
	}
	if err := ensureAIDevelopmentHomeTraversal(); err != nil {
		return err
	}
	sshDirectory := filepath.Join(aiDevelopmentHome(systemUser), ".ssh")
	if err := os.MkdirAll(sshDirectory, 0755); err != nil {
		return err
	}
	if err := os.Chown(sshDirectory, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(sshDirectory, 0755); err != nil {
		return err
	}
	content := []byte("restrict,pty " + key + "\n")
	return writeAIDevelopmentFile(filepath.Join(sshDirectory, "authorized_keys"), content, 0644)
}

func ensureAIDevelopmentHomeTraversal() error {
	if err := os.MkdirAll(aiDevelopmentHomeRoot, 0711); err != nil {
		return err
	}
	for _, path := range []string{filepath.Dir(aiDevelopmentHomeRoot), aiDevelopmentHomeRoot} {
		if err := os.Chown(path, 0, 0); err != nil {
			return err
		}
		if err := os.Chmod(path, 0711); err != nil {
			return err
		}
	}
	return nil
}

func (productionAIDevelopmentSystem) RevokeKey(_ context.Context, systemUser string) error {
	if !aiDevelopmentUserPattern.MatchString(systemUser) {
		return errors.New("invalid site user")
	}
	err := os.Remove(filepath.Join(aiDevelopmentHome(systemUser), ".ssh", "authorized_keys"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (productionAIDevelopmentSystem) TerminateSessions(ctx context.Context, systemUser string) error {
	if !aiDevelopmentUserPattern.MatchString(systemUser) {
		return errors.New("invalid site user")
	}
	out, err := exec.CommandContext(ctx, "loginctl", "list-sessions", "--no-legend", "--no-pager").Output()
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[2] != systemUser {
			continue
		}
		sessionID := fields[0]
		if !aiDevelopmentSessionPattern.MatchString(sessionID) {
			return errors.New("invalid login session identifier")
		}
		if err := exec.CommandContext(ctx, "loginctl", "terminate-session", sessionID).Run(); err != nil {
			return err
		}
	}
	return nil
}

func (productionAIDevelopmentSystem) Restore(ctx context.Context, systemUser string, passwd aiDevelopmentPasswd) error {
	if !aiDevelopmentUserPattern.MatchString(systemUser) || !filepath.IsAbs(passwd.Home) || !filepath.IsAbs(passwd.Shell) {
		return errors.New("invalid site user restore metadata")
	}
	restore := func(run func() error) error {
		return retryAIDevelopmentUsermodAfterTerminate(ctx, aiDevelopmentUsermodRetryDelay, aiDevelopmentUsermodAttempts, func() error {
			return productionAIDevelopmentSystem{}.terminateSitePHPProcesses(ctx, systemUser)
		}, run)
	}
	if err := restore(func() error {
		return exec.CommandContext(ctx, "usermod", "-s", passwd.Shell, systemUser).Run()
	}); err != nil {
		return err
	}
	return restore(func() error {
		return exec.CommandContext(ctx, "usermod", "-d", passwd.Home, systemUser).Run()
	})
}

func retryAIDevelopmentUsermodAfterTerminate(ctx context.Context, delay time.Duration, attempts int, terminate, run func() error) error {
	terminateBeforeRun := false
	return retryAIDevelopmentUsermod(ctx, delay, attempts, func() error {
		if terminateBeforeRun {
			if err := terminate(); err != nil {
				return err
			}
			terminateBeforeRun = false
		}
		err := run()
		terminateBeforeRun = isAIDevelopmentUsermodBusy(err)
		return err
	})
}

func retryAIDevelopmentUsermod(ctx context.Context, delay time.Duration, attempts int, run func() error) error {
	for attempt := 1; attempt <= attempts; attempt++ {
		err := run()
		if err == nil || !isAIDevelopmentUsermodBusy(err) || attempt == attempts {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func isAIDevelopmentUsermodBusy(err error) bool {
	var exitErr interface{ ExitCode() int }
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 8
}

func (productionAIDevelopmentSystem) RemoveHome(path string) error {
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(aiDevelopmentHomeRoot, clean)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, filepath.Separator) || !aiDevelopmentUserPattern.MatchString(rel) {
		return errors.New("invalid AI home path")
	}
	return os.RemoveAll(clean)
}

func writeAIDevelopmentFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".wp-panel-ai-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chown(0, 0); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func buildAIDevelopmentHandoff(site AIDevelopmentSite, fingerprint string) string {
	return fmt.Sprintf(`# WP Panel AI Development Handoff

Site: %s
WebRoot: %s
System user: %s
SSH key fingerprint: %s

You have full control of this site's files and its WordPress database. You do not have sudo access and must not attempt to modify WP Panel, system services, or other sites.

Before any change, follow the local control project's AGENTS.md workflow. Perform read-only discovery first and create AI-CONTEXT.md in the local control-project root, not in this WebRoot. Inspect WordPress/PHP/tool versions, themes and child themes, plugins, custom code, multisite, WooCommerce integration indicators, Git state, build tools and relevant logs. Do not expose secrets or copy full configuration files into local documents.

Ask the user to confirm staging versus production, a recent restorable backup, the desired outcome and acceptance criteria, protected business flows, permission for test data or temporary accounts, third-party sandbox constraints and any Git workflow. Then create DEVELOPMENT-PLAN.md locally and wait for explicit approval before modifying files or the database. During approved work maintain AI-CHANGELOG.md locally.

Do not automatically create backups, WordPress users, orders or other test data. Do not ask for root, WP Panel or database passwords. Use WP-CLI for normal inspection; request a temporary WordPress administrator only when browser-admin testing is necessary, explain why, obtain permission and never record the password in project documents.

Start every WordPress command with an explicit target:

    wp --path=%s <command>

If WP-CLI or Node.js/npm is unavailable, ask the administrator to open WP Panel -> Software -> Development Tools. Do not ask for the root password and do not attempt system package installation.

When development is complete, report changed files, database/test-data changes, and verification results. Ask the administrator to review the site and disable AI development access.
`, site.Domain, site.WebRoot, site.SystemUser, fingerprint, site.WebRoot)
}

func VerifyAIDevelopmentDatabaseIsolation(ctx context.Context, site AIDevelopmentSite) error {
	cfg := config.AppConfig
	if cfg == nil {
		return errors.New("panel configuration unavailable")
	}
	args := []string{"--batch", "--skip-column-names", "-u", cfg.MariaDB.RootUser}
	query := "SHOW GRANTS FOR '" + strings.ReplaceAll(site.DBUser, "'", "''") + "'@'localhost'"
	args = append(args, "-e", query)
	cmd := exec.CommandContext(ctx, "mariadb", args...)
	cmd.Env = os.Environ()
	if cfg.MariaDB.RootPassword != "" {
		cmd.Env = append(cmd.Env, "MYSQL_PWD="+cfg.MariaDB.RootPassword)
	}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("inspect database grants: %w", err)
	}
	grants := strings.TrimSpace(string(out))
	if grants == "" {
		return errors.New("database grants are empty")
	}
	upper := strings.ToUpper(grants)
	normalized := regexp.MustCompile(`[^A-Z0-9_*]+`).ReplaceAllString(upper, " ")
	forbidden := []string{"FILE", "SUPER", "PROCESS", "SHUTDOWN", "RELOAD", "CREATE USER", "GRANT OPTION"}
	for _, token := range forbidden {
		if strings.Contains(" "+normalized+" ", " "+token+" ") {
			return fmt.Errorf("database user has forbidden privilege: %s", token)
		}
	}
	allowedDBTarget := "ON `" + strings.ToUpper(strings.ReplaceAll(site.DBName, "`", "``")) + "`.*"
	for _, line := range strings.Split(upper, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "GRANT USAGE ON *.*") || strings.Contains(line, allowedDBTarget) {
			continue
		}
		// A GRANT without an ON clause is a role assignment. Roles can inherit
		// privileges outside this site's database, so fail closed.
		if strings.HasPrefix(line, "GRANT ") && !strings.Contains(line, " ON ") {
			return errors.New("database user inherits a role")
		}
		return errors.New("database user has grants outside its site database")
	}
	return nil
}

func DetectSSHPort(ctx context.Context) int {
	for _, binary := range []string{"/usr/sbin/sshd", "sshd"} {
		out, err := exec.CommandContext(ctx, binary, "-T").Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || strings.ToLower(fields[0]) != "port" {
				continue
			}
			port, err := strconv.Atoi(fields[1])
			if err == nil && port > 0 && port <= 65535 {
				return port
			}
		}
	}
	return 22
}
