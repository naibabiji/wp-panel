package database

import "testing"

// TestUpgradeAddsImageOptimizerSchemaFrom1048 verifies old installs upgrading
// from before 1.0.49 get the same site_image_optimization_jobs /
// site_image_optimization_files schema that a fresh install gets via
// migrations.go, reusing imageOptimizerSchemaStatements for both paths so the
// two can't drift apart (same convention as the wp_inventory schema tests).
func TestUpgradeAddsImageOptimizerSchemaFrom1048(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("initial RunUpgrades() error = %v", err)
	}

	if _, err := DB.Exec(`INSERT INTO websites
		(name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path)
		VALUES ('existing.example.com', 'existing.example.com', 'active', 'wp_existing', '/tmp/www', '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf')`); err != nil {
		t.Fatalf("insert existing website: %v", err)
	}

	for _, table := range []string{"site_image_optimization_files", "site_image_optimization_jobs"} {
		if _, err := DB.Exec("DROP TABLE " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
		t.Fatalf("delete schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.48')"); err != nil {
		t.Fatalf("seed schema_version: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades() error = %v", err)
	}
	// Idempotency: running the upgrade path twice must not error (mirrors what
	// happens if the panel restarts mid-upgrade or is started twice).
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades() error = %v", err)
	}

	if got := LatestVersion(); got != "1.0.49" {
		t.Fatalf("LatestVersion() = %q, want 1.0.49", got)
	}

	var sites int
	if err := DB.QueryRow("SELECT COUNT(*) FROM websites WHERE domain = 'existing.example.com'").Scan(&sites); err != nil {
		t.Fatalf("query existing website: %v", err)
	}
	if sites != 1 {
		t.Fatalf("existing websites = %d, want 1 (upgrade must not touch unrelated data)", sites)
	}

	for _, table := range []string{"site_image_optimization_jobs", "site_image_optimization_files"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("table %s exists=%d err=%v", table, exists, err)
		}
	}
	var indexExists int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='ux_site_image_optimization_jobs_active_site'").Scan(&indexExists); err != nil || indexExists != 1 {
		t.Fatalf("unique active-job index missing: exists=%d err=%v", indexExists, err)
	}

	// Functional check: the "only one active job per site" constraint the
	// batch feature depends on for idempotent re-triggering must actually hold.
	if _, err := DB.Exec(`INSERT INTO site_image_optimization_jobs (site_id, status) VALUES (1, 'queued')`); err != nil {
		t.Fatalf("insert first queued job: %v", err)
	}
	if _, err := DB.Exec(`INSERT INTO site_image_optimization_jobs (site_id, status) VALUES (1, 'running')`); err == nil {
		t.Fatalf("expected unique index to reject a second active job for the same site")
	}
}

// TestFreshInstallImageOptimizerSchemaMatchesUpgradePath ensures a brand new
// install (migrations.go only) already has the full table set the upgrade
// path adds for old installs, so both paths converge on the same schema.
func TestFreshInstallImageOptimizerSchemaMatchesUpgradePath(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	for _, table := range []string{"site_image_optimization_jobs", "site_image_optimization_files"} {
		var exists int
		if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("fresh install missing table %s: exists=%d err=%v", table, exists, err)
		}
	}
}
