package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

func setupWPPackageSchedulerTestDB(t *testing.T) {
	t.Helper()
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = oldDB
	})
}

func TestFetchLatestStableWordPressVersionParsesLatestEntry(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.wordpress.org/core/stable-check/1.0/" {
			t.Fatalf("URL = %s", req.URL)
		}
		body := `{"6.5.5":"outdated","6.6.2":"latest","6.6.1":"insecure"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header), Request: req}, nil
	})}
	version, err := fetchLatestStableWordPressVersion(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if version != "6.6.2" {
		t.Fatalf("version = %q, want 6.6.2", version)
	}
}

func TestFetchLatestStableWordPressVersionRejectsNonOKStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(bytes.NewBufferString("")), Header: make(http.Header), Request: req}, nil
	})}
	if _, err := fetchLatestStableWordPressVersion(context.Background(), client); err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestFetchLatestStableWordPressVersionRejectsMissingLatestEntry(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"6.5.5":"outdated"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header), Request: req}, nil
	})}
	if _, err := fetchLatestStableWordPressVersion(context.Background(), client); err == nil {
		t.Fatal("expected error when no version is marked latest")
	}
}

func TestRunWPPackageAutoCheckSkipsWhenDisabled(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	setSecuritySetting("wp_package_auto_check_enabled", "false")

	target := filepath.Join(t.TempDir(), "wordpress.zip")
	if err := os.WriteFile(target, []byte("old-package"), 0644); err != nil {
		t.Fatal(err)
	}
	svc, err := NewWPPackageService(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetchCalled := false
	runWPPackageAutoCheck(&config.Config{Paths: config.PathsConfig{WordPressPackage: target}}, svc, func(ctx context.Context) (string, error) {
		fetchCalled = true
		return "9.9.9", nil
	})
	if fetchCalled {
		t.Fatal("version fetch must not run when auto-check is disabled")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old-package" {
		t.Fatalf("target changed to %q", got)
	}
}

func TestRunWPPackageAutoCheckSkipsDownloadWhenUpToDate(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	target := validWordPressZIP(t) // locally "published" package is version 7.0.2

	downloadCalled := false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalled = true
		return nil, errors.New("must not be called")
	})}
	svc, err := NewWPPackageService(target, client)
	if err != nil {
		t.Fatal(err)
	}

	runWPPackageAutoCheck(&config.Config{Paths: config.PathsConfig{WordPressPackage: target}}, svc, func(ctx context.Context) (string, error) {
		return "7.0.2", nil // same as local: nothing to do
	})
	if downloadCalled {
		t.Fatal("download must not run when local package is already current")
	}
	if got := readSecuritySetting("wp_package_last_check_status"); got != "up_to_date" {
		t.Fatalf("status = %q, want up_to_date", got)
	}
}

func TestRunWPPackageAutoCheckDownloadsNewerVersion(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	target := filepath.Join(t.TempDir(), "wordpress.zip")
	if err := os.WriteFile(target, []byte("old-package"), 0644); err != nil {
		t.Fatal(err)
	}
	newZIP, err := os.ReadFile(validWordPressZIP(t))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(newZIP)), Header: make(http.Header), Request: req}, nil
	})}
	svc, err := NewWPPackageService(target, client)
	if err != nil {
		t.Fatal(err)
	}

	runWPPackageAutoCheck(&config.Config{Paths: config.PathsConfig{WordPressPackage: target}}, svc, func(ctx context.Context) (string, error) {
		return "7.0.2", nil
	})

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newZIP) {
		t.Fatal("target was not replaced with the downloaded package")
	}
	if status := readSecuritySetting("wp_package_last_check_status"); status != "updated" {
		t.Fatalf("status = %q, want updated", status)
	}
}

func TestRunWPPackageAutoCheckFailedDownloadPreservesExistingPackage(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	target := filepath.Join(t.TempDir(), "wordpress.zip")
	original := []byte("manually-uploaded-package")
	if err := os.WriteFile(target, original, 0644); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Simulate a flaky network returning a truncated/corrupt body.
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString("not-a-zip")), Header: make(http.Header), Request: req}, nil
	})}
	svc, err := NewWPPackageService(target, client)
	if err != nil {
		t.Fatal(err)
	}

	runWPPackageAutoCheck(&config.Config{Paths: config.PathsConfig{WordPressPackage: target}}, svc, func(ctx context.Context) (string, error) {
		return "9.9.9", nil
	})

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("a failed auto-download must not touch the existing package; got %q", got)
	}
	if status := readSecuritySetting("wp_package_last_check_status"); status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if errText := readSecuritySetting("wp_package_last_check_error"); errText == "" {
		t.Fatal("expected last_check_error to be recorded")
	}
}

func TestRunWPPackageAutoCheckVersionFetchFailurePreservesExistingPackage(t *testing.T) {
	setupWPPackageSchedulerTestDB(t)
	target := filepath.Join(t.TempDir(), "wordpress.zip")
	original := []byte("manually-uploaded-package")
	if err := os.WriteFile(target, original, 0644); err != nil {
		t.Fatal(err)
	}
	downloadCalled := false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalled = true
		return nil, errors.New("must not be called")
	})}
	svc, err := NewWPPackageService(target, client)
	if err != nil {
		t.Fatal(err)
	}

	runWPPackageAutoCheck(&config.Config{Paths: config.PathsConfig{WordPressPackage: target}}, svc, func(ctx context.Context) (string, error) {
		return "", errors.New("network unreachable")
	})

	if downloadCalled {
		t.Fatal("download must not be attempted when the version check itself failed")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("target must remain untouched when the version check fails")
	}
	if status := readSecuritySetting("wp_package_last_check_status"); status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
}
