package executor

import (
	"bufio"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

const logAnalysisDetailMaxPageSize = 100

// AnalyzeWebsiteLogDetails rescans access logs for one drill-down dimension.
func AnalyzeWebsiteLogDetails(site *models.Website, startAt, endAt time.Time, db *sql.DB, kind, value string, page, pageSize int) (*models.LogAnalysisDetail, error) {
	if site == nil || site.ID <= 0 || !startAt.Before(endAt) || endAt.Sub(startAt) > 7*24*time.Hour {
		return nil, fmt.Errorf("invalid log analysis detail request")
	}
	if kind != "status" && kind != "path" && kind != "bot" {
		return nil, fmt.Errorf("invalid detail kind")
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 500 {
		return nil, fmt.Errorf("invalid detail value")
	}
	if kind == "status" {
		if len(value) != 3 {
			return nil, fmt.Errorf("invalid status code")
		}
		if _, err := strconv.Atoi(value); err != nil {
			return nil, fmt.Errorf("invalid status code")
		}
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > logAnalysisDetailMaxPageSize {
		pageSize = 20
	}

	cleanDir := filepath.Clean(site.LogDir)
	if !filepath.IsAbs(cleanDir) {
		return nil, fmt.Errorf("invalid site log directory")
	}
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("read site log directory: %w", err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasPrefix(entry.Name(), "access.log") && !strings.HasPrefix(entry.Name(), "wp-security.log")) || !isAnalyzableLogName(entry.Name()) {
			continue
		}
		path := filepath.Join(cleanDir, entry.Name())
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr == nil && isPathWithinRoot(cleanDir, resolved) {
			files = append(files, resolved)
		}
	}
	sort.Strings(files)

	result := &models.LogAnalysisDetail{Kind: kind, Value: value, Page: page, PageSize: pageSize, Lines: []string{}, SecurityEventRetentionDays: 30, BanHistoryLimit: 300}
	ips := map[string]int{}
	paths := map[string]int{}
	statuses := map[string]int{}
	userAgents := map[string]int{}
	methods := map[string]int{}
	hourly := map[string]int{}
	ipPathPairs := map[string]int{}
	offset := (page - 1) * pageSize
	checker := newSearchBotIPChecker(db)
	seenBots := map[string]struct{}{}
	var bytesScanned int64
	for _, path := range files {
		if bytesScanned >= logAnalysisMaxBytes {
			break
		}
		_ = scanLogAnalysisDetailFile(path, func(line string) bool {
			bytesScanned += int64(len(line) + 1)
			if bytesScanned > logAnalysisMaxBytes {
				return false
			}
			m := combinedLogPattern.FindStringSubmatch(line)
			if len(m) != 7 {
				return true
			}
			stamp, parseErr := time.Parse("02/Jan/2006:15:04:05 -0700", m[2])
			if parseErr != nil || stamp.Before(startAt) || stamp.After(endAt) {
				return true
			}
			security := strings.HasPrefix(filepath.Base(path), "wp-security.log")
			matched := false
			switch kind {
			case "status":
				matched = !security && m[5] == value
			case "path":
				matched = !security && normalizeLogPath(m[4]) == value
			case "bot":
				name, verification := identifySearchBot(checker, m[6], m[1])
				matched = name+":"+verification == value
				if matched {
					requestKey := m[2] + "|" + m[1] + "|" + m[4] + "|" + m[6]
					if _, exists := seenBots[requestKey]; exists {
						matched = false
					} else {
						seenBots[requestKey] = struct{}{}
					}
				}
			}
			if matched {
				normalizedPath := normalizeLogPath(m[4])
				ips[m[1]]++
				paths[normalizedPath]++
				statuses[m[5]]++
				userAgents[normalizeUserAgent(m[6])]++
				methods[m[3]]++
				hourly[stamp.Format("2006-01-02 15:00")]++
				ipPathPairs[m[1]+" → "+normalizedPath]++
				if result.Total >= offset && len(result.Lines) < pageSize {
					result.Lines = append(result.Lines, sanitizeLogSample(line))
				}
				result.Total++
			}
			return true
		})
	}
	result.UniqueIPs = len(ips)
	result.UniquePaths = len(paths)
	result.TopPaths = sortedCounts(paths, logAnalysisTopLimit)
	result.StatusCodes = sortedCounts(statuses, logAnalysisTopLimit)
	result.UserAgents = sortedCounts(userAgents, logAnalysisTopLimit)
	result.Methods = sortedCounts(methods, logAnalysisTopLimit)
	result.Hourly = sortedNamedCounts(hourly)
	result.IPPathPairs = sortedCounts(ipPathPairs, logAnalysisTopLimit)
	result.TopIPs = buildLogAnalysisIPDetails(db, site.ID, ips, startAt, endAt)
	return result, nil
}

func normalizeUserAgent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if len(value) > 160 {
		return value[:160]
	}
	return value
}

func buildLogAnalysisIPDetails(db *sql.DB, siteID int, counts map[string]int, startAt, endAt time.Time) []models.LogAnalysisIPDetail {
	ranked := sortedCounts(counts, logAnalysisTopLimit)
	items := make([]models.LogAnalysisIPDetail, 0, len(ranked))
	for _, rankedIP := range ranked {
		item := models.LogAnalysisIPDetail{IPAddress: rankedIP.Name, Count: rankedIP.Count, SecurityEventTypes: []string{}}
		if db != nil {
			var expires sql.NullString
			_ = db.QueryRow(`SELECT reason,source_jail,expires_at FROM firewall_bans
				WHERE ip_address=? AND unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now'))
				ORDER BY banned_at DESC,id DESC LIMIT 1`, rankedIP.Name).Scan(&item.CurrentBanReason, &item.CurrentBanSource, &expires)
			if item.CurrentBanSource != "" || item.CurrentBanReason != "" || expires.Valid {
				item.CurrentlyBanned = true
				item.CurrentBanExpires = expires.String
			}
			var started, ended, historicalExpires sql.NullString
			_ = db.QueryRow(`SELECT reason,source_jail,banned_at,expires_at,unbanned_at,ban_count
				FROM firewall_bans WHERE ip_address=? AND banned_at <= ?
				AND COALESCE(unbanned_at,expires_at,datetime('now')) >= ?
				ORDER BY banned_at DESC,id DESC LIMIT 1`, rankedIP.Name,
				endAt.UTC().Format("2006-01-02 15:04:05"), startAt.UTC().Format("2006-01-02 15:04:05")).
				Scan(&item.RangeBanReason, &item.RangeBanSource, &started, &historicalExpires, &ended, &item.RangeBanCount)
			if started.Valid {
				item.BannedInRange = true
				item.RangeBanStartedAt = started.String
				item.RangeBanExpiresAt = historicalExpires.String
				item.RangeBanEndedAt = ended.String
			}
			rows, err := db.Query(`SELECT event_type,COUNT(*),MAX(occurred_at) FROM wp_security_events
				WHERE site_id=? AND ip_address=? GROUP BY event_type ORDER BY COUNT(*) DESC,event_type ASC`, siteID, rankedIP.Name)
			if err == nil {
				for rows.Next() {
					var eventType, lastSeen string
					var count int
					if rows.Scan(&eventType, &count, &lastSeen) == nil {
						item.RetainedEventCount += count
						item.SecurityEventTypes = append(item.SecurityEventTypes, eventType)
						if lastSeen > item.LastSecurityEvent {
							item.LastSecurityEvent = lastSeen
						}
					}
				}
				rows.Close()
			}
		}
		items = append(items, item)
	}
	return items
}

func scanLogAnalysisDetailFile(path string, consume func(string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var reader io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, gzipErr := gzip.NewReader(f)
		if gzipErr != nil {
			return gzipErr
		}
		defer gz.Close()
		reader = gz
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if !consume(scanner.Text()) {
			break
		}
	}
	return scanner.Err()
}

func BuildLogAnalysisDetailPrompt(detail *models.LogAnalysisDetail, report *models.LogAnalysisReport, domain string, startAt, endAt time.Time) (string, string, error) {
	if detail == nil || detail.Total < 1 {
		return "", "", fmt.Errorf("log analysis detail is empty")
	}
	copyDetail := *detail
	copyDetail.Value = sanitizeLogAnalysisAIText(copyDetail.Value)
	copyDetail.TopIPs = append([]models.LogAnalysisIPDetail(nil), detail.TopIPs...)
	for i := range copyDetail.TopIPs {
		copyDetail.TopIPs[i].IPAddress = maskIP(copyDetail.TopIPs[i].IPAddress)
	}
	copyDetail.IPPathPairs = append([]models.LogAnalysisCount(nil), detail.IPPathPairs...)
	for i := range copyDetail.IPPathPairs {
		copyDetail.IPPathPairs[i].Name = sanitizeLogAnalysisAIText(copyDetail.IPPathPairs[i].Name)
	}
	copyDetail.Lines = append([]string(nil), detail.Lines...)
	for i := range copyDetail.Lines {
		copyDetail.Lines[i] = sanitizeLogAnalysisAIText(copyDetail.Lines[i])
	}
	contextData := struct {
		Domain        string                    `json:"domain"`
		StartAt       time.Time                 `json:"start_at"`
		EndAt         time.Time                 `json:"end_at"`
		OverallReport *models.LogAnalysisReport `json:"overall_report,omitempty"`
		SelectedGroup *models.LogAnalysisDetail `json:"selected_group"`
		Notes         []string                  `json:"interpretation_notes"`
	}{
		Domain: domain, StartAt: startAt, EndAt: endAt, OverallReport: report, SelectedGroup: &copyDetail,
		Notes: []string{
			"selected_group 的统计覆盖该时间段内全部匹配项，lines 最多 100 条且均为脱敏样本",
			"Top 列表最多保留 12 项，不能视为完整枚举",
			"HTTP 444 表示请求已被面板安全规则拒绝，不是新的违规",
			"verified 表示命中已缓存的 Google/Bing 官方 IP 段；fake 表示未命中对应官方段；unknown 表示对应官方 IP 段尚未成功缓存；unverified 表示仅按 User-Agent 识别",
			"封禁和关联安全事件只反映面板当前仍保留的记录，显示 0 不代表历史上从未发生",
		},
	}
	if report != nil {
		redacted := *report
		redacted.TopIPs = append([]models.LogAnalysisCount(nil), report.TopIPs...)
		for i := range redacted.TopIPs {
			redacted.TopIPs[i].Name = maskIP(redacted.TopIPs[i].Name)
		}
		redacted.Samples = nil
		redacted.Findings = append([]models.LogAnalysisFinding(nil), report.Findings...)
		for i := range redacted.Findings {
			redacted.Findings[i].Evidence = nil
		}
		contextData.OverallReport = &redacted
	}
	data, err := json.Marshal(contextData)
	if err != nil {
		return "", "", err
	}
	system := "你是 WP Panel 的网站日志分析助手。请结合总体报告，只分析当前筛选组的请求特征、主要来源、路径和状态码关系、可能成因、现有安全防护是否已经处理，以及管理员是否还需操作。统计汇总覆盖全部匹配日志，但原始行只是最多 100 条样本；不得用样本比例冒充总体比例。不要编造未提供的 IP 与路径关联，不要建议执行 shell 命令，不要重复建议已经明确生效的防护。使用简洁 Markdown，按结论、证据、判断、建议四部分回答。"
	return system, "以下是站点总体报告、当前筛选组统计和脱敏样本：\n" + string(data), nil
}
