package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveLegacyWPCommandAt_RemovesOldPanelScript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp")
	if err := os.WriteFile(path, []byte(wpScript), 0755); err != nil {
		t.Fatalf("failed to seed legacy script: %v", err)
	}
	// wpScript already carries the current "wpp" marker; simulate the
	// pre-rename script that would actually be sitting on disk.
	legacyContent := "#!/bin/bash\n" + legacyWPCommandMarker + "\n\necho hi\n"
	if err := os.WriteFile(path, []byte(legacyContent), 0755); err != nil {
		t.Fatalf("failed to seed legacy script: %v", err)
	}

	removeLegacyWPCommandAt(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected legacy panel script to be removed, stat err = %v", err)
	}
}

func TestRemoveLegacyWPCommandAt_KeepsUnrelatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp")
	content := "#!/usr/bin/env php\n<?php // WP-CLI\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("failed to seed file: %v", err)
	}

	removeLegacyWPCommandAt(path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected unrelated WP-CLI-like file to survive, stat err = %v", err)
	}
}

func TestRemoveLegacyWPCommandAt_KeepsSubstringMatchOutsideLineBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp")
	// The marker appears, but not as an exact, standalone line — should not match.
	content := "#!/bin/bash\necho '" + legacyWPCommandMarker + " extra text'\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("failed to seed file: %v", err)
	}

	removeLegacyWPCommandAt(path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected non-exact-line match to survive, stat err = %v", err)
	}
}

func TestRemoveLegacyWPCommandAt_MissingFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist")
	removeLegacyWPCommandAt(path) // must not panic
}

func TestWriteFileAtomic_WritesContentAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wpp")
	content := []byte("#!/bin/bash\necho hi\n")

	if err := writeFileAtomic(path, content, 0755); err != nil {
		t.Fatalf("writeFileAtomic failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: got %q, want %q", got, content)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat written file: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("unexpected permissions: got %v, want 0755", info.Mode().Perm())
	}

	// No leftover temp files in the target directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "wpp" {
		t.Fatalf("unexpected directory contents: %v", entries)
	}
}

func TestWriteFileAtomic_OverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wpp")
	if err := os.WriteFile(path, []byte("old content"), 0644); err != nil {
		t.Fatalf("failed to seed existing file: %v", err)
	}

	newContent := []byte("new content")
	if err := writeFileAtomic(path, newContent, 0755); err != nil {
		t.Fatalf("writeFileAtomic failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("content mismatch: got %q, want %q", got, newContent)
	}
}
