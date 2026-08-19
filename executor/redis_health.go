package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const redisHealthCheckTimeout = 5 * time.Second

// RedisReady 检查 Redis 是否真的能响应 PING（不只是 systemd 认为它在运行）。
//
// 面板从未修改过 Debian redis-server 包默认的监听方式（只改 maxmemory 一行），
// 所以固定用本机默认 TCP 地址 127.0.0.1:6379，跟面板生成/编辑的 redis.conf
// 实际生效的监听配置一致；如果以后面板开始允许修改 bind/port，这里需要跟着改。
func RedisReady(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, redisHealthCheckTimeout)
	defer cancel()

	out, execErr := exec.CommandContext(checkCtx, "redis-cli", "-h", "127.0.0.1", "-p", "6379", "ping").CombinedOutput()
	resp := strings.TrimSpace(string(out))

	switch {
	case resp == "PONG":
		return nil
	case strings.Contains(resp, "NOAUTH"):
		// 面板自己从不设置 requirepass，但管理员可以在面板管理范围之外手动给
		// redis.conf 加密码。这种情况下 PING 会因为鉴权失败返回 NOAUTH——这本身
		// 已经证明 Redis 进程是活的、能正常处理请求，应该算作"就绪"，而不是让
		// 每一次保存都因为面板没有认证凭据而被判定成"服务没起来"进而永远回滚。
		return nil
	case execErr != nil:
		return fmt.Errorf("%s", resp)
	default:
		return fmt.Errorf("unexpected response: %s", resp)
	}
}
