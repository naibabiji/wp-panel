package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/config"
)

// TestDownloadWPUsesLocalCacheWithoutNetwork proves downloadWP wires
// cfg.Paths.WordPressPackage into AcquireCorePackage correctly: a warm cache
// is deployed with zero network access, matching what deployWordPress needs
// during site creation on a server with no outbound connectivity.
func TestDownloadWPUsesLocalCacheWithoutNetwork(t *testing.T) {
	cachePath := wordPressZIPWithVersion(t, "7.1")
	cfg := &config.Config{Paths: config.PathsConfig{WordPressPackage: cachePath}}
	destPath := filepath.Join(t.TempDir(), "wordpress.zip")

	if err := downloadWP(context.Background(), cfg, destPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWordPressPackage(context.Background(), destPath); err != nil {
		t.Fatalf("deployed file is not a valid package: %v", err)
	}
}

// TestDeployWordPressExtractsCachedPackageIntoWebRoot exercises the full
// deployWordPress path (download + unzip + move into webRoot) against a
// locally cached package, with no network involved.
func TestDeployWordPressExtractsCachedPackageIntoWebRoot(t *testing.T) {
	cachePath := wordPressZIPWithVersion(t, "7.1")
	cfg := &config.Config{Paths: config.PathsConfig{WordPressPackage: cachePath}}
	webRoot := t.TempDir()
	tmpDir := filepath.Join(t.TempDir(), "deploy-work")

	if err := deployWordPress(context.Background(), cfg, webRoot, tmpDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(webRoot, "wp-admin", "index.php")); err != nil {
		t.Fatalf("wp-admin/index.php missing after deploy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(webRoot, "wp-includes", "version.php")); err != nil {
		t.Fatalf("wp-includes/version.php missing after deploy: %v", err)
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatal("deployWordPress must clean up its temp working directory")
	}
}

func TestDownloadWPSurfacesErrorWhenCacheMissingAndRefreshUnavailable(t *testing.T) {
	// An unresolvable WordPressPackage path with no real cache and no
	// network in this test environment must fail cleanly, not panic, and
	// must not leave a partial file behind.
	cfg := &config.Config{Paths: config.PathsConfig{WordPressPackage: filepath.Join(t.TempDir(), "missing.zip")}}
	destPath := filepath.Join(t.TempDir(), "wordpress.zip")
	ctx, cancel := context.WithTimeout(context.Background(), 0) // already-expired context: refresh must fail fast, not hit the real network
	defer cancel()

	if err := downloadWP(ctx, cfg, destPath); err == nil {
		t.Fatal("expected an error when the cache is empty and the context is already done")
	}
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		t.Fatal("a failed download must not leave a partial file at destPath")
	}
}
