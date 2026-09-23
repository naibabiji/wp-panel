package executor

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func newSecureSiteTestRoot(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "site")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidateSecureSiteParentModes(t *testing.T) {
	tests := []struct {
		name    string
		mode    uint32
		gid     uint32
		wantErr bool
	}{
		{name: "root group writable", mode: uint32(unix.S_IFDIR | 0775), gid: 0},
		{name: "non-root group writable", mode: uint32(unix.S_IFDIR | 0770), gid: 1, wantErr: true},
		{name: "other writable", mode: uint32(unix.S_IFDIR | 0757), gid: 0, wantErr: true},
		{name: "sticky other writable", mode: uint32(unix.S_IFDIR | unix.S_ISVTX | 0777), gid: 0, wantErr: true},
		{name: "not a directory", mode: uint32(unix.S_IFREG | 0644), gid: 0, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stat := unix.Stat_t{Mode: tt.mode, Uid: uint32(os.Geteuid()), Gid: tt.gid}
			err := validateSecureSiteParent(&stat)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSecureSiteParent() error = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestSecureSiteFileAllowsRootGroupWritableParent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to construct a root:root 0775 parent")
	}
	root := newSecureSiteTestRoot(t)
	parent := filepath.Dir(root)
	if err := os.Chown(parent, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := readWPConfigSecure(root); err != nil || string(data) != "ok" {
		t.Fatalf("root:root 0775 parent read = %q, %v", data, err)
	}
}

func TestSecureSiteFileRejectsSymlinkHardlinkAndNonRegular(t *testing.T) {
	t.Run("root-owned ancestor symlink", func(t *testing.T) {
		realParent := t.TempDir()
		realRoot := filepath.Join(realParent, "site")
		if err := os.Mkdir(realRoot, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(realRoot, "wp-config.php"), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
		linkContainer := t.TempDir()
		linkedParent := filepath.Join(linkContainer, "trusted-parent")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatal(err)
		}
		linkedRoot := filepath.Join(linkedParent, "site")
		if data, err := readWPConfigSecure(linkedRoot); err != nil || string(data) != "before" {
			t.Fatalf("trusted ancestor symlink read = %q, %v", data, err)
		}
		if err := writeWPConfigSecure(linkedRoot, []byte("after")); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(realRoot, "wp-config.php")); string(got) != "after" {
			t.Fatalf("target content = %q", got)
		}
	})

	t.Run("writable parent", func(t *testing.T) {
		root := newSecureSiteTestRoot(t)
		if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(root), 0770); err != nil {
			t.Fatal(err)
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(filepath.Dir(root), 0, 1); err != nil {
				t.Fatal(err)
			}
		} else if os.Getegid() == 0 {
			t.Skip("cannot construct a non-root group-owned parent")
		}
		if _, err := readWPConfigSecure(root); err == nil {
			t.Fatal("read accepted non-root-group-writable parent")
		}
	})

	t.Run("sticky other-writable parent", func(t *testing.T) {
		root := newSecureSiteTestRoot(t)
		if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(root), os.ModeSticky|0777); err != nil {
			t.Fatal(err)
		}
		if _, err := readWPConfigSecure(root); err == nil {
			t.Fatal("read accepted sticky other-writable parent")
		}
	})

	t.Run("webroot symlink", func(t *testing.T) {
		parent := t.TempDir()
		realRoot := t.TempDir()
		if err := os.WriteFile(filepath.Join(realRoot, "wp-config.php"), []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		linkedRoot := filepath.Join(parent, "site")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := readWPConfigSecure(linkedRoot); err == nil {
			t.Fatal("read accepted WebRoot symlink")
		}
		if err := writeWPConfigSecure(linkedRoot, []byte("changed")); err == nil {
			t.Fatal("write accepted WebRoot symlink")
		}
		if got, _ := os.ReadFile(filepath.Join(realRoot, "wp-config.php")); string(got) != "outside" {
			t.Fatalf("WebRoot symlink target changed: %q", got)
		}
	})

	t.Run("final symlink", func(t *testing.T) {
		root := newSecureSiteTestRoot(t)
		outside := filepath.Join(t.TempDir(), "outside")
		original := []byte("outside-original")
		if err := os.WriteFile(outside, original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "wp-config.php")); err != nil {
			t.Fatal(err)
		}
		if _, err := readWPConfigSecure(root); err == nil {
			t.Fatal("read accepted final symlink")
		}
		if err := writeWPConfigSecure(root, []byte("changed")); err == nil {
			t.Fatal("write accepted final symlink")
		}
		got, err := os.ReadFile(outside)
		if err != nil || string(got) != string(original) {
			t.Fatalf("outside target changed: %q %v", got, err)
		}
	})

	t.Run("intermediate symlink", func(t *testing.T) {
		root := newSecureSiteTestRoot(t)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "config.php"), []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readSecureSiteFile(root, filepath.Join("linked", "config.php")); err == nil {
			t.Fatal("read accepted intermediate symlink")
		}
		if err := writeSecureSiteFile(root, filepath.Join("linked", "config.php"), []byte("changed"), 0600); err == nil {
			t.Fatal("write accepted intermediate symlink")
		}
		got, _ := os.ReadFile(filepath.Join(outside, "config.php"))
		if string(got) != "outside" {
			t.Fatalf("outside target changed: %q", got)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		root := newSecureSiteTestRoot(t)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(outside, filepath.Join(root, "wp-config.php")); err != nil {
			t.Fatal(err)
		}
		if _, err := readWPConfigSecure(root); err == nil {
			t.Fatal("read accepted hardlink")
		}
		if err := writeWPConfigSecure(root, []byte("changed")); err == nil {
			t.Fatal("write accepted hardlink")
		}
		got, _ := os.ReadFile(outside)
		if string(got) != "outside" {
			t.Fatalf("outside target changed: %q", got)
		}
	})

	t.Run("non regular", func(t *testing.T) {
		root := newSecureSiteTestRoot(t)
		path := filepath.Join(root, "wp-config.php")
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readWPConfigSecure(root); err == nil {
			t.Fatal("read accepted FIFO")
		}
		if err := writeWPConfigSecure(root, []byte("changed")); err == nil {
			t.Fatal("write accepted FIFO")
		}
	})
}

