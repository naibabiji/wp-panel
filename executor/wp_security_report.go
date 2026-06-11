package executor

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type WPSecurityReportItem struct {
	IPAddress      string   `json:"ip_address"`
	Domain         string   `json:"domain"`
	RiskLevel      string   `json:"risk_level"`
	Recommendation string   `json:"recommendation"`
	FirstSeen      string   `json:"first_seen"`
	LastSeen       string   `json:"last_seen"`
	EventCount     int      `json:"event_count"`
	SamplePaths    []string `json:"sample_paths"`
	Evidence       []string `json:"evidence"`
	CopyText       string   `json:"copy_text"`
}

type wpSecuritySite struct {
	ID     int
	Domain string
	LogDir string
}

type wpSecurityAggregate struct {
	ip        string
	domain    string
	firstSeen time.Time
	lastSeen  time.Time
	events    int
	paths     map[string]int
	evidence  map[string]bool
}

var (
	combinedLogRe = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "([A-Z]+) ([^" ]+) [^"]*" ([0-9]{3})`)
	nginxErrorRe  = regexp.MustCompile(`client: ([^,]+), server: ([^,]+), request: "([A-Z]+) ([^" ]+) [^"]*"`)

	wpSecurityReportCacheMu sync.Mutex
	wpSecurityReportCache   = map[int]wpSecurityReportCacheEntry{}
)

type wpSecurityReportCacheEntry struct {
	createdAt time.Time
	items     []WPSecurityReportItem
}

func BuildWPSecurityReport(limit int) ([]WPSecurityReportItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	if items, ok := getWPSecurityReportCache(limit); ok {
		return items, nil
	}

	sites, err := listWordPressSecuritySites(database.GetDB())
	if err != nil {
		return nil, err
	}

	aggregates := map[string]*wpSecurityAggregate{}
	for _, site := range sites {
		if !isAllowedSiteLogDir(site.LogDir) {
			continue
		}
		readWPSecurityLog(site, filepath.Join(site.LogDir, "wp-security.log"), aggregates)
		readNginxErrorLog(site, filepath.Join(site.LogDir, "error.log"), aggregates)
	}

	items := make([]WPSecurityReportItem, 0, len(aggregates))
	for _, agg := range aggregates {
		items = append(items, buildWPSecurityReportItem(agg))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].RiskLevel != items[j].RiskLevel {
			return riskWeight(items[i].RiskLevel) > riskWeight(items[j].RiskLevel)
		}
		if items[i].EventCount != items[j].EventCount {
			return items[i].EventCount > items[j].EventCount
		}
		return items[i].LastSeen > items[j].LastSeen
	})
	if len(items) > limit {
		items = items[:limit]
	}
	setWPSecurityReportCache(limit, items)
	return items, nil
}

func getWPSecurityReportCache(limit int) ([]WPSecurityReportItem, bool) {
	wpSecurityReportCacheMu.Lock()
	defer wpSecurityReportCacheMu.Unlock()

	entry, ok := wpSecurityReportCache[limit]
	if !ok || time.Since(entry.createdAt) > 30*time.Second {
		return nil, false
	}
	return cloneWPSecurityReportItems(entry.items), true
}

func setWPSecurityReportCache(limit int, items []WPSecurityReportItem) {
	wpSecurityReportCacheMu.Lock()
	defer wpSecurityReportCacheMu.Unlock()

	wpSecurityReportCache[limit] = wpSecurityReportCacheEntry{
		createdAt: time.Now(),
		items:     cloneWPSecurityReportItems(items),
	}
}

func cloneWPSecurityReportItems(items []WPSecurityReportItem) []WPSecurityReportItem {
	out := make([]WPSecurityReportItem, len(items))
	for i, item := range items {
		out[i] = item
		out[i].SamplePaths = append([]string(nil), item.SamplePaths...)
		out[i].Evidence = append([]string(nil), item.Evidence...)
	}
	return out
}

func listWordPressSecuritySites(db *sql.DB) ([]wpSecuritySite, error) {
	rows, err := db.Query(`SELECT id, domain, log_dir FROM websites WHERE site_type = 'wordpress' ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []wpSecuritySite
	for rows.Next() {
		var site wpSecuritySite
		if err := rows.Scan(&site.ID, &site.Domain, &site.LogDir); err == nil {
			sites = append(sites, site)
		}
	}
	return sites, rows.Err()
}

