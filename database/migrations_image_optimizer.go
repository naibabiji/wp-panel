package database

// imageOptimizerSchemaStatements 同时供全新安装和 1.0.49 增量升级使用，避免两条
// 路径的表结构、约束或索引随维护产生漂移（参照 wpInventorySchemaStatements 的做法）。
//
// site_image_optimization_jobs 记录历史图库批量优化的任务级进度；
// site_image_optimization_files 记录已处理过的文件（按站点+相对路径+文件大小做
// 幂等指纹），重复运行批量任务时跳过没有变化的文件，参照 AutoDeployPluginUpdates
// 的内容比对思路，但用文件大小+修改时间做轻量指纹，不对每个文件做全量内容比对。
var imageOptimizerSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS site_image_optimization_jobs (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id         INTEGER NOT NULL,
		status          TEXT NOT NULL DEFAULT 'queued',
		total_files     INTEGER NOT NULL DEFAULT 0,
		processed_files INTEGER NOT NULL DEFAULT 0,
		succeeded_files INTEGER NOT NULL DEFAULT 0,
		failed_files    INTEGER NOT NULL DEFAULT 0,
		bytes_before    INTEGER NOT NULL DEFAULT 0,
		bytes_after     INTEGER NOT NULL DEFAULT 0,
		last_error      TEXT NOT NULL DEFAULT '',
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		finished_at     DATETIME,
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE,
		CHECK (status IN ('queued','running','succeeded','failed','stopped'))
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_site_image_optimization_jobs_active_site
		ON site_image_optimization_jobs(site_id) WHERE status IN ('queued','running')`,
	`CREATE INDEX IF NOT EXISTS ix_site_image_optimization_jobs_site
		ON site_image_optimization_jobs(site_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS site_image_optimization_files (
		site_id        INTEGER NOT NULL,
		relative_path  TEXT NOT NULL,
		original_size  INTEGER NOT NULL,
		optimized_size INTEGER NOT NULL,
		mtime_unix     INTEGER NOT NULL,
		processed_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (site_id, relative_path),
		FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
	)`,
}
