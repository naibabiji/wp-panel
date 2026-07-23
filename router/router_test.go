package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/naibabiji/wp-panel/i18n"
)

var pageTemplates = map[string]string{
	"login.html":                 "",
	"dashboard.html":             "dashboard_content",
	"websites.html":              "websites_content",
	"wordpress_overview.html":    "wordpress_overview_content",
	"website_new.html":           "websites_new_content",
	"website_detail.html":        "websites_detail_content",
	"wordpress_site_detail.html": "wordpress_site_detail_content",
	"databases.html":             "databases_content",
	"database_detail.html":       "database_detail_content",
	"ai_diagnostics.html":        "ai_diagnostics_content",
	"cron.html":                  "cron_content",
	"backups.html":               "backups_content",
	"firewall.html":              "firewall_content",
	"files.html":                 "files_content",
	"security.html":              "security_content",
	"settings.html":              "settings_content",
	"alert.html":                 "alert_content",
	"extension.html":             "extensions_content",
	"software.html":              "software_content",
	"help.html":                  "help_content",
}

func TestPageTemplatesRender(t *testing.T) {
	for page, content := range pageTemplates {
		t.Run(page, func(t *testing.T) {
			if output := renderPage(t, page, content); len(output) == 0 {
				t.Fatalf("render %s: empty output", page)
			}
		})
	}
}

func TestContentTemplatesRender(t *testing.T) {
	contents := []string{
		"dashboard_content", "websites_content", "wordpress_overview_content", "websites_new_content",
		"websites_detail_content", "wordpress_site_detail_content", "databases_content", "database_detail_content", "ai_diagnostics_content", "cron_content", "backups_content", "firewall_content",
		"files_content", "security_content", "settings_content",
		"alert_content", "extensions_content", "software_content", "help_content",
	}
	for _, content := range contents {
		t.Run(content, func(t *testing.T) {
			tmpl := parseTemplates(t)
			var output bytes.Buffer
			if err := tmpl.ExecuteTemplate(&output, content, testPageData("")); err != nil {
				t.Fatalf("render %s: %v", content, err)
			}
		})
	}
}

func TestRenderedPageScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}

	scriptPattern := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	for page, content := range pageTemplates {
		t.Run(page, func(t *testing.T) {
			rendered := renderPage(t, page, content)
			for index, script := range scriptPattern.FindAllSubmatch(rendered, -1) {
				if len(bytes.TrimSpace(script[1])) == 0 {
					continue
				}
				scriptPath := filepath.Join(t.TempDir(), fmt.Sprintf("%s-%d.js", page, index))
				if err := os.WriteFile(scriptPath, script[1], 0600); err != nil {
					t.Fatal(err)
				}
				if output, err := exec.Command(node, "--check", scriptPath).CombinedOutput(); err != nil {
					t.Fatalf("%s inline script %d: invalid JavaScript: %v\n%s", page, index+1, err, output)
				}
			}
		})
	}
}

func TestWebsiteLogRoutesRegistered(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`protected.GET("/api/websites/:id/log-files", websiteHandler.ListLogFiles)`,
		`protected.GET("/api/websites/:id/logs/download", websiteHandler.DownloadLogFile)`,
	} {
		if !bytes.Contains(source, []byte(route)) {
			t.Fatalf("router.go missing route %s", route)
		}
	}
}

func TestWebsiteProtectionRoutesRegistered(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`protected.PUT("/api/websites/:id/file-editor", websiteHandler.SetFileEditingProtection)`,
		`protected.PUT("/api/websites/:id/file-lock", websiteHandler.SetFileLock)`,
		`protected.GET("/api/websites/:id/file-lock/preview", websiteHandler.PreviewFileLock)`,
	} {
		if !bytes.Contains(source, []byte(route)) {
			t.Fatalf("router.go missing route %s", route)
		}
	}
}

func TestWPInventoryRoutesRegistered(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`protected.GET("/api/websites/:id/wp-inventory", wpInventoryHandler.Summary)`,
		`protected.POST("/api/websites/:id/wp-inventory/refresh", wpInventoryHandler.Refresh)`,
		`protected.GET("/api/websites/:id/wp-inventory/tasks/:task_id", wpInventoryHandler.Task)`,
		`protected.GET("/api/websites/:id/wp-inventory/components", wpInventoryHandler.Components)`,
		`protected.GET("/api/websites/:id/wp-inventory/updates", wpInventoryHandler.Updates)`,
	} {
		if !bytes.Contains(source, []byte(route)) {
			t.Fatalf("router.go missing protected inventory route %s", route)
		}
	}
}

func TestWPCoreUpdateRoutesRegisteredOnProtectedGroup(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`protected.GET("/api/websites/:id/wp-core-update/preview", wpCoreUpdateHandler.Preview)`,
		`protected.POST("/api/websites/:id/wp-core-update/confirm", wpCoreUpdateHandler.Confirm)`,
		`protected.GET("/api/websites/:id/wp-core-update/tasks/latest", wpCoreUpdateHandler.LatestTask)`,
		`protected.GET("/api/websites/:id/wp-core-update/tasks/:task_id", wpCoreUpdateHandler.Task)`,
	} {
		if !bytes.Contains(source, []byte(route)) {
			t.Fatalf("missing protected route %s", route)
		}
	}
}

func TestWPCoreUpdateHandlerKeepsNilInterfaceWhenConstructionFails(t *testing.T) {
	handler := newWPCoreUpdateHandler(nil, "")
	if handler == nil || handler.Service != nil {
		t.Fatalf("failed construction left a typed nil service: %#v", handler)
	}
}

