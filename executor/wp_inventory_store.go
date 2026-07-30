package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const wpInventoryDBTimeLayout = "2006-01-02 15:04:05"

type wpInventoryTrigger string

const (
	wpInventoryTriggerManual         wpInventoryTrigger = "manual"
	wpInventoryTriggerSiteCreated    wpInventoryTrigger = "site_created"
	wpInventoryTriggerUpdateFollowup wpInventoryTrigger = "update_followup"
	wpInventoryTriggerScheduled      wpInventoryTrigger = "scheduled"
)

type wpInventoryJobStatus string

const (
	wpInventoryJobQueued    wpInventoryJobStatus = "queued"
	wpInventoryJobRunning   wpInventoryJobStatus = "running"
	wpInventoryJobSucceeded wpInventoryJobStatus = "succeeded"
	wpInventoryJobFailed    wpInventoryJobStatus = "failed"
)

const (
	wpInventoryErrorSiteChanged   = "site_changed_during_collection"
	wpInventoryErrorRepeatedCrash = "inventory_worker_repeated_crash"
	wpInventoryErrorStageStore    = "store"
	wpInventoryErrorStageWorker   = "worker"
	wpInventoryWarningUnknown     = WPInventoryWarning("unknown_runner_entry")
)

var (
	errWPInventoryJobNotFound             = errors.New("wordpress inventory job not found")
	errWPInventoryLeaseLost               = errors.New("wordpress inventory job lease lost")
	errWPInventorySiteChanged             = errors.New("wordpress inventory site changed")
	errWPInventoryScheduledSiteIneligible = errors.New("wordpress inventory scheduled site ineligible")
)

type wpInventoryStore struct {
	db *sql.DB
}

type wpInventoryQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type wpInventorySiteIdentity struct {
	ID         int
	Domain     string
	Status     models.WebsiteStatus
	SystemUser string
	WebRoot    string
	SiteType   string
}

type wpInventoryJob struct {
	ID                 string
	SiteID             int
	Trigger            wpInventoryTrigger
	Status             wpInventoryJobStatus
	RequestedAt        string
	NotBefore          string
	LeaseOwner         string
	LeaseExpiresAt     string
	AttemptCount       int
	LeaseRecoveryCount int
	StartedAt          string
	ErrorCode          string
	ErrorStage         string
	FinishedAt         string
	TimedOut           bool
	ExitCode           int
	WallTimeMS         int64
	UserCPUMS          int64
	SystemCPUMS        int64
	MaxRSSKiB          int64
	StdoutBytes        int64
	StderrBytes        int64
	ProtocolBytes      int64
	RunnerHash         string
	RunnerVersion      string
	SchemaVersion      int
}

type wpInventoryState struct {
	SiteID             int
	Status             string
	WordPressVersion   string
	WordPressLocale    string
	Multisite          bool
	CurrentThemeKey    string
	PluginCount        int
	ActivePluginCount  int
	ThemeCount         int
	CoreUpdateCount    int
	PluginUpdateCount  int
	ThemeUpdateCount   int
	CoreTransient      bool
	CoreLastChecked    int64
	CoreVersionChecked string
	PluginTransient    bool
	PluginLastChecked  int64
	ThemeTransient     bool
	ThemeLastChecked   int64
	CollectionID       string
	LastAttemptAt      string
	LastSuccessAt      string
	LastErrorCode      string
	LastErrorStage     string
}

type wpInventoryStoredComponent struct {
	Type          string
	Key           string
	Name          string
	Version       string
	Active        bool
	NetworkActive bool
	CurrentTheme  bool
	CollectionID  string
	CollectedAt   string
}

type wpInventoryStoredUpdate struct {
	Type           string
	Key            string
	CurrentVersion string
	Version        string
	Response       string
	Locale         string
	CollectionID   string
	CollectedAt    string
}

type wpInventorySummarySnapshot struct {
	Identity             wpInventorySiteIdentity
	State                wpInventoryState
	ActiveJob            *wpInventoryJob
	CoreUpgradeAvailable bool
}

type wpInventoryComponentPageSnapshot struct {
	State wpInventoryState
	Items []wpInventoryStoredComponent
	Total int
}

type wpInventoryUpdatePageSnapshot struct {
	State wpInventoryState
	Items []wpInventoryStoredUpdate
	Total int
}

const wpInventoryComponentPageWhereSQL = `site_id = ? AND collection_id = ?
	AND component_type IN ('plugin','theme')
	AND (? = '' OR component_type = ?)
	AND (? = '' OR component_key LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR version LIKE ? ESCAPE '\')`

const wpInventoryUpdatePageWhereSQL = `u.site_id = ? AND u.collection_id = ?
	AND (u.component_type <> 'core' OR u.response = 'upgrade')
	AND (? = '' OR u.component_type = ?)
	AND (? = '' OR u.component_key LIKE ? ESCAPE '\' OR u.target_version LIKE ? ESCAPE '\')`

func newWPInventoryStore() (*wpInventoryStore, error) {
	return newWPInventoryStoreWithDB(database.GetDB())
}

func newWPInventoryStoreWithDB(db *sql.DB) (*wpInventoryStore, error) {
	if db == nil {
		return nil, errors.New("wordpress inventory database is nil")
	}
	return &wpInventoryStore{db: db}, nil
}

