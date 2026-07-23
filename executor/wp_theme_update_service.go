package executor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

var (
	ErrWPThemeUpdateInvalid     = errors.New("invalid theme update request")
	ErrWPThemeUpdateNotFound    = errors.New("theme update resource not found")
	ErrWPThemeUpdateConflict    = errors.New("theme update request conflict")
	ErrWPThemeUpdateBusy        = errors.New("theme update service busy")
	ErrWPThemeUpdateUnavailable = errors.New("theme update upstream unavailable")
)

type wpThemeOffer struct {
	Slug, Version, DownloadURL string
}

type wpThemeOfferFetcher func(context.Context, string) (wpThemeOffer, error)
type wpThemePackageDownloader func(context.Context, string, string) (string, string, error)

type WPThemeUpdateService struct {
	db            *sql.DB
	store         *wpUpdateStore
	artifacts     *wpUpdateArtifactService
	confirmations *wpThemeConfirmationStore
	fetchOffer    wpThemeOfferFetcher
	download      wpThemePackageDownloader
	now           func() time.Time
}

type wpThemeUpdateCandidate struct {
	siteID                                            int
	domain, webRoot, collectionID, componentKey, name string
	currentVersion, targetVersion, template           string
	currentTheme                                      bool
	lastSuccess                                       time.Time
}

func NewWPThemeUpdateService(db *sql.DB, backupDir string) (*WPThemeUpdateService, error) {
	if db == nil || !validWPCoreUpdateRoot(backupDir) {
		return nil, ErrWPThemeUpdateInvalid
	}
	store := newWPUpdateStore(db)
	artifacts, err := newWPUpdateArtifactService(store, filepath.Join(filepath.Clean(backupDir), "wp-updates"), defaultWPUpdateDatabaseDumper)
	if err != nil {
		return nil, ErrWPThemeUpdateUnavailable
	}
	return &WPThemeUpdateService{
		db: db, store: store, artifacts: artifacts, confirmations: newWPThemeConfirmationStore(),
		fetchOffer: defaultWPThemeOfferFetcher(nil), download: defaultWPThemePackageDownloader(nil, artifacts.root), now: time.Now,
	}, nil
}

func (s *WPThemeUpdateService) Preview(ctx context.Context, siteID int, username, componentKey string) (models.WPThemeUpdatePreview, error) {
	if s == nil || siteID <= 0 || username == "" || !validWPThemeComponentKey(componentKey) {
		return models.WPThemeUpdatePreview{}, ErrWPThemeUpdateInvalid
	}
	candidate, err := s.loadCandidate(ctx, siteID, componentKey)
	if err != nil {
		return models.WPThemeUpdatePreview{}, err
	}
	if wpConfigHasUserFileModsLock(candidate.webRoot) {
		return models.WPThemeUpdatePreview{}, ErrWPThemeUpdateConflict
	}
	offer, err := s.fetchOffer(ctx, componentKey)
	if err != nil {
		return models.WPThemeUpdatePreview{}, ErrWPThemeUpdateUnavailable
	}
	if offer.Slug != componentKey || offer.Version != candidate.targetVersion ||
		!validWPThemeDownloadURL(offer.DownloadURL, componentKey, candidate.targetVersion) {
		return models.WPThemeUpdatePreview{}, ErrWPThemeUpdateConflict
	}
	record, err := s.confirmations.create(wpThemeConfirmation{
		username: username, siteID: siteID, domain: candidate.domain, collectionID: candidate.collectionID,
		componentKey: componentKey, currentVersion: candidate.currentVersion, targetVersion: candidate.targetVersion,
		downloadURL: offer.DownloadURL, template: candidate.template, currentTheme: candidate.currentTheme,
	})
	if err != nil {
		return models.WPThemeUpdatePreview{}, ErrWPThemeUpdateBusy
	}
	expires := record.expiresAt
	return models.WPThemeUpdatePreview{
		Available: true, SiteID: siteID, Domain: candidate.domain, ComponentKey: componentKey, Name: candidate.name,
		CurrentVersion: candidate.currentVersion, TargetVersion: candidate.targetVersion, Template: candidate.template,
		CurrentTheme: candidate.currentTheme, PackageSource: "wordpress.org", VerificationRequired: "structure_only",
		DatabaseBackup: true, ThemeFilesBackup: true, ConfirmationToken: record.token, RiskToken: record.riskToken, ExpiresAt: &expires,
	}, nil
}

