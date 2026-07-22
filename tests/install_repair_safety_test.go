package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInstallRepairPreflightRunsBeforeAPT(t *testing.T) {
	script := readInstallScript(t, installScriptPath)

	preflight := requiredIndex(t, script, `log_info "repair预检与备份完成"`)
	apt := requiredIndex(t, script, `log_info "配置 APT 源..."`)
	if preflight >= apt {
		t.Fatalf("repair preflight offset=%d must be before APT offset=%d", preflight, apt)
	}
	for _, required := range []string{
		`--repair-config-check --config "$CONFIG_FILE"`,
		`config.json，无法安全repair`,
		`repair模式保持config.json字节不变`,
		`repair模式保留现有登录凭据和安全入口`,
		`repair模式保留现有TLS证书与私钥`,
		`repair模式保留现有MariaDB身份与配置`,
		`sha256sum -c "$REPAIR_BACKUP_DIR/SHA256SUMS"`,
		`mv "$BIN_TMP" "$BIN_PATH"`,
	} {
		requiredIndex(t, script, required)
	}
}

func TestRepairRollbackFaultInjection(t *testing.T) {
	script := readInstallScript(t, installScriptPath)
	rollback := extractShellFunction(t, script, "repair_rollback", "apt_package_available")

	t.Run("restores existing files and active service", func(t *testing.T) {
		root := t.TempDir()
		backup := filepath.Join(root, "backup")
		certDir := filepath.Join(root, "certs")
		mustWriteTestFile(t, filepath.Join(backup, "wp-panel"), "old-binary", 0755)
		mustWriteTestFile(t, filepath.Join(backup, "wp-panel.service"), "old-unit", 0644)
		mustWriteTestFile(t, filepath.Join(backup, "panel.crt"), "old-cert", 0644)
		mustWriteTestFile(t, filepath.Join(backup, "panel.key"), "old-key", 0600)
		binary := filepath.Join(root, "wp-panel")
		unit := filepath.Join(root, "wp-panel.service")
		mustWriteTestFile(t, binary, "new-binary", 0755)
		mustWriteTestFile(t, unit, "new-unit", 0644)
		mustWriteTestFile(t, filepath.Join(certDir, "panel.crt"), "new-cert", 0644)
		mustWriteTestFile(t, filepath.Join(certDir, "panel.key"), "new-key", 0600)
		logPath := filepath.Join(root, "systemctl.log")

		runRollbackFixture(t, rollback, root, backup, binary, unit, logPath, true, true, true, "preserve")
		assertTestFile(t, binary, "old-binary")
		assertTestFile(t, unit, "old-unit")
		assertTestFile(t, filepath.Join(certDir, "panel.crt"), "old-cert")
		assertTestFile(t, filepath.Join(certDir, "panel.key"), "old-key")
		assertTestFile(t, logPath, "daemon-reload\nstart wp-panel\n")
	})

	t.Run("removes newly created files and preserves inactive service", func(t *testing.T) {
		root := t.TempDir()
		backup := filepath.Join(root, "backup")
		if err := os.MkdirAll(backup, 0700); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(root, "wp-panel")
		unit := filepath.Join(root, "wp-panel.service")
		mustWriteTestFile(t, binary, "new-binary", 0755)
		mustWriteTestFile(t, unit, "new-unit", 0644)
		mustWriteTestFile(t, filepath.Join(root, "certs", "panel.crt"), "new-cert", 0644)
		mustWriteTestFile(t, filepath.Join(root, "certs", "panel.key"), "new-key", 0600)
		logPath := filepath.Join(root, "systemctl.log")

		runRollbackFixture(t, rollback, root, backup, binary, unit, logPath, false, false, false, "generate")
		for _, path := range []string{binary, unit, filepath.Join(root, "certs", "panel.crt"), filepath.Join(root, "certs", "panel.key")} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("new repair file still exists after rollback: %s", path)
			}
		}
		assertTestFile(t, logPath, "daemon-reload\nstop wp-panel\n")
	})
}

func TestInstallRepairDoesNotParseExistingJSONWithTextTools(t *testing.T) {
	script := readInstallScript(t, installScriptPath)
	for _, forbidden := range []string{
		`grep -o '"root_password"`,
		`sed -i "$CONFIG_FILE"`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("install.sh contains forbidden repair/config pattern %q", forbidden)
		}
	}
}

func TestInstallCNDoesNotDuplicateRepairImplementation(t *testing.T) {
	cnScript := readInstallScript(t, installCNScriptPath)
	for _, forbidden := range []string{
		"REPAIR_MODE",
		"repair-config-check",
		"create_repair_backup",
		"repair_rollback",
	} {
		if strings.Contains(cnScript, forbidden) {
			t.Errorf("install-cn.sh duplicates repair implementation %q", forbidden)
		}
	}
}

func extractShellFunction(t *testing.T, script, name, nextName string) string {
	t.Helper()
	start := strings.Index(script, name+"() {")
	end := strings.Index(script[start:], "\n"+nextName+"() {")
	if start < 0 || end < 0 {
		t.Fatalf("cannot extract %s", name)
	}
	return script[start : start+end]
}

func runRollbackFixture(t *testing.T, rollback, root, backup, binary, unit, logPath string, binExisted, unitExisted, tlsExisted bool, tlsAction string) {
	t.Helper()
	shell := fmt.Sprintf(`set -e
REPAIR_MODE=true
REPAIR_COMMITTED=false
REPAIR_MUTATED=true
REPAIR_BACKUP_DIR=%s
REPAIR_BIN_EXISTED=%t
REPAIR_UNIT_EXISTED=%t
REPAIR_TLS_EXISTED=%t
REPAIR_TLS_ACTION=%s
REPAIR_SERVICE_WAS_ACTIVE=%t
BIN_PATH=%s
SERVICE_PATH=%s
INSTALL_DIR=%s
SYSTEMCTL_LOG=%s
log_warn() { :; }
install() { cp "${@: -2:1}" "${@: -1}"; chmod "$2" "${@: -1}"; }
systemctl() { printf '%%s\n' "$*" >> "$SYSTEMCTL_LOG"; }
%s
repair_rollback
`, strconv.Quote(backup), binExisted, unitExisted, tlsExisted, strconv.Quote(tlsAction), binExisted, strconv.Quote(binary), strconv.Quote(unit), strconv.Quote(root), strconv.Quote(logPath), rollback)
	cmd := exec.Command("bash", "-c", shell)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rollback fixture failed: %v\n%s", err, output)
	}
}

func mustWriteTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
