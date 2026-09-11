package database

import "testing"

func TestSiteMigrationSchemaFreshInstall(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations(): %v", err)
	}

	for _, table := range []string{
		"site_migration_peers",
		"site_migration_batches",
		"site_migration_sites",
		"site_migration_artifacts",
		"site_migration_resources",
		"site_migration_locks",
		"site_migration_events",
	} {
		var count int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count=%d, want 1", table, count)
		}
	}
}

func TestSiteMigrationSchemaUpgradeFrom1053IsIdempotent(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations(): %v", err)
	}
	for _, table := range []string{
		"site_migration_events", "site_migration_locks", "site_migration_resources",
		"site_migration_artifacts", "site_migration_sites", "site_migration_batches", "site_migration_peers",
	} {
		if _, err := DB.Exec("DROP TABLE " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := DB.Exec("INSERT INTO schema_version(version) VALUES ('1.0.53')"); err != nil {
		t.Fatalf("seed version: %v", err)
	}

	if err := RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades(): %v", err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatalf("second RunUpgrades(): %v", err)
	}
	if got := LatestVersion(); got != "1.0.60" {
		t.Fatalf("LatestVersion()=%q, want 1.0.60", got)
	}
	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='site_migration_sites'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("upgraded site_migration_sites count=%d err=%v", count, err)
	}
	for _, column := range []string{"estimated_file_bytes", "estimated_database_bytes", "reserved_bytes"} {
		var exists int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('site_migration_sites') WHERE name=?`, column).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("column %s exists=%d err=%v", column, exists, err)
		}
	}
}

func TestSiteMigrationSchemaEnforcesActiveSiteLock(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("RunMigrations(): %v", err)
	}
	if _, err := DB.Exec(`INSERT INTO site_migration_peers(id,status) VALUES ('peer-1','paired')`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO site_migration_batches(id,peer_id,direction) VALUES ('batch-1','peer-1','source')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"migration-1", "migration-2"} {
		if _, err := DB.Exec(`INSERT INTO site_migration_sites(id,batch_id,source_domain,target_domain,site_type) VALUES (?,'batch-1',?,?, 'wordpress')`, id, id+".example", id+".example"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DB.Exec(`INSERT INTO site_migration_locks(domain,migration_site_id,direction) VALUES ('example.com','migration-1','source')`); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err := DB.Exec(`INSERT INTO site_migration_locks(domain,migration_site_id,direction) VALUES ('example.com','migration-2','source')`); err == nil {
		t.Fatal("duplicate active domain lock unexpectedly succeeded")
	}
	if _, err := DB.Exec(`UPDATE site_migration_locks SET status='released', released_at=CURRENT_TIMESTAMP WHERE domain='example.com' AND status='active'`); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	if _, err := DB.Exec(`INSERT INTO site_migration_locks(domain,migration_site_id,direction) VALUES ('example.com','migration-2','source')`); err != nil {
		t.Fatalf("reacquire released domain lock: %v", err)
	}
}
