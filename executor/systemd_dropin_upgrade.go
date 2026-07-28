package executor

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/naibabiji/wp-panel/database"
)

const (
	managedServiceDropInLegacyContent = "[Service]\nRestart=always\nRestartSec=5s\nStartLimitIntervalSec=0\n"
	managedServiceDropInFixedContent  = "[Unit]\nStartLimitIntervalSec=0\n\n[Service]\nRestart=always\nRestartSec=5s\n"
)

var (
	managedServiceDropInRoot = "/etc/systemd/system"
	managedSystemctlCommand  = func(args ...string) error {
		output, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return nil
	}
)

func init() {
	database.RegisterUpgrade("1.0.40", ensureManagedServiceDropInStartLimitSections)
}

// ensureManagedServiceDropInStartLimitSections repairs only the exact drop-ins
// produced by old WP Panel installers. Custom administrator drop-ins are left
// untouched so an update does not overwrite local service policy.
func ensureManagedServiceDropInStartLimitSections() error {
	changed, err := repairManagedServiceDropIns(managedServiceDropInRoot)
	if err != nil {
		log.Printf("[升级] 修正 systemd drop-in 失败，已跳过且不会阻止面板启动: %v", err)
		return nil
	}
	if !changed {
		log.Printf("[升级] systemd drop-in 无需修正")
		return nil
	}
	if err := managedSystemctlCommand("daemon-reload"); err != nil {
		log.Printf("[升级] systemd daemon-reload 失败，请手动执行 systemctl daemon-reload: %v", err)
		return nil
	}
	log.Printf("[升级] 已修正受管服务 systemd drop-in 的 StartLimitIntervalSec 段落位置")
	return nil
}

func repairManagedServiceDropIns(root string) (bool, error) {
	changed := false
	for _, svc := range []string{"nginx", "php8.3-fpm", "mariadb", "redis-server"} {
		path := filepath.Join(root, svc+".service.d", "wp-panel.conf")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return changed, fmt.Errorf("读取 %s: %w", path, err)
		}
		if string(data) == managedServiceDropInFixedContent {
			continue
		}
		if string(data) != managedServiceDropInLegacyContent {
			log.Printf("[升级] 跳过自定义 systemd drop-in: %s", path)
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return changed, fmt.Errorf("检查 %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(managedServiceDropInFixedContent), info.Mode().Perm()); err != nil {
			return changed, fmt.Errorf("写入 %s: %w", path, err)
		}
		changed = true
	}
	return changed, nil
}
