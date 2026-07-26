package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestEnsureAutomaticSwapCreatesEligibleServerSwap(t *testing.T) {
	root := t.TempDir()
	meminfo := filepath.Join(root, "meminfo")
	swaps := filepath.Join(root, "swaps")
	swapfile := filepath.Join(root, "swapfile")
	fstab := filepath.Join(root, "fstab")
	sysctlPath := filepath.Join(root, "swap.conf")

	mustWriteSwapTestFile(t, meminfo, "MemTotal:        4194304 kB\n")
	mustWriteSwapTestFile(t, swaps, "Filename Type Size Used Priority\n")
	mustWriteSwapTestFile(t, fstab, "rootfs / ext4 defaults 0 1\n")

	oldCommand := swapCommand
	oldStatfs := swapStatfs
	t.Cleanup(func() {
		swapCommand = oldCommand
		swapStatfs = oldStatfs
	})
	var commands []string
	swapCommand = func(name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "dd" {
			return os.WriteFile(swapfile, []byte("allocated"), 0600)
		}
		return nil
	}
	swapStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Bsize = 4096
		stat.Blocks = 10 * 1024 * 1024
		stat.Bfree = 9 * 1024 * 1024
		stat.Bavail = 9 * 1024 * 1024
		return nil
	}

	created, reason, err := ensureAutomaticSwap(meminfo, swaps, swapfile, fstab, sysctlPath)
	if err != nil || !created || reason != "" {
		t.Fatalf("ensureAutomaticSwap() = created %v, reason %q, err %v", created, reason, err)
	}
	if got := strings.Join(commands, "\n"); !strings.Contains(got, "mkswap "+swapfile) ||
		!strings.Contains(got, "swapon "+swapfile) ||
		!strings.Contains(got, "sysctl -p "+sysctlPath) {
		t.Fatalf("commands = %q", got)
	}
	fstabData, err := os.ReadFile(fstab)
	if err != nil || !strings.Contains(string(fstabData), swapfile+" none swap sw 0 0") {
		t.Fatalf("fstab = %q, err %v", fstabData, err)
	}
	sysctlData, err := os.ReadFile(sysctlPath)
	if err != nil || !strings.Contains(string(sysctlData), "vm.swappiness = 10") {
		t.Fatalf("sysctl = %q, err %v", sysctlData, err)
	}
}

func TestEnsureAutomaticSwapSkipsExistingSwap(t *testing.T) {
	root := t.TempDir()
	meminfo := filepath.Join(root, "meminfo")
	swaps := filepath.Join(root, "swaps")
	mustWriteSwapTestFile(t, meminfo, "MemTotal:        4194304 kB\n")
	mustWriteSwapTestFile(t, swaps, "Filename Type Size Used Priority\n/dev/vda2 partition 1 0 -2\n")

	created, reason, err := ensureAutomaticSwap(
		meminfo, swaps, filepath.Join(root, "swapfile"), filepath.Join(root, "fstab"), filepath.Join(root, "swap.conf"),
	)
	if err != nil || created || reason != "系统已有启用的 Swap" {
		t.Fatalf("ensureAutomaticSwap() = created %v, reason %q, err %v", created, reason, err)
	}
}

func TestEnsureAutomaticSwapSkipsLargeMemoryServer(t *testing.T) {
	root := t.TempDir()
	meminfo := filepath.Join(root, "meminfo")
	swaps := filepath.Join(root, "swaps")
	mustWriteSwapTestFile(t, meminfo, "MemTotal:       16777216 kB\n")
	mustWriteSwapTestFile(t, swaps, "Filename Type Size Used Priority\n")

	created, reason, err := ensureAutomaticSwap(
		meminfo, swaps, filepath.Join(root, "swapfile"), filepath.Join(root, "fstab"), filepath.Join(root, "swap.conf"),
	)
	if err != nil || created || reason != "物理内存超过 8GB" {
		t.Fatalf("ensureAutomaticSwap() = created %v, reason %q, err %v", created, reason, err)
	}
}

func TestEnsureAutomaticSwapSkipsLowDiskSpace(t *testing.T) {
	root := t.TempDir()
	meminfo := filepath.Join(root, "meminfo")
	swaps := filepath.Join(root, "swaps")
	mustWriteSwapTestFile(t, meminfo, "MemTotal:        4194304 kB\n")
	mustWriteSwapTestFile(t, swaps, "Filename Type Size Used Priority\n")

	oldStatfs := swapStatfs
	swapStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Bsize = 4096
		stat.Blocks = 2 * 1024 * 1024
		stat.Bfree = 1024
		stat.Bavail = 1024
		return nil
	}
	t.Cleanup(func() { swapStatfs = oldStatfs })

	created, reason, err := ensureAutomaticSwap(
		meminfo, swaps, filepath.Join(root, "swapfile"), filepath.Join(root, "fstab"), filepath.Join(root, "swap.conf"),
	)
	if err != nil || created || reason != "根分区可用空间不足 8GB" {
		t.Fatalf("ensureAutomaticSwap() = created %v, reason %q, err %v", created, reason, err)
	}
}

func TestEnsureAutomaticSwapCleansFailedAllocation(t *testing.T) {
	root := t.TempDir()
	meminfo := filepath.Join(root, "meminfo")
	swaps := filepath.Join(root, "swaps")
	swapfile := filepath.Join(root, "swapfile")
	mustWriteSwapTestFile(t, meminfo, "MemTotal:        4194304 kB\n")
	mustWriteSwapTestFile(t, swaps, "Filename Type Size Used Priority\n")

	oldCommand := swapCommand
	oldStatfs := swapStatfs
	t.Cleanup(func() {
		swapCommand = oldCommand
		swapStatfs = oldStatfs
	})
	swapCommand = func(name string, _ ...string) error {
		if name == "dd" {
			_ = os.WriteFile(swapfile, []byte("partial"), 0600)
			return errors.New("disk error")
		}
		return nil
	}
	swapStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Bsize = 4096
		stat.Blocks = 10 * 1024 * 1024
		stat.Bfree = 9 * 1024 * 1024
		stat.Bavail = 9 * 1024 * 1024
		return nil
	}

	created, _, err := ensureAutomaticSwap(
		meminfo, swaps, swapfile, filepath.Join(root, "fstab"), filepath.Join(root, "swap.conf"),
	)
	if err == nil || created {
		t.Fatalf("ensureAutomaticSwap() = created %v, err %v", created, err)
	}
	if _, statErr := os.Stat(swapfile); !os.IsNotExist(statErr) {
		t.Fatalf("partial swap file remains: %v", statErr)
	}
}

func TestRollbackFstabAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	original := "rootfs / ext4 defaults 0 1\n"
	mustWriteSwapTestFile(t, path, original+"\n/swapfile none swap sw 0 0\n")

	rollbackFstabAppend(path, int64(len(original)))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("fstab after rollback = %q, want %q", data, original)
	}
}

func mustWriteSwapTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
