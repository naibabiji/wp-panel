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

func TestWPInventoryServiceSummaryExposesUpdateChecks(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))

	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-update-checks", now)
	result := sampleWPInventoryResult()
	if err := store.persistSuccess(ctx, jobID, "worker-update-checks", identity, result, now.Add(time.Second)); err != nil {
		t.Fatalf("persistSuccess(): %v", err)
	}
	summary, err := service.Summary(ctx, siteID)
	if err != nil {
		t.Fatalf("Summary(): %v", err)
	}
	if summary.UpdateChecks != (models.WPInventoryUpdateChecks{Core: true, Plugins: true, Themes: true}) {
		t.Fatalf("update_checks with populated transients = %+v", summary.UpdateChecks)
	}

	blockedAt := now.Add(time.Minute)
	blockedJob, blockedIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-update-checks-blocked", blockedAt)
	blocked := sampleWPInventoryResult()
	blocked.Inventory.Updates.Core.TransientPresent = false
	blocked.Inventory.Updates.Core.Items = nil
	blocked.Inventory.Updates.Plugins.TransientPresent = false
	blocked.Inventory.Updates.Plugins.Items = nil
	blocked.Inventory.Updates.Themes.TransientPresent = false
	blocked.Inventory.Updates.Themes.Items = nil
	if err := store.persistSuccess(ctx, blockedJob, "worker-update-checks-blocked", blockedIdentity, blocked, blockedAt.Add(time.Second)); err != nil {
		t.Fatalf("persist blocked success: %v", err)
	}
	blockedSummary, err := service.Summary(ctx, siteID)
	if err != nil {
		t.Fatalf("Summary(blocked): %v", err)
	}
	if blockedSummary.UpdateChecks != (models.WPInventoryUpdateChecks{}) {
		t.Fatalf("update_checks with blocked transients = %+v", blockedSummary.UpdateChecks)
	}
}

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

func TestWPInventoryServicePagesUnknownAndFirstFailureAsEmptyArrays(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	options := WPInventoryListOptions{Page: 1, PageSize: 50}

	components, err := service.Components(ctx, siteID, options)
	if err != nil || components.Total != 0 || len(wpInventoryComponentItems(t, components)) != 0 {
		t.Fatalf("unknown components = %+v, err = %v", components, err)
	}
	updates, err := service.Updates(ctx, siteID, options)
	if err != nil || updates.Total != 0 || len(wpInventoryUpdateItems(t, updates)) != 0 {
		t.Fatalf("unknown updates = %+v, err = %v", updates, err)
	}

	now := time.Date(2026, 7, 21, 4, 0, 0, 0, time.UTC)
	jobID, _ := enqueueAndClaimInventory(t, store, siteID, "worker-page-first-failure", now)
	runErr := runError(WPInventoryWordPressBootstrapFailed, WPInventoryStageProtocol, 255, false, errors.New("secret"))
	if err := store.persistFailure(ctx, jobID, "worker-page-first-failure", runErr, WPInventoryRunMeta{}, now.Add(time.Second)); err != nil {
		t.Fatalf("persistFailure(): %v", err)
	}
	components, err = service.Components(ctx, siteID, options)
	if err != nil || components.Total != 0 || len(wpInventoryComponentItems(t, components)) != 0 {
		t.Fatalf("first-failure components = %+v, err = %v", components, err)
	}
}

