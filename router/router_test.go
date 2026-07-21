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
	"login.html":           "",
	"dashboard.html":       "dashboard_content",
	"websites.html":        "websites_content",
	"website_new.html":     "websites_new_content",
	"website_detail.html":  "websites_detail_content",
	"databases.html":       "databases_content",
	"database_detail.html": "database_detail_content",
	"ai_diagnostics.html":  "ai_diagnostics_content",
	"cron.html":            "cron_content",
	"backups.html":         "backups_content",
	"firewall.html":        "firewall_content",
	"files.html":           "files_content",
	"security.html":        "security_content",
	"settings.html":        "settings_content",
	"alert.html":           "alert_content",
	"extension.html":       "extensions_content",
	"software.html":        "software_content",
	"help.html":            "help_content",
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
		"dashboard_content", "websites_content", "websites_new_content",
		"websites_detail_content", "databases_content", "database_detail_content", "ai_diagnostics_content", "cron_content", "backups_content", "firewall_content",
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

func TestWPInventoryPanelIsIsolatedAndWired(t *testing.T) {
	detail, err := os.ReadFile("../templates/website_detail.html")
	if err != nil {
		t.Fatal(err)
	}
	panel, err := os.ReadFile("../templates/wp_inventory_panel.html")
	if err != nil {
		t.Fatal(err)
	}
	call := []byte(`{{template "wp_inventory_panel" .}}`)
	if count := bytes.Count(detail, call); count != 1 {
		t.Fatalf("website detail inventory template calls = %d, want 1", count)
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
	rendered := renderPage(t, "website_detail.html", "websites_detail_content")
	if !bytes.Contains(rendered, []byte(`function wpInventoryPanel()`)) {
		t.Fatal("rendered website detail is missing the inventory component")
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
		[]byte(`if (tab === 'plugins') return 'plugin'`),
		[]byte(`if (tab === 'themes') return 'theme'`),
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
	rendered := renderPage(t, "website_detail.html", "websites_detail_content")
	scriptPattern := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	for _, match := range scriptPattern.FindAllSubmatch(rendered, -1) {
		if bytes.Contains(match[1], []byte(`function wpInventoryPanel()`)) {
			return match[1]
		}
	}
	t.Fatal("rendered website detail is missing the inventory panel script")
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
