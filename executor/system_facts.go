package executor

import (
	"runtime"

	"github.com/naibabiji/wp-panel/database"
)

// SystemFacts 是硬件规格自适应配置计算所需的基础事实：服务器总内存、CPU 核心数、
// 当前面板已建站点数。
//
// 采集策略：每次需要生成配置建议时实时 Collect，不做全局缓存——站点数量会随时间变化，
// 采集成本（一次 /proc/meminfo 读取 + 一条 COUNT(*) 查询）可以忽略不计。
// 同一次批量操作（例如未来重建全部站点配置）应该只 Collect 一次，在调用方内部传递复用，
// 不要在循环里对每个站点重复调用。
type SystemFacts struct {
	TotalMemoryBytes uint64
	CPUCores         int
	SiteCount        int
}

// CollectSystemFacts 采集当前系统事实。
func CollectSystemFacts() SystemFacts {
	return SystemFacts{
		TotalMemoryBytes: uint64(getTotalMemoryKB()) * 1024,
		CPUCores:         runtime.NumCPU(),
		SiteCount:        TotalSiteCount(),
	}
}

// TotalSiteCount 返回面板当前管理的站点总数（不区分状态）。
func TotalSiteCount() int {
	if database.DB == nil {
		return 0
	}
	var count int
	if err := database.DB.QueryRow("SELECT COUNT(*) FROM websites").Scan(&count); err != nil {
		return 0
	}
	return count
}
