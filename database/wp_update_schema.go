package database

var wpUpdateSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS wp_update_tasks (
		id                    TEXT PRIMARY KEY,
		site_id               INTEGER NOT NULL,
		component_type        TEXT NOT NULL,
		component_key         TEXT NOT NULL DEFAULT 'core',
		task_kind             TEXT NOT NULL DEFAULT 'update',
		parent_task_id         TEXT,
		trigger_type          TEXT NOT NULL DEFAULT 'manual',
		status                TEXT NOT NULL DEFAULT 'preparing',
		stage                 TEXT NOT NULL DEFAULT 'created',
		failure_stage         TEXT NOT NULL DEFAULT '',
		rollback_status       TEXT NOT NULL DEFAULT 'not_required',
		requires_attention    INTEGER NOT NULL DEFAULT 0,
		manual_disposition    TEXT NOT NULL DEFAULT '',
		current_version       TEXT NOT NULL,
		target_version        TEXT NOT NULL,
		package_source        TEXT NOT NULL,
		download_url          TEXT NOT NULL,
		downloaded_sha256     TEXT NOT NULL DEFAULT '',
		verification_level    TEXT NOT NULL DEFAULT '',
		package_snapshot_path TEXT NOT NULL DEFAULT '',
		backup_ready          INTEGER NOT NULL DEFAULT 0,
		database_backup_mode  TEXT NOT NULL DEFAULT 'fresh',
		database_backup_source_id INTEGER,
		plan_sealed_at        DATETIME,
		lease_owner           TEXT NOT NULL DEFAULT '',
		lease_expires_at      DATETIME,
		requested_at          DATETIME NOT NULL,
		started_at            DATETIME,
		finished_at           DATETIME,
		created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		FOREIGN KEY (parent_task_id) REFERENCES wp_update_tasks(id) ON DELETE CASCADE,
		CHECK (component_type IN ('core','plugin','theme')),
		CHECK (task_kind IN ('update','rollback')),
		CHECK (trigger_type = 'manual'),
		CHECK (status IN ('preparing','queued','running','success','failed','interrupted_unknown')),
		CHECK (rollback_status IN ('not_required','pending','success','failed')),
		CHECK (requires_attention IN (0,1)),
		CHECK (manual_disposition IN ('','confirmed_target_version','manually_rolled_back','marked_failed_no_action','escalated')),
		CHECK (verification_level IN ('','structure_only','official_verified')),
		CHECK (database_backup_mode IN ('fresh','reuse')),
		CHECK ((task_kind = 'update' AND parent_task_id IS NULL) OR (task_kind = 'rollback' AND parent_task_id IS NOT NULL))
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_wp_update_tasks_active_site
		ON wp_update_tasks(site_id) WHERE status IN ('preparing','queued','running')`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_wp_update_tasks_active_rollback_parent
		ON wp_update_tasks(parent_task_id) WHERE task_kind = 'rollback' AND status IN ('preparing','queued','running')`,
	`CREATE INDEX IF NOT EXISTS ix_wp_update_tasks_claim
		ON wp_update_tasks(status, requested_at)`,
	`CREATE INDEX IF NOT EXISTS ix_wp_update_tasks_site_history
		ON wp_update_tasks(site_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS wp_update_task_events (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id     TEXT NOT NULL,
		stage       TEXT NOT NULL,
		result      TEXT NOT NULL,
		error_code  TEXT NOT NULL DEFAULT '',
		summary     TEXT NOT NULL DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (task_id) REFERENCES wp_update_tasks(id) ON DELETE CASCADE,
		CHECK (result IN ('info','success','failed','interrupted','manual'))
	)`,
	`CREATE INDEX IF NOT EXISTS ix_wp_update_task_events_task
		ON wp_update_task_events(task_id, id)`,
	`CREATE TABLE IF NOT EXISTS wp_update_task_backups (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id     TEXT NOT NULL,
		kind        TEXT NOT NULL,
		file_path   TEXT NOT NULL,
		file_size   INTEGER NOT NULL DEFAULT 0,
		sha256      TEXT NOT NULL,
		protected   INTEGER NOT NULL DEFAULT 1,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deleted_at  DATETIME,
		cleanup_result TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (task_id) REFERENCES wp_update_tasks(id) ON DELETE CASCADE,
		CHECK (kind IN ('database','core_files','plugin_files','theme_files')),
		CHECK (protected IN (0,1)),
		UNIQUE (task_id, kind, file_path)
	)`,
	`CREATE INDEX IF NOT EXISTS ix_wp_update_task_backups_protected
		ON wp_update_task_backups(task_id, protected, deleted_at)`,
	`CREATE TRIGGER IF NOT EXISTS trg_wp_update_tasks_sealed_immutable
		BEFORE UPDATE OF site_id, component_type, component_key, task_kind, parent_task_id,
			current_version, target_version, package_source, download_url,
			downloaded_sha256, verification_level, package_snapshot_path, database_backup_mode
		ON wp_update_tasks
		WHEN OLD.plan_sealed_at IS NOT NULL AND (
			NEW.site_id != OLD.site_id OR NEW.component_type != OLD.component_type OR
			NEW.component_key != OLD.component_key OR NEW.task_kind != OLD.task_kind OR
			COALESCE(NEW.parent_task_id, '') != COALESCE(OLD.parent_task_id, '') OR
			NEW.current_version != OLD.current_version OR NEW.target_version != OLD.target_version OR
			NEW.package_source != OLD.package_source OR NEW.download_url != OLD.download_url OR
			NEW.downloaded_sha256 != OLD.downloaded_sha256 OR
			NEW.verification_level != OLD.verification_level OR
			NEW.package_snapshot_path != OLD.package_snapshot_path OR
			NEW.database_backup_mode != OLD.database_backup_mode
		)
		BEGIN SELECT RAISE(ABORT, 'sealed update plan is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS trg_wp_update_tasks_sealed_backup_mode_immutable
		BEFORE UPDATE OF database_backup_mode ON wp_update_tasks
		WHEN OLD.plan_sealed_at IS NOT NULL AND NEW.database_backup_mode != OLD.database_backup_mode
		BEGIN SELECT RAISE(ABORT, 'sealed update backup mode is immutable'); END`,
}
