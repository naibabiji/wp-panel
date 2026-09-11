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
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('site_wp_anomaly_state')`).Scan(&n); err != nil || n != 14 {
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
	if _, err := DB.Exec(`DROP TABLE site_wp_anomaly_state;
CREATE TABLE site_wp_anomaly_state (
 site_id INTEGER PRIMARY KEY REFERENCES websites(id) ON DELETE CASCADE,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)), threshold INTEGER NOT NULL DEFAULT 5,
 baseline_since INTEGER NOT NULL DEFAULT 0, last_success INTEGER NOT NULL DEFAULT 0,
 next_check INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', admins TEXT NOT NULL DEFAULT '[]',
 post_count INTEGER NOT NULL DEFAULT 0, post_alerted INTEGER NOT NULL DEFAULT 0 CHECK(post_alerted IN (0,1))
);
DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES('1.0.59')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	check()
	if LatestVersion() != "1.0.61" {
		t.Fatal(LatestVersion())
	}
}
