package executor

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/naibabiji/wp-panel/database"
)

const (
	swapFilePath       = "/swapfile"
	swapFstabPath      = "/etc/fstab"
	swapSysctlPath     = "/etc/sysctl.d/99-wp-panel-swap.conf"
	swapSizeBytes      = int64(2 * 1024 * 1024 * 1024)
	swapMaxMemoryBytes = int64(8 * 1024 * 1024 * 1024)
	swapMinFreeBytes   = int64(8 * 1024 * 1024 * 1024)
)

var swapCommand = func(name string, args ...string) error {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

var swapStatfs = syscall.Statfs

func init() {
	database.RegisterUpgrade("1.0.38", ensureSwapUpgrade)
}

// ensureSwapUpgrade is best-effort: v1.3.1 must not fail to start merely
// because this optional safety buffer could not be created.
func ensureSwapUpgrade() error {
	created, reason, err := ensureAutomaticSwap(
		"/proc/meminfo",
		"/proc/swaps",
		swapFilePath,
		swapFstabPath,
		swapSysctlPath,
	)
	if err != nil {
		log.Printf("[升级] 自动创建 Swap 失败，已跳过且不会自动重试: %v", err)
		return nil
	}
	if created {
		log.Printf("[升级] 已自动创建 2GB Swap")
	} else {
		log.Printf("[升级] 跳过自动创建 Swap: %s", reason)
	}
	return nil
}

func ensureAutomaticSwap(meminfoPath, swapsPath, swapPath, fstabPath, sysctlPath string) (bool, string, error) {
	totalMemory, err := readMeminfoValue(meminfoPath, "MemTotal:")
	if err != nil {
		return false, "", err
	}
	if totalMemory > swapMaxMemoryBytes {
		return false, "物理内存超过 8GB", nil
	}

	hasSwap, err := swapsConfigured(swapsPath)
	if err != nil {
		return false, "", err
	}
	if hasSwap {
		return false, "系统已有启用的 Swap", nil
	}
	if _, err := os.Stat(swapPath); err == nil {
		return false, swapPath + " 已存在", nil
	} else if !os.IsNotExist(err) {
		return false, "", fmt.Errorf("检查 %s: %w", swapPath, err)
	}

	var fs syscall.Statfs_t
	if err := swapStatfs("/", &fs); err != nil {
		return false, "", fmt.Errorf("检查根分区空间: %w", err)
	}
	freeBytes := int64(fs.Bavail) * int64(fs.Bsize)
	totalBytes := int64(fs.Blocks) * int64(fs.Bsize)
	usedBytes := totalBytes - int64(fs.Bfree)*int64(fs.Bsize)
	if freeBytes < swapMinFreeBytes {
		return false, "根分区可用空间不足 8GB", nil
	}
	if totalBytes <= 0 || (usedBytes+swapSizeBytes)*100/totalBytes > 85 {
		return false, "创建后根分区使用率将超过 85%", nil
	}

	log.Printf("[升级] 正在创建 2GB Swap，可能需要几十秒")
	if err := swapCommand("dd", "if=/dev/zero", "of="+swapPath, "bs=1M", "count=2048", "status=none"); err != nil {
		_ = os.Remove(swapPath)
		return false, "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(swapPath)
		}
	}()

	if err := os.Chmod(swapPath, 0600); err != nil {
		return false, "", fmt.Errorf("设置 Swap 文件权限: %w", err)
	}
	if err := swapCommand("mkswap", swapPath); err != nil {
		return false, "", err
	}
	if err := swapCommand("swapon", swapPath); err != nil {
		return false, "", err
	}

	fstabInfo, err := os.Stat(fstabPath)
	if err != nil {
		_ = swapCommand("swapoff", swapPath)
		return false, "", fmt.Errorf("检查 %s: %w", fstabPath, err)
	}
	fstabSize := fstabInfo.Size()
	fstab, err := os.OpenFile(fstabPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		_ = swapCommand("swapoff", swapPath)
		return false, "", fmt.Errorf("打开 %s: %w", fstabPath, err)
	}
	if _, err := fmt.Fprintf(fstab, "\n# WP Panel managed swap\n%s none swap sw 0 0\n", swapPath); err != nil {
		_ = fstab.Close()
		rollbackFstabAppend(fstabPath, fstabSize)
		_ = swapCommand("swapoff", swapPath)
		return false, "", fmt.Errorf("写入 %s: %w", fstabPath, err)
	}
	if err := fstab.Sync(); err != nil {
		_ = fstab.Close()
		rollbackFstabAppend(fstabPath, fstabSize)
		_ = swapCommand("swapoff", swapPath)
		return false, "", fmt.Errorf("同步 %s: %w", fstabPath, err)
	}
	if err := fstab.Close(); err != nil {
		rollbackFstabAppend(fstabPath, fstabSize)
		_ = swapCommand("swapoff", swapPath)
		return false, "", fmt.Errorf("关闭 %s: %w", fstabPath, err)
	}

	cleanup = false
	if err := os.WriteFile(sysctlPath, []byte("# WP Panel managed swap\nvm.swappiness = 10\n"), 0644); err != nil {
		log.Printf("[升级] Swap 已启用，但写入 swappiness 配置失败: %v", err)
		return true, "", nil
	}
	if err := swapCommand("sysctl", "-p", sysctlPath); err != nil {
		log.Printf("[升级] Swap 已启用，但应用 swappiness=10 失败: %v", err)
	}
	return true, "", nil
}

func rollbackFstabAppend(path string, size int64) {
	if err := os.Truncate(path, size); err != nil {
		log.Printf("[升级] 回滚 %s 失败，请手动删除 Swap 配置行: %v", path, err)
	}
}

func readMeminfoValue(path, key string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == key {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, err
			}
			return kb * 1024, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%s not found in %s", key, path)
}

func swapsConfigured(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		if strings.TrimSpace(scanner.Text()) != "" {
			return true, nil
		}
	}
	return false, scanner.Err()
}
