package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

const guardCommandTimeout = 5 * time.Second

var (
	guardCommand            = runGuardCommand
	serviceIncidentNotifier = sendServiceIncidentNotification
)

type GuardService struct {
	Name         string `json:"name"`
	ServiceName  string `json:"service"`
	Running      bool   `json:"running"`
	Paused       bool   `json:"paused"`
	Restarts     int    `json:"restarts"`
	LastIncident string `json:"last_incident"`
	restartCount uint64
	countKnown   bool
}

type ProcessGuard struct {
	mu         sync.RWMutex
	services   []*GuardService
	stopCh     chan struct{}
	firstRun   bool
	pausedFile string
}

var guard *ProcessGuard

func init() {
	guard = &ProcessGuard{
		services: []*GuardService{
			{Name: "Nginx", ServiceName: "nginx"},
			{Name: "PHP-FPM", ServiceName: "php8.3-fpm"},
			{Name: "MariaDB", ServiceName: "mariadb"},
			{Name: "Redis", ServiceName: "redis-server"},
			{Name: "nftables", ServiceName: "nftables"},
			{Name: "Fail2ban", ServiceName: "fail2ban"},
		},
		stopCh:     make(chan struct{}),
		firstRun:   true,
		pausedFile: "/www/server/panel/guard_paused.json",
	}
	guard.loadPaused()
}

func StartProcessGuard() {
	go guard.loop()
}

func GetGuardStatus() []GuardService {
	guard.mu.RLock()
	defer guard.mu.RUnlock()
	result := make([]GuardService, len(guard.services))
	for i, s := range guard.services {
		result[i] = GuardService{
			Name:         s.Name,
			ServiceName:  s.ServiceName,
			Running:      s.Running,
			Paused:       s.Paused,
			Restarts:     s.Restarts,
			LastIncident: s.LastIncident,
		}
	}
	return result
}

func SetServiceState(serviceName, action string) error {
	guard.mu.Lock()
	defer guard.mu.Unlock()

	var s *GuardService
	for _, svc := range guard.services {
		if svc.ServiceName == serviceName {
			s = svc
			break
		}
	}
	if s == nil {
		return nil
	}

	switch action {
	case "start":
		if out, err := exec.Command("systemctl", "start", serviceName).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
		}
		s.Paused = false
		time.Sleep(300 * time.Millisecond)
		out, _ := exec.Command("systemctl", "is-active", serviceName).Output()
		s.Running = strings.TrimSpace(string(out)) == "active"
	case "stop":
		s.Paused = true
		if out, err := exec.Command("systemctl", "stop", serviceName).CombinedOutput(); err != nil {
			s.Paused = false
			return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
		}
		s.Running = false
	case "restart":
		if out, err := exec.Command("systemctl", "restart", serviceName).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
		}
		s.Paused = false
		time.Sleep(300 * time.Millisecond)
		out, _ := exec.Command("systemctl", "is-active", serviceName).Output()
		s.Running = strings.TrimSpace(string(out)) == "active"
	}

	guard.savePaused()
	return nil
}

func (pg *ProcessGuard) loop() {
	pg.checkAll()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pg.checkAll()
		case <-pg.stopCh:
			return
		}
	}
}

func (pg *ProcessGuard) checkAll() {
	for _, s := range pg.services {
		pg.check(s)
	}
	pg.firstRun = false
}

func (pg *ProcessGuard) check(s *GuardService) {
	pg.mu.Lock()
	state := readGuardServiceState(s.ServiceName)
	if !state.valid {
		pg.mu.Unlock()
		return
	}
	active := state.active
	wasRunning := s.Running
	s.Running = active

	if s.Paused {
		pg.mu.Unlock()
		if active {
			logIncident(s, "unexpected_active", s.Name+" 在暂停守护期间被外部启动", false)
		}
		return
	}

	var incidentEvent string
	var incidentState guardServiceState
	recovered := false
	if !s.countKnown {
		s.restartCount = state.restarts
		s.countKnown = state.valid
	} else if state.valid && state.restarts > s.restartCount {
		s.Restarts += int(state.restarts - s.restartCount)
		s.restartCount = state.restarts
		s.LastIncident = time.Now().Format("2006-01-02 15:04:05")
		incidentEvent = "auto_restart"
		incidentState = state
		recovered = true
	} else if state.valid {
		s.restartCount = state.restarts
	}

	if !active {
		if pg.firstRun {
			pg.mu.Unlock()
			return
		}
		_, _ = guardCommand("systemctl", "start", s.ServiceName)
		recovered = readGuardServiceState(s.ServiceName).active
		s.Running = recovered
		if wasRunning {
			s.Restarts++
			now := time.Now().Format("2006-01-02 15:04:05")
			s.LastIncident = now
			incidentEvent = "restart"
			incidentState = state
		}
	}
	pg.mu.Unlock()

	if incidentEvent != "" {
		message := diagnoseServiceIncident(s, incidentState, recovered)
		logIncident(s, incidentEvent, message, recovered)
	}
}

