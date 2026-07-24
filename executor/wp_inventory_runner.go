package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

const (
	wpInventoryProtocol               = "wp-panel-inventory"
	wpInventoryRunnerVersion          = "1"
	wpInventorySchemaVersion          = 1
	wpInventoryRunnerRoot             = "/var/wp-panel/runners/wp-inventory"
	wpInventoryPHPPath                = "/usr/bin/php8.3"
	wpInventoryRunuserPath            = "/usr/sbin/runuser"
	wpInventoryLockWait               = 10 * time.Second
	wpInventoryExecutionTimeout       = 5 * time.Second
	// wpInventoryForceUpdateTimeout bounds a scan that also triggers a live
	// WordPress update check (core/plugins/themes API calls). The check makes
	// several outbound requests to api.wordpress.org, so it needs far more
	// headroom than the read-only scan.
	wpInventoryForceUpdateTimeout = 60 * time.Second
	// wpInventoryRunnerMemoryLimit bounds the read-only inventory scan's PHP
	// process (WordPress bootstrap + plugin/theme introspection), not real
	// site traffic. 64M turned out to be too tight for real-world sites with
	// a normal plugin load (WooCommerce, page builders, SEO/cache plugins
	// routinely need well over that just to bootstrap) — it's still a hard,
	// enforced ceiling, just a more realistic one.
	wpInventoryRunnerMemoryLimit = "256M"
	wpInventoryStreamLimit      int64 = 64 * 1024
	wpInventoryProtocolLimit    int64 = 1024 * 1024
	wpInventorySourceLimit            = 256 * 1024
	wpInventoryPluginLimit            = 2000
	wpInventoryThemeLimit             = 1000
	wpInventoryUpdateLimit            = 3000
	wpInventoryNameLimit              = 512
	wpInventoryVersionLimit           = 128
)

var (
	wpInventoryUserPattern = regexp.MustCompile(`^wp_[a-z0-9_]{1,27}$`)
	wpInventoryHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	wpInventorySlot        = make(chan struct{}, 1)
)

//go:embed assets/wp-inventory-runner/inventory.php
var wpInventoryRunnerSource []byte

type WPInventoryErrorCode string

const (
	WPInventoryUnsupportedPlatform      WPInventoryErrorCode = "unsupported_platform"
	WPInventoryInvalidSite              WPInventoryErrorCode = "invalid_site"
	WPInventoryInvalidSitePath          WPInventoryErrorCode = "invalid_site_path"
	WPInventorySiteUserUnavailable      WPInventoryErrorCode = "site_user_unavailable"
	WPInventoryInsufficientPrivileges   WPInventoryErrorCode = "insufficient_privileges"
	WPInventoryPHPCLIUnavailable        WPInventoryErrorCode = "php_cli_unavailable"
	WPInventoryRunuserUnavailable       WPInventoryErrorCode = "runuser_unavailable"
	WPInventoryRunnerPrepareFailed      WPInventoryErrorCode = "runner_prepare_failed"
	WPInventoryRunnerIntegrityFailed    WPInventoryErrorCode = "runner_integrity_failed"
	WPInventoryRunnerLockFailed         WPInventoryErrorCode = "runner_lock_failed"
	WPInventoryRunnerCanceled           WPInventoryErrorCode = "runner_canceled"
	WPInventoryRunnerStartFailed        WPInventoryErrorCode = "runner_start_failed"
	WPInventoryRunnerTimeout            WPInventoryErrorCode = "runner_timeout"
	WPInventoryStdoutLimitExceeded      WPInventoryErrorCode = "stdout_limit_exceeded"
	WPInventoryStderrLimitExceeded      WPInventoryErrorCode = "stderr_limit_exceeded"
	WPInventoryProtocolLimitExceeded    WPInventoryErrorCode = "protocol_limit_exceeded"
	WPInventoryProtocolInvalid          WPInventoryErrorCode = "protocol_invalid"
	WPInventoryRunnerPolicyMismatch     WPInventoryErrorCode = "runner_policy_mismatch"
	WPInventoryWordPressBootstrapFailed WPInventoryErrorCode = "wordpress_bootstrap_failed"
	WPInventoryWordPressTerminated      WPInventoryErrorCode = "wordpress_terminated"
	WPInventoryMemoryLimitExceeded      WPInventoryErrorCode = "memory_limit_exhausted"
	WPInventoryInventoryLimitExceeded   WPInventoryErrorCode = "inventory_limit_exceeded"
	WPInventoryRunnerInternalError      WPInventoryErrorCode = "runner_internal_error"
)

type WPInventoryStage string

const (
	WPInventoryStageValidate WPInventoryStage = "validate"
	WPInventoryStageLock     WPInventoryStage = "lock"
	WPInventoryStagePrepare  WPInventoryStage = "prepare"
	WPInventoryStageStart    WPInventoryStage = "start"
	WPInventoryStageExecute  WPInventoryStage = "execute"
	WPInventoryStageProtocol WPInventoryStage = "protocol"
)

type WPInventoryRunError struct {
	Code     WPInventoryErrorCode
	Stage    WPInventoryStage
	ExitCode int
	TimedOut bool
	cause    error
}

