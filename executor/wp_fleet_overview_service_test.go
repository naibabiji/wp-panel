package executor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

var wpFleetTestNow = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

// TestWPFleetOverviewHandlesLegacyDatetimeTextFormats reproduces a real
// production failure: websites.expires_at is written elsewhere in the
// codebase as a bare date ("2006-01-02", handlers/website.go UpdateExpiry),
// and ssl_expires_at is written by passing a raw time.Time to db.Exec
// (executor/ssl.go), which the sqlite driver stores using Go's default
// time.Time.String() format ("2006-01-02 15:04:05 +0000 UTC") rather than
// the wp-inventory subsystem's own "2006-01-02 15:04:05" layout. The fleet
// overview query used to CAST these columns to TEXT and hand-parse them
// with a layout that matches neither format, failing the whole request for
// every site on any server with pre-existing (non wp-inventory-managed)
// website records.
func TestWPFleetOverviewHandlesLegacyDatetimeTextFormats(t *testing.T) {
	service, db := newWPFleetOverviewTest(t)
	if _, err := db.Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user,
		 php_pool_path, nginx_conf_path, site_type, ssl_enabled, ssl_expires_at, expires_at,
		 monitoring_enabled, file_lock_enabled, fastcgi_cache_enabled, access_log_mode, created_at)
		VALUES (1, 'legacy.example.com', 'legacy.example.com', 'active', 'wp_test', '/tmp/www', '/tmp/log',
		 'db', 'user', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress', 1,
		 '2026-08-24 10:43:42 +0000 UTC', '2027-05-05', 1, 1, 1, 'full', '2026-05-26 11:42:13')`); err != nil {
		t.Fatalf("insert legacy-format site: %v", err)
	}

	overview, err := service.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview() with legacy datetime text formats: %v", err)
	}
	if len(overview.Sites) != 1 {
		t.Fatalf("sites=%+v", overview.Sites)
	}
	site := overview.Sites[0]
	if site.CreatedAt.Format("2006-01-02") != "2026-05-26" {
		t.Fatalf("created_at=%v", site.CreatedAt)
	}
	if site.ExpiresAt == nil || site.ExpiresAt.Format("2006-01-02") != "2027-05-05" {
		t.Fatalf("expires_at=%v", site.ExpiresAt)
	}
	if site.SSLExpiresAt == nil || site.SSLExpiresAt.Format("2006-01-02 15:04:05") != "2026-08-24 10:43:42" {
		t.Fatalf("ssl_expires_at=%v", site.SSLExpiresAt)
	}
}

func TestWPFleetOverviewEmptyAndMixedSites(t *testing.T) {
	service, db := newWPFleetOverviewTest(t)
	ctx := context.Background()

	empty, err := service.Overview(ctx)
	if err != nil {
		t.Fatalf("empty Overview(): %v", err)
	}
	if empty.Sites == nil || len(empty.Sites) != 0 || empty.Counts.TotalSites != 0 {
		t.Fatalf("empty overview = %+v", empty)
	}

	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 1, Domain: "updates.example.com", SiteType: "wordpress", Status: "active",
		SSLEnabled: true, SSLExpiresAt: wpFleetTestNow.Add(30 * 24 * time.Hour), BackupEnabled: true,
	})
	insertWPFleetState(t, db, 1, "complete", "collection-current", "7.0", 0, 0,
		wpFleetTestNow.Add(-time.Hour), wpFleetTestNow.Add(-time.Hour), "", "")
	insertWPFleetCoreUpdate(t, db, 1, "collection-old", "upgrade")
	insertWPFleetCoreUpdate(t, db, 1, "collection-current", "latest")
	insertWPFleetCoreUpdate(t, db, 1, "collection-current", "development")
	insertWPFleetCoreUpdate(t, db, 1, "collection-current", "autoupdate")
	insertWPFleetCoreUpdate(t, db, 1, "collection-current", "")
	insertWPFleetCoreUpdate(t, db, 1, "collection-current", "future-value")
	insertWPFleetCoreUpdate(t, db, 1, "collection-current", "upgrade")
	insertWPFleetComponentUpdate(t, db, 1, "collection-current", "plugin", "demo-a/demo-a.php", "2.0")
	insertWPFleetComponentUpdate(t, db, 1, "collection-current", "plugin", "demo-b/demo-b.php", "3.0")
	insertWPFleetComponentUpdate(t, db, 1, "collection-current", "theme", "demo-theme", "1.5")
	// Already-satisfied candidate (stale WordPress update-check transient):
	// target equals the installed version, so it must not be counted.
	insertWPFleetSatisfiedComponentUpdate(t, db, 1, "collection-current", "plugin", "demo-c/demo-c.php", "1.0")

	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 2, Domain: "php.example.com", SiteType: "php", Status: "paused",
	})
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 3, Domain: "unknown.example.com", SiteType: "wordpress", Status: "creating",
	})
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 4, Domain: "failed.example.com", SiteType: "wordpress", Status: "deleting",
		SSLEnabled: true, SSLExpiresAt: wpFleetTestNow.Add(14 * 24 * time.Hour),
	})
	insertWPFleetState(t, db, 4, "failed", "collection-failed", "6.9", 0, 0,
		wpFleetTestNow.Add(-time.Hour), wpFleetTestNow.Add(-8*24*time.Hour), "runner_timeout", "execute")
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 5, Domain: "error.example.com", SiteType: "wordpress", Status: "error",
	})
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 6, Domain: "expired.example.com", SiteType: "php", Status: "active",
		SSLEnabled: true, SSLExpiresAt: wpFleetTestNow,
	})

	overview, err := service.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview(): %v", err)
	}
	if overview.GeneratedAt != wpFleetTestNow || len(overview.Sites) != 6 {
		t.Fatalf("overview generated/sites = %s/%d", overview.GeneratedAt, len(overview.Sites))
	}
	byID := wpFleetSitesByID(overview.Sites)
	updates := byID[1]
	if updates.Inventory == nil || updates.Inventory.Status != "complete" ||
		updates.Inventory.UpdateTotal != 4 || !updates.Inventory.CoreUpgradeAvailable ||
		updates.Health.Level != "warning" || !equalStrings(updates.Health.Issues, []string{"updates_available"}) {
		t.Fatalf("updates site = %+v", updates)
	}
	if byID[2].Inventory != nil || byID[2].Status != "paused" || byID[2].Health.Level != "healthy" {
		t.Fatalf("php site = %+v", byID[2])
	}
	if byID[3].Status != "creating" || byID[3].Inventory == nil ||
		byID[3].Inventory.Status != "unknown" || byID[3].Health.Level != "unknown" {
		t.Fatalf("unknown site = %+v", byID[3])
	}
	failed := byID[4]
	if failed.Status != "deleting" || failed.Inventory == nil || failed.Inventory.Status != "failed" ||
		!failed.Inventory.HasSuccessfulInventory || failed.Inventory.WordPressVersion != "6.9" ||
		!failed.Inventory.Stale || failed.SSLState != "expiring" ||
		!equalStrings(failed.Health.Issues, []string{"ssl_expiring", "inventory_failed", "inventory_stale"}) {
		t.Fatalf("failed site = %+v", failed)
	}
	if byID[5].Status != "error" || byID[5].Health.Level != "critical" ||
		!equalStrings(byID[5].Health.Issues, []string{"site_error", "inventory_uncollected"}) {
		t.Fatalf("error site = %+v", byID[5])
	}
	if byID[6].SSLState != "expired" || byID[6].Health.Level != "critical" {
		t.Fatalf("expired site = %+v", byID[6])
	}
	wantCounts := struct {
		total, wordpress, critical, warning, unknown, healthy, updates, failed, stale, attention, uncollected int
	}{6, 4, 2, 2, 1, 1, 1, 1, 1, 1, 2}
	got := overview.Counts
	if got.TotalSites != wantCounts.total || got.WordPressSites != wantCounts.wordpress ||
		got.CriticalSites != wantCounts.critical || got.WarningSites != wantCounts.warning ||
		got.UnknownSites != wantCounts.unknown || got.HealthySites != wantCounts.healthy ||
		got.UpdateSites != wantCounts.updates || got.FailedInventorySites != wantCounts.failed ||
		got.StaleInventorySites != wantCounts.stale || got.InventoryAttentionSites != wantCounts.attention ||
		got.UncollectedSites != wantCounts.uncollected {
		t.Fatalf("counts = %+v", got)
	}
	if got.CriticalSites+got.WarningSites+got.UnknownSites+got.HealthySites != got.TotalSites {
		t.Fatalf("health counts do not sum to total: %+v", got)
	}
}

func TestWPFleetOverviewActiveTaskAndHealthBoundaries(t *testing.T) {
	service, db := newWPFleetOverviewTest(t)
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 1, Domain: "queued.example.com", SiteType: "wordpress", Status: "active",
		SSLEnabled: true, SSLExpiresAt: wpFleetTestNow.Add(14*24*time.Hour + time.Second),
	})
	insertWPFleetState(t, db, 1, "complete", "queued-old", "7.0", 0, 0,
		wpFleetTestNow.Add(-time.Hour), wpFleetTestNow.Add(-7*24*time.Hour), "", "")
	insertWPFleetActiveJob(t, db, 1, "queued")
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 2, Domain: "running.example.com", SiteType: "wordpress", Status: "active",
		SSLEnabled: true,
	})
	insertWPFleetActiveJob(t, db, 2, "running")
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 3, Domain: "pending.example.com", SiteType: "php", Status: "active", SSLLastError: "private detail",
	})
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 4, Domain: "disabled.example.com", SiteType: "php", Status: "active",
	})
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 5, Domain: "expiring.example.com", SiteType: "php", Status: "active",
		SSLEnabled: true, SSLExpiresAt: wpFleetTestNow.Add(14 * 24 * time.Hour),
	})
	insertWPFleetSite(t, db, wpFleetTestSite{
		ID: 6, Domain: "failed-empty.example.com", SiteType: "wordpress", Status: "active",
	})
	insertWPFleetState(t, db, 6, "failed", "", "", 0, 0,
		wpFleetTestNow.Add(-time.Hour), time.Time{}, "runner_timeout", "execute")

	overview, err := service.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview(): %v", err)
	}
	byID := wpFleetSitesByID(overview.Sites)
	if byID[1].Inventory.Status != "queued" || byID[1].Inventory.Stale || byID[1].SSLState != "valid" {
		t.Fatalf("queued boundary = %+v", byID[1])
	}
	if byID[2].Inventory.Status != "running" || byID[2].Health.Level != "warning" ||
		!equalStrings(byID[2].Health.Issues, []string{"ssl_expiry_unknown", "inventory_uncollected"}) {
		t.Fatalf("running site = %+v", byID[2])
	}
	if byID[3].SSLState != "pending_error" || byID[3].Health.Level != "warning" ||
		!equalStrings(byID[3].Health.Issues, []string{"ssl_setup_failed"}) {
		t.Fatalf("pending SSL = %+v", byID[3])
	}
	if byID[4].SSLState != "disabled" || byID[4].Health.Level != "healthy" {
		t.Fatalf("disabled SSL = %+v", byID[4])
	}
	if byID[5].SSLState != "expiring" || byID[5].Health.Level != "warning" {
		t.Fatalf("expiring boundary = %+v", byID[5])
	}
	if byID[6].Inventory == nil || byID[6].Inventory.Status != "failed" ||
		byID[6].Inventory.HasSuccessfulInventory || byID[6].Health.Level != "warning" ||
		!equalStrings(byID[6].Health.Issues, []string{"inventory_failed", "inventory_uncollected"}) {
		t.Fatalf("failed empty = %+v", byID[6])
	}
}

func TestWPFleetOverviewDisabledUpdateChecksDoNotReportCachedUpdates(t *testing.T) {
	service, db := newWPFleetOverviewTest(t)
	insertWPFleetSite(t, db, wpFleetTestSite{ID: 1, Domain: "disabled-updates.example.com", SiteType: "wordpress", Status: "active", UpdateChecksDisabled: true})
	insertWPFleetState(t, db, 1, "complete", "cached", "7.0", 1, 0, wpFleetTestNow, wpFleetTestNow, "", "")
	insertWPFleetComponentUpdate(t, db, 1, "cached", "plugin", "demo/demo.php", "2.0")
	overview, err := service.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	site := overview.Sites[0]
	if !site.UpdateChecksDisabled || site.Inventory == nil || site.Inventory.UpdateTotal != 1 {
		t.Fatalf("site = %+v", site)
	}
	if site.Health.Level != "healthy" || len(site.Health.Issues) != 0 {
		t.Fatalf("disabled checks must suppress cached update warning: %+v", site.Health)
	}
	if overview.Counts.UpdateSites != 0 || overview.Counts.WarningSites != 0 {
		t.Fatalf("counts = %+v", overview.Counts)
	}
}

func TestWPFleetOverviewUsesSingleQuery(t *testing.T) {
	source, err := os.ReadFile("wp_fleet_overview_service.go")
	if err != nil {
		t.Fatalf("read service source: %v", err)
	}
	if got := bytes.Count(source, []byte(".QueryContext(")); got != 1 {
		t.Fatalf("QueryContext calls = %d, want 1", got)
	}
	for _, forbidden := range [][]byte{[]byte(".QueryRow("), []byte(".QueryRowContext("), []byte(".Summary(")} {
		if bytes.Contains(source, forbidden) {
			t.Fatalf("service contains forbidden per-site query %q", forbidden)
		}
	}
}

func TestWPFleetOverviewRejectsInvalidStoredStateAndHidesSensitiveFields(t *testing.T) {
	service, db := newWPFleetOverviewTest(t)
	insertWPFleetSite(t, db, wpFleetTestSite{ID: 1, Domain: "safe.example.com", SiteType: "wordpress", Status: "active"})
	insertWPFleetState(t, db, 1, "complete", "safe-collection", "7.0", 0, 0,
		wpFleetTestNow, wpFleetTestNow, "", "")
	overview, err := service.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview(): %v", err)
	}
	payload, err := json.Marshal(overview)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	for _, forbidden := range []string{
		"system_user", "web_root", "log_dir", "db_name", "db_user", "php_pool_path", "nginx_conf_path",
		"fastcgi_cache_key", "ssl_last_error", "ssl_cert_path", "ssl_key_path", "collection_id",
		"lease_owner", "runner_hash", "runner_version", "last_error_stage", "response", "stdout", "max_rss",
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("fleet payload exposed %q: %s", forbidden, payload)
		}
	}

	if _, err := db.Exec(`UPDATE site_wp_inventory_state SET last_success_at = NULL WHERE site_id = 1`); err != nil {
		t.Fatalf("corrupt inventory state: %v", err)
	}
	if _, err := service.Overview(context.Background()); err == nil {
		t.Fatal("Overview() accepted inconsistent inventory state")
	}
}

func TestWPFleetOverviewSnapshotDuringReplacement(t *testing.T) {
	service, db := newWPFleetOverviewTest(t)
	insertWPFleetSite(t, db, wpFleetTestSite{ID: 1, Domain: "snapshot.example.com", SiteType: "wordpress", Status: "active"})
	if err := replaceWPFleetSnapshot(db, "A", 1, false); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 80; i++ {
			version, pluginUpdates, core := "A", 1, false
			if i%2 == 1 {
				version, pluginUpdates, core = "B", 2, true
			}
			if err := replaceWPFleetSnapshot(db, version, pluginUpdates, core); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 160; i++ {
		overview, err := service.Overview(context.Background())
		if err != nil {
			t.Fatalf("Overview() during replacement: %v", err)
		}
		inv := overview.Sites[0].Inventory
		if inv.WordPressVersion == "A" && (inv.PluginUpdates != 1 || inv.CoreUpgradeAvailable || inv.UpdateTotal != 1) {
			t.Fatalf("mixed A snapshot: %+v", inv)
		}
		if inv.WordPressVersion == "B" && (inv.PluginUpdates != 2 || !inv.CoreUpgradeAvailable || inv.UpdateTotal != 3) {
			t.Fatalf("mixed B snapshot: %+v", inv)
		}
		if inv.WordPressVersion != "A" && inv.WordPressVersion != "B" {
			t.Fatalf("unexpected snapshot: %+v", inv)
		}
	}
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatalf("snapshot writer: %v", err)
	default:
	}
}

func TestWPFleetOverviewMaximumDatasetBudget(t *testing.T) {
	service, db := newWPFleetOverviewTest(t)
	insertWPFleetBudgetSites(t, db, 1, 100)
	p95At100, bytesAt100 := measureWPFleetOverview(t, service, 100)
	t.Logf("100-site fleet overview: p95=%s json=%d bytes", p95At100, bytesAt100)
	if p95At100 > 100*time.Millisecond {
		t.Fatalf("100-site overview p95 = %s, budget 100ms", p95At100)
	}
	if bytesAt100 > 256*1024 {
		t.Fatalf("100-site JSON = %d bytes, budget %d", bytesAt100, 256*1024)
	}

	insertWPFleetBudgetSites(t, db, 101, 300)
	plans := make([]string, 0)
	planRows, err := db.Query("EXPLAIN QUERY PLAN " + wpFleetOverviewSQL)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plans = append(plans, detail)
	}
	_ = planRows.Close()
	planText := strings.Join(plans, "\n")
	for _, table := range []string{"bs", "s", "j", "u"} {
		if !strings.Contains(planText, "SEARCH "+table) {
			t.Fatalf("query plan does not use a lookup for %s:\n%s", table, planText)
		}
	}

	p95At300, bytesAt300 := measureWPFleetOverview(t, service, 300)
	t.Logf("300-site fleet overview: p95=%s json=%d bytes query-plan=%q", p95At300, bytesAt300, plans)
	if p95At300 > 200*time.Millisecond {
		t.Fatalf("300-site overview p95 = %s, budget 200ms", p95At300)
	}
	if bytesAt300 > 768*1024 {
		t.Fatalf("300-site JSON = %d bytes, budget %d", bytesAt300, 768*1024)
	}
}

func insertWPFleetBudgetSites(t *testing.T, db *sql.DB, first, last int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	websiteStmt, err := tx.Prepare(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user,
		php_pool_path, nginx_conf_path, site_type, created_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare websites: %v", err)
	}
	stateStmt, err := tx.Prepare(`INSERT INTO site_wp_inventory_state
		(site_id, status, wordpress_version, plugin_update_count, theme_update_count,
		collection_id, last_attempt_at, last_success_at, updated_at)
		VALUES (?, 'complete', '7.0', 1, 1, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare states: %v", err)
	}
	for i := first; i <= last; i++ {
		domain := fmt.Sprintf("site-%03d.example.com", i)
		siteType := "php"
		if i%2 == 0 {
			siteType = "wordpress"
		}
		if _, err := websiteStmt.Exec(i, domain, domain, "wp_test", "/tmp/www", "/tmp/log", "db", "user",
			"/tmp/php.conf", "/tmp/nginx.conf", siteType, wpInventoryDBTime(wpFleetTestNow)); err != nil {
			t.Fatalf("insert website %d: %v", i, err)
		}
		if siteType == "wordpress" {
			collection := fmt.Sprintf("collection-%03d", i)
			at := wpInventoryDBTime(wpFleetTestNow.Add(-time.Hour))
			if _, err := stateStmt.Exec(i, collection, at, at, at); err != nil {
				t.Fatalf("insert state %d: %v", i, err)
			}
		}
	}
	_ = websiteStmt.Close()
	_ = stateStmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit dataset: %v", err)
	}
}

