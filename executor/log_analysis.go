package executor

import (
	"bufio"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

const (
	logAnalysisMaxBytes   = int64(512 * 1024 * 1024)
	logAnalysisMaxSamples = 30
	logAnalysisTopLimit   = 12
)

var combinedLogPattern = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^]]+)\] "(\S+) ([^ ]+) [^"]+" (\d{3}) \S+ "[^"]*" "([^"]*)"`)
var bracketTimePattern = regexp.MustCompile(`^\[([^]]+)\]`)
var nginxTimePattern = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})`)
var logAnalysisIPv4Pattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var logAnalysisSecretPattern = regexp.MustCompile(`(?i)(password|passwd|token|api[_-]?key|authorization|cookie)([=: ]+)([^\s&"']+)`)
var logAnalysisWebRootPattern = regexp.MustCompile(`/www/wwwroot/[^/\s]+`)

type logAnalysisAccumulator struct {
	report       *models.LogAnalysisReport
	status       map[string]int
	hourly       map[string]int
	paths        map[string]int
	ips          map[string]int
	uniqueIPs    map[string]struct{}
	bots         map[string]*models.LogAnalysisBotCount
	botChecker   *searchBotIPChecker
	botRequests  map[string]struct{}
	fiveXX       int
	fiveXXLines  []string
	notFound     int
	lastLineTime time.Time
	lang         string
}

// AnalyzeWebsiteLogs scans all current and rotated site logs and keeps only
// records whose own timestamp falls inside the requested interval.
func AnalyzeWebsiteLogs(site *models.Website, startAt, endAt time.Time, db *sql.DB, lang string) (*models.LogAnalysisReport, error) {
	if site == nil || site.ID <= 0 {
		return nil, fmt.Errorf("website not found")
	}
	if !startAt.Before(endAt) || endAt.Sub(startAt) > 7*24*time.Hour {
		return nil, fmt.Errorf("invalid analysis time range")
	}
	cleanDir := filepath.Clean(site.LogDir)
	if !filepath.IsAbs(cleanDir) {
		return nil, fmt.Errorf("invalid site log directory")
	}
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("read site log directory: %w", err)
	}

	report := &models.LogAnalysisReport{
		SiteID: site.ID, Domain: site.Domain, StartAt: startAt, EndAt: endAt,
		GeneratedAt: time.Now(), Findings: []models.LogAnalysisFinding{}, Samples: []string{},
	}
	acc := &logAnalysisAccumulator{
		report: report, status: map[string]int{}, hourly: map[string]int{},
		paths: map[string]int{}, ips: map[string]int{}, uniqueIPs: map[string]struct{}{},
		bots:       map[string]*models.LogAnalysisBotCount{},
		botChecker: newSearchBotIPChecker(db), botRequests: map[string]struct{}{}, lang: lang,
	}
	if db != nil {
		_ = db.QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'last_whitelist_update'`).Scan(&report.CrawlerRangesUpdatedAt)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !isAnalyzableLogName(entry.Name()) {
			continue
		}
		path := filepath.Join(cleanDir, entry.Name())
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil || !isPathWithinRoot(cleanDir, resolved) {
			continue
		}
		files = append(files, resolved)
	}
	sort.Strings(files)
	for _, path := range files {
		if report.BytesScanned >= logAnalysisMaxBytes {
			report.Truncated = true
			break
		}
		if err := acc.scanFile(path, startAt, endAt); err != nil {
			continue
		}
		report.FilesScanned++
	}

	report.UniqueIPs = len(acc.uniqueIPs)
	report.StatusCodes = sortedCounts(acc.status, 0)
	report.HourlyRequests = sortedNamedCounts(acc.hourly)
	report.TopPaths = sortedCounts(acc.paths, logAnalysisTopLimit)
	report.TopIPs = sortedCounts(acc.ips, logAnalysisTopLimit)
	report.Bots = sortedBots(acc.bots)
	acc.buildFindings()
	return report, nil
}

func isAnalyzableLogName(name string) bool {
	for _, base := range []string{"access.log", "error.log", "php-error.log", "php-slow.log", "wp-security.log"} {
		if name == base || isRotatedLogName(base, name) {
			return true
		}
	}
	return false
}

func isRotatedLogName(base, name string) bool {
	suffix := strings.TrimSuffix(name, ".gz")
	if strings.HasPrefix(suffix, base+".") {
		value := strings.TrimPrefix(suffix, base+".")
		if value == "" {
			return false
		}
		for _, r := range value {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(suffix, base+"-") {
		_, err := time.Parse("2006-01-02", strings.TrimPrefix(suffix, base+"-"))
		return err == nil
	}
	return false
}

func (a *logAnalysisAccumulator) scanFile(path string, startAt, endAt time.Time) error {
	a.lastLineTime = time.Time{}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var reader io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		reader = gz
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	base := filepath.Base(path)
	for scanner.Scan() {
		line := scanner.Text()
		a.report.BytesScanned += int64(len(line) + 1)
		a.report.LinesScanned++
		if a.report.BytesScanned > logAnalysisMaxBytes {
			a.report.Truncated = true
			return nil
		}
		if strings.HasPrefix(base, "access.log") || strings.HasPrefix(base, "wp-security.log") {
			a.consumeAccess(line, strings.HasPrefix(base, "wp-security.log"), startAt, endAt)
		} else {
			a.consumeError(line, base, startAt, endAt)
		}
	}
	return scanner.Err()
}

func (a *logAnalysisAccumulator) consumeAccess(line string, security bool, startAt, endAt time.Time) {
	m := combinedLogPattern.FindStringSubmatch(line)
	if len(m) != 7 {
		return
	}
	stamp, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[2])
	if err != nil || stamp.Before(startAt) || stamp.After(endAt) {
		return
	}
	a.report.LinesInRange++
	if security {
		a.report.SecurityRequestCount++
		a.classifyBot(m[6], m[1], m[2]+"|"+m[1]+"|"+m[4]+"|"+m[6])
		return
	}
	a.report.AccessRequests++
	ip, path, code, ua := m[1], normalizeLogPath(m[4]), m[5], m[6]
	a.uniqueIPs[ip] = struct{}{}
	a.ips[ip]++
	a.paths[path]++
	a.status[code]++
	a.hourly[stamp.Format("2006-01-02 15:00")]++
	if strings.HasPrefix(code, "5") {
		a.fiveXX++
		if len(a.fiveXXLines) < logAnalysisMaxSamples {
			a.fiveXXLines = append(a.fiveXXLines, sanitizeLogSample(line))
		}
		a.addSample(fmt.Sprintf("%s %s %s %s", stamp.Format(time.RFC3339), code, maskIP(ip), path))
	}
	if code == "404" {
		a.notFound++
	}
	a.classifyBot(ua, ip, m[2]+"|"+ip+"|"+m[4]+"|"+ua)
}

func (a *logAnalysisAccumulator) consumeError(line, base string, startAt, endAt time.Time) {
	stamp, ok := parseErrorLogTime(line)
	if ok {
		a.lastLineTime = stamp
	} else {
		stamp = a.lastLineTime
	}
	if stamp.IsZero() || stamp.Before(startAt) || stamp.After(endAt) {
		return
	}
	a.report.LinesInRange++
	lower := strings.ToLower(line)
	switch {
	case strings.HasPrefix(base, "php-slow.log") && ok:
		a.report.SlowRequestCount++
	case strings.Contains(lower, "fatal error") || strings.Contains(lower, "uncaught ") || strings.Contains(lower, "parse error") || strings.Contains(lower, "allowed memory size"):
		a.report.PHPFatalCount++
		a.addSample(sanitizeLogSample(line))
	case strings.Contains(lower, "warning") || strings.Contains(lower, "deprecated") || strings.Contains(lower, "notice"):
		a.report.PHPWarningCount++
	case strings.HasPrefix(base, "error.log"):
		a.report.NginxErrorCount++
		if strings.Contains(lower, "upstream") || strings.Contains(lower, "permission denied") || strings.Contains(lower, "connect() failed") {
			a.addSample(sanitizeLogSample(line))
		}
	}
}

func parseErrorLogTime(line string) (time.Time, bool) {
	if m := nginxTimePattern.FindStringSubmatch(line); len(m) == 2 {
		t, err := time.ParseInLocation("2006/01/02 15:04:05", m[1], time.Local)
		return t, err == nil
	}
	if m := bracketTimePattern.FindStringSubmatch(line); len(m) == 2 {
		for _, layout := range []string{"02-Jan-2006 15:04:05 MST", "02-Jan-2006 15:04:05", "02-Jan-2006 15:04:05 UTC"} {
			if t, err := time.ParseInLocation(layout, m[1], time.Local); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func normalizeLogPath(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Path != "" {
		return parsed.Path
	}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func (a *logAnalysisAccumulator) classifyBot(ua, ip, requestKey string) {
	name, verification := identifySearchBot(a.botChecker, ua, ip)
	if name == "" {
		return
	}
	if _, exists := a.botRequests[requestKey]; exists {
		return
	}
	a.botRequests[requestKey] = struct{}{}
	switch verification {
	case "verified":
		a.report.VerifiedSearchCount++
	case "fake":
		a.report.FakeSearchBotCount++
	case "unverified":
		a.report.UnverifiedBotCount++
	}
	key := name + ":" + verification
	if a.bots[key] == nil {
		a.bots[key] = &models.LogAnalysisBotCount{Name: name, Verification: verification}
	}
	a.bots[key].Count++
}

func identifySearchBot(checker *searchBotIPChecker, ua, ip string) (string, string) {
	lower := strings.ToLower(ua)
	name, verification := "", ""
	switch {
	case strings.Contains(lower, "googlebot"):
		name = "Googlebot"
		if len(checker.googlebot) == 0 {
			verification = "unknown"
		} else if checker.containsGooglebot(ip) {
			verification = "verified"
		} else {
			verification = "fake"
		}
	case strings.Contains(lower, "bingbot"):
		name = "Bingbot"
		if len(checker.bingbot) == 0 {
			verification = "unknown"
		} else if checker.containsBingbot(ip) {
			verification = "verified"
		} else {
			verification = "fake"
		}
	default:
		known := []struct{ token, label string }{
			{"baiduspider", "Baiduspider"}, {"yandexbot", "YandexBot"},
			{"duckduckbot", "DuckDuckBot"}, {"sogou", "Sogou"},
			{"semrushbot", "SemrushBot"}, {"ahrefsbot", "AhrefsBot"},
		}
		for _, item := range known {
			if strings.Contains(lower, item.token) {
				name, verification = item.label, "unverified"
				break
			}
		}
		if name == "" && (strings.Contains(lower, "bot") || strings.Contains(lower, "crawler") || strings.Contains(lower, "spider") || strings.Contains(lower, "scraper")) {
			name, verification = "Other bot", "unverified"
		}
	}
	return name, verification
}

func (a *logAnalysisAccumulator) addSample(sample string) {
	if sample != "" && len(a.report.Samples) < logAnalysisMaxSamples {
		a.report.Samples = append(a.report.Samples, sample)
	}
}

func (a *logAnalysisAccumulator) buildFindings() {
	r := a.report
	if a.fiveXX > 0 {
		r.Findings = append(r.Findings, models.LogAnalysisFinding{Severity: "high", Title: i18n.T(a.lang, "log_analysis.finding_5xx_title"), Detail: i18n.T(a.lang, "log_analysis.finding_5xx_detail", i18n.P{"count": fmt.Sprint(a.fiveXX)}), Evidence: append([]string(nil), a.fiveXXLines...)})
	}
	if r.PHPFatalCount > 0 {
		r.Findings = append(r.Findings, models.LogAnalysisFinding{Severity: "high", Title: i18n.T(a.lang, "log_analysis.finding_php_title"), Detail: i18n.T(a.lang, "log_analysis.finding_php_detail", i18n.P{"count": fmt.Sprint(r.PHPFatalCount)})})
	}
	if r.AccessRequests > 0 && a.notFound*100/r.AccessRequests >= 20 {
		r.Findings = append(r.Findings, models.LogAnalysisFinding{Severity: "medium", Title: i18n.T(a.lang, "log_analysis.finding_404_title"), Detail: i18n.T(a.lang, "log_analysis.finding_404_detail", i18n.P{"count": fmt.Sprint(a.notFound), "percent": fmt.Sprint(a.notFound * 100 / r.AccessRequests)})})
	}
	if len(r.TopIPs) > 0 && r.AccessRequests >= 100 && r.TopIPs[0].Count*100/r.AccessRequests >= 40 {
		r.Findings = append(r.Findings, models.LogAnalysisFinding{Severity: "medium", Title: i18n.T(a.lang, "log_analysis.finding_ip_title"), Detail: i18n.T(a.lang, "log_analysis.finding_ip_detail", i18n.P{"ip": maskIP(r.TopIPs[0].Name), "count": fmt.Sprint(r.TopIPs[0].Count), "percent": fmt.Sprint(r.TopIPs[0].Count * 100 / r.AccessRequests)})})
	}
	if len(r.Findings) == 0 {
		r.Findings = append(r.Findings, models.LogAnalysisFinding{Severity: "low", Title: i18n.T(a.lang, "log_analysis.finding_normal_title"), Detail: i18n.T(a.lang, "log_analysis.finding_normal_detail")})
	}
}

func sortedCounts(values map[string]int, limit int) []models.LogAnalysisCount {
	out := make([]models.LogAnalysisCount, 0, len(values))
	for name, count := range values {
		out = append(out, models.LogAnalysisCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count || out[i].Count == out[j].Count && out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func sortedNamedCounts(values map[string]int) []models.LogAnalysisCount {
	out := make([]models.LogAnalysisCount, 0, len(values))
	for name, count := range values {
		out = append(out, models.LogAnalysisCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortedBots(values map[string]*models.LogAnalysisBotCount) []models.LogAnalysisBotCount {
	out := make([]models.LogAnalysisBotCount, 0, len(values))
	for _, item := range values {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

func maskIP(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		return strings.Join(parts[:3], ".") + ".*"
	}
	if strings.Contains(ip, ":") {
		parts = strings.Split(ip, ":")
		if len(parts) > 3 {
			return strings.Join(parts[:3], ":") + ":*"
		}
	}
	return ip
}

func sanitizeLogSample(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > 500 {
		line = line[:500]
	}
	return line
}

func BuildLogAnalysisPrompt(report *models.LogAnalysisReport) (string, string, error) {
	if report == nil {
		return "", "", fmt.Errorf("log analysis report is empty")
	}
	copyReport := *report
	copyReport.TopIPs = append([]models.LogAnalysisCount(nil), report.TopIPs...)
	for i := range copyReport.TopIPs {
		copyReport.TopIPs[i].Name = maskIP(copyReport.TopIPs[i].Name)
	}
	copyReport.Samples = append([]string(nil), report.Samples...)
	for i := range copyReport.Samples {
		copyReport.Samples[i] = sanitizeLogAnalysisAIText(copyReport.Samples[i])
	}
	copyReport.Findings = append([]models.LogAnalysisFinding(nil), report.Findings...)
	for i := range copyReport.Findings {
		copyReport.Findings[i].Evidence = append([]string(nil), report.Findings[i].Evidence...)
		for j := range copyReport.Findings[i].Evidence {
			copyReport.Findings[i].Evidence[j] = sanitizeLogAnalysisAIText(copyReport.Findings[i].Evidence[j])
		}
	}
	data, err := json.Marshal(copyReport)
	if err != nil {
		return "", "", err
	}
	system := "你是 WP Panel 的网站日志分析助手。请只依据本地分析报告解释正常流量、异常流量、错误和爬虫行为。报告中的高频路径与高频 IP 是彼此独立的排行榜，除非证据明确关联，否则不得声称某 IP 访问了某路径。HTTP 444 表示请求已被面板安全规则拒绝，是拦截结果，不是新的违规。verified 表示命中 Google/Bing 官方 IP 段；fake 表示 UA 冒充但 IP 未命中已缓存的对应官方段；unknown 表示对应官方 IP 段尚未成功缓存；unverified 表示其他爬虫仅按 User-Agent 识别。不要单独将 fake、unknown 或 unverified 视为高风险，也不要仅因身份状态建议封禁。样本和排行榜有数量上限，不能当作全部原始日志。不要编造日志中没有的事实，不要建议执行 shell 命令，不要重复建议报告中已明确生效的防护。使用简洁 Markdown，按总体结论、主要异常、正常活动、处理建议四部分回答，每项结论尽量引用对应数字。"
	user := "以下是服务器本地完整扫描指定时间段后生成的结构化报告：\n" + string(data)
	return system, user, nil
}

func sanitizeLogAnalysisAIText(value string) string {
	value = sanitizeLogAnalysisAITextKeepingIPs(value)
	value = logAnalysisIPv4Pattern.ReplaceAllStringFunc(value, maskIP)
	return value
}

func sanitizeLogAnalysisAITextKeepingIPs(value string) string {
	value = logAnalysisSecretPattern.ReplaceAllString(value, "$1$2[redacted]")
	value = logAnalysisWebRootPattern.ReplaceAllString(value, "/site")
	return value
}
