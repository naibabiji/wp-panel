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
