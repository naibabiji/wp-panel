package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestWPInventoryStoreSuccessFailureAndCascade(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	unknown, err := store.getState(ctx, siteID)
	if err != nil {
		t.Fatalf("get unknown state: %v", err)
	}
	if unknown.Status != "unknown" || countRows(t, store.db, "site_wp_inventory_state") != 0 {
		t.Fatalf("unknown state = %+v, persisted rows = %d", unknown, countRows(t, store.db, "site_wp_inventory_state"))
	}

	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-a", now)
	result := sampleWPInventoryResult()
	result.Meta.Warnings = []WPInventoryWarning{WPInventoryWarningStaleCleanupFailed, WPInventoryWarningStaleCleanupFailed}
	if err := store.persistSuccess(ctx, jobID, "worker-a", identity, result, now.Add(time.Second)); err != nil {
		t.Fatalf("persist success: %v", err)
	}

	state, err := store.getState(ctx, siteID)
	if err != nil {
		t.Fatalf("get success state: %v", err)
	}
	if state.Status != "complete" || state.CollectionID != jobID || state.WordPressVersion != "7.0" ||
		state.PluginCount != 2 || state.ActivePluginCount != 2 || state.ThemeCount != 1 ||
		state.CoreUpdateCount != 1 || state.PluginUpdateCount != 1 || state.ThemeUpdateCount != 1 ||
		!state.CoreTransient || state.CoreLastChecked != 100 || state.CoreVersionChecked != "7.0" ||
		!state.PluginTransient || state.PluginLastChecked != 101 || !state.ThemeTransient || state.ThemeLastChecked != 102 {
		t.Fatalf("unexpected success state: %+v", state)
	}
	components, err := store.getComponents(ctx, siteID)
	if err != nil {
		t.Fatalf("get components: %v", err)
	}
	if len(components) != 4 || components[0].Type != "core" {
		t.Fatalf("components = %+v", components)
	}
	updates, err := store.getUpdates(ctx, siteID)
	if err != nil {
		t.Fatalf("get updates: %v", err)
	}
	if len(updates) != 3 {
		t.Fatalf("updates count = %d, want 3", len(updates))
	}
	warnings, err := store.getWarnings(ctx, jobID)
	if err != nil {
		t.Fatalf("get warnings: %v", err)
	}
	if len(warnings) != 1 || warnings[0] != WPInventoryWarningStaleCleanupFailed {
		t.Fatalf("warnings = %v", warnings)
	}
	succeededJob, err := store.getJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get succeeded job: %v", err)
	}
	if succeededJob.Status != wpInventoryJobSucceeded || succeededJob.WallTimeMS != 1000 ||
		succeededJob.UserCPUMS != 100 || succeededJob.SystemCPUMS != 50 ||
		succeededJob.RunnerHash != strings.Repeat("a", 64) || succeededJob.SchemaVersion != 1 {
		t.Fatalf("succeeded job metrics = %+v", succeededJob)
	}

	failureJob, _ := enqueueAndClaimInventory(t, store, siteID, "worker-b", now.Add(2*time.Second))
	runErr := &WPInventoryRunError{Code: WPInventoryRunnerTimeout, Stage: WPInventoryStageExecute, ExitCode: -1, TimedOut: true}
	if err := store.persistFailure(ctx, failureJob, "worker-b", runErr, WPInventoryRunMeta{}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("persist failure: %v", err)
	}
	failedState, err := store.getState(ctx, siteID)
	if err != nil {
		t.Fatalf("get failed state: %v", err)
	}
	if failedState.Status != "failed" || failedState.CollectionID != jobID || failedState.LastSuccessAt == "" ||
		failedState.LastErrorCode != string(WPInventoryRunnerTimeout) {
		t.Fatalf("failed state did not preserve success: %+v", failedState)
	}
	afterFailure, err := store.getComponents(ctx, siteID)
	if err != nil || len(afterFailure) != len(components) {
		t.Fatalf("components after failure = %d, err = %v", len(afterFailure), err)
	}

	if _, err := store.db.Exec("DELETE FROM websites WHERE id = ?", siteID); err != nil {
		t.Fatalf("delete website: %v", err)
	}
	for _, table := range []string{
		"site_wp_inventory_state", "site_wp_components", "site_wp_component_updates",
		"site_wp_inventory_jobs", "site_wp_inventory_job_warnings",
	} {
		if got := countRows(t, store.db, table); got != 0 {
			t.Fatalf("%s rows after cascade = %d, want 0", table, got)
		}
	}
}

