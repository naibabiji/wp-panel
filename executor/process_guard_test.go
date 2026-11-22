package executor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func setupServiceControlTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldCfg, oldDPKG := config.AppConfig, maintenanceDPKGQuery
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: dir}}
	maintenanceDPKGQuery = func() (bool, error) { return false, nil }
	t.Cleanup(func() { config.AppConfig, maintenanceDPKGQuery = oldCfg, oldDPKG })
	return dir
}

func setupPlannedGuard(t *testing.T) (string, *ProcessGuard, *GuardService, *int) {
	t.Helper()
	dir := setupServiceControlTest(t)
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE process_guard_incidents (id INTEGER PRIMARY KEY,service TEXT,event TEXT,message TEXT,created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY,svalue TEXT)`)
	notifications := new(int)
	oldNotifier := serviceIncidentNotifier
	serviceIncidentNotifier = func(string, string) { *notifications++ }
	t.Cleanup(func() { serviceIncidentNotifier = oldNotifier })
	s := &GuardService{Name: "MariaDB", ServiceName: "mariadb", Running: true, Restarts: 2, LastIncident: "old", restartCount: 3, countKnown: true}
	pg := &ProcessGuard{services: []*GuardService{s}}
	return dir, pg, s, notifications
}

func mockPlannedGuardCommand(t *testing.T, fn func(string, ...string) ([]byte, error)) {
	t.Helper()
	old := guardCommand
	guardCommand = fn
	t.Cleanup(func() { guardCommand = old })
}

func TestProcessGuardPlannedInterruptionRetainsStateAndRebaselines(t *testing.T) {
	dir, pg, s, _ := setupPlannedGuard(t)
	oldGuard := guard
	guard = pg
	t.Cleanup(func() { guard = oldGuard })
	for _, running := range []bool{true, false} {
		s.Running = running
		release, err := tryServiceControlLocks(dir, "mariadb")
		if err != nil {
			t.Fatal(err)
		}
		mockPlannedGuardCommand(t, func(string, ...string) ([]byte, error) {
			t.Fatal("queried service owned by planned operation")
			return nil, nil
		})
		pg.check(s)
		release()
		if s.Running != running || s.Restarts != 2 || s.LastIncident != "old" || !s.rebaselineAfterPlannedOperation {
			t.Fatalf("frozen state changed: %+v", s)
		}
		if firing, _ := checkService(); firing == running {
			t.Fatal("planned interval changed pre-existing alert condition")
		}
	}
	mockPlannedGuardCommand(t, func(string, ...string) ([]byte, error) { return nil, errors.New("query unavailable") })
	pg.check(s)
	if !s.rebaselineAfterPlannedOperation {
		t.Fatal("query failure discarded rebaseline")
	}
	mockPlannedGuardCommand(t, func(_ string, args ...string) ([]byte, error) {
		if other, err := tryServiceControlLocks(dir, "mariadb"); !errors.Is(err, errServiceControlBusy) {
			if other != nil {
				other()
			}
			t.Fatalf("read without held service lock: %v", err)
		}
		return []byte("ActiveState=active\nNRestarts=7\n"), nil
	})
	pg.check(s)
	if !s.Running || s.restartCount != 7 || s.Restarts != 2 || s.LastIncident != "old" || s.rebaselineAfterPlannedOperation {
		t.Fatalf("bad resumed state: %+v", s)
	}
	if firing, _ := checkService(); firing {
		t.Fatal("real recovery did not clear service condition")
	}
}

func TestProcessGuardPausedAndShortPlannedOperations(t *testing.T) {
	_, pg, s, _ := setupPlannedGuard(t)
	s.Paused = true
	s.Running = false
	s.rebaselineAfterPlannedOperation = true
	mockPlannedGuardCommand(t, func(_ string, args ...string) ([]byte, error) {
		if args[0] == "start" {
			t.Fatal("started paused service")
		}
		return []byte("ActiveState=inactive\nNRestarts=0\n"), nil
	})
	pg.check(s)
	if s.Running || s.Restarts != 2 || s.rebaselineAfterPlannedOperation {
		t.Fatalf("paused state changed: %+v", s)
	}
	// A controlled restart between guard ticks resets systemd's automatic
	// restart counter; it is not an increase and must retain incident history.
	s.Paused = false
	mockPlannedGuardCommand(t, func(string, ...string) ([]byte, error) { return []byte("ActiveState=active\nNRestarts=0\n"), nil })
	pg.check(s)
	if !s.Running || s.Restarts != 2 || s.LastIncident != "old" {
		t.Fatalf("short operation misreported: %+v", s)
	}
}

func TestSetServiceStateBusyDoesNotInvokeCommands(t *testing.T) {
	dir := setupServiceControlTest(t)
	release, err := tryServiceControlLocks(dir, "mariadb")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mockPlannedGuardCommand(t, func(string, ...string) ([]byte, error) {
		t.Fatal("manual action ran through service owner")
		return nil, nil
	})
	if err := SetServiceState("mariadb", "restart"); !errors.Is(err, errServiceControlBusy) {
		t.Fatal(err)
	}
}

func TestProcessGuardPlannedFailureIsVisibleEvenOnFirstRun(t *testing.T) {
	for _, recovered := range []bool{true, false} {
		t.Run(fmt.Sprintf("recovered_%v", recovered), func(t *testing.T) {
			dir, pg, s, _ := setupPlannedGuard(t)
			s.Running = false
			s.Restarts = 0
			pg.firstRun = true
			release, err := tryServiceControlLocks(dir, "mariadb")
			if err != nil {
				t.Fatal(err)
			}
			pg.check(s)
			release()
			starts := 0
			mockPlannedGuardCommand(t, func(name string, args ...string) ([]byte, error) {
				if name == "journalctl" {
					return nil, nil
				}
				if strings.Join(args, " ") == "show --property=SystemState --value" {
					return []byte("running"), nil
				}
				if args[0] == "start" {
					starts++
					if other, err := tryServiceControlLocks(dir, "mariadb"); !errors.Is(err, errServiceControlBusy) {
						if other != nil {
							other()
						}
						t.Fatal("start without service lock")
					}
					return nil, nil
				}
				state := "failed"
				if starts > 0 && recovered {
					state = "active"
				}
				return []byte("ActiveState=" + state + "\nNRestarts=0\n"), nil
			})
			pg.check(s)
			if starts != 1 || s.Running != recovered || s.Restarts != 1 {
				t.Fatalf("starts=%d service=%+v", starts, s)
			}
			var message string
			if err := database.GetDB().QueryRow(`SELECT message FROM process_guard_incidents`).Scan(&message); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(message, "计划操作结束后服务仍未运行") || strings.Contains(message, "进程异常退出") {
				t.Fatal(message)
			}
		})
	}
}

func TestProcessGuardTransitionsKeepRealRestartEvidence(t *testing.T) {
	for _, state := range []string{"activating", "deactivating", "reloading"} {
		t.Run(state, func(t *testing.T) {
			_, pg, s, _ := setupPlannedGuard(t)
			mockPlannedGuardCommand(t, func(string, ...string) ([]byte, error) {
				return []byte("ActiveState=" + state + "\nNRestarts=5\n"), nil
			})
			pg.check(s)
			if !s.Running || s.restartCount != 3 || s.Restarts != 2 || s.rebaselineAfterPlannedOperation {
				t.Fatalf("transition altered baseline: %+v", s)
			}
			mockPlannedGuardCommand(t, func(string, ...string) ([]byte, error) { return []byte("ActiveState=active\nNRestarts=5\n"), nil })
			pg.check(s)
			if s.Restarts != 4 {
				t.Fatalf("real automatic restart lost: %+v", s)
			}
		})
	}
}

func TestProcessGuardDoesNotStartDuringShutdownOrDPKG(t *testing.T) {
	for _, reason := range []string{"shutdown", "dpkg", "dpkg_error"} {
		t.Run(reason, func(t *testing.T) {
			_, pg, s, _ := setupPlannedGuard(t)
			if reason != "shutdown" {
				maintenanceDPKGQuery = func() (bool, error) {
					if reason == "dpkg_error" {
						return false, errors.New("unavailable")
					}
					return true, nil
				}
			}
			mockPlannedGuardCommand(t, func(_ string, args ...string) ([]byte, error) {
				if args[0] == "start" {
					t.Fatal("start during shutdown/package operation")
				}
				if strings.Join(args, " ") == "show --property=SystemState --value" {
					return []byte("stopping"), nil
				}
				return []byte("ActiveState=inactive\nNRestarts=0\n"), nil
			})
			pg.check(s)
			if !s.Running || s.Restarts != 2 {
				t.Fatalf("unsafe sample changed state: %+v", s)
			}
			if reason == "shutdown" {
				pg.checkAll()
				if !s.Running {
					t.Fatal("shutdown cycle changed state")
				}
			}
		})
	}
}

func TestSetServiceStateRejectsUnknownService(t *testing.T) {
	err := SetServiceState("not-managed", "restart")
	if !errors.Is(err, ErrUnknownGuardService) {
		t.Fatalf("error=%v, want ErrUnknownGuardService", err)
	}
}

func TestSetServiceStateRejectsCommandSuccessWithoutActiveState(t *testing.T) {
	setupServiceControlTest(t)
	oldCommand := guardCommand
	oldTimeout, oldPoll := guardStateWaitTimeout, guardStatePollInterval
	guardCommand = func(_ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "show" {
			return []byte("ActiveState=failed\nNRestarts=0\n"), nil
		}
		return nil, nil
	}
	guardStateWaitTimeout, guardStatePollInterval = 5*time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		guardCommand = oldCommand
		guardStateWaitTimeout, guardStatePollInterval = oldTimeout, oldPoll
	})

	err := SetServiceState("nginx", "start")
	if err == nil || !strings.Contains(err.Error(), "目标状态") {
		t.Fatalf("error=%v, want final-state failure", err)
	}
}

func TestSetServiceStateRestoresServiceWhenPauseFileCannotBeSaved(t *testing.T) {
	setupServiceControlTest(t)
	oldCommand := guardCommand
	oldPath := guard.pausedFile
	service := guard.services[0]
	oldPaused, oldRunning := service.Paused, service.Running
	service.Paused, service.Running = false, true
	guard.pausedFile = filepath.Join(t.TempDir(), "missing", "guard.json")
	active := true
	guardCommand = func(_ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "stop" {
			active = false
		}
		if len(args) > 0 && args[0] == "start" {
			active = true
		}
		if len(args) > 0 && args[0] == "show" {
			state := "inactive"
			if active {
				state = "active"
			}
			return []byte("ActiveState=" + state + "\nNRestarts=0\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		guardCommand = oldCommand
		guard.pausedFile = oldPath
		service.Paused, service.Running = oldPaused, oldRunning
	})

	err := SetServiceState("nginx", "stop")
	if err == nil || !active || service.Paused || !service.Running {
		t.Fatalf("error=%v active=%v paused=%v running=%v", err, active, service.Paused, service.Running)
	}
}

func TestClassifyServiceFailure(t *testing.T) {
	tests := []struct {
		name    string
		journal string
		state   guardServiceState
		want    string
	}{
		{name: "oom", journal: "kernel: Out of memory: Killed process 10 (mariadbd)", want: "系统内存耗尽（OOM）"},
		{name: "segfault", journal: "php-fpm[10]: segfault at 0", want: "进程发生崩溃（段错误）"},
		{name: "port", journal: "listen() failed: Address already in use", want: "端口被占用"},
		{name: "permission", journal: "open() failed (13: Permission denied)", want: "权限不足"},
		{name: "config", journal: "nginx: configuration file /etc/nginx/nginx.conf test failed", want: "配置检查失败"},
		{name: "signal", state: guardServiceState{exitCode: "killed"}, want: "进程被信号强制终止"},
		{name: "exit status", state: guardServiceState{exitStatus: "1"}, want: "进程异常退出（状态码 1）"},
		{name: "unknown", want: "服务意外停止，系统日志未提供明确原因"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyServiceFailure(tt.journal, tt.state); got != tt.want {
				t.Fatalf("classifyServiceFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCoreGuardServices(t *testing.T) {
	for _, service := range []string{"nginx", "php8.3-fpm", "mariadb", "redis-server"} {
		if !isCoreGuardService(service) {
			t.Fatalf("%s should be a core service", service)
		}
	}
	for _, service := range []string{"nftables", "fail2ban"} {
		if isCoreGuardService(service) {
			t.Fatalf("%s should not send core service alerts", service)
		}
	}
}

func TestLogIncidentWritesCoreServiceAlert(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE process_guard_incidents (
		id INTEGER PRIMARY KEY, service TEXT, event TEXT, message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY, alert_type TEXT, level TEXT, message TEXT,
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		attribution TEXT
	)`)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`)
	mustExec(t, db, `INSERT INTO security_settings (skey, svalue) VALUES ('alert_service', 'true')`)

	service := &GuardService{Name: "MariaDB", ServiceName: "mariadb"}
	logIncident(service, "auto_restart", "MariaDB 进程异常退出，自动恢复成功", true)

	var incidents, alerts, resolved int
	_ = db.QueryRow("SELECT COUNT(*) FROM process_guard_incidents").Scan(&incidents)
	_ = db.QueryRow("SELECT COUNT(*) FROM alert_log WHERE alert_type = 'alert_service'").Scan(&alerts)
	_ = db.QueryRow("SELECT COALESCE(MAX(resolved), 0) FROM alert_log WHERE alert_type = 'alert_service'").Scan(&resolved)
	if incidents != 1 || alerts != 1 {
		t.Fatalf("incidents=%d alerts=%d, want 1 and 1", incidents, alerts)
	}
	if resolved != 1 {
		t.Fatalf("fast recovered service event resolved=%d, want 1", resolved)
	}
}

func TestUnexpectedActiveDoesNotCreateAbnormalAlert(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE process_guard_incidents (
		id INTEGER PRIMARY KEY, service TEXT, event TEXT, message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY, alert_type TEXT, level TEXT, message TEXT,
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		attribution TEXT
	)`)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`)
	mustExec(t, db, `INSERT INTO security_settings (skey, svalue) VALUES ('alert_service', 'true')`)

	service := &GuardService{Name: "Nginx", ServiceName: "nginx"}
	logIncident(service, "unexpected_active", "Nginx 在暂停守护期间被外部启动", false)

	var alerts int
	_ = db.QueryRow("SELECT COUNT(*) FROM alert_log").Scan(&alerts)
	if alerts != 0 {
		t.Fatalf("alerts=%d, want 0", alerts)
	}
}

func TestProcessGuardReadsStateAfterAcquiringLock(t *testing.T) {
	setupServiceControlTest(t)
	oldCommand := guardCommand
	called := make(chan struct{}, 1)
	guardCommand = func(_ string, _ ...string) ([]byte, error) {
		called <- struct{}{}
		return []byte("ActiveState=active\nNRestarts=0\nResult=success\n"), nil
	}
	t.Cleanup(func() { guardCommand = oldCommand })

	pg := &ProcessGuard{firstRun: true}
	service := &GuardService{Name: "MariaDB", ServiceName: "mariadb"}
	pg.mu.Lock()
	done := make(chan struct{})
	go func() {
		pg.check(service)
		close(done)
	}()

	select {
	case <-called:
		pg.mu.Unlock()
		t.Fatal("service state was read before acquiring the process guard lock")
	case <-time.After(30 * time.Millisecond):
	}
	pg.mu.Unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("process guard check did not finish")
	}
}

func TestRunGuardCommandTimesOut(t *testing.T) {
	start := time.Now()
	_, err := runGuardCommandWithTimeout(20*time.Millisecond, "sleep", "1")
	if err == nil {
		t.Fatal("slow command should time out")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("command timeout took too long: %v", time.Since(start))
	}
}

func TestLogIncidentLeavesUnrecoveredFailureToStateMonitor(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE process_guard_incidents (
		id INTEGER PRIMARY KEY, service TEXT, event TEXT, message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY, alert_type TEXT, level TEXT, message TEXT,
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		attribution TEXT
	)`)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`)
	mustExec(t, db, `INSERT INTO security_settings (skey, svalue) VALUES ('alert_service', 'true')`)

	oldNotifier := serviceIncidentNotifier
	notifications := 0
	serviceIncidentNotifier = func(_, _ string) { notifications++ }
	t.Cleanup(func() { serviceIncidentNotifier = oldNotifier })

	service := &GuardService{Name: "MariaDB", ServiceName: "mariadb"}
	logIncident(service, "restart", "MariaDB 进程异常退出，自动恢复失败", false)
	if notifications != 0 {
		t.Fatalf("notifications=%d, want 0", notifications)
	}

	var alerts int
	if err := db.QueryRow("SELECT COUNT(*) FROM alert_log").Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 0 {
		t.Fatalf("unrecovered failure alerts=%d, want 0 before central state monitor confirms it", alerts)
	}
}

func TestLogIncidentSuppressesRecoveredEventWhenStateAlertIsFiring(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE process_guard_incidents (
		id INTEGER PRIMARY KEY, service TEXT, event TEXT, message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY, alert_type TEXT, level TEXT, message TEXT,
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		attribution TEXT)`)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`)
	mustExec(t, db, `INSERT INTO security_settings VALUES ('alert_service', 'true')`)
	mustExec(t, db, `CREATE TABLE alert_runtime_state (alert_type TEXT PRIMARY KEY, status TEXT, email_alert_count INTEGER NOT NULL DEFAULT 0)`)
	mustExec(t, db, `INSERT INTO alert_runtime_state (alert_type, status) VALUES ('alert_service', 'firing')`)

	oldNotifier := serviceIncidentNotifier
	notifications := 0
	serviceIncidentNotifier = func(_, _ string) { notifications++ }
	t.Cleanup(func() { serviceIncidentNotifier = oldNotifier })
	service := &GuardService{Name: "MariaDB", ServiceName: "mariadb"}
	logIncident(service, "auto_restart", "MariaDB 自动恢复成功", true)
	if notifications != 0 {
		t.Fatalf("notifications=%d, want 0 while central alert is firing", notifications)
	}
	var alerts int
	_ = db.QueryRow("SELECT COUNT(*) FROM alert_log").Scan(&alerts)
	if alerts != 0 {
		t.Fatalf("alerts=%d, want 0; central monitor owns the recovery transition", alerts)
	}
}
