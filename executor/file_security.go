package executor

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	urlpath "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const (
	FileSecurityEventRuntimePHPAccess = "runtime_php_access"
	FileSecurityEventSuspiciousFile   = "suspicious_runtime_php_file"
)

var (
	fileSecurityCombinedLogRe = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "([A-Z]+) ([^" ]+) [^"]*" ([0-9]{3}) \S+ "[^"]*" "([^"]*)"`)
	fileSecurityLogDirAllowed = isAllowedSiteLogDir
)

type fileSecuritySite struct {
	ID      int
	Domain  string
	WebRoot string
	LogDir  string
}

type fileSecurityRecord struct {
	SiteID        int
	Domain        string
	EventType     string
	Source        string
	RiskLevel     string
	Path          string
	RequestMethod string
	IPAddress     string
	UserAgent     string
	Status        int
	FileSize      int64
	FileMTime     string
	Message       string
	FirstSeen     string
	LastSeen      string
	EventCount    int
}

func RefreshFileSecurityEvents() (models.FileSecurityRefreshSummary, error) {
	db := database.GetDB()
	if db == nil {
		return models.FileSecurityRefreshSummary{}, fmt.Errorf("database is nil")
	}

	sites, err := listFileSecuritySites(db)
	if err != nil {
		return models.FileSecurityRefreshSummary{}, err
	}

	var summary models.FileSecurityRefreshSummary
	for _, site := range sites {
		summary.SitesScanned++
		fileCount, err := scanSiteSuspiciousRuntimeFiles(db, site)
		if err != nil {
			return summary, err
		}
		accessCount, err := importSiteRuntimePHPAccessEvents(db, site)
		if err != nil {
			return summary, err
		}
		summary.FileEvents += fileCount
		summary.AccessEvents += accessCount
	}
	return summary, nil
}

func ListFileSecurityEvents(limit int) ([]models.FileSecurityEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	db := database.GetDB()
	if db == nil {
		return nil, fmt.Errorf("database is nil")
	}

	rows, err := db.Query(`SELECT id, site_id, domain, event_type, source, risk_level, path,
			request_method, ip_address, user_agent, status, file_size, file_mtime, message,
			first_seen, last_seen, event_count, resolved_at
		FROM file_security_events
		ORDER BY resolved_at IS NOT NULL, last_seen DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []models.FileSecurityEvent{}
	for rows.Next() {
		var event models.FileSecurityEvent
		var fileMTime, resolvedAt sql.NullString
		if err := rows.Scan(&event.ID, &event.SiteID, &event.Domain, &event.EventType, &event.Source,
			&event.RiskLevel, &event.Path, &event.RequestMethod, &event.IPAddress, &event.UserAgent,
			&event.Status, &event.FileSize, &fileMTime, &event.Message, &event.FirstSeen,
			&event.LastSeen, &event.EventCount, &resolvedAt); err != nil {
			return nil, err
		}
		if fileMTime.Valid {
			event.FileMTime = &fileMTime.String
		}
		if resolvedAt.Valid {
			event.ResolvedAt = &resolvedAt.String
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func listFileSecuritySites(db *sql.DB) ([]fileSecuritySite, error) {
	rows, err := db.Query(`SELECT id, domain, web_root, log_dir FROM websites WHERE site_type = 'wordpress' ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sites := []fileSecuritySite{}
	for rows.Next() {
		var site fileSecuritySite
		if err := rows.Scan(&site.ID, &site.Domain, &site.WebRoot, &site.LogDir); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

func scanSiteSuspiciousRuntimeFiles(db *sql.DB, site fileSecuritySite) (int, error) {
	webRoot, err := safeSiteWebRoot(site.WebRoot)
	if err != nil {
		return 0, nil
	}

	seen := map[string]bool{}
	count := 0
	for _, dir := range []string{"uploads", "cache", "languages", "wflogs"} {
		root := filepath.Join(webRoot, "wp-content", dir)
		if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !IsWPExecutableFile(path) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			relPath := siteRelativeSlashPath(webRoot, path)
			if relPath == "" {
				return nil
			}
			seen[relPath] = true
			count++
			return upsertFileSecurityEvent(db, fileSecurityRecord{
				SiteID:     site.ID,
				Domain:     site.Domain,
				EventType:  FileSecurityEventSuspiciousFile,
				Source:     "scanner",
				RiskLevel:  "high",
				Path:       relPath,
				FileSize:   info.Size(),
				FileMTime:  formatEventTime(info.ModTime()),
				Message:    "运行数据目录中发现 PHP 可执行文件。Nginx 会阻止直接执行，请确认来源后删除或隔离。",
				FirstSeen:  formatEventTime(time.Now()),
				LastSeen:   formatEventTime(time.Now()),
				EventCount: 1,
			})
		})
		if err != nil {
			return count, err
		}
	}
	if err := markResolvedSuspiciousRuntimeFiles(db, site.ID, seen); err != nil {
		return count, err
	}
	return count, nil
}

func importSiteRuntimePHPAccessEvents(db *sql.DB, site fileSecuritySite) (int, error) {
	if !fileSecurityLogDirAllowed(site.LogDir) {
		return 0, nil
	}

	aggregates := map[string]fileSecurityRecord{}
	for _, line := range tailLogLines(filepath.Join(site.LogDir, "wp-security.log"), 2000) {
		m := fileSecurityCombinedLogRe.FindStringSubmatch(line)
		if len(m) != 7 {
			continue
		}
		requestPath := runtimePHPRequestPath(m[4])
		if requestPath == "" {
			continue
		}
		status, _ := strconv.Atoi(m[5])
		seenAt := formatEventTime(parseNginxAccessTime(m[2]))
		if seenAt == "" {
			seenAt = formatEventTime(time.Now())
		}
		key := strings.Join([]string{m[1], m[3], requestPath}, "\x00")
		record := aggregates[key]
		if record.EventCount == 0 {
			record = fileSecurityRecord{
				SiteID:        site.ID,
				Domain:        site.Domain,
				EventType:     FileSecurityEventRuntimePHPAccess,
				Source:        "nginx",
				RiskLevel:     "high",
				Path:          requestPath,
				RequestMethod: m[3],
				IPAddress:     m[1],
				UserAgent:     m[6],
				Status:        status,
				Message:       "运行数据目录 PHP 执行请求已被 Nginx 拦截。建议检查是否存在同名可疑文件，并结合 IP 来源判断是否封禁。",
				FirstSeen:     seenAt,
				LastSeen:      seenAt,
			}
		}
		record.EventCount++
		if seenAt < record.FirstSeen {
			record.FirstSeen = seenAt
		}
		if seenAt > record.LastSeen {
			record.LastSeen = seenAt
		}
		aggregates[key] = record
	}

	count := 0
	for _, record := range aggregates {
		if err := upsertFileSecurityEvent(db, record); err != nil {
			return count, err
		}
		count += record.EventCount
	}
	return count, nil
}

func upsertFileSecurityEvent(db *sql.DB, event fileSecurityRecord) error {
	if event.EventCount <= 0 {
		event.EventCount = 1
	}
	now := formatEventTime(time.Now())
	if event.FirstSeen == "" {
		event.FirstSeen = now
	}
	if event.LastSeen == "" {
		event.LastSeen = now
	}

	_, err := db.Exec(`INSERT INTO file_security_events (
			site_id, domain, event_type, source, risk_level, path, request_method, ip_address,
			user_agent, status, file_size, file_mtime, message, first_seen, last_seen, event_count, resolved_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULL, CURRENT_TIMESTAMP)
		ON CONFLICT(site_id, event_type, path, ip_address, request_method) DO UPDATE SET
			domain = excluded.domain,
			source = excluded.source,
			risk_level = excluded.risk_level,
			user_agent = excluded.user_agent,
			status = excluded.status,
			file_size = excluded.file_size,
			file_mtime = excluded.file_mtime,
			message = excluded.message,
			first_seen = CASE
				WHEN file_security_events.first_seen = '' OR excluded.first_seen < file_security_events.first_seen THEN excluded.first_seen
				ELSE file_security_events.first_seen
			END,
			last_seen = CASE
				WHEN excluded.last_seen > file_security_events.last_seen THEN excluded.last_seen
				ELSE file_security_events.last_seen
			END,
			event_count = CASE
				WHEN excluded.event_count > file_security_events.event_count THEN excluded.event_count
				ELSE file_security_events.event_count
			END,
			resolved_at = NULL,
			updated_at = CURRENT_TIMESTAMP`,
		event.SiteID, event.Domain, event.EventType, event.Source, event.RiskLevel, event.Path,
		event.RequestMethod, event.IPAddress, event.UserAgent, event.Status, event.FileSize,
		event.FileMTime, event.Message, event.FirstSeen, event.LastSeen, event.EventCount)
	return err
}

func markResolvedSuspiciousRuntimeFiles(db *sql.DB, siteID int, seen map[string]bool) error {
	rows, err := db.Query(`SELECT path FROM file_security_events
		WHERE site_id = ? AND event_type = ? AND resolved_at IS NULL`, siteID, FileSecurityEventSuspiciousFile)
	if err != nil {
		return err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return err
		}
		if !seen[p] {
			stale = append(stale, p)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range stale {
		if _, err := db.Exec(`UPDATE file_security_events
			SET resolved_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
			WHERE site_id = ? AND event_type = ? AND path = ? AND resolved_at IS NULL`,
			siteID, FileSecurityEventSuspiciousFile, p); err != nil {
			return err
		}
	}
	return nil
}

func runtimePHPRequestPath(rawURI string) string {
	parsed, err := url.ParseRequestURI(rawURI)
	if err != nil || parsed.Path == "" {
		return ""
	}
	p := urlpath.Clean("/" + strings.TrimPrefix(parsed.Path, "/"))
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) < 3 || parts[0] != "wp-content" {
		return ""
	}
	switch parts[1] {
	case "plugins", "themes", "mu-plugins":
		return ""
	}
	if !IsWPExecutableFile(parts[len(parts)-1]) {
		return ""
	}
	return p
}

func siteRelativeSlashPath(webRoot, targetPath string) string {
	rel, err := filepath.Rel(webRoot, targetPath)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return ""
	}
	return "/" + rel
}

func formatEventTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