func (s *WPThemeUpdateService) Confirm(ctx context.Context, siteID int, username, componentKey, token, riskToken, target string) (models.WPThemeUpdateTask, error) {
	if s == nil || siteID <= 0 || username == "" || !validWPThemeComponentKey(componentKey) || token == "" || target == "" {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateInvalid
	}
	record, err := s.confirmations.consume(token, riskToken, username, siteID, componentKey, target)
	if err != nil {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateConflict
	}
	candidate, err := s.loadCandidate(ctx, siteID, componentKey)
	if err != nil || candidate.collectionID != record.collectionID || candidate.currentVersion != record.currentVersion ||
		candidate.targetVersion != record.targetVersion || candidate.template != record.template ||
		candidate.currentTheme != record.currentTheme || wpConfigHasUserFileModsLock(candidate.webRoot) {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateConflict
	}
	task, err := s.store.createThemeManualPlan(ctx, WPUpdatePlan{
		SiteID: siteID, ComponentKey: componentKey, CurrentVersion: record.currentVersion, TargetVersion: record.targetVersion,
		PackageSource: "wordpress.org", DownloadURL: record.downloadURL,
	}, s.now().UTC())
	if err != nil {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateConflict
	}
	tempPath, digest, err := s.download(ctx, record.downloadURL, record.targetVersion)
	if err == nil {
		defer os.Remove(tempPath)
		_, _, err = s.artifacts.snapshotValidateAndSealThemePackage(ctx, task.ID, tempPath, digest, record.template)
	}
	if err != nil {
		if failErr := s.store.failPreparingPlan(context.Background(), task.ID, "package_prepare_failed", s.now().UTC()); failErr == nil {
			_ = os.RemoveAll(filepath.Join(s.artifacts.root, task.ID))
		}
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateUnavailable
	}
	sealed, err := s.store.getTask(ctx, task.ID)
	if err != nil {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateUnavailable
	}
	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO operation_logs(operation,target,status,message)
		VALUES('wp_theme_update_request',?,'success',?)`, record.domain, "task="+sealed.ID+" component="+sealed.ComponentKey+" version="+sealed.TargetVersion)
	return s.taskModel(ctx, sealed, false)
}

func (s *WPThemeUpdateService) Task(ctx context.Context, siteID int, taskID string) (models.WPThemeUpdateTask, error) {
	if s == nil || siteID <= 0 || !wpUpdateTaskIDPattern.MatchString(taskID) {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateInvalid
	}
	task, err := s.store.getTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (task.SiteID != siteID || task.TaskKind != "update" || task.ComponentType != "theme") {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateNotFound
	}
	if err != nil {
		return models.WPThemeUpdateTask{}, err
	}
	return s.taskModel(ctx, task, true)
}

func (s *WPThemeUpdateService) LatestTask(ctx context.Context, siteID int, componentKey string) (models.WPThemeUpdateTask, error) {
	if s == nil || siteID <= 0 || !validWPThemeComponentKey(componentKey) {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateInvalid
	}
	task, err := s.store.latestThemeUpdateTask(ctx, siteID, componentKey)
	if errors.Is(err, sql.ErrNoRows) {
		return models.WPThemeUpdateTask{}, ErrWPThemeUpdateNotFound
	}
	if err != nil {
		return models.WPThemeUpdateTask{}, err
	}
	return s.taskModel(ctx, task, true)
}

func (s *WPThemeUpdateService) taskModel(ctx context.Context, task WPUpdateTask, includeEvents bool) (models.WPThemeUpdateTask, error) {
	helper := &WPPluginUpdateService{db: s.db}
	return helper.taskModel(ctx, task, includeEvents)
}

func (s *WPThemeUpdateService) loadCandidate(ctx context.Context, siteID int, componentKey string) (wpThemeUpdateCandidate, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return wpThemeUpdateCandidate{}, err
	}
	defer tx.Rollback()
	var c wpThemeUpdateCandidate
	var siteStatus, siteType, inventoryStatus, lastSuccess string
	var multisite, blocked, currentTheme int
	err = tx.QueryRowContext(ctx, `SELECT w.id,w.domain,w.web_root,w.status,w.site_type,s.status,s.collection_id,
		s.is_multisite,COALESCE(s.last_success_at,''),
		(SELECT COUNT(*) FROM wp_update_tasks t WHERE t.site_id=w.id AND
			(t.status IN ('preparing','queued','running') OR (t.status='interrupted_unknown' AND t.manual_disposition='')))
		FROM websites w JOIN site_wp_inventory_state s ON s.site_id=w.id WHERE w.id=?`, siteID).
		Scan(&c.siteID, &c.domain, &c.webRoot, &siteStatus, &siteType, &inventoryStatus, &c.collectionID, &multisite, &lastSuccess, &blocked)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrWPThemeUpdateNotFound
	}
	if err != nil {
		return c, err
	}
	parsedSuccess, err := parseRequiredWPInventoryTime(lastSuccess)
	if err != nil {
		return c, ErrWPThemeUpdateConflict
	}
	c.lastSuccess = parsedSuccess
	if siteStatus != "active" || siteType != "wordpress" || inventoryStatus != "complete" || multisite != 0 ||
		c.collectionID == "" || blocked != 0 || !filepath.IsAbs(c.webRoot) || sTimeStale(c.lastSuccess, s.now().UTC()) {
		return c, ErrWPThemeUpdateConflict
	}
	c.componentKey = componentKey
	rows, err := tx.QueryContext(ctx, `SELECT c.name,c.version,c.is_current_theme,u.target_version
		FROM site_wp_components c JOIN site_wp_component_updates u
			ON u.site_id=c.site_id AND u.collection_id=c.collection_id AND u.component_type=c.component_type
			AND u.component_key=c.component_key
		WHERE c.site_id=? AND c.collection_id=? AND c.component_type='theme' AND c.component_key=?
			AND u.response='upgrade'`, siteID, c.collectionID, componentKey)
	if err != nil {
		return c, err
	}
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&c.name, &c.currentVersion, &currentTheme, &c.targetVersion); err != nil {
			rows.Close()
			return c, err
		}
	}
	if err := rows.Close(); err != nil {
		return c, err
	}
	if count == 0 {
		return c, ErrWPThemeUpdateNotFound
	}
	if count != 1 || c.name == "" || !wpComponentVersionPattern.MatchString(c.currentVersion) ||
		!wpComponentVersionPattern.MatchString(c.targetVersion) || c.currentVersion == c.targetVersion {
		return c, ErrWPThemeUpdateConflict
	}
	c.currentTheme = currentTheme != 0
	version, template, err := readInstalledWPThemeIdentity(c.webRoot, componentKey)
	if err != nil || version != c.currentVersion {
		return c, ErrWPThemeUpdateConflict
	}
	c.template = template
	if err := tx.Commit(); err != nil {
		return c, err
	}
	return c, nil
}

func defaultWPThemeOfferFetcher(client *http.Client) wpThemeOfferFetcher {
	if client == nil {
		client = defaultWPPackageHTTPClient()
	}
	return func(ctx context.Context, slug string) (wpThemeOffer, error) {
		if !validWPThemeComponentKey(slug) {
			return wpThemeOffer{}, errors.New("invalid theme slug")
		}
		u, _ := url.Parse("https://api.wordpress.org/themes/info/1.2/")
		q := u.Query()
		q.Set("action", "theme_information")
		q.Set("request[slug]", slug)
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return wpThemeOffer{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return wpThemeOffer{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.URL.Scheme != "https" ||
			resp.Request.URL.Hostname() != "api.wordpress.org" {
			return wpThemeOffer{}, errors.New("theme offer unavailable")
		}
		var body struct {
			Slug         string `json:"slug"`
			Version      string `json:"version"`
			DownloadLink string `json:"download_link"`
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
		if err != nil || len(raw) > 2<<20 {
			return wpThemeOffer{}, errors.New("theme offer invalid")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return wpThemeOffer{}, errors.New("theme offer invalid")
		}
		return wpThemeOffer{Slug: body.Slug, Version: body.Version, DownloadURL: body.DownloadLink}, nil
	}
}

func defaultWPThemePackageDownloader(client *http.Client, artifactRoot string) wpThemePackageDownloader {
	if client == nil {
		client = defaultWPPackageHTTPClient()
	}
	return func(ctx context.Context, rawURL, targetVersion string) (string, string, error) {
		u, err := url.Parse(rawURL)
		if err != nil || u == nil {
			return "", "", errors.New("invalid theme package download")
		}
		suffix := "." + targetVersion + ".zip"
		base := path.Base(u.Path)
		if len(base) <= len(suffix) {
			return "", "", errors.New("invalid theme package download")
		}
		slug := base[:len(base)-len(suffix)]
		if !wpComponentVersionPattern.MatchString(targetVersion) ||
			!validWPThemeDownloadURL(rawURL, slug, targetVersion) {
			return "", "", errors.New("invalid theme package download")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Request == nil ||
			!validWPThemeDownloadURL(resp.Request.URL.String(), slug, targetVersion) {
			return "", "", errors.New("theme package download failed")
		}
		temp, err := os.CreateTemp(artifactRoot, ".theme-download-*.zip")
		if err != nil {
			return "", "", err
		}
		name := temp.Name()
		keep := false
		defer func() {
			_ = temp.Close()
			if !keep {
				_ = os.Remove(name)
			}
		}()
		if err := temp.Chmod(0600); err != nil {
			return "", "", err
		}
		written, err := io.Copy(temp, io.LimitReader(resp.Body, wpDownloadMaxBytes+1))
		if err != nil || written == 0 || written > wpDownloadMaxBytes {
			return "", "", errors.New("theme package download invalid")
		}
		if err := temp.Sync(); err != nil {
			return "", "", err
		}
		if err := temp.Close(); err != nil {
			return "", "", err
		}
		digest, size, err := hashRegularFile(name)
		if err != nil || size != written {
			return "", "", errors.New("theme package download changed")
		}
		keep = true
		return name, digest, nil
	}
}
