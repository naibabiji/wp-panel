package executor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHealthCheckVersionRequiresRunningTargetVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"version":"v1.2.3"}`))
	}))
	defer server.Close()

	if err := healthCheckVersion(server.URL, "1.2.3"); err != nil {
		t.Fatalf("matching version rejected: %v", err)
	}
	if err := healthCheckVersion(server.URL, "v1.2.4"); err == nil {
		t.Fatal("old running version accepted as the update target")
	}
}

func TestHealthCheckVersionRejectsMissingVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	if err := healthCheckVersion(server.URL, "v1.2.3"); err == nil {
		t.Fatal("health response without a process version was accepted")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{a: "v1.2.0", b: "v1.1.9", want: 1},
		{a: "v1.2.0", b: "v1.2.0", want: 0},
		{a: "v1.2.0-rc5", b: "v1.2.0-rc4", want: 1},
		{a: "v1.2.0", b: "v1.2.0-rc5", want: 1},
		{a: "v1.2.0-rc4", b: "v1.2.0", want: -1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		if (got > 0 && tt.want <= 0) || (got == 0 && tt.want != 0) || (got < 0 && tt.want >= 0) {
			t.Fatalf("CompareVersions(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIsPatchBump(t *testing.T) {
	tests := []struct {
		current string
		target  string
		want    bool
	}{
		{"v1.2.3", "v1.2.4", true},
		{"1.2.3", "1.2.5", true},
		{"v1.2.3", "v1.3.0", false},
		{"v1.2.3", "v2.0.0", false},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.4-rc1", false},
	}
	for _, tt := range tests {
		if got := IsPatchBump(tt.current, tt.target); got != tt.want {
			t.Fatalf("IsPatchBump(%q, %q) = %v, want %v", tt.current, tt.target, got, tt.want)
		}
	}
}

func TestIsStableVersion(t *testing.T) {
	if !IsStableVersion("v1.2.3") {
		t.Fatal("stable version rejected")
	}
	if IsStableVersion("v1.2.3-rc1") {
		t.Fatal("prerelease accepted as stable")
	}
}

func TestWithinAutoUpdateWindow(t *testing.T) {
	base := time.Date(2026, 6, 19, 3, 30, 0, 0, time.Local)
	if !withinAutoUpdateWindow("03:00-05:00", base) {
		t.Fatal("time inside same-day window was rejected")
	}
	if withinAutoUpdateWindow("04:00-05:00", base) {
		t.Fatal("time before same-day window was accepted")
	}
	late := time.Date(2026, 6, 19, 23, 30, 0, 0, time.Local)
	if !withinAutoUpdateWindow("23:00-02:00", late) {
		t.Fatal("time inside cross-day late window was rejected")
	}
	early := time.Date(2026, 6, 19, 1, 30, 0, 0, time.Local)
	if !withinAutoUpdateWindow("23:00-02:00", early) {
		t.Fatal("time inside cross-day early window was rejected")
	}
	for _, invalid := range []string{"", "25:00-26:00", "not-a-window"} {
		if withinAutoUpdateWindow(invalid, base) {
			t.Fatalf("invalid window %q was accepted", invalid)
		}
	}
}

func TestShouldFetchForAutoUpdate(t *testing.T) {
	now := time.Date(2026, 6, 19, 3, 30, 0, 0, time.UTC)
	base := autoUpdateSettings{
		LastCheckAt:      now.Add(-time.Hour),
		SignatureTimeout: 120 * time.Minute,
		ReleaseDelay:     15 * time.Minute,
	}
	if shouldFetchForAutoUpdate(base, now) {
		t.Fatal("normal check should respect 24 hour fetch interval")
	}
	base.LastCheckAt = now.Add(-25 * time.Hour)
	if !shouldFetchForAutoUpdate(base, now) {
		t.Fatal("normal check should run after 24 hour fetch interval")
	}
	waitingSig := autoUpdateSettings{
		LastCheckAt:              now.Add(-time.Hour),
		LastStatus:               "waiting",
		LastSignatureWaitVersion: "v1.2.4",
		LastSignatureWaitAt:      now.Add(-30 * time.Minute),
		SignatureTimeout:         120 * time.Minute,
	}
	if !shouldFetchForAutoUpdate(waitingSig, now) {
		t.Fatal("signature waiting should bypass 24 hour fetch interval")
	}
	releaseReady := autoUpdateSettings{
		LastCheckAt:       now.Add(-time.Hour),
		LastStatus:        "waiting",
		LastStage:         "waiting_release_delay",
		LastTargetVersion: "v1.2.4",
		LastAttemptAt:     now.Add(-20 * time.Minute),
		ReleaseDelay:      15 * time.Minute,
	}
	if !shouldFetchForAutoUpdate(releaseReady, now) {
		t.Fatal("release delay completion should bypass 24 hour fetch interval")
	}
}

func TestSanitizeBackupPart(t *testing.T) {
	got := sanitizeBackupPart("v1.2.3; rm -rf /_ok")
	want := "v1.2.3rm-rf_ok"
	if got != want {
		t.Fatalf("sanitizeBackupPart() = %q, want %q", got, want)
	}
}

func TestVersionedBackupPath(t *testing.T) {
	got := versionedBackupPath("v1.2.3; bad")
	if !strings.HasPrefix(got, panelInstallPath+".bak.v1.2.3bad.") {
		t.Fatalf("versionedBackupPath() = %q", got)
	}
	if strings.ContainsAny(strings.TrimPrefix(got, panelInstallPath+".bak."), " ;/\\") {
		t.Fatalf("versionedBackupPath contains unsafe characters: %q", got)
	}
}

func TestCopyFileCopiesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := copyPanelFile(src, dst, 0750); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("dst content = %q, want hello", string(data))
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0750 {
		t.Fatalf("dst mode = %o, want 0750", info.Mode().Perm())
	}
}

func TestSnapshotPanelUpdateStatusExpiresTerminalState(t *testing.T) {
	restore := preservePanelUpdateStatus(t)
	panelUpdateStatusMu.Lock()
	currentPanelUpdateStatus = PanelUpdateStatus{
		Completed: true,
		Stage:     "completed",
		Message:   "更新完成",
		Percent:   100,
		UpdatedAt: time.Now().Add(-updateTerminalStatusTTL - time.Second),
	}
	panelUpdateStatusMu.Unlock()
	got := SnapshotPanelUpdateStatus()
	if got.Stage != "idle" || got.Completed || got.Running || got.Percent != 0 {
		t.Fatalf("expired status = %+v, want idle", got)
	}
	restore()
}

func preservePanelUpdateStatus(t *testing.T) func() {
	t.Helper()
	prev := SnapshotPanelUpdateStatus()
	restored := false
	restore := func() {
		if restored {
			return
		}
		panelUpdateStatusMu.Lock()
		currentPanelUpdateStatus = prev
		panelUpdateStatusMu.Unlock()
		restored = true
	}
	t.Cleanup(restore)
	return restore
}
