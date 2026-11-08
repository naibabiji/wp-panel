package executor

import (
	"context"
	"fmt"
	"log"
	"math"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/collector"
	"github.com/naibabiji/wp-panel/database"
)

// 资源告警的初步归因：根据每分钟对象级采样回答“先查谁、查哪个时间段”。
// 结果只追加在通知正文和 alert_log.attribution 中，不改变原告警内容，
// 生成失败时返回空字符串，告警照常发送。

const (
	attributionQueryTimeout   = 2 * time.Second
	attributionClueWindow     = 5 * time.Minute
	attributionMinCoverage    = 0.6
	attributionHighShare      = 0.6
	attributionHighLead       = 2.0
	attributionHighHostShare  = 0.5
	attributionLikelyShare    = 0.4
	attributionLikelyLead     = 1.3
	attributionIOWaitClue     = 20.0
	attributionStealClue      = 10.0
	attributionLogMarginMins  = 5
	attributionTopListLength  = 3
	attributionDisclaimerText = "说明：这是资源占用归因，不代表已确认根因"
	attributionRSSCaveat      = "进程 RSS 线索，可能重复计算共享内存，不等于独占内存"
)

const diskAlertChecklist = "未执行目录占用统计。建议优先检查网站日志目录 /www/wwwlogs、面板备份目录和各网站上传目录（通用检查清单，不是归因结论）。"

type objectUsage struct {
	objType    string
	key        string
	label      string
	cpuSeconds float64
	rssSum     int64
	samples    int
}

type attributionData struct {
	start, end     time.Time
	objects        []*objectUsage
	windows        int
	coveredSeconds float64
	hostCPUSeconds float64
	hostMetrics    int
	avgIOWait      float64
	avgSteal       float64
}

var (
	attributionCPUCount = runtime.NumCPU
	lookupPIDObject     = collector.LookupPIDObject
)

// 主机指标与对象样本由同一次采集写入，时间戳最多相差几秒；在此范围内视为同一分钟。
const attributionHostMatchTolerance = 30 * time.Second

// buildAlertAttribution 为告警规则循环生成归因文字。ongoing 表示持续中重发。
func buildAlertAttribution(key string, pendingSince, now time.Time, ongoing bool) string {
	switch key {
	case "alert_disk":
		return diskAlertChecklist
	case "alert_cpu", "alert_memory":
		start := pendingSince
		if ongoing || start.IsZero() || !start.Before(now) {
			start = now.Add(-attributionClueWindow)
		}
		data, err := loadAttributionData(start, now)
		if err != nil {
			log.Printf("[告警归因] %s 归因查询失败: %v", key, err)
			return ""
		}
		if key == "alert_cpu" {
			return formatCPUAttribution(data, ongoing)
		}
		return formatMemoryClue(data, "内存线索", ongoing)
	case "alert_service":
		return serviceIncidentClue(now)
	}
	return ""
}

func serviceIncidentClue(now time.Time) string {
	data, err := loadAttributionData(now.Add(-attributionClueWindow), now)
	if err != nil {
		log.Printf("[告警归因] 服务异常线索查询失败: %v", err)
		return ""
	}
	return formatServiceClue(data)
}

// buildOOMAttribution 给出被终止进程所属网站以及事件前 5 分钟的内存线索。
func buildOOMAttribution(pid int, occurredAt time.Time) string {
	var lines []string
	if obj, ok := lookupPIDObject(pid); ok {
		if obj.Type == collector.ObjectTypeSite {
			lines = append(lines, "被终止进程所属网站："+obj.Label)
		} else {
			name := objectDisplayName(&objectUsage{objType: obj.Type, label: obj.Label})
			lines = append(lines, "被终止进程属于"+name+"，不属于单个网站，不能据此判断是否与某网站有关")
		}
	} else {
		lines = append(lines, "被终止进程未能与最近采样匹配，不能据此判断是否与某网站有关")
	}
	data, err := loadAttributionData(occurredAt.Add(-attributionClueWindow), occurredAt)
	if err != nil {
		log.Printf("[告警归因] OOM 内存线索查询失败: %v", err)
	} else if clue := formatMemoryClue(data, "事件前内存线索", false); clue != "" {
		lines = append(lines, clue)
	}
	return strings.Join(lines, "\n")
}

