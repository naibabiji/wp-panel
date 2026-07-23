package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed assets/wp-update-offer-runner/offer.php
var wpUpdateOfferPHPSource []byte

const (
	wpOfferRuntimeRoot = "/var/wp-panel/wp-update-offer"
	wpOfferResultName  = "offer-result.json"
	wpOfferResultMax   = 16 << 10
	wpOfferExecTimeout = 20 * time.Second
	wpOfferMemoryLimit = "256M"
)

var (
	errWPOfferNotFound       = errors.New("offer not found (not in repository)")
	errWPOfferLicenseInvalid = errors.New("offer license invalid")
	errWPOfferUnavailable    = errors.New("offer upstream unavailable")
)

type wpUpdateOfferEnvelope struct {
	Token       string `json:"token"`
	OK          bool   `json:"ok"`
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	ErrorCode   string `json:"error_code"`
}

type wpUpdateOfferRunner struct {
	db          *sql.DB
	phpPath     string
	runuserPath string
	phpDir      string
	runuserDir  string
	ownerUID    int
	ownerGID    int
	lookupUser  func(string) (*user.User, error)
	chown       func(string, int, int) error
}

func newWPUpdateOfferRunner(db *sql.DB) (*wpUpdateOfferRunner, error) {
	if db == nil {
		return nil, errors.New("invalid update offer runner")
	}
	return &wpUpdateOfferRunner{
		db: db, phpPath: wpInventoryPHPPath, runuserPath: wpInventoryRunuserPath,
		phpDir: "/usr/bin", runuserDir: "/usr/sbin", ownerUID: 0, ownerGID: 0,
		lookupUser: user.Lookup, chown: os.Chown,
	}, nil
}

// FetchPluginOffer resolves the update offer for a plugin from the site's own
// WordPress update mechanism instead of the panel server querying
// api.wordpress.org. This lets commercial/licensed plugins (whose packages are
// served by the vendor, not WordPress.org) be discovered when their license is
// active on the site.
func (r *wpUpdateOfferRunner) FetchPluginOffer(ctx context.Context, siteID int, webRoot, componentKey string) (wpPluginOffer, error) {
	env, err := r.run(ctx, siteID, webRoot, "plugin", componentKey)
	if err != nil {
		return wpPluginOffer{}, err
	}
	if !env.OK {
		return wpPluginOffer{}, classifyOfferError(env.ErrorCode)
	}
	slug := strings.SplitN(componentKey, "/", 2)[0]
	return wpPluginOffer{Slug: slug, Version: env.Version, DownloadURL: env.DownloadURL}, nil
}

func (r *wpUpdateOfferRunner) FetchThemeOffer(ctx context.Context, siteID int, webRoot, stylesheet string) (wpThemeOffer, error) {
	env, err := r.run(ctx, siteID, webRoot, "theme", stylesheet)
	if err != nil {
		return wpThemeOffer{}, err
	}
	if !env.OK {
		return wpThemeOffer{}, classifyOfferError(env.ErrorCode)
	}
	return wpThemeOffer{Slug: stylesheet, Version: env.Version, DownloadURL: env.DownloadURL}, nil
}

func classifyOfferError(code string) error {
	switch code {
	case "not_in_repository":
		return errWPOfferNotFound
	case "license_invalid":
		return errWPOfferLicenseInvalid
	default:
		return errWPOfferUnavailable
	}
}

func (r *wpUpdateOfferRunner) run(ctx context.Context, siteID int, webRoot, componentType, componentKey string) (wpUpdateOfferEnvelope, error) {
	if !filepath.IsAbs(webRoot) {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	var systemUser string
	if err := r.db.QueryRowContext(ctx, `SELECT system_user FROM websites WHERE id=?`, siteID).Scan(&systemUser); err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	if !wpInventoryUserPattern.MatchString(systemUser) {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	u, err := r.lookupUser(systemUser)
	if err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil || uid <= 0 || gid <= 0 || u.Username != systemUser || !filepath.IsAbs(u.HomeDir) {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	if _, err := os.Stat(filepath.Join(webRoot, "wp-load.php")); err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	phpPath, err := validateInventoryBinary(r.phpPath, r.phpDir, r.ownerUID, r.ownerGID)
	if err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	runuserPath, err := validateInventoryBinary(r.runuserPath, r.runuserDir, r.ownerUID, r.ownerGID)
	if err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	if err := os.MkdirAll(wpOfferRuntimeRoot, 0711); err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	token := hex.EncodeToString(tokenBytes)
	runtimeDir := filepath.Join(wpOfferRuntimeRoot, token)
	if filepath.Dir(runtimeDir) != filepath.Clean(wpOfferRuntimeRoot) || os.Mkdir(runtimeDir, 0710) != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(runtimeDir)
		}
	}()
	if err := os.Chown(runtimeDir, r.ownerUID, gid); err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	result := filepath.Join(runtimeDir, wpOfferResultName)
	if err := createWPPluginRunnerResult(result, uid, gid); err != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	openBase := strings.Join([]string{webRoot, runtimeDir, "/tmp", "/usr/share/php"}, ":")
	execCtx, cancel := context.WithTimeout(ctx, wpOfferExecTimeout)
	defer cancel()
	args := []string{"-u", u.Username, "--", "/usr/bin/env", "-i",
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username, "TMPDIR=/tmp",
		phpPath,
		"-d", "open_basedir=" + openBase,
		"-d", "disable_functions=" + sitePHPDisabledFunctions(),
		"-d", "allow_url_include=0",
		"-d", "memory_limit=" + wpOfferMemoryLimit,
		"-r", string(wpUpdateOfferPHPSource),
		token, webRoot, componentType, componentKey, "0.0.0", "0.0.0", result,
	}
	cmd := exec.CommandContext(execCtx, runuserPath, args...)
	if err := cmd.Run(); err != nil && !errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		// PHP may have written an error envelope before exiting non-zero; fall
		// through to read the result file.
		_ = err
	}
	env, readErr := readWPUpdateOfferResult(result, token)
	if readErr != nil {
		return wpUpdateOfferEnvelope{}, errWPOfferUnavailable
	}
	keep = true
	return env, nil
}

func readWPUpdateOfferResult(name, token string) (wpUpdateOfferEnvelope, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > wpOfferResultMax {
		return wpUpdateOfferEnvelope{}, errors.New("invalid update offer result")
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return wpUpdateOfferEnvelope{}, err
	}
	var env wpUpdateOfferEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Token != token {
		return wpUpdateOfferEnvelope{}, errors.New("invalid update offer envelope")
	}
	return env, nil
}
