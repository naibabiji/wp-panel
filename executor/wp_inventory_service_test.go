package executor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

func TestWPInventoryServiceUnknownSummaryIsReadOnly(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)

	summary, err := service.Summary(context.Background(), siteID)
	if err != nil {
		t.Fatalf("Summary(): %v", err)
	}
	if summary.SiteID != siteID || summary.CollectionStatus != "unknown" || summary.HasSuccessfulInventory ||
		summary.LastAttemptAt != nil || summary.LastSuccessAt != nil || summary.LastError != nil || summary.ActiveTask != nil ||
		summary.CoreUpgradeAvailable || summary.Counts != (models.WPInventoryCounts{}) || summary.WordPress != (models.WPInventoryWordPress{}) {
		t.Fatalf("unknown summary = %+v", summary)
	}
	if countRows(t, store.db, "site_wp_inventory_state") != 0 || countRows(t, store.db, "site_wp_inventory_jobs") != 0 {
		t.Fatal("unknown summary created inventory state or job")
	}
}

func TestWPInventoryServiceSuccessFailureAndCoreSemantics(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))

	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-service-success", now)
	running, err := service.Summary(ctx, siteID)
	if err != nil || running.CollectionStatus != "unknown" || running.ActiveTask == nil ||
		running.ActiveTask.ID != jobID || running.ActiveTask.Status != "running" {
		t.Fatalf("running Summary() = %+v, err = %v", running, err)
	}
	result := sampleWPInventoryResult()
	if err := store.persistSuccess(ctx, jobID, "worker-service-success", identity, result, now.Add(time.Second)); err != nil {
		t.Fatalf("persistSuccess(): %v", err)
	}
	summary, err := service.Summary(ctx, siteID)
	if err != nil {
		t.Fatalf("Summary(success): %v", err)
	}
	if summary.CollectionStatus != "complete" || !summary.HasSuccessfulInventory || !summary.CoreUpgradeAvailable || summary.ActiveTask != nil ||
		summary.WordPress.Version != "7.0" || summary.WordPress.Locale != "zh_CN" || !summary.WordPress.Multisite ||
		summary.WordPress.CurrentThemeKey != "theme-one" || summary.Counts.Plugins != 2 ||
		summary.Counts.ActivePlugins != 2 || summary.Counts.Themes != 1 || summary.Counts.PluginUpdates != 1 ||
		summary.Counts.ThemeUpdates != 1 || summary.LastAttemptAt == nil || summary.LastSuccessAt == nil ||
		!summary.LastSuccessAt.Equal(time.Date(2026, 7, 21, 1, 30, 1, 0, time.UTC)) || summary.LastError != nil {
		t.Fatalf("success summary = %+v", summary)
	}

	failureAt := now.Add(2 * time.Minute)
	failureJob, failureIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-service-failure", failureAt)
	_ = failureIdentity
	runErr := runError(WPInventoryRunnerTimeout, WPInventoryStageExecute, -1, true, errors.New("secret runner cause"))
	if err := store.persistFailure(ctx, failureJob, "worker-service-failure", runErr, WPInventoryRunMeta{}, failureAt.Add(time.Second)); err != nil {
		t.Fatalf("persistFailure(): %v", err)
	}
	failed, err := service.Summary(ctx, siteID)
	if err != nil {
		t.Fatalf("Summary(failed): %v", err)
	}
	if failed.CollectionStatus != "failed" || !failed.HasSuccessfulInventory || !failed.CoreUpgradeAvailable ||
		failed.WordPress != summary.WordPress || failed.Counts != summary.Counts ||
		failed.LastSuccessAt == nil || !failed.LastSuccessAt.Equal(*summary.LastSuccessAt) ||
		failed.LastError == nil || failed.LastError.Code != string(WPInventoryRunnerTimeout) ||
		failed.LastError.Stage != string(WPInventoryStageExecute) {
		t.Fatalf("failed summary = %+v", failed)
	}

	secondAt := now.Add(4 * time.Minute)
	secondJob, secondIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-service-second", secondAt)
	second := sampleWPInventoryResult()
	second.Inventory.Updates.Core.Items = []WPInventoryCoreUpdate{
		{Version: "7.0", Locale: "zh_CN", Response: "latest"},
		{Version: "7.1", Locale: "zh_CN", Response: "autoupdate"},
		{Version: "7.2", Locale: "zh_CN", Response: "development"},
		{Version: "7.3", Locale: "zh_CN", Response: "unexpected-secret-response"},
	}
	if err := store.persistSuccess(ctx, secondJob, "worker-service-second", secondIdentity, second, secondAt.Add(time.Second)); err != nil {
		t.Fatalf("persist second success: %v", err)
	}
	withoutUpgrade, err := service.Summary(ctx, siteID)
	if err != nil {
		t.Fatalf("Summary(non-upgrade responses): %v", err)
	}
	if withoutUpgrade.CoreUpgradeAvailable {
		t.Fatal("core_upgrade_available = true without an upgrade response")
	}
}

