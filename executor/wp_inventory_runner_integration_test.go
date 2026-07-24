//go:build linux

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

func TestWPInventoryRunnerE2E(t *testing.T) {
	if os.Getenv("WP_PANEL_RUNNER_E2E") != "1" {
		t.Skip("set WP_PANEL_RUNNER_E2E=1 inside the disposable Docker validation environment")
	}
	siteRoot := os.Getenv("WP_PANEL_RUNNER_SITE_ROOT")
	domain := os.Getenv("WP_PANEL_RUNNER_DOMAIN")
	systemUser := os.Getenv("WP_PANEL_RUNNER_USER")
	wwwRoot := os.Getenv("WP_PANEL_RUNNER_WWW_ROOT")
	if siteRoot == "" || domain == "" || systemUser == "" || wwwRoot == "" {
		t.Fatal("missing E2E site environment")
	}
	repeat := 1
	if raw := os.Getenv("WP_PANEL_RUNNER_REPEAT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			t.Fatalf("invalid WP_PANEL_RUNNER_REPEAT %q", raw)
		}
		repeat = parsed
	}
	expectedCode := WPInventoryErrorCode(os.Getenv("WP_PANEL_RUNNER_EXPECT_CODE"))
	expectedPluginTransient := os.Getenv("WP_PANEL_RUNNER_EXPECT_PLUGIN_TRANSIENT")
	requireMultisite := os.Getenv("WP_PANEL_RUNNER_REQUIRE_MULTISITE") == "1"

	runner, err := NewWPInventoryRunner()
	if err != nil {
		t.Fatalf("construct production runner: %v", err)
	}
	cfg := &config.Config{Paths: config.PathsConfig{WWWRoot: wwwRoot}}
	site := &models.Website{ID: 1, Domain: domain, SystemUser: systemUser, WebRoot: siteRoot, SiteType: "wordpress", Status: models.StatusActive}
	walls := make([]time.Duration, 0, repeat)
	var maxRSS int64
	for i := 0; i < repeat; i++ {
		result, collectErr := runner.Collect(context.Background(), cfg, site, false)
		if expectedCode == "" {
			if collectErr != nil {
				t.Fatalf("collect %d/%d: %v", i+1, repeat, collectErr)
			}
			if result.Inventory.WordPress.Version == "" || result.Inventory.WordPress.Multisite != requireMultisite {
				t.Fatalf("collect %d returned unexpected WordPress inventory: %#v", i+1, result.Inventory.WordPress)
			}
			switch expectedPluginTransient {
			case "present":
				if !result.Inventory.Updates.Plugins.TransientPresent || result.Inventory.Updates.Plugins.LastChecked != 1784500000 || len(result.Inventory.Updates.Plugins.Items) != 0 {
					t.Fatalf("collect %d did not preserve present-empty plugin transient: %#v", i+1, result.Inventory.Updates.Plugins)
				}
			case "absent":
				if result.Inventory.Updates.Plugins.TransientPresent || result.Inventory.Updates.Plugins.LastChecked != 0 || len(result.Inventory.Updates.Plugins.Items) != 0 {
					t.Fatalf("collect %d did not preserve absent plugin transient: %#v", i+1, result.Inventory.Updates.Plugins)
				}
			case "":
			default:
				t.Fatalf("invalid WP_PANEL_RUNNER_EXPECT_PLUGIN_TRANSIENT %q", expectedPluginTransient)
			}
		} else {
			var runErr *WPInventoryRunError
			if !errors.As(collectErr, &runErr) || runErr.Code != expectedCode {
				t.Fatalf("collect %d error = %v, want %s", i+1, collectErr, expectedCode)
			}
		}
		walls = append(walls, result.Meta.WallTime)
		if result.Meta.MaxRSSKiB > maxRSS {
			maxRSS = result.Meta.MaxRSSKiB
		}
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	report := struct {
		Repeat    int     `json:"repeat"`
		Expected  string  `json:"expected"`
		TotalMS   float64 `json:"total_ms"`
		P95MS     float64 `json:"p95_ms"`
		MaxRSSKiB int64   `json:"max_rss_kib"`
	}{Repeat: repeat, Expected: string(expectedCode), MaxRSSKiB: maxRSS}
	for _, wall := range walls {
		report.TotalMS += float64(wall.Microseconds()) / 1000
	}
	report.P95MS = float64(walls[(len(walls)*95-1)/100].Microseconds()) / 1000
	encoded, _ := json.Marshal(report)
	fmt.Printf("WP_PANEL_RUNNER_E2E_REPORT %s\n", encoded)
	if expectedCode == "" && repeat == 100 {
		if report.TotalMS > 120000 || report.P95MS > 1000 || report.MaxRSSKiB > 128*1024 {
			t.Fatalf("production runner performance budget exceeded: %s", encoded)
		}
	}
}