func readWPSecurityLog(site wpSecuritySite, path string, aggregates map[string]*wpSecurityAggregate) {
	for _, line := range tailLogLines(path, 1000) {
		m := combinedLogRe.FindStringSubmatch(line)
		if len(m) != 6 {
			continue
		}
		status, _ := strconv.Atoi(m[5])
		seen := parseNginxAccessTime(m[2])
		uri := normalizeLoggedURI(m[4])
		reason := "WordPress 异常文件路径访问"
		if strings.HasSuffix(strings.ToLower(uri), ".php") && status == 404 {
			reason = "不存在 PHP 文件探测"
		}
		addWPSecurityEvent(aggregates, site.Domain, strings.TrimSpace(m[1]), seen, m[3], uri, status, reason)
	}
}

func readNginxErrorLog(site wpSecuritySite, path string, aggregates map[string]*wpSecurityAggregate) {
	for _, line := range tailLogLines(path, 1000) {
		m := nginxErrorRe.FindStringSubmatch(line)
		if len(m) != 5 {
			continue
		}
		reason := ""
		switch {
		case strings.Contains(line, "Primary script unknown"):
			reason = "Primary script unknown：不存在 PHP 文件进入 PHP-FPM"
		case strings.Contains(line, "open()") && strings.Contains(line, "failed (2: No such file or directory)"):
			reason = "open() failed：不存在文件访问"
		default:
			continue
		}
		addWPSecurityEvent(aggregates, site.Domain, strings.TrimSpace(m[1]), parseNginxErrorTime(line), m[3], normalizeLoggedURI(m[4]), 0, reason)
	}
}

func addWPSecurityEvent(aggregates map[string]*wpSecurityAggregate, domain, ip string, seen time.Time, method, uri string, status int, reason string) {
	if ip == "" || uri == "" {
		return
	}
	key := domain + "|" + ip
	agg := aggregates[key]
	if agg == nil {
		agg = &wpSecurityAggregate{
			ip:       ip,
			domain:   domain,
			paths:    map[string]int{},
			evidence: map[string]bool{},
		}
		aggregates[key] = agg
	}
	agg.events++
	if !seen.IsZero() {
		if agg.firstSeen.IsZero() || seen.Before(agg.firstSeen) {
			agg.firstSeen = seen
		}
		if agg.lastSeen.IsZero() || seen.After(agg.lastSeen) {
			agg.lastSeen = seen
		}
	}
	agg.paths[method+" "+uri]++
	if status > 0 {
		agg.evidence[fmt.Sprintf("%s（HTTP %d）", reason, status)] = true
	} else {
		agg.evidence[reason] = true
	}
}

func buildWPSecurityReportItem(agg *wpSecurityAggregate) WPSecurityReportItem {
	paths := topSecurityPaths(agg.paths, 8)
	evidence := sortedEvidence(agg.evidence, 6)
	risk := classifyWPSecurityRisk(agg, paths, evidence)
	item := WPSecurityReportItem{
		IPAddress:      agg.ip,
		Domain:         agg.domain,
		RiskLevel:      risk,
		Recommendation: recommendationForRisk(risk),
		FirstSeen:      formatReportTime(agg.firstSeen),
		LastSeen:       formatReportTime(agg.lastSeen),
		EventCount:     agg.events,
		SamplePaths:    paths,
		Evidence:       evidence,
	}
	item.CopyText = buildWPSecurityCopyText(item)
	return item
}