func TestWPInventoryStoreSuccessReplacesWholeSiteInventory(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 15, 0, 0, time.UTC)
	firstJob, firstIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-first", now)
	if err := store.persistSuccess(ctx, firstJob, "worker-first", firstIdentity, sampleWPInventoryResult(), now.Add(time.Second)); err != nil {
		t.Fatalf("persist first inventory: %v", err)
	}

	secondJob, secondIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-second", now.Add(2*time.Second))
	replacement := sampleWPInventoryResult()
	replacement.Inventory.WordPress.Version = "7.1"
	replacement.Inventory.Plugins = replacement.Inventory.Plugins[:1]
	replacement.Inventory.Themes = nil
	replacement.Inventory.CurrentTheme = nil
	replacement.Inventory.Updates = WPInventoryUpdates{}
	if err := store.persistSuccess(ctx, secondJob, "worker-second", secondIdentity, replacement, now.Add(3*time.Second)); err != nil {
		t.Fatalf("persist replacement inventory: %v", err)
	}

	state, err := store.getState(ctx, siteID)
	if err != nil {
		t.Fatalf("get replacement state: %v", err)
	}
	if state.CollectionID != secondJob || state.WordPressVersion != "7.1" || state.PluginCount != 1 || state.ThemeCount != 0 {
		t.Fatalf("replacement state = %+v", state)
	}
	components, err := store.getComponents(ctx, siteID)
	if err != nil || len(components) != 2 {
		t.Fatalf("replacement components = %d, err = %v", len(components), err)
	}
	updates, err := store.getUpdates(ctx, siteID)
	if err != nil || len(updates) != 0 {
		t.Fatalf("replacement updates = %d, err = %v", len(updates), err)
	}
}

func TestWPInventoryStoreFirstFailureCreatesStateWithoutComponents(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 30, 0, 0, time.UTC)
	jobID, _ := enqueueAndClaimInventory(t, store, siteID, "worker-first-failure", now)
	runErr := &WPInventoryRunError{Code: WPInventoryWordPressBootstrapFailed, Stage: WPInventoryStageProtocol, ExitCode: 255}
	if err := store.persistFailure(ctx, jobID, "worker-first-failure", runErr, WPInventoryRunMeta{}, now.Add(time.Second)); err != nil {
		t.Fatalf("persist first failure: %v", err)
	}
	state, err := store.getState(ctx, siteID)
	if err != nil {
		t.Fatalf("get first failure state: %v", err)
	}
	if state.Status != "failed" || state.LastSuccessAt != "" || state.CollectionID != "" ||
		state.LastErrorCode != string(WPInventoryWordPressBootstrapFailed) {
		t.Fatalf("first failure state = %+v", state)
	}
	if countRows(t, store.db, "site_wp_components") != 0 || countRows(t, store.db, "site_wp_component_updates") != 0 {
		t.Fatal("first failure created component data")
	}
}

