package database

import "testing"

func TestWPAnomalyNewInstallAndUpgrade(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		var n int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('site_wp_anomaly_state')`).Scan(&n); err != nil || n != 10 {
			t.Fatal(n, err)
		}
	}
	check()
	if _, err := DB.Exec(`DROP TABLE site_wp_anomaly_state; DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES('1.0.58')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := RunUpgrades(); err != nil {
			t.Fatal(err)
		}
		check()
	}
	if LatestVersion() != "1.0.59" {
		t.Fatal(LatestVersion())
	}
}