func TestWPInventoryServicePagesCurrentCollectionFiltersSearchAndCoreResponses(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 4, 15, 0, 0, time.UTC)
	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-page-success", now)
	if err := store.persistSuccess(ctx, jobID, "worker-page-success", identity, sampleWPInventoryResult(), now.Add(time.Second)); err != nil {
		t.Fatalf("persistSuccess(): %v", err)
	}
	collected := wpInventoryDBTime(now.Add(time.Second))
	for _, row := range []struct{ key, name, version string }{
		{`literal%/plugin.php`, "Percent Plugin", "3.0"},
		{`literalX/plugin.php`, "Plain Plugin", "3.1"},
		{`under_score/plugin.php`, "Under Plugin", "4.0"},
		{`underXscore/plugin.php`, "Plain Under Plugin", "4.1"},
		{`slash\plugin/plugin.php`, "Slash Plugin", "5.0"},
	} {
		if _, err := store.db.Exec(`INSERT INTO site_wp_components
			(site_id, component_type, component_key, name, version, collection_id, collected_at)
			VALUES (?, 'plugin', ?, ?, ?, ?, ?)`, siteID, row.key, row.name, row.version, jobID, collected); err != nil {
			t.Fatalf("insert searchable component %q: %v", row.key, err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_components
		(site_id, component_type, component_key, name, version, collection_id, collected_at)
		VALUES (?, 'plugin', 'stale/stale.php', 'Stale', '9.9', 'stale-collection', ?)`, siteID, collected); err != nil {
		t.Fatalf("insert stale component: %v", err)
	}
	for _, response := range []string{"latest", "development", "autoupdate", "", "unknown"} {
		if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates
			(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
			VALUES (?, 'core', 'wordpress', ?, ?, 'zh_CN', ?, ?)`, siteID, "8."+response, response, jobID, collected); err != nil {
			t.Fatalf("insert core response %q: %v", response, err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (?, 'plugin', 'stale/stale.php', '9.9', '', '', 'stale-collection', ?)`, siteID, collected); err != nil {
		t.Fatalf("insert stale update: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (?, 'plugin', 'literal%/plugin.php', '3.2', '', '', ?, ?)`, siteID, jobID, collected); err != nil {
		t.Fatalf("insert literal-percent update: %v", err)
	}

	allComponents, err := service.Components(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 100})
	if err != nil || allComponents.Total != 8 || len(wpInventoryComponentItems(t, allComponents)) != 8 {
		t.Fatalf("all components = %+v, err = %v", allComponents, err)
	}
	plugins, err := service.Components(ctx, siteID, WPInventoryListOptions{Page: 2, PageSize: 2, Type: "plugin"})
	pluginItems := wpInventoryComponentItems(t, plugins)
	if err != nil || plugins.Total != 7 || len(pluginItems) != 2 || plugins.Page != 2 || plugins.PageSize != 2 {
		t.Fatalf("plugin page = %+v, err = %v", plugins, err)
	}
	themes, err := service.Components(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 10, Type: "theme"})
	if err != nil || themes.Total != 1 || len(wpInventoryComponentItems(t, themes)) != 1 {
		t.Fatalf("theme page = %+v, err = %v", themes, err)
	}
	for _, tc := range []struct {
		search string
		key    string
	}{
		{search: `%`, key: `literal%/plugin.php`},
		{search: `_`, key: `under_score/plugin.php`},
		{search: `\`, key: `slash\plugin/plugin.php`},
		{search: `  Percent Plugin  `, key: `literal%/plugin.php`},
		{search: `5.0`, key: `slash\plugin/plugin.php`},
	} {
		page, err := service.Components(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 10, Search: tc.search})
		items := wpInventoryComponentItems(t, page)
		if err != nil || page.Total != 1 || len(items) != 1 || items[0].Key != tc.key {
			t.Fatalf("component search %q = %+v, err = %v", tc.search, page, err)
		}
	}

	updates, err := service.Updates(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 100})
	if err != nil || updates.Total != 4 || len(wpInventoryUpdateItems(t, updates)) != 4 {
		t.Fatalf("all public updates = %+v, err = %v", updates, err)
	}
	core, err := service.Updates(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 10, Type: "core"})
	coreItems := wpInventoryUpdateItems(t, core)
	if err != nil || core.Total != 1 || len(coreItems) != 1 || coreItems[0].CurrentVersion != "7.0" || coreItems[0].TargetVersion != "7.1" {
		t.Fatalf("core upgrades = %+v, err = %v", core, err)
	}
	themeUpdates, err := service.Updates(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 10, Type: "theme"})
	if err != nil || themeUpdates.Total != 1 || len(wpInventoryUpdateItems(t, themeUpdates)) != 1 {
		t.Fatalf("theme updates = %+v, err = %v", themeUpdates, err)
	}
	pluginUpdates, err := service.Updates(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 10, Type: "plugin", Search: "alpha"})
	pluginUpdateItems := wpInventoryUpdateItems(t, pluginUpdates)
	if err != nil || pluginUpdates.Total != 1 || len(pluginUpdateItems) != 1 || pluginUpdateItems[0].CurrentVersion != "1.0" || pluginUpdateItems[0].TargetVersion != "1.1" {
		t.Fatalf("plugin update search = %+v, err = %v", pluginUpdates, err)
	}
	literalUpdate, err := service.Updates(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 10, Search: "%"})
	literalUpdateItems := wpInventoryUpdateItems(t, literalUpdate)
	if err != nil || literalUpdate.Total != 1 || len(literalUpdateItems) != 1 || literalUpdateItems[0].Key != "literal%/plugin.php" {
		t.Fatalf("literal update search = %+v, err = %v", literalUpdate, err)
	}
	overflow, err := service.Updates(ctx, siteID, WPInventoryListOptions{Page: 10000, PageSize: 100})
	if err != nil || overflow.Total != 4 || len(wpInventoryUpdateItems(t, overflow)) != 0 || overflow.Page != 10000 {
		t.Fatalf("overflow update page = %+v, err = %v", overflow, err)
	}

	failureAt := now.Add(2 * time.Minute)
	failureJob, _ := enqueueAndClaimInventory(t, store, siteID, "worker-page-failure", failureAt)
	runErr := runError(WPInventoryRunnerTimeout, WPInventoryStageExecute, -1, true, errors.New("secret"))
	if err := store.persistFailure(ctx, failureJob, "worker-page-failure", runErr, WPInventoryRunMeta{}, failureAt.Add(time.Second)); err != nil {
		t.Fatalf("persist page failure: %v", err)
	}
	afterFailure, err := service.Components(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 100})
	if err != nil || afterFailure.Total != 8 || len(wpInventoryComponentItems(t, afterFailure)) != 8 {
		t.Fatalf("components after failed refresh = %+v, err = %v", afterFailure, err)
	}
}

func TestWPInventoryServicePageValidationEligibilityAndTimeFailure(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	valid := WPInventoryListOptions{Page: 1, PageSize: 50}

	invalid := []WPInventoryListOptions{
		{Page: 0, PageSize: 50}, {Page: -1, PageSize: 50}, {Page: 10001, PageSize: 50},
		{Page: 1, PageSize: 0}, {Page: 1, PageSize: 101}, {Page: 1, PageSize: 50, Type: "core"},
		{Page: 1, PageSize: 50, Type: "Plugin"}, {Page: 1, PageSize: 50, Search: strings.Repeat("a", 129)},
		{Page: 1, PageSize: 50, Search: string([]byte{0xff})},
	}
	for _, options := range invalid {
		if _, err := service.Components(ctx, siteID, options); !errors.Is(err, ErrWPInventoryInvalidRequest) {
			t.Fatalf("Components(%+v) error = %v", options, err)
		}
	}
	if _, err := service.Components(ctx, siteID, WPInventoryListOptions{
		Page: 1, PageSize: 50, Search: strings.Repeat("a", 128),
	}); err != nil {
		t.Fatalf("Components(128-byte search): %v", err)
	}
	if _, err := service.Updates(ctx, siteID, WPInventoryListOptions{Page: 1, PageSize: 50, Type: "unknown"}); !errors.Is(err, ErrWPInventoryInvalidRequest) {
		t.Fatalf("Updates(unknown type) error = %v", err)
	}
	if _, err := service.Components(ctx, 999999, valid); !errors.Is(err, ErrWPInventorySiteNotFound) {
		t.Fatalf("missing Components() error = %v", err)
	}
	phpSite := insertWPInventoryServiceSite(t, store, "page-php.example.com", "active", "php")
	if _, err := service.Updates(ctx, phpSite, valid); !errors.Is(err, ErrWPInventoryUnsupportedSite) {
		t.Fatalf("PHP Updates() error = %v", err)
	}
	creatingSite := insertWPInventoryServiceSite(t, store, "page-creating.example.com", "creating", "wordpress")
	creating, err := service.Components(ctx, creatingSite, valid)
	if err != nil || creating.Total != 0 || len(wpInventoryComponentItems(t, creating)) != 0 {
		t.Fatalf("creating Components() = %+v, err = %v", creating, err)
	}

	now := time.Date(2026, 7, 21, 5, 0, 0, 0, time.UTC)
	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-page-time", now)
	if err := store.persistSuccess(ctx, jobID, "worker-page-time", identity, sampleWPInventoryResult(), now.Add(time.Second)); err != nil {
		t.Fatalf("persistSuccess(): %v", err)
	}
	if _, err := store.db.Exec(`UPDATE site_wp_components SET collected_at = 'not-a-time'
		WHERE site_id = ? AND component_type = 'plugin'`, siteID); err != nil {
		t.Fatalf("corrupt collected_at: %v", err)
	}
	if _, err := service.Components(ctx, siteID, valid); err == nil {
		t.Fatal("Components() accepted invalid collected_at")
	}
}

func wpInventoryComponentItems(t *testing.T, page models.PaginatedResult) []models.WPInventoryComponent {
	t.Helper()
	items, ok := page.Items.([]models.WPInventoryComponent)
	if !ok || items == nil {
		t.Fatalf("component items type/value = %T/%v", page.Items, page.Items)
	}
	return items
}

func wpInventoryUpdateItems(t *testing.T, page models.PaginatedResult) []models.WPInventoryUpdate {
	t.Helper()
	items, ok := page.Items.([]models.WPInventoryUpdate)
	if !ok || items == nil {
		t.Fatalf("update items type/value = %T/%v", page.Items, page.Items)
	}
	return items
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