func classifyWPSecurityRisk(agg *wpSecurityAggregate, paths, evidence []string) string {
	if agg.events >= 10 || hasHighSignalPath(paths) || hasEvidence(evidence, "Primary script unknown") {
		return "高"
	}
	if agg.events >= 3 {
		return "中"
	}
	return "低"
}

func recommendationForRisk(risk string) string {
	switch risk {
	case "高":
		return "建议管理员结合 IP 来源确认后手动封禁"
	case "中":
		return "建议观察或结合 IP 信息判断"
	default:
		return "建议先观察"
	}
}

func buildWPSecurityCopyText(item WPSecurityReportItem) string {
	return fmt.Sprintf(`IP: %s
站点: %s
时间: %s - %s
事件次数: %d
风险等级: %s
访问路径样本:
%s
错误/证据:
%s
面板建议: %s
说明: 面板仅做本地日志统计，未自动封禁。请管理员结合 IP 来源、业务访问情况和 AI/IP 查询工具综合判断。`,
		item.IPAddress,
		item.Domain,
		defaultIfEmpty(item.FirstSeen, "未知"),
		defaultIfEmpty(item.LastSeen, "未知"),
		item.EventCount,
		item.RiskLevel,
		formatReportList(item.SamplePaths),
		formatReportList(item.Evidence),
		item.Recommendation,
	)
}

func topSecurityPaths(paths map[string]int, limit int) []string {
	type kv struct {
		Path  string
		Count int
	}
	items := make([]kv, 0, len(paths))
	for path, count := range paths {
		items = append(items, kv{Path: path, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Path < items[j].Path
	})
	out := make([]string, 0, minInt(limit, len(items)))
	for i := 0; i < len(items) && i < limit; i++ {
		out = append(out, fmt.Sprintf("%s × %d", items[i].Path, items[i].Count))
	}
	return out
}

func sortedEvidence(evidence map[string]bool, limit int) []string {
	out := make([]string, 0, len(evidence))
	for e := range evidence {
		out = append(out, e)
	}
	sort.Strings(out)
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func hasHighSignalPath(paths []string) bool {
	needles := []string{"phpinfo", "config", "database", ".env", "test.php", "phptest.php", "settings.php", "wp-config"}
	for _, path := range paths {
		lower := strings.ToLower(path)
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				return true
			}
		}
	}
	return false
}

func hasEvidence(evidence []string, needle string) bool {
	for _, item := range evidence {
		if strings.Contains(item, needle) {
			return true
		}
	}
	return false
}

func normalizeLoggedURI(raw string) string {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Path == "" {
		return raw
	}
	return parsed.RequestURI()
}

func parseNginxAccessTime(raw string) time.Time {
	t, _ := time.Parse("02/Jan/2006:15:04:05 -0700", raw)
	return t
}

func parseNginxErrorTime(line string) time.Time {
	if len(line) < len("2006/01/02 15:04:05") {
		return time.Time{}
	}
	t, _ := time.ParseInLocation("2006/01/02 15:04:05", line[:19], time.Local)
	return t
}

func tailLogLines(path string, maxLines int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() <= 0 {
		return nil
	}
	const maxBytes int64 = 512 * 1024
	start := int64(0)
	if info.Size() > maxBytes {
		start = info.Size() - maxBytes
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && len(buf) == 0 {
		return nil
	}
	lines := strings.Split(string(buf), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func isAllowedSiteLogDir(logDir string) bool {
	clean := filepath.Clean(logDir)
	root := filepath.Clean("/www/wwwlogs")
	return clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator))
}

func formatReportTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func formatReportList(items []string) string {
	if len(items) == 0 {
		return "- 无"
	}
	return "- " + strings.Join(items, "\n- ")
}

func defaultIfEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func riskWeight(risk string) int {
	switch risk {
	case "高":
		return 3
	case "中":
		return 2
	default:
		return 1
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