func TestWPInventoryServiceRefreshEligibilityAndDeduplication(t *testing.T) {
	store, activeSiteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 2, 0, 0, 0, time.UTC)

	first, err := service.Refresh(ctx, activeSiteID, now)
	if err != nil || !first.Created || first.Task.Status != "queued" || first.Task.SiteID != activeSiteID {
		t.Fatalf("first Refresh() = %+v, err = %v", first, err)
	}
	second, err := service.Refresh(ctx, activeSiteID, now.Add(time.Second))
	if err != nil || second.Created || second.Task.ID != first.Task.ID {
		t.Fatalf("deduplicated Refresh() = %+v, err = %v", second, err)
	}

	for _, tc := range []struct {
		name     string
		status   string
		siteType string
		wantErr  error
		allowed  bool
	}{
		{name: "paused", status: "paused", siteType: "wordpress", allowed: true},
		{name: "error", status: "error", siteType: "wordpress", allowed: true},
		{name: "creating", status: "creating", siteType: "wordpress", wantErr: ErrWPInventorySiteUnavailable},
		{name: "deleting", status: "deleting", siteType: "wordpress", wantErr: ErrWPInventorySiteUnavailable},
		{name: "future", status: "future", siteType: "wordpress", wantErr: ErrWPInventorySiteUnavailable},
		{name: "php", status: "active", siteType: "php", wantErr: ErrWPInventoryUnsupportedSite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			siteID := insertWPInventoryServiceSite(t, store, tc.name+".example.com", tc.status, tc.siteType)
			result, err := service.Refresh(ctx, siteID, now)
			if tc.allowed {
				if err != nil || !result.Created || result.Task.SiteID != siteID {
					t.Fatalf("Refresh() = %+v, err = %v", result, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Refresh() error = %v, want %v", err, tc.wantErr)
			}
			var jobs int
			if err := store.db.QueryRow("SELECT COUNT(*) FROM site_wp_inventory_jobs WHERE site_id = ?", siteID).Scan(&jobs); err != nil || jobs != 0 {
				t.Fatalf("ineligible jobs = %d, err = %v", jobs, err)
			}
		})
	}

	if _, err := service.Refresh(ctx, 999999, now); !errors.Is(err, ErrWPInventorySiteNotFound) {
		t.Fatalf("missing site error = %v", err)
	}
}

func TestWPInventoryServiceTaskOwnershipAndSafeProjection(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 3, 0, 0, 0, time.UTC)
	refresh, err := service.Refresh(ctx, siteID, now)
	if err != nil {
		t.Fatalf("Refresh(): %v", err)
	}
	task, err := service.Task(ctx, siteID, refresh.Task.ID)
	if err != nil || task.ID != refresh.Task.ID || task.Status != "queued" || task.Error != nil {
		t.Fatalf("Task() = %+v, err = %v", task, err)
	}

	otherSiteID := insertWPInventoryServiceSite(t, store, "other-task.example.com", "active", "wordpress")
	if _, err := service.Task(ctx, otherSiteID, refresh.Task.ID); !errors.Is(err, ErrWPInventoryTaskNotFound) {
		t.Fatalf("cross-site task error = %v", err)
	}
	if _, err := service.Task(ctx, siteID, strings.Repeat("A", 32)); !errors.Is(err, ErrWPInventoryInvalidRequest) {
		t.Fatalf("uppercase task error = %v", err)
	}
	if _, err := service.Task(ctx, siteID, "short"); !errors.Is(err, ErrWPInventoryInvalidRequest) {
		t.Fatalf("short task error = %v", err)
	}
}

