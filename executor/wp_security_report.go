package executor

import (
	"database/sql"
	"fmt"
	"net"
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

// WordPress 安全事件类型（只记录、不拦截）
const (
	SecurityEventSensitiveFileScan = "sensitive_file_scan"
	SecurityEventSQLiProbe         = "sqli_probe"
	SecurityEventFakeSearchBot     = "fake_search_bot"
	SecurityEventSuspiciousPHP     = "suspicious_php"
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
	Types          []string `json:"types"`
	CopyText       string   `json:"copy_text"`
}

type wpSecuritySite struct {
	ID     int
	Domain string
	LogDir string
}

type wpSecurityAggregate struct {
	ip         string
	domain     string
	firstSeen  time.Time
	lastSeen   time.Time
	events     int
	paths      map[string]int
	evidence   map[string]bool
	eventTypes map[string]bool
	riskLevel  string
}

type searchBotIPChecker struct {
	googlebot []string
	bingbot   []string
}

var (
	// combined 日志格式：$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
	combinedLogRe = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "([A-Z]+) ([^" ]+) [^"]*" ([0-9]{3}) \S+ "[^"]*" "([^"]*)"`)
	nginxErrorRe  = regexp.MustCompile(`client: ([^,]+), server: ([^,]+), request: "([A-Z]+) ([^" ]+) [^"]*"`)

	// 保守的 SQL 注入探测正则，只记录不拦截；误报率优先于漏报率
	sqliProbePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)union\s+select`),
		regexp.MustCompile(`(?i)sleep\s*\(`),
		regexp.MustCompile(`(?i)benchmark\s*\(`),
		regexp.MustCompile(`(?i)information_schema`),
		regexp.MustCompile(`(?i)\bxp_cmdshell\b`),
		regexp.MustCompile(`(?i)\bwaitfor\s+delay\b`),
		regexp.MustCompile(`(?i)\bload_file\s*\(`),
		regexp.MustCompile(`(?i)\binto\s+outfile\b`),
		// 兼容 ' OR 1=1 和经典的 ' OR '1'='1（等号两侧都带引号）两种写法
		regexp.MustCompile(`(?i)(?:'|%27)\s*or\s*(?:'|%27)?\s*[0-9]+\s*(?:'|%27)?\s*=\s*(?:'|%27)?\s*[0-9]+`),
		regexp.MustCompile(`(?i)0x[0-9a-f]{16,}`),
		regexp.MustCompile(`(?i)(?:;|%3b)\s*(?:drop|insert|update|delete)\s+`),
	}

	// 敏感文件扫描路径特征（与 fail2ban filter 保持一致）
	sensitiveFilePatterns = []string{
		".env", ".git", "config.bak", "wp-config.php", ".sql", ".tar", ".gz", ".zip",
		".old", ".swp", ".save", ".DS_Store",
	}

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

	checker := newSearchBotIPChecker(database.GetDB())
	aggregates := map[string]*wpSecurityAggregate{}
	for _, site := range sites {
		if !wpSecurityLogDirAllowed(site.LogDir) {
			continue
		}
		readWPSecurityLog(site, filepath.Join(site.LogDir, "wp-security.log"), aggregates, checker)
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
		// 注意：append(nil, x...) 在 x 为空切片时会返回 nil 而不是空切片，
		// JSON 序列化后前端拿到的就是 null。Types 对没有命中 4 类明确分类的
		// IP（只有兜底的"WordPress 异常路径访问"）是常见的空值场景，
		// 必须显式保留为非 nil 的空切片，否则前端 x-for 遍历 null 会报错。
		out[i].Types = append([]string{}, item.Types...)
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

func readWPSecurityLog(site wpSecuritySite, path string, aggregates map[string]*wpSecurityAggregate, checker *searchBotIPChecker) {
	for _, line := range tailLogLines(path, 1000) {
		m := combinedLogRe.FindStringSubmatch(line)
		if len(m) != 7 {
			continue
		}
		status, _ := strconv.Atoi(m[5])
		seen := parseNginxAccessTime(m[2])
		uri := normalizeLoggedURI(m[4])
		ua := strings.TrimSpace(m[6])
		ip := strings.TrimSpace(m[1])
		method := m[3]

		eventType, risk, message := classifySecurityEvent(method, uri, ua, ip, status, checker)
		addWPSecurityEvent(aggregates, site.Domain, ip, seen, method, uri, status, message, eventType, risk)
	}
}

func readNginxErrorLog(site wpSecuritySite, path string, aggregates map[string]*wpSecurityAggregate) {
	for _, line := range tailLogLines(path, 1000) {
		m := nginxErrorRe.FindStringSubmatch(line)
		if len(m) != 5 {
			continue
		}
		reqPath := normalizeLoggedURI(m[4])
		reason := ""
		eventType := ""
		risk := "low"
		switch {
		case strings.Contains(line, "Primary script unknown"):
			// PHP-FPM 收到了一个不存在的 PHP 脚本请求，这只会在探测 PHP 文件时发生，
			// 单次命中就直接判高危，和分类系统加入之前的行为一致。
			reason = "Primary script unknown：不存在 PHP 文件进入 PHP-FPM"
			eventType = SecurityEventSuspiciousPHP
			risk = "high"
		case strings.Contains(line, "open()") && strings.Contains(line, "failed (2: No such file or directory)"):
			reason = "open() failed：不存在文件访问"
			// 通用的"文件不存在"在 error.log 里绝大多数是缺图片/CSS/JS 之类的正常
			// 404，只有当请求的确实是 .php 文件时才算"不存在 PHP 探测"这类明确
			// 分类事件；否则保持未分类，和旧行为一样靠累计次数（events >= 3）
			// 才升到中风险，避免单条缺图记录就被打成中风险噪音。
			if strings.HasSuffix(strings.ToLower(reqPath), ".php") {
				eventType = SecurityEventSuspiciousPHP
				risk = "medium"
			}
		default:
			continue
		}
		addWPSecurityEvent(aggregates, site.Domain, strings.TrimSpace(m[1]), parseNginxErrorTime(line), m[3], reqPath, 0, reason, eventType, risk)
	}
}

func addWPSecurityEvent(aggregates map[string]*wpSecurityAggregate, domain, ip string, seen time.Time, method, uri string, status int, reason, eventType, risk string) {
	if ip == "" || uri == "" {
		return
	}
	key := domain + "|" + ip
	agg := aggregates[key]
	if agg == nil {
		agg = &wpSecurityAggregate{
			ip:         ip,
			domain:     domain,
			paths:      map[string]int{},
			evidence:   map[string]bool{},
			eventTypes: map[string]bool{},
			riskLevel:  "low",
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
	if eventType != "" {
		agg.eventTypes[eventType] = true
		if eventRiskWeight(risk) > eventRiskWeight(agg.riskLevel) {
			agg.riskLevel = risk
		}
	}
}

// classifySecurityEvent 根据请求特征判断安全事件类型（只记录、不拦截）
func classifySecurityEvent(method, uri, ua, ip string, status int, checker *searchBotIPChecker) (eventType, riskLevel, message string) {
	lowerURI := strings.ToLower(uri)

	// 1. SQL 注入探测（优先判断，因为 query string 中可能同时命中其他模式）
	if isSQLiProbe(uri) {
		return SecurityEventSQLiProbe, "high", "SQL 注入探测"
	}

	// 2. 伪装搜索引擎爬虫
	if isFakeSearchBot(ua, ip, checker) {
		return SecurityEventFakeSearchBot, "medium", "UA 声明为搜索引擎爬虫，但来源 IP 不在官方段"
	}

	// 3. 敏感文件扫描
	for _, pattern := range sensitiveFilePatterns {
		if strings.Contains(lowerURI, pattern) {
			return SecurityEventSensitiveFileScan, "medium", "敏感文件扫描"
		}
	}

	// 4. 不存在 PHP 文件探测
	if strings.HasSuffix(lowerURI, ".php") && status == 404 {
		return SecurityEventSuspiciousPHP, "low", "不存在 PHP 文件探测"
	}

	return "", "low", "WordPress 异常路径访问"
}

// isSQLiProbe 对 URI 原文和 URL 解码后的形式都做匹配。
// nginx 记录的是请求行原文，攻击者常见工具会把空格编码为 %20 或 +，
// 只匹配原文会漏掉这类编码后的 payload（如 UNION%20SELECT）。
func isSQLiProbe(uri string) bool {
	candidates := []string{uri}
	if decoded, err := url.QueryUnescape(uri); err == nil && decoded != uri {
		candidates = append(candidates, decoded)
	}
	for _, candidate := range candidates {
		for _, re := range sqliProbePatterns {
			if re.MatchString(candidate) {
				return true
			}
		}
	}
	return false
}

func isFakeSearchBot(ua, ip string, checker *searchBotIPChecker) bool {
	lowerUA := strings.ToLower(ua)
	if checker == nil {
		return false
	}
	if strings.Contains(lowerUA, "googlebot") {
		if len(checker.googlebot) == 0 {
			return false
		}
		return !checker.containsGooglebot(ip)
	}
	if strings.Contains(lowerUA, "bingbot") {
		if len(checker.bingbot) == 0 {
			return false
		}
		return !checker.containsBingbot(ip)
	}
	return false
}

func newSearchBotIPChecker(db *sql.DB) *searchBotIPChecker {
	checker := &searchBotIPChecker{}
	if db == nil {
		return checker
	}
	var googleIPs, bingIPs string
	_ = db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'googlebot_ips'`).Scan(&googleIPs)
	_ = db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'bingbot_ips'`).Scan(&bingIPs)
	checker.googlebot = parseIPRangeLines(googleIPs)
	checker.bingbot = parseIPRangeLines(bingIPs)
	return checker
}

func (c *searchBotIPChecker) containsGooglebot(ip string) bool {
	if c == nil {
		return false
	}
	return ipInRanges(ip, c.googlebot)
}

func (c *searchBotIPChecker) containsBingbot(ip string) bool {
	if c == nil {
		return false
	}
	return ipInRanges(ip, c.bingbot)
}

func parseIPRangeLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func ipInRanges(ip string, ranges []string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}
	for _, r := range ranges {
		if strings.Contains(r, "/") {
			_, ipNet, err := net.ParseCIDR(r)
			if err == nil && ipNet.Contains(parsedIP) {
				return true
			}
		} else {
			if target := net.ParseIP(r); target != nil && target.Equal(parsedIP) {
				return true
			}
		}
	}
	return false
}

func sortedEventTypes(types map[string]bool) []string {
	out := make([]string, 0, len(types))
	for t := range types {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func buildWPSecurityReportItem(agg *wpSecurityAggregate) WPSecurityReportItem {
	paths := topSecurityPaths(agg.paths, 8)
	evidence := sortedEvidence(agg.evidence, 6)
	risk := classifyWPSecurityRisk(agg, paths, evidence)
	types := sortedEventTypes(agg.eventTypes)
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
		Types:          types,
	}
	item.CopyText = buildWPSecurityCopyText(item)
	return item
}

func classifyWPSecurityRisk(agg *wpSecurityAggregate, paths, evidence []string) string {
	// 如果分类器已经识别到高风险事件类型，直接采用其风险等级
	if agg.riskLevel == "high" || agg.events >= 10 || hasHighSignalPath(paths) || hasEvidence(evidence, "Primary script unknown") {
		return "高"
	}
	if agg.riskLevel == "medium" || agg.events >= 3 {
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

// wpSecurityLogDirAllowed 是可在测试中替换的函数变量，生产环境保持 isAllowedSiteLogDir 的行为不变。
var wpSecurityLogDirAllowed = isAllowedSiteLogDir

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

// eventRiskWeight 对应 classifySecurityEvent/readNginxErrorLog 使用的英文风险等级
// （"high"/"medium"/"low"），与 riskWeight 使用的中文 "高"/"中"/"低" 词表分开维护，
// 避免两套词表混用导致比较结果恒为 false。
func eventRiskWeight(risk string) int {
	switch risk {
	case "high":
		return 3
	case "medium":
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