func TestSecureSiteFileAtomicWritePreservesMetadata(t *testing.T) {
	root := newSecureSiteTestRoot(t)
	path := filepath.Join(root, "wp-config.php")
	if err := os.WriteFile(path, []byte("before"), 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat := before.Sys().(*syscall.Stat_t)
	if err := writeWPConfigSecure(root, []byte("after")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterStat := after.Sys().(*syscall.Stat_t)
	if after.Mode().Perm() != before.Mode().Perm() || afterStat.Uid != beforeStat.Uid || afterStat.Gid != beforeStat.Gid {
		t.Fatalf("metadata changed: before=%v %d:%d after=%v %d:%d", before.Mode().Perm(), beforeStat.Uid, beforeStat.Gid, after.Mode().Perm(), afterStat.Uid, afterStat.Gid)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after" {
		t.Fatalf("content = %q, err=%v", got, err)
	}
}

func TestSecureSiteFileRejectsWrongOwnerWhenWebRootHasSiteOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to construct mismatched ownership")
	}
	root := newSecureSiteTestRoot(t)
	path := filepath.Join(root, "wp-config.php")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := readWPConfigSecure(root); err == nil {
		t.Fatal("read accepted mismatched owner")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "before" {
		t.Fatalf("target changed: %q %v", got, err)
	}
}

func TestSecureSiteFileMissingIsReported(t *testing.T) {
	_, err := readWPConfigSecure(newSecureSiteTestRoot(t))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
}
