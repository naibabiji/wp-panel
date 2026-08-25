package database

// siteMigrationSchemaStatements is shared by fresh installs and migration upgrades.
// upgrade so both paths converge on exactly the same migration-task schema.
var siteMigrationSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS site_migration_peers (
		id                    TEXT PRIMARY KEY,
		name                  TEXT NOT NULL DEFAULT '',
		base_url              TEXT NOT NULL DEFAULT '',
		certificate_sha256    TEXT NOT NULL DEFAULT '',
		local_certificate_sha256 TEXT NOT NULL DEFAULT '',
		inbound_credential_hash TEXT NOT NULL DEFAULT '',
		outbound_credential     TEXT NOT NULL DEFAULT '',
		pair_token_hash       TEXT NOT NULL DEFAULT '',
		pair_token_expires_at DATETIME,
		pairing_challenge     TEXT NOT NULL DEFAULT '',
		pair_attempts         INTEGER NOT NULL DEFAULT 0,
		protocol_version      INTEGER NOT NULL DEFAULT 1,
		status                TEXT NOT NULL DEFAULT 'pending',
		paired_at             DATETIME,
		revoked_at            DATETIME,
		created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CHECK (status IN ('pending','paired','expired','revoked')),
		CHECK (pair_attempts >= 0)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_site_migration_peers_status
		ON site_migration_peers(status, pair_token_expires_at)`,

	`CREATE TABLE IF NOT EXISTS site_migration_batches (
		id           TEXT PRIMARY KEY,
		peer_id      TEXT NOT NULL,
		direction    TEXT NOT NULL,
		status       TEXT NOT NULL DEFAULT 'draft',
		requested_by TEXT NOT NULL DEFAULT '',
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		finished_at  DATETIME,
		FOREIGN KEY (peer_id) REFERENCES site_migration_peers(id) ON DELETE RESTRICT,
		CHECK (direction IN ('source','target')),
		CHECK (status IN ('draft','preflight','active','completed','failed','abandoned'))
	)`,
	`CREATE INDEX IF NOT EXISTS idx_site_migration_batches_peer
		ON site_migration_batches(peer_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS site_migration_sites (
		id                  TEXT PRIMARY KEY,
		batch_id            TEXT NOT NULL,
		source_site_id      INTEGER,
		target_site_id      INTEGER,
		source_domain       TEXT NOT NULL,
		target_domain       TEXT NOT NULL,
		site_type           TEXT NOT NULL,
		status              TEXT NOT NULL DEFAULT 'draft',
		stage               TEXT NOT NULL DEFAULT 'draft',
		lease_owner         TEXT NOT NULL DEFAULT '',
		lease_expires_at    DATETIME,
		attempt_count       INTEGER NOT NULL DEFAULT 0,
		estimated_file_bytes INTEGER NOT NULL DEFAULT 0,
		estimated_database_bytes INTEGER NOT NULL DEFAULT 0,
		reserved_bytes      INTEGER NOT NULL DEFAULT 0,
		settings_snapshot   TEXT NOT NULL DEFAULT '{}',
		error_code          TEXT NOT NULL DEFAULT '',
		error_message       TEXT NOT NULL DEFAULT '',
		cleanup_status      TEXT NOT NULL DEFAULT 'not_needed',
		created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		finished_at         DATETIME,
		FOREIGN KEY (batch_id) REFERENCES site_migration_batches(id) ON DELETE CASCADE,
		FOREIGN KEY (source_site_id) REFERENCES websites(id) ON DELETE RESTRICT,
		FOREIGN KEY (target_site_id) REFERENCES websites(id) ON DELETE RESTRICT,
		CHECK (site_type IN ('wordpress','php')),
		CHECK (status IN ('draft','queued','running','awaiting_cutover','completed','failed_retryable','failed_manual','interrupted_unknown','cancelling','cleanup_failed','abandoned')),
		CHECK (cleanup_status IN ('not_needed','pending','running','complete','failed')),
		CHECK (attempt_count >= 0),
		CHECK (estimated_file_bytes >= 0 AND estimated_database_bytes >= 0 AND reserved_bytes >= 0)
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_site_migration_sites_batch_source
		ON site_migration_sites(batch_id, source_domain)`,
	`CREATE INDEX IF NOT EXISTS idx_site_migration_sites_claim
		ON site_migration_sites(status, lease_expires_at, created_at)`,

	`CREATE TABLE IF NOT EXISTS site_migration_artifacts (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		migration_site_id TEXT NOT NULL,
		relative_path     TEXT NOT NULL,
		artifact_type     TEXT NOT NULL,
		entry_type        TEXT NOT NULL DEFAULT 'file',
		file_mode         INTEGER NOT NULL DEFAULT 0,
		link_target       TEXT NOT NULL DEFAULT '',
		modified_unix_ns  INTEGER NOT NULL DEFAULT 0,
		file_size         INTEGER NOT NULL DEFAULT 0,
		sha256            TEXT NOT NULL DEFAULT '',
		confirmed_offset  INTEGER NOT NULL DEFAULT 0,
		staging_key       TEXT NOT NULL DEFAULT '',
		status            TEXT NOT NULL DEFAULT 'pending',
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (migration_site_id) REFERENCES site_migration_sites(id) ON DELETE CASCADE,
		UNIQUE (migration_site_id, artifact_type, relative_path),
		CHECK (artifact_type IN ('file','database','certificate','private_key')),
		CHECK (entry_type IN ('file','directory','symlink')),
		CHECK (status IN ('pending','transferring','verified','published','failed')),
		CHECK (file_size >= 0 AND confirmed_offset >= 0 AND confirmed_offset <= file_size)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_site_migration_artifacts_site
		ON site_migration_artifacts(migration_site_id, status)`,

	`CREATE TABLE IF NOT EXISTS site_migration_resources (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		migration_site_id TEXT NOT NULL,
		resource_type     TEXT NOT NULL,
		identifier        TEXT NOT NULL,
		ownership_tag     TEXT NOT NULL DEFAULT '',
		status            TEXT NOT NULL DEFAULT 'created',
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (migration_site_id) REFERENCES site_migration_sites(id) ON DELETE CASCADE,
		UNIQUE (migration_site_id, resource_type, identifier),
		CHECK (status IN ('created','published','removed','remove_failed'))
	)`,

	`CREATE TABLE IF NOT EXISTS site_migration_locks (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		domain            TEXT NOT NULL,
		site_id           INTEGER,
		migration_site_id TEXT NOT NULL,
		direction         TEXT NOT NULL,
		status            TEXT NOT NULL DEFAULT 'active',
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		released_at       DATETIME,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE RESTRICT,
		FOREIGN KEY (migration_site_id) REFERENCES site_migration_sites(id) ON DELETE CASCADE,
		CHECK (direction IN ('source','target')),
		CHECK (status IN ('active','released'))
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_site_migration_locks_active_domain
		ON site_migration_locks(domain) WHERE status='active'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_site_migration_locks_active_site
		ON site_migration_locks(site_id) WHERE site_id IS NOT NULL AND status='active'`,

	`CREATE TABLE IF NOT EXISTS site_migration_events (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		migration_site_id TEXT NOT NULL,
		stage             TEXT NOT NULL DEFAULT '',
		result            TEXT NOT NULL,
		message           TEXT NOT NULL DEFAULT '',
		created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (migration_site_id) REFERENCES site_migration_sites(id) ON DELETE CASCADE,
		CHECK (result IN ('info','success','failed','warning'))
	)`,
	`CREATE INDEX IF NOT EXISTS idx_site_migration_events_site
		ON site_migration_events(migration_site_id, created_at)`,
}
