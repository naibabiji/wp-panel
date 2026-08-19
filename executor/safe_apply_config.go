package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// 用 var 而不是 const，方便测试时临时调小，避免真的等 30/60 秒。
var (
	safeApplyRestartTimeout        = 60 * time.Second
	safeApplyReadinessPollTimeout  = 30 * time.Second
	safeApplyReadinessPollInterval = 1 * time.Second
)

// safeApplyMu 串行化所有 SafeApplyRestartConfig 调用，见该函数注释。
var safeApplyMu sync.Mutex

// restartServiceFunc 实际执行 `systemctl restart <service>`，声明成变量是为了让测试
// 可以替换成假实现，不必依赖真实的 systemd。
var restartServiceFunc = func(serviceName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), safeApplyRestartTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "restart", serviceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// SafeApplyResult 描述一次"写配置 + 重启 + 健康检查"的最终结果，供调用方转成
// 明确的用户提示，而不是不管成没成功都显示"已更新"。
type SafeApplyResult struct {
	Applied           bool  // 新配置已生效
	RolledBack        bool  // 曾尝试回滚到旧配置
	RollbackSucceeded bool  // 回滚是否成功恢复服务（只有 RolledBack 为 true 时才有意义）
	Err               error // 非 nil 时表示应用失败；nil 且 Applied 为 true 表示成功
}

// SafeApplyRestartConfig 用于 MariaDB/Redis 这类"没有可靠配置语法预检查工具、
// 只能靠重启验证配置是否合法"的服务：原子写入新配置 → 重启 → 轮询健康检查 →
// 失败则恢复旧配置并重启 → 再次健康检查确认恢复成功。
//
// 不做语法预检查（MariaDB/Redis 都没有能独立当语法校验依据的命令），因此每一步
// 都设了超时，避免面板被外部命令卡死；失败时不会无限重试，最多"新配置重启一次 +
// 旧配置回滚重启一次"，第二次也失败就明确报告，不再自动尝试。
//
// ready 是调用方提供的健康检查函数（真正尝试连接服务，而不只是看 systemctl 状态），
// 新配置和回滚后的旧配置都会用同一个 ready 函数验证。
//
// 用一把全局互斥锁串行化所有调用——这是面板管理员操作，不是高频路径，没必要为每个
// configPath 单独加锁；串行化能避免两次并发保存互相覆盖对方临时文件/配置内容。
func SafeApplyRestartConfig(configPath, newContent, oldContent, serviceName string, ready func(context.Context) error) SafeApplyResult {
	safeApplyMu.Lock()
	defer safeApplyMu.Unlock()

	if err := atomicWriteConfigFile(configPath, newContent); err != nil {
		return SafeApplyResult{Err: fmt.Errorf("写入配置失败，配置未改动: %w", err)}
	}

	if err := restartServiceFunc(serviceName); err != nil {
		return rollbackConfigAndRestart(configPath, oldContent, serviceName, ready, fmt.Errorf("重启服务失败: %w", err))
	}
	if err := pollServiceReady(ready); err != nil {
		return rollbackConfigAndRestart(configPath, oldContent, serviceName, ready, fmt.Errorf("重启后健康检查失败: %w", err))
	}
	return SafeApplyResult{Applied: true}
}

func atomicWriteConfigFile(path, content string) error {
	// 保留原文件的权限位——如果管理员手动把配置加固成了更严格的模式（比如 Redis
	// 配置写了 requirepass 后改成 0600），这里不应该在面板保存配置时悄悄改回 0644。
	// 文件不存在（第一次写入）时才用 0644 兜底，跟历史行为一致。
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmpPath := path + ".wppanel.tmp"
	if err := os.WriteFile(tmpPath, []byte(content), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// pollServiceReady 在总超时时间内每秒重试一次 ready()，直到成功或超时。
func pollServiceReady(ready func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), safeApplyReadinessPollTimeout)
	defer cancel()

	ticker := time.NewTicker(safeApplyReadinessPollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := ready(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return lastErr
		case <-ticker.C:
		}
	}
}

func rollbackConfigAndRestart(configPath, oldContent, serviceName string, ready func(context.Context) error, applyErr error) SafeApplyResult {
	if err := atomicWriteConfigFile(configPath, oldContent); err != nil {
		// 这里必须标记 RolledBack:true——新配置已经落盘且大概率导致服务异常，回滚写入
		// 又失败了，磁盘上现在停留的是坏配置。调用方要把这种情况当成"回滚失败、需要
		// 管理员立即介入"处理，不能落回默认分支显示"未做任何改动"（那是假的）。
		return SafeApplyResult{RolledBack: true, RollbackSucceeded: false, Err: fmt.Errorf("%v；回滚写入配置也失败: %w", applyErr, err)}
	}
	if err := restartServiceFunc(serviceName); err != nil {
		return SafeApplyResult{RolledBack: true, RollbackSucceeded: false, Err: fmt.Errorf("%v；回滚重启也失败: %w", applyErr, err)}
	}
	if err := pollServiceReady(ready); err != nil {
		return SafeApplyResult{RolledBack: true, RollbackSucceeded: false, Err: fmt.Errorf("%v；回滚重启成功但服务仍未就绪: %w", applyErr, err)}
	}
	return SafeApplyResult{RolledBack: true, RollbackSucceeded: true, Err: applyErr}
}
