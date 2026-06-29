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
)

var pageTemplates = map[string]string{
	"login.html":          "",
	"dashboard.html":      "dashboard_content",
	"websites.html":       "websites_content",
	"website_new.html":    "websites_new_content",
	"website_detail.html": "websites_detail_content",
	"ai_diagnostics.html": "ai_diagnostics_content",
	"cron.html":           "cron_content",
	"firewall.html":       "firewall_content",
	"files.html":          "files_content",
	"security.html":       "security_content",
	"settings.html":       "settings_content",
	"alert.html":          "alert_content",
	"extension.html":      "extensions_content",
	"software.html":       "software_content",
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
		"websites_detail_content", "ai_diagnostics_content", "cron_content", "firewall_content",
		"files_content", "security_content", "settings_content",
		"alert_content", "extensions_content", "software_content",
	}
	for _, content := range contents {
		t.Run(content, func(t *testing.T) {
			tmpl := parseTemplates(t)
			var output bytes.Buffer
			if err := tmpl.ExecuteTemplate(&output, content, map[string]any{}); err != nil {
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

func renderPage(t *testing.T, page, content string) []byte {
	t.Helper()
	data := map[string]any{
		"Title":           "Test",
		"PanelTitle":      "WP Panel",
		"PanelVersion":    "test",
		"AssetVersion":    "test",
		"ContentTemplate": content,
		"RandomSuffix":    "test",
		"Active":          "dashboard",
		"AssetPrefix":     "/test/assets",
		"CSRFToken":       "test",
	}
	var output bytes.Buffer
	if err := parseTemplates(t).ExecuteTemplate(&output, page, data); err != nil {
		t.Fatalf("render %s: %v", page, err)
	}
	return output.Bytes()
}

func parseTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("").ParseFS(os.DirFS(".."), "templates/*.html"))
}
