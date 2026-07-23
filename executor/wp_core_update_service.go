package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

var (
	ErrWPCoreUpdateInvalid     = errors.New("invalid core update request")
	ErrWPCoreUpdateNotFound    = errors.New("core update resource not found")
	ErrWPCoreUpdateConflict    = errors.New("core update request conflict")
	ErrWPCoreUpdateBusy        = errors.New("core update service busy")
	ErrWPCoreUpdateUnavailable = errors.New("core update upstream unavailable")
	ErrWPCoreUpdateSiteBusy    = errors.New("core update blocked by active site restore")

	// errWPCoreUpdateNoCandidate is an internal signal (not an API error): the
	// site's inventory state is healthy but WordPress.org reports no newer
	// core version. Preview() converts it into an Available:false response
	// instead of surfacing it as a conflict.
	errWPCoreUpdateNoCandidate = errors.New("no core update candidate")
)

var wpStableCoreVersionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,3}$`)

type wpCoreVersionOffer struct {
	Version, Locale, DownloadURL, PHPMin, MySQLMin string
}

type wpCoreOfferFetcher func(context.Context, string, string, string, string) (wpCoreVersionOffer, error)
type wpCoreInstalledVersions func(context.Context) (string, string, error)

type WPCoreUpdateService struct {
	db            *sql.DB
	store         *wpUpdateStore
	artifacts     *wpUpdateArtifactService
	confirmations *wpCoreConfirmationStore
	fetchOffer    wpCoreOfferFetcher
	versions      wpCoreInstalledVersions
	now           func() time.Time
}

type wpCoreUpdateCandidate struct {
	siteID                                int
	domain, webRoot, collectionID         string
	currentVersion, targetVersion, locale string
	lastSuccess                           time.Time
}

func NewWPCoreUpdateService(db *sql.DB, backupDir string) (*WPCoreUpdateService, error) {
	if db == nil || !validWPCoreUpdateRoot(backupDir) {
		return nil, ErrWPCoreUpdateInvalid
	}
	store := newWPUpdateStore(db)
	artifacts, err := newWPUpdateArtifactService(store, filepath.Join(filepath.Clean(backupDir), "wp-updates"), defaultWPUpdateDatabaseDumper)
	if err != nil {
		return nil, ErrWPCoreUpdateUnavailable
	}
	return &WPCoreUpdateService{db: db, store: store, artifacts: artifacts, confirmations: newWPCoreConfirmationStore(), fetchOffer: defaultWPCoreOfferFetcher(nil), versions: defaultWPCoreInstalledVersions, now: time.Now}, nil
}

func (s *WPCoreUpdateService) Preview(ctx context.Context, siteID int, username string) (models.WPCoreUpdatePreview, error) {
	if s == nil || siteID <= 0 || username == "" {
		return models.WPCoreUpdatePreview{}, ErrWPCoreUpdateInvalid
	}
	candidate, err := s.loadCandidate(ctx, siteID)
	if errors.Is(err, errWPCoreUpdateNoCandidate) {
		return models.WPCoreUpdatePreview{Available: false, SiteID: siteID, Domain: candidate.domain, CurrentVersion: candidate.currentVersion}, nil
	}
	if err != nil {
		return models.WPCoreUpdatePreview{}, err
	}
	if wpConfigHasUserFileModsLock(candidate.webRoot) {
		return models.WPCoreUpdatePreview{}, ErrWPCoreUpdateConflict
	}
	phpVersion, mysqlVersion, err := s.versions(ctx)
	if err != nil {
		log.Printf("核心更新预览失败 site=%d: 读取本机 PHP/MySQL 版本失败: %v", siteID, err)
		return models.WPCoreUpdatePreview{}, ErrWPCoreUpdateUnavailable
	}
	offer, err := s.fetchOffer(ctx, candidate.currentVersion, candidate.locale, phpVersion, mysqlVersion)
	if err != nil {
		log.Printf("核心更新预览失败 site=%d domain=%s: 查询 WordPress.org 版本信息失败: %v", siteID, candidate.domain, err)
		return models.WPCoreUpdatePreview{}, ErrWPCoreUpdateUnavailable
	}
	if offer.Version != candidate.targetVersion || offer.Locale != candidate.locale || !wpStableCoreVersionPattern.MatchString(offer.Version) ||
		!validWPCoreUpdatePackageURL(offer.DownloadURL) || offer.PHPMin == "" || offer.MySQLMin == "" ||
		compareWPVersions(phpVersion, offer.PHPMin) < 0 || compareWPVersions(mysqlVersion, offer.MySQLMin) < 0 {
		return models.WPCoreUpdatePreview{}, ErrWPCoreUpdateConflict
	}
	now := s.now().UTC()
	recent := recentBackupOrNil(s.store, ctx, siteID, now)
	var recentID int64
	if recent != nil {
		recentID = recent.BackupID
	}
	record, err := s.confirmations.create(wpCoreConfirmation{username: username, siteID: siteID, domain: candidate.domain,
		collectionID: candidate.collectionID, currentVersion: candidate.currentVersion, targetVersion: candidate.targetVersion,
		locale: candidate.locale, downloadURL: offer.DownloadURL, recentBackupID: recentID})
	if err != nil {
		return models.WPCoreUpdatePreview{}, ErrWPCoreUpdateBusy
	}
	expires := record.expiresAt
	return models.WPCoreUpdatePreview{Available: true, SiteID: siteID, Domain: candidate.domain,
		CurrentVersion: candidate.currentVersion, TargetVersion: candidate.targetVersion, Locale: candidate.locale,
		PackageSource: "wordpress.org", VerificationRequired: "official_verified", DatabaseBackup: true, RecentDatabaseBackup: recent,
		CoreFilesBackup: true, UploadsIncluded: false, Compatibility: models.WPCoreUpdateCompatibility{PHP: "compatible", MySQL: "compatible"},
		ConfirmationToken: record.token, ExpiresAt: &expires}, nil
}

func (s *WPCoreUpdateService) Confirm(ctx context.Context, siteID int, username, token, target, backupMode string) (models.WPCoreUpdateTask, error) {
	if s == nil || siteID <= 0 || username == "" || token == "" || target == "" {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateInvalid
	}
	record, err := s.confirmations.consume(token, username, siteID, target)
	if err != nil {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateConflict
	}
	if backupMode == "" {
		backupMode = "fresh"
	}
	var sourceID int64
	if backupMode == "reuse" {
		sourceID = record.recentBackupID
	}
	if err := s.store.validateDatabaseBackupChoice(ctx, siteID, backupMode, sourceID, s.artifacts.root, s.now().UTC()); err != nil {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateConflict
	}
	candidate, err := s.loadCandidate(ctx, siteID)
	if err != nil || candidate.collectionID != record.collectionID || candidate.currentVersion != record.currentVersion ||
		candidate.targetVersion != record.targetVersion || candidate.locale != record.locale || wpConfigHasUserFileModsLock(candidate.webRoot) {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateConflict
	}
	if !TryAcquireSiteOpLock(siteID, "wp_core_update") {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateSiteBusy
	}
	task, err := s.store.createCoreManualPlan(ctx, WPUpdatePlan{SiteID: siteID, CurrentVersion: record.currentVersion,
		TargetVersion: record.targetVersion, PackageSource: "wordpress.org", DownloadURL: record.downloadURL,
		DatabaseBackupMode: backupMode, DatabaseBackupSourceID: sourceID}, s.now().UTC())
	// The task row itself (status='preparing', already inside the active set that
	// wp_update_backup.go's restore path checks) is now the durable "this site is
	// busy" signal, so the lock only needs to be held long enough to make that
	// write atomic with the check above — not for the rest of this (possibly slow)
	// download/seal work.
	ReleaseSiteOpLock(siteID)
	if err != nil {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateConflict
	}
	sealed, err := s.downloadAndSeal(ctx, task, record)
	if err != nil {
		log.Printf("核心更新下载/封存失败 site=%d domain=%s task=%s: %v", siteID, record.domain, task.ID, err)
		if failErr := s.store.failPreparingPlan(context.Background(), task.ID, "package_prepare_failed", s.now().UTC()); failErr == nil {
			_ = os.RemoveAll(filepath.Join(s.artifacts.root, task.ID))
		} else if current, lookupErr := s.store.getTask(context.Background(), task.ID); lookupErr == nil && current.Status == wpUpdateFailed && current.PlanSealedAt == "" {
			_ = os.RemoveAll(filepath.Join(s.artifacts.root, task.ID))
		}
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateUnavailable
	}
	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO operation_logs(operation,target,status,message) VALUES('wp_core_update_request',?,'success',?)`, record.domain, "task="+sealed.ID+" version="+sealed.TargetVersion)
	return s.taskModel(ctx, sealed, false)
}

