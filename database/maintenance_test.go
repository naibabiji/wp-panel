package database

import (
	"path/filepath"
	"testing"
)

func TestMaintenanceSchemaNewAndUpgrade(t *testing.T) {
	if err := Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatal(err)
	}
	defer Close()
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name='maintenance_security'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("fresh: %d %v", n, err)
	}
	if _, err := DB.Exec(`ALTER TABLE websites DROP COLUMN maintenance_security`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`UPDATE schema_version SET version='1.0.57'`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	if err := ensureMaintenanceSecurityColumn(); err != nil {
		t.Fatal(err)
	}
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name='maintenance_security'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("upgrade: %d %v", n, err)
	}
}
