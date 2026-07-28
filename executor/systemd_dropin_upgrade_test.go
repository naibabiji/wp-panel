package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepairManagedServiceDropInsUpdatesOnlyLegacyContent(t *testing.T) {
	root := t.TempDir()
	writeDropIn(t, root, "nginx", managedServiceDropInLegacyContent)
	writeDropIn(t, root, "php8.3-fpm", managedServiceDropInFixedContent)
	writeDropIn(t, root, "mariadb", "[Service]\nRestart=on-failure\nRestartSec=2s\nStartLimitIntervalSec=0\n")

	changed, err := repairManagedServiceDropIns(root)
	if err != nil {
		t.Fatalf("repairManagedServiceDropIns() error = %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}

	if got := readDropIn(t, root, "nginx"); got != managedServiceDropInFixedContent {
		t.Fatalf("nginx drop-in = %q, want fixed content", got)
	}
	if got := readDropIn(t, root, "php8.3-fpm"); got != managedServiceDropInFixedContent {
		t.Fatalf("php drop-in = %q, want fixed content", got)
	}
	if got := readDropIn(t, root, "mariadb"); got != "[Service]\nRestart=on-failure\nRestartSec=2s\nStartLimitIntervalSec=0\n" {
		t.Fatalf("custom mariadb drop-in was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "redis-server.service.d", "wp-panel.conf")); !os.IsNotExist(err) {
		t.Fatalf("missing redis drop-in stat err = %v, want not exist", err)
	}
}

func TestRepairManagedServiceDropInsReportsNoChangeWhenAlreadyFixed(t *testing.T) {
	root := t.TempDir()
	for _, svc := range []string{"nginx", "php8.3-fpm", "mariadb", "redis-server"} {
		writeDropIn(t, root, svc, managedServiceDropInFixedContent)
	}

	changed, err := repairManagedServiceDropIns(root)
	if err != nil {
		t.Fatalf("repairManagedServiceDropIns() error = %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
}

func writeDropIn(t *testing.T, root, svc, content string) {
	t.Helper()
	dir := filepath.Join(root, svc+".service.d")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	path := filepath.Join(dir, "wp-panel.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func readDropIn(t *testing.T, root, svc string) string {
	t.Helper()
	path := filepath.Join(root, svc+".service.d", "wp-panel.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(data)
}
