package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/models"
)

func TestCompanionPluginStatus(t *testing.T) {
	site := &models.Website{SiteType: "wordpress", Status: models.StatusActive, WebRoot: t.TempDir()}
	file := filepath.Join(site.WebRoot, "wp-content/plugins/wp-panel-optimizer/wp-panel-optimizer.php")
	called := false
	result := WPInventoryRunResult{}
	var collectErr error
	collect := func() (WPInventoryRunResult, error) { called = true; return result, collectErr }
	check := func(want string) {
		t.Helper()
		if got := companionPluginStatus(site, collect); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	}
	check("not_installed")
	if called {
		t.Fatal("missing plugin must not boot WordPress")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("<?php"), 0644); err != nil {
		t.Fatal(err)
	}
	check("unknown") // File exists, but no matching plugin in inventory.
	result.Inventory.Plugins = []WPInventoryPlugin{{File: "wp-panel-optimizer/wp-panel-optimizer.php"}}
	check("inactive")
	result.Inventory.Plugins[0].Active = true
	check("installed")
	collectErr = errors.New("runner timed out")
	check("unknown")
	collectErr = nil
	result.Inventory.WordPress.Multisite = true
	check("unknown")
	result.Inventory.WordPress.Multisite = false
	site.Status = models.StatusPaused
	called = false
	check("unknown")
	if called {
		t.Fatal("paused site must not boot WordPress")
	}
	site.Status = models.StatusActive
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	check("not_installed") // External deletion must not retain installed state.
	site.WebRoot = filepath.Join(site.WebRoot, "missing-root")
	check("unknown")
}
