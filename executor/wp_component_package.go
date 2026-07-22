package executor

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const (
	wpComponentMaxPHPFiles  = 5_000
	wpComponentMaxPHPBytes  = 8 << 20
	wpComponentMaxPathBytes = 1_024
	wpComponentHeaderBytes  = 8 << 10
)

var (
	wpComponentSlugPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	wpComponentPHPFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.php$`)
	wpComponentVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,63}$`)
)

type WPComponentPackageExpectation struct {
	ComponentType string
	ComponentKey  string
	OfficialSlug  string
	TargetVersion string
	Template      string
}

type WPComponentPackageReport struct {
	Inspection ZIPInspection
	Root       string
	Version    string
	MainFile   string
	Template   string
	PHPFiles   int
}

func ValidateWPComponentPackage(ctx context.Context, filename string, expected WPComponentPackageExpectation) (WPComponentPackageReport, error) {
	if !validWPComponentExpectation(expected) {
		return WPComponentPackageReport{}, archiveError("package_identity_invalid", nil)
	}
	f, err := os.Open(filename)
	if err != nil {
		return WPComponentPackageReport{}, archiveError("archive_open_failed", err)
	}
	defer f.Close()
	inspection, zr, err := inspectOpenZIP(ctx, f, WordPressFullZIPPolicy())
	if err != nil {
		return WPComponentPackageReport{}, err
	}
	report := WPComponentPackageReport{Inspection: inspection}
	root, err := validateWPComponentPaths(ctx, inspection.NormalizedNames, expected)
	if err != nil {
		return WPComponentPackageReport{}, err
	}
	report.Root = root

	entries := make(map[string]*zip.File, len(zr.File))
	for _, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return WPComponentPackageReport{}, archiveError("archive_validation_timeout", err)
		}
		name := strings.TrimSuffix(zf.Name, "/")
		entries[name] = zf
		if err := validateWPComponentPHPSize(zf); err != nil {
			return WPComponentPackageReport{}, err
		}
		if !zf.FileInfo().IsDir() && strings.EqualFold(path.Ext(name), ".php") {
			report.PHPFiles++
			if report.PHPFiles > wpComponentMaxPHPFiles {
				return WPComponentPackageReport{}, archiveError("package_structure_invalid", nil)
			}
		}
	}

	if expected.ComponentType == "plugin" {
		return validateWPPluginPackage(report, entries, expected)
	}
	return validateWPThemePackage(report, entries, expected)
}

func validWPComponentExpectation(expected WPComponentPackageExpectation) bool {
	if !wpComponentSlugPattern.MatchString(expected.OfficialSlug) || !wpComponentVersionPattern.MatchString(expected.TargetVersion) {
		return false
	}
	switch expected.ComponentType {
	case "plugin":
		if expected.Template != "" || strings.Count(expected.ComponentKey, "/") != 1 {
			return false
		}
		parts := strings.Split(expected.ComponentKey, "/")
		return parts[0] == expected.OfficialSlug && wpComponentPHPFilePattern.MatchString(parts[1]) && len(expected.ComponentKey) <= wpComponentMaxPathBytes
	case "theme":
		return expected.ComponentKey == expected.OfficialSlug && (expected.Template == "" || wpComponentSlugPattern.MatchString(expected.Template))
	default:
		return false
	}
}

func validateWPComponentPaths(ctx context.Context, names []string, expected WPComponentPackageExpectation) (string, error) {
	roots := make(map[string]struct{})
	canonicalNames := make(map[string]string, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return "", archiveError("archive_validation_timeout", err)
		}
		if len(name) > wpComponentMaxPathBytes {
			return "", archiveError("package_structure_invalid", nil)
		}
		parts := strings.Split(name, "/")
		roots[parts[0]] = struct{}{}
		for i := 1; i <= len(parts); i++ {
			original := strings.Join(parts[:i], "/")
			canonical := strings.ToLower(norm.NFC.String(original))
			if previous, ok := canonicalNames[canonical]; ok && previous != original {
				return "", archiveError("package_identity_invalid", nil)
			}
			canonicalNames[canonical] = original
		}
		if strings.EqualFold(path.Ext(name), ".zip") {
			return "", archiveError("package_structure_invalid", nil)
		}
	}
	if len(roots) != 1 {
		return "", archiveError("package_structure_invalid", nil)
	}
	for root := range roots {
		if root != expected.OfficialSlug {
			return "", archiveError("package_identity_invalid", nil)
		}
		return root, nil
	}
	return "", archiveError("package_structure_invalid", nil)
}

func validateWPPluginPackage(report WPComponentPackageReport, entries map[string]*zip.File, expected WPComponentPackageExpectation) (WPComponentPackageReport, error) {
	mainFile, ok := entries[expected.ComponentKey]
	if !ok || mainFile.FileInfo().IsDir() {
		return WPComponentPackageReport{}, archiveError("package_identity_invalid", nil)
	}
	headers, err := readWPComponentHeaders(mainFile, "Plugin Name", "Version")
	if err != nil || headers["Plugin Name"] == "" {
		return WPComponentPackageReport{}, archiveError("package_identity_invalid", err)
	}
	if headers["Version"] != expected.TargetVersion {
		return WPComponentPackageReport{}, archiveError("package_version_invalid", nil)
	}
	report.MainFile = expected.ComponentKey
	report.Version = headers["Version"]
	return report, nil
}

func validateWPThemePackage(report WPComponentPackageReport, entries map[string]*zip.File, expected WPComponentPackageExpectation) (WPComponentPackageReport, error) {
	style, ok := entries[expected.OfficialSlug+"/style.css"]
	if !ok || style.FileInfo().IsDir() {
		return WPComponentPackageReport{}, archiveError("package_identity_invalid", nil)
	}
	headers, err := readWPComponentHeaders(style, "Theme Name", "Version", "Template")
	if err != nil || headers["Theme Name"] == "" || headers["Template"] != expected.Template {
		return WPComponentPackageReport{}, archiveError("package_identity_invalid", err)
	}
	if headers["Version"] != expected.TargetVersion {
		return WPComponentPackageReport{}, archiveError("package_version_invalid", nil)
	}
	report.Version = headers["Version"]
	report.Template = headers["Template"]
	return report, nil
}

func validateWPComponentPHPSize(zf *zip.File) error {
	if strings.EqualFold(path.Ext(zf.Name), ".php") && zf.UncompressedSize64 > wpComponentMaxPHPBytes {
		return archiveError("package_structure_invalid", nil)
	}
	return nil
}

func readWPComponentHeaders(zf *zip.File, names ...string) (map[string]string, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(rc, wpComponentHeaderBytes))
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	text := strings.ReplaceAll(string(body), "\r", "\n")
	headers := make(map[string]string, len(names))
	for _, name := range names {
		headers[name] = findWPComponentHeader(text, name)
	}
	return headers, nil
}

func findWPComponentHeader(body, header string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimLeft(line, " \t")
		if len(line) >= 5 && strings.EqualFold(line[:5], "<?php") {
			line = strings.TrimLeft(line[5:], " \t")
		}
		line = strings.TrimLeft(line, " \t/*#@")
		prefix := header + ":"
		if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
			continue
		}
		value := line[len(prefix):]
		if end := strings.Index(value, "*/"); end >= 0 {
			value = value[:end]
		}
		if end := strings.Index(value, "?>"); end >= 0 {
			value = value[:end]
		}
		return strings.TrimSpace(value)
	}
	return ""
}