func TestWPInventoryStoreConcurrentEnqueueAndClaim(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)

	const callers = 8
	type enqueueResult struct {
		id      string
		created bool
		err     error
	}
	results := make(chan enqueueResult, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, created, err := store.enqueue(ctx, siteID, wpInventoryTriggerManual, now, now)
			results <- enqueueResult{id: id, created: created, err: err}
		}()
	}
	wg.Wait()
	close(results)
	var jobID string
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("enqueue: %v", result.err)
		}
		if jobID == "" {
			jobID = result.id
		}
		if result.id != jobID {
			t.Fatalf("dedupe returned %q and %q", jobID, result.id)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 || countRows(t, store.db, "site_wp_inventory_jobs") != 1 {
		t.Fatalf("created = %d, rows = %d", createdCount, countRows(t, store.db, "site_wp_inventory_jobs"))
	}

	type claimResult struct {
		job *wpInventoryJob
		err error
	}
	claims := make(chan claimResult, 2)
	for _, owner := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			job, err := store.claim(ctx, owner, now, time.Minute)
			claims <- claimResult{job: job, err: err}
		}(owner)
	}
	wg.Wait()
	close(claims)
	claimed := 0
	notFound := 0
	for result := range claims {
		switch {
		case result.err == nil:
			claimed++
			if result.job.ID != jobID {
				t.Fatalf("claimed job = %q, want %q", result.job.ID, jobID)
			}
		case errors.Is(result.err, errWPInventoryJobNotFound):
			notFound++
		default:
			t.Fatalf("claim: %v", result.err)
		}
	}
	if claimed != 1 || notFound != 1 {
		t.Fatalf("claimed/notFound = %d/%d, want 1/1", claimed, notFound)
	}
}

