package executor

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/naibabiji/wp-panel/models"
)

var (
	ErrWPInventoryInvalidRequest  = errors.New("invalid wordpress inventory request")
	ErrWPInventorySiteNotFound    = errors.New("wordpress inventory site not found")
	ErrWPInventoryUnsupportedSite = errors.New("wordpress inventory unsupported site")
	ErrWPInventorySiteUnavailable = errors.New("wordpress inventory site unavailable")
	ErrWPInventoryTaskNotFound    = errors.New("wordpress inventory task not found")
)

var wpInventoryTaskIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type WPInventoryService struct {
	store *wpInventoryStore
}

type WPInventoryListOptions struct {
	Page     int
	PageSize int
	Type     string
	Search   string
}

func NewWPInventoryService(db *sql.DB) (*WPInventoryService, error) {
	store, err := newWPInventoryStoreWithDB(db)
	if err != nil {
		return nil, err
	}
	return &WPInventoryService{store: store}, nil
}

func (s *WPInventoryService) Summary(ctx context.Context, siteID int) (models.WPInventorySummary, error) {
	if s == nil || s.store == nil || siteID <= 0 {
		return models.WPInventorySummary{}, ErrWPInventoryInvalidRequest
	}
	snapshot, err := s.store.getSummarySnapshot(ctx, siteID)
	if err != nil {
		return models.WPInventorySummary{}, err
	}
	return wpInventorySummaryModel(snapshot)
}

func (s *WPInventoryService) Refresh(ctx context.Context, siteID int, requestedAt time.Time) (models.WPInventoryRefreshResult, error) {
	if s == nil || s.store == nil || siteID <= 0 || requestedAt.IsZero() {
		return models.WPInventoryRefreshResult{}, ErrWPInventoryInvalidRequest
	}
	job, created, err := s.store.enqueueEligibleManual(ctx, siteID, requestedAt.UTC())
	if err != nil {
		return models.WPInventoryRefreshResult{}, err
	}
	task, err := wpInventoryTaskModel(job)
	if err != nil {
		return models.WPInventoryRefreshResult{}, err
	}
	return models.WPInventoryRefreshResult{Task: task, Created: created}, nil
}

func (s *WPInventoryService) Task(ctx context.Context, siteID int, taskID string) (models.WPInventoryTask, error) {
	if s == nil || s.store == nil || siteID <= 0 || !wpInventoryTaskIDPattern.MatchString(taskID) {
		return models.WPInventoryTask{}, ErrWPInventoryInvalidRequest
	}
	job, err := s.store.getJobForSite(ctx, siteID, taskID)
	if err != nil {
		return models.WPInventoryTask{}, err
	}
	return wpInventoryTaskModel(job)
}

func (s *WPInventoryService) Components(ctx context.Context, siteID int, options WPInventoryListOptions) (models.PaginatedResult, error) {
	if s == nil || s.store == nil {
		return models.PaginatedResult{}, ErrWPInventoryInvalidRequest
	}
	normalized, err := normalizeWPInventoryListOptions(siteID, options, false)
	if err != nil {
		return models.PaginatedResult{}, err
	}
	snapshot, err := s.store.getComponentPage(ctx, siteID, normalized.Type, normalized.Search,
		normalized.PageSize, (normalized.Page-1)*normalized.PageSize)
	if err != nil {
		return models.PaginatedResult{}, err
	}
	if err := validateWPInventoryPublicState(snapshot.State); err != nil {
		return models.PaginatedResult{}, err
	}
	items := make([]models.WPInventoryComponent, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		collectedAt, err := parseRequiredWPInventoryTime(item.CollectedAt)
		if err != nil {
			return models.PaginatedResult{}, err
		}
		items = append(items, models.WPInventoryComponent{
			Type: item.Type, Key: item.Key, Name: item.Name, Version: item.Version,
			Active: item.Active, NetworkActive: item.NetworkActive, CurrentTheme: item.CurrentTheme,
			CollectedAt: collectedAt,
		})
	}
	return models.PaginatedResult{
		Items: items, Total: snapshot.Total, Page: normalized.Page, PageSize: normalized.PageSize,
	}, nil
}

