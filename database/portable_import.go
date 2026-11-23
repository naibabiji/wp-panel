package database

const portableImportSchema = `CREATE TABLE IF NOT EXISTS portable_import_tasks (
	id TEXT PRIMARY KEY,
	backup_id TEXT NOT NULL,
	manifest_sha256 TEXT NOT NULL,
	domain TEXT NOT NULL,
	aliases TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL,
	bundle_size INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'receiving',
	phase TEXT NOT NULL DEFAULT 'uploading',
	message TEXT NOT NULL DEFAULT '',
	site_id INTEGER REFERENCES websites(id) ON DELETE SET NULL,
	log_dir TEXT NOT NULL DEFAULT '',
	system_user TEXT NOT NULL DEFAULT '',
	web_root TEXT NOT NULL DEFAULT '',
	db_name TEXT NOT NULL DEFAULT '',
	db_user TEXT NOT NULL DEFAULT '',
	nginx_config TEXT NOT NULL DEFAULT '',
	php_pool TEXT NOT NULL DEFAULT '',
	cleanup_proof TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
)`
