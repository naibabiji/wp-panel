package executor

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

const oomJournalLookback = 2 * time.Minute

var oomKilledProcessPattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s+.*Out of memory: Killed process ([0-9]+) \(([^)]+)\)`)

type oomEvent struct {
	Key        string
	Process    string
	PID        int
	OccurredAt time.Time
}

// StartOOMMonitor records kernel OOM kills independently from resource threshold alerts.
func StartOOMMonitor() {
	go func() {
		checkOOMEvents()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			checkOOMEvents()
		}
	}()
}

func checkOOMEvents() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	since := fmt.Sprintf("%d seconds ago", int(oomJournalLookback.Seconds()))
	out, err := exec.CommandContext(ctx, "journalctl", "-k", "--since", since, "--no-pager", "-o", "short-unix").Output()
	if err != nil {
		log.Printf("[OOM监控] 读取内核日志失败: %v", err)
		return
	}
	for _, event := range parseOOMEvents(string(out)) {
		if err := recordOOMEvent(event); err != nil {
			log.Printf("[OOM监控] 记录事件失败: %v", err)
		}
	}
}

func parseOOMEvents(output string) []oomEvent {
	var events []oomEvent
	for _, line := range strings.Split(output, "\n") {
		match := oomKilledProcessPattern.FindStringSubmatch(line)
		if len(match) != 4 {
			continue
		}
		unixSeconds, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		occurredAt := time.Unix(0, int64(unixSeconds*float64(time.Second))).UTC()
		events = append(events, oomEvent{
			Key:        fmt.Sprintf("%s:%d:%s", match[1], pid, match[3]),
			Process:    match[3],
			PID:        pid,
			OccurredAt: occurredAt,
		})
	}
	return events
}

func recordOOMEvent(event oomEvent) error {
	process := oomProcessLabel(event.Process)
	message := fmt.Sprintf(
		"系统内存耗尽，内核强制终止 %s（PID %d），发生于 %s；服务可能已由 systemd 自动重启",
		process, event.PID, event.OccurredAt.Local().Format("2006-01-02 15:04:05"),
	)
	result, err := database.GetDB().Exec(
		`INSERT OR IGNORE INTO system_oom_events
		 (event_key, process, pid, message, occurred_at) VALUES (?, ?, ?, ?, ?)`,
		event.Key, event.Process, event.PID, message, event.OccurredAt,
	)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 || !isRuleEnabled("alert_oom") {
		return err
	}

	if _, err := database.GetDB().Exec(
		"INSERT INTO alert_log (alert_type, level, message, created_at) VALUES (?, ?, ?, ?)",
		"alert_oom", "critical", message, event.OccurredAt,
	); err != nil {
		return err
	}
	database.GetDB().Exec("DELETE FROM alert_log WHERE id NOT IN (SELECT id FROM alert_log ORDER BY id DESC LIMIT 30)")
	title := getPanelTitle() + " 告警 — 系统内存耗尽"
	if cfg := GetSMTPConfig(); cfg != nil && cfg.Host != "" && cfg.AdminEmail != "" {
		go SendMail("", title, formatEmailHTML("系统内存耗尽", message, "请检查当时的内存占用和相关服务日志。", true))
	}
	if cfg := GetWebhookConfig(); cfg != nil && cfg.Enabled == "true" && cfg.URL != "" {
		go SendWebhook(title, message)
	}
	return nil
}

func oomProcessLabel(process string) string {
	switch {
	case process == "mariadbd" || process == "mysqld":
		return "MariaDB"
	case strings.HasPrefix(process, "php-fpm"):
		return "PHP-FPM"
	case process == "redis-server":
		return "Redis"
	case process == "nginx":
		return "Nginx"
	case process == "wp-panel":
		return "WP Panel"
	default:
		return process
	}
}