func TestWPInventoryStoreSuccessRollbackPreservesOldInventory(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 14, 0, 0, 0, time.UTC)

	firstJob, firstIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-a", now)
	first := sampleWPInventoryResult()
	if err := store.persistSuccess(ctx, firstJob, "worker-a", firstIdentity, first, now.Add(time.Second)); err != nil {
		t.Fatalf("persist first success: %v", err)
	}

	secondJob, secondIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-b", now.Add(2*time.Second))
	if _, err := store.db.Exec(`CREATE TRIGGER fail_inventory_plugin_insert
		BEFORE INSERT ON site_wp_components WHEN NEW.component_type = 'plugin'
		BEGIN SELECT RAISE(ABORT, 'injected inventory failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	second := sampleWPInventoryResult()
	second.Inventory.WordPress.Version = "7.1"
	if err := store.persistSuccess(ctx, secondJob, "worker-b", secondIdentity, second, now.Add(3*time.Second)); err == nil {
		t.Fatal("persist second success error = nil, want injected failure")
	}
	if _, err := store.db.Exec("DROP TRIGGER fail_inventory_plugin_insert"); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}

	state, err := store.getState(ctx, siteID)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if state.CollectionID != firstJob || state.WordPressVersion != "7.0" {
		t.Fatalf("state changed after rollback: %+v", state)
	}
	components, err := store.getComponents(ctx, siteID)
	if err != nil || len(components) != 4 {
		t.Fatalf("components after rollback = %d, err = %v", len(components), err)
	}
	job, err := store.getJob(ctx, secondJob)
	if err != nil {
		t.Fatalf("get second job: %v", err)
	}
	if job.Status != wpInventoryJobRunning || job.LeaseOwner != "worker-b" {
		t.Fatalf("second job after rollback = %+v", job)
	}
}

func TestWPInventoryStoreLeaseRecoveryReleaseAndFencing(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC)
	jobID, _, err := store.enqueue(ctx, siteID, wpInventoryTriggerManual, now, now)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		owner := fmt.Sprintf("worker-%d", attempt)
		claimedAt := now.Add(time.Duration(attempt) * time.Minute)
		if _, err := store.claim(ctx, owner, claimedAt, time.Second); err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if attempt == 1 {
			identity, err := store.loadSiteIdentity(ctx, siteID)
			if err != nil {
				t.Fatalf("load identity for expired lease: %v", err)
			}
			if err := store.persistSuccess(ctx, jobID, owner, identity, sampleWPInventoryResult(), claimedAt.Add(2*time.Second)); !errors.Is(err, errWPInventoryLeaseLost) {
				t.Fatalf("expired lease persist error = %v, want lease lost", err)
			}
		}
		requeued, failed, err := store.recoverExpired(ctx, claimedAt.Add(2*time.Second))
		if err != nil {
			t.Fatalf("recover attempt %d: %v", attempt, err)
		}
		if attempt < 3 && (requeued != 1 || failed != 0) {
			t.Fatalf("recover attempt %d = %d/%d, want 1/0", attempt, requeued, failed)
		}
		if attempt == 3 && (requeued != 0 || failed != 1) {
			t.Fatalf("recover attempt 3 = %d/%d, want 0/1", requeued, failed)
		}
	}
	job, err := store.getJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get recovered job: %v", err)
	}
	if job.Status != wpInventoryJobFailed || job.LeaseRecoveryCount != 3 || job.ErrorCode != wpInventoryErrorRepeatedCrash {
		t.Fatalf("recovered job = %+v", job)
	}
	state, err := store.getState(ctx, siteID)
	if err != nil || state.Status != "failed" || state.LastErrorCode != wpInventoryErrorRepeatedCrash {
		t.Fatalf("recovery state = %+v, err = %v", state, err)
	}

	releaseJob, _, err := store.enqueue(ctx, siteID, wpInventoryTriggerManual, now.Add(10*time.Minute), now.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("enqueue release job: %v", err)
	}
	if _, err := store.claim(ctx, "worker-release", now.Add(10*time.Minute), time.Minute); err != nil {
		t.Fatalf("claim release job: %v", err)
	}
	if err := store.releaseOwned(ctx, releaseJob, "wrong-owner", now.Add(11*time.Minute)); !errors.Is(err, errWPInventoryLeaseLost) {
		t.Fatalf("wrong owner release error = %v, want lease lost", err)
	}
	if err := store.releaseOwned(ctx, releaseJob, "worker-release", now.Add(11*time.Minute)); err != nil {
		t.Fatalf("release owned: %v", err)
	}
	released, err := store.getJob(ctx, releaseJob)
	if err != nil || released.Status != wpInventoryJobQueued || released.LeaseOwner != "" {
		t.Fatalf("released job = %+v, err = %v", released, err)
	}
}

func TestWPInventoryStoreRejectsStaleOwnerAndSiteChange(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-a", now)
	result := sampleWPInventoryResult()
	if err := store.persistSuccess(ctx, jobID, "worker-old", identity, result, now.Add(time.Second)); !errors.Is(err, errWPInventoryLeaseLost) {
		t.Fatalf("stale owner error = %v, want lease lost", err)
	}
	if _, err := store.db.Exec("UPDATE websites SET domain = 'changed.example.com' WHERE id = ?", siteID); err != nil {
		t.Fatalf("change website: %v", err)
	}
	if err := store.persistSuccess(ctx, jobID, "worker-a", identity, result, now.Add(2*time.Second)); !errors.Is(err, errWPInventorySiteChanged) {
		t.Fatalf("site changed error = %v, want site changed", err)
	}
	job, err := store.getJob(ctx, jobID)
	if err != nil || job.Status != wpInventoryJobFailed || job.ErrorCode != wpInventoryErrorSiteChanged {
		t.Fatalf("site changed job = %+v, err = %v", job, err)
	}
	if got := countRows(t, store.db, "site_wp_components"); got != 0 {
		t.Fatalf("components after site change = %d, want 0", got)
	}

	deletedJob, deletedIdentity := enqueueAndClaimInventory(t, store, siteID, "worker-delete", now.Add(3*time.Second))
	if _, err := store.db.Exec("DELETE FROM websites WHERE id = ?", siteID); err != nil {
		t.Fatalf("delete website during collection: %v", err)
	}
	if err := store.persistSuccess(ctx, deletedJob, "worker-delete", deletedIdentity, result, now.Add(4*time.Second)); !errors.Is(err, errWPInventoryLeaseLost) {
		t.Fatalf("deleted site persist error = %v, want lease lost", err)
	}
	if countRows(t, store.db, "site_wp_inventory_jobs") != 0 || countRows(t, store.db, "site_wp_inventory_state") != 0 {
		t.Fatal("deleted site inventory was recreated")
	}
}

func TestWPInventoryStoreRejectsInvalidInputAndPrunesInBatches(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-a", now)
	invalid := sampleWPInventoryResult()
	invalid.Inventory.Plugins[0].File = "../secret.php"
	if err := store.persistSuccess(ctx, jobID, "worker-a", identity, invalid, now.Add(time.Second)); err == nil {
		t.Fatal("invalid inventory was persisted")
	}
	badWarning := sampleWPInventoryResult()
	badWarning.Meta.Warnings = []WPInventoryWarning{"path=/secret"}
	if err := store.persistSuccess(ctx, jobID, "worker-a", identity, badWarning, now.Add(time.Second)); err == nil {
		t.Fatal("invalid warning was persisted")
	}
	inconsistent := sampleWPInventoryResult()
	inconsistent.Meta.ProtocolExceeded = true
	if err := store.persistSuccess(ctx, jobID, "worker-a", identity, inconsistent, now.Add(time.Second)); err == nil {
		t.Fatal("inconsistent success metrics were persisted")
	}

	if _, err := store.db.Exec("DELETE FROM site_wp_inventory_jobs"); err != nil {
		t.Fatalf("clear active job: %v", err)
	}
	old := wpInventoryDBTime(now.Add(-31 * 24 * time.Hour))
	for i := 0; i < 205; i++ {
		id := fmt.Sprintf("%032x", i+1)
		if _, err := store.db.Exec(`INSERT INTO site_wp_inventory_jobs
			(id, site_id, trigger_type, status, requested_at, not_before, finished_at)
			VALUES (?, ?, 'manual', 'succeeded', ?, ?, ?)`, id, siteID, old, old, old); err != nil {
			t.Fatalf("insert old job %d: %v", i, err)
		}
		if _, err := store.db.Exec(`INSERT INTO site_wp_inventory_job_warnings (job_id, warning_code)
			VALUES (?, 'stale_runner_cleanup_failed')`, id); err != nil {
			t.Fatalf("insert old warning %d: %v", i, err)
		}
	}
	activeID, _, err := store.enqueue(ctx, siteID, wpInventoryTriggerManual, now, now)
	if err != nil {
		t.Fatalf("enqueue active job: %v", err)
	}
	deleted, err := store.prune(ctx, now.Add(-30*24*time.Hour), 200)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 200 || countRows(t, store.db, "site_wp_inventory_jobs") != 6 ||
		countRows(t, store.db, "site_wp_inventory_job_warnings") != 5 {
		t.Fatalf("prune deleted/jobs/warnings = %d/%d/%d, want 200/6/5", deleted,
			countRows(t, store.db, "site_wp_inventory_jobs"), countRows(t, store.db, "site_wp_inventory_job_warnings"))
	}
	active, err := store.getJob(ctx, activeID)
	if err != nil || active.Status != wpInventoryJobQueued {
		t.Fatalf("active job after prune = %+v, err = %v", active, err)
	}
}

func TestWPInventoryStoreMaximumDatasetBudget(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 18, 0, 0, 0, time.UTC)
	jobID, identity := enqueueAndClaimInventory(t, store, siteID, "worker-perf", now)
	result := sampleWPInventoryResult()
	result.Inventory.Plugins = make([]WPInventoryPlugin, wpInventoryPluginLimit)
	result.Inventory.Themes = make([]WPInventoryTheme, wpInventoryThemeLimit)
	result.Inventory.CurrentTheme = nil
	result.Inventory.Updates.Core.Items = nil
	result.Inventory.Updates.Plugins.Items = make([]WPInventoryComponentUpdate, wpInventoryPluginLimit)
	result.Inventory.Updates.Themes.Items = make([]WPInventoryComponentUpdate, wpInventoryThemeLimit)
	for i := range result.Inventory.Plugins {
		key := fmt.Sprintf("plugin-%04d/plugin.php", i)
		result.Inventory.Plugins[i] = WPInventoryPlugin{File: key, Name: key, Version: "1.0"}
		result.Inventory.Updates.Plugins.Items[i] = WPInventoryComponentUpdate{ID: key, Version: "2.0"}
	}
	for i := range result.Inventory.Themes {
		key := fmt.Sprintf("theme-%04d", i)
		result.Inventory.Themes[i] = WPInventoryTheme{Stylesheet: key, Name: key, Version: "1.0"}
		result.Inventory.Updates.Themes.Items[i] = WPInventoryComponentUpdate{ID: key, Version: "2.0"}
	}

	started := time.Now()
	if err := store.persistSuccess(ctx, jobID, "worker-perf", identity, result, now.Add(time.Second)); err != nil {
		t.Fatalf("persist maximum dataset: %v", err)
	}
	elapsed := time.Since(started)
	t.Logf("maximum inventory persistence: %s", elapsed)
	if elapsed > 2*time.Second {
		t.Fatalf("maximum inventory persistence = %s, budget 2s", elapsed)
	}
	if got := countRows(t, store.db, "site_wp_components"); got != 1+wpInventoryPluginLimit+wpInventoryThemeLimit {
		t.Fatalf("maximum components = %d", got)
	}
	if got := countRows(t, store.db, "site_wp_component_updates"); got != wpInventoryUpdateLimit {
		t.Fatalf("maximum updates = %d", got)
	}
	var pageCount, pageSize int64
	if err := store.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		t.Fatalf("query page_count: %v", err)
	}
	if err := store.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatalf("query page_size: %v", err)
	}
	t.Logf("maximum inventory sqlite pages: %d bytes", pageCount*pageSize)

	typicalStarted := time.Now()
	for i := 0; i < 100; i++ {
		domain := fmt.Sprintf("typical-%03d.example.com", i)
		siteResult, err := store.db.Exec(`INSERT INTO websites
			(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
			VALUES (?, ?, 'active', 'wp_typical', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf')`, domain, domain)
		if err != nil {
			t.Fatalf("insert typical site %d: %v", i, err)
		}
		typicalSiteID, err := siteResult.LastInsertId()
		if err != nil {
			t.Fatalf("typical site id %d: %v", i, err)
		}
		at := now.Add(time.Duration(i+1) * time.Minute)
		typicalJob, typicalIdentity := enqueueAndClaimInventory(t, store, int(typicalSiteID), fmt.Sprintf("worker-%03d", i), at)
		if err := store.persistSuccess(ctx, typicalJob, fmt.Sprintf("worker-%03d", i), typicalIdentity, sampleWPInventoryResult(), at.Add(time.Second)); err != nil {
			t.Fatalf("persist typical site %d: %v", i, err)
		}
	}
	typicalElapsed := time.Since(typicalStarted)
	t.Logf("100 typical inventory persistence: %s", typicalElapsed)
	if typicalElapsed > 10*time.Second {
		t.Fatalf("100 typical inventory persistence = %s, budget 10s", typicalElapsed)
	}
}

func TestWPInventoryStorePageSnapshotStaysConsistentDuringReplacement(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	service := newTestWPInventoryService(t, store)
	ctx := context.Background()
	writePageCollection := func(collection, prefix string, count int) error {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "DELETE FROM site_wp_components WHERE site_id = ?", siteID); err != nil {
			return err
		}
		for i := 0; i < count; i++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO site_wp_components
				(site_id, component_type, component_key, name, version, collection_id, collected_at)
				VALUES (?, 'plugin', ?, ?, '1.0', ?, '2026-07-21 07:00:00')`,
				siteID, fmt.Sprintf("%s/%03d.php", prefix, i), prefix, collection); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO site_wp_inventory_state
			(site_id, status, collection_id, last_attempt_at, last_success_at, updated_at)
			VALUES (?, 'complete', ?, '2026-07-21 07:00:00', '2026-07-21 07:00:00', '2026-07-21 07:00:00')
			ON CONFLICT(site_id) DO UPDATE SET status='complete', collection_id=excluded.collection_id,
			last_attempt_at=excluded.last_attempt_at, last_success_at=excluded.last_success_at,
			last_error_code='', last_error_stage='', updated_at=excluded.updated_at`, siteID, collection)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := writePageCollection("collection-a", "a", 3); err != nil {
		t.Fatalf("write initial collection: %v", err)
	}

	writerErrors := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 120; i++ {
			if i%2 == 0 {
				if err := writePageCollection("collection-b", "b", 7); err != nil {
					writerErrors <- err
					return
				}
			} else if err := writePageCollection("collection-a", "a", 3); err != nil {
				writerErrors <- err
				return
			}
		}
	}()

	options := WPInventoryListOptions{Page: 1, PageSize: 100}
	for i := 0; i < 400; i++ {
		page, err := service.Components(ctx, siteID, options)
		if err != nil {
			t.Fatalf("Components() during replacement: %v", err)
		}
		items := wpInventoryComponentItems(t, page)
		if page.Total != len(items) || (page.Total != 3 && page.Total != 7) {
			t.Fatalf("mixed page total/items = %d/%d", page.Total, len(items))
		}
		wantPrefix := "a/"
		if page.Total == 7 {
			wantPrefix = "b/"
		}
		for _, item := range items {
			if !strings.HasPrefix(item.Key, wantPrefix) {
				t.Fatalf("mixed collection total=%d key=%q, want prefix %q", page.Total, item.Key, wantPrefix)
			}
		}
		select {
		case <-done:
			if i > 120 {
				i = 400
			}
		default:
		}
	}
	<-done
	select {
	case err := <-writerErrors:
		t.Fatalf("replace collection: %v", err)
	default:
	}
}

