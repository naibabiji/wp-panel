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

func TestInstallRepairInstallsSQLiteOnlyAfterSafeConfigCheck(t *testing.T) {
	script := readInstallScript(t, installScriptPath)
	configCheck := requiredIndex(t, script, `--repair-config-check --config "$CONFIG_FILE"`)
	sqliteInstall := requiredIndex(t, script, `DEBIAN_FRONTEND=noninteractive apt-get install -y sqlite3`)
	backupAfterInstall := strings.Index(script[sqliteInstall:], "\n    create_repair_backup")
	if backupAfterInstall < 0 {
		t.Fatal("repair sqlite dependency install must be followed by create_repair_backup call")
	}
	backup := sqliteInstall + backupAfterInstall
	if !(configCheck < sqliteInstall && sqliteInstall < backup) {
		t.Fatalf("repair sqlite dependency order is invalid: config_check=%d sqlite_install=%d backup=%d", configCheck, sqliteInstall, backup)
	}

	componentInstall := requiredIndex(t, script, "apt-get install -y \\\n    nginx \\")
	componentComplete := requiredIndex(t, script, `log_info "基础组件安装完成"`)
	if strings.Contains(script[componentInstall:componentComplete], "sqlite3") {
		t.Fatal("fresh install must not install repair-only sqlite3 dependency")
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
		mustWriteTestFile(t, filepath.Join(backup, "panel.db"), "old-database", 0600)
		binary := filepath.Join(root, "wp-panel")
		unit := filepath.Join(root, "wp-panel.service")
		database := filepath.Join(root, "panel.db")
		mustWriteTestFile(t, binary, "new-binary", 0755)
		mustWriteTestFile(t, unit, "new-unit", 0644)
		mustWriteTestFile(t, database, "new-database", 0600)
		mustWriteTestFile(t, database+"-wal", "new-wal", 0600)
		mustWriteTestFile(t, database+"-shm", "new-shm", 0600)
		mustWriteTestFile(t, filepath.Join(certDir, "panel.crt"), "new-cert", 0644)
		mustWriteTestFile(t, filepath.Join(certDir, "panel.key"), "new-key", 0600)
		logPath := filepath.Join(root, "systemctl.log")

		runRollbackFixture(t, rollback, root, backup, binary, unit, database, logPath, true, true, true, true, "preserve", false)
		assertTestFile(t, binary, "old-binary")
		assertTestFile(t, unit, "old-unit")
		assertTestFile(t, filepath.Join(certDir, "panel.crt"), "old-cert")
		assertTestFile(t, filepath.Join(certDir, "panel.key"), "old-key")
		assertTestFile(t, database, "old-database")
		for _, sidecar := range []string{database + "-wal", database + "-shm"} {
			if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
				t.Fatalf("SQLite sidecar still exists after rollback: %s", sidecar)
			}
		}
		assertTestFile(t, logPath, "stop wp-panel\ndaemon-reload\nstart wp-panel\n")
	})

	t.Run("removes newly created files and preserves inactive service", func(t *testing.T) {
		root := t.TempDir()
		backup := filepath.Join(root, "backup")
		if err := os.MkdirAll(backup, 0700); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(root, "wp-panel")
		unit := filepath.Join(root, "wp-panel.service")
		database := filepath.Join(root, "panel.db")
		mustWriteTestFile(t, binary, "new-binary", 0755)
		mustWriteTestFile(t, unit, "new-unit", 0644)
		mustWriteTestFile(t, filepath.Join(root, "certs", "panel.crt"), "new-cert", 0644)
		mustWriteTestFile(t, filepath.Join(root, "certs", "panel.key"), "new-key", 0600)
		mustWriteTestFile(t, database, "new-database", 0600)
		mustWriteTestFile(t, database+"-wal", "new-wal", 0600)
		logPath := filepath.Join(root, "systemctl.log")

		runRollbackFixture(t, rollback, root, backup, binary, unit, database, logPath, false, false, false, false, "generate", false)
		for _, path := range []string{binary, unit, database, database + "-wal", filepath.Join(root, "certs", "panel.crt"), filepath.Join(root, "certs", "panel.key")} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("new repair file still exists after rollback: %s", path)
			}
		}
		assertTestFile(t, logPath, "stop wp-panel\ndaemon-reload\nstop wp-panel\n")
	})

	t.Run("keeps old service stopped when database restore fails", func(t *testing.T) {
		root := t.TempDir()
		backup := filepath.Join(root, "backup")
		mustWriteTestFile(t, filepath.Join(backup, "wp-panel"), "old-binary", 0755)
		mustWriteTestFile(t, filepath.Join(backup, "panel.db"), "old-database", 0600)
		binary := filepath.Join(root, "wp-panel")
		unit := filepath.Join(root, "wp-panel.service")
		database := filepath.Join(root, "panel.db")
		mustWriteTestFile(t, binary, "new-binary", 0755)
		mustWriteTestFile(t, database, "new-database", 0600)
		logPath := filepath.Join(root, "systemctl.log")

		runRollbackFixture(t, rollback, root, backup, binary, unit, database, logPath, true, false, false, true, "preserve", true)
		assertTestFile(t, binary, "old-binary")
		assertTestFile(t, database, "new-database")
		assertTestFile(t, logPath, "stop wp-panel\ndaemon-reload\nstop wp-panel\n")
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

func runRollbackFixture(t *testing.T, rollback, root, backup, binary, unit, database, logPath string, binExisted, unitExisted, tlsExisted, dbExisted bool, tlsAction string, dbRestoreFails bool) {
	t.Helper()
	shell := fmt.Sprintf(`set -e
REPAIR_MODE=true
REPAIR_COMMITTED=false
REPAIR_MUTATED=true
REPAIR_BACKUP_DIR=%s
REPAIR_BIN_EXISTED=%t
REPAIR_UNIT_EXISTED=%t
REPAIR_TLS_EXISTED=%t
REPAIR_DB_EXISTED=%t
REPAIR_TLS_ACTION=%s
REPAIR_SERVICE_WAS_ACTIVE=%t
BIN_PATH=%s
SERVICE_PATH=%s
DB_PATH=%s
INSTALL_DIR=%s
SYSTEMCTL_LOG=%s
log_warn() { :; }
DB_RESTORE_FAILS=%t
install() {
    if $DB_RESTORE_FAILS && [[ "${@: -1}" == "${DB_PATH}.repair-rollback.$$" ]]; then
        return 1
    fi
    cp "${@: -2:1}" "${@: -1}"
    chmod "$2" "${@: -1}"
}
systemctl() { printf '%%s\n' "$*" >> "$SYSTEMCTL_LOG"; }
%s
repair_rollback
`, strconv.Quote(backup), binExisted, unitExisted, tlsExisted, dbExisted, strconv.Quote(tlsAction), binExisted, strconv.Quote(binary), strconv.Quote(unit), strconv.Quote(database), strconv.Quote(root), strconv.Quote(logPath), dbRestoreFails, rollback)
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
