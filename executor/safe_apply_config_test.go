package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shrinkSafeApplyTimeouts 把轮询超时/间隔调小，避免测试真的等 30/60 秒；
// 由每个用到失败路径的测试自行调用并在结束时恢复。
func shrinkSafeApplyTimeouts(t *testing.T) {
	t.Helper()
	oldRestart, oldPollTimeout, oldPollInterval := safeApplyRestartTimeout, safeApplyReadinessPollTimeout, safeApplyReadinessPollInterval
	safeApplyRestartTimeout = 200 * time.Millisecond
	safeApplyReadinessPollTimeout = 50 * time.Millisecond
	safeApplyReadinessPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		safeApplyRestartTimeout, safeApplyReadinessPollTimeout, safeApplyReadinessPollInterval = oldRestart, oldPollTimeout, oldPollInterval
	})
}

func withFakeRestart(t *testing.T, fn func(serviceName string) error) {
	t.Helper()
	old := restartServiceFunc
	restartServiceFunc = fn
	t.Cleanup(func() { restartServiceFunc = old })
}

func alwaysReady(context.Context) error { return nil }

func TestSafeApplyRestartConfigSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.conf")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	restartCalls := 0
	withFakeRestart(t, func(string) error { restartCalls++; return nil })

	result := SafeApplyRestartConfig(path, "new", "old", "fake-service", alwaysReady)
	if !result.Applied || result.Err != nil {
		t.Fatalf("expected success, got %+v", result)
	}
	if restartCalls != 1 {
		t.Fatalf("expected exactly 1 restart call, got %d", restartCalls)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatalf("expected file to contain new content, got %q", string(data))
	}
}

func TestSafeApplyRestartConfigRollsBackWhenRestartFails(t *testing.T) {
	shrinkSafeApplyTimeouts(t)
	path := filepath.Join(t.TempDir(), "config.conf")
	os.WriteFile(path, []byte("old"), 0644)

	calls := 0
	withFakeRestart(t, func(string) error {
		calls++
		if calls == 1 {
			return errors.New("boom")
		}
		return nil // rollback restart succeeds
	})

	result := SafeApplyRestartConfig(path, "new", "old", "fake-service", alwaysReady)
	if result.Applied {
		t.Fatal("expected Applied=false")
	}
	if !result.RolledBack || !result.RollbackSucceeded {
		t.Fatalf("expected successful rollback, got %+v", result)
	}
	if result.Err == nil {
		t.Fatal("expected a non-nil error explaining the failure")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Fatalf("expected file to be restored to old content, got %q", string(data))
	}
}

func TestSafeApplyRestartConfigRollsBackWhenReadinessFails(t *testing.T) {
	shrinkSafeApplyTimeouts(t)
	path := filepath.Join(t.TempDir(), "config.conf")
	os.WriteFile(path, []byte("old"), 0644)

	restartCalls := 0
	withFakeRestart(t, func(string) error { restartCalls++; return nil })

	// ready 跟当前处于"应用新配置"还是"回滚阶段"绑定：第一次重启（应用新配置）之后
	// 服务一直起不来，直到触发回滚、第二次重启（恢复旧配置）之后才恢复正常。
	// 用 restartCalls 而不是固定次数/时间来判断阶段，避免测试受轮询间隔精度影响而不稳定。
	ready := func(context.Context) error {
		if restartCalls >= 2 {
			return nil
		}
		return errors.New("not ready")
	}

	result := SafeApplyRestartConfig(path, "new", "old", "fake-service", ready)
	if result.Applied {
		t.Fatal("expected Applied=false since new config never became ready in time")
	}
	if !result.RolledBack || !result.RollbackSucceeded {
		t.Fatalf("expected successful rollback, got %+v", result)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Fatalf("expected file to be restored to old content, got %q", string(data))
	}
	if restartCalls < 2 {
		t.Fatalf("expected at least 2 restarts (apply + rollback), got %d", restartCalls)
	}
}

func TestSafeApplyRestartConfigReportsCriticalFailureWhenRollbackRestartAlsoFails(t *testing.T) {
	shrinkSafeApplyTimeouts(t)
	path := filepath.Join(t.TempDir(), "config.conf")
	os.WriteFile(path, []byte("old"), 0644)

	withFakeRestart(t, func(string) error { return errors.New("service refuses to start") })

	result := SafeApplyRestartConfig(path, "new", "old", "fake-service", alwaysReady)
	if result.Applied {
		t.Fatal("expected Applied=false")
	}
	if !result.RolledBack {
		t.Fatal("expected RolledBack=true (rollback was attempted)")
	}
	if result.RollbackSucceeded {
		t.Fatal("expected RollbackSucceeded=false since the rollback restart also failed")
	}
	if result.Err == nil {
		t.Fatal("expected a non-nil error describing the critical failure")
	}
	// 关键：即使回滚失败，配置文件内容也应该已经被写回旧值（只是服务没能重启成功），
	// 不能让磁盘上停留着一个应用失败的新配置。
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Fatalf("expected file to still be restored to old content even if restart failed, got %q", string(data))
	}
}

