package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func installFakeChpasswd(t *testing.T, body string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake chpasswd integration test requires a POSIX shell")
	}
	dir := t.TempDir()
	stdinPath := filepath.Join(dir, "stdin")
	argsPath := filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$#\" > \"$WP_PANEL_TEST_ARGS\"\n" +
		"printf '%s\\n' \"$@\" >> \"$WP_PANEL_TEST_ARGS\"\n" +
		"cat > \"$WP_PANEL_TEST_STDIN\"\n" + body + "\n"
	path := filepath.Join(dir, "chpasswd")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WP_PANEL_TEST_STDIN", stdinPath)
	t.Setenv("WP_PANEL_TEST_ARGS", argsPath)
	return stdinPath, argsPath
}

func TestChangeRootPasswordUsesFixedCommandAndStdin(t *testing.T) {
	stdinPath, argsPath := installFakeChpasswd(t, "exit 0")
	const secret = "safe secret"
	if err := ChangeRootPassword(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != "root:"+secret+"\n" {
		t.Fatalf("stdin=%q", stdin)
	}
	if string(args) != "0\n\n" {
		t.Fatalf("chpasswd received arguments: %q", args)
	}
}

func TestChangeRootPasswordReturnsGenericError(t *testing.T) {
	installFakeChpasswd(t, "echo 'backend output containing do-not-leak' >&2\nexit 1")
	err := ChangeRootPassword(context.Background(), "do-not-leak")
	if !errors.Is(err, ErrRootPasswordChangeFailed) || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("error=%v", err)
	}
}

func TestChangeRootPasswordReportsTimeout(t *testing.T) {
	installFakeChpasswd(t, "sleep 2")
	err := changeRootPasswordWithTimeout(context.Background(), "do-not-leak", 20*time.Millisecond)
	if !errors.Is(err, ErrRootPasswordChangeTimeout) || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("error=%v", err)
	}
}
