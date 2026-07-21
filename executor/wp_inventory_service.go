package executor

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

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

func wpInventorySummaryModel(snapshot wpInventorySummarySnapshot) (models.WPInventorySummary, error) {
	state := snapshot.State
	hasCollection := state.CollectionID != ""
	hasSuccessTime := state.LastSuccessAt != ""
	if hasCollection != hasSuccessTime {
		return models.WPInventorySummary{}, errors.New("inconsistent wordpress inventory success state")
	}
	hasError := state.LastErrorCode != "" || state.LastErrorStage != ""
	switch state.Status {
	case "unknown":
		if hasCollection || state.LastAttemptAt != "" || hasError {
			return models.WPInventorySummary{}, errors.New("inconsistent unknown wordpress inventory state")
		}
	case "complete":
		if !hasCollection || state.LastAttemptAt == "" || hasError {
			return models.WPInventorySummary{}, errors.New("inconsistent complete wordpress inventory state")
		}
	case "failed":
		if state.LastAttemptAt == "" || state.LastErrorCode == "" || state.LastErrorStage == "" {
			return models.WPInventorySummary{}, errors.New("inconsistent failed wordpress inventory state")
		}
	default:
		return models.WPInventorySummary{}, errors.New("invalid wordpress inventory state")
	}
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
		LastAttemptAt:        lastAttempt, LastSuccessAt: lastSuccess, LastError: lastError, ActiveTask: activeTask,
	}, nil
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