func TestWPPluginUpdateRoutesRegisteredOnProtectedGroup(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`protected.GET("/api/websites/:id/wp-plugin-update/preview", wpPluginUpdateHandler.Preview)`,
		`protected.POST("/api/websites/:id/wp-plugin-update/confirm", wpPluginUpdateHandler.Confirm)`,
		`protected.GET("/api/websites/:id/wp-plugin-update/tasks/latest", wpPluginUpdateHandler.LatestTask)`,
		`protected.GET("/api/websites/:id/wp-plugin-update/tasks/:task_id", wpPluginUpdateHandler.Task)`,
	} {
		if !bytes.Contains(source, []byte(route)) {
			t.Fatalf("missing protected route %s", route)
		}
	}
}

func TestWPPluginUpdateHandlerKeepsNilInterfaceWhenConstructionFails(t *testing.T) {
	handler := newWPPluginUpdateHandler(nil, "")
	if handler == nil || handler.Service != nil {
		t.Fatalf("failed construction left a typed nil service: %#v", handler)
	}
}

func TestWPFleetOverviewRouteRegistered(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	route := []byte(`protected.GET("/api/wp-fleet/overview", wpFleetOverviewHandler.Overview)`)
	if !bytes.Contains(source, route) {
		t.Fatalf("router.go missing protected fleet overview route %s", route)
	}
}