func (e *WPInventoryRunError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("wordpress inventory runner: code=%s stage=%s exit=%d timed_out=%t", e.Code, e.Stage, e.ExitCode, e.TimedOut)
}

func (e *WPInventoryRunError) Unwrap() error { return e.cause }

type WPInventoryRunResult struct {
	Inventory WPInventory
	Meta      WPInventoryRunMeta
}

type WPInventoryRunMeta struct {
	WallTime         time.Duration
	UserCPUTime      time.Duration
	SystemCPUTime    time.Duration
	MaxRSSKiB        int64
	ExitCode         int
	TimedOut         bool
	StdoutBytes      int64
	StderrBytes      int64
	ProtocolBytes    int64
	StdoutExceeded   bool
	StderrExceeded   bool
	ProtocolExceeded bool
	RunnerHash       string
	RunnerVersion    string
	SchemaVersion    int
	Warnings         []WPInventoryWarning
}

type WPInventoryWarning string

const WPInventoryWarningStaleCleanupFailed WPInventoryWarning = "stale_runner_cleanup_failed"

type WPInventory struct {
	WordPress    WPInventoryWordPress     `json:"wordpress"`
	Plugins      []WPInventoryPlugin      `json:"plugins"`
	Themes       []WPInventoryTheme       `json:"themes"`
	CurrentTheme *WPInventoryCurrentTheme `json:"current_theme"`
	Updates      WPInventoryUpdates       `json:"updates"`
}

type WPInventoryWordPress struct {
	Version   string `json:"version"`
	Locale    string `json:"locale"`
	Multisite bool   `json:"multisite"`
}