func (s *wpInventoryStore) enqueue(ctx context.Context, siteID int, trigger wpInventoryTrigger, requestedAt, notBefore time.Time) (string, bool, error) {
	if siteID <= 0 || !validWPInventoryTrigger(trigger) || requestedAt.IsZero() || notBefore.IsZero() {
		return "", false, errors.New("invalid wordpress inventory enqueue request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	jobID, created, err := enqueueWPInventoryJobTx(ctx, tx, siteID, trigger, requestedAt, notBefore)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return jobID, created, nil
}

func (s *wpInventoryStore) enqueueEligibleManual(ctx context.Context, siteID int, requestedAt time.Time) (wpInventoryJob, bool, error) {
	return s.enqueueEligible(ctx, siteID, wpInventoryTriggerManual, requestedAt)
}

func (s *wpInventoryStore) enqueueEligibleUpdateFollowup(ctx context.Context, siteID int, requestedAt time.Time) (wpInventoryJob, bool, error) {
	return s.enqueueEligible(ctx, siteID, wpInventoryTriggerUpdateFollowup, requestedAt)
}

func (s *wpInventoryStore) enqueueEligible(ctx context.Context, siteID int, trigger wpInventoryTrigger, requestedAt time.Time) (wpInventoryJob, bool, error) {
	if siteID <= 0 || requestedAt.IsZero() || (trigger != wpInventoryTriggerManual && trigger != wpInventoryTriggerUpdateFollowup) {
		return wpInventoryJob{}, false, ErrWPInventoryInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wpInventoryJob{}, false, err
	}
	defer tx.Rollback()

	identity, err := loadWPInventorySiteIdentity(ctx, tx, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return wpInventoryJob{}, false, ErrWPInventorySiteNotFound
	}
	if err != nil {
		return wpInventoryJob{}, false, err
	}
	if identity.SiteType != "wordpress" {
		return wpInventoryJob{}, false, ErrWPInventoryUnsupportedSite
	}
	switch identity.Status {
	case models.StatusActive, models.StatusPaused, models.StatusError:
	default:
		return wpInventoryJob{}, false, ErrWPInventorySiteUnavailable
	}

	jobID, created, err := enqueueWPInventoryJobTx(ctx, tx, siteID, trigger, requestedAt, requestedAt)
	if err != nil {
		return wpInventoryJob{}, false, err
	}
	job, err := loadWPInventoryJob(ctx, tx, `SELECT id, site_id, trigger_type, status, requested_at, not_before,
		lease_owner, COALESCE(lease_expires_at, ''), attempt_count, lease_recovery_count,
		COALESCE(started_at, ''), error_code, error_stage, COALESCE(finished_at, ''),
		timed_out, exit_code, wall_time_ms, user_cpu_ms, system_cpu_ms, max_rss_kib,
		stdout_bytes, stderr_bytes, protocol_bytes, runner_hash, runner_version, inventory_schema_version
		FROM site_wp_inventory_jobs WHERE id = ? AND site_id = ?`, jobID, siteID)
	if err != nil {
		return wpInventoryJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return wpInventoryJob{}, false, err
	}
	return job, created, nil
}

func (s *wpInventoryStore) enqueueEligibleScheduled(ctx context.Context, siteID int, requestedAt time.Time) (string, bool, error) {
	if siteID <= 0 || requestedAt.IsZero() {
		return "", false, errors.New("invalid wordpress inventory scheduled enqueue request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	identity, err := loadWPInventorySiteIdentity(ctx, tx, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, errWPInventoryScheduledSiteIneligible
	}
	if err != nil {
		return "", false, err
	}
	if identity.SiteType != "wordpress" {
		return "", false, errWPInventoryScheduledSiteIneligible
	}
	switch identity.Status {
	case models.StatusActive, models.StatusPaused, models.StatusError:
	default:
		return "", false, errWPInventoryScheduledSiteIneligible
	}
	jobID, created, err := enqueueWPInventoryJobTx(ctx, tx, siteID, wpInventoryTriggerScheduled, requestedAt, requestedAt)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return jobID, created, nil
}

func (s *wpInventoryStore) listScheduleCandidates(ctx context.Context, limit int) ([]wpInventoryScheduleSite, error) {
	if limit <= 0 || limit > wpInventoryScheduleCandidateLimit {
		return nil, errors.New("invalid wordpress inventory schedule candidate limit")
	}
	rows, err := s.db.QueryContext(ctx, `WITH latest_scheduled AS (
		SELECT site_id, MAX(requested_at) AS last_requested_at
		FROM site_wp_inventory_jobs
		WHERE trigger_type = 'scheduled'
		GROUP BY site_id
	)
	SELECT w.id, w.site_type, w.status,
		COALESCE(s.last_success_at, ''),
		COALESCE(ls.last_requested_at, '')
		FROM websites w
		LEFT JOIN site_wp_inventory_state s ON s.site_id = w.id
		LEFT JOIN latest_scheduled ls ON ls.site_id = w.id
		WHERE w.site_type = 'wordpress'
			AND w.status IN ('active','paused','error')
			AND NOT EXISTS (SELECT 1 FROM site_wp_inventory_jobs active_job
				WHERE active_job.site_id = w.id AND active_job.status IN ('queued','running'))
		ORDER BY CASE WHEN s.last_success_at IS NULL AND ls.last_requested_at IS NULL THEN 0 ELSE 1 END,
			CASE
				WHEN ls.last_requested_at IS NULL THEN s.last_success_at
				WHEN s.last_success_at IS NULL THEN ls.last_requested_at
				WHEN ls.last_requested_at > s.last_success_at THEN ls.last_requested_at
				ELSE s.last_success_at
			END,
			w.id
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]wpInventoryScheduleSite, 0)
	for rows.Next() {
		var site wpInventoryScheduleSite
		var status, lastSuccess, lastScheduled string
		if err := rows.Scan(&site.ID, &site.SiteType, &status, &lastSuccess, &lastScheduled); err != nil {
			return nil, err
		}
		site.Status = models.WebsiteStatus(status)
		site.LastSuccess, err = parseOptionalWPInventoryTime(lastSuccess)
		if err != nil {
			return nil, err
		}
		site.LastScheduledRequest, err = parseOptionalWPInventoryTime(lastScheduled)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, site)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *wpInventoryStore) claim(ctx context.Context, owner string, now time.Time, lease time.Duration) (*wpInventoryJob, error) {
	if !validWPInventoryLeaseOwner(owner) || now.IsZero() || lease <= 0 {
		return nil, errors.New("invalid wordpress inventory lease")
	}
	nowValue := wpInventoryDBTime(now)
	leaseValue := wpInventoryDBTime(now.Add(lease))
	row := s.db.QueryRowContext(ctx, `UPDATE site_wp_inventory_jobs
		SET status = 'running', lease_owner = ?, lease_expires_at = ?,
			attempt_count = attempt_count + 1, started_at = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM site_wp_inventory_jobs
			WHERE status = 'queued' AND not_before <= ?
			ORDER BY requested_at, id LIMIT 1
		)
		RETURNING id, site_id, trigger_type, status, lease_owner,
			COALESCE(lease_expires_at, ''), attempt_count, lease_recovery_count`,
		owner, leaseValue, nowValue, nowValue, nowValue)
	job := &wpInventoryJob{}
	if err := row.Scan(&job.ID, &job.SiteID, &job.Trigger, &job.Status, &job.LeaseOwner,
		&job.LeaseExpiresAt, &job.AttemptCount, &job.LeaseRecoveryCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errWPInventoryJobNotFound
		}
		return nil, err
	}
	return job, nil
}

func (s *wpInventoryStore) recoverExpired(ctx context.Context, now time.Time) (int64, int64, error) {
	if now.IsZero() {
		return 0, 0, errors.New("invalid wordpress inventory recovery time")
	}
	nowValue := wpInventoryDBTime(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `UPDATE site_wp_inventory_jobs
		SET status = 'failed', lease_owner = '', lease_expires_at = NULL,
			lease_recovery_count = lease_recovery_count + 1,
			finished_at = ?, error_code = ?, error_stage = ?, updated_at = ?
		WHERE status = 'running' AND lease_expires_at <= ? AND lease_recovery_count >= 2
		RETURNING site_id`, nowValue, wpInventoryErrorRepeatedCrash, wpInventoryErrorStageWorker, nowValue, nowValue)
	if err != nil {
		return 0, 0, err
	}
	var failedSites []int
	for rows.Next() {
		var siteID int
		if err := rows.Scan(&siteID); err != nil {
			rows.Close()
			return 0, 0, err
		}
		failedSites = append(failedSites, siteID)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	for _, siteID := range failedSites {
		if err := upsertWPInventoryFailureState(ctx, tx, siteID, nowValue, wpInventoryErrorRepeatedCrash, wpInventoryErrorStageWorker); err != nil {
			return 0, 0, err
		}
	}

	result, err := tx.ExecContext(ctx, `UPDATE site_wp_inventory_jobs
		SET status = 'queued', lease_owner = '', lease_expires_at = NULL,
			lease_recovery_count = lease_recovery_count + 1,
			started_at = NULL, not_before = ?, updated_at = ?
		WHERE status = 'running' AND lease_expires_at <= ? AND lease_recovery_count < 2`,
		nowValue, nowValue, nowValue)
	if err != nil {
		return 0, 0, err
	}
	requeued, err := result.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return requeued, int64(len(failedSites)), nil
}

func (s *wpInventoryStore) releaseOwned(ctx context.Context, jobID, owner string, now time.Time) error {
	if jobID == "" || !validWPInventoryLeaseOwner(owner) || now.IsZero() {
		return errors.New("invalid wordpress inventory release")
	}
	nowValue := wpInventoryDBTime(now)
	result, err := s.db.ExecContext(ctx, `UPDATE site_wp_inventory_jobs
		SET status = 'queued', lease_owner = '', lease_expires_at = NULL,
			started_at = NULL, not_before = ?, updated_at = ?
		WHERE id = ? AND status = 'running' AND lease_owner = ?`,
		nowValue, nowValue, jobID, owner)
	if err != nil {
		return err
	}
	return requireWPInventoryRow(result)
}

func (s *wpInventoryStore) loadSiteIdentity(ctx context.Context, siteID int) (wpInventorySiteIdentity, error) {
	return loadWPInventorySiteIdentity(ctx, s.db, siteID)
}

func (s *wpInventoryStore) persistSuccess(ctx context.Context, jobID, owner string, expected wpInventorySiteIdentity, result WPInventoryRunResult, completedAt time.Time) error {
	if jobID == "" || !validWPInventoryLeaseOwner(owner) || completedAt.IsZero() {
		return errors.New("invalid wordpress inventory success")
	}
	if err := validateWPInventory(&result.Inventory); err != nil {
		return fmt.Errorf("invalid wordpress inventory: %w", err)
	}
	if err := validateWPInventoryMeta(result.Meta, true); err != nil {
		return err
	}
	warnings, err := normalizedWPInventoryWarnings(result.Meta.Warnings)
	if err != nil {
		return err
	}
	completed := wpInventoryDBTime(completedAt)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	siteID, err := fenceWPInventoryJob(ctx, tx, jobID, owner, completed)
	if err != nil {
		return err
	}
	current, err := loadWPInventorySiteIdentity(ctx, tx, siteID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errWPInventoryLeaseLost
		}
		return err
	}
	if expected.ID != siteID || current != expected {
		if err := markWPInventorySiteChanged(ctx, tx, jobID, owner, siteID, completed); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return errWPInventorySiteChanged
	}

	for _, table := range []string{"site_wp_components", "site_wp_component_updates"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE site_id = ?", siteID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM site_wp_inventory_job_warnings WHERE job_id = ?", jobID); err != nil {
		return err
	}
	if err := insertWPInventoryComponents(ctx, tx, siteID, jobID, completed, result.Inventory); err != nil {
		return err
	}
	if err := insertWPInventoryUpdates(ctx, tx, siteID, jobID, completed, result.Inventory.Updates); err != nil {
		return err
	}
	if err := insertWPInventoryWarnings(ctx, tx, jobID, completed, warnings); err != nil {
		return err
	}
	if err := upsertWPInventorySuccessState(ctx, tx, siteID, jobID, completed, result.Inventory); err != nil {
		return err
	}
	if err := finishWPInventoryJob(ctx, tx, jobID, owner, completed, result.Meta, "", "", wpInventoryJobSucceeded); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpInventoryStore) persistFailure(ctx context.Context, jobID, owner string, runErr *WPInventoryRunError, meta WPInventoryRunMeta, completedAt time.Time) error {
	if jobID == "" || !validWPInventoryLeaseOwner(owner) || !validWPInventoryRunError(runErr) || completedAt.IsZero() {
		return errors.New("invalid wordpress inventory failure")
	}
	if err := validateWPInventoryMeta(meta, false); err != nil {
		return err
	}
	warnings, err := normalizedWPInventoryWarnings(meta.Warnings)
	if err != nil {
		return err
	}
	completed := wpInventoryDBTime(completedAt)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	siteID, err := fenceWPInventoryJob(ctx, tx, jobID, owner, completed)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM site_wp_inventory_job_warnings WHERE job_id = ?", jobID); err != nil {
		return err
	}
	if err := insertWPInventoryWarnings(ctx, tx, jobID, completed, warnings); err != nil {
		return err
	}
	if err := upsertWPInventoryFailureState(ctx, tx, siteID, completed, string(runErr.Code), string(runErr.Stage)); err != nil {
		return err
	}
	meta.ExitCode = runErr.ExitCode
	meta.TimedOut = runErr.TimedOut
	if err := finishWPInventoryJob(ctx, tx, jobID, owner, completed, meta, string(runErr.Code), string(runErr.Stage), wpInventoryJobFailed); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *wpInventoryStore) getState(ctx context.Context, siteID int) (wpInventoryState, error) {
	return loadWPInventoryState(ctx, s.db, siteID)
}

func loadWPInventoryState(ctx context.Context, queryer wpInventoryQueryer, siteID int) (wpInventoryState, error) {
	state := wpInventoryState{SiteID: siteID, Status: "unknown"}
	err := queryer.QueryRowContext(ctx, `SELECT site_id, status, wordpress_version, wordpress_locale,
		is_multisite, current_theme_key, plugin_count, active_plugin_count, theme_count,
		core_update_count, plugin_update_count, theme_update_count,
		core_updates_transient_present, core_updates_last_checked, core_version_checked,
		plugin_updates_transient_present, plugin_updates_last_checked,
		theme_updates_transient_present, theme_updates_last_checked, collection_id,
		COALESCE(last_attempt_at, ''), COALESCE(last_success_at, ''), last_error_code, last_error_stage
		FROM site_wp_inventory_state WHERE site_id = ?`, siteID).Scan(
		&state.SiteID, &state.Status, &state.WordPressVersion, &state.WordPressLocale,
		&state.Multisite, &state.CurrentThemeKey, &state.PluginCount, &state.ActivePluginCount,
		&state.ThemeCount, &state.CoreUpdateCount, &state.PluginUpdateCount,
		&state.ThemeUpdateCount, &state.CoreTransient, &state.CoreLastChecked,
		&state.CoreVersionChecked, &state.PluginTransient, &state.PluginLastChecked,
		&state.ThemeTransient, &state.ThemeLastChecked, &state.CollectionID, &state.LastAttemptAt,
		&state.LastSuccessAt, &state.LastErrorCode, &state.LastErrorStage)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	return state, err
}

func (s *wpInventoryStore) getSummarySnapshot(ctx context.Context, siteID int) (wpInventorySummarySnapshot, error) {
	if siteID <= 0 {
		return wpInventorySummarySnapshot{}, ErrWPInventoryInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return wpInventorySummarySnapshot{}, err
	}
	defer tx.Rollback()

	identity, err := loadWPInventorySiteIdentity(ctx, tx, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return wpInventorySummarySnapshot{}, ErrWPInventorySiteNotFound
	}
	if err != nil {
		return wpInventorySummarySnapshot{}, err
	}
	if identity.SiteType != "wordpress" {
		return wpInventorySummarySnapshot{}, ErrWPInventoryUnsupportedSite
	}
	state, err := loadWPInventoryState(ctx, tx, siteID)
	if err != nil {
		return wpInventorySummarySnapshot{}, err
	}
	activeJob, err := loadOptionalWPInventoryJob(ctx, tx, `SELECT id, site_id, trigger_type, status, requested_at, not_before,
		lease_owner, COALESCE(lease_expires_at, ''), attempt_count, lease_recovery_count,
		COALESCE(started_at, ''), error_code, error_stage, COALESCE(finished_at, ''),
		timed_out, exit_code, wall_time_ms, user_cpu_ms, system_cpu_ms, max_rss_kib,
		stdout_bytes, stderr_bytes, protocol_bytes, runner_hash, runner_version, inventory_schema_version
		FROM site_wp_inventory_jobs WHERE site_id = ? AND status IN ('queued','running') LIMIT 1`, siteID)
	if err != nil {
		return wpInventorySummarySnapshot{}, err
	}
	coreUpgradeAvailable, pluginCount, themeCount, err := wpInventoryLiveUpdateCounts(ctx, tx, siteID, state.CollectionID, state.WordPressVersion)
	if err != nil {
		return wpInventorySummarySnapshot{}, err
	}
	state.PluginUpdateCount = pluginCount
	state.ThemeUpdateCount = themeCount
	if err := tx.Commit(); err != nil {
		return wpInventorySummarySnapshot{}, err
	}
	return wpInventorySummarySnapshot{
		Identity: identity, State: state, ActiveJob: activeJob, CoreUpgradeAvailable: coreUpgradeAvailable,
	}, nil
}

// wpInventoryRowQueryer is satisfied by both *sql.DB and *sql.Tx, letting
// wpInventoryLiveUpdateCounts run inside an existing transaction.
type wpInventoryRowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// wpInventoryLiveUpdateCounts re-evaluates stored update candidates against the
// live installed version recorded in the same inventory snapshot, instead of
// trusting the raw candidate rows (or the counts cached at scan time in
// site_wp_inventory_state). The site's own WordPress update-check transient
// (update_core/update_plugins/update_themes) can lag the live installed
// version by hours after an update completes out-of-band (see
// refreshCoreInventoryAfterUpdate), leaving stale site_wp_component_updates
// rows whose target version is not actually newer than what is installed.
// Reporting those as "update available" misleads the site summary panel (this
// function's only caller) and the update list (getUpdatePage, which applies
// the same rule inline); the fleet overview applies the identical rule as
// scalar subqueries directly in wpFleetOverviewSQL to keep that endpoint a
// single query (see TestWPFleetOverviewUsesSingleQuery) rather than issuing a
// follow-up query per site. When the installed version of a component can't
// be determined, the candidate is kept (fail open) rather than silently
// hidden.
func wpInventoryLiveUpdateCounts(ctx context.Context, q wpInventoryRowQueryer, siteID int, collectionID, coreVersion string) (coreAvailable bool, pluginCount, themeCount int, err error) {
	if collectionID == "" {
		return false, 0, 0, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT u.component_type,
		CASE WHEN u.component_type = 'core' THEN ? ELSE COALESCE(c.version, '') END,
		u.target_version
		FROM site_wp_component_updates u
		LEFT JOIN site_wp_components c ON c.site_id = u.site_id AND c.collection_id = u.collection_id
			AND c.component_type = u.component_type AND c.component_key = u.component_key
		WHERE u.site_id = ? AND u.collection_id = ?
			AND (u.component_type <> 'core' OR u.response = 'upgrade')`,
		coreVersion, siteID, collectionID)
	if err != nil {
		return false, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var componentType, current, target string
		if err := rows.Scan(&componentType, &current, &target); err != nil {
			return false, 0, 0, err
		}
		if current != "" && compareWPVersions(target, current) <= 0 {
			continue
		}
		switch componentType {
		case "core":
			coreAvailable = true
		case "plugin":
			pluginCount++
		case "theme":
			themeCount++
		}
	}
	if err := rows.Err(); err != nil {
		return false, 0, 0, err
	}
	return coreAvailable, pluginCount, themeCount, nil
}

func (s *wpInventoryStore) getComponents(ctx context.Context, siteID int) ([]wpInventoryStoredComponent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT component_type, component_key, name, version,
		is_active, is_network_active, is_current_theme, collection_id, collected_at
		FROM site_wp_components WHERE site_id = ? ORDER BY component_type, component_key`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []wpInventoryStoredComponent
	for rows.Next() {
		var item wpInventoryStoredComponent
		if err := rows.Scan(&item.Type, &item.Key, &item.Name, &item.Version, &item.Active,
			&item.NetworkActive, &item.CurrentTheme, &item.CollectionID, &item.CollectedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *wpInventoryStore) getUpdates(ctx context.Context, siteID int) ([]wpInventoryStoredUpdate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT component_type, component_key, target_version,
		response, locale, collection_id, collected_at FROM site_wp_component_updates
		WHERE site_id = ? ORDER BY component_type, component_key, target_version, locale, response`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []wpInventoryStoredUpdate
	for rows.Next() {
		var item wpInventoryStoredUpdate
		if err := rows.Scan(&item.Type, &item.Key, &item.Version, &item.Response, &item.Locale, &item.CollectionID, &item.CollectedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *wpInventoryStore) getComponentPage(ctx context.Context, siteID int, componentType, search string, limit, offset int) (wpInventoryComponentPageSnapshot, error) {
	snapshot := wpInventoryComponentPageSnapshot{Items: make([]wpInventoryStoredComponent, 0)}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()

	state, err := loadWPInventoryListState(ctx, tx, siteID)
	if err != nil {
		return snapshot, err
	}
	snapshot.State = state
	if state.CollectionID == "" {
		if err := tx.Commit(); err != nil {
			return snapshot, err
		}
		return snapshot, nil
	}

	pattern := wpInventoryLikePattern(search)
	args := []any{siteID, state.CollectionID, componentType, componentType, search, pattern, pattern, pattern}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_wp_components WHERE `+wpInventoryComponentPageWhereSQL, args...).Scan(&snapshot.Total); err != nil {
		return snapshot, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT component_type, component_key, name, version,
		is_active, is_network_active, is_current_theme, collected_at
		FROM site_wp_components WHERE `+wpInventoryComponentPageWhereSQL+`
		ORDER BY component_type, component_key LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var item wpInventoryStoredComponent
		if err := rows.Scan(&item.Type, &item.Key, &item.Name, &item.Version, &item.Active,
			&item.NetworkActive, &item.CurrentTheme, &item.CollectedAt); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return snapshot, err
	}
	if err := rows.Close(); err != nil {
		return snapshot, err
	}
	if err := tx.Commit(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (s *wpInventoryStore) getUpdatePage(ctx context.Context, siteID int, componentType, search string, limit, offset int) (wpInventoryUpdatePageSnapshot, error) {
	snapshot := wpInventoryUpdatePageSnapshot{Items: make([]wpInventoryStoredUpdate, 0)}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()

	state, err := loadWPInventoryListState(ctx, tx, siteID)
	if err != nil {
		return snapshot, err
	}
	snapshot.State = state
	if state.CollectionID == "" {
		if err := tx.Commit(); err != nil {
			return snapshot, err
		}
		return snapshot, nil
	}

	pattern := wpInventoryLikePattern(search)
	args := []any{siteID, state.CollectionID, componentType, componentType, search, pattern, pattern}
	queryArgs := append([]any{state.WordPressVersion}, args...)
	rows, err := tx.QueryContext(ctx, `SELECT u.component_type, u.component_key,
		CASE WHEN u.component_type = 'core' THEN ? ELSE COALESCE(c.version, '') END,
		u.target_version, u.locale, u.collected_at
		FROM site_wp_component_updates u
		LEFT JOIN site_wp_components c ON c.site_id = u.site_id AND c.collection_id = u.collection_id
			AND c.component_type = u.component_type AND c.component_key = u.component_key
		WHERE `+wpInventoryUpdatePageWhereSQL+`
		ORDER BY u.component_type, u.component_key, u.target_version, u.locale, u.response`, queryArgs...)
	if err != nil {
		return snapshot, err
	}
	all := make([]wpInventoryStoredUpdate, 0)
	for rows.Next() {
		var item wpInventoryStoredUpdate
		if err := rows.Scan(&item.Type, &item.Key, &item.CurrentVersion, &item.Version, &item.Locale, &item.CollectedAt); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return snapshot, err
	}
	if err := rows.Close(); err != nil {
		return snapshot, err
	}
	// Hide candidates that are already satisfied (target version not newer than
	// the installed version). The site's own WordPress update-check transient
	// can lag the live version by hours, leaving stale rows behind; surfacing
	// them here would contradict the "no update available" result the
	// single-candidate check/preview flows already report for the same data.
	// When the installed version can't be determined, keep the candidate
	// (fail open) rather than silently hiding it.
	visible := make([]wpInventoryStoredUpdate, 0, len(all))
	for _, item := range all {
		if item.CurrentVersion == "" || compareWPVersions(item.Version, item.CurrentVersion) > 0 {
			visible = append(visible, item)
		}
	}
	snapshot.Total = len(visible)
	if offset < 0 {
		offset = 0
	}
	if offset > len(visible) {
		offset = len(visible)
	}
	end := offset + limit
	if limit <= 0 || end > len(visible) {
		end = len(visible)
	}
	snapshot.Items = visible[offset:end]
	if err := tx.Commit(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (s *wpInventoryStore) getJob(ctx context.Context, jobID string) (wpInventoryJob, error) {
	job, err := loadWPInventoryJob(ctx, s.db, `SELECT id, site_id, trigger_type, status, requested_at, not_before,
		lease_owner, COALESCE(lease_expires_at, ''), attempt_count, lease_recovery_count,
		COALESCE(started_at, ''), error_code, error_stage, COALESCE(finished_at, ''),
		timed_out, exit_code, wall_time_ms, user_cpu_ms, system_cpu_ms, max_rss_kib,
		stdout_bytes, stderr_bytes, protocol_bytes, runner_hash, runner_version, inventory_schema_version
		FROM site_wp_inventory_jobs WHERE id = ?`, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return job, errWPInventoryJobNotFound
	}
	return job, err
}

func (s *wpInventoryStore) getJobForSite(ctx context.Context, siteID int, jobID string) (wpInventoryJob, error) {
	if siteID <= 0 {
		return wpInventoryJob{}, ErrWPInventoryInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return wpInventoryJob{}, err
	}
	defer tx.Rollback()
	identity, err := loadWPInventorySiteIdentity(ctx, tx, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return wpInventoryJob{}, ErrWPInventorySiteNotFound
	}
	if err != nil {
		return wpInventoryJob{}, err
	}
	if identity.SiteType != "wordpress" {
		return wpInventoryJob{}, ErrWPInventoryUnsupportedSite
	}
	job, err := loadWPInventoryJob(ctx, tx, `SELECT id, site_id, trigger_type, status, requested_at, not_before,
		lease_owner, COALESCE(lease_expires_at, ''), attempt_count, lease_recovery_count,
		COALESCE(started_at, ''), error_code, error_stage, COALESCE(finished_at, ''),
		timed_out, exit_code, wall_time_ms, user_cpu_ms, system_cpu_ms, max_rss_kib,
		stdout_bytes, stderr_bytes, protocol_bytes, runner_hash, runner_version, inventory_schema_version
		FROM site_wp_inventory_jobs WHERE id = ? AND site_id = ?`, jobID, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return wpInventoryJob{}, ErrWPInventoryTaskNotFound
	}
	if err != nil {
		return wpInventoryJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return wpInventoryJob{}, err
	}
	return job, nil
}

func (s *wpInventoryStore) getWarnings(ctx context.Context, jobID string) ([]WPInventoryWarning, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT warning_code FROM site_wp_inventory_job_warnings
		WHERE job_id = ? ORDER BY warning_code`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var warnings []WPInventoryWarning
	for rows.Next() {
		var warning WPInventoryWarning
		if err := rows.Scan(&warning); err != nil {
			return nil, err
		}
		warnings = append(warnings, warning)
	}
	return warnings, rows.Err()
}

func (s *wpInventoryStore) prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit <= 0 || limit > 200 {
		return 0, errors.New("invalid wordpress inventory prune limit")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM site_wp_inventory_jobs WHERE id IN (
		SELECT id FROM site_wp_inventory_jobs
		WHERE status IN ('succeeded','failed') AND finished_at < ?
		ORDER BY finished_at, id LIMIT ?
	)`, wpInventoryDBTime(before), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func enqueueWPInventoryJobTx(ctx context.Context, tx *sql.Tx, siteID int, trigger wpInventoryTrigger, requestedAt, notBefore time.Time) (string, bool, error) {
	jobID, err := newWPInventoryJobID()
	if err != nil {
		return "", false, err
	}
	requested := wpInventoryDBTime(requestedAt)
	notBeforeValue := wpInventoryDBTime(notBefore)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO site_wp_inventory_jobs
		(id, site_id, trigger_type, status, requested_at, not_before, created_at, updated_at)
		VALUES (?, ?, ?, 'queued', ?, ?, ?, ?)`,
		jobID, siteID, trigger, requested, notBeforeValue, requested, requested)
	if err != nil {
		return "", false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if inserted == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM site_wp_inventory_jobs
			WHERE site_id = ? AND status IN ('queued','running') LIMIT 1`, siteID).Scan(&jobID); err != nil {
			return "", false, err
		}
	}
	return jobID, inserted == 1, nil
}

func loadWPInventoryJob(ctx context.Context, queryer wpInventoryQueryer, query string, args ...any) (wpInventoryJob, error) {
	job := wpInventoryJob{}
	err := queryer.QueryRowContext(ctx, query, args...).Scan(
		&job.ID, &job.SiteID, &job.Trigger, &job.Status, &job.RequestedAt, &job.NotBefore, &job.LeaseOwner,
		&job.LeaseExpiresAt, &job.AttemptCount, &job.LeaseRecoveryCount,
		&job.StartedAt, &job.ErrorCode, &job.ErrorStage, &job.FinishedAt,
		&job.TimedOut, &job.ExitCode, &job.WallTimeMS, &job.UserCPUMS, &job.SystemCPUMS,
		&job.MaxRSSKiB, &job.StdoutBytes, &job.StderrBytes, &job.ProtocolBytes,
		&job.RunnerHash, &job.RunnerVersion, &job.SchemaVersion)
	return job, err
}

func loadOptionalWPInventoryJob(ctx context.Context, queryer wpInventoryQueryer, query string, args ...any) (*wpInventoryJob, error) {
	job, err := loadWPInventoryJob(ctx, queryer, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func loadWPInventorySiteIdentity(ctx context.Context, queryer wpInventoryQueryer, siteID int) (wpInventorySiteIdentity, error) {
	var identity wpInventorySiteIdentity
	var status string
	err := queryer.QueryRowContext(ctx, `SELECT id, domain, status, system_user, web_root, site_type
		FROM websites WHERE id = ?`, siteID).Scan(&identity.ID, &identity.Domain, &status,
		&identity.SystemUser, &identity.WebRoot, &identity.SiteType)
	identity.Status = models.WebsiteStatus(status)
	return identity, err
}

func loadWPInventoryListState(ctx context.Context, tx *sql.Tx, siteID int) (wpInventoryState, error) {
	if siteID <= 0 {
		return wpInventoryState{}, ErrWPInventoryInvalidRequest
	}
	identity, err := loadWPInventorySiteIdentity(ctx, tx, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return wpInventoryState{}, ErrWPInventorySiteNotFound
	}
	if err != nil {
		return wpInventoryState{}, err
	}
	if identity.SiteType != "wordpress" {
		return wpInventoryState{}, ErrWPInventoryUnsupportedSite
	}
	return loadWPInventoryState(ctx, tx, siteID)
}

func wpInventoryLikePattern(search string) string {
	escaped := strings.ReplaceAll(search, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `%`, `\%`)
	escaped = strings.ReplaceAll(escaped, `_`, `\_`)
	return `%` + escaped + `%`
}

func fenceWPInventoryJob(ctx context.Context, tx *sql.Tx, jobID, owner, completed string) (int, error) {
	result, err := tx.ExecContext(ctx, `UPDATE site_wp_inventory_jobs SET updated_at = updated_at
		WHERE id = ? AND status = 'running' AND lease_owner = ? AND lease_expires_at > ?`, jobID, owner, completed)
	if err != nil {
		return 0, err
	}
	if err := requireWPInventoryRow(result); err != nil {
		return 0, err
	}
	var siteID int
	if err := tx.QueryRowContext(ctx, `SELECT site_id FROM site_wp_inventory_jobs
		WHERE id = ? AND status = 'running' AND lease_owner = ? AND lease_expires_at > ?`, jobID, owner, completed).Scan(&siteID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errWPInventoryLeaseLost
		}
		return 0, err
	}
	return siteID, nil
}

func insertWPInventoryComponents(ctx context.Context, tx *sql.Tx, siteID int, collectionID, collectedAt string, inv WPInventory) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO site_wp_components
		(site_id, component_type, component_key, name, version, is_active,
		 is_network_active, is_current_theme, collection_id, collected_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	if _, err := stmt.ExecContext(ctx, siteID, "core", "wordpress", "WordPress", inv.WordPress.Version,
		0, 0, 0, collectionID, collectedAt); err != nil {
		return err
	}
	for _, plugin := range inv.Plugins {
		if _, err := stmt.ExecContext(ctx, siteID, "plugin", plugin.File, plugin.Name, plugin.Version,
			boolDB(plugin.Active), boolDB(plugin.NetworkActive), 0, collectionID, collectedAt); err != nil {
			return err
		}
	}
	currentTheme := ""
	if inv.CurrentTheme != nil {
		currentTheme = inv.CurrentTheme.Stylesheet
	}
	for _, theme := range inv.Themes {
		if _, err := stmt.ExecContext(ctx, siteID, "theme", theme.Stylesheet, theme.Name, theme.Version,
			0, 0, boolDB(theme.Stylesheet == currentTheme), collectionID, collectedAt); err != nil {
			return err
		}
	}
	return nil
}

func insertWPInventoryUpdates(ctx context.Context, tx *sql.Tx, siteID int, collectionID, collectedAt string, updates WPInventoryUpdates) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range updates.Core.Items {
		if _, err := stmt.ExecContext(ctx, siteID, "core", "wordpress", item.Version, item.Response, item.Locale, collectionID, collectedAt); err != nil {
			return err
		}
	}
	for _, group := range []struct {
		kind  string
		items []WPInventoryComponentUpdate
	}{
		{"plugin", updates.Plugins.Items},
		{"theme", updates.Themes.Items},
	} {
		for _, item := range group.items {
			if _, err := stmt.ExecContext(ctx, siteID, group.kind, item.ID, item.Version, "", "", collectionID, collectedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertWPInventoryWarnings(ctx context.Context, tx *sql.Tx, jobID, createdAt string, warnings []WPInventoryWarning) error {
	for _, warning := range warnings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_wp_inventory_job_warnings
			(job_id, warning_code, created_at) VALUES (?, ?, ?)`, jobID, warning, createdAt); err != nil {
			return err
		}
	}
	return nil
}

func upsertWPInventorySuccessState(ctx context.Context, tx *sql.Tx, siteID int, collectionID, completed string, inv WPInventory) error {
	activePlugins := 0
	for _, plugin := range inv.Plugins {
		if plugin.Active || plugin.NetworkActive {
			activePlugins++
		}
	}
	currentTheme := ""
	if inv.CurrentTheme != nil {
		currentTheme = inv.CurrentTheme.Stylesheet
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO site_wp_inventory_state (
		site_id, status, wordpress_version, wordpress_locale, is_multisite,
		current_theme_key, plugin_count, active_plugin_count, theme_count,
		core_update_count, plugin_update_count, theme_update_count,
		core_updates_transient_present, core_updates_last_checked, core_version_checked,
		plugin_updates_transient_present, plugin_updates_last_checked,
		theme_updates_transient_present, theme_updates_last_checked,
		collection_id, last_attempt_at, last_success_at, last_error_code, last_error_stage, updated_at
	) VALUES (?, 'complete', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?)
	ON CONFLICT(site_id) DO UPDATE SET
		status = excluded.status, wordpress_version = excluded.wordpress_version,
		wordpress_locale = excluded.wordpress_locale, is_multisite = excluded.is_multisite,
		current_theme_key = excluded.current_theme_key, plugin_count = excluded.plugin_count,
		active_plugin_count = excluded.active_plugin_count, theme_count = excluded.theme_count,
		core_update_count = excluded.core_update_count, plugin_update_count = excluded.plugin_update_count,
		theme_update_count = excluded.theme_update_count,
		core_updates_transient_present = excluded.core_updates_transient_present,
		core_updates_last_checked = excluded.core_updates_last_checked,
		core_version_checked = excluded.core_version_checked,
		plugin_updates_transient_present = excluded.plugin_updates_transient_present,
		plugin_updates_last_checked = excluded.plugin_updates_last_checked,
		theme_updates_transient_present = excluded.theme_updates_transient_present,
		theme_updates_last_checked = excluded.theme_updates_last_checked,
		collection_id = excluded.collection_id, last_attempt_at = excluded.last_attempt_at,
		last_success_at = excluded.last_success_at, last_error_code = '', last_error_stage = '',
		updated_at = excluded.updated_at`,
		siteID, inv.WordPress.Version, inv.WordPress.Locale, boolDB(inv.WordPress.Multisite),
		currentTheme, len(inv.Plugins), activePlugins, len(inv.Themes), len(inv.Updates.Core.Items),
		len(inv.Updates.Plugins.Items), len(inv.Updates.Themes.Items), boolDB(inv.Updates.Core.TransientPresent),
		inv.Updates.Core.LastChecked, inv.Updates.Core.VersionChecked, boolDB(inv.Updates.Plugins.TransientPresent),
		inv.Updates.Plugins.LastChecked, boolDB(inv.Updates.Themes.TransientPresent), inv.Updates.Themes.LastChecked,
		collectionID, completed, completed, completed)
	return err
}

func upsertWPInventoryFailureState(ctx context.Context, tx *sql.Tx, siteID int, completed, code, stage string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO site_wp_inventory_state
		(site_id, status, last_attempt_at, last_error_code, last_error_stage, updated_at)
		VALUES (?, 'failed', ?, ?, ?, ?)
		ON CONFLICT(site_id) DO UPDATE SET status = 'failed', last_attempt_at = excluded.last_attempt_at,
			last_error_code = excluded.last_error_code, last_error_stage = excluded.last_error_stage,
			updated_at = excluded.updated_at`, siteID, completed, code, stage, completed)
	return err
}

func finishWPInventoryJob(ctx context.Context, tx *sql.Tx, jobID, owner, completed string, meta WPInventoryRunMeta, code, stage string, status wpInventoryJobStatus) error {
	result, err := tx.ExecContext(ctx, `UPDATE site_wp_inventory_jobs SET
		status = ?, lease_owner = '', lease_expires_at = NULL, finished_at = ?,
		error_code = ?, error_stage = ?, timed_out = ?, exit_code = ?,
		wall_time_ms = ?, user_cpu_ms = ?, system_cpu_ms = ?, max_rss_kib = ?,
		stdout_bytes = ?, stderr_bytes = ?, protocol_bytes = ?, runner_hash = ?,
		runner_version = ?, inventory_schema_version = ?, updated_at = ?
		WHERE id = ? AND status = 'running' AND lease_owner = ?`,
		status, completed, code, stage, boolDB(meta.TimedOut), meta.ExitCode,
		meta.WallTime.Milliseconds(), meta.UserCPUTime.Milliseconds(), meta.SystemCPUTime.Milliseconds(),
		meta.MaxRSSKiB, meta.StdoutBytes, meta.StderrBytes, meta.ProtocolBytes, meta.RunnerHash,
		meta.RunnerVersion, meta.SchemaVersion, completed, jobID, owner)
	if err != nil {
		return err
	}
	return requireWPInventoryRow(result)
}

func markWPInventorySiteChanged(ctx context.Context, tx *sql.Tx, jobID, owner string, siteID int, completed string) error {
	if err := upsertWPInventoryFailureState(ctx, tx, siteID, completed, wpInventoryErrorSiteChanged, wpInventoryErrorStageStore); err != nil {
		return err
	}
	return finishWPInventoryJob(ctx, tx, jobID, owner, completed, WPInventoryRunMeta{ExitCode: -1},
		wpInventoryErrorSiteChanged, wpInventoryErrorStageStore, wpInventoryJobFailed)
}

func validWPInventoryTrigger(trigger wpInventoryTrigger) bool {
	switch trigger {
	case wpInventoryTriggerManual, wpInventoryTriggerSiteCreated, wpInventoryTriggerUpdateFollowup, wpInventoryTriggerScheduled:
		return true
	default:
		return false
	}
}

func validWPInventoryLeaseOwner(owner string) bool {
	if owner == "" || len(owner) > 128 || !utf8.ValidString(owner) {
		return false
	}
	for _, r := range owner {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validWPInventoryRunError(runErr *WPInventoryRunError) bool {
	if runErr == nil {
		return false
	}
	switch runErr.Code {
	case WPInventoryUnsupportedPlatform, WPInventoryInvalidSite, WPInventoryInvalidSitePath,
		WPInventorySiteUserUnavailable, WPInventoryInsufficientPrivileges, WPInventoryPHPCLIUnavailable,
		WPInventoryRunuserUnavailable, WPInventoryRunnerPrepareFailed, WPInventoryRunnerIntegrityFailed,
		WPInventoryRunnerLockFailed, WPInventoryRunnerCanceled, WPInventoryRunnerStartFailed,
		WPInventoryRunnerTimeout, WPInventoryStdoutLimitExceeded, WPInventoryStderrLimitExceeded,
		WPInventoryProtocolLimitExceeded, WPInventoryProtocolInvalid, WPInventoryRunnerPolicyMismatch,
		WPInventoryWordPressBootstrapFailed, WPInventoryWordPressTerminated, WPInventoryMemoryLimitExceeded,
		WPInventoryInventoryLimitExceeded, WPInventoryRunnerInternalError:
	default:
		return false
	}
	switch runErr.Stage {
	case WPInventoryStageValidate, WPInventoryStageLock, WPInventoryStagePrepare,
		WPInventoryStageStart, WPInventoryStageExecute, WPInventoryStageProtocol:
		return true
	default:
		return false
	}
}

func validateWPInventoryMeta(meta WPInventoryRunMeta, success bool) error {
	if meta.WallTime < 0 || meta.UserCPUTime < 0 || meta.SystemCPUTime < 0 || meta.MaxRSSKiB < 0 ||
		meta.StdoutBytes < 0 || meta.StderrBytes < 0 || meta.ProtocolBytes < 0 || meta.SchemaVersion < 0 {
		return errors.New("invalid wordpress inventory metrics")
	}
	if len(meta.RunnerVersion) > wpInventoryVersionLimit || !utf8.ValidString(meta.RunnerVersion) {
		return errors.New("invalid wordpress inventory runner version")
	}
	if meta.RunnerHash != "" && !wpInventoryHashPattern.MatchString(meta.RunnerHash) {
		return errors.New("invalid wordpress inventory runner hash")
	}
	if success && (meta.RunnerHash == "" || meta.RunnerVersion == "" || meta.SchemaVersion <= 0) {
		return errors.New("missing wordpress inventory runner identity")
	}
	if success && (meta.ExitCode != 0 || meta.TimedOut || meta.StdoutExceeded || meta.StderrExceeded || meta.ProtocolExceeded) {
		return errors.New("inconsistent wordpress inventory success metrics")
	}
	_, err := normalizedWPInventoryWarnings(meta.Warnings)
	return err
}

func normalizedWPInventoryWarnings(input []WPInventoryWarning) ([]WPInventoryWarning, error) {
	seen := make(map[WPInventoryWarning]struct{}, len(input))
	for _, warning := range input {
		switch warning {
		case WPInventoryWarningStaleCleanupFailed, wpInventoryWarningUnknown:
			seen[warning] = struct{}{}
		default:
			return nil, errors.New("invalid wordpress inventory warning")
		}
	}
	warnings := make([]WPInventoryWarning, 0, len(seen))
	for warning := range seen {
		warnings = append(warnings, warning)
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i] < warnings[j] })
	return warnings, nil
}

func newWPInventoryJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func wpInventoryDBTime(value time.Time) string {
	return value.UTC().Format(wpInventoryDBTimeLayout)
}

func boolDB(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requireWPInventoryRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errWPInventoryLeaseLost
	}
	return nil
}