func TestWPFleetOverviewPanelIsIsolatedAndWired(t *testing.T) {
	websites, err := os.ReadFile("../templates/websites.html")
	if err != nil {
		t.Fatal(err)
	}
	panel, err := os.ReadFile("../templates/wp_fleet_overview.html")
	if err != nil {
		t.Fatal(err)
	}
	overviewPage, err := os.ReadFile("../templates/wordpress_overview.html")
	if err != nil {
		t.Fatal(err)
	}
	call := []byte(`{{template "wp_fleet_overview" .}}`)
	if count := bytes.Count(websites, call); count != 0 {
		t.Fatalf("websites fleet overview template calls = %d, want 0", count)
	}
	if count := bytes.Count(overviewPage, call); count != 1 {
		t.Fatalf("WordPress overview fleet template calls = %d, want 1", count)
	}
	for _, required := range [][]byte{
		[]byte(`api('/websites')`),
		[]byte(`websites: []`),
		[]byte(`fetchList()`),
	} {
		if !bytes.Contains(websites, required) {
			t.Fatalf("websites template is missing restored list behavior %q", required)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte(`function wpFleetOverview()`),
		[]byte(`/wp-fleet/overview`),
		[]byte(`healthFilter`),
		[]byte(`inventoryFilter`),
	} {
		if bytes.Contains(websites, forbidden) {
			t.Fatalf("websites template contains fleet implementation %q", forbidden)
		}
	}
	for _, required := range [][]byte{
		[]byte(`{{define "wp_fleet_overview"}}`),
		[]byte(`function wpFleetOverview()`),
		[]byte(`x-data="wpFleetOverview()"`),
		[]byte(`wordpressOnlyOverview(response.data)`),
		[]byte(`site.site_type === 'wordpress'`),
		[]byte(`overview.sites.length > 10`),
	} {
		if !bytes.Contains(panel, required) {
			t.Fatalf("fleet overview panel is missing %q", required)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte(`api('/websites/'`),
		[]byte(`function toggleStatus(`),
		[]byte(`function reinstallWP(`),
		[]byte(`function deleteSite(`),
		[]byte(`{{template "base" .}}`),
		[]byte(`siteTypeFilter`),
		[]byte(`filter_php`),
		[]byte(`@click="toggleStatus(site)"`),
		[]byte(`@click="reinstallWP(site)"`),
		[]byte(`@click="deleteSite(site)"`),
	} {
		if bytes.Contains(panel, forbidden) {
			t.Fatalf("fleet overview panel contains duplicated write behavior %q", forbidden)
		}
	}
	rendered := renderPage(t, "wordpress_overview.html", "wordpress_overview_content")
	if !bytes.Contains(rendered, []byte(`function wpFleetOverview()`)) {
		t.Fatal("rendered WordPress overview page is missing the fleet overview component")
	}
}

func TestWPFleetOverviewPanelAPIContract(t *testing.T) {
	panel, err := os.ReadFile("../templates/wp_fleet_overview.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{
		[]byte(`api('/wp-fleet/overview', { signal: controller.signal, suppressToast: true })`),
		[]byte(`new TextEncoder().encode(query).length > 128`),
		[]byte(`toLocaleDateString(currentLocale())`),
		[]byte(`toLocaleString(currentLocale())`),
	} {
		if !bytes.Contains(panel, required) {
			t.Fatalf("fleet overview panel is missing API contract %q", required)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte(`setInterval(`),
		[]byte(`setTimeout(`),
		[]byte(`/wp-inventory`),
		[]byte(`method: 'POST'`),
		[]byte(`toLocaleDateString('zh-CN')`),
		[]byte(`toLocaleString('zh-CN')`),
		[]byte(`api('/websites`),
	} {
		if bytes.Contains(panel, forbidden) {
			t.Fatalf("fleet overview panel contains forbidden API behavior %q", forbidden)
		}
	}
}

func TestWPFleetOverviewPanelBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}
	script := wpFleetOverviewPanelScript(t)
	harness := []byte(`
function assert(condition, message) {
    if (!condition) throw new Error(message);
}
global.t = (key, params = {}) => key + (params.count === undefined ? '' : ':' + params.count);
global.currentLocale = () => 'en-US';

const inventory = (status, successful, updates, stale = false) => ({
    status,
    has_successful_inventory: successful,
    wordpress_version: successful ? '7.0' : '',
    plugin_updates: updates,
    theme_updates: 0,
    core_upgrade_available: false,
    update_total: updates,
    last_attempt_at: '2026-07-21T00:00:00Z',
    last_success_at: successful ? '2026-07-21T00:00:00Z' : null,
    stale,
});
const site = (id, domain, siteType, status, health, siteInventory, createdAt) => ({
    id,
    name: domain.split('.')[0],
    domain,
    site_type: siteType,
    status,
    created_at: createdAt || '2026-07-21T00:00:00Z',
    expires_at: null,
    ssl_enabled: false,
    ssl_state: 'disabled',
    monitoring_enabled: false,
    backup_enabled: false,
    file_lock_enabled: false,
    fastcgi_cache_enabled: false,
    access_log_mode: 'off',
    health: { level: health, issues: [] },
    inventory: siteInventory,
});
const sites = [
    site(1, 'alpha.example', 'wordpress', 'active', 'critical', inventory('complete', true, 1), '2026-07-20T00:00:00Z'),
    site(2, 'beta.example', 'wordpress', 'paused', 'warning', inventory('failed', true, 2, true), '2026-07-19T00:00:00Z'),
    site(3, 'gamma.example', 'wordpress', 'creating', 'unknown', inventory('unknown', false, 0), '2026-07-18T00:00:00Z'),
    site(4, 'delta.example', 'php', 'active', 'healthy', null, '2026-07-17T00:00:00Z'),
    site(5, 'epsilon.example', 'wordpress', 'deleting', 'healthy', inventory('complete', true, 0), '2026-07-21T00:00:00Z'),
];
const counts = {
    total_sites: 5,
    wordpress_sites: 4,
    critical_sites: 1,
    warning_sites: 1,
    unknown_sites: 1,
    healthy_sites: 2,
    update_sites: 2,
    failed_inventory_sites: 1,
    stale_inventory_sites: 1,
    inventory_attention_sites: 1,
    uncollected_sites: 1,
};
const data = (nextSites = sites, nextCounts = counts) => ({ generated_at: '2026-07-21T00:00:00Z', counts: nextCounts, sites: nextSites });

(async () => {
    const panel = wpFleetOverview();
    panel.overview = panel.wordpressOnlyOverview(data());

    assert(panel.filteredSites().map(item => item.id).join(',') === '1,2,3,5', 'PHP sites automatically excluded');
    assert(panel.overview.counts.total_sites === 4 && panel.overview.counts.healthy_sites === 1, 'WordPress-only counts');
    panel.healthFilter = 'warning';
    panel.updateFilter = 'has_updates';
    panel.search = ' beta ';
    assert(panel.filteredSites().map(item => item.id).join(',') === '2', 'combined filtering');

    panel.healthFilter = 'all';
    panel.updateFilter = 'all';
    panel.search = 'a'.repeat(128);
    panel.filteredSites();
    assert(panel.searchError === '', '128 byte search accepted');
    panel.search = '测'.repeat(43);
    assert(panel.filteredSites().length === 0 && panel.searchError === 'wp_fleet.search_too_long', '129 byte search rejected');

    panel.search = '';
    assert(panel.filteredSites().map(item => item.id).join(',') === '1,2,3,5', 'attention sorting remains fixed');

    assert(['active', 'paused', 'error', 'creating', 'deleting'].map(value => panel.statusKey(value)).join(',') === [
        'wp_fleet.status_active', 'wp_fleet.status_paused', 'wp_fleet.status_error', 'wp_fleet.status_creating', 'wp_fleet.status_deleting'
    ].join(','), 'five website statuses');

    let usedLocale = '';
    const originalDate = Date.prototype.toLocaleDateString;
    Date.prototype.toLocaleDateString = function(locale) { usedLocale = locale; return 'date'; };
    assert(panel.displayDate('2026-07-21T00:00:00Z') === 'date' && usedLocale === 'en-US', 'current locale date');
    Date.prototype.toLocaleDateString = originalDate;

    let resolveFirst;
    let resolveSecond;
    let calls = 0;
    global.api = () => new Promise(resolve => {
        calls++;
        if (calls === 1) resolveFirst = resolve;
        else resolveSecond = resolve;
    });
    const first = panel.reloadOverview();
    const second = panel.reloadOverview();
    const secondData = data([sites[4]], { ...counts, total_sites: 1, critical_sites: 0, warning_sites: 0, unknown_sites: 0, healthy_sites: 1 });
    resolveSecond({ data: secondData });
    await second;
    resolveFirst({ data: data([sites[0]]) });
    await first;
    assert(panel.overview.sites[0].id === 5, 'old response discarded');
    assert(panel.overview.counts.total_sites === 1, 'counts use API response');

    const previous = panel.overview;
    global.api = async () => { throw new Error('reload failed'); };
    await panel.reloadOverview();
    assert(panel.overview === previous && panel.staleOverview && panel.loadError === 'reload failed', 'reload failure preserves old overview');

    const initialFailure = wpFleetOverview();
    global.api = async () => { throw new Error('initial failed'); };
    await initialFailure.reloadOverview();
    assert(initialFailure.overview === null && !initialFailure.staleOverview && initialFailure.loadError === 'initial failed', 'initial failure state');

    const empty = wpFleetOverview();
    global.api = async () => ({ data: data([], { ...counts, total_sites: 0, critical_sites: 0, warning_sites: 0, unknown_sites: 0, healthy_sites: 0 }) });
    await empty.reloadOverview();
    assert(Array.isArray(empty.overview.sites) && empty.overview.sites.length === 0, 'empty state');

    const many = [];
    for (let index = 0; index < 300; index++) many.push(site(index + 10, 'site-' + index + '.example', 'wordpress', 'active', index % 2 ? 'healthy' : 'warning', inventory('complete', true, index % 3), '2026-07-21T00:00:00Z'));
    panel.overview = data(many, counts);
    panel.search = 'site';
    const started = performance.now();
    assert(panel.filteredSites().length === 300, '300 site filtering');
    const filterElapsed = performance.now() - started;
    assert(filterElapsed < 50, '300 site filtering budget');
    console.log('fleet-filter-300-ms=' + filterElapsed.toFixed(3));
})().catch(error => {
    console.error(error);
    process.exit(1);
});
`)
	testScript := append(append([]byte{}, script...), harness...)
	scriptPath := filepath.Join(t.TempDir(), "wp-fleet-overview-behavior.js")
	if err := os.WriteFile(scriptPath, testScript, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, scriptPath).CombinedOutput()
	if err != nil {
		t.Fatalf("fleet overview behavior failed: %v\n%s", err, output)
	}
	if output = bytes.TrimSpace(output); len(output) > 0 {
		t.Log(string(output))
	}
}

func TestWPFleetEnglishPlaceholders(t *testing.T) {
	content, err := os.ReadFile("../i18n/locales/en-US.json")
	if err != nil {
		t.Fatal(err)
	}
	var locale struct {
		WPFleet map[string]string `json:"wp_fleet"`
	}
	if err := json.Unmarshal(content, &locale); err != nil {
		t.Fatal(err)
	}
	if len(locale.WPFleet) == 0 {
		t.Fatal("en-US is missing wp_fleet messages")
	}
	for key, value := range locale.WPFleet {
		want := "EN_TODO: wp_fleet." + key
		if value != want {
			t.Errorf("wp_fleet.%s = %q, want %q", key, value, want)
		}
	}
}

func wpFleetOverviewPanelScript(t *testing.T) []byte {
	t.Helper()
	rendered := renderPage(t, "wordpress_overview.html", "wordpress_overview_content")
	scriptPattern := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	for _, match := range scriptPattern.FindAllSubmatch(rendered, -1) {
		if bytes.Contains(match[1], []byte(`function wpFleetOverview()`)) {
			return match[1]
		}
	}
	t.Fatal("rendered WordPress overview page is missing the fleet overview script")
	return nil
}

func TestWPInventoryPanelIsIsolatedAndWired(t *testing.T) {
	detail, err := os.ReadFile("../templates/website_detail.html")
	if err != nil {
		t.Fatal(err)
	}
	panel, err := os.ReadFile("../templates/wp_inventory_panel.html")
	if err != nil {
		t.Fatal(err)
	}
	wordpressDetail, err := os.ReadFile("../templates/wordpress_site_detail.html")
	if err != nil {
		t.Fatal(err)
	}
	call := []byte(`{{template "wp_inventory_panel" .}}`)
	if count := bytes.Count(detail, call); count != 0 {
		t.Fatalf("website management inventory template calls = %d, want 0", count)
	}
	if count := bytes.Count(wordpressDetail, call); count != 1 {
		t.Fatalf("WordPress detail inventory template calls = %d, want 1", count)
	}
	for _, forbidden := range [][]byte{
		[]byte(`function wpInventoryPanel()`),
		[]byte(`/wp-inventory`),
		[]byte(`pollTimer`),
	} {
		if bytes.Contains(detail, forbidden) {
			t.Fatalf("website detail contains inventory implementation %q", forbidden)
		}
	}
	for _, required := range [][]byte{
		[]byte(`{{define "wp_inventory_panel"}}`),
		[]byte(`function wpInventoryPanel()`),
		[]byte(`x-effect="setSite(site)"`),
		[]byte(`x-show="site && site.site_type === 'wordpress'"`),
	} {
		if !bytes.Contains(panel, required) {
			t.Fatalf("inventory panel is missing %q", required)
		}
	}
	if bytes.Contains(panel, []byte(`{{template "base" .}}`)) {
		t.Fatal("inventory panel must not render the base template")
	}
	rendered := renderPage(t, "wordpress_site_detail.html", "wordpress_site_detail_content")
	if !bytes.Contains(rendered, []byte(`function wpInventoryPanel()`)) {
		t.Fatal("rendered WordPress detail is missing the inventory component")
	}
	if !bytes.Contains(rendered, []byte(`function wordpressSiteDetail()`)) {
		t.Fatal("rendered WordPress detail is missing its site loader")
	}
}

func TestWPCoreUpdatePanelIsWiredAndUsesFixedAPIContract(t *testing.T) {
	panel, err := os.ReadFile("../templates/wp_core_update_panel.html")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := os.ReadFile("../templates/wordpress_site_detail.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{
		[]byte(`{{template "wp_core_update_panel" .}}`),
		[]byte(`api('/websites/' + siteID + '/wp-core-update/preview'`),
		[]byte(`api('/websites/' + siteID + '/wp-core-update/confirm'`),
		[]byte(`api('/websites/' + siteID + '/wp-core-update/tasks/latest'`),
		[]byte(`'/wp-core-update/tasks/' + encodeURIComponent(taskID)`),
		[]byte(`confirmation_token: preview.confirmation_token`),
		[]byte(`target_version: preview.target_version`),
		[]byte(`confirm: true`),
		[]byte(`confirmModal(`),
	} {
		source := panel
		if bytes.HasPrefix(required, []byte(`{{template`)) {
			source = detail
		}
		if !bytes.Contains(source, required) {
			t.Fatalf("core update UI is missing %q", required)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte(`download_url`),
		[]byte(`package_snapshot_path`),
		[]byte(`downloaded_sha256`),
		[]byte(`sessionStorage`),
		[]byte(`setInterval(`),
	} {
		if bytes.Contains(panel, forbidden) {
			t.Fatalf("core update UI contains forbidden behavior %q", forbidden)
		}
	}
	rendered := renderPage(t, "wordpress_site_detail.html", "wordpress_site_detail_content")
	if !bytes.Contains(rendered, []byte(`function wpCoreUpdatePanel()`)) {
		t.Fatal("rendered WordPress detail is missing the core update component")
	}
}

func TestWPCoreUpdatePanelBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}
	rendered := renderPage(t, "wordpress_site_detail.html", "wordpress_site_detail_content")
	scriptPattern := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	var script []byte
	for _, match := range scriptPattern.FindAllSubmatch(rendered, -1) {
		if bytes.Contains(match[1], []byte(`function wpCoreUpdatePanel()`)) {
			script = match[1]
			break
		}
	}
	if len(script) == 0 {
		t.Fatal("core update panel script not found")
	}
	harness := []byte(`
function assert(condition, message) { if (!condition) throw new Error(message); }
global.t = (key, params = {}) => key;
global.window = {};
(async () => {
    const panel = wpCoreUpdatePanel();
    panel.siteID = 7;
    assert(panel.validPreview({
        available: true, site_id: 7, current_version: '7.0.1', target_version: '7.0.2',
        confirmation_token: 'opaque', verification_required: 'official_verified',
        database_backup: true, core_files_backup: true, uploads_included: false
    }), 'valid preview rejected');
    assert(!panel.validPreview({ available: true, site_id: 8 }), 'cross-site preview accepted');
    assert(panel.validTask({ task_id: 'wpu_test', site_id: 7, component_type: 'core', task_kind: 'update', status: 'queued' }), 'valid task rejected');
    assert(!panel.validTask({ task_id: 'wpu_test', site_id: 8, component_type: 'core', task_kind: 'update', status: 'queued' }), 'cross-site task accepted');
    panel.task = { status: 'interrupted_unknown', stage: 'health_check' };
    assert(panel.taskStatusText() === 'wp_core_update.status_interrupted_unknown', 'interrupted status mapping');
    assert(panel.stageText() === 'wp_core_update.stage_health_check', 'health-check stage mapping');

    panel.task = { task_id: 'stale', site_id: 7, component_type: 'core', task_kind: 'update', status: 'queued', stage: 'queued' };
    let scheduled = 0;
    panel.schedulePoll = () => { scheduled++; };
    global.api = async () => { const error = new Error('not found'); error.status = 404; throw error; };
    await panel.pollTask();
    assert(panel.task === null, '404 task was not cleared');
    assert(scheduled === 0, '404 task scheduled another poll');

    panel.task = { task_id: 'done', site_id: 7, component_type: 'core', task_kind: 'update', status: 'queued', stage: 'queued' };
    global.api = async () => ({ data: { task_id: 'done', site_id: 7, component_type: 'core', task_kind: 'update', status: 'success', stage: 'complete' } });
    await panel.pollTask();
    assert(panel.task.status === 'success', 'terminal task not retained for display');

    scheduled = 0;
    panel.task = null;
    const recent = new Date().toISOString();
    global.api = async () => ({ data: {
        task_id: 'latest', site_id: 7, component_type: 'core', task_kind: 'update', status: 'running', stage: 'updating_core',
        current_version: '7.0.1', target_version: '7.0.2', requested_at: recent
    } });
    const recovered = await panel.loadLatestTask({ preview: { current_version: '7.0.1', target_version: '7.0.2' }, since: Date.now() });
    assert(recovered === true && panel.task.task_id === 'latest', 'recent matching task was not recovered');
    assert(scheduled === 1, 'active recovered task was not scheduled');

    panel.stopPolling();
    panel.task = null;
    global.api = async () => ({ data: {
        task_id: 'old', site_id: 7, component_type: 'core', task_kind: 'update', status: 'success', stage: 'complete',
        current_version: '7.0.1', target_version: '7.0.2', requested_at: '2020-01-01T00:00:00Z'
    } });
    const stale = await panel.loadLatestTask({ preview: { current_version: '7.0.1', target_version: '7.0.2' }, since: Date.now() });
    assert(stale === false && panel.task === null, 'old matching task was accepted as confirmation recovery');

    global.api = async () => ({ data: {
        task_id: 'previous', site_id: 7, component_type: 'core', task_kind: 'update', status: 'success', stage: 'complete',
        current_version: '7.0.1', target_version: '7.0.2', requested_at: new Date().toISOString()
    } });
    const previous = await panel.loadLatestTask({ preview: { current_version: '7.0.1', target_version: '7.0.2' }, since: Date.now(), previousTaskID: 'previous' });
    assert(previous === false && panel.task === null, 'pre-existing task was accepted as confirmation recovery');

    for (const mismatch of [
        { id: 'wrong-current', current: '7.0.0', target: '7.0.2' },
        { id: 'wrong-target', current: '7.0.1', target: '7.0.3' },
    ]) {
        global.api = async () => ({ data: {
            task_id: mismatch.id, site_id: 7, component_type: 'core', task_kind: 'update', status: 'running', stage: 'updating_core',
            current_version: mismatch.current, target_version: mismatch.target, requested_at: new Date().toISOString()
        } });
        const versionMismatch = await panel.loadLatestTask({ preview: { current_version: '7.0.1', target_version: '7.0.2' }, since: Date.now() });
        assert(versionMismatch === false && panel.task === null, mismatch.id + ' task was accepted as confirmation recovery');
    }
})().catch(error => { console.error(error); process.exit(1); });
`)
	testScript := append(append([]byte{}, script...), harness...)
	scriptPath := filepath.Join(t.TempDir(), "wp-core-update-panel-behavior.js")
	if err := os.WriteFile(scriptPath, testScript, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("core update panel behavior failed: %v\n%s", err, output)
	}
}

