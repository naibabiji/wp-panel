// Package database — 版本升级机制说明
//
// 两个文件的分工：
//
//   - migrations.go   全量建表 + 种子数据，给全新安装用，始终代表数据库的最新状态。
//                     每次启动都会完整执行一遍（依赖 IF NOT EXISTS / OR IGNORE 保证幂等）。
//   - upgrades.go     增量升级步骤，给老版本升级用。仅在版本落后时顺序执行。
//
// 数据库变更流程：
//
//   1. 在 migrations.go 对应位置添加 CREATE / INSERT 语句（新装用）。
//   2. 在 upgrades.go 末尾追加 Upgrade 条目（升级用）。
//   3. 升级条目永久保留，严禁删除。用户可能跨多个版本升级，删除升级条目会导致
//      老版本跳过必要的 ALTER TABLE 等增量迁移。
//
// 运行时逻辑（main.go 启动 → database.Open → RunMigrations → RunUpgrades）：
//
//   新装：  migrations 创建全部表 + 种子 → upgrades 发现版本表为空 → 跳过所有升级 → 写入最新版本号
//   升级：  migrations 幂等执行（无实际变化）→ upgrades 发现版本落后 → 逐条执行缺失的升级 → 更新版本号
//   已最新：migrations 幂等执行 → upgrades 发现版本已是最新 → 跳过
//
// 版本号约定：
//   使用语义化版本号（如 "1.0.0"），与 Git tag 保持一致。LatestVersion() 返回 upgrades 列表中
//   最后一条的版本号（列表为空时返回 "1.0.0"），即当前代码所代表的数据库版本。

package database

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// Upgrade 定义一次版本升级需要执行的数据库变更。
// SQL 中的语句应使用 IF NOT EXISTS / OR IGNORE 等幂等写法，确保重复执行安全。
// Func 为可选的 Go 代码迁移，在 SQL 之后执行，用于文件系统清理等非数据库操作。
type Upgrade struct {
	Version     string       // 目标版本号，如 "1.0.0"
	Description string       // 本次升级做了什么
	SQL         []string     // 要执行的 SQL 语句
	Func        func() error // 可选的 Go 函数迁移
}

// registeredFuncs 存放外部包注册的升级函数，解决循环依赖问题（database 不能 import executor）。
var registeredFuncs = map[string]func() error{}

// RegisterUpgrade 供外部包注册升级函数，version 必须与 upgrades 列表中的 Version 匹配。
func RegisterUpgrade(version string, fn func() error) {
	registeredFuncs[version] = fn
}

