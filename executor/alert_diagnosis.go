package executor

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

// AlertDiagnosisContext is the server-validated, read-only context selected by
// the administrator. It deliberately keeps the alert ID as the stable handle;
// the browser never supplies the message or time range.
type AlertDiagnosisContext struct {
	Domain           string    `json:"-"`
	ID               int       `json:"id"`
	AlertType        string    `json:"alert_type"`
	Message          string    `json:"message"`
	Attribution      string    `json:"attribution"`
	CreatedAt        time.Time `json:"created_at"`
	AttributionStart time.Time `json:"attribution_start"`
	AttributionEnd   time.Time `json:"attribution_end"`
	LogStart         time.Time `json:"log_start"`
	LogEnd           time.Time `json:"log_end"`
	Relation         string    `json:"relation"`
}

var alertWindowPattern = regexp.MustCompile(`(?m)(\d{4}-\d{2}-\d{2} \d{2}:\d{2})–(?:(\d{4}-\d{2}-\d{2} )?(\d{2}:\d{2}))`)

func AlertDiagnosisCandidates(db *sql.DB, site *models.Website, limit int) ([]AlertDiagnosisContext, error) {
	if db == nil || site == nil || site.ID <= 0 || strings.TrimSpace(site.Domain) == "" {
		return nil, fmt.Errorf("invalid alert diagnosis scope")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := db.Query(`SELECT id, alert_type, message, COALESCE(attribution,''), created_at
		FROM alert_log WHERE created_at >= datetime('now','-90 days')
		AND alert_type IN ('alert_cpu','alert_memory','alert_service','alert_oom')
		ORDER BY id DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AlertDiagnosisContext, 0, limit)
	for rows.Next() {
		var item AlertDiagnosisContext
		var createdValue any
		if err := rows.Scan(&item.ID, &item.AlertType, &item.Message, &item.Attribution, &createdValue); err != nil {
			continue
		}
		item.CreatedAt, err = parseAlertSQLiteValue(createdValue)
		if err != nil {
			continue
		}
		parsed, ok := parseAlertDiagnosisContext(item, site.Domain)
		if !ok {
			continue
		}
		parsed.Domain = site.Domain
		result = append(result, parsed)
		if len(result) >= limit {
			break
		}
	}
	return result, rows.Err()
}

func LoadAlertDiagnosisContext(db *sql.DB, site *models.Website, alertID int) (AlertDiagnosisContext, error) {
	if db == nil || site == nil || site.ID <= 0 || alertID <= 0 {
		return AlertDiagnosisContext{}, fmt.Errorf("invalid alert diagnosis scope")
	}
	var item AlertDiagnosisContext
	var createdValue any
	err := db.QueryRow(`SELECT id, alert_type, message, COALESCE(attribution,''), created_at
		FROM alert_log WHERE id=?`, alertID).Scan(&item.ID, &item.AlertType, &item.Message, &item.Attribution, &createdValue)
	if err != nil {
		return item, err
	}
	item.CreatedAt, err = parseAlertSQLiteValue(createdValue)
	if err != nil {
		return item, err
	}
	parsed, ok := parseAlertDiagnosisContext(item, site.Domain)
	if !ok {
		return item, fmt.Errorf("alert is not diagnosable for this website")
	}
	parsed.Domain = site.Domain
	return parsed, nil
}

func parseAlertDiagnosisContext(item AlertDiagnosisContext, domain string) (AlertDiagnosisContext, bool) {
	if item.AlertType != "alert_cpu" && item.AlertType != "alert_memory" && item.AlertType != "alert_service" && item.AlertType != "alert_oom" {
		return item, false
	}
	attr := strings.TrimSpace(item.Attribution)
	if attr == "" || !alertContainsExactDomain(attr, domain) {
		return item, false
	}
	m := alertWindowPattern.FindStringSubmatch(attr)
	if len(m) != 4 {
		return item, false
	}
	start, err := time.ParseInLocation("2006-01-02 15:04", m[1], time.Local)
	if err != nil {
		return item, false
	}
	endText := m[2] + m[3]
	if m[2] == "" {
		endText = start.Format("2006-01-02 ") + m[3]
	}
	end, err := time.ParseInLocation("2006-01-02 15:04", endText, time.Local)
	if err != nil || !start.Before(end) {
		return item, false
	}
	created := item.CreatedAt.In(time.Local)
	if end.Before(created.Add(-30*time.Minute)) || end.After(created.Add(5*time.Minute)) {
		return item, false
	}
	item.AttributionStart, item.AttributionEnd = start, end
	item.LogStart, item.LogEnd = start.Add(-5*time.Minute), end.Add(5*time.Minute)
	if alertHasDomainAfter(attr, "初步归因：", domain) || alertHasDomainAfter(attr, "被终止进程所属网站：", domain) {
		item.Relation = "attribution"
	} else {
		item.Relation = "clue"
	}
	return item, true
}

func alertHasDomainAfter(value, prefix, domain string) bool {
	r := regexp.MustCompile(regexp.QuoteMeta(prefix) + `\s*` + regexp.QuoteMeta(strings.TrimSpace(domain)) + `(?:\s|（|约|、|；|：|:|$)`)
	return r.MatchString(value)
}

func alertContainsExactDomain(value, domain string) bool {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return false
	}
	r := regexp.MustCompile(`(?m)(?:^|[、；：:\n])\s*` + regexp.QuoteMeta(domain) + `(?:\s|（|约|$)`)
	return r.MatchString(value)
}

func parseAlertSQLiteTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return t.In(time.Local), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid alert time")
}

func parseAlertSQLiteValue(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v.In(time.Local), nil
	case string:
		return parseAlertSQLiteTime(v)
	case []byte:
		return parseAlertSQLiteTime(string(v))
	default:
		return time.Time{}, fmt.Errorf("invalid alert time")
	}
}

func AlertDiagnosisTypeLabel(alertType string) string {
	switch alertType {
	case "alert_cpu":
		return "CPU 使用率告警"
	case "alert_memory":
		return "内存告警"
	case "alert_service":
		return "服务异常告警"
	case "alert_oom":
		return "系统内存耗尽告警"
	default:
		return alertType
	}
}

func AlertDiagnosisFormatTime(t time.Time) string {
	return t.In(time.Local).Format("2006-01-02 15:04")
}

func AlertDiagnosisRelationLabel(relation string) string {
	if relation == "clue" {
		return "线索名单之一"
	}
	return "初步归因对象"
}

func AlertDiagnosisFormatWindow(start, end time.Time) string {
	return AlertDiagnosisFormatTime(start) + "–" + AlertDiagnosisFormatTime(end)
}