func (pg *ProcessGuard) loadPaused() {
	data, err := os.ReadFile(pg.pausedFile)
	if err != nil {
		return
	}
	var paused map[string]bool
	if json.Unmarshal(data, &paused) != nil {
		return
	}
	for _, s := range pg.services {
		if v, ok := paused[s.ServiceName]; ok {
			s.Paused = v
		}
	}
}

func (pg *ProcessGuard) savePaused() {
	paused := make(map[string]bool)
	for _, s := range pg.services {
		paused[s.ServiceName] = s.Paused
	}
	data, _ := json.Marshal(paused)
	os.WriteFile(pg.pausedFile, data, 0600)
}

type guardServiceState struct {
	active     bool
	valid      bool
	restarts   uint64
	result     string
	exitCode   string
	exitStatus string
}

func readGuardServiceState(service string) guardServiceState {
	out, err := guardCommand(
		"systemctl", "show", service,
		"--property=ActiveState",
		"--property=NRestarts",
		"--property=Result",
		"--property=ExecMainCode",
		"--property=ExecMainStatus",
	)
	if err != nil {
		return guardServiceState{}
	}
	state := guardServiceState{valid: true}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			state.active = value == "active"
		case "NRestarts":
			state.restarts, _ = strconv.ParseUint(value, 10, 64)
		case "Result":
			state.result = value
		case "ExecMainCode":
			state.exitCode = value
		case "ExecMainStatus":
			state.exitStatus = value
		}
	}
	return state
}

func diagnoseServiceIncident(s *GuardService, state guardServiceState, recovered bool) string {
	out, _ := guardCommand(
		"journalctl", "-u", s.ServiceName, "--since", "2 minutes ago",
		"--no-pager", "-n", "30",
	)
	reason := classifyServiceFailure(string(out), state)
	recovery := "自动恢复成功"
	if !recovered {
		recovery = "自动恢复失败"
	}
	return fmt.Sprintf("%s 进程异常退出，%s；初步原因：%s", s.Name, recovery, reason)
}

func classifyServiceFailure(journal string, state guardServiceState) string {
	text := strings.ToLower(journal)
	switch {
	case strings.Contains(text, "out of memory") || strings.Contains(text, "oom-kill"):
		return "系统内存耗尽（OOM）"
	case strings.Contains(text, "segfault") || strings.Contains(text, "segmentation fault"):
		return "进程发生崩溃（段错误）"
	case strings.Contains(text, "address already in use"):
		return "端口被占用"
	case strings.Contains(text, "permission denied"):
		return "权限不足"
	case strings.Contains(text, "configuration file") && strings.Contains(text, "error"),
		strings.Contains(text, "syntax error"),
		strings.Contains(text, "test failed"):
		return "配置检查失败"
	case state.exitCode == "killed" || strings.Contains(text, "code=killed"):
		return "进程被信号强制终止"
	case state.exitStatus != "" && state.exitStatus != "0":
		return fmt.Sprintf("进程异常退出（状态码 %s）", state.exitStatus)
	case state.result != "" && state.result != "success":
		return "服务运行失败（" + state.result + "）"
	default:
		return "服务意外停止，系统日志未提供明确原因"
	}
}

func runGuardCommand(name string, args ...string) ([]byte, error) {
	return runGuardCommandWithTimeout(guardCommandTimeout, name, args...)
}

func runGuardCommandWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func logIncident(s *GuardService, event, message string, notify bool) {
	db := database.GetDB()
	if db == nil {
		return
	}
	coreIncident := event != "unexpected_active" && isCoreGuardService(s.ServiceName)
	if coreIncident {
		if snapshot := captureIncidentResourceSnapshot(); snapshot != "" {
			message += "；" + snapshot
		}
	}
	_, err := db.Exec(
		"INSERT INTO process_guard_incidents (service, event, message) VALUES (?, ?, ?)",
		s.Name, event, message,
	)
	if err != nil {
		log.Printf("记录进程守护事件失败: %v", err)
	}
	pruneIncidents(db)
	if coreIncident && isRuleEnabled("alert_service") {
		if notify && !isAlertRuntimeFiring("alert_service") {
			serviceIncidentNotifier(s.Name, message)
		}
	}
}

func isCoreGuardService(service string) bool {
	switch service {
	case "nginx", "php8.3-fpm", "mariadb", "redis-server":
		return true
	default:
		return false
	}
}

func sendServiceIncidentNotification(service, message string) {
	sendResolvedAlertEvent("alert_service", service+" 服务异常", message, "请查看服务日志和自动恢复结果。")
}

func pruneIncidents(db *sql.DB) {
	db.Exec("DELETE FROM process_guard_incidents WHERE id NOT IN (SELECT id FROM process_guard_incidents ORDER BY id DESC LIMIT 500)")
}
