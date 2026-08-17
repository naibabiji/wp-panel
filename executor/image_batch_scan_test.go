package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/naibabiji/wp-panel/config"
)

// setUpImageBatchTestSite builds a minimal fake WordPress site directory
// (wwwRoot/domain/, with a wp-load.php so validateInventorySitePath accepts
// it) and points config.AppConfig.Paths.WWWRoot at it, restoring the previous
// config on test cleanup.
func setUpImageBatchTestSite(t *testing.T) (wwwRoot, webRoot string) {
	t.Helper()
	wwwRoot = t.TempDir()
	webRoot = filepath.Join(wwwRoot, "example.com")
	if err := os.MkdirAll(webRoot, 0755); err != nil {
		t.Fatalf("mkdir webRoot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "wp-load.php"), []byte("<?php\n"), 0644); err != nil {
		t.Fatalf("seed wp-load.php: %v", err)
	}

	oldConfig := config.AppConfig
	config.AppConfig = &config.Config{Paths: config.PathsConfig{WWWRoot: wwwRoot}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	return wwwRoot, webRoot
}

func TestScanSiteUploadsForImagesFindsJPEGAndPNGOnly(t *testing.T) {
	_, webRoot := setUpImageBatchTestSite(t)
	uploadsDir := filepath.Join(webRoot, "wp-content", "uploads", "2026", "08")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}
	pngBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	files := map[string]bool{ // name -> should be picked up
		"photo.jpg":   true,
		"photo.jpeg":  true,
		"icon.png":    true,
		"readme.txt":  false,
		"archive.zip": false,
		"noext":       false,
	}
	contents := map[string][]byte{
		"photo.jpg":  jpegBytes,
		"photo.jpeg": jpegBytes,
		"icon.png":   pngBytes,
	}
	for name := range files {
		data, ok := contents[name]
		if !ok {
			data = []byte("data")
		}
		if err := os.WriteFile(filepath.Join(uploadsDir, name), data, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	candidates, err := scanSiteUploadsForImages(webRoot)
	if err != nil {
		t.Fatalf("scanSiteUploadsForImages: %v", err)
	}
	found := map[string]bool{}
	for _, c := range candidates {
		found[filepath.Base(c.AbsPath)] = true
	}
	for name, want := range files {
		if found[name] != want {
			t.Errorf("file %s: found=%v want=%v", name, found[name], want)
		}
	}
}

func TestScanSiteUploadsForImagesRejectsSymlinkedFile(t *testing.T) {
	_, webRoot := setUpImageBatchTestSite(t)
	uploadsDir := filepath.Join(webRoot, "wp-content", "uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}

	// A sensitive file living outside uploads/, and a symlink inside uploads/
	// pointing at it — this must never be picked up for "optimization".
	secret := filepath.Join(webRoot, "wp-config.php")
	if err := os.WriteFile(secret, []byte("<?php secret\n"), 0644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(uploadsDir, "sneaky.jpg")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	candidates, err := scanSiteUploadsForImages(webRoot)
	if err != nil {
		t.Fatalf("scanSiteUploadsForImages: %v", err)
	}
	for _, c := range candidates {
		if c.AbsPath == secret || filepath.Base(c.AbsPath) == "sneaky.jpg" {
			t.Fatalf("symlinked file must be rejected, got candidate: %+v", c)
		}
	}
	if len(candidates) != 0 {
		t.Fatalf("expected zero candidates, got %d: %+v", len(candidates), candidates)
	}
}

func TestScanSiteUploadsForImagesSkipsSymlinkedDirectory(t *testing.T) {
	_, webRoot := setUpImageBatchTestSite(t)
	uploadsDir := filepath.Join(webRoot, "wp-content", "uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}

	// A directory outside uploads/ containing a real jpg, linked into uploads/
	// as a subdirectory — must not be descended into.
	outsideDir := filepath.Join(webRoot, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "leak.jpg"), []byte("data"), 0644); err != nil {
		t.Fatalf("write leak.jpg: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(uploadsDir, "linked-dir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	candidates, err := scanSiteUploadsForImages(webRoot)
	if err != nil {
		t.Fatalf("scanSiteUploadsForImages: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected zero candidates (symlinked dir must not be descended into), got %d: %+v", len(candidates), candidates)
	}
}

func TestValidateImageBatchFilePathRejectsTraversal(t *testing.T) {
	_, webRoot := setUpImageBatchTestSite(t)
	uploadsDir := filepath.Join(webRoot, "wp-content", "uploads")
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	siteRoot := webRoot

	if _, err := validateImageBatchFilePath(siteRoot, "../../../../etc/passwd"); err == nil {
		t.Fatalf("expected traversal path to be rejected")
	}

	real := filepath.Join(uploadsDir, "photo.jpg")
	if err := os.WriteFile(real, []byte("data"), 0644); err != nil {
		t.Fatalf("write photo.jpg: %v", err)
	}
	if got, err := validateImageBatchFilePath(siteRoot, "photo.jpg"); err != nil || got == "" {
		t.Fatalf("expected a legitimate uploads file to validate, got %q err=%v", got, err)
	}
}