// upgrades 按版本顺序排列（旧→新），永久保留，严禁删除旧条目（跨版本升级依赖完整迁移链）。
// v1.0.0 正式版发布时清空过一次历史，此后所有升级条目持续累积。
var upgrades = []Upgrade{
	{
		Version:     "1.0.1",
		Description: "迁移 wp-panel-config.json 到 Web 目录外，轮换 API Key",
		Func:        migratePluginConfigs,
	},
	{
		Version:     "1.0.2",
		Description: "新增 XML-RPC 站点开关，默认禁用",
		SQL: []string{
			`ALTER TABLE websites ADD COLUMN xmlrpc_enabled INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		Version:     "1.0.3",
		Description: "cron_jobs 补充 running 列 + 默认插件新增 Redis Cache",
		SQL: []string{
			`ALTER TABLE cron_jobs ADD COLUMN running INTEGER NOT NULL DEFAULT 0`,
			`INSERT OR IGNORE INTO wp_extension_config (etype, slug, name, enabled) VALUES ('plugin', 'redis-cache', 'Redis Cache', 1)`,
		},
	},
	{
		Version:     "1.0.4",
		Description: "强化每站点 Unix 用户组隔离和敏感文件权限",
	},
	{
		Version:     "1.0.5",
		Description: "新增系统可用更新告警开关",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('alert_system_update', 'true', '系统可用更新告警')`,
		},
	},
	{
		Version:     "1.0.6",
		Description: "新增面板新版本告警开关",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('alert_panel_update', 'true', '面板新版本告警')`,
		},
	},
	{
		Version:     "1.0.7",
		Description: "新增 WP_DEBUG / 文章修订 / 内存限制 优化项",
		SQL: []string{
			`ALTER TABLE websites ADD COLUMN wp_debug_enabled INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN wp_post_revisions INTEGER NOT NULL DEFAULT -1`,
			`ALTER TABLE websites ADD COLUMN wp_memory_limit TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		Version:     "1.0.8",
		Description: "新增匿名安装统计开关",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('telemetry_enabled', 'true', '匿名安装统计（仅上报机器标识和版本号）')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('telemetry_url', '', '自定义统计上报地址（留空使用默认）')`,
		},
	},
	{
		Version:     "1.0.9",
		Description: "新增 GitHub 反代地址设置",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('github_proxy', '', 'GitHub 反代地址，留空直连')`,
		},
	},
	{
		Version:     "1.0.10",
		Description: "Backfill WP_CACHE_KEY_SALT for existing WordPress sites",
	},
	{
		Version:     "1.0.11",
		Description: "新增 WordPress 安全日志路径白名单设置",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('wp_security_log_whitelist', '', 'WordPress安全日志路径白名单')`,
		},
	},
	{
		Version:     "1.0.12",
		Description: "新增网站级 CDN 真实 IP 配置组",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS cdn_realip_groups (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				name        TEXT    NOT NULL UNIQUE,
				provider    TEXT    NOT NULL DEFAULT 'custom',
				header_name TEXT    NOT NULL,
				ip_ranges   TEXT    NOT NULL DEFAULT '',
				builtin     INTEGER NOT NULL DEFAULT 0,
				enabled     INTEGER NOT NULL DEFAULT 1,
				description TEXT    DEFAULT '',
				created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_cdn_realip_groups_enabled ON cdn_realip_groups(enabled)`,
			`CREATE TABLE IF NOT EXISTS website_cdn_realip_groups (
				website_id INTEGER NOT NULL,
				group_id   INTEGER NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (website_id, group_id),
				FOREIGN KEY (website_id) REFERENCES websites(id) ON DELETE CASCADE,
				FOREIGN KEY (group_id) REFERENCES cdn_realip_groups(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_website_cdn_realip_groups_group ON website_cdn_realip_groups(group_id)`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('cloudflare_realip_ips', '', 'Cloudflare Real IP 专用官方IP段')`,
			`INSERT OR IGNORE INTO cdn_realip_groups (name, provider, header_name, ip_ranges, builtin, enabled, description) VALUES
				('Cloudflare', 'cloudflare', 'CF-Connecting-IP', '', 1, 1, 'Cloudflare 官方 IP 段由面板自动拉取'),
				('通用 CDN（兼容模式）', 'compatible', 'X-Forwarded-For', '', 1, 1, '不校验来源 IP，直接信任 X-Forwarded-For')`,
		},
		Func: ensureCDNRealIPEnabledColumn,
	},
	{
		Version:     "1.0.13",
		Description: "新增 Bot UA 统一限速设置",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('bot_limit_enabled', 'false', '是否开启Bot UA统一限速')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('bot_limit_rpm', '30', '每站点Bot每分钟最大请求数')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('bot_limit_burst', '20', 'Bot突发缓冲允许量')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('googlebot_ips', '', 'Googlebot官方IP段缓存')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('bingbot_ips', '', 'Bingbot官方IP段缓存')`,
		},
	},
	{
		Version:     "1.0.14",
		Description: "记录站点最近一次 SSL 申请失败原因",
		Func:        ensureSSLLastErrorColumn,
	},
	{
		Version:     "1.0.15",
		Description: "新增面板自动更新设置",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_enabled', 'false', '是否启用面板自动更新')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_mode', 'patch_only', '面板自动更新模式：patch_only/all_stable')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_window', '03:00-05:00', '面板自动更新时间窗口')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_release_delay_minutes', '15', '面板自动更新发布延迟分钟数')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_signature_timeout_minutes', '120', '面板自动更新等待签名超时分钟数')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_target_version', '', '面板自动更新最近目标版本')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_check_at', '', '面板自动更新最近检查时间')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_attempt_at', '', '面板自动更新最近尝试时间')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_status', '', '面板自动更新最近状态')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_stage', '', '面板自动更新最近阶段')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_error', '', '面板自动更新最近错误')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_success_at', '', '面板自动更新最近成功时间')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_last_success_version', '', '面板自动更新最近成功版本')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_signature_wait_version', '', '面板自动更新等待签名版本')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('panel_auto_update_signature_wait_at', '', '面板自动更新等待签名开始时间')`,
		},
	},
	{
		Version:     "1.0.16",
		Description: "新增站点级 SSL 证书导出开关",
		Func:        ensureSSLExportEnabledColumn,
	},
	{
		Version:     "1.0.17",
		Description: "新增 PHP 站点 Web 入口目录配置",
		Func:        ensureDocumentRootSubdirColumn,
	},
	{
		Version:     "1.0.18",
		Description: "新增站点 AI 只读诊断设置和会话记录",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS ai_settings (
				id              INTEGER PRIMARY KEY,
				enabled         INTEGER NOT NULL DEFAULT 0,
				provider        TEXT    NOT NULL DEFAULT 'deepseek',
				base_url        TEXT    NOT NULL DEFAULT 'https://api.deepseek.com',
				model           TEXT    NOT NULL DEFAULT 'deepseek-v4-pro',
				api_key         TEXT    NOT NULL DEFAULT '',
				timeout_seconds INTEGER NOT NULL DEFAULT 60,
				created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`INSERT OR IGNORE INTO ai_settings (id) VALUES (1)`,
			`CREATE TABLE IF NOT EXISTS ai_sessions (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				site_id        INTEGER NOT NULL,
				symptom        TEXT    NOT NULL DEFAULT '',
				status         TEXT    NOT NULL DEFAULT 'pending',
				risk_level     TEXT    NOT NULL DEFAULT '',
				summary        TEXT    NOT NULL DEFAULT '',
				report_json    TEXT    NOT NULL DEFAULT '',
				raw_text       TEXT    NOT NULL DEFAULT '',
				prompt_chars   INTEGER NOT NULL DEFAULT 0,
				response_chars INTEGER NOT NULL DEFAULT 0,
				error_message  TEXT    NOT NULL DEFAULT '',
				created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (site_id) REFERENCES websites(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_sessions_site ON ai_sessions(site_id, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_sessions_status ON ai_sessions(site_id, status)`,
		},
	},
	{
		Version:     "1.0.19",
		Description: "新增 AI 诊断会话追问消息记录",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS ai_messages (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id     INTEGER NOT NULL,
				role           TEXT    NOT NULL DEFAULT '',
				content        TEXT    NOT NULL DEFAULT '',
				prompt_chars   INTEGER NOT NULL DEFAULT 0,
				response_chars INTEGER NOT NULL DEFAULT 0,
				error_message  TEXT    NOT NULL DEFAULT '',
				created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (session_id) REFERENCES ai_sessions(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_messages_session ON ai_messages(session_id, created_at)`,
		},
	},
	{
		Version:     "1.0.20",
		Description: "远程备份新增 S3 兼容对象存储后端",
		SQL: []string{
			`CREATE TABLE IF NOT EXISTS remote_backup_settings (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				enabled     INTEGER NOT NULL DEFAULT 0,
				backup_type TEXT    NOT NULL DEFAULT 'rsync',
				host        TEXT    NOT NULL DEFAULT '',
				port        INTEGER NOT NULL DEFAULT 22,
				username    TEXT    NOT NULL DEFAULT 'root',
				auth_type   TEXT    NOT NULL DEFAULT 'password',
				password    TEXT    NOT NULL DEFAULT '',
				ssh_key     TEXT    NOT NULL DEFAULT '',
				remote_path TEXT    NOT NULL DEFAULT '',
				keep_local  INTEGER NOT NULL DEFAULT 1,
				s3_endpoint      TEXT NOT NULL DEFAULT '',
				s3_bucket        TEXT NOT NULL DEFAULT '',
				s3_region        TEXT NOT NULL DEFAULT 'auto',
				s3_access_key_id TEXT NOT NULL DEFAULT '',
				s3_secret_key    TEXT NOT NULL DEFAULT '',
				s3_path_prefix   TEXT NOT NULL DEFAULT '',
				created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`INSERT OR IGNORE INTO remote_backup_settings (id) VALUES (1)`,
			`ALTER TABLE remote_backup_settings ADD COLUMN backup_type TEXT NOT NULL DEFAULT 'rsync'`,
			`ALTER TABLE remote_backup_settings ADD COLUMN s3_endpoint TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE remote_backup_settings ADD COLUMN s3_bucket TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE remote_backup_settings ADD COLUMN s3_region TEXT NOT NULL DEFAULT 'auto'`,
			`ALTER TABLE remote_backup_settings ADD COLUMN s3_access_key_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE remote_backup_settings ADD COLUMN s3_secret_key TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE remote_backup_settings ADD COLUMN s3_path_prefix TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		Version:     "1.0.21",
		Description: "新增 WordPress 站点文件锁定开关",
		Func:        ensureFileLockEnabledColumn,
	},
}

func ensureFileLockEnabledColumn() error {
	var tableExists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'websites'`).Scan(&tableExists); err != nil {
		return err
	}
	if tableExists == 0 {
		return nil
	}
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'file_lock_enabled'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	_, err := DB.Exec(`ALTER TABLE websites ADD COLUMN file_lock_enabled INTEGER NOT NULL DEFAULT 0`)
	return err
}

func ensureDocumentRootSubdirColumn() error {
	var tableExists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'websites'`).Scan(&tableExists); err != nil {
		return err
	}
	if tableExists == 0 {
		return nil
	}
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'document_root_subdir'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	_, err := DB.Exec(`ALTER TABLE websites ADD COLUMN document_root_subdir TEXT NOT NULL DEFAULT ''`)
	return err
}

