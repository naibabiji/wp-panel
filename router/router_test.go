package router

import (
	"bytes"
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