func (s *WPCoreUpdateService) Task(ctx context.Context, siteID int, taskID string) (models.WPCoreUpdateTask, error) {
	if s == nil || siteID <= 0 || !wpUpdateTaskIDPattern.MatchString(taskID) {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateInvalid
	}
	task, err := s.store.getTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (task.SiteID != siteID || task.TaskKind != "update" || task.ComponentType != "core") {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateNotFound
	}
	if err != nil {
		return models.WPCoreUpdateTask{}, err
	}
	return s.taskModel(ctx, task, true)
}

func (s *WPCoreUpdateService) LatestTask(ctx context.Context, siteID int) (models.WPCoreUpdateTask, error) {
	if s == nil || siteID <= 0 {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateInvalid
	}
	task, err := s.store.latestCoreUpdateTask(ctx, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.WPCoreUpdateTask{}, ErrWPCoreUpdateNotFound
	}
	if err != nil {
		return models.WPCoreUpdateTask{}, err
	}
	return s.taskModel(ctx, task, true)
}

func (s *WPCoreUpdateService) loadCandidate(ctx context.Context, siteID int) (wpCoreUpdateCandidate, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return wpCoreUpdateCandidate{}, err
	}
	defer tx.Rollback()
	var c wpCoreUpdateCandidate
	var status, siteType, inventoryStatus, stateLocale, lastSuccess string
	var multisite, blocked int
	err = tx.QueryRowContext(ctx, `SELECT w.id,w.domain,w.web_root,w.status,w.site_type,s.status,s.wordpress_version,
		s.wordpress_locale,s.is_multisite,s.collection_id,COALESCE(s.last_success_at,''),
		(SELECT COUNT(*) FROM wp_update_tasks t WHERE t.site_id=w.id AND (t.status IN ('preparing','queued','running') OR (t.status='interrupted_unknown' AND t.manual_disposition='')))
		FROM websites w JOIN site_wp_inventory_state s ON s.site_id=w.id WHERE w.id=?`, siteID).
		Scan(&c.siteID, &c.domain, &c.webRoot, &status, &siteType, &inventoryStatus, &c.currentVersion, &stateLocale, &multisite, &c.collectionID, &lastSuccess, &blocked)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrWPCoreUpdateNotFound
	}
	if err != nil {
		return c, err
	}
	parsedSuccess, parseErr := parseRequiredWPInventoryTime(lastSuccess)
	if parseErr != nil {
		return c, ErrWPCoreUpdateConflict
	}
	c.lastSuccess = parsedSuccess
	if status != "active" || siteType != "wordpress" || inventoryStatus != "complete" || multisite != 0 || c.collectionID == "" || c.currentVersion == "" || stateLocale == "" || blocked != 0 || !filepath.IsAbs(c.webRoot) || sTimeStale(c.lastSuccess, s.now().UTC()) {
		return c, ErrWPCoreUpdateConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT target_version,locale FROM site_wp_component_updates
		WHERE site_id=? AND collection_id=? AND component_type='core' AND component_key='wordpress' AND response='upgrade'`, siteID, c.collectionID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&c.targetVersion, &c.locale); err != nil {
			return c, err
		}
	}
	if rows.Err() != nil {
		return c, rows.Err()
	}
	if count == 0 {
		return c, errWPCoreUpdateNoCandidate
	}
	// A target at or below the installed version is not an error: it means the
	// cached candidate is already satisfied (e.g. the site was rescanned or
	// updated out-of-band after the candidate was stored). Treat it as "no
	// update available" rather than a scary conflict that forces a recheck.
	if compareWPVersions(c.targetVersion, c.currentVersion) <= 0 {
		return c, errWPCoreUpdateNoCandidate
	}
	if count != 1 || c.targetVersion == "" || c.locale == "" || c.locale != stateLocale {
		return c, ErrWPCoreUpdateConflict
	}
	if err := tx.Commit(); err != nil {
		return c, err
	}
	return c, nil
}

func sTimeStale(value time.Time, now time.Time) bool {
	return value.IsZero() || value.After(now.Add(time.Minute)) || now.Sub(value) > 12*time.Hour
}

func (s *WPCoreUpdateService) downloadAndSeal(ctx context.Context, task WPUpdateTask, record wpCoreConfirmation) (WPUpdateTask, error) {
	taskDir, err := s.artifacts.createTaskDir(task.ID)
	if err != nil {
		return WPUpdateTask{}, err
	}
	target := filepath.Join(taskDir, "package.zip")
	packages, err := NewWPPackageService(target, nil)
	if err != nil {
		return WPUpdateTask{}, err
	}
	report, err := packages.download(ctx, record.downloadURL)
	if err != nil || report.Version != record.targetVersion || report.Locale != record.locale {
		return WPUpdateTask{}, errors.New("core update package identity mismatch")
	}
	checksums, err := defaultWPCoreChecksumFetcher(nil)(ctx, record.targetVersion, record.locale)
	if err != nil || validateWPCoreChecksumSet(checksums, record.targetVersion, record.locale) != nil || verifyWPCorePackageChecksums(target, checksums) != nil {
		return WPUpdateTask{}, errors.New("core update package verification failed")
	}
	if err := os.Chmod(target, 0600); err != nil {
		return WPUpdateTask{}, err
	}
	sha, _, err := hashRegularFile(target)
	if err != nil {
		return WPUpdateTask{}, err
	}
	return s.store.sealPlan(ctx, task.ID, sha, "official_verified", target, s.now().UTC())
}

func (s *WPCoreUpdateService) taskModel(ctx context.Context, task WPUpdateTask, includeEvents bool) (models.WPCoreUpdateTask, error) {
	requested, err := parseRequiredWPInventoryTime(task.RequestedAt)
	if err != nil {
		return models.WPCoreUpdateTask{}, err
	}
	started, err := parseOptionalWPInventoryTime(task.StartedAt)
	if err != nil {
		return models.WPCoreUpdateTask{}, err
	}
	finished, err := parseOptionalWPInventoryTime(task.FinishedAt)
	if err != nil {
		return models.WPCoreUpdateTask{}, err
	}
	model := models.WPCoreUpdateTask{ID: task.ID, SiteID: task.SiteID, ComponentType: task.ComponentType, TaskKind: task.TaskKind,
		Status: task.Status, Stage: task.Stage, FailureStage: task.FailureStage, RollbackStatus: task.RollbackStatus,
		RequiresAttention: task.RequiresAttention, ManualDisposition: task.ManualDisposition, CurrentVersion: task.CurrentVersion,
		TargetVersion: task.TargetVersion, VerificationLevel: task.VerificationLevel, DatabaseBackupMode: task.DatabaseBackupMode,
		RequestedAt: requested, StartedAt: started, FinishedAt: finished}
	if !includeEvents {
		return model, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT stage,result,error_code,created_at FROM
		(SELECT id,stage,result,error_code,created_at FROM wp_update_task_events WHERE task_id=? ORDER BY id DESC LIMIT 50)
		ORDER BY id`, task.ID)
	if err != nil {
		return model, err
	}
	defer rows.Close()
	for rows.Next() {
		var event models.WPCoreUpdateTaskEvent
		var created string
		if err := rows.Scan(&event.Stage, &event.Result, &event.ErrorCode, &created); err != nil {
			return model, err
		}
		event.CreatedAt, err = parseRequiredWPInventoryTime(created)
		if err != nil {
			return model, err
		}
		model.Events = append(model.Events, event)
	}
	return model, rows.Err()
}

