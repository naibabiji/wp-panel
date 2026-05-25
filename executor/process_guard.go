package executor

import (
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

type GuardService struct {
	Name         string `json:"name"`
	ServiceName  string `json:"service"`
	Running      bool   `json:"running"`
	Paused       bool   `json:"paused"`
	Restarts     int    `json:"restarts"`
	LastIncident string `json:"last_incident"`
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
		if err := exec.Command("systemctl", "start", serviceName).Run(); err != nil {
			return err
		}
		s.Paused = false
		time.Sleep(300 * time.Millisecond)
		out, _ := exec.Command("systemctl", "is-active", serviceName).Output()
		s.Running = strings.TrimSpace(string(out)) == "active"
	case "stop":
		s.Paused = true
		if err := exec.Command("systemctl", "stop", serviceName).Run(); err != nil {
			s.Paused = false
			return err
		}
		s.Running = false
	case "restart":
		if err := exec.Command("systemctl", "restart", serviceName).Run(); err != nil {
			return err
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
	out, err := exec.Command("systemctl", "is-active", s.ServiceName).Output()
	active := err == nil && strings.TrimSpace(string(out)) == "active"

	pg.mu.Lock()
	wasRunning := s.Running
	s.Running = active

	if s.Paused {
		if active {
			logIncident(s, "unexpected_active")
		}
		pg.mu.Unlock()
		return
	}

	if !active {
		if pg.firstRun {
			pg.mu.Unlock()
			return
		}
		exec.Command("systemctl", "start", s.ServiceName).Run()
		if wasRunning {
			s.Restarts++
			now := time.Now().Format("2006-01-02 15:04:05")
			s.LastIncident = now
			logIncident(s, "restart")
		}
	}
	pg.mu.Unlock()
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

func logIncident(s *GuardService, event string) {
	db := database.GetDB()
	if db == nil {
		return
	}
	_, err := db.Exec(
		"INSERT INTO process_guard_incidents (service, event, message) VALUES (?, ?, ?)",
		s.Name, event, s.Name+" 进程异常退出，已自动重启",
	)
	if err != nil {
		log.Printf("记录进程守护事件失败: %v", err)
	}
	pruneIncidents(db)
}

func pruneIncidents(db *sql.DB) {
	db.Exec("DELETE FROM process_guard_incidents WHERE id NOT IN (SELECT id FROM process_guard_incidents ORDER BY id DESC LIMIT 500)")
}
