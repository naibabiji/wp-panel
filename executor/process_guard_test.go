package executor

import (
	"strings"
	"testing"
	"time"
)

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
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`)
	mustExec(t, db, `INSERT INTO security_settings (skey, svalue) VALUES ('alert_service', 'true')`)

	service := &GuardService{Name: "MariaDB", ServiceName: "mariadb"}
	logIncident(service, "auto_restart", "MariaDB 进程异常退出，自动恢复成功", true)

	var incidents, alerts int
	_ = db.QueryRow("SELECT COUNT(*) FROM process_guard_incidents").Scan(&incidents)
	_ = db.QueryRow("SELECT COUNT(*) FROM alert_log WHERE alert_type = 'alert_service'").Scan(&alerts)
	if incidents != 1 || alerts != 1 {
		t.Fatalf("incidents=%d alerts=%d, want 1 and 1", incidents, alerts)
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
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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

func TestLogIncidentSkipsImmediateNotificationWhenRecoveryFailed(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE process_guard_incidents (
		id INTEGER PRIMARY KEY, service TEXT, event TEXT, message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY, alert_type TEXT, level TEXT, message TEXT,
		resolved INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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

	var message string
	if err := db.QueryRow("SELECT message FROM alert_log").Scan(&message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "自动恢复失败") {
		t.Fatalf("alert log should retain failure details: %q", message)
	}
}
