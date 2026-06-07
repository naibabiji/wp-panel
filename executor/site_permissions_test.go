package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsPathWithinRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "wp-content")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}

	if !isPathWithinRoot(root, inside) {
		t.Fatal("inside path should be allowed")
	}
	if isPathWithinRoot(root, outside) {
		t.Fatal("outside path should be rejected")
	}
}

func TestChownSitePathRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "wp-content")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		path       string
		root       string
		systemUser string
	}{
		{name: "empty path", path: "", root: root, systemUser: "wp_site"},
		{name: "root path", path: string(filepath.Separator), root: root, systemUser: "wp_site"},
		{name: "empty allowed root", path: inside, root: "", systemUser: "wp_site"},
		{name: "unsafe allowed root", path: inside, root: string(filepath.Separator), systemUser: "wp_site"},
		{name: "outside allowed root", path: outside, root: root, systemUser: "wp_site"},
		{name: "empty system user", path: inside, root: root, systemUser: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ChownSitePath(tt.path, tt.root, tt.systemUser); err == nil {
				t.Fatal("ChownSitePath error = nil, want rejection")
			}
		})
	}
}
