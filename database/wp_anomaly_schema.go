package database

const wpAnomalySchema = `CREATE TABLE IF NOT EXISTS site_wp_anomaly_state (
 site_id INTEGER PRIMARY KEY REFERENCES websites(id) ON DELETE CASCADE,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 threshold INTEGER NOT NULL DEFAULT 5 CHECK(threshold BETWEEN 1 AND 10000),
 baseline_since INTEGER NOT NULL DEFAULT 0,
 last_success INTEGER NOT NULL DEFAULT 0,
 next_check INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '',
 admins TEXT NOT NULL DEFAULT '[]',
 post_count INTEGER NOT NULL DEFAULT 0,
 post_alerted INTEGER NOT NULL DEFAULT 0 CHECK(post_alerted IN (0,1)),
 content_items TEXT NOT NULL DEFAULT '[]',
 critical_options TEXT NOT NULL DEFAULT '{}',
 content_changes TEXT NOT NULL DEFAULT '[]',
 content_alerted INTEGER NOT NULL DEFAULT 0 CHECK(content_alerted IN (0,1))
)`

func ensureWPAnomalyContentColumns() error {
	for _, column := range []struct {
		name string
		sql  string
	}{
		{"content_items", `ALTER TABLE site_wp_anomaly_state ADD COLUMN content_items TEXT NOT NULL DEFAULT '[]'`},
		{"critical_options", `ALTER TABLE site_wp_anomaly_state ADD COLUMN critical_options TEXT NOT NULL DEFAULT '{}'`},
		{"content_changes", `ALTER TABLE site_wp_anomaly_state ADD COLUMN content_changes TEXT NOT NULL DEFAULT '[]'`},
		{"content_alerted", `ALTER TABLE site_wp_anomaly_state ADD COLUMN content_alerted INTEGER NOT NULL DEFAULT 0 CHECK(content_alerted IN (0,1))`},
	} {
		var exists int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('site_wp_anomaly_state') WHERE name=?`, column.name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			if _, err := DB.Exec(column.sql); err != nil {
				return err
			}
		}
	}
	return nil
}
