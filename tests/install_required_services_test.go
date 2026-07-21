package tests

import (
	"os"
	"strings"
	"testing"
)

const (
	installScriptPath   = "../install.sh"
	installCNScriptPath = "../install-cn.sh"
)

func TestInstallRequiredWebServicesOrder(t *testing.T) {
	script := readInstallScript(t, installScriptPath)

	guardConfigured := requiredIndex(t, script, `log_info "systemd 进程守护配置完成"`)
	daemonReload := requiredLastIndexBefore(t, script, "systemctl daemon-reload", guardConfigured)
	phpStart := requiredIndex(t, script, "systemctl_start_required php8.3-fpm")
	nginxStart := requiredIndex(t, script, "systemctl_start_required nginx")
	mariaDBStart := requiredIndex(t, script, "systemctl_start_required mariadb")
	panelStart := requiredIndex(t, script, "systemctl_start_required wp-panel")

	if !(daemonReload < guardConfigured && guardConfigured < phpStart && phpStart < nginxStart && nginxStart < mariaDBStart && mariaDBStart < panelStart) {
		t.Fatalf("required service order is invalid: daemon_reload=%d guard_log=%d php=%d nginx=%d mariadb=%d panel=%d",
			daemonReload, guardConfigured, phpStart, nginxStart, mariaDBStart, panelStart)
	}
}

func TestInstallRequiredWebServicesUseSharedStartHelper(t *testing.T) {
	script := readInstallScript(t, installScriptPath)
	cnScript := readInstallScript(t, installCNScriptPath)

	if got := strings.Count(script, "systemctl_start_required() {"); got != 1 {
		t.Fatalf("systemctl_start_required helper definitions = %d, want 1", got)
	}
	for _, call := range []string{
		"systemctl_start_required php8.3-fpm",
		"systemctl_start_required nginx",
	} {
		if got := countExactLine(script, call); got != 1 {
			t.Errorf("%q exact calls = %d, want 1", call, got)
		}
		if strings.Contains(cnScript, call) {
			t.Errorf("install-cn.sh duplicates main installer call %q", call)
		}
	}

	for _, forbidden := range []string{
		"systemctl restart php8.3-fpm",
		"systemctl restart nginx",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("install.sh contains forbidden restart command %q", forbidden)
		}
	}
}

func readInstallScript(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func requiredIndex(t *testing.T, script, text string) int {
	t.Helper()
	index := strings.Index(script, text)
	if index < 0 {
		t.Fatalf("install.sh missing %q", text)
	}
	return index
}

func requiredLastIndexBefore(t *testing.T, script, text string, before int) int {
	t.Helper()
	index := strings.LastIndex(script[:before], text)
	if index < 0 {
		t.Fatalf("install.sh missing %q before offset %d", text, before)
	}
	return index
}

func countExactLine(script, want string) int {
	count := 0
	for _, line := range strings.Split(script, "\n") {
		if strings.TrimSpace(line) == want {
			count++
		}
	}
	return count
}