func (s *WPInventoryService) Updates(ctx context.Context, siteID int, options WPInventoryListOptions) (models.PaginatedResult, error) {
	if s == nil || s.store == nil {
		return models.PaginatedResult{}, ErrWPInventoryInvalidRequest
	}
	normalized, err := normalizeWPInventoryListOptions(siteID, options, true)
	if err != nil {
		return models.PaginatedResult{}, err
	}
	snapshot, err := s.store.getUpdatePage(ctx, siteID, normalized.Type, normalized.Search,
		normalized.PageSize, (normalized.Page-1)*normalized.PageSize)
	if err != nil {
		return models.PaginatedResult{}, err
	}
	if err := validateWPInventoryPublicState(snapshot.State); err != nil {
		return models.PaginatedResult{}, err
	}
	items := make([]models.WPInventoryUpdate, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		collectedAt, err := parseRequiredWPInventoryTime(item.CollectedAt)
		if err != nil {
			return models.PaginatedResult{}, err
		}
		items = append(items, models.WPInventoryUpdate{
			Type: item.Type, Key: item.Key, CurrentVersion: item.CurrentVersion, TargetVersion: item.Version,
			Locale: item.Locale, CollectedAt: collectedAt,
		})
	}
	return models.PaginatedResult{
		Items: items, Total: snapshot.Total, Page: normalized.Page, PageSize: normalized.PageSize,
	}, nil
}

func normalizeWPInventoryListOptions(siteID int, options WPInventoryListOptions, allowCore bool) (WPInventoryListOptions, error) {
	if siteID <= 0 || options.Page < 1 || options.Page > 10000 || options.PageSize < 1 || options.PageSize > 100 {
		return WPInventoryListOptions{}, ErrWPInventoryInvalidRequest
	}
	validType := options.Type == "" || options.Type == "plugin" || options.Type == "theme"
	if allowCore && options.Type == "core" {
		validType = true
	}
	if !validType {
		return WPInventoryListOptions{}, ErrWPInventoryInvalidRequest
	}
	options.Search = strings.TrimSpace(options.Search)
	if !utf8.ValidString(options.Search) || len(options.Search) > 128 {
		return WPInventoryListOptions{}, ErrWPInventoryInvalidRequest
	}
	return options, nil
}

func wpInventorySummaryModel(snapshot wpInventorySummarySnapshot) (models.WPInventorySummary, error) {
	state := snapshot.State
	if err := validateWPInventoryPublicState(state); err != nil {
		return models.WPInventorySummary{}, err
	}
	hasCollection := state.CollectionID != ""
	hasError := state.LastErrorCode != "" || state.LastErrorStage != ""
	lastAttempt, err := parseOptionalWPInventoryTime(state.LastAttemptAt)
	if err != nil {
		return models.WPInventorySummary{}, err
	}
	lastSuccess, err := parseOptionalWPInventoryTime(state.LastSuccessAt)
	if err != nil {
		return models.WPInventorySummary{}, err
	}
	var lastError *models.WPInventoryStateError
	if hasError {
		lastError = &models.WPInventoryStateError{Code: state.LastErrorCode, Stage: state.LastErrorStage}
	}
	var activeTask *models.WPInventoryTask
	if snapshot.ActiveJob != nil {
		task, err := wpInventoryTaskModel(*snapshot.ActiveJob)
		if err != nil {
			return models.WPInventorySummary{}, err
		}
		activeTask = &task
	}
	return models.WPInventorySummary{
		SiteID:                 snapshot.Identity.ID,
		CollectionStatus:       state.Status,
		HasSuccessfulInventory: hasCollection,
		WordPress: models.WPInventoryWordPress{
			Version: state.WordPressVersion, Locale: state.WordPressLocale,
			Multisite: state.Multisite, CurrentThemeKey: state.CurrentThemeKey,
		},
		Counts: models.WPInventoryCounts{
			Plugins: state.PluginCount, ActivePlugins: state.ActivePluginCount, Themes: state.ThemeCount,
			PluginUpdates: state.PluginUpdateCount, ThemeUpdates: state.ThemeUpdateCount,
		},
		CoreUpgradeAvailable: snapshot.CoreUpgradeAvailable,
		UpdateChecks: models.WPInventoryUpdateChecks{
			Core: state.CoreTransient, Plugins: state.PluginTransient, Themes: state.ThemeTransient,
		},
		LastAttemptAt: lastAttempt, LastSuccessAt: lastSuccess, LastError: lastError, ActiveTask: activeTask,
	}, nil
}

