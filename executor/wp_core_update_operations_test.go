package executor

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCoreRunner struct{ updates, checks int }

func (f *fakeCoreRunner) Update(context.Context, wpCoreUpdateExecution) error {
	f.updates++
	return nil
}
func (f *fakeCoreRunner) CheckLoad(_ context.Context, _ wpCoreUpdateExecution, _ string) error {
	f.checks++
	return nil
}

func TestWPCoreSystemOperationsPrepareAndHealth(t *testing.T) {
	webRoot := filepath.Join(t.TempDir(), "wordpress")
	if err := os.Mkdir(webRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writeWordPressCoreFixture(t, webRoot)
	writeVersionFixture(t, webRoot, "7.0.1", "zh_CN")
	packagePath := validWordPressZIP(t)
	runner := &fakeCoreRunner{}
	fetchCalls := []string{}
	fetch := func(_ context.Context, version, locale string) (wpCoreChecksumSet, error) {
		fetchCalls = append(fetchCalls, version+":"+locale)
		if version == "7.0.2" {
			return checksumSetForPackage(t, packagePath, version, locale), nil
		}
		return checksumFixture(t, webRoot, version, locale), nil
	}
	probeCalls := 0
	ops, err := newWPCoreSystemOperations(runner, func(context.Context, string, string) error { return nil }, func(context.Context, string, string, string, string) error { return nil }, fetch, func(context.Context, string) error { probeCalls++; return nil }, func(context.Context, wpCoreUpdateExecution, ZIPInspection) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	execution := wpCoreUpdateExecution{Task: WPUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", SiteID: 1, CurrentVersion: "7.0.1", TargetVersion: "7.0.2", VerificationLevel: "official_verified"}, WebRoot: webRoot, Domain: "example.com", SystemUser: "wp_test", PackagePath: packagePath}
	if err := ops.Prepare(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if len(fetchCalls) != 2 || fetchCalls[0] != "7.0.2:zh_CN" || fetchCalls[1] != "7.0.1:zh_CN" {
		t.Fatalf("fetch calls=%v", fetchCalls)
	}
	writeVersionFixture(t, webRoot, "7.0.2", "zh_CN")
	prepared, _ := ops.preparedHealth(execution.Task.ID)
	prepared.target = checksumFixture(t, webRoot, "7.0.2", "zh_CN")
	ops.prepared[execution.Task.ID] = prepared
	if err := ops.CheckTargetHealth(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if runner.checks != 1 || probeCalls != 1 {
		t.Fatalf("checks=%d probes=%d", runner.checks, probeCalls)
	}
}

func TestCheckWPCoreFilesystemRejectsMaintenanceAndMismatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wordpress")
	if err := os.MkdirAll(filepath.Join(root, "wp-includes"), 0755); err != nil {
		t.Fatal(err)
	}
	writeVersionFixture(t, root, "7.0.2", "en_US")
	if err := os.WriteFile(filepath.Join(root, "wp-settings.php"), []byte("core"), 0644); err != nil {
		t.Fatal(err)
	}
	set := checksumFixture(t, root, "7.0.2", "en_US")
	if err := checkWPCoreFilesystem(root, set); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".maintenance"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkWPCoreFilesystem(root, set); err == nil {
		t.Fatal("expected maintenance rejection")
	}
	_ = os.Remove(filepath.Join(root, ".maintenance"))
	if err := os.WriteFile(filepath.Join(root, "wp-settings.php"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkWPCoreFilesystem(root, set); err == nil {
		t.Fatal("expected checksum rejection")
	}
}

func TestWPCoreSystemOperationsControlsMaintenanceFile(t *testing.T) {
	root := t.TempDir()
	ops, err := newWPCoreSystemOperations(&fakeCoreRunner{}, func(context.Context, string, string) error { return nil }, func(context.Context, string, string, string, string) error { return nil }, func(context.Context, string, string) (wpCoreChecksumSet, error) { return wpCoreChecksumSet{}, nil }, func(context.Context, string) error { return nil }, func(context.Context, wpCoreUpdateExecution, ZIPInspection) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	execution := wpCoreUpdateExecution{WebRoot: root}
	if err := ops.SetMaintenance(context.Background(), execution, true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".maintenance"))
	if err != nil || !strings.Contains(string(body), "$upgrading") {
		t.Fatalf("maintenance=%q err=%v", body, err)
	}
	if err := ops.SetMaintenance(context.Background(), execution, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".maintenance")); !os.IsNotExist(err) {
		t.Fatalf("maintenance remains: %v", err)
	}
}

func TestValidateWPCoreChecksumSetRejectsTraversalAndMissingIdentity(t *testing.T) {
	base := wpCoreChecksumSet{Version: "7.0.2", Locale: "en_US", Checksums: map[string]string{"wp-includes/version.php": strings.Repeat("a", 32), "wp-settings.php": strings.Repeat("b", 32)}}
	if err := validateWPCoreChecksumSet(base, "7.0.2", "en_US"); err != nil {
		t.Fatal(err)
	}
	base.Checksums["../escape.php"] = strings.Repeat("c", 32)
	if err := validateWPCoreChecksumSet(base, "7.0.2", "en_US"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestVerifyWPCorePackageChecksumsRejectsUnlistedCoreFile(t *testing.T) {
	packagePath := validWordPressZIP(t)
	set := checksumSetForPackage(t, packagePath, "7.0.2", "en_US")

	source, err := zip.OpenReader(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	maliciousPath := filepath.Join(t.TempDir(), "extra-core-file.zip")
	destination, err := os.Create(maliciousPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(destination)
	for _, entry := range source.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		writer, err := zw.CreateHeader(&entry.FileHeader)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(writer, reader); err != nil {
			t.Fatal(err)
		}
		_ = reader.Close()
	}
	extra, err := zw.Create("wordpress/wp-admin/backdoor.php")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extra.Write([]byte("<?php")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	_ = source.Close()

	_, err = InspectZIP(context.Background(), maliciousPath, WordPressFullZIPPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWPCorePackageChecksums(maliciousPath, set); err == nil {
		t.Fatal("expected unlisted core file rejection")
	}
}

func TestDefaultWPCoreHomeProberUsesLoopbackAndHost(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "http://127.0.0.1/" || req.Host != "example.com" {
			t.Fatalf("request=%s host=%s", req.URL, req.Host)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header), Request: req}, nil
	})}
	if err := defaultWPCoreHomeProber(client)(context.Background(), "example.com"); err != nil {
		t.Fatal(err)
	}
}

func checksumFixture(t *testing.T, root, version, locale string) wpCoreChecksumSet {
	t.Helper()
	checks := map[string]string{}
	for _, name := range []string{"wp-includes/version.php", "wp-settings.php"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		checks[name] = fmt.Sprintf("%x", md5.Sum(body))
	}
	return wpCoreChecksumSet{Version: version, Locale: locale, Checksums: checks}
}

func checksumSetForPackage(t *testing.T, packagePath, version, locale string) wpCoreChecksumSet {
	t.Helper()
	zr, err := zip.OpenReader(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	checks := map[string]string{}
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !strings.HasPrefix(entry.Name, "wordpress/") || strings.HasPrefix(entry.Name, "wordpress/wp-content/") {
			continue
		}
		src, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		h := md5.New()
		_, err = io.Copy(h, src)
		_ = src.Close()
		if err != nil {
			t.Fatal(err)
		}
		checks[strings.TrimPrefix(entry.Name, "wordpress/")] = fmt.Sprintf("%x", h.Sum(nil))
	}
	return wpCoreChecksumSet{Version: version, Locale: locale, Checksums: checks}
}
func writeVersionFixture(t *testing.T, root, version, locale string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "wp-includes"), 0755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("<?php\n$wp_version = '%s';\n$wp_local_package = '%s';\n", version, locale)
	if err := os.WriteFile(filepath.Join(root, "wp-includes", "version.php"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
