//go:build linux

package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWPInventoryFileLockRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := wpInventoryAcquireFileLock(context.Background(), link, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("symlink lock accepted")
	}
}

func TestWPInventoryFileLockCoordinatesProcesses(t *testing.T) {
	if os.Getenv("WP_PANEL_LOCK_HELPER") == "1" {
		lock, err := wpInventoryAcquireFileLock(context.Background(), os.Getenv("WP_PANEL_LOCK_PATH"), os.Geteuid(), os.Getegid())
		if err != nil {
			os.Exit(2)
		}
		defer lock.Close()
		if err := os.WriteFile(os.Getenv("WP_PANEL_LOCK_READY"), nil, 0600); err != nil {
			os.Exit(3)
		}
		time.Sleep(2 * time.Second)
		return
	}

	root := t.TempDir()
	lockPath := filepath.Join(root, ".lock")
	readyPath := filepath.Join(root, "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWPInventoryFileLockCoordinatesProcesses$")
	cmd.Env = []string{"WP_PANEL_LOCK_HELPER=1", "WP_PANEL_LOCK_PATH=" + lockPath, "WP_PANEL_LOCK_READY=" + readyPath}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := wpInventoryAcquireFileLock(ctx, lockPath, os.Geteuid(), os.Getegid())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contending lock error = %v, want deadline", err)
	}
}