func ensureSSLLastErrorColumn() error {
	var tableExists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'websites'`).Scan(&tableExists); err != nil {
		return err
	}
	if tableExists == 0 {
		return nil
	}
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'ssl_last_error'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	_, err := DB.Exec(`ALTER TABLE websites ADD COLUMN ssl_last_error TEXT NOT NULL DEFAULT ''`)
	return err
}

func ensureCDNRealIPEnabledColumn() error {
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'cdn_realip_enabled'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	_, err := DB.Exec(`ALTER TABLE websites ADD COLUMN cdn_realip_enabled INTEGER NOT NULL DEFAULT 0`)
	return err
}

func ensureSSLExportEnabledColumn() error {
	var tableExists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'websites'`).Scan(&tableExists); err != nil {
		return err
	}
	if tableExists == 0 {
		return nil
	}
	var exists int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name = 'ssl_export_enabled'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	_, err := DB.Exec(`ALTER TABLE websites ADD COLUMN ssl_export_enabled INTEGER NOT NULL DEFAULT 0`)
	return err
}

// LatestVersion 返回 upgrades 列表中的最新版本号。
func LatestVersion() string {
	if len(upgrades) == 0 {
		return "1.0.0"
	}
	return upgrades[len(upgrades)-1].Version
}

// newInstallCanary 从 upgrades 列表中提取最后一条 ALTER TABLE ADD COLUMN 的表名和字段名，
// 用于判断数据库是否已包含最新 schema（新装检测的 canary 列）。
func newInstallCanary() (table, column string) {
	for i := len(upgrades) - 1; i >= 0; i-- {
		for _, sql := range upgrades[i].SQL {
			upper := strings.ToUpper(strings.TrimSpace(sql))
			if strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD COLUMN") {
				fields := strings.Fields(sql)
				// ALTER TABLE <table> ADD COLUMN <column> ...
				for j, f := range fields {
					if strings.ToUpper(f) == "TABLE" && j+1 < len(fields) {
						table = fields[j+1]
					}
					if strings.ToUpper(f) == "COLUMN" && j+1 < len(fields) {
						column = fields[j+1]
						if idx := strings.Index(column, "("); idx > 0 {
							column = column[:idx]
						}
					}
				}
				if table != "" && column != "" {
					return
				}
			}
		}
	}
	return "", ""
}

