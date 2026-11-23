package database

import "testing"

func TestPortableImportSchemaFromPreviousVersion(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DROP TABLE portable_import_tasks;
		ALTER TABLE websites DROP COLUMN ssl_temporary_fingerprint;
		DELETE FROM schema_version;
		INSERT INTO schema_version(version) VALUES('1.0.71')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RunMigrations(); err != nil {
			t.Fatal(err)
		}
		if err := RunUpgrades(); err != nil {
			t.Fatal(err)
		}
	}
	var taskTable, certificateColumn int
	if err := DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='portable_import_tasks' AND type='table'").Scan(&taskTable); err != nil || taskTable != 1 {
		t.Fatalf("portable task table = %d, error %v", taskTable, err)
	}
	if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name='ssl_temporary_fingerprint'").Scan(&certificateColumn); err != nil || certificateColumn != 1 {
		t.Fatalf("temporary SSL column = %d, error %v", certificateColumn, err)
	}
}

func TestPortableImportAliasUpgradePreservesHistoricalTasks(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`ALTER TABLE portable_import_tasks DROP COLUMN aliases;
		DELETE FROM schema_version;
		INSERT INTO schema_version(version) VALUES('1.0.72');
		INSERT INTO portable_import_tasks(id, backup_id, manifest_sha256, domain, url, status, phase, expires_at, updated_at)
		VALUES('existing','backup','hash','example.com','https://example.com','failed','validating',0,1);`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RunMigrations(); err != nil {
			t.Fatal(err)
		}
		if err := RunUpgrades(); err != nil {
			t.Fatal(err)
		}
	}
	var aliases, status string
	if err := DB.QueryRow("SELECT aliases, status FROM portable_import_tasks WHERE id='existing'").Scan(&aliases, &status); err != nil || aliases != "" || status != "failed" {
		t.Fatalf("old task after aliases migration: aliases=%q status=%q err=%v", aliases, status, err)
	}
}
