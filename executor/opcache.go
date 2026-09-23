package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const opcacheClearTimeout = 30 * time.Second

// ClearOPcache 清空 PHP 的 OPcache 字节码缓存。
//
// OPcache 是整个 PHP-FPM 实例共享的，不区分站点，所以这是一个全局操作，不接受
// 站点 ID 参数。实现方式是 systemctl reload php8.3-fpm：php8.3-fpm.service 的
// ExecReload 是 kill -USR2 $MAINPID，PHP-FPM 主进程原地重载并重新初始化 opcache
// 共享内存，效果等同于清空。
//
// 注意：重载期间监听 socket 由新主进程继承，新连接不会被拒绝；但在默认
// process_control_timeout=0 下，旧 worker 会被立即结束，服务器上所有网站正在
// 执行的 PHP 请求都会被中断（Debian 13 + Sury PHP 8.3 LAN 实测：进行中的请求
// 立即返回 502）。因此这是一个会短暂影响全部网站的管理员操作，界面确认框必须
// 如实提示，建议在访问低峰执行。
func ClearOPcache() error {
	ctx, cancel := context.WithTimeout(context.Background(), opcacheClearTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "reload", "php8.3-fpm").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}
