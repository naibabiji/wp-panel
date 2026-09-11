package handlers

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestMaintenanceMoveRechecksSourceBeforeDeletion(t *testing.T) {
	for _, phase := range []string{"unlocked", "expired", "relocking", "locked"} {
		t.Run(phase, func(t *testing.T) {
			setupCacheHelperTestDB(t)
			root, destRoot := t.TempDir(), t.TempDir()
			source := filepath.Join(root, "wp-content", "plugins", "moved")
			dest := filepath.Join(destRoot, "moved")
			if err := os.MkdirAll(source, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "plugin.php"), []byte("<?php // fixture"), 0644); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			if _, err := db.Exec(`UPDATE websites SET web_root=?,file_lock_enabled=0,file_lock_mode='strict',maintenance_security=json_object('window',json_object('id','move-window','state','unlocked','expires',?)) WHERE id=1`, root, time.Now().Unix()+600); err != nil {
				t.Fatal(err)
			}
			items := []fileTransferItem{{name: "moved", src: source, dest: dest}}
			// The source passes the real move precheck and its data is copied.
			// Site 0 keeps the destination writable independently of source state.
			if err := checkTransferFileLock(1, 0, items, true); err != nil {
				t.Fatalf("move precheck: %v", err)
			}
			if err := copyFileOrDirWithOverwrite(root, destRoot, source, dest, false, fileCopyWriteGuard(0)); err != nil {
				t.Fatal(err)
			}
			// Deterministically model expiry/relock between copying and deletion.
			var err error
			switch phase {
			case "expired":
				_, err = db.Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window.expires',0) WHERE id=1`)
			case "relocking":
				_, err = db.Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window.state','relocking') WHERE id=1`)
			case "locked":
				_, err = db.Exec(`UPDATE websites SET maintenance_security='{}',file_lock_enabled=1,file_lock_apply_status='ready' WHERE id=1`)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = removeTransferredSource(1, source)
			if phase == "unlocked" {
				if err != nil {
					t.Fatalf("valid window: %v", err)
				}
				if _, err := os.Stat(source); !os.IsNotExist(err) {
					t.Fatalf("source was not removed: %v", err)
				}
			} else {
				if !isFileLockWriteError(err) {
					t.Fatalf("source deletion allowed after %s: %v", phase, err)
				}
				if data, err := os.ReadFile(filepath.Join(source, "plugin.php")); err != nil || string(data) != "<?php // fixture" {
					t.Fatalf("source not preserved: %q %v", data, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(dest, "plugin.php")); err != nil || string(data) != "<?php // fixture" {
				t.Fatalf("destination copy not preserved: %q %v", data, err)
			}
		})
	}
}

func TestMaintenanceSameSiteRenameRechecksBeforeMutation(t *testing.T) {
	for _, phase := range []string{"unlocked", "expired", "relocking", "locked"} {
		t.Run(phase, func(t *testing.T) {
			setupCacheHelperTestDB(t)
			root := t.TempDir()
			source := filepath.Join(root, "wp-content", "plugins", "source.php")
			dest := filepath.Join(root, "wp-content", "plugins", "dest.php")
			if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("<?php // fixture"), 0644); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			if _, err := db.Exec(`UPDATE websites SET web_root=?,file_lock_enabled=0,file_lock_mode='strict',maintenance_security=json_object('window',json_object('id','rename-window','state','unlocked','expires',?)) WHERE id=1`, root, time.Now().Unix()+600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch phase {
			case "expired":
				_, err = db.Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window.expires',0) WHERE id=1`)
			case "relocking":
				_, err = db.Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window.state','relocking') WHERE id=1`)
			case "locked":
				_, err = db.Exec(`UPDATE websites SET maintenance_security='{}',file_lock_enabled=1,file_lock_apply_status='ready' WHERE id=1`)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = renameTransferredPath(1, source, dest)
			if phase == "unlocked" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(dest); err != nil {
					t.Fatal("destination missing after valid rename")
				}
				return
			}
			if !isFileLockWriteError(err) {
				t.Fatalf("rename allowed after %s: %v", phase, err)
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatal("source changed after rejected rename")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("destination created after rejected rename")
			}
		})
	}
}

func TestMaintenanceCopyChecksDuringActualTraversal(t *testing.T) {
	for _, phase := range []string{"expired", "relocking", "locked"} {
		t.Run(phase, func(t *testing.T) {
			setupCacheHelperTestDB(t)
			root, source := t.TempDir(), t.TempDir()
			dest := filepath.Join(root, "wp-content", "plugins", "copied")
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a.php", "b.php"} {
				if err := os.WriteFile(filepath.Join(source, name), []byte("<?php"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			db := database.GetDB()
			_, err := db.Exec(`UPDATE websites SET web_root=?,file_lock_enabled=0,file_lock_mode='strict',maintenance_security=json_object('window',json_object('id','test-window','state','unlocked','expires',?)) WHERE id=1`, root, time.Now().Unix()+600)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkFileLockCopyDestination(fileLockSite(1), source, dest); err != nil {
				t.Fatalf("precheck: %v", err)
			}
			guard := func(path string, dir bool) error {
				if filepath.Base(path) == "b.php" {
					if _, err := os.Stat(filepath.Join(dest, "a.php")); err != nil {
						t.Fatal("first file not copied")
					}
					switch phase {
					case "expired":
						_, err = db.Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window.expires',0)`)
					case "relocking":
						_, err = db.Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window.state','relocking')`)
					case "locked":
						_, err = db.Exec(`UPDATE websites SET maintenance_security='{}',file_lock_enabled=1,file_lock_apply_status='ready'`)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				return fileCopyWriteGuard(1)(path, dir)
			}
			if err := copyFileOrDirWithOverwrite(source, root, source, dest, false, guard); !isFileLockWriteError(err) {
				t.Fatalf("copy allowed after %s: %v", phase, err)
			}
			if _, err := os.Stat(filepath.Join(dest, "b.php")); !os.IsNotExist(err) {
				t.Fatal("opened a new file after relock")
			}
		})
	}
}