func TestWPInventoryPageMaximumDatasetBudget(t *testing.T) {
	store, siteID := newWPInventoryStoreTest(t)
	ctx := context.Background()
	const rowsPerTable = 10000
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(): %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO site_wp_inventory_state
		(site_id, status, collection_id, last_attempt_at, last_success_at, updated_at)
		VALUES (?, 'complete', 'page-budget', '2026-07-21 08:00:00', '2026-07-21 08:00:00', '2026-07-21 08:00:00')`, siteID); err != nil {
		t.Fatalf("insert page state: %v", err)
	}
	componentStmt, err := tx.Prepare(`INSERT INTO site_wp_components
		(site_id, component_type, component_key, name, version, collection_id, collected_at)
		VALUES (?, 'plugin', ?, ?, '1.0', 'page-budget', '2026-07-21 08:00:00')`)
	if err != nil {
		t.Fatalf("prepare components: %v", err)
	}
	updateStmt, err := tx.Prepare(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (?, 'plugin', ?, '2.0', '', '', 'page-budget', '2026-07-21 08:00:00')`)
	if err != nil {
		t.Fatalf("prepare updates: %v", err)
	}
	for i := 0; i < rowsPerTable; i++ {
		key := fmt.Sprintf("plugin-%05d/plugin.php", i)
		if _, err := componentStmt.Exec(siteID, key, fmt.Sprintf("Plugin %05d", i)); err != nil {
			t.Fatalf("insert component %d: %v", i, err)
		}
		if _, err := updateStmt.Exec(siteID, key); err != nil {
			t.Fatalf("insert update %d: %v", i, err)
		}
	}
	_ = componentStmt.Close()
	_ = updateStmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit page dataset: %v", err)
	}

	for _, query := range []struct {
		name string
		sql  string
		args []any
	}{
		{name: "components", sql: `EXPLAIN QUERY PLAN SELECT COUNT(*) FROM site_wp_components WHERE ` + wpInventoryComponentPageWhereSQL,
			args: []any{siteID, "page-budget", "", "", "", "%", "%", "%"}},
		{name: "updates", sql: `EXPLAIN QUERY PLAN SELECT COUNT(*) FROM site_wp_component_updates WHERE ` + wpInventoryUpdatePageWhereSQL,
			args: []any{siteID, "page-budget", "", "", "", "%", "%"}},
	} {
		rows, err := store.db.Query(query.sql, query.args...)
		if err != nil {
			t.Fatalf("EXPLAIN %s: %v", query.name, err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatalf("scan EXPLAIN %s: %v", query.name, err)
			}
			t.Logf("%s query plan: %s", query.name, detail)
		}
		_ = rows.Close()
	}

	started := time.Now()
	components, err := store.getComponentPage(ctx, siteID, "", "plugin-099", 100, 0)
	componentElapsed := time.Since(started)
	if err != nil || components.Total != 100 || len(components.Items) != 100 {
		t.Fatalf("component budget page = %d/%d, err = %v", components.Total, len(components.Items), err)
	}
	started = time.Now()
	updates, err := store.getUpdatePage(ctx, siteID, "", "plugin-099", 100, 0)
	updateElapsed := time.Since(started)
	if err != nil || updates.Total != 100 || len(updates.Items) != 100 {
		t.Fatalf("update budget page = %d/%d, err = %v", updates.Total, len(updates.Items), err)
	}
	t.Logf("10000-row page queries: components=%s updates=%s", componentElapsed, updateElapsed)
	if componentElapsed > 200*time.Millisecond || updateElapsed > 200*time.Millisecond {
		t.Fatalf("page query budget exceeded: components=%s updates=%s budget=200ms", componentElapsed, updateElapsed)
	}
}