type WPInventoryPlugin struct {
	File          string `json:"file"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	Active        bool   `json:"active"`
	NetworkActive bool   `json:"network_active"`
}

type WPInventoryTheme struct {
	Stylesheet string `json:"stylesheet"`
	Name       string `json:"name"`
	Version    string `json:"version"`
}

type WPInventoryCurrentTheme = WPInventoryTheme

type WPInventoryUpdates struct {
	Core    WPInventoryCoreUpdates      `json:"core"`
	Plugins WPInventoryComponentUpdates `json:"plugins"`
	Themes  WPInventoryComponentUpdates `json:"themes"`
}

type WPInventoryCoreUpdates struct {
	TransientPresent bool                    `json:"transient_present"`
	LastChecked      int64                   `json:"last_checked"`
	VersionChecked   string                  `json:"version_checked"`
	Items            []WPInventoryCoreUpdate `json:"items"`
}

type WPInventoryCoreUpdate struct {
	Version  string `json:"version"`
	Response string `json:"response"`
	Locale   string `json:"locale"`
}

type WPInventoryComponentUpdates struct {
	TransientPresent bool                         `json:"transient_present"`
	LastChecked      int64                        `json:"last_checked"`
	Items            []WPInventoryComponentUpdate `json:"items"`
}

type WPInventoryComponentUpdate struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type wpInventoryUser struct {
	Name    string
	UID     int
	GID     int
	HomeDir string
}

type wpInventoryRunnerOptions struct {
	source      []byte
	runnerRoot  string
	trustedRoot string
	phpPath     string
	runuserPath string
	phpDir      string
	runuserDir  string
	requireRoot bool
	ownerUID    int
	ownerGID    int
	lookupUser  func(string) (*user.User, error)
	now         func() time.Time
}

type WPInventoryRunner struct {
	source      []byte
	hash        string
	runnerRoot  string
	trustedRoot string
	phpPath     string
	runuserPath string
	phpDir      string
	runuserDir  string
	requireRoot bool
	ownerUID    int
	ownerGID    int
	lookupUser  func(string) (*user.User, error)
	now         func() time.Time
}

func NewWPInventoryRunner() (*WPInventoryRunner, error) {
	return newWPInventoryRunner(wpInventoryRunnerOptions{
		source: wpInventoryRunnerSource, runnerRoot: wpInventoryRunnerRoot, trustedRoot: "/var",
		phpPath: wpInventoryPHPPath, runuserPath: wpInventoryRunuserPath,
		phpDir: "/usr/bin", runuserDir: "/usr/sbin",
		requireRoot: true, ownerUID: 0, ownerGID: 0, lookupUser: user.Lookup, now: time.Now,
	})
}

func newWPInventoryRunner(opts wpInventoryRunnerOptions) (*WPInventoryRunner, error) {
	if len(opts.source) == 0 || len(opts.source) > wpInventorySourceLimit {
		return nil, runError(WPInventoryRunnerPrepareFailed, WPInventoryStagePrepare, -1, false, errors.New("invalid embedded source"))
	}
	if opts.runnerRoot == "" || opts.trustedRoot == "" || opts.phpPath == "" || opts.runuserPath == "" || opts.phpDir == "" || opts.runuserDir == "" || opts.lookupUser == nil || opts.now == nil {
		return nil, runError(WPInventoryRunnerPrepareFailed, WPInventoryStagePrepare, -1, false, errors.New("invalid runner options"))
	}
	sum := sha256.Sum256(opts.source)
	return &WPInventoryRunner{
		source: append([]byte(nil), opts.source...), hash: hex.EncodeToString(sum[:]),
		runnerRoot: opts.runnerRoot, trustedRoot: opts.trustedRoot, phpPath: opts.phpPath, runuserPath: opts.runuserPath,
		phpDir: opts.phpDir, runuserDir: opts.runuserDir,
		requireRoot: opts.requireRoot, ownerUID: opts.ownerUID, ownerGID: opts.ownerGID, lookupUser: opts.lookupUser, now: opts.now,
	}, nil
}

func (r *WPInventoryRunner) Collect(ctx context.Context, cfg *config.Config, site *models.Website, forceUpdateCheck bool) (WPInventoryRunResult, error) {
	meta := WPInventoryRunMeta{ExitCode: -1, RunnerHash: r.hash, RunnerVersion: wpInventoryRunnerVersion, SchemaVersion: wpInventorySchemaVersion}
	if err := wpInventoryPlatformSupported(); err != nil {
		return WPInventoryRunResult{Meta: meta}, runError(WPInventoryUnsupportedPlatform, WPInventoryStageValidate, -1, false, err)
	}
	if r.requireRoot && wpInventoryEffectiveUID() != 0 {
		return WPInventoryRunResult{Meta: meta}, runError(WPInventoryInsufficientPrivileges, WPInventoryStageValidate, -1, false, errors.New("effective uid is not root"))
	}
	validated, err := r.validateInputs(cfg, site)
	if err != nil {
		return WPInventoryRunResult{Meta: meta}, err
	}

	lockCtx, cancelLock := context.WithTimeout(ctx, wpInventoryLockWait)
	defer cancelLock()
	if err := acquireInventorySlot(lockCtx); err != nil {
		return WPInventoryRunResult{Meta: meta}, canceledOrLockError(ctx, err)
	}
	defer releaseInventorySlot()

	if err := r.ensureRunnerRoot(); err != nil {
		return WPInventoryRunResult{Meta: meta}, err
	}
	lock, err := wpInventoryAcquireFileLock(lockCtx, filepath.Join(r.runnerRoot, ".lock"), r.ownerUID, r.ownerGID)
	if err != nil {
		return WPInventoryRunResult{Meta: meta}, canceledOrLockError(ctx, err)
	}
	defer lock.Close()

	runnerPath, warnings, err := r.prepareRunner()
	meta.Warnings = warnings
	if err != nil {
		return WPInventoryRunResult{Meta: meta}, err
	}
	return r.execute(ctx, validated, runnerPath, meta, forceUpdateCheck)
}

type wpInventoryValidatedInput struct {
	siteRoot string
	domain   string
	user     wpInventoryUser
	phpPath  string
	runuser  string
}

func (r *WPInventoryRunner) validateInputs(cfg *config.Config, site *models.Website) (wpInventoryValidatedInput, error) {
	if cfg == nil || site == nil || site.ID <= 0 || site.Domain == "" || site.SystemUser == "" || site.WebRoot == "" || site.SiteType != "wordpress" || !IsValidDomain(site.Domain) || !wpInventoryUserPattern.MatchString(site.SystemUser) {
		return wpInventoryValidatedInput{}, runError(WPInventoryInvalidSite, WPInventoryStageValidate, -1, false, errors.New("invalid site fields"))
	}
	switch site.Status {
	case models.StatusActive, models.StatusPaused, models.StatusError:
	default:
		return wpInventoryValidatedInput{}, runError(WPInventoryInvalidSite, WPInventoryStageValidate, -1, false, errors.New("invalid site status"))
	}
	root, err := validateInventorySitePath(cfg.Paths.WWWRoot, site.WebRoot)
	if err != nil {
		return wpInventoryValidatedInput{}, runError(WPInventoryInvalidSitePath, WPInventoryStageValidate, -1, false, err)
	}
	u, err := r.lookupUser(site.SystemUser)
	if err != nil {
		return wpInventoryValidatedInput{}, runError(WPInventorySiteUserUnavailable, WPInventoryStageValidate, -1, false, err)
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || u.Username != site.SystemUser || strings.TrimSpace(u.HomeDir) == "" {
		return wpInventoryValidatedInput{}, runError(WPInventorySiteUserUnavailable, WPInventoryStageValidate, -1, false, errors.New("invalid site uid or gid"))
	}
	phpPath, err := validateInventoryBinary(r.phpPath, r.phpDir, r.ownerUID, r.ownerGID)
	if err != nil {
		return wpInventoryValidatedInput{}, runError(WPInventoryPHPCLIUnavailable, WPInventoryStageValidate, -1, false, err)
	}
	runuserPath, err := validateInventoryBinary(r.runuserPath, r.runuserDir, r.ownerUID, r.ownerGID)
	if err != nil {
		return wpInventoryValidatedInput{}, runError(WPInventoryRunuserUnavailable, WPInventoryStageValidate, -1, false, err)
	}
	return wpInventoryValidatedInput{siteRoot: root, domain: site.Domain, user: wpInventoryUser{Name: u.Username, UID: uid, GID: gid, HomeDir: u.HomeDir}, phpPath: phpPath, runuser: runuserPath}, nil
}

func validateInventorySitePath(wwwRoot, siteRoot string) (string, error) {
	managed, err := managedSubpath(wwwRoot, siteRoot, "wordpress inventory")
	if err != nil {
		return "", err
	}
	for _, p := range []string{wwwRoot, managed} {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("site directory contract failed")
		}
	}
	realWWW, err := filepath.EvalSymlinks(wwwRoot)
	if err != nil {
		return "", err
	}
	realSite, err := filepath.EvalSymlinks(managed)
	if err != nil {
		return "", err
	}
	if !pathWithin(realWWW, realSite, false) {
		return "", errors.New("real site root escapes www root")
	}
	wpLoad := filepath.Join(realSite, "wp-load.php")
	info, err := os.Lstat(wpLoad)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("invalid wp-load.php")
	}
	realLoad, err := filepath.EvalSymlinks(wpLoad)
	if err != nil || !pathWithin(realSite, realLoad, false) {
		return "", errors.New("wp-load.php escapes site root")
	}
	return filepath.Clean(realSite), nil
}

func validateInventoryBinary(binary, expectedDir string, ownerUID, ownerGID int) (string, error) {
	info, err := os.Lstat(binary)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(binary)
		if err != nil {
			return "", err
		}
		binary = resolved
	}
	real, err := filepath.EvalSymlinks(binary)
	if err != nil || filepath.Dir(real) != expectedDir {
		return "", errors.New("binary resolves outside fixed system directory")
	}
	info, err = os.Stat(real)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return "", errors.New("binary mode contract failed")
	}
	uid, gid, ok := wpInventoryFileOwner(info)
	if !ok || uid != ownerUID || gid != ownerGID {
		return "", errors.New("binary owner contract failed")
	}
	return real, nil
}

func pathWithin(root, target string, allowEqual bool) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return allowEqual || rel != "."
}

func acquireInventorySlot(ctx context.Context) error {
	select {
	case wpInventorySlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseInventorySlot() { <-wpInventorySlot }

func canceledOrLockError(caller context.Context, err error) error {
	if caller.Err() != nil {
		return runError(WPInventoryRunnerCanceled, WPInventoryStageLock, -1, false, caller.Err())
	}
	if errors.Is(err, context.Canceled) {
		return runError(WPInventoryRunnerCanceled, WPInventoryStageLock, -1, false, err)
	}
	return runError(WPInventoryRunnerLockFailed, WPInventoryStageLock, -1, false, err)
}

func runError(code WPInventoryErrorCode, stage WPInventoryStage, exit int, timedOut bool, cause error) *WPInventoryRunError {
	return &WPInventoryRunError{Code: code, Stage: stage, ExitCode: exit, TimedOut: timedOut, cause: cause}
}

type countingSink struct {
	mu       sync.Mutex
	limit    int64
	total    int64
	exceeded bool
	keep     bool
	buf      bytes.Buffer
}

func newCountingSink(limit int64, keep bool) *countingSink {
	return &countingSink{limit: limit, keep: keep}
}

func (s *countingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	s.total += int64(n)
	if s.total > s.limit {
		s.exceeded = true
	}
	if s.keep && int64(s.buf.Len()) < s.limit {
		remaining := s.limit - int64(s.buf.Len())
		if int64(n) > remaining {
			n = int(remaining)
		}
		_, _ = s.buf.Write(p[:n])
	}
	return len(p), nil
}

func (s *countingSink) snapshot() (int64, bool, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, s.exceeded, append([]byte(nil), s.buf.Bytes()...)
}

type wpInventoryEnvelope struct {
	Protocol               string                    `json:"protocol"`
	RunnerVersion          string                    `json:"runner_version"`
	InventorySchemaVersion int                       `json:"inventory_schema_version"`
	OK                     bool                      `json:"ok"`
	Data                   *WPInventory              `json:"data,omitempty"`
	Error                  *wpInventoryProtocolError `json:"error,omitempty"`
	Diagnostics            wpInventoryDiagnostics    `json:"diagnostics"`
}

type wpInventoryProtocolError struct {
	Code string `json:"code"`
}

type wpInventoryDiagnostics struct {
	SAPI                 string `json:"sapi"`
	EffectiveUID         int    `json:"effective_uid"`
	EffectiveGID         int    `json:"effective_gid"`
	OpenBaseDir          string `json:"open_basedir"`
	DisableFunctions     string `json:"disable_functions"`
	AllowURLInclude      string `json:"allow_url_include"`
	MemoryLimit          string `json:"memory_limit"`
	BootstrapOutputBytes int64  `json:"bootstrap_output_bytes"`
}

func parseWPInventoryProtocol(raw []byte, token string) (wpInventoryEnvelope, error) {
	begin := []byte("WP_PANEL_INVENTORY_BEGIN " + token + "\n")
	end := []byte("WP_PANEL_INVENTORY_END " + token + "\n")
	if !bytes.HasPrefix(raw, begin) || !bytes.HasSuffix(raw, end) || bytes.Count(raw, begin) != 1 || bytes.Count(raw, end) != 1 {
		return wpInventoryEnvelope{}, errors.New("invalid protocol frame")
	}
	body := raw[len(begin) : len(raw)-len(end)]
	if bytes.Contains(body, []byte("WP_PANEL_INVENTORY_BEGIN ")) || bytes.Contains(body, []byte("WP_PANEL_INVENTORY_END ")) {
		return wpInventoryEnvelope{}, errors.New("nested protocol frame")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var env wpInventoryEnvelope
	if err := dec.Decode(&env); err != nil {
		return wpInventoryEnvelope{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return wpInventoryEnvelope{}, errors.New("trailing protocol data")
	}
	if env.Protocol != wpInventoryProtocol || env.RunnerVersion != wpInventoryRunnerVersion || env.InventorySchemaVersion != wpInventorySchemaVersion {
		return wpInventoryEnvelope{}, errors.New("protocol version mismatch")
	}
	if env.OK == (env.Data == nil) || env.OK == (env.Error != nil) {
		return wpInventoryEnvelope{}, errors.New("invalid success/error fields")
	}
	if env.Error != nil && env.Error.Code == "" {
		return wpInventoryEnvelope{}, errors.New("empty protocol error")
	}
	if env.Data != nil {
		if err := validateWPInventory(env.Data); err != nil {
			return wpInventoryEnvelope{}, err
		}
	}
	return env, nil
}

func validateWPInventory(inv *WPInventory) error {
	if len(inv.Plugins) > wpInventoryPluginLimit || len(inv.Themes) > wpInventoryThemeLimit || len(inv.Updates.Core.Items)+len(inv.Updates.Plugins.Items)+len(inv.Updates.Themes.Items) > wpInventoryUpdateLimit {
		return errors.New("inventory count limit exceeded")
	}
	if err := validateShort(inv.WordPress.Version, wpInventoryVersionLimit); err != nil {
		return err
	}
	if err := validateShort(inv.WordPress.Locale, wpInventoryVersionLimit); err != nil {
		return err
	}
	pluginKeys := make([]string, len(inv.Plugins))
	for i, item := range inv.Plugins {
		if !safeRelativeComponent(item.File) || validateShort(item.File, wpInventoryNameLimit) != nil || validateShort(item.Name, wpInventoryNameLimit) != nil || validateShort(item.Version, wpInventoryVersionLimit) != nil {
			return errors.New("invalid plugin item")
		}
		pluginKeys[i] = item.File
	}
	if !strictSortedUnique(pluginKeys) {
		return errors.New("plugins not sorted and unique")
	}
	themeKeys := make([]string, len(inv.Themes))
	for i, item := range inv.Themes {
		if !safeRelativeComponent(item.Stylesheet) || validateShort(item.Stylesheet, wpInventoryNameLimit) != nil || validateShort(item.Name, wpInventoryNameLimit) != nil || validateShort(item.Version, wpInventoryVersionLimit) != nil {
			return errors.New("invalid theme item")
		}
		themeKeys[i] = item.Stylesheet
	}
	if !strictSortedUnique(themeKeys) {
		return errors.New("themes not sorted and unique")
	}
	if inv.CurrentTheme != nil && (!safeRelativeComponent(inv.CurrentTheme.Stylesheet) || validateShort(inv.CurrentTheme.Name, wpInventoryNameLimit) != nil || validateShort(inv.CurrentTheme.Version, wpInventoryVersionLimit) != nil) {
		return errors.New("invalid current theme")
	}
	coreKeys := make([]string, len(inv.Updates.Core.Items))
	for i, item := range inv.Updates.Core.Items {
		if validateShort(item.Version, wpInventoryVersionLimit) != nil || validateShort(item.Response, wpInventoryVersionLimit) != nil || validateShort(item.Locale, wpInventoryVersionLimit) != nil {
			return errors.New("invalid core update")
		}
		coreKeys[i] = item.Version + "\x00" + item.Locale + "\x00" + item.Response
	}
	if !strictSortedUnique(coreKeys) {
		return errors.New("core updates not sorted and unique")
	}
	for _, group := range []*WPInventoryComponentUpdates{&inv.Updates.Plugins, &inv.Updates.Themes} {
		keys := make([]string, len(group.Items))
		for i, item := range group.Items {
			if !safeRelativeComponent(item.ID) || validateShort(item.ID, wpInventoryNameLimit) != nil || validateShort(item.Version, wpInventoryVersionLimit) != nil {
				return errors.New("invalid component update")
			}
			keys[i] = item.ID
		}
		if !strictSortedUnique(keys) {
			return errors.New("component updates not sorted and unique")
		}
	}
	return nil
}

func validateShort(value string, limit int) error {
	if !utf8.ValidString(value) || len(value) > limit {
		return errors.New("invalid string length")
	}
	return nil
}

func safeRelativeComponent(value string) bool {
	if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func strictSortedUnique(items []string) bool {
	return sort.SliceIsSorted(items, func(i, j int) bool { return items[i] < items[j] }) && !hasAdjacentDuplicate(items)
}

func hasAdjacentDuplicate(items []string) bool {
	for i := 1; i < len(items); i++ {
		if items[i] == items[i-1] {
			return true
		}
	}
	return false
}

// wpInventoryPolicyMismatch reports which reported diagnostic(s), if any,
// don't match what the panel required of the PHP CLI invocation. It never
// includes disable_functions' actual value (only whether it matched) since
// that string can be long and isn't useful for remote diagnosis; every other
// mismatched field is reported with both the expected and observed value so
// a failure can be root-caused from the server log alone, without needing
// to reproduce it interactively.
func wpInventoryPolicyMismatch(d wpInventoryDiagnostics, expectedUID, expectedGID int, expectedOpenBaseDir string) string {
	var mismatches []string
	if d.SAPI != "cli" {
		mismatches = append(mismatches, fmt.Sprintf("sapi=%q want=cli", d.SAPI))
	}
	if d.EffectiveUID != expectedUID {
		mismatches = append(mismatches, fmt.Sprintf("effective_uid=%d want=%d", d.EffectiveUID, expectedUID))
	}
	if d.EffectiveGID != expectedGID {
		mismatches = append(mismatches, fmt.Sprintf("effective_gid=%d want=%d", d.EffectiveGID, expectedGID))
	}
	if d.OpenBaseDir != expectedOpenBaseDir {
		mismatches = append(mismatches, fmt.Sprintf("open_basedir=%q want=%q", d.OpenBaseDir, expectedOpenBaseDir))
	}
	if d.DisableFunctions != sitePHPDisabledFunctions() {
		mismatches = append(mismatches, "disable_functions mismatch")
	}
	if d.AllowURLInclude != "0" {
		mismatches = append(mismatches, fmt.Sprintf("allow_url_include=%q want=0", d.AllowURLInclude))
	}
	if d.MemoryLimit != wpInventoryRunnerMemoryLimit {
		mismatches = append(mismatches, fmt.Sprintf("memory_limit=%q want=%q", d.MemoryLimit, wpInventoryRunnerMemoryLimit))
	}
	return strings.Join(mismatches, "; ")
}

func randomInventoryToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (r *WPInventoryRunner) execute(ctx context.Context, input wpInventoryValidatedInput, runnerPath string, meta WPInventoryRunMeta, forceUpdateCheck bool) (WPInventoryRunResult, error) {
	token, err := randomInventoryToken()
	if err != nil {
		return WPInventoryRunResult{Meta: meta}, runError(WPInventoryRunnerStartFailed, WPInventoryStageStart, -1, false, err)
	}
	runnerDir := filepath.Dir(runnerPath)
	openBaseDir := sitePHPRunnerOpenBaseDir(input.siteRoot, input.domain, runnerDir)
	args := []string{"-u", input.user.Name, "--", input.phpPath,
		"-d", "open_basedir=" + openBaseDir,
		"-d", "disable_functions=" + sitePHPDisabledFunctions(),
		"-d", "allow_url_include=0", "-d", "memory_limit=" + wpInventoryRunnerMemoryLimit, runnerPath, input.siteRoot,
	}
	execTimeout := wpInventoryExecutionTimeout
	if forceUpdateCheck {
		execTimeout = wpInventoryForceUpdateTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	cmd := exec.CommandContext(execCtx, input.runuser, args...)
	forceEnv := "WP_PANEL_FORCE_UPDATE_CHECK=0"
	if forceUpdateCheck {
		forceEnv = "WP_PANEL_FORCE_UPDATE_CHECK=1"
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + input.user.HomeDir, "USER=" + input.user.Name, "LOGNAME=" + input.user.Name, "TMPDIR=/tmp", "WP_PANEL_RUNNER_TOKEN=" + token, forceEnv}
	stdout := newCountingSink(wpInventoryStreamLimit, false)
	stderr := newCountingSink(wpInventoryStreamLimit, false)
	protocol := newCountingSink(wpInventoryProtocolLimit, true)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	protocolRead, protocolWrite, err := os.Pipe()
	if err != nil {
		return WPInventoryRunResult{Meta: meta}, runError(WPInventoryRunnerStartFailed, WPInventoryStageStart, -1, false, err)
	}
	defer protocolRead.Close()
	cmd.ExtraFiles = []*os.File{protocolWrite}
	wpInventoryConfigureCommand(cmd)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		protocolWrite.Close()
		return WPInventoryRunResult{Meta: meta}, runError(WPInventoryRunnerStartFailed, WPInventoryStageStart, -1, false, err)
	}
	_ = protocolWrite.Close()
	protocolDone := make(chan error, 1)
	go func() { _, copyErr := io.Copy(protocol, protocolRead); protocolDone <- copyErr }()
	waitErr := cmd.Wait()
	copyErr := <-protocolDone
	meta.WallTime = time.Since(started)
	meta.ExitCode = wpInventoryExitCode(cmd.ProcessState)
	meta.UserCPUTime, meta.SystemCPUTime, meta.MaxRSSKiB = wpInventoryProcessMetrics(cmd.ProcessState)
	meta.StdoutBytes, meta.StdoutExceeded, _ = stdout.snapshot()
	meta.StderrBytes, meta.StderrExceeded, _ = stderr.snapshot()
	protocolBytes, protocolExceeded, raw := protocol.snapshot()
	meta.ProtocolBytes = protocolBytes
	meta.ProtocolExceeded = protocolExceeded
	meta.TimedOut = errors.Is(execCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	result := WPInventoryRunResult{Meta: meta}
	if ctx.Err() != nil {
		return result, runError(WPInventoryRunnerCanceled, WPInventoryStageExecute, meta.ExitCode, false, ctx.Err())
	}
	if meta.TimedOut {
		return result, runError(WPInventoryRunnerTimeout, WPInventoryStageExecute, meta.ExitCode, true, execCtx.Err())
	}
	if copyErr != nil {
		return result, runError(WPInventoryProtocolInvalid, WPInventoryStageProtocol, meta.ExitCode, false, copyErr)
	}
	if meta.ProtocolExceeded {
		return result, runError(WPInventoryProtocolLimitExceeded, WPInventoryStageProtocol, meta.ExitCode, false, errors.New("protocol limit exceeded"))
	}
	env, parseErr := parseWPInventoryProtocol(raw, token)
	if parseErr != nil {
		return result, runError(WPInventoryProtocolInvalid, WPInventoryStageProtocol, meta.ExitCode, false, parseErr)
	}
	if mismatch := wpInventoryPolicyMismatch(env.Diagnostics, input.user.UID, input.user.GID, openBaseDir); mismatch != "" {
		log.Printf("WordPress 库存 Runner 策略校验失败 site=%s: %s", input.domain, mismatch)
		return result, runError(WPInventoryRunnerPolicyMismatch, WPInventoryStageProtocol, meta.ExitCode, false, errors.New("runner diagnostics mismatch: "+mismatch))
	}
	if meta.StdoutExceeded {
		return result, runError(WPInventoryStdoutLimitExceeded, WPInventoryStageExecute, meta.ExitCode, false, errors.New("stdout limit exceeded"))
	}
	if meta.StderrExceeded {
		return result, runError(WPInventoryStderrLimitExceeded, WPInventoryStageExecute, meta.ExitCode, false, errors.New("stderr limit exceeded"))
	}
	if !env.OK {
		return result, mapWPInventoryProtocolError(env.Error.Code, meta.ExitCode)
	}
	if waitErr != nil || meta.ExitCode != 0 {
		return result, runError(WPInventoryProtocolInvalid, WPInventoryStageProtocol, meta.ExitCode, false, waitErr)
	}
	result.Inventory = *env.Data
	return result, nil
}

func mapWPInventoryProtocolError(code string, exit int) error {
	var mapped WPInventoryErrorCode
	switch code {
	case "invalid_sapi", "invalid_site_root", "invalid_wp_load":
		mapped = WPInventoryProtocolInvalid
	case "bootstrap_throwable", "fatal_error":
		mapped = WPInventoryWordPressBootstrapFailed
	case "bootstrap_terminated":
		mapped = WPInventoryWordPressTerminated
	case "memory_limit_exhausted":
		mapped = WPInventoryMemoryLimitExceeded
	case "inventory_limit_exceeded":
		mapped = WPInventoryInventoryLimitExceeded
	case "json_encode_failed":
		mapped = WPInventoryRunnerInternalError
	default:
		mapped = WPInventoryProtocolInvalid
	}
	return runError(mapped, WPInventoryStageProtocol, exit, false, errors.New("runner failure envelope"))
}

func (r *WPInventoryRunner) ensureRunnerRoot() error {
	root := filepath.Clean(r.runnerRoot)
	trusted := filepath.Clean(r.trustedRoot)
	if !pathWithin(trusted, root, false) {
		return runError(WPInventoryRunnerPrepareFailed, WPInventoryStagePrepare, -1, false, errors.New("runner root escapes trusted root"))
	}
	if err := ensureInventoryDirectory(trusted, 0, r.ownerUID, r.ownerGID, true); err != nil {
		return runError(WPInventoryRunnerPrepareFailed, WPInventoryStagePrepare, -1, false, err)
	}
	rel, _ := filepath.Rel(trusted, root)
	current := trusted
	for index, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := ensureInventoryDirectory(current, 0711, r.ownerUID, r.ownerGID, index == 0); err != nil {
			return runError(WPInventoryRunnerPrepareFailed, WPInventoryStagePrepare, -1, false, err)
		}
	}
	return nil
}

func ensureInventoryDirectory(path string, mode fs.FileMode, ownerUID, ownerGID int, allowExistingMode bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("unsafe runner directory")
	}
	uid, gid, ok := wpInventoryFileOwner(info)
	if !ok || uid != ownerUID || gid != ownerGID || info.Mode().Perm()&0022 != 0 {
		return errors.New("runner directory owner or mode invalid")
	}
	if !allowExistingMode && info.Mode().Perm() != mode {
		return errors.New("runner directory mode invalid")
	}
	return nil
}

func (r *WPInventoryRunner) prepareRunner() (string, []WPInventoryWarning, error) {
	finalDir := filepath.Join(r.runnerRoot, r.hash)
	finalFile := filepath.Join(finalDir, "inventory.php")
	if _, err := os.Lstat(finalDir); err == nil {
		if err := r.validatePublishedRunner(finalDir); err != nil {
			return "", nil, runError(WPInventoryRunnerIntegrityFailed, WPInventoryStagePrepare, -1, false, err)
		}
	} else if !os.IsNotExist(err) {
		return "", nil, runError(WPInventoryRunnerPrepareFailed, WPInventoryStagePrepare, -1, false, err)
	} else if err := r.publishRunner(finalDir); err != nil {
		return "", nil, runError(WPInventoryRunnerPrepareFailed, WPInventoryStagePrepare, -1, false, err)
	}
	warnings, err := r.cleanupStaleRunners()
	if err != nil {
		return "", nil, runError(WPInventoryRunnerIntegrityFailed, WPInventoryStagePrepare, -1, false, err)
	}
	return finalFile, warnings, nil
}

func (r *WPInventoryRunner) publishRunner(finalDir string) error {
	tempDir, err := os.MkdirTemp(r.runnerRoot, ".wp-inventory-tmp-")
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()
	if err := os.Chmod(tempDir, 0700); err != nil {
		return err
	}
	filePath := filepath.Join(tempDir, "inventory.php")
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(r.source); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chown(filePath, r.ownerUID, r.ownerGID); err != nil {
		return err
	}
	if err := os.Chmod(filePath, 0444); err != nil {
		return err
	}
	if err := os.Chown(tempDir, r.ownerUID, r.ownerGID); err != nil {
		return err
	}
	if err := os.Chmod(tempDir, 0555); err != nil {
		return err
	}
	if err := validateInventoryFile(filePath, r.source, r.ownerUID, r.ownerGID); err != nil {
		return err
	}
	if err := syncDirectory(tempDir); err != nil {
		return err
	}
	if err := os.Rename(tempDir, finalDir); err != nil {
		if os.IsExist(err) {
			return r.validatePublishedRunner(finalDir)
		}
		return err
	}
	removeTemp = false
	if err := syncDirectory(r.runnerRoot); err != nil {
		return err
	}
	return r.validatePublishedRunner(finalDir)
}

func (r *WPInventoryRunner) validatePublishedRunner(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0555 {
		return errors.New("invalid runner version directory")
	}
	uid, gid, ok := wpInventoryFileOwner(info)
	if !ok || uid != r.ownerUID || gid != r.ownerGID {
		return errors.New("invalid runner directory owner")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "inventory.php" {
		return errors.New("invalid runner directory structure")
	}
	return validateInventoryFile(filepath.Join(dir, "inventory.php"), r.source, r.ownerUID, r.ownerGID)
}

func validateInventoryFile(path string, source []byte, ownerUID, ownerGID int) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0444 || info.Size() != int64(len(source)) {
		return errors.New("invalid runner file")
	}
	uid, gid, ok := wpInventoryFileOwner(info)
	if !ok || uid != ownerUID || gid != ownerGID {
		return errors.New("invalid runner file owner")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, source) {
		return errors.New("runner content mismatch")
	}
	return nil
}

func (r *WPInventoryRunner) cleanupStaleRunners() ([]WPInventoryWarning, error) {
	entries, err := os.ReadDir(r.runnerRoot)
	if err != nil {
		return nil, err
	}
	var warnings []WPInventoryWarning
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(r.runnerRoot, name)
		if name == r.hash || name == ".lock" {
			continue
		}
		if strings.HasPrefix(name, ".wp-inventory-tmp-") {
			info, statErr := os.Lstat(path)
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, errors.New("unsafe temporary runner entry")
			}
			uid, gid, ok := wpInventoryFileOwner(info)
			if !ok || uid != r.ownerUID || gid != r.ownerGID {
				return nil, errors.New("unsafe temporary runner owner")
			}
			if r.now().Sub(info.ModTime()) >= time.Hour && os.RemoveAll(path) != nil {
				warnings = appendWarning(warnings, WPInventoryWarningStaleCleanupFailed)
			}
			continue
		}
		if !wpInventoryHashPattern.MatchString(name) {
			continue
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0555 {
			return nil, errors.New("unsafe stale runner entry")
		}
		uid, gid, ok := wpInventoryFileOwner(info)
		if !ok || uid != r.ownerUID || gid != r.ownerGID {
			return nil, errors.New("unsafe stale runner owner")
		}
		if err := validateStaleRunnerStructure(path, name, r.ownerUID, r.ownerGID); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(path); err != nil {
			warnings = appendWarning(warnings, WPInventoryWarningStaleCleanupFailed)
		}
	}
	return warnings, nil
}

func validateStaleRunnerStructure(dir, expectedHash string, ownerUID, ownerGID int) error {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "inventory.php" {
		return errors.New("invalid stale runner structure")
	}
	path := filepath.Join(dir, "inventory.php")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0444 {
		return errors.New("invalid stale runner file")
	}
	uid, gid, ok := wpInventoryFileOwner(info)
	if !ok || uid != ownerUID || gid != ownerGID {
		return errors.New("invalid stale runner owner")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expectedHash {
		return errors.New("stale runner hash mismatch")
	}
	return nil
}

func appendWarning(warnings []WPInventoryWarning, warning WPInventoryWarning) []WPInventoryWarning {
	for _, existing := range warnings {
		if existing == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