func isBetaVersion(v string) bool {
	return strings.Contains(strings.ToLower(v), "beta")
}

// RunUpgrades 执行所有尚未应用的版本升级。新装数据库已是最新版本，跳过所有升级。
func RunUpgrades() error {
	if DB == nil {
		return fmt.Errorf("数据库未初始化")
	}

	// 确保版本追踪表存在
	if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version    TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("创建 schema_version 表失败: %w", err)
	}

	// 查询当前版本
	var currentVersion string
	if err := DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC, rowid DESC LIMIT 1").Scan(&currentVersion); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("查询当前版本失败: %w", err)
	}

	// 新装检测：currentVersion 为空时，检查数据库是否已包含最新 schema。
	// migrations.go 已全量建表，若最新升级中的字段已存在则说明是新装，无需执行任何升级。
	if currentVersion == "" {
		if table, col := newInstallCanary(); col != "" {
			var exists int
			if err := DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", table, col).Scan(&exists); err != nil {
				return fmt.Errorf("检测数据库结构失败: %w", err)
			}
			if exists > 0 {
				log.Printf("[升级] 新装数据库，跳过所有升级步骤")
				if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES (?)", LatestVersion()); err != nil {
					return fmt.Errorf("记录新装版本失败: %w", err)
				}
				return nil
			}
		}
	}

	// Beta 版本归一化到 1.0.0 正式基线
	if currentVersion != "" && isBetaVersion(currentVersion) {
		log.Printf("[升级] beta 版本 %s 归一化到 1.0.0", currentVersion)
		if _, err := DB.Exec("DELETE FROM schema_version"); err != nil {
			log.Printf("[升级] 清理 beta 版本记录失败: %v", err)
		} else if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES ('1.0.0')"); err != nil {
			log.Printf("[升级] 写入归一化版本失败: %v", err)
		} else {
			currentVersion = "1.0.0"
		}
	}

	// 验证当前版本合法性：必须在 upgrades 列表中，或者是基线 1.0.0，或者是空（新装）
	if currentVersion != "" && currentVersion != "1.0.0" && currentVersion != LatestVersion() {
		found := false
		for _, u := range upgrades {
			if u.Version == currentVersion {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("未知数据库版本 %s，请先手动迁移到 1.0.0", currentVersion)
		}
	}

	// 基线 1.0.0 视为已应用所有旧升级，从 upgrades 第一条开始执行
	applied := currentVersion == "" || currentVersion == "1.0.0"

	for _, u := range upgrades {
		if !applied {
			if u.Version == currentVersion {
				applied = true
			}
			continue
		}

		log.Printf("[升级] 执行 %s: %s", u.Version, u.Description)

		for _, sql := range u.SQL {
			if _, err := DB.Exec(sql); err != nil {
				if strings.Contains(err.Error(), "duplicate column name") {
					log.Printf("[升级] %s: 字段已存在，跳过 (%s)", u.Version, strings.TrimSpace(sql))
					continue
				}
				return fmt.Errorf("升级 %s 失败: %w\nSQL: %s", u.Version, err, sql)
			}
		}

		fn := u.Func
		if fn == nil {
			fn = registeredFuncs[u.Version]
		}
		if fn != nil {
			if err := fn(); err != nil {
				return fmt.Errorf("升级 %s 函数迁移失败: %w", u.Version, err)
			}
		}

		if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES (?)", u.Version); err != nil {
			return fmt.Errorf("记录升级版本 %s 失败: %w", u.Version, err)
		}

		log.Printf("[升级] %s 完成", u.Version)
	}

	// 新装数据库：无任何版本记录，直接写入最新版本号，下次启动跳过所有升级
	var count int
	if err := DB.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count); err != nil {
		log.Printf("[升级] 查询版本记录失败: %v", err)
	}
	if count == 0 {
		if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES (?)", LatestVersion()); err != nil {
			return fmt.Errorf("记录新装版本失败: %w", err)
		}
	}

	return nil
}
