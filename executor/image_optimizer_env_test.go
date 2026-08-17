package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStubBinary drops an executable shell script named `name` into dir that,
// when run, appends its own name plus all args to markerFile (one call per
// line) and exits with exitCode.
func writeStubBinary(t *testing.T, dir, name string, exitCode int, markerFile string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\necho \"$0 $*\" >> %q\nexit %d\n", markerFile, exitCode)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

func readMarkerLines(t *testing.T, markerFile string) []string {
	t.Helper()
	data, err := os.ReadFile(markerFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read marker file: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestEnsurePHPExifExtensionSkipsWhenAlreadyInstalled(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "calls.log")

	writeStubBinary(t, binDir, "dpkg", 0, marker)    // "installed"
	writeStubBinary(t, binDir, "apt-get", 1, marker) // must never be invoked
	writeStubBinary(t, binDir, "systemctl", 1, marker)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	EnsurePHPExifExtension()

	calls := readMarkerLines(t, marker)
	if len(calls) != 1 || !strings.Contains(calls[0], "dpkg") {
		t.Fatalf("expected only the dpkg check to run, got calls: %v", calls)
	}
}

func TestEnsurePHPExifExtensionInstallsAndReloadsWhenMissing(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "calls.log")

	writeStubBinary(t, binDir, "dpkg", 1, marker)      // "not installed"
	writeStubBinary(t, binDir, "apt-get", 0, marker)   // install succeeds
	writeStubBinary(t, binDir, "systemctl", 0, marker) // reload succeeds

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	EnsurePHPExifExtension()

	calls := readMarkerLines(t, marker)
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "apt-get") {
		t.Fatalf("expected apt-get install to run when extension is missing, calls: %v", calls)
	}
	if !strings.Contains(joined, "systemctl") || !strings.Contains(joined, "reload") || !strings.Contains(joined, "php8.3-fpm") {
		t.Fatalf("expected systemctl reload php8.3-fpm to run after a successful install, calls: %v", calls)
	}
}

func TestEnsurePHPExifExtensionDoesNotReloadOnInstallFailure(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "calls.log")

	writeStubBinary(t, binDir, "dpkg", 1, marker)      // "not installed"
	writeStubBinary(t, binDir, "apt-get", 1, marker)   // install fails
	writeStubBinary(t, binDir, "systemctl", 1, marker) // must not be reached

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	EnsurePHPExifExtension()

	calls := readMarkerLines(t, marker)
	for _, c := range calls {
		if strings.Contains(c, "systemctl") {
			t.Fatalf("systemctl reload must not run when apt-get install failed, calls: %v", calls)
		}
	}
}