func TestAPIErrorPreservesHTTPStatus(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}
	app, err := os.ReadFile("../static/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	harness := []byte(`
global.window = { WP_PANEL_I18N: { lang: 'zh-CN', messages: {} }, location: {} };
global.document = {
    body: { dataset: { panelPrefix: '/panel' } },
    querySelector: () => ({ content: 'csrf' }),
};
global.fetch = async () => ({
    status: 404,
    ok: false,
    headers: { get: () => 'application/json' },
    json: async () => ({ success: false, message: 'not found' }),
});
api('/missing', { suppressToast: true }).then(
    () => { throw new Error('request unexpectedly succeeded'); },
    error => {
        if (error.status !== 404) throw new Error('HTTP status was not preserved');
    }
).catch(error => { console.error(error); process.exit(1); });
`)
	testScript := append(append([]byte{}, app...), harness...)
	scriptPath := filepath.Join(t.TempDir(), "api-status.js")
	if err := os.WriteFile(scriptPath, testScript, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("API status behavior failed: %v\n%s", err, output)
	}
}

func TestWPInventoryPanelAPIContract(t *testing.T) {
	panel, err := os.ReadFile("../templates/wp_inventory_panel.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][]byte{
		[]byte(`api('/websites/' + siteID + '/wp-inventory'`),
		[]byte(`api('/websites/' + siteID + '/wp-inventory/refresh', { method: 'POST'`),
		[]byte(`'/wp-inventory/tasks/' + encodeURIComponent(taskID)`),
		[]byte(`new URLSearchParams()`),
		[]byte(`params.set('page_size', String(state.pageSize))`),
		[]byte(`currentPage().total > currentPage().pageSize`),
		[]byte(`item.current_version || t('common.none')`),
		[]byte(`!item.network_active && !item.active && !item.current_theme`),
		[]byte(`if (tab === 'plugins') return 'plugin'`),
		[]byte(`if (tab === 'themes') return 'theme'`),
		[]byte(`'/wp-plugin-update/preview?component_key=' + encodeURIComponent(item.key)`),
		[]byte(`api('/websites/' + siteID + '/wp-plugin-update/confirm'`),
		[]byte(`'/wp-plugin-update/tasks/latest?component_key=' + encodeURIComponent(componentKey)`),
		[]byte(`'/wp-plugin-update/tasks/' + encodeURIComponent(taskID)`),
		[]byte(`confirmation_token: preview.confirmation_token`),
		[]byte(`target_version: preview.target_version`),
		[]byte(`confirm: true`),
		[]byte(`setTimeout(() =>`),
	} {
		if !bytes.Contains(panel, required) {
			t.Fatalf("inventory panel is missing API contract %q", required)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte(`setInterval(`),
		[]byte(`params.set('response'`),
		[]byte(`params.set('sort'`),
		[]byte(`params.set('order'`),
		[]byte(`params.set('column'`),
		[]byte(`params.set('collection_id'`),
		[]byte(`sessionStorage`),
		[]byte(`localStorage`),
		[]byte(`download_url`),
	} {
		if bytes.Contains(panel, forbidden) {
			t.Fatalf("inventory panel contains forbidden API behavior %q", forbidden)
		}
	}
}

func TestWPInventoryPanelBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}
	script := wpInventoryPanelScript(t)
	harness := []byte(`
function assert(condition, message) {
    if (!condition) throw new Error(message);
}
global.t = key => key;
global.fmtTime = value => String(value);
global.clearTimeout = id => { global.clearedTimer = id; };

(async () => {
    const panel = wpInventoryPanel();
    const summary = (status, successful) => ({
        site_id: 1,
        collection_status: status,
        has_successful_inventory: successful,
        wordpress: {},
        counts: {},
        active_task: null,
        last_error: null
    });

    panel.summary = summary('unknown', false);
    assert(panel.statusKey() === 'wp_inventory.status_unknown', 'unknown status');
    panel.summary = summary('complete', true);
    assert(panel.statusKey() === 'wp_inventory.status_complete', 'complete status');
    panel.summary = summary('failed', true);
    assert(panel.statusKey() === 'wp_inventory.status_failed_stale', 'failed stale status');
    panel.summary = summary('failed', false);
    assert(panel.statusKey() === 'wp_inventory.status_failed_empty', 'failed empty status');
    panel.summary.active_task = { id: 'queued', status: 'queued' };
    assert(panel.statusKey() === 'wp_inventory.status_queued', 'queued priority');
    panel.summary.active_task = { id: 'running', status: 'running' };
    assert(panel.statusKey() === 'wp_inventory.status_running', 'running priority');
    assert(panel.pollDelay(0) === 2000, 'initial poll delay');
    assert(panel.pollDelay(29999) === 2000, 'poll delay before 30 seconds');
    assert(panel.pollDelay(30000) === 5000, 'poll delay after 30 seconds');

    panel.pollTimer = 91;
    panel.stopPolling();
    assert(global.clearedTimer === 91 && panel.pollTimer === null, 'timer cleanup');
    assert(panel.errorKey({ code: 'future_error' }) === 'wp_inventory.error_generic', 'unknown error fallback');

    panel.siteID = 1;
    panel.requestGeneration = 7;
    panel.loadSummary = async () => {};
    await panel.setSite({ id: 2, site_type: 'wordpress' });
    assert(panel.siteID === 2, 'site switch');
    assert(panel.requestGeneration === 8, 'site switch invalidates old requests');

    panel.summary = summary('complete', true);
    panel.summary.site_id = 2;
    panel.summary.active_task = { id: 'task-id', site_id: 2, status: 'running' };
    panel.pages.plugins.page = 3;
    panel.pages.themes.page = 4;
    panel.pages.updates.page = 5;
    global.api = async () => ({ data: { id: 'task-id', site_id: 2, status: 'succeeded' } });
    await panel.pollTask('task-id');
    assert(panel.pages.plugins.page === 1, 'terminal task resets plugin page');
    assert(panel.pages.themes.page === 1, 'terminal task resets theme page');
    assert(panel.pages.updates.page === 1, 'terminal task resets update page');

    const candidate = { type: 'plugin', key: 'classic-editor/classic-editor.php', current_version: '1.6', target_version: '1.7' };
    assert(panel.canUpdatePlugin(candidate), 'official directory plugin candidate rejected');
    assert(!panel.canUpdatePlugin({ ...candidate, type: 'theme' }), 'theme candidate accepted by plugin updater');
    assert(!panel.canUpdatePlugin({ ...candidate, key: 'hello.php' }), 'unsupported single-file plugin accepted');

    panel.pluginUpdate.componentKey = candidate.key;
    assert(panel.validPluginPreview({
        available: true, site_id: 2, component_key: candidate.key, current_version: '1.6', target_version: '1.7',
        confirmation_token: 'opaque', package_source: 'wordpress.org', verification_required: 'structure_only',
        database_backup: true, plugin_files_backup: true
    }), 'valid plugin preview rejected');
    assert(!panel.validPluginPreview({
        available: true, site_id: 1, component_key: candidate.key, current_version: '1.6', target_version: '1.7',
        confirmation_token: 'opaque', package_source: 'wordpress.org', verification_required: 'structure_only',
        database_backup: true, plugin_files_backup: true
    }), 'cross-site plugin preview accepted');

    panel.pluginUpdate.task = {
        task_id: 'plugin-stale', site_id: 2, component_type: 'plugin', component_key: candidate.key,
        task_kind: 'update', status: 'queued', stage: 'queued'
    };
    let pluginScheduled = 0;
    panel.schedulePluginPoll = () => { pluginScheduled++; };
    global.api = async () => { const error = new Error('not found'); error.status = 404; throw error; };
    await panel.pollPluginTask();
    assert(panel.pluginUpdate.task === null, '404 plugin task was not cleared');
    assert(pluginScheduled === 0, '404 plugin task scheduled another poll');

    panel.pluginUpdate.task = {
        task_id: 'plugin-done', site_id: 2, component_type: 'plugin', component_key: candidate.key,
        task_kind: 'update', status: 'queued', stage: 'queued'
    };
    global.api = async () => ({ data: {
        task_id: 'plugin-done', site_id: 2, component_type: 'plugin', component_key: candidate.key,
        task_kind: 'update', status: 'success', stage: 'complete'
    } });
    await panel.pollPluginTask();
    assert(panel.pluginUpdate.task.status === 'success', 'terminal plugin task not retained');

    panel.pluginUpdate.task = null;
    global.api = async () => ({ data: {
        task_id: 'plugin-latest', site_id: 2, component_type: 'plugin', component_key: candidate.key,
        task_kind: 'update', status: 'running', stage: 'updating_component',
        current_version: '1.6', target_version: '1.7', requested_at: new Date().toISOString()
    } });
    const recovered = await panel.loadLatestPluginTask(candidate.key, {
        componentKey: candidate.key, currentVersion: '1.6', targetVersion: '1.7', since: Date.now(), previousTaskID: ''
    });
    assert(recovered === true && panel.pluginUpdate.task.task_id === 'plugin-latest', 'recent plugin task was not recovered');
    assert(pluginScheduled === 1, 'active recovered plugin task was not scheduled');
})().catch(error => {
    console.error(error);
    process.exit(1);
});
`)
	testScript := append(append([]byte{}, script...), harness...)
	scriptPath := filepath.Join(t.TempDir(), "wp-inventory-panel-behavior.js")
	if err := os.WriteFile(scriptPath, testScript, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("inventory panel behavior failed: %v\n%s", err, output)
	}
}

func TestWPInventoryEnglishPlaceholders(t *testing.T) {
	content, err := os.ReadFile("../i18n/locales/en-US.json")
	if err != nil {
		t.Fatal(err)
	}
	var locale struct {
		WPInventory map[string]string `json:"wp_inventory"`
	}
	if err := json.Unmarshal(content, &locale); err != nil {
		t.Fatal(err)
	}
	messages := locale.WPInventory
	if len(messages) == 0 {
		t.Fatal("en-US is missing wp_inventory messages")
	}
	for key, value := range messages {
		want := "EN_TODO: wp_inventory." + key
		if value != want {
			t.Errorf("wp_inventory.%s = %q, want %q", key, value, want)
		}
	}
}

func wpInventoryPanelScript(t *testing.T) []byte {
	t.Helper()
	rendered := renderPage(t, "wordpress_site_detail.html", "wordpress_site_detail_content")
	scriptPattern := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	for _, match := range scriptPattern.FindAllSubmatch(rendered, -1) {
		if bytes.Contains(match[1], []byte(`function wpInventoryPanel()`)) {
			return match[1]
		}
	}
	t.Fatal("rendered WordPress detail is missing the inventory panel script")
	return nil
}

func TestDatabaseManagementRoutesRegistered(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`protected.GET("/api/databases", databaseManagerHandler.List)`,
		`protected.GET("/databases", func(c *gin.Context)`,
		`protected.GET("/databases/:id", func(c *gin.Context)`,
	} {
		if !bytes.Contains(source, []byte(route)) {
			t.Fatalf("router.go missing route %s", route)
		}
	}
}

func TestWebsiteDetailCardOrderAndDatabaseNavigation(t *testing.T) {
	source, err := os.ReadFile("../templates/website_detail.html")
	if err != nil {
		t.Fatal(err)
	}
	monitoring := bytes.Index(source, []byte(`website.site_monitoring`))
	ssl := bytes.Index(source, []byte(`website.ssl_management`))
	nginx := bytes.Index(source, []byte(`website.nginx_custom_config`))
	if monitoring < 0 || ssl < 0 || nginx < 0 || !(monitoring < ssl && ssl < nginx) {
		t.Fatalf("runtime configuration order = monitoring:%d ssl:%d nginx:%d", monitoring, ssl, nginx)
	}
	if !bytes.Contains(source, []byte(`'/databases/' + site.id`)) {
		t.Fatal("website detail is missing the direct database management link")
	}
	if !bytes.Contains(source, []byte(`site.site_type === 'wordpress' ? '' : 'md:col-span-2'`)) {
		t.Fatal("non-WordPress optimization card does not span the full desktop row")
	}
}

func TestSidebarNavigationOrder(t *testing.T) {
	source, err := os.ReadFile("../templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	items := [][]byte{
		[]byte(`href="/{{$.RandomSuffix}}/"`),
		[]byte(`href="/{{$.RandomSuffix}}/websites"`),
		[]byte(`href="/{{$.RandomSuffix}}/files"`),
		[]byte(`href="/{{$.RandomSuffix}}/databases"`),
		[]byte(`href="/{{$.RandomSuffix}}/backups"`),
		[]byte(`href="/{{$.RandomSuffix}}/cron"`),
		[]byte(`href="/{{$.RandomSuffix}}/firewall"`),
		[]byte(`href="/{{$.RandomSuffix}}/security"`),
		[]byte(`href="/{{$.RandomSuffix}}/software"`),
		[]byte(`href="/{{$.RandomSuffix}}/alert"`),
		[]byte(`href="/{{$.RandomSuffix}}/ai-diagnostics"`),
		[]byte(`href="/{{$.RandomSuffix}}/extensions"`),
		[]byte(`href="/{{$.RandomSuffix}}/settings"`),
		[]byte(`href="/{{$.RandomSuffix}}/help"`),
	}
	previous := -1
	for _, item := range items {
		position := bytes.Index(source, item)
		if position < 0 || position <= previous {
			t.Fatalf("sidebar item %s is missing or out of order", item)
		}
		previous = position
	}
}

func TestDatabaseDetailShowsFiveRecentBackupsByDefault(t *testing.T) {
	source, err := os.ReadFile("../templates/database_detail.html")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte(`this.backups.slice(0, 5)`)) {
		t.Fatal("database detail does not limit the default backup list to five entries")
	}
}

func TestFileManagerStartsWithDirectoryList(t *testing.T) {
	source, err := os.ReadFile("../templates/files.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range [][]byte{
		[]byte(`x-model.trim="siteSearch"`),
		[]byte(`@click="openRoot(site.id)"`),
		[]byte(`@click="openRoot(0)"`),
		[]byte(`@click="showRootList()"`),
		[]byte(`url.searchParams.set('site_id', this.selectedSite)`),
		[]byte(`calculateDirectorySize(siteID, path)`),
		[]byte(`'/files/size?site_id='`),
		[]byte(`{ timeout: 65000, suppressToast: true }`),
		[]byte(`deepLinkFailed: false`),
		[]byte(`this.deepLinkFailed = true`),
	} {
		if !bytes.Contains(source, expected) {
			t.Fatalf("file manager is missing directory-list behavior %s", expected)
		}
	}
	if bytes.Contains(source, []byte(`x-model="selectedSite"`)) {
		t.Fatal("file manager still requires the website dropdown")
	}
	if bytes.Contains(source, []byte(`:disabled="directorySizeState`)) {
		t.Fatal("directory size buttons must remain recoverable while a request is in progress")
	}
	if bytes.Contains(source, []byte(`x-show="!siteSearch"`)) {
		t.Fatal("file manager hides the backup directory while filtering websites")
	}
}

func TestDirectorySizeRouteRegistered(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte(`protected.GET("/api/files/size", fileHandler.DirectorySize)`)) {
		t.Fatal("directory size route is not registered")
	}
}

func TestPageTitleKeysExist(t *testing.T) {
	for active, key := range pageTitleKeys {
		t.Run(active, func(t *testing.T) {
			if got := i18n.T(i18n.DefaultLang, key); got == key {
				t.Fatalf("missing zh-CN page title key %q", key)
			}
			if got := i18n.T(i18n.English, key); got == key {
				t.Fatalf("missing en-US page title key %q", key)
			}
		})
	}
}

func renderPage(t *testing.T, page, content string) []byte {
	t.Helper()
	data := testPageData(content)
	var output bytes.Buffer
	if err := parseTemplates(t).ExecuteTemplate(&output, page, data); err != nil {
		t.Fatalf("render %s: %v", page, err)
	}
	return output.Bytes()
}

func testPageData(content string) map[string]any {
	return map[string]any{
		"Title":           "Test",
		"PanelTitle":      "WP Panel",
		"PanelVersion":    "test",
		"AssetVersion":    "test",
		"ContentTemplate": content,
		"RandomSuffix":    "test",
		"Active":          "dashboard",
		"AssetPrefix":     "/test/assets",
		"CSRFToken":       "test",
		"Lang":            i18n.DefaultLang,
		"MessagesJSON":    i18n.MessagesJSON(i18n.DefaultLang, i18nKeys),
	}
}

func parseTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("").Funcs(i18n.FuncMap()).ParseFS(os.DirFS(".."), "templates/*.html"))
}