func measureWPFleetOverview(t *testing.T, service *WPFleetOverviewService, wantSites int) (time.Duration, int) {
	t.Helper()
	durations := make([]time.Duration, 0, 20)
	var payloadSize int
	for i := 0; i < 20; i++ {
		started := time.Now()
		overview, err := service.Overview(context.Background())
		if err != nil {
			t.Fatalf("Overview() run %d: %v", i, err)
		}
		durations = append(durations, time.Since(started))
		if len(overview.Sites) != wantSites {
			t.Fatalf("sites = %d, want %d", len(overview.Sites), wantSites)
		}
		payload, err := json.Marshal(overview)
		if err != nil {
			t.Fatalf("json.Marshal(): %v", err)
		}
		payloadSize = len(payload)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[18], payloadSize
}

type wpFleetTestSite struct {
	ID                   int
	Domain               string
	SiteType             string
	Status               string
	SSLEnabled           bool
	SSLExpiresAt         time.Time
	SSLLastError         string
	BackupEnabled        bool
	UpdateChecksDisabled bool
}

func newWPFleetOverviewTest(t *testing.T) (*WPFleetOverviewService, *sql.DB) {
	t.Helper()
	store, _ := newWPInventoryStoreTest(t)
	if _, err := store.db.Exec("DELETE FROM websites"); err != nil {
		t.Fatalf("clear websites: %v", err)
	}
	service, err := newWPFleetOverviewService(store.db, func() time.Time { return wpFleetTestNow })
	if err != nil {
		t.Fatalf("newWPFleetOverviewService(): %v", err)
	}
	return service, store.db
}

func insertWPFleetSite(t *testing.T, db *sql.DB, site wpFleetTestSite) {
	t.Helper()
	sslExpiry := any(nil)
	if !site.SSLExpiresAt.IsZero() {
		sslExpiry = wpInventoryDBTime(site.SSLExpiresAt)
	}
	if _, err := db.Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user,
		php_pool_path, nginx_conf_path, site_type, ssl_enabled, ssl_expires_at, ssl_last_error,
		monitoring_enabled, file_lock_enabled, fastcgi_cache_enabled, access_log_mode, disable_wp_updates, created_at)
		VALUES (?, ?, ?, ?, ?, '/tmp/www', '/tmp/log', 'db', 'user', '/tmp/php.conf',
		'/tmp/nginx.conf', ?, ?, ?, ?, 1, 1, 1, 'full', ?, ?)`,
		site.ID, site.Domain, site.Domain, site.Status, "wp_test", site.SiteType, boolDB(site.SSLEnabled),
		sslExpiry, site.SSLLastError, boolDB(site.UpdateChecksDisabled), wpInventoryDBTime(wpFleetTestNow.Add(-time.Duration(site.ID)*time.Minute))); err != nil {
		t.Fatalf("insert site %d: %v", site.ID, err)
	}
	if site.BackupEnabled {
		if _, err := db.Exec(`INSERT INTO backup_settings (site_id, enabled) VALUES (?, 1)`, site.ID); err != nil {
			t.Fatalf("insert backup settings %d: %v", site.ID, err)
		}
	}
}

func insertWPFleetState(t *testing.T, db *sql.DB, siteID int, status, collection, version string,
	pluginUpdates, themeUpdates int, lastAttempt, lastSuccess time.Time, errorCode, errorStage string,
) {
	t.Helper()
	lastSuccessValue := any(nil)
	if !lastSuccess.IsZero() {
		lastSuccessValue = wpInventoryDBTime(lastSuccess)
	}
	if _, err := db.Exec(`INSERT INTO site_wp_inventory_state
		(site_id, status, wordpress_version, plugin_update_count, theme_update_count,
		collection_id, last_attempt_at, last_success_at, last_error_code, last_error_stage, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, siteID, status, version, pluginUpdates, themeUpdates,
		collection, wpInventoryDBTime(lastAttempt), lastSuccessValue, errorCode, errorStage, wpInventoryDBTime(lastAttempt)); err != nil {
		t.Fatalf("insert inventory state %d: %v", siteID, err)
	}
}

