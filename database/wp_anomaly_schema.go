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
 post_alerted INTEGER NOT NULL DEFAULT 0 CHECK(post_alerted IN (0,1))
)`
