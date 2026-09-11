package executor

import (
	"context"
	"os"
	"path/filepath"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

// CompanionPluginStatus is a fresh observation, not a cached installation flag.
// Callers hold the shared site operation lock and resolve the site under it.
func CompanionPluginStatus(ctx context.Context, cfg *config.Config, site *models.Website) string {
	return companionPluginStatus(site, func() (WPInventoryRunResult, error) {
		runner, err := NewWPInventoryRunner()
		if err != nil {
			return WPInventoryRunResult{}, err
		}
		return runner.Collect(ctx, cfg, site, false)
	})
}

func companionPluginStatus(site *models.Website, collect func() (WPInventoryRunResult, error)) string {
	if site == nil || site.SiteType != "wordpress" || site.Status != models.StatusActive {
		return "unknown"
	}
	root, err := os.Stat(site.WebRoot)
	if err != nil || !root.IsDir() {
		return "unknown"
	}
	const pluginFile = "wp-panel-optimizer/wp-panel-optimizer.php"
	file, err := os.Stat(filepath.Join(site.WebRoot, "wp-content", "plugins", pluginFile))
	if os.IsNotExist(err) {
		return "not_installed"
	}
	if err != nil || !file.Mode().IsRegular() {
		return "unknown"
	}
	result, err := collect()
	if err != nil || result.Inventory.WordPress.Multisite {
		return "unknown"
	}
	for _, plugin := range result.Inventory.Plugins {
		if plugin.File == pluginFile {
			if !plugin.Active {
				return "inactive"
			}
			return "installed"
		}
	}
	// A present file missing from the fresh inventory is not proof of activation.
	return "unknown"
}
