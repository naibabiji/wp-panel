package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWatchdogCommandDispatchesWithUnavailableDatabase(t *testing.T) {
	if os.Getenv("WP_PANEL_TEST_WATCHDOG_ENTRY") == "1" {
		flag.CommandLine = flag.NewFlagSet("wp-panel", flag.ExitOnError)
		os.Args = []string{"wp-panel", "--config", os.Getenv("WP_PANEL_TEST_WATCHDOG_CONFIG"),
			"--update-watchdog", os.Getenv("WP_PANEL_TEST_WATCHDOG_PLAN")}
		main()
		return
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	config := `{"panel":{"port":8080,"random_suffix":"fixture"},"sqlite":{"path":` +
		strconv.Quote(dir) + `},"mariadb":{"root_password":"fixture"},"admin":{"username":"fixture","password_hash":"fixture"}}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWatchdogCommandDispatchesWithUnavailableDatabase$")
	cmd.Env = append(os.Environ(), "WP_PANEL_TEST_WATCHDOG_ENTRY=1",
		"WP_PANEL_TEST_WATCHDOG_CONFIG="+configPath, "WP_PANEL_TEST_WATCHDOG_PLAN="+filepath.Join(dir, "missing.json"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "继续检查及回退") || !strings.Contains(string(out), "读取回滚计划失败") {
		t.Fatalf("watchdog command did not reach its rollback plan with unavailable SQLite: %v\n%s", err, out)
	}
}
