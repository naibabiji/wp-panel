package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

const mariadbHealthCheckTimeout = 5 * time.Second

// MariaDBReady 检查 MariaDB 是否真的能接受连接（不只是 systemd 认为它在运行）。
// 用于 SafeApplyRestartConfig 重启后的健康检查，跟 runMySQL 不同的是这里带了超时，
// 避免面板在服务卡死、连接无响应时被拖住。
func MariaDBReady(ctx context.Context, cfg *config.Config) error {
	checkCtx, cancel := context.WithTimeout(ctx, mariadbHealthCheckTimeout)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "mysql", "-u", cfg.MariaDB.RootUser, "-N", "-e", "SELECT 1")
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.MariaDB.RootPassword)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}