func TestWPInventoryServiceFailedTaskUsesFixedSafeError(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 3, 30, 0, 0, time.UTC)
	refresh, err := service.Refresh(ctx, siteID, now)
	if err != nil {
		t.Fatalf("Refresh(): %v", err)
	}
	job, err := store.claim(ctx, "worker-service-task-failure", now, time.Minute)
	if err != nil || job == nil || job.ID != refresh.Task.ID {
		t.Fatalf("claim() = %+v, err = %v", job, err)
	}
	runErr := runError(WPInventoryRunnerTimeout, WPInventoryStageExecute, -1, true,
		errors.New("secret path /var/www/private and database detail"))
	if err := store.persistFailure(ctx, job.ID, "worker-service-task-failure", runErr, WPInventoryRunMeta{}, now.Add(time.Second)); err != nil {
		t.Fatalf("persistFailure(): %v", err)
	}
	task, err := service.Task(ctx, siteID, job.ID)
	if err != nil || task.Status != "failed" || task.StartedAt == nil || task.FinishedAt == nil ||
		task.Error == nil || task.Error.Code != string(WPInventoryRunnerTimeout) ||
		task.Error.Stage != string(WPInventoryStageExecute) || !task.Error.TimedOut {
		t.Fatalf("failed Task() = %+v, err = %v", task, err)
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("json.Marshal(task): %v", err)
	}
	for _, forbidden := range []string{"secret", "/var/www", "database detail", "lease_owner", "runner_hash", "exit_code"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("failed task exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestWPInventoryServiceRejectsInvalidStoredTime(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	if _, err := store.db.Exec(`INSERT INTO site_wp_inventory_state
		(site_id, status, last_attempt_at, updated_at) VALUES (?, 'failed', 'not-a-time', CURRENT_TIMESTAMP)`, siteID); err != nil {
		t.Fatalf("insert invalid state: %v", err)
	}
	if _, err := service.Summary(context.Background(), siteID); err == nil {
		t.Fatal("Summary() accepted invalid stored time")
	}
}

func TestParseRequiredWPInventoryTimeAcceptsStoreRepresentations(t *testing.T) {
	for _, value := range []string{
		"2026-07-21 03:04:05",
		"2026-07-21T11:04:05.123456789+08:00",
	} {
		parsed, err := parseRequiredWPInventoryTime(value)
		if err != nil {
			t.Fatalf("parseRequiredWPInventoryTime(%q): %v", value, err)
		}
		if parsed.Location() != time.UTC {
			t.Fatalf("parseRequiredWPInventoryTime(%q) location = %v", value, parsed.Location())
		}
	}
	if _, err := parseRequiredWPInventoryTime("2026/07/21 03:04:05"); err == nil {
		t.Fatal("parseRequiredWPInventoryTime() accepted an unsupported representation")
	}
}

func newTestWPInventoryService(t *testing.T, store *wpInventoryStore) *WPInventoryService {
	t.Helper()
	service, err := NewWPInventoryService(store.db)
	if err != nil {
		t.Fatalf("NewWPInventoryService(): %v", err)
	}
	return service
}

func insertWPInventoryServiceSite(t *testing.T, store *wpInventoryStore, domain, status, siteType string) int {
	t.Helper()
	result, err := store.db.Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type)
		VALUES (?, ?, ?, 'wp_service', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', ?)`,
		domain, domain, status, siteType)
	if err != nil {
		t.Fatalf("insert service site %s: %v", domain, err)
	}
	siteID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("service site ID: %v", err)
	}
	return int(siteID)
}