func validateWPInventoryPublicState(state wpInventoryState) error {
	hasCollection := state.CollectionID != ""
	hasSuccessTime := state.LastSuccessAt != ""
	if hasCollection != hasSuccessTime {
		return errors.New("inconsistent wordpress inventory success state")
	}
	hasError := state.LastErrorCode != "" || state.LastErrorStage != ""
	switch state.Status {
	case "unknown":
		if hasCollection || state.LastAttemptAt != "" || hasError {
			return errors.New("inconsistent unknown wordpress inventory state")
		}
	case "complete":
		if !hasCollection || state.LastAttemptAt == "" || hasError {
			return errors.New("inconsistent complete wordpress inventory state")
		}
	case "failed":
		if state.LastAttemptAt == "" || state.LastErrorCode == "" || state.LastErrorStage == "" {
			return errors.New("inconsistent failed wordpress inventory state")
		}
	default:
		return errors.New("invalid wordpress inventory state")
	}
	return nil
}

func wpInventoryTaskModel(job wpInventoryJob) (models.WPInventoryTask, error) {
	switch job.Status {
	case wpInventoryJobQueued, wpInventoryJobRunning, wpInventoryJobSucceeded, wpInventoryJobFailed:
	default:
		return models.WPInventoryTask{}, errors.New("invalid wordpress inventory task status")
	}
	requestedAt, err := parseRequiredWPInventoryTime(job.RequestedAt)
	if err != nil {
		return models.WPInventoryTask{}, err
	}
	startedAt, err := parseOptionalWPInventoryTime(job.StartedAt)
	if err != nil {
		return models.WPInventoryTask{}, err
	}
	finishedAt, err := parseOptionalWPInventoryTime(job.FinishedAt)
	if err != nil {
		return models.WPInventoryTask{}, err
	}
	var taskError *models.WPInventoryTaskError
	if job.Status == wpInventoryJobFailed {
		if job.ErrorCode == "" || job.ErrorStage == "" {
			return models.WPInventoryTask{}, errors.New("missing wordpress inventory task error")
		}
		taskError = &models.WPInventoryTaskError{Code: job.ErrorCode, Stage: job.ErrorStage, TimedOut: job.TimedOut}
	} else if job.ErrorCode != "" || job.ErrorStage != "" || job.TimedOut {
		return models.WPInventoryTask{}, errors.New("unexpected wordpress inventory task error")
	}
	return models.WPInventoryTask{
		ID: job.ID, SiteID: job.SiteID, Status: string(job.Status), RequestedAt: requestedAt,
		StartedAt: startedAt, FinishedAt: finishedAt, AttemptCount: job.AttemptCount, Error: taskError,
	}, nil
}

func parseRequiredWPInventoryTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("missing wordpress inventory time")
	}
	if parsed, err := time.ParseInLocation(wpInventoryDBTimeLayout, value, time.UTC); err == nil {
		return parsed.UTC(), nil
	}
	// modernc.org/sqlite converts values selected directly from DATETIME columns
	// to time.Time. database/sql then formats those values as RFC3339Nano when
	// scanning into the Store's string fields. COALESCE expressions retain the
	// original SQLite text representation, so both fixed internal forms occur.
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, errors.New("invalid wordpress inventory time")
}

func parseOptionalWPInventoryTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseRequiredWPInventoryTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
