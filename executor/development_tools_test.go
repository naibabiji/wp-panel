package executor

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWPCLICommandVersionAllowsRootStatusCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wp")
	script := "#!/bin/sh\nif [ \"$1\" != \"--allow-root\" ] || [ \"$2\" != \"--version\" ]; then exit 1; fi\necho 'WP-CLI 2.12.0'\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	version, ok := wpCLICommandVersion(context.Background(), path)
	if !ok || !strings.Contains(version, "WP-CLI 2.12.0") {
		t.Fatalf("version=%q ok=%v", version, ok)
	}
}

func TestDownloadWPCLIWithFallbackUsesProxyAfterDirectFailure(t *testing.T) {
	content := []byte("valid wp-cli test content")
	sum := sha512.Sum512(content)
	expectedSHA512 := hex.EncodeToString(sum[:])
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("invalid direct content"))
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer proxy.Close()

	dst, err := os.CreateTemp(t.TempDir(), "wp-cli-*")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := downloadWPCLIWithFallback(context.Background(), dst, direct.URL, proxy.URL, expectedSHA512); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content = %q", got)
	}
}

func TestDownloadWPCLIWithFallbackReportsProxyFailure(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer failing.Close()

	dst, err := os.CreateTemp(t.TempDir(), "wp-cli-*")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	err = downloadWPCLIWithFallback(context.Background(), dst, failing.URL, failing.URL, strings.Repeat("0", sha512.Size*2))
	if !errors.Is(err, ErrWPCLIProxyDownloadFailed) {
		t.Fatalf("error = %v", err)
	}
}