func insertWPFleetCoreUpdate(t *testing.T, db *sql.DB, siteID int, collection, response string) {
	t.Helper()
	key := fmt.Sprintf("wordpress-%s-%s", collection, response)
	if _, err := db.Exec(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (?, 'core', ?, '7.1', ?, 'zh_CN', ?, ?)`, siteID, key, response, collection,
		wpInventoryDBTime(wpFleetTestNow)); err != nil {
		t.Fatalf("insert core update: %v", err)
	}
}

// insertWPFleetComponentUpdate seeds a plugin/theme update candidate row
// without a matching site_wp_components row, so it is counted as available
// (the fleet overview fails open when it cannot determine the installed
// version of a component).
func insertWPFleetComponentUpdate(t *testing.T, db *sql.DB, siteID int, collection, componentType, key, targetVersion string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO site_wp_component_updates
		(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
		VALUES (?, ?, ?, ?, '', '', ?, ?)`, siteID, componentType, key, targetVersion, collection,
		wpInventoryDBTime(wpFleetTestNow)); err != nil {
		t.Fatalf("insert component update: %v", err)
	}
}

// insertWPFleetSatisfiedComponentUpdate seeds a plugin/theme update candidate
// row whose target version already matches the installed version recorded in
// site_wp_components — the stale-transient scenario the fleet overview must
// exclude from its counts.
func insertWPFleetSatisfiedComponentUpdate(t *testing.T, db *sql.DB, siteID int, collection, componentType, key, version string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO site_wp_components
		(site_id, component_type, component_key, name, version, is_active, is_network_active, is_current_theme, collection_id, collected_at)
		VALUES (?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`, siteID, componentType, key, key, version, collection,
		wpInventoryDBTime(wpFleetTestNow)); err != nil {
		t.Fatalf("insert satisfied component: %v", err)
	}
	insertWPFleetComponentUpdate(t, db, siteID, collection, componentType, key, version)
}

func insertWPFleetActiveJob(t *testing.T, db *sql.DB, siteID int, status string) {
	t.Helper()
	id := fmt.Sprintf("%032x", siteID)
	if _, err := db.Exec(`INSERT INTO site_wp_inventory_jobs
		(id, site_id, trigger_type, status, requested_at, not_before)
		VALUES (?, ?, 'manual', ?, ?, ?)`, id, siteID, status,
		wpInventoryDBTime(wpFleetTestNow), wpInventoryDBTime(wpFleetTestNow)); err != nil {
		t.Fatalf("insert active job: %v", err)
	}
}

func wpFleetSitesByID(sites []models.WPFleetSite) map[int]models.WPFleetSite {
	result := make(map[int]models.WPFleetSite, len(sites))
	for _, site := range sites {
		result[site.ID] = site
	}
	return result
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func replaceWPFleetSnapshot(db *sql.DB, version string, pluginUpdates int, core bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	collection := "snapshot-" + version
	at := wpInventoryDBTime(wpFleetTestNow)
	if _, err := tx.Exec(`INSERT INTO site_wp_inventory_state
		(site_id, status, wordpress_version, plugin_update_count, theme_update_count,
		collection_id, last_attempt_at, last_success_at, updated_at)
		VALUES (1, 'complete', ?, ?, 0, ?, ?, ?, ?)
		ON CONFLICT(site_id) DO UPDATE SET status = 'complete', wordpress_version = excluded.wordpress_version,
		plugin_update_count = excluded.plugin_update_count, theme_update_count = 0,
		collection_id = excluded.collection_id, last_attempt_at = excluded.last_attempt_at,
		last_success_at = excluded.last_success_at, last_error_code = '', last_error_stage = '',
		updated_at = excluded.updated_at`, version, pluginUpdates, collection, at, at, at); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM site_wp_component_updates WHERE site_id = 1`); err != nil {
		return err
	}
	if core {
		if _, err := tx.Exec(`INSERT INTO site_wp_component_updates
			(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
			VALUES (1, 'core', 'wordpress', '7.1', 'upgrade', 'zh_CN', ?, ?)`, collection, at); err != nil {
			return err
		}
	}
	for i := 0; i < pluginUpdates; i++ {
		key := fmt.Sprintf("plugin-%d/plugin-%d.php", i, i)
		if _, err := tx.Exec(`INSERT INTO site_wp_component_updates
			(site_id, component_type, component_key, target_version, response, locale, collection_id, collected_at)
			VALUES (1, 'plugin', ?, '2.0', '', '', ?, ?)`, key, collection, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}
