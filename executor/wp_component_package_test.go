package executor

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestValidateWPComponentPackagePlugin(t *testing.T) {
	filename := writeComponentZIP(t, []componentZIPEntry{
		{name: "classic-editor/", directory: true},
		{name: "classic-editor/classic-editor.php", body: "<?php\r/*\rPlugin Name: Classic Editor\rVersion: 1.7.0\r*/"},
		{name: "classic-editor/readme.txt", body: "readme"},
	})
	report, err := ValidateWPComponentPackage(context.Background(), filename, WPComponentPackageExpectation{
		ComponentType: "plugin", ComponentKey: "classic-editor/classic-editor.php", OfficialSlug: "classic-editor", TargetVersion: "1.7.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Root != "classic-editor" || report.MainFile != "classic-editor/classic-editor.php" || report.Version != "1.7.0" || report.Template != "" || report.PHPFiles != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Inspection.EntryCount != 3 || report.Inspection.VerifiedUncompressed == 0 {
		t.Fatalf("unexpected inspection: %+v", report.Inspection)
	}
}

func TestValidateWPComponentPackageTheme(t *testing.T) {
	filename := writeComponentZIP(t, []componentZIPEntry{
		{name: "child-theme/style.css", body: "/*\nTheme Name: Child Theme\nVersion: 2.4.0\nTemplate: parent-theme\n*/"},
		{name: "child-theme/functions.php", body: "<?php\n"},
	})
	report, err := ValidateWPComponentPackage(context.Background(), filename, WPComponentPackageExpectation{
		ComponentType: "theme", ComponentKey: "child-theme", OfficialSlug: "child-theme", TargetVersion: "2.4.0", Template: "parent-theme",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Root != "child-theme" || report.MainFile != "" || report.Version != "2.4.0" || report.Template != "parent-theme" || report.PHPFiles != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestValidateWPComponentPackageRejectsInvalidExpectation(t *testing.T) {
	filename := writeComponentZIP(t, []componentZIPEntry{{name: "plugin/plugin.php", body: "<?php\n/* Plugin Name: P\nVersion: 1.0.0\n*/"}})
	tests := []WPComponentPackageExpectation{
		{},
		{ComponentType: "core", ComponentKey: "plugin/plugin.php", OfficialSlug: "plugin", TargetVersion: "1.0.0"},
		{ComponentType: "plugin", ComponentKey: "plugin.php", OfficialSlug: "plugin", TargetVersion: "1.0.0"},
		{ComponentType: "plugin", ComponentKey: "plugin/sub/plugin.php", OfficialSlug: "plugin", TargetVersion: "1.0.0"},
		{ComponentType: "plugin", ComponentKey: "other/plugin.php", OfficialSlug: "plugin", TargetVersion: "1.0.0"},
		{ComponentType: "theme", ComponentKey: "theme/child", OfficialSlug: "theme", TargetVersion: "1.0.0"},
		{ComponentType: "theme", ComponentKey: "theme", OfficialSlug: "other", TargetVersion: "1.0.0"},
	}
	for i, expected := range tests {
		if _, err := ValidateWPComponentPackage(context.Background(), filename, expected); ArchiveErrorCode(err) != "package_identity_invalid" {
			t.Fatalf("case %d code = %q, want package_identity_invalid", i, ArchiveErrorCode(err))
		}
	}
}

func TestValidateWPComponentPackageRejectsIdentityAndVersionDrift(t *testing.T) {
	basePlugin := WPComponentPackageExpectation{ComponentType: "plugin", ComponentKey: "sample/sample.php", OfficialSlug: "sample", TargetVersion: "2.0.0"}
	tests := []struct {
		name     string
		entries  []componentZIPEntry
		expected WPComponentPackageExpectation
		code     string
	}{
		{name: "extra root", entries: []componentZIPEntry{{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")}, {name: "other/file.txt", body: "x"}}, expected: basePlugin, code: "package_structure_invalid"},
		{name: "root drift", entries: []componentZIPEntry{{name: "renamed/sample.php", body: pluginHeader("Sample", "2.0.0")}}, expected: basePlugin, code: "package_identity_invalid"},
		{name: "main file missing", entries: []componentZIPEntry{{name: "sample/other.php", body: pluginHeader("Sample", "2.0.0")}}, expected: basePlugin, code: "package_identity_invalid"},
		{name: "plugin name missing", entries: []componentZIPEntry{{name: "sample/sample.php", body: "<?php\n/* Version: 2.0.0 */"}}, expected: basePlugin, code: "package_identity_invalid"},
		{name: "plugin version drift", entries: []componentZIPEntry{{name: "sample/sample.php", body: pluginHeader("Sample", "1.9.0")}}, expected: basePlugin, code: "package_version_invalid"},
		{name: "theme name missing", entries: []componentZIPEntry{{name: "theme/style.css", body: "/* Version: 2.0.0 */"}}, expected: WPComponentPackageExpectation{ComponentType: "theme", ComponentKey: "theme", OfficialSlug: "theme", TargetVersion: "2.0.0"}, code: "package_identity_invalid"},
		{name: "theme template drift", entries: []componentZIPEntry{{name: "theme/style.css", body: themeHeader("Theme", "2.0.0", "new-parent")}}, expected: WPComponentPackageExpectation{ComponentType: "theme", ComponentKey: "theme", OfficialSlug: "theme", TargetVersion: "2.0.0", Template: "old-parent"}, code: "package_identity_invalid"},
		{name: "theme version drift", entries: []componentZIPEntry{{name: "theme/style.css", body: themeHeader("Theme", "1.0.0", "")}}, expected: WPComponentPackageExpectation{ComponentType: "theme", ComponentKey: "theme", OfficialSlug: "theme", TargetVersion: "2.0.0"}, code: "package_version_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filename := writeComponentZIP(t, test.entries)
			_, err := ValidateWPComponentPackage(context.Background(), filename, test.expected)
			if code := ArchiveErrorCode(err); code != test.code {
				t.Fatalf("code = %q, want %q", code, test.code)
			}
		})
	}
}

func TestValidateWPComponentPackageRejectsUnicodeCollisionAndLongPath(t *testing.T) {
	expected := WPComponentPackageExpectation{ComponentType: "plugin", ComponentKey: "sample/sample.php", OfficialSlug: "sample", TargetVersion: "2.0.0"}
	unicodeZIP := writeComponentZIP(t, []componentZIPEntry{
		{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")},
		{name: "sample/caf\u00e9.txt", body: "nfc"},
		{name: "sample/cafe\u0301.txt", body: "nfd"},
	})
	if _, err := ValidateWPComponentPackage(context.Background(), unicodeZIP, expected); ArchiveErrorCode(err) != "package_identity_invalid" {
		t.Fatalf("unicode code = %q", ArchiveErrorCode(err))
	}
	caseZIP := writeComponentZIP(t, []componentZIPEntry{
		{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")},
		{name: "sample/Assets/one.txt", body: "one"},
		{name: "sample/assets/two.txt", body: "two"},
	})
	if _, err := ValidateWPComponentPackage(context.Background(), caseZIP, expected); ArchiveErrorCode(err) != "package_identity_invalid" {
		t.Fatalf("case collision code = %q", ArchiveErrorCode(err))
	}
	longZIP := writeComponentZIP(t, []componentZIPEntry{
		{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")},
		{name: "sample/" + strings.Repeat("a", wpComponentMaxPathBytes) + ".txt", body: "x"},
	})
	if _, err := ValidateWPComponentPackage(context.Background(), longZIP, expected); ArchiveErrorCode(err) != "package_structure_invalid" {
		t.Fatalf("long path code = %q", ArchiveErrorCode(err))
	}
}

func TestValidateWPComponentPackageRejectsPHPFileCount(t *testing.T) {
	entries := make([]componentZIPEntry, 0, wpComponentMaxPHPFiles+1)
	entries = append(entries, componentZIPEntry{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")})
	for i := 0; i < wpComponentMaxPHPFiles; i++ {
		entries = append(entries, componentZIPEntry{name: "sample/php/" + strconv.Itoa(i) + ".php", body: "<?php"})
	}
	filename := writeComponentZIP(t, entries)
	_, err := ValidateWPComponentPackage(context.Background(), filename, WPComponentPackageExpectation{ComponentType: "plugin", ComponentKey: "sample/sample.php", OfficialSlug: "sample", TargetVersion: "2.0.0"})
	if code := ArchiveErrorCode(err); code != "package_structure_invalid" {
		t.Fatalf("code = %q, want package_structure_invalid", code)
	}
}

func TestValidateWPComponentPackageReadsOnlyFirstHeaderBlock(t *testing.T) {
	body := "<?php\n/* Plugin Name: Sample\n" + strings.Repeat(" ", wpComponentHeaderBytes) + "\nVersion: 2.0.0\n*/"
	filename := writeComponentZIP(t, []componentZIPEntry{{name: "sample/sample.php", body: body, store: true}})
	_, err := ValidateWPComponentPackage(context.Background(), filename, WPComponentPackageExpectation{ComponentType: "plugin", ComponentKey: "sample/sample.php", OfficialSlug: "sample", TargetVersion: "2.0.0"})
	if code := ArchiveErrorCode(err); code != "package_version_invalid" {
		t.Fatalf("code = %q, want package_version_invalid", code)
	}
}

func TestValidateWPComponentPackageRejectsPHPBudgetsAndNestedPackage(t *testing.T) {
	expected := WPComponentPackageExpectation{ComponentType: "plugin", ComponentKey: "sample/sample.php", OfficialSlug: "sample", TargetVersion: "2.0.0"}
	largeZIP := writeComponentZIP(t, []componentZIPEntry{
		{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")},
		{name: "sample/large.php", body: strings.Repeat("x", wpComponentMaxPHPBytes+1), store: true},
	})
	if _, err := ValidateWPComponentPackage(context.Background(), largeZIP, expected); ArchiveErrorCode(err) != "package_structure_invalid" {
		t.Fatalf("large PHP code = %q", ArchiveErrorCode(err))
	}
	for _, nestedName := range []string{"sample/sample.zip", "sample/assets/sample.ZIP", "sample/backup-2024.zip"} {
		nestedZIP := writeComponentZIP(t, []componentZIPEntry{
			{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")},
			{name: nestedName, body: "nested"},
		})
		if _, err := ValidateWPComponentPackage(context.Background(), nestedZIP, expected); ArchiveErrorCode(err) != "package_structure_invalid" {
			t.Fatalf("nested package %q code = %q", nestedName, ArchiveErrorCode(err))
		}
	}
}

func TestValidateWPComponentPackageHonorsCancelledContext(t *testing.T) {
	filename := writeComponentZIP(t, []componentZIPEntry{{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ValidateWPComponentPackage(ctx, filename, WPComponentPackageExpectation{ComponentType: "plugin", ComponentKey: "sample/sample.php", OfficialSlug: "sample", TargetVersion: "2.0.0"})
	if code := ArchiveErrorCode(err); code != "archive_validation_timeout" {
		t.Fatalf("code = %q, want archive_validation_timeout", code)
	}
}

type componentZIPEntry struct {
	name      string
	body      string
	directory bool
	store     bool
}

func writeComponentZIP(t *testing.T, entries []componentZIPEntry) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "component.zip")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, entry := range entries {
		method := uint16(zip.Deflate)
		if entry.store {
			method = zip.Store
		}
		header := &zip.FileHeader{Name: entry.name, Method: method}
		if entry.directory {
			header.SetMode(os.ModeDir | 0755)
		} else {
			header.SetMode(0644)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return filename
}

func pluginHeader(name, version string) string {
	return "<?php\n/*\nPlugin Name: " + name + "\nVersion: " + version + "\n*/"
}

func themeHeader(name, version, template string) string {
	value := "/*\nTheme Name: " + name + "\nVersion: " + version + "\n"
	if template != "" {
		value += "Template: " + template + "\n"
	}
	return value + "*/"
}