func loadAttributionData(start, end time.Time) (*attributionData, error) {
	db := database.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), attributionQueryTimeout)
	defer cancel()
	startText := start.UTC().Format("2006-01-02 15:04:05")
	endText := end.UTC().Format("2006-01-02 15:04:05")

	rows, err := db.QueryContext(ctx, `SELECT window_start, window_end, object_type, object_key, label, cpu_seconds, rss_bytes
		FROM resource_object_samples WHERE window_end > ? AND window_end <= ?`, startText, endText)
	if err != nil {
		return nil, err
	}
	data := &attributionData{start: start, end: end}
	byKey := make(map[string]*objectUsage)
	windows := make(map[string]float64)
	for rows.Next() {
		var ws, we, objType, key, label string
		var cpu float64
		var rss int64
		if err := rows.Scan(&ws, &we, &objType, &key, &label, &cpu, &rss); err != nil {
			rows.Close()
			return nil, err
		}
		if _, seen := windows[we]; !seen {
			windows[we] = sampleWindowSeconds(ws, we)
		}
		mapKey := objType + "\x00" + key
		u := byKey[mapKey]
		if u == nil {
			u = &objectUsage{objType: objType, key: key}
			byKey[mapKey] = u
		}
		u.label = label // 以窗口内最新的显示名为准
		u.cpuSeconds += cpu
		u.rssSum += rss
		u.samples++
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, u := range byKey {
		data.objects = append(data.objects, u)
	}
	data.windows = len(windows)
	for _, seconds := range windows {
		data.coveredSeconds += seconds
	}

	// 主机指标：I/O 等待与 steal 是整个时间段的环境线索；用于“高可信”判断的
	// 主机 CPU 用量只按与对象样本同一分钟的指标累加，保证分子分母口径一致。
	hostRows, err := db.QueryContext(ctx, `SELECT recorded_at, COALESCE(cpu_percent, 0), cpu_iowait_percent, cpu_steal_percent
		FROM monitoring_metrics WHERE recorded_at > ? AND recorded_at <= ?`,
		startText, end.Add(attributionHostMatchTolerance).UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	type hostSample struct {
		at  time.Time
		cpu float64
	}
	var hosts []hostSample
	var sumIOWait, sumSteal float64
	for hostRows.Next() {
		var recorded string
		var cpu, iowait, steal float64
		if err := hostRows.Scan(&recorded, &cpu, &iowait, &steal); err != nil {
			hostRows.Close()
			return nil, err
		}
		at, ok := parseAttributionTime(recorded)
		if !ok {
			continue
		}
		hosts = append(hosts, hostSample{at: at, cpu: cpu})
		if !at.After(end.UTC()) {
			data.hostMetrics++
			sumIOWait += iowait
			sumSteal += steal
		}
	}
	if err := hostRows.Close(); err != nil {
		return nil, err
	}
	if err := hostRows.Err(); err != nil {
		return nil, err
	}
	if data.hostMetrics > 0 {
		data.avgIOWait = sumIOWait / float64(data.hostMetrics)
		data.avgSteal = sumSteal / float64(data.hostMetrics)
	}
	cores := float64(attributionCPUCount())
	for we, seconds := range windows {
		windowEnd, ok := parseAttributionTime(we)
		if !ok {
			continue
		}
		for _, h := range hosts {
			if diff := h.at.Sub(windowEnd); diff >= -attributionHostMatchTolerance && diff <= attributionHostMatchTolerance {
				data.hostCPUSeconds += h.cpu / 100 * cores * seconds
				break
			}
		}
	}
	return data, nil
}

// parseAttributionTime 解析 SQLite 中的 UTC 时间；驱动按列类型可能返回
// “2006-01-02 15:04:05”或 RFC3339 两种格式。
func parseAttributionTime(value string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func sampleWindowSeconds(start, end string) float64 {
	s, ok1 := parseAttributionTime(start)
	e, ok2 := parseAttributionTime(end)
	if !ok1 || !ok2 || !e.After(s) {
		return 60
	}
	return e.Sub(s).Seconds()
}

func expectedWindows(start, end time.Time) int {
	n := int(math.Round(end.Sub(start).Minutes()))
	if n < 1 {
		n = 1
	}
	return n
}

func (d *attributionData) sufficient() bool {
	return d.windows > 0 && float64(d.windows) >= math.Ceil(attributionMinCoverage*float64(expectedWindows(d.start, d.end)))
}

// rankedByCPU 返回可排名的对象（不含合并的其余进程），以及已统计 CPU 总量（含其余进程）。
func (d *attributionData) rankedByCPU() ([]*objectUsage, float64) {
	var total float64
	var ranked []*objectUsage
	for _, u := range d.objects {
		total += u.cpuSeconds
		if u.objType == collector.ObjectTypeOther && u.key == collector.OtherRestKey {
			continue
		}
		if u.cpuSeconds > 0 {
			ranked = append(ranked, u)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].cpuSeconds != ranked[j].cpuSeconds {
			return ranked[i].cpuSeconds > ranked[j].cpuSeconds
		}
		return ranked[i].label < ranked[j].label
	})
	return ranked, total
}

func (d *attributionData) rankedByRSS() []*objectUsage {
	var ranked []*objectUsage
	for _, u := range d.objects {
		if u.objType == collector.ObjectTypeOther && u.key == collector.OtherRestKey {
			continue
		}
		if u.rssSum > 0 && u.samples > 0 {
			ranked = append(ranked, u)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		ai, aj := ranked[i].avgRSS(), ranked[j].avgRSS()
		if ai != aj {
			return ai > aj
		}
		return ranked[i].label < ranked[j].label
	})
	return ranked
}

func (u *objectUsage) avgRSS() int64 {
	if u.samples == 0 {
		return 0
	}
	return u.rssSum / int64(u.samples)
}

type cpuVerdict int

const (
	verdictInsufficient cpuVerdict = iota
	verdictHigh
	verdictLikely
	verdictUnattributed
)

func classifyCPU(d *attributionData) (cpuVerdict, []*objectUsage, float64) {
	ranked, total := d.rankedByCPU()
	if !d.sufficient() || total <= 0 || len(ranked) == 0 {
		return verdictInsufficient, ranked, total
	}
	s1 := ranked[0].cpuSeconds
	var s2 float64
	if len(ranked) > 1 {
		s2 = ranked[1].cpuSeconds
	}
	share := s1 / total
	hostOK := d.hostCPUSeconds > 0 && s1 >= attributionHighHostShare*d.hostCPUSeconds
	if share >= attributionHighShare && s1 >= attributionHighLead*s2 && hostOK {
		return verdictHigh, ranked, total
	}
	if share >= attributionLikelyShare && s1 >= attributionLikelyLead*s2 {
		return verdictLikely, ranked, total
	}
	return verdictUnattributed, ranked, total
}

func formatCPUAttribution(d *attributionData, ongoing bool) string {
	verdict, ranked, total := classifyCPU(d)
	envClues := environmentClues(d)
	var lines []string
	window := "时间段：" + formatAttributionWindow(d.start, d.end) + "（服务器本地时间）"
	if ongoing {
		window = "时间段（当前窗口）：" + formatAttributionWindow(d.start, d.end) + "（服务器本地时间）"
	}

	switch verdict {
	case verdictInsufficient:
		lines = append(lines, "初步归因：未知（采样不足）", window)
		lines = append(lines, envClues...)
		lines = append(lines, "建议：在资源趋势中扩大时间范围，人工检查该时间段的网站与系统日志")
	case verdictUnattributed:
		lines = append(lines, "初步归因：无法归因到单个网站", window)
		lines = append(lines, envClues...)
		lines = append(lines, "证据：占用前 3："+formatCPUTopList(ranked, total))
		lines = append(lines, "建议：按上述顺序检查各对象同一时间段的日志")
	default:
		top := ranked[0]
		confidence := "高"
		if verdict == verdictLikely {
			confidence = "可能相关"
		}
		lines = append(lines, fmt.Sprintf("初步归因：%s（可信度：%s）", objectDisplayName(top), confidence), window)
		evidence := fmt.Sprintf("证据：%s 占已统计 CPU 的 %s", objectDisplayName(top), formatShare(top.cpuSeconds, total))
		if len(ranked) > 1 {
			evidence += fmt.Sprintf("，第二名 %s 占 %s", objectDisplayName(ranked[1]), formatShare(ranked[1].cpuSeconds, total))
		} else {
			evidence += "（未采到其他占用对象）"
		}
		lines = append(lines, evidence)
		for _, clue := range envClues {
			lines = append(lines, "附加"+clue)
		}
		lines = append(lines, objectSuggestion(top, d.start, d.end))
	}
	lines = append(lines, attributionDisclaimerText)
	return strings.Join(lines, "\n")
}

func formatMemoryClue(d *attributionData, title string, ongoing bool) string {
	window := formatAttributionWindow(d.start, d.end)
	if ongoing {
		window = "当前窗口 " + window
	}
	if !d.sufficient() {
		return title + "：采样不足（" + window + "）"
	}
	ranked := d.rankedByRSS()
	if len(ranked) == 0 {
		return title + "：采样不足（" + window + "）"
	}
	var items []string
	for i := 0; i < len(ranked) && i < attributionTopListLength; i++ {
		items = append(items, objectDisplayName(ranked[i])+" 约 "+formatIncidentBytes(ranked[i].avgRSS()))
	}
	return fmt.Sprintf("%s（%s 进程内存均值，%s）：%s\n建议：优先检查上述对象；网站进程内存偏高时，查看该站同时段的访问日志与 PHP 错误日志",
		title, window, attributionRSSCaveat, strings.Join(items, "、"))
}

func formatServiceClue(d *attributionData) string {
	if !d.sufficient() {
		return ""
	}
	ranked, total := d.rankedByCPU()
	var parts []string
	if len(ranked) > 0 && total > 0 {
		parts = append(parts, "CPU 占用最多："+formatCPUTopList(ranked, total))
	}
	if byRSS := d.rankedByRSS(); len(byRSS) > 0 {
		var items []string
		for i := 0; i < len(byRSS) && i < attributionTopListLength; i++ {
			items = append(items, objectDisplayName(byRSS[i])+" 约 "+formatIncidentBytes(byRSS[i].avgRSS()))
		}
		parts = append(parts, "内存占用最多："+strings.Join(items, "、"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "线索：异常前 5 分钟（" + formatAttributionWindow(d.start, d.end) + "）" + strings.Join(parts, "；") + "。仅为线索，不代表异常原因"
}

func environmentClues(d *attributionData) []string {
	if d.hostMetrics == 0 {
		return nil
	}
	var clues []string
	if d.avgIOWait >= attributionIOWaitClue {
		clues = append(clues, fmt.Sprintf("线索：同时段 I/O 等待均值 %.0f%%，可能有磁盘延迟影响", d.avgIOWait))
	}
	if d.avgSteal >= attributionStealClue {
		clues = append(clues, fmt.Sprintf("线索：同时段宿主机 CPU 争用（steal）均值 %.0f%%", d.avgSteal))
	}
	return clues
}

func formatCPUTopList(ranked []*objectUsage, total float64) string {
	var items []string
	for i := 0; i < len(ranked) && i < attributionTopListLength; i++ {
		items = append(items, objectDisplayName(ranked[i])+" "+formatShare(ranked[i].cpuSeconds, total))
	}
	return strings.Join(items, "、")
}

func formatShare(part, total float64) string {
	if total <= 0 || part <= 0 {
		return "0%"
	}
	share := part / total * 100
	if share < 0.5 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", share)
}

func objectDisplayName(u *objectUsage) string {
	switch u.objType {
	case collector.ObjectTypeSite:
		return u.label
	case collector.ObjectTypeService:
		return "公共服务 " + u.label
	default:
		return "进程 " + u.label
	}
}

func objectSuggestion(u *objectUsage, start, end time.Time) string {
	margin := attributionLogMarginMins * time.Minute
	logWindow := formatAttributionWindow(start.Add(-margin), end.Add(margin))
	switch u.objType {
	case collector.ObjectTypeSite:
		return "建议：查看该网站 " + logWindow + " 的访问日志、PHP 错误日志和 WP Cron"
	case collector.ObjectTypeService:
		return "建议：检查该服务 " + logWindow + " 的日志；公共服务的占用可能由某个网站的请求或查询引起，需结合各网站同一时间段的日志判断"
	default:
		return "建议：确认该进程在 " + logWindow + " 执行的任务（如备份、计划任务）"
	}
}

func formatAttributionWindow(start, end time.Time) string {
	s, e := start.Local(), end.Local()
	if s.Year() == e.Year() && s.YearDay() == e.YearDay() {
		return s.Format("2006-01-02 15:04") + "–" + e.Format("15:04")
	}
	return s.Format("2006-01-02 15:04") + "–" + e.Format("2006-01-02 15:04")
}
