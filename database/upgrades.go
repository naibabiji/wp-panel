// Package database — 版本升级机制说明
//
// 两个文件的分工：
//
//   - migrations.go   全量建表 + 种子数据，给全新安装用，始终代表数据库的最新状态。
//                     每次启动都会完整执行一遍（依赖 IF NOT EXISTS / OR IGNORE 保证幂等）。
//   - upgrades.go     增量升级步骤，给老版本升级用。仅在版本落后时执行一次。
//
// 日常开发流程：
//
//   1. 新功能需要数据库变更时（新表、新字段、新种子行等），在 upgrades.go 末尾追加一条 Upgrade。
//   2. 同时在 migrations.go 的对应位置添加等价的 CREATE / INSERT 语句——全新安装走 migrations，
//      升级安装走 upgrades，两条路径都要覆盖。
//   3. 发布正式版本后，upgrades.go 中属于该版本的条目可以删除；但 migrations.go 中的对应语句
//      需要永久保留（它们是新装数据库的起点）。
//
// 运行时逻辑（main.go 启动 → database.Open → RunMigrations → RunUpgrades）：
//
//   新装：  migrations 创建全部表 + 种子 → upgrades 发现版本表为空 → 跳过所有升级 → 写入最新版本号
//   升级：  migrations 幂等执行（无实际变化）→ upgrades 发现版本落后 → 逐条执行缺失的升级 → 更新版本号
//   已最新：migrations 幂等执行 → upgrades 发现版本已是最新 → 跳过
//
// 版本号约定：
//   使用语义化版本号（如 "1.0.0"），与 Git tag 保持一致。LatestVersion() 返回 upgrades 列表中
//   最后一条的版本号，即当前代码所代表的数据库版本。

package database

import (
	"fmt"
	"log"
	"strings"
)

// Upgrade 定义一次版本升级需要执行的数据库变更。
// SQL 中的语句应使用 IF NOT EXISTS / OR IGNORE 等幂等写法，确保重复执行安全。
type Upgrade struct {
	Version     string   // 目标版本号，如 "1.0.0"
	Description string   // 本次升级做了什么
	SQL         []string // 要执行的 SQL 语句
}

// upgrades 按版本顺序排列（旧→新）。每次正式发布后，可删除对应条目（但 migrations.go 中的等价语句需保留）。
var upgrades = []Upgrade{
	{
		Version:     "1.0.0",
		Description: "新增 Webhook 推送配置项",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('webhook_enabled', 'false', '是否启用 Webhook 推送')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('webhook_channel', 'wecom', '推送渠道')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('webhook_url', '', 'Webhook 推送地址')`,
		},
	},
	{
		Version:     "1.0.1",
		Description: "WordPress 优化字段：禁止自动更新、禁止文件编辑",
		SQL: []string{
			`ALTER TABLE websites ADD COLUMN disable_wp_updates INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE websites ADD COLUMN disable_file_editing INTEGER NOT NULL DEFAULT 0`,
		},
	},
	{
		Version:     "1.0.2",
		Description: "网站日志保留天数",
		SQL: []string{
			`ALTER TABLE websites ADD COLUMN log_retention_days INTEGER NOT NULL DEFAULT 7`,
		},
	},
	{
		Version:     "1.1.0-beta4",
		Description: "计划任务执行锁 + 远程备份失败告警",
		SQL: []string{
			`ALTER TABLE cron_jobs ADD COLUMN running INTEGER NOT NULL DEFAULT 0`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('alert_remote_backup', 'false', '远程备份失败告警（需先启用远程备份）')`,
		},
	},
	{
		Version:     "1.1.0-beta5",
		Description: "计划任务失败告警 + 网站不可用告警种子",
		SQL: []string{
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('alert_cron_fail', 'true', '计划任务执行失败告警')`,
			`INSERT OR IGNORE INTO security_settings (skey, svalue, description) VALUES ('alert_site', 'true', '网站不可用告警')`,
		},
	},
	{
		Version:     "1.1.0-beta6",
		Description: "计划任务文件备份保留份数",
		SQL: []string{
			`ALTER TABLE cron_jobs ADD COLUMN keep_count INTEGER NOT NULL DEFAULT 3`,
		},
	},
	{
		Version:     "1.1.0-beta7",
		Description: "补充计划任务keep_count列（修正beta6遗漏）",
		SQL: []string{
			`ALTER TABLE cron_jobs ADD COLUMN keep_count INTEGER NOT NULL DEFAULT 3`,
		},
	},
}

// LatestVersion 返回 upgrades 列表中的最新版本号。
func LatestVersion() string {
	if len(upgrades) == 0 {
		return "1.0.0"
	}
	return upgrades[len(upgrades)-1].Version
}

// newInstallColumn 从 upgrades 列表中提取最后一条 ALTER TABLE ADD COLUMN 的字段名，
// 用于判断数据库是否已包含最新 schema（新装检测的 canary 列）。
func newInstallColumn() string {
	for i := len(upgrades) - 1; i >= 0; i-- {
		for _, sql := range upgrades[i].SQL {
			upper := strings.ToUpper(strings.TrimSpace(sql))
			if strings.HasPrefix(upper, "ALTER TABLE") && strings.Contains(upper, "ADD COLUMN") {
				fields := strings.Fields(sql)
				for j, f := range fields {
					if strings.ToUpper(f) == "COLUMN" && j+1 < len(fields) {
						col := fields[j+1]
						if idx := strings.Index(col, "("); idx > 0 {
							col = col[:idx]
						}
						return col
					}
				}
			}
		}
	}
	return ""
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
	DB.QueryRow("SELECT version FROM schema_version ORDER BY updated_at DESC LIMIT 1").Scan(&currentVersion)

	// 新装检测：currentVersion 为空时，检查数据库是否已包含最新 schema。
	// migrations.go 已全量建表，若最新升级中的字段已存在则说明是新装，无需执行任何升级。
	if currentVersion == "" {
		if col := newInstallColumn(); col != "" {
			var exists int
			DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('websites') WHERE name=?", col).Scan(&exists)
			if exists > 0 {
				log.Printf("[升级] 新装数据库，跳过所有升级步骤")
				DB.Exec("INSERT INTO schema_version (version) VALUES (?)", LatestVersion())
				return nil
			}
		}
	}

	// currentVersion 为空且非新装 → 旧版本数据库（无版本记录），从第一个升级开始执行。
	// currentVersion 非空 → 已记录版本，从下一条升级开始执行。
	applied := currentVersion == ""

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

		if _, err := DB.Exec("INSERT INTO schema_version (version) VALUES (?)", u.Version); err != nil {
			return fmt.Errorf("记录升级版本 %s 失败: %w", u.Version, err)
		}

		log.Printf("[升级] %s 完成", u.Version)
	}

	// 新装数据库：无任何版本记录，直接写入最新版本号，下次启动跳过所有升级
	var count int
	DB.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count)
	if count == 0 {
		DB.Exec("INSERT INTO schema_version (version) VALUES (?)", LatestVersion())
	}

	return nil
}
