package executor

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

// 方案 D 阶段三：把 classifySecurityEvent() 识别出的明确类型事件持久化到
// wp_security_events 表，配合 wp_security_log_positions 做增量读取（按字节偏移），
// 解决 BuildWPSecurityReport() 仅读取日志尾部 512KB/1000 行、在 logrotate 按天轮转后
// 历史数据丢失的问题。只持久化 4 类明确分类的事件，通用的"WordPress 异常路径访问"
// 兜底事件不入库，避免表无限增长。

const wpSecurityEventRetentionDays = 30

// IngestWPSecurityEvents 对所有 WordPress 站点做一次增量日志摄取，返回新入库的事件数。
func IngestWPSecurityEvents() (int, error) {
	db := database.GetDB()
	if db == nil {
		return 0, fmt.Errorf("database is nil")
	}

	sites, err := listWordPressSecuritySites(db)
	if err != nil {
		return 0, err
	}

	checker := newSearchBotIPChecker(db)
	total := 0
	for _, site := range sites {
		n, err := ingestSiteSecurityEvents(db, site, checker)
		if err != nil {
			log.Printf("wp security event ingest skipped for %s: %v", site.Domain, err)
			continue
		}
		total += n
	}
	return total, nil
}

// ingestSiteSecurityEvents 从上次记录的字节偏移继续读取该站点的 wp-security.log，
// 只处理自上次以来新增的、以换行符结尾的完整行；文件被 copytruncate 轮转导致体积
// 变小或文件首行变化时视为已轮转，从头开始读取新内容。
func ingestSiteSecurityEvents(db *sql.DB, site wpSecuritySite, checker *searchBotIPChecker) (int, error) {
	if !wpSecurityLogDirAllowed(site.LogDir) {
		return 0, nil
	}

	path := filepath.Join(site.LogDir, "wp-security.log")
	f, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, nil
	}

	storedPosition := getWPSecurityLogPosition(db, site.ID)
	offset := storedPosition.byteOffset
	firstLineHash := firstCompleteLineHash(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if info.Size() < offset || (storedPosition.firstLineHash != "" && firstLineHash != "" && storedPosition.firstLineHash != firstLineHash) {
		// 文件比记录的偏移还小，说明已被 copytruncate 轮转，从头重新读取新内容。
		// 高流量站点轮转后文件可能很快增长到超过旧偏移，首行指纹可覆盖这个场景。
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	var consumed int64
	count := 0
	for {
		lineBytes, readErr := reader.ReadBytes('\n')
		if readErr == nil {
			// 只有以换行符结尾的完整行才计入已消费字节；
			// 末尾还没写完的半行留到下一轮再读，避免把偏移记到半行中间。
			consumed += int64(len(lineBytes))
			line := strings.TrimRight(string(lineBytes), "\r\n")
			inserted, err := ingestSecurityLogLine(db, site, line, checker)
			if err != nil {
				if consumed > int64(len(lineBytes)) {
					consumed -= int64(len(lineBytes))
				} else {
					consumed = 0
				}
				break
			}
			if inserted {
				count++
			}
		}
		if readErr != nil {
			break
		}
	}

	newOffset := offset + consumed
	if newOffset != storedPosition.byteOffset || firstLineHash != storedPosition.firstLineHash {
		if err := setWPSecurityLogPosition(db, site.ID, newOffset, firstLineHash); err != nil {
			return count, err
		}
	}
	return count, nil
}

func ingestSecurityLogLine(db *sql.DB, site wpSecuritySite, line string, checker *searchBotIPChecker) (bool, error) {
	m := combinedLogRe.FindStringSubmatch(line)
	if len(m) != 7 {
		return false, nil
	}

	status, _ := strconv.Atoi(m[5])
	occurred := parseNginxAccessTime(m[2])
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	uri := normalizeLoggedURI(m[4])
	ua := strings.TrimSpace(m[6])
	ip := strings.TrimSpace(m[1])
	method := m[3]

	eventType, risk, message := classifySecurityEvent(method, uri, ua, ip, status, checker)
	if eventType == "" {
		return false, nil
	}

	_, err := db.Exec(`INSERT INTO wp_security_events
		(site_id, domain, ip_address, event_type, risk_level, method, path, user_agent, status, message, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		site.ID, site.Domain, ip, eventType, risk, method,
		truncateRunes(uri, 512), truncateRunes(ua, 256), status, message,
		occurred.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		log.Printf("wp security event insert failed for %s: %v", site.Domain, err)
		return false, err
	}
	return true, nil
}

type wpSecurityLogPosition struct {
	byteOffset    int64
	firstLineHash string
}

func getWPSecurityLogPosition(db *sql.DB, siteID int) wpSecurityLogPosition {
	var pos wpSecurityLogPosition
	_ = db.QueryRow(`SELECT byte_offset, first_line_hash FROM wp_security_log_positions WHERE site_id = ?`, siteID).Scan(&pos.byteOffset, &pos.firstLineHash)
	return pos
}

func setWPSecurityLogPosition(db *sql.DB, siteID int, offset int64, firstLineHash string) error {
	_, err := db.Exec(`INSERT INTO wp_security_log_positions (site_id, byte_offset, first_line_hash, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(site_id) DO UPDATE SET byte_offset = excluded.byte_offset, first_line_hash = excluded.first_line_hash, updated_at = excluded.updated_at`,
		siteID, offset, firstLineHash)
	return err
}

func firstCompleteLineHash(f *os.File) string {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	lineBytes, err := reader.ReadBytes('\n')
	if err != nil {
		return ""
	}
	line := strings.TrimRight(string(lineBytes), "\r\n")
	sum := sha256.Sum256([]byte(line))
	return hex.EncodeToString(sum[:])
}

// CountRecentSecurityEventsByIP 统计指定事件类型在时间窗口内每个 IP 的出现次数，
// 供方案 D 阶段四的告警规则使用。
func CountRecentSecurityEventsByIP(eventType string, since time.Time) (map[string]int, error) {
	db := database.GetDB()
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	rows, err := db.Query(`SELECT ip_address, COUNT(*) FROM wp_security_events
		WHERE event_type = ? AND occurred_at >= ?
		GROUP BY ip_address`,
		eventType, since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var ip string
		var count int
		if err := rows.Scan(&ip, &count); err != nil {
			continue
		}
		counts[ip] = count
	}
	return counts, rows.Err()
}

// WPSecurityAlertOffender 是超过告警阈值的单个 IP 摘要，供邮件/Webhook 告警使用。
type WPSecurityAlertOffender struct {
	IP    string
	Count int
	Paths []string
}

// topWPSecurityOffenders 返回时间窗口内命中次数达到阈值的 IP 列表（按次数降序），
// 每个 IP 附带命中次数最多的若干条请求路径样本。
func topWPSecurityOffenders(eventType string, since time.Time, threshold, pathLimit int) ([]WPSecurityAlertOffender, error) {
	db := database.GetDB()
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	counts, err := CountRecentSecurityEventsByIP(eventType, since)
	if err != nil {
		return nil, err
	}

	var offenders []WPSecurityAlertOffender
	for ip, count := range counts {
		if count < threshold {
			continue
		}
		paths, err := topWPSecurityEventPaths(db, eventType, since, ip, pathLimit)
		if err != nil {
			paths = nil
		}
		offenders = append(offenders, WPSecurityAlertOffender{IP: ip, Count: count, Paths: paths})
	}
	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i].Count != offenders[j].Count {
			return offenders[i].Count > offenders[j].Count
		}
		return offenders[i].IP < offenders[j].IP
	})
	return offenders, nil
}

func topWPSecurityEventPaths(db *sql.DB, eventType string, since time.Time, ip string, limit int) ([]string, error) {
	rows, err := db.Query(`SELECT path, COUNT(*) c FROM wp_security_events
		WHERE event_type = ? AND ip_address = ? AND occurred_at >= ?
		GROUP BY path ORDER BY c DESC, path ASC LIMIT ?`,
		eventType, ip, since.UTC().Format("2006-01-02 15:04:05"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		var count int
		if err := rows.Scan(&path, &count); err != nil {
			continue
		}
		paths = append(paths, fmt.Sprintf("%s × %d", path, count))
	}
	return paths, rows.Err()
}

// PruneWPSecurityEvents 删除超过保留期限的历史事件，避免表无限增长。
func PruneWPSecurityEvents(retentionDays int) error {
	db := database.GetDB()
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	if retentionDays <= 0 {
		retentionDays = wpSecurityEventRetentionDays
	}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour).Format("2006-01-02 15:04:05")
	_, err := db.Exec(`DELETE FROM wp_security_events WHERE occurred_at < ?`, cutoff)
	return err
}

// StartWPSecurityEventIngestor 启动后台增量摄取调度：每 wpSecurityIngestorInterval
// 读取一次新增日志，并顺带清理过期历史事件。StopWPSecurityEventIngestor 可关闭
// 该调度，供 main.go 在收到 SIGINT/SIGTERM 时调用（审核优化项 3.2）。
//
// 保留初始 runWPSecurityEventIngestCycle() 调用（select 之前），保证面板启动后
// 立即摄取一次，而不是等到第一个 tick。代价是 Stop 无法中断正在执行的初始摄取，
// 但该调用在生产环境秒级完成，可接受。
var (
	wpSecurityIngestorMu       sync.Mutex
	wpSecurityIngestorStopCh   chan struct{}
	wpSecurityIngestorDone     chan struct{}
	wpSecurityIngestorInterval = 5 * time.Minute // var 而非 const，便于测试覆盖
)

func StartWPSecurityEventIngestor() {
	wpSecurityIngestorMu.Lock()
	wpSecurityIngestorStopCh = make(chan struct{})
	wpSecurityIngestorDone = make(chan struct{})
	stopCh := wpSecurityIngestorStopCh
	done := wpSecurityIngestorDone
	wpSecurityIngestorMu.Unlock()

	go func() {
		defer close(done)
		runWPSecurityEventIngestCycle()
		ticker := time.NewTicker(wpSecurityIngestorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runWPSecurityEventIngestCycle()
			case <-stopCh:
				return
			}
		}
	}()
}

// StopWPSecurityEventIngestor 关闭后台摄取调度。幂等：多次调用安全。
// 调用后 wpSecurityIngestorDone 会被关闭，调用方可 select 等待退出完成。
func StopWPSecurityEventIngestor() {
	wpSecurityIngestorMu.Lock()
	defer wpSecurityIngestorMu.Unlock()
	if wpSecurityIngestorStopCh != nil {
		close(wpSecurityIngestorStopCh)
		wpSecurityIngestorStopCh = nil
	}
}

func runWPSecurityEventIngestCycle() {
	if _, err := IngestWPSecurityEvents(); err != nil {
		log.Printf("wp security event ingest failed: %v", err)
	}
	if err := PruneWPSecurityEvents(wpSecurityEventRetentionDays); err != nil {
		log.Printf("wp security event prune failed: %v", err)
	}
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}