func newWPInventoryStoreTest(t *testing.T) (*wpInventoryStore, int) {
	t.Helper()
	if database.DB != nil {
		_ = database.Close()
	}
	dbPath := filepath.Join(t.TempDir(), "panel.db")
	if err := database.Open(dbPath); err != nil {
		t.Fatalf("database.Open(): %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations(): %v", err)
	}
	if err := database.RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades(): %v", err)
	}
	store, err := newWPInventoryStore()
	if err != nil {
		t.Fatalf("newWPInventoryStore(): %v", err)
	}
	result, err := store.db.Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES ('inventory.example.com', 'inventory.example.com', 'active', 'wp_inventory', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf')`)
	if err != nil {
		t.Fatalf("insert website: %v", err)
	}
	siteID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("website id: %v", err)
	}
	return store, int(siteID)
}

func enqueueAndClaimInventory(t *testing.T, store *wpInventoryStore, siteID int, owner string, now time.Time) (string, wpInventorySiteIdentity) {
	t.Helper()
	ctx := context.Background()
	jobID, created, err := store.enqueue(ctx, siteID, wpInventoryTriggerManual, now, now)
	if err != nil || !created {
		t.Fatalf("enqueue = %q/%t, err = %v", jobID, created, err)
	}
	job, err := store.claim(ctx, owner, now, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if job.ID != jobID {
		t.Fatalf("claimed job = %q, want %q", job.ID, jobID)
	}
	identity, err := store.loadSiteIdentity(ctx, siteID)
	if err != nil {
		t.Fatalf("load site identity: %v", err)
	}
	return jobID, identity
}

func sampleWPInventoryResult() WPInventoryRunResult {
	return WPInventoryRunResult{
		Inventory: WPInventory{
			WordPress: WPInventoryWordPress{Version: "7.0", Locale: "zh_CN", Multisite: true},
			Plugins: []WPInventoryPlugin{
				{File: "alpha/alpha.php", Name: "Alpha", Version: "1.0", Active: true},
				{File: "beta/beta.php", Name: "Beta", Version: "2.0", NetworkActive: true},
			},
			Themes:       []WPInventoryTheme{{Stylesheet: "theme-one", Name: "Theme One", Version: "1.0"}},
			CurrentTheme: &WPInventoryCurrentTheme{Stylesheet: "theme-one", Name: "Theme One", Version: "1.0"},
			Updates: WPInventoryUpdates{
				Core: WPInventoryCoreUpdates{TransientPresent: true, LastChecked: 100, VersionChecked: "7.0", Items: []WPInventoryCoreUpdate{
					{Version: "7.1", Response: "upgrade", Locale: "zh_CN"},
				}},
				Plugins: WPInventoryComponentUpdates{TransientPresent: true, LastChecked: 101, Items: []WPInventoryComponentUpdate{
					{ID: "alpha/alpha.php", Version: "1.1"},
				}},
				Themes: WPInventoryComponentUpdates{TransientPresent: true, LastChecked: 102, Items: []WPInventoryComponentUpdate{
					{ID: "theme-one", Version: "1.1"},
				}},
			},
		},
		Meta: WPInventoryRunMeta{
			WallTime: time.Second, UserCPUTime: 100 * time.Millisecond, SystemCPUTime: 50 * time.Millisecond,
			MaxRSSKiB: 64 * 1024, ExitCode: 0, StdoutBytes: 10, StderrBytes: 20, ProtocolBytes: 100,
			RunnerHash: strings.Repeat("a", 64), RunnerVersion: "1", SchemaVersion: 1,
		},
	}
}

func countRows(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
