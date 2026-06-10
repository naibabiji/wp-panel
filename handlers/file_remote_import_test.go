package handlers

import (
	"net"
	"net/url"
	"testing"
)

func TestValidateRemoteImportURLRejectsUnsafeInputs(t *testing.T) {
	tests := []string{
		"http://example.com/backup.zip",
		"ftp://example.com/backup.zip",
		"https://user:pass@example.com/backup.zip",
		"https://127.0.0.1/backup.zip",
		"https://10.0.0.2/backup.zip",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/backup.zip",
	}
	for _, raw := range tests {
		if _, err := validateRemoteImportURL(raw); err == nil {
			t.Fatalf("validateRemoteImportURL(%q) error = nil, want error", raw)
		}
	}
}

func TestIsBlockedRemoteImportIP(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"100.64.1.1", true},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
	}
	for _, tt := range tests {
		if got := isBlockedRemoteImportIP(net.ParseIP(tt.ip)); got != tt.blocked {
			t.Fatalf("isBlockedRemoteImportIP(%s) = %v, want %v", tt.ip, got, tt.blocked)
		}
	}
}

func TestNormalizeRemoteImportFingerprint(t *testing.T) {
	raw := "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"
	got, err := normalizeRemoteImportFingerprint(raw)
	if err != nil {
		t.Fatalf("normalizeRemoteImportFingerprint: %v", err)
	}
	want := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	if got != want {
		t.Fatalf("fingerprint = %q, want %q", got, want)
	}
	if _, err := normalizeRemoteImportFingerprint("abc"); err == nil {
		t.Fatal("short fingerprint error = nil, want error")
	}
}

func TestRemoteImportFilenameFromURL(t *testing.T) {
	u, err := url.Parse("https://example.com/path/site%20backup.zip?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteImportFilename(u); got != "site backup.zip" {
		t.Fatalf("remoteImportFilename = %q, want %q", got, "site backup.zip")
	}
}
