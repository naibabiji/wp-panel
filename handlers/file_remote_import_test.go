package handlers

import (
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/naibabiji/wp-panel/i18n"
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
		if _, err := validateRemoteImportURL(raw, i18n.DefaultLang); err == nil {
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

func TestRemoteImportHTTPClientInsecureTLSOption(t *testing.T) {
	secureTransport := remoteImportHTTPClient(false).Transport
	secure, ok := secureTransport.(*http.Transport)
	if !ok {
		t.Fatalf("secure transport type = %T, want *http.Transport", secureTransport)
	}
	if secure.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("default remote import client should verify TLS certificates")
	}

	insecureTransport := remoteImportHTTPClient(true).Transport
	insecure, ok := insecureTransport.(*http.Transport)
	if !ok {
		t.Fatalf("insecure transport type = %T, want *http.Transport", insecureTransport)
	}
	if !insecure.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("insecure remote import client should allow untrusted TLS certificates")
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
