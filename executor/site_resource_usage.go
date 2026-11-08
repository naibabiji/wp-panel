package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/naibabiji/wp-panel/collector"
	"github.com/naibabiji/wp-panel/database"
)

// 仪表盘“网站资源”和 AI 诊断共用的网站近期资源占用，来自每分钟对象级采样。

const siteUsageWindow = 5 * time.Minute

// SiteResourceUsage 是某个网站最近 5 分钟的平均占用。
// CPU 为单核百分比（多核时可超过 100%）；Mem 为进程 RSS 占主机总内存的百分比（含共享内存）；
// ProcCount 为最近一次扫描中该网站系统用户的进程数。
type SiteResourceUsage struct {
	SiteID    int     `json:"site_id"`
	Domain    string  `json:"domain"`
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	ProcCount int     `json:"proc_count"`
}

var (
	siteUsageSamplingStart = collector.SamplingStartedAt
	siteUsageProcessCounts = collector.SiteProcessCounts
)

// RecentSiteResourceUsage 返回本次面板启动后、最近 5 分钟内有进程样本的网站，按 CPU、内存降序。
// 没有样本时返回空列表；查询失败返回错误。
func RecentSiteResourceUsage(now time.Time) ([]SiteResourceUsage, error) {
	db := database.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	samplingStart := siteUsageSamplingStart()
	if samplingStart.IsZero() {
		return []SiteResourceUsage{}, nil
	}
	start := now.Add(-siteUsageWindow)
	if samplingStart.After(start) {
		start = samplingStart
	}

	ctx, cancel := context.WithTimeout(context.Background(), attributionQueryTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT window_start, window_end, object_type, object_key, label, cpu_seconds, rss_bytes
		FROM resource_object_samples WHERE window_end > ? AND window_end <= ?`,
		start.UTC().Format("2006-01-02 15:04:05"), now.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	type siteAgg struct {
		label string
		cpu   float64
		rss   float64
	}
	windows := make(map[string]float64)
	sites := make(map[string]*siteAgg)
	for rows.Next() {
		var ws, we, objType, key, label string
		var cpu float64
		var rss int64
		if err := rows.Scan(&ws, &we, &objType, &key, &label, &cpu, &rss); err != nil {
			rows.Close()
			return nil, err
		}
		// W 由全部对象样本确定，网站只出现在部分分钟时分母仍为整个窗口。
		if _, seen := windows[we]; !seen {
			windows[we] = sampleWindowSeconds(ws, we)
		}
		if objType != collector.ObjectTypeSite {
			continue
		}
		agg := sites[key]
		if agg == nil {
			agg = &siteAgg{}
			sites[key] = agg
		}
		agg.label = label
		agg.cpu += cpu
		agg.rss += float64(rss)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := []SiteResourceUsage{}
	if len(windows) == 0 || len(sites) == 0 {
		return result, nil
	}
	var totalSeconds float64
	for _, seconds := range windows {
		totalSeconds += seconds
	}

	var memTotal sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT memory_total_bytes FROM monitoring_metrics ORDER BY id DESC LIMIT 1`).Scan(&memTotal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	counts := siteUsageProcessCounts()
	for key, agg := range sites {
		id, _ := strconv.Atoi(key)
		usage := SiteResourceUsage{SiteID: id, Domain: agg.label, ProcCount: counts[key]}
		if totalSeconds > 0 {
			usage.CPU = agg.cpu / totalSeconds * 100
		}
		if memTotal.Valid && memTotal.Int64 > 0 {
			usage.Mem = agg.rss / float64(len(windows)) / float64(memTotal.Int64) * 100
		}
		result = append(result, usage)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CPU != result[j].CPU {
			return result[i].CPU > result[j].CPU
		}
		if result[i].Mem != result[j].Mem {
			return result[i].Mem > result[j].Mem
		}
		return result[i].Domain < result[j].Domain
	})
	return result, nil
}
