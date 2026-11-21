//go:build linux

package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"

	"golang.org/x/sys/unix"
)

func maintenanceFixtureIdentity(t *testing.T, path string) maintenanceFileIdentity {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return maintenanceFileIdentity{uint64(st.Dev), st.Ino}
}

func maintenanceFixtureMap(path string, id maintenanceFileIdentity, deleted bool) string {
	line := fmt.Sprintf("1000-2000 r-xp 00000000 %02x:%02x %d %s", unix.Major(id.Device), unix.Minor(id.Device), id.Inode, path)
	if deleted {
		line += " (deleted)"
	}
	return line + "\n"
}

func maintenanceFixtureProcess(t *testing.T) (proc, root, dir string, old maintenanceFileIdentity) {
	t.Helper()
	work := t.TempDir()
	proc, root = filepath.Join(work, "proc"), filepath.Join(work, "root")
	dir = filepath.Join(proc, "123")
	for _, p := range []string{filepath.Join(root, "usr", "lib"), filepath.Join(proc, "1"), dir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(proc, "1", "root"), filepath.Join(dir, "root")} {
		if err := os.Symlink(root, p); err != nil {
			t.Fatal(err)
		}
	}
	calibration := filepath.Join(root, "usr", "lib", "calibration.so")
	target := filepath.Join(root, "usr", "lib", "example.so")
	for _, p := range []string{calibration, target} {
		if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() }) // Pin the old inode during replacement.
	old = maintenanceFixtureIdentity(t, target)
	newPath := target + ".next"
	if err := os.WriteFile(newPath, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newPath, target); err != nil {
		t.Fatal(err)
	}
	statFields := strings.Fields("S 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 456")
	files := map[string]string{
		"stat":   "123 (name with spaces and ) inside) " + strings.Join(statFields, " "),
		"cgroup": "0::/system.slice/app-test.scope\n",
		"maps": maintenanceFixtureMap("/usr/lib/calibration.so", maintenanceFixtureIdentity(t, calibration), false) +
			maintenanceFixtureMap("/usr/lib/example.so", old, true),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestMaintenanceRuntimeScansReplacedFileAndPackageOwnership(t *testing.T) {
	proc, _, _, old := maintenanceFixtureProcess(t)
	got, err := scanMaintenanceRuntimeAt(context.Background(), proc, func(_ context.Context, paths []string) (map[string]string, error) {
		if len(paths) != 1 || paths[0] != "/usr/lib/example.so" {
			t.Fatalf("paths=%v", paths)
		}
		return map[string]string{paths[0]: "example-lib:amd64"}, nil
	})
	if err != nil || len(got.Evidence) != 1 || len(got.UnknownUnits) != 0 {
		t.Fatalf("snapshot=%+v err=%v", got, err)
	}
	e := got.Evidence[0]
	if e.Old != old || e.Old == e.Current || e.Unit != "app-test.scope" || e.Start != 456 || e.PID != 123 || e.Package != "example-lib:amd64" {
		t.Fatalf("evidence=%+v", e)
	}
	got, err = scanMaintenanceRuntimeAt(context.Background(), proc, func(context.Context, []string) (map[string]string, error) {
		return map[string]string{}, nil
	})
	if err != nil || len(got.Evidence) != 0 || !got.UnknownUnits["app-test.scope"] {
		t.Fatalf("unowned replacement accepted: %+v %v", got, err)
	}
}

func TestMaintenanceRuntimeRejectsPrivateViewSymlinkAndMissingPath(t *testing.T) {
	for _, variant := range []string{"private", "symlink", "missing", "uncalibrated"} {
		t.Run(variant, func(t *testing.T) {
			proc, root, dir, _ := maintenanceFixtureProcess(t)
			if variant == "private" {
				private := t.TempDir()
				if err := os.MkdirAll(filepath.Join(private, "usr", "lib"), 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"calibration.so", "example.so"} {
					if err := os.WriteFile(filepath.Join(private, "usr", "lib", name), []byte("private"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				os.Remove(filepath.Join(dir, "root"))
				if err := os.Symlink(private, filepath.Join(dir, "root")); err != nil {
					t.Fatal(err)
				}
			} else if variant == "uncalibrated" {
				b, _ := os.ReadFile(filepath.Join(dir, "maps"))
				if err := os.WriteFile(filepath.Join(dir, "maps"), []byte(strings.SplitN(string(b), "\n", 2)[1]), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(root, "usr", "lib", "example.so")
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if variant == "symlink" {
					if err := os.Symlink("calibration.so", target); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := scanMaintenanceRuntimeAt(context.Background(), proc, func(context.Context, []string) (map[string]string, error) {
				return map[string]string{"/usr/lib/example.so": "example-lib"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if variant == "private" {
				if len(got.Evidence) != 0 || got.Unsupported != 1 {
					t.Fatalf("private file accepted: %+v", got)
				}
			} else if !got.UnknownUnits["app-test.scope"] {
				t.Fatalf("%s file not marked unknown: %+v", variant, got)
			}
		})
	}
}

func TestMaintenanceChrootMissingUnchangedCalibrationDoesNotWarn(t *testing.T) {
	for _, variant := range []string{"unchanged", "changed", "deleted"} {
		t.Run(variant, func(t *testing.T) {
			proc, root, dir, old := maintenanceFixtureProcess(t)
			path := "/usr/lib/calibration.so"
			identity := maintenanceFixtureIdentity(t, filepath.Join(root, "usr/lib/calibration.so"))
			if variant != "unchanged" {
				path, identity = "/usr/lib/example.so", old
			}
			if err := os.WriteFile(filepath.Join(dir, "maps"), []byte(maintenanceFixtureMap(path, identity, variant == "deleted")), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, "root")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "root")); err != nil {
				t.Fatal(err)
			}
			got, err := scanMaintenanceRuntimeAt(context.Background(), proc, func(context.Context, []string) (map[string]string, error) {
				t.Fatal("an unresolved process view cannot produce actionable package evidence")
				return nil, nil
			})
			if err != nil || len(got.Evidence) != 0 {
				t.Fatalf("unexpected candidate: %+v %v", got, err)
			}
			wantUnknown := variant != "unchanged"
			if got.UnknownUnits["app-test.scope"] != wantUnknown {
				t.Fatalf("%s: %+v", variant, got)
			}
		})
	}
}

func TestMaintenanceProcessRaceDiscardsPartialEvidence(t *testing.T) {
	proc, _, dir, _ := maintenanceFixtureProcess(t)
	host, err := unix.Open(filepath.Join(proc, "1", "root"), unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(host)
	for _, variant := range []string{"reused", "exited", "esrch"} {
		reads := 0
		read := func(string) (uint64, error) {
			reads++
			if reads == 1 {
				return 456, nil
			}
			if variant == "exited" {
				return 0, os.ErrNotExist
			}
			if variant == "esrch" {
				return 0, unix.ESRCH
			}
			return 789, nil
		}
		got, err := scanMaintenanceProcess(context.Background(), dir, 123, host, read)
		if !errors.Is(err, errMaintenanceProcessChanged) || len(got.evidence) != 0 {
			t.Fatalf("%s retained race evidence: %+v %v", variant, got, err)
		}
	}
}

func TestMaintenanceUserManagerBoundaryCannotClaimSystemService(t *testing.T) {
	enableMaintenanceForTest(t)
	for _, path := range []string{
		"0::/user.slice/user-0.slice/user@0.service/app.slice/dbus.service\n",
		"0::/user.slice/user-0.slice/user@0.service/app.slice/gpg-agent.service\n",
		"0::/user.slice/user-0.slice/user@0.service/app.slice/app-test.scope\n",
	} {
		proc, _, dir, _ := maintenanceFixtureProcess(t)
		if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := scanMaintenanceRuntimeAt(context.Background(), proc, func(context.Context, []string) (map[string]string, error) {
			return map[string]string{"/usr/lib/example.so": "example-lib"}, nil
		})
		if err != nil || len(got.Evidence) != 1 || got.Evidence[0].Unit != "user@0.service" {
			t.Fatalf("user manager lost: %+v %v", got, err)
		}
		if _, ok, err := claimMaintenanceAction(database.GetDB(), "service", "dbus.service", got.Evidence, "boot", time.Now()); err == nil || ok {
			t.Fatal("user evidence authorized system dbus")
		}
		if _, ok, err := claimMaintenanceAction(database.GetDB(), "service", "user@0.service", got.Evidence, "boot", time.Now()); err == nil || ok {
			t.Fatal("user manager authorized as managed system service")
		}
	}
	if !maintenanceProcessGone(unix.ESRCH) || !maintenanceProcessGone(fmt.Errorf("wrapped: %w", unix.ESRCH)) || maintenanceProcessGone(unix.EACCES) {
		t.Fatal("incorrect process exit classification")
	}
}

func TestMaintenanceMappingParserAndReadLimits(t *testing.T) {
	for _, line := range []string{
		"1000-2000 rw-p 0 00:01 2 /dev/shm/test (deleted)",
		"1000-2000 r-xp 0 00:01 2 /usr/local/bin/custom (deleted)",
		"1000-2000 r-xp 0 00:00 0 [vdso]",
	} {
		if _, use, err := parseMaintenanceMapping(line); use || err != nil {
			t.Fatalf("out-of-scope mapping not ignored: %q", line)
		}
	}
	for _, path := range []string{"/usr/lib/../lib/a.so", "/usr/lib/a*.so", "/usr/lib/a\\040b.so"} {
		line := "1000-2000 r-xp 0 08:01 2 " + path + " (deleted)"
		if _, _, err := parseMaintenanceMapping(line); err == nil {
			t.Fatalf("ambiguous mapping accepted: %q", path)
		}
	}
	path := filepath.Join(t.TempDir(), "oversize")
	if err := os.WriteFile(path, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readMaintenanceProcFile(path, 4); err == nil {
		t.Fatal("unbounded metadata accepted")
	}
	line := "1000-2000 r-xp 0 08:ff 42 /usr/lib/a.so (deleted)"
	e, use, err := parseMaintenanceMapping(line)
	if err != nil || !use || e.old.Device != unix.Mkdev(8, 255) || e.old.Inode != 42 {
		t.Fatalf("hexadecimal device parsed incorrectly: %+v %v", e, err)
	}
}

func TestMaintenanceOwnershipPartialResultAndInvalidInput(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' 'example-lib:amd64: /usr/lib/example.so'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "dpkg-query"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := maintenancePackageOwners(context.Background(), []string{"/usr/lib/example.so", "/usr/lib/missing.so"})
	if err != nil || got["/usr/lib/example.so"] != "example-lib:amd64" || got["/usr/lib/missing.so"] != "" {
		t.Fatalf("partial ownership: %#v %v", got, err)
	}
	if _, err := maintenancePackageOwners(context.Background(), []string{"/usr/lib/a*.so"}); err == nil {
		t.Fatal("dpkg glob input accepted")
	}
	var output maintenanceOwnershipOutput
	if _, err := output.Write([]byte(strings.Repeat("x", (1<<20)+1))); err == nil || !output.overflow {
		t.Fatal("unbounded ownership output accepted")
	}
}

func TestMaintenanceProcessStartParsesEmbeddedParenthesis(t *testing.T) {
	_, _, dir, _ := maintenanceFixtureProcess(t)
	got, err := maintenanceProcessStart(dir)
	if err != nil || got != 456 {
		t.Fatalf("start=%s err=%v", strconv.FormatUint(got, 10), err)
	}
}

func TestMaintenanceRuntimeGlobalCalibrationAndReadFailures(t *testing.T) {
	for _, variant := range []string{"global_calibration", "root_missing", "cgroup_unreadable", "normal_identity_changed", "device_changed", "ownership_partial", "cancelled"} {
		t.Run(variant, func(t *testing.T) {
			proc, root, dir, old := maintenanceFixtureProcess(t)
			ctx := context.Background()
			owners := func(context.Context, []string) (map[string]string, error) {
				return map[string]string{"/usr/lib/example.so": "example-lib"}, nil
			}
			switch variant {
			case "global_calibration":
				// The candidate process has no surviving /usr mapping. Another
				// stable process on the same device supplies the calibration.
				b, err := os.ReadFile(filepath.Join(dir, "maps"))
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.SplitN(string(b), "\n", 2)
				other := filepath.Join(proc, "124")
				if err := os.Mkdir(other, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"stat", "cgroup"} {
					b, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(other, name), b, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(root, filepath.Join(other, "root")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(other, "maps"), []byte(lines[0]+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "maps"), []byte(lines[1]), 0600); err != nil {
					t.Fatal(err)
				}
			case "root_missing":
				if err := os.Remove(filepath.Join(dir, "root")); err != nil {
					t.Fatal(err)
				}
			case "cgroup_unreadable":
				// Reading a directory fails even when tests run as root.
				if err := os.Remove(filepath.Join(dir, "cgroup")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(dir, "cgroup"), 0700); err != nil {
					t.Fatal(err)
				}
			case "normal_identity_changed":
				if err := os.WriteFile(filepath.Join(dir, "maps"), []byte(maintenanceFixtureMap("/usr/lib/example.so", old, false)), 0600); err != nil {
					t.Fatal(err)
				}
			case "device_changed":
				old.Device++
				if err := os.WriteFile(filepath.Join(dir, "maps"), []byte(maintenanceFixtureMap("/usr/lib/example.so", old, true)), 0600); err != nil {
					t.Fatal(err)
				}
			case "ownership_partial":
				owners = func(context.Context, []string) (map[string]string, error) { return nil, nil }
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := scanMaintenanceRuntimeAt(ctx, proc, owners)
			if variant == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if variant == "global_calibration" {
				if len(got.Evidence) != 1 || len(got.UnknownUnits) != 0 {
					t.Fatalf("global calibration failed: %+v", got)
				}
				return
			}
			unit := "app-test.scope"
			if variant == "cgroup_unreadable" {
				unit = "*"
			}
			if !got.UnknownUnits[unit] || got.UnknownReasons[unit] == "" || len(got.Evidence) != 0 {
				t.Fatalf("failure not classified: %+v", got)
			}
		})
	}
}

func TestMaintenanceOwnershipRejectsAmbiguousOrFailedQueries(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		exit         int
		wantErr      bool
		wantOwner    string
	}{
		{"two_owners", "one: /usr/lib/example.so\ntwo: /usr/lib/example.so\n", 0, true, ""},
		{"comma_owners", "one, two: /usr/lib/example.so\n", 0, false, ""},
		{"diversion", "diversion by example from: /usr/lib/example.so\ndiversion by example to: /usr/lib/example.so.distrib\n", 0, false, ""},
		{"query_failure", "example: /usr/lib/example.so\n", 2, true, ""},
		{"multiarch", "example:amd64: /usr/lib/example.so\n", 0, false, "example:amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Fixed fixture output, not user input or a command line value.
			script := "#!/bin/sh\nprintf '%s' '" + tc.output + "'\nexit " + strconv.Itoa(tc.exit) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "dpkg-query"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			got, err := maintenancePackageOwners(context.Background(), []string{"/usr/lib/example.so"})
			if (err != nil) != tc.wantErr || (!tc.wantErr && got["/usr/lib/example.so"] != tc.wantOwner) {
				t.Fatalf("owners=%v err=%v", got, err)
			}
		})
	}
}

func TestMaintenanceRuntimeManyWorkersKeepBoundedStableEvidence(t *testing.T) {
	proc, root, dir, old := maintenanceFixtureProcess(t)
	calibration := maintenanceFixtureMap("/usr/lib/calibration.so", maintenanceFixtureIdentity(t, filepath.Join(root, "usr/lib/calibration.so")), false)
	// 300 workers * 40 replaced identities = 12,000 raw mappings. They
	// represent only 40 files in one service, not 12,000 action targets.
	var maps strings.Builder
	maps.WriteString(calibration)
	for file := 0; file < 40; file++ {
		path := fmt.Sprintf("/usr/lib/replaced-%d.so", file)
		if err := os.WriteFile(filepath.Join(root, path), []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
		identity := old
		identity.Inode += uint64(file + 1000000)
		maps.WriteString(maintenanceFixtureMap(path, identity, true))
	}
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	for pid := 123; pid < 423; pid++ {
		worker := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.MkdirAll(worker, 0700); err != nil {
			t.Fatal(err)
		}
		if pid != 123 {
			if err := os.Symlink(root, filepath.Join(worker, "root")); err != nil {
				t.Fatal(err)
			}
		}
		for name, value := range map[string]string{"stat": string(stat), "maps": maps.String(), "cgroup": "0::/system.slice/php8.3-fpm.service\n"} {
			if err := os.WriteFile(filepath.Join(worker, name), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	owners := func(_ context.Context, paths []string) (map[string]string, error) {
		if len(paths) != 40 {
			t.Fatalf("ownership query not deduplicated: %d", len(paths))
		}
		result := map[string]string{}
		for _, path := range paths {
			result[path] = "example-lib"
		}
		return result, nil
	}
	first, err := scanMaintenanceRuntimeAt(context.Background(), proc, owners)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scanMaintenanceRuntimeAt(context.Background(), proc, owners)
	if err != nil {
		t.Fatal(err)
	}
	second.At = first.At.Add(systemMaintenanceObservationInterval)
	if want := 40 * maintenanceMaxRepresentatives; len(first.Evidence) != want || len(persistentMaintenanceEvidence(first, second)) != want || len(first.UnknownUnits) != 0 {
		t.Fatalf("large worker pool lost evidence: first=%d stable=%d unknown=%v", len(first.Evidence), len(persistentMaintenanceEvidence(first, second)), first.UnknownUnits)
	}
}