func defaultWPCoreOfferFetcher(client *http.Client) wpCoreOfferFetcher {
	if client == nil {
		client = defaultWPPackageHTTPClient()
	}
	return func(ctx context.Context, current, locale, phpVersion, mysqlVersion string) (wpCoreVersionOffer, error) {
		u, _ := url.Parse("https://api.wordpress.org/core/version-check/1.7/")
		q := u.Query()
		q.Set("version", current)
		q.Set("locale", locale)
		q.Set("php", phpVersion)
		q.Set("mysql", mysqlVersion)
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return wpCoreVersionOffer{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return wpCoreVersionOffer{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL.Hostname() != "api.wordpress.org" {
			return wpCoreVersionOffer{}, errors.New("version offer unavailable")
		}
		var body struct {
			Offers []struct {
				Response     string `json:"response"`
				Version      string `json:"version"`
				Locale       string `json:"locale"`
				PHPVersion   string `json:"php_version"`
				MySQLVersion string `json:"mysql_version"`
				Packages     struct {
					Full string `json:"full"`
				} `json:"packages"`
			} `json:"offers"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
			return wpCoreVersionOffer{}, err
		}
		for _, offer := range body.Offers {
			if offer.Response == "upgrade" && offer.Version != "" && offer.Locale == locale {
				return wpCoreVersionOffer{Version: offer.Version, Locale: offer.Locale, DownloadURL: offer.Packages.Full, PHPMin: offer.PHPVersion, MySQLMin: offer.MySQLVersion}, nil
			}
		}
		return wpCoreVersionOffer{}, errors.New("version offer missing")
	}
}

func defaultWPCoreInstalledVersions(ctx context.Context) (string, string, error) {
	php, err := validateInventoryBinary(wpInventoryPHPPath, "/usr/bin", 0, 0)
	if err != nil {
		return "", "", err
	}
	phpOut, err := exec.CommandContext(ctx, php, "-r", "echo PHP_VERSION;").Output()
	if err != nil {
		return "", "", err
	}
	password := readMariaDBPassword()
	mysql, err := validateInventoryBinary(wpCoreMySQLPath, "/usr/bin", 0, 0)
	if err != nil || password == "" {
		return "", "", errors.New("database version unavailable")
	}
	cmd := exec.CommandContext(ctx, mysql, "-u", "root", "-B", "-N", "-e", "SELECT VERSION()")
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	mysqlOut, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(phpOut)), strings.TrimSpace(string(mysqlOut)), nil
}

func compareWPVersions(actual, required string) int {
	if required == "" {
		return 0
	}
	parse := func(value string) [3]int {
		var out [3]int
		parts := strings.FieldsFunc(value, func(r rune) bool { return r < '0' || r > '9' })
		for i := 0; i < len(parts) && i < 3; i++ {
			out[i], _ = strconv.Atoi(parts[i])
		}
		return out
	}
	a, b := parse(actual), parse(required)
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func validWPCoreUpdatePackageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return (host == "wordpress.org" || host == "downloads.wordpress.org") && strings.HasSuffix(strings.ToLower(u.Path), ".zip")
}
