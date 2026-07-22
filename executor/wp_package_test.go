package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func validWordPressZIP(t *testing.T) string {
	return writeTestZIP(t, map[string]string{
		"wordpress/":                        "",
		"wordpress/wp-admin/":               "",
		"wordpress/wp-admin/index.php":      "<?php",
		"wordpress/wp-includes/":            "",
		"wordpress/wp-includes/load.php":    "<?php",
		"wordpress/wp-includes/version.php": "<?php\n$wp_version = '7.0.2';\n$wp_local_package = 'zh_CN';\n",
		"wordpress/wp-settings.php":         "<?php",
		"wordpress/wp-load.php":             "<?php",
	})
}

func TestValidateWordPressPackage(t *testing.T) {
	report, err := ValidateWordPressPackage(context.Background(), validWordPressZIP(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != "7.0.2" || report.Locale != "zh_CN" || report.Verification != "structure_only" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestValidateWordPressPackageRejectsExtraRoot(t *testing.T) {
	filename := writeTestZIP(t, map[string]string{
		"wordpress/wp-admin/index.php":      "x",
		"wordpress/wp-includes/load.php":    "x",
		"wordpress/wp-includes/version.php": "$wp_version = '7.0.2';",
		"wordpress/wp-settings.php":         "x",
		"wordpress/wp-load.php":             "x",
		"readme.txt":                        "extra",
	})
	_, err := ValidateWordPressPackage(context.Background(), filename)
	if code := ArchiveErrorCode(err); code != "package_structure_invalid" {
		t.Fatalf("code = %q, want package_structure_invalid", code)
	}
}

func TestParseWordPressVersionFile(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		wantVersion string
		wantLocale  string
		wantError   bool
	}{
		{name: "default locale", body: "$wp_version = '7.0.2';", wantVersion: "7.0.2", wantLocale: "en_US"},
		{name: "double quoted", body: "$wp_version = \"7.1-beta1\";\n$wp_local_package = \"zh_CN\";", wantVersion: "7.1-beta1", wantLocale: "zh_CN"},
		{name: "duplicate version", body: "$wp_version = '7.0';\n$wp_version = '7.1';", wantError: true},
		{name: "dynamic version", body: "$wp_version = getenv('VERSION');", wantError: true},
		{name: "dynamic locale", body: "$wp_version = '7.0';\n$wp_local_package = get_locale();", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			version, locale, err := parseWordPressVersionFile(test.body)
			if (err != nil) != test.wantError {
				t.Fatalf("err = %v, wantError %v", err, test.wantError)
			}
			if !test.wantError && (version != test.wantVersion || locale != test.wantLocale) {
				t.Fatalf("got %q %q, want %q %q", version, locale, test.wantVersion, test.wantLocale)
			}
		})
	}
}

func TestWPPackageServiceFailedValidationPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wordpress.zip")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	service, err := NewWPPackageService(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PublishUpload(context.Background(), bytes.NewBufferString("not zip"), 7)
	if err == nil {
		t.Fatal("expected validation error")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("target changed to %q", got)
	}
}

func TestWPPackageServicePublishesValidPackage(t *testing.T) {
	source := validWordPressZIP(t)
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "wordpress.zip")
	service, err := NewWPPackageService(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.PublishUpload(context.Background(), bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	sha, _, err := fileSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	if sha != report.Inspection.SHA256 {
		t.Fatalf("sha = %s, want %s", sha, report.Inspection.SHA256)
	}
}

func TestWPPackageServiceRejectsConcurrentOperation(t *testing.T) {
	service, err := NewWPPackageService(filepath.Join(t.TempDir(), "wordpress.zip"), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	_, err = service.PublishUpload(context.Background(), bytes.NewBufferString("anything"), 8)
	if code := ArchiveErrorCode(err); code != "package_busy" {
		t.Fatalf("code = %q, want package_busy", code)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWPPackageServiceDownloadUsesFixedOfficialURL(t *testing.T) {
	body, err := os.ReadFile(validWordPressZIP(t))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != wordpressLatestURL {
			t.Fatalf("URL = %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
	service, err := NewWPPackageService(filepath.Join(t.TempDir(), "wordpress.zip"), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DownloadLatest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAllowedWordPressURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://wordpress.org/latest.zip":                     true,
		"https://downloads.wordpress.org/release/test.zip":     true,
		"http://wordpress.org/latest.zip":                      false,
		"https://wordpress.org.example.com/latest.zip":         false,
		"https://downloads.wordpress.org.example.com/test.zip": false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := allowedWordPressURL(u); got != want {
			t.Errorf("allowedWordPressURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
