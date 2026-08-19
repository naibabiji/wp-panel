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
// 站点 ID 参数。实现方式是 systemctl reload php8.3-fpm——这套 Debian 13 + PHP 8.3
// 环境下已经真机验证过：php8.3-fpm.service 的 ExecReload 是 kill -USR2 $MAINPID，
// PHP-FPM 收到 SIGUSR2 后会原地二进制重载主进程（socket fd 无缝继承，不中断正在
// 监听的连接），这个过程会重新初始化 opcache 共享内存，效果等同于清空。不需要
// 额外部署 PHP 探测脚本或依赖第三方工具（对比 WordOps 需要装一整套 admin tools、
// 对本地网页发 HTTP 请求触发 opcache_reset() 才能做到同样的事）。
func ClearOPcache() error {
	ctx, cancel := context.WithTimeout(context.Background(), opcacheClearTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "reload", "php8.3-fpm").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}