// TestSafeApplyRestartConfigReportsCriticalFailureWhenRollbackWriteFails 覆盖一个曾经
// 真实存在的 bug（代码审核发现）：回滚阶段自己的 atomicWriteConfigFile 失败时，早期实现
// 没有设置 RolledBack:true，导致调用方会误判成"配置保存失败，未做任何改动"——但实际上
// 坏的新配置已经落盘、服务大概率异常，这是最危险的一种状态却给出了最轻描淡写的提示。
func TestSafeApplyRestartConfigReportsCriticalFailureWhenRollbackWriteFails(t *testing.T) {
	shrinkSafeApplyTimeouts(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.conf")
	os.WriteFile(path, []byte("old"), 0644)

	withFakeRestart(t, func(string) error {
		// 应用新配置的第一次重启本身就失败，触发回滚；在回滚真正开始写文件之前，
		// 把整个目录删掉，让回滚的 atomicWriteConfigFile 必然失败。
		os.RemoveAll(dir)
		return errors.New("boom")
	})

	result := SafeApplyRestartConfig(path, "new", "old", "fake-service", alwaysReady)
	if result.Applied {
		t.Fatal("expected Applied=false")
	}
	if !result.RolledBack {
		t.Fatal("expected RolledBack=true even though the rollback's own write failed — regression test for the reported bug")
	}
	if result.RollbackSucceeded {
		t.Fatal("expected RollbackSucceeded=false since the rollback write itself failed")
	}
	if result.Err == nil {
		t.Fatal("expected a non-nil error")
	}
}

// TestSafeApplyRestartConfigSerializesConcurrentCalls 确认两次并发调用不会互相踩踏
// 同一个 .wppanel.tmp 临时文件——用一个会记录"同一时刻正在执行的调用数"的假重启函数，
// 如果互斥锁没生效，多个 goroutine 会同时进入临界区，maxInFlight 就会大于 1。
func TestSafeApplyRestartConfigSerializesConcurrentCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.conf")
	os.WriteFile(path, []byte("old"), 0644)

	var inFlight, maxInFlight int32
	withFakeRestart(t, func(string) error {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxInFlight)
			if n <= cur {
				break
			}
			if atomic.CompareAndSwapInt32(&maxInFlight, cur, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			SafeApplyRestartConfig(path, "new", "old", "fake-service", alwaysReady)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxInFlight); got > 1 {
		t.Fatalf("expected concurrent calls to be serialized by the mutex, but saw %d overlapping restarts", got)
	}
}

// TestSafeApplyRestartConfigPreservesFilePermissions 覆盖代码审核提出的一点：如果管理员
// 手动把配置文件加固成更严格的权限（比如 Redis 配置写了 requirepass 后改成 0600），
// 面板保存配置时不应该把权限悄悄改回 0644。
func TestSafeApplyRestartConfigPreservesFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.conf")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	withFakeRestart(t, func(string) error { return nil })

	result := SafeApplyRestartConfig(path, "new", "old", "fake-service", alwaysReady)
	if !result.Applied {
		t.Fatalf("expected success, got %+v", result)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected file permissions to stay 0600, got %o", perm)
	}
}

func TestSafeApplyRestartConfigDoesNotTouchServiceWhenInitialWriteFails(t *testing.T) {
	// 用一个不存在的目录制造写入失败，不需要真的碰权限。
	path := filepath.Join(t.TempDir(), "no-such-dir", "config.conf")

	restartCalls := 0
	withFakeRestart(t, func(string) error { restartCalls++; return nil })

	result := SafeApplyRestartConfig(path, "new", "old", "fake-service", alwaysReady)
	if result.Applied || result.RolledBack {
		t.Fatalf("expected a plain write failure with no restart/rollback attempted, got %+v", result)
	}
	if result.Err == nil {
		t.Fatal("expected a non-nil error")
	}
	if restartCalls != 0 {
		t.Fatalf("expected restart to never be called when the write itself failed, got %d calls", restartCalls)
	}
}
