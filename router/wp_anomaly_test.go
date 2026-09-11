package router

import (
	"bytes"
	"strings"
	"testing"
)

func TestWPAnomalyPreservesLegacySecurityAlerts(t *testing.T) {
	code := string(renderPage(t, "alert.html", "alert_content"))
	if strings.Contains(code, "wp-security-settings-title") {
		t.Fatal("new security section must be removed; preserve v1.5.14 layout")
	}
	for _, control := range []string{"rules.alert_wp_fake_search_bot", "wpSecCfg.threshold", "wpSecCfg.windowHours"} {
		if !strings.Contains(code, control) {
			t.Fatalf("legacy security control removed: %s", control)
		}
	}
	if strings.Contains(code, `x-model="rules.alert_wp_sqli_probe"`) {
		t.Fatal("SQL switch must move to firewall")
	}
	for _, alertType := range []string{"alert_wp_content_change", "alert_wp_content_volume", "alert_wp_setting_change"} {
		if !strings.Contains(code, alertType) {
			t.Fatalf("new anomaly alert label missing: %s", alertType)
		}
	}
	firewall := string(renderPage(t, "firewall.html", "firewall_content"))
	if !strings.Contains(firewall, `x-data="sqliSettings()"`) || !strings.Contains(firewall, "alert_wp_sqli_probe:String(this.enabled)") {
		t.Fatal("firewall SQL settings missing")
	}
}

func TestWPAnomalyPanelPlacementAndScript(t *testing.T) {
	html := renderPage(t, "website_detail.html", "websites_detail_content")
	if !bytes.Contains(html, []byte(`x-data="wpAnomalyMonitor(site.id)"`)) {
		t.Fatal("monitor panel missing")
	}
	if bytes.Contains(html, []byte(`href="/alert"`)) {
		t.Fatal("alert link bypasses random prefix")
	}
	code := string(html)
	if !strings.Contains(code, `x-show="site.site_type === 'wordpress'" id="companion-plugin-controls"`) {
		t.Fatal("companion controls must not depend on optimization toggles")
	}
	for _, marker := range []string{"pluginStatus === 'inactive'", "pluginStatus === 'unknown'", "pluginStatusLoading", "fetchPluginStatus()"} {
		if !strings.Contains(code, marker) {
			t.Fatalf("missing plugin state UI: %s", marker)
		}
	}
	if strings.Index(code, `x-data="wpAnomalyMonitor(site.id)"`) < strings.Index(code, `@click="saveWPOptimizations()"`) {
		t.Fatal("monitor must follow original optimization controls")
	}
	if !strings.Contains(code, "<hr class=\"my-5 border-gray-700\">") {
		t.Fatal("shared section divider missing")
	}
	endTemplate := strings.LastIndex(code, "</template>")
	script := strings.Index(code, "function wpAnomalyMonitor(siteID)")
	if script < endTemplate {
		t.Fatal("monitor script is inside inert template")
	}
}
