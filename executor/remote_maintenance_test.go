package executor

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func maintenanceFileRow(id int, filename, mode string) remoteMaintenanceRow {
	return remoteMaintenanceRow{
		id: id, siteID: 1, domain: "example.com", filename: filename,
		subdir: "files", mode: mode, source: BackupSourceFile,
		createdAt: time.Unix(int64(id), 0),
	}
}

func TestAssessRemoteFileBackupChain(t *testing.T) {
	rows := []remoteMaintenanceRow{
		maintenanceFileRow(1, "old-full.tar.gz", "full"),
		maintenanceFileRow(2, "old-inc.tar.gz", "incremental"),
		maintenanceFileRow(3, "new-full.tar.gz", "full"),
		maintenanceFileRow(4, "new-inc.tar.gz", "incremental"),
	}
	tests := []struct {
		name       string
		existing   map[string]bool
		latestFull int
		rebuild    bool
		oldFiles   []string
	}{
		{name: "missing baseline", existing: map[string]bool{}, latestFull: -1, rebuild: true},
		{name: "complete latest chain", existing: map[string]bool{"new-full.tar.gz": true, "new-inc.tar.gz": true}, latestFull: 2, oldFiles: []string{"old-full.tar.gz", "old-inc.tar.gz"}},
		{name: "missing latest incremental", existing: map[string]bool{"new-full.tar.gz": true}, latestFull: 2, rebuild: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			latest, rebuild, _, oldFiles := assessRemoteFileBackupChain(rows, func(row remoteMaintenanceRow) bool {
				return tt.existing[row.filename]
			})
			if latest != tt.latestFull || rebuild != tt.rebuild || !reflect.DeepEqual(oldFiles, tt.oldFiles) {
				t.Fatalf("assess = (%d,%v,%v), want (%d,%v,%v)", latest, rebuild, oldFiles, tt.latestFull, tt.rebuild, tt.oldFiles)
			}
		})
	}
}

func TestCleanupSupersededFileBackupChainRequiresRemoteDeleteFirst(t *testing.T) {
	dir := t.TempDir()
	filename := "file_full_20260101_000000.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("backup"), 0600); err != nil {
		t.Fatal(err)
	}
	localCalled, recordCalled := false, false
	cleaned := cleanupSupersededFileBackupChainWith(1, "example.com", dir, []string{filename},
		func(string) error { return errors.New("remote unavailable") },
		func(string) error { localCalled = true; return nil },
		func(int, string) error { recordCalled = true; return nil })
	if cleaned != 0 || localCalled || recordCalled {
		t.Fatalf("cleanup after remote failure = %d, local=%v record=%v", cleaned, localCalled, recordCalled)
	}
}

func TestCleanupSupersededFileBackupChainDeletesInOrder(t *testing.T) {
	var calls []string
	cleaned := cleanupSupersededFileBackupChainWith(7, "example.com", "/backup/example.com/files", []string{"old.tar.gz"},
		func(rel string) error { calls = append(calls, "remote:"+rel); return nil },
		func(local string) error { calls = append(calls, "local:"+local); return nil },
		func(siteID int, filename string) error { calls = append(calls, "record"); return nil })
	want := []string{"remote:example.com/files/old.tar.gz", "local:/backup/example.com/files/old.tar.gz", "record"}
	if cleaned != 1 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("cleanup = %d, calls=%v, want 1,%v", cleaned, calls, want)
	}
}

func TestMaintainOldChainCleanupIsGatedToLowPeakMaintenance(t *testing.T) {
	called := false
	status, _, cleaned := maintainOldChainCleanup(false, []string{"old-full.tar.gz"}, func([]string) int {
		called = true
		return 1
	})
	if called || cleaned != 0 || status != "cleanup_pending" {
		t.Fatalf("manual maintenance cleanup: called=%v cleaned=%d status=%q", called, cleaned, status)
	}

	status, _, cleaned = maintainOldChainCleanup(true, []string{"old-full.tar.gz", "old-inc.tar.gz"}, func([]string) int {
		called = true
		return 1
	})
	if !called || cleaned != 1 || status != "cleanup_pending" {
		t.Fatalf("low-peak partial cleanup: called=%v cleaned=%d status=%q", called, cleaned, status)
	}
}

func testRemoteMaintenanceDeps(rows []remoteMaintenanceRow, remoteKeys map[string]bool) remoteMaintenanceDeps {
	return remoteMaintenanceDeps{
		enabled:  func() bool { return true },
		loadRows: func() ([]remoteMaintenanceRow, error) { return rows, nil },
		loadKeys: func() (string, string, map[string]bool, error) { return "rsync", "", remoteKeys, nil },
		localRegular: func(remoteMaintenanceRow) (string, bool) {
			return "", false
		},
		sync:         func(remoteMaintenanceRow, string) bool { return false },
		updateStatus: func(remoteMaintenanceRow, string, string) {},
		cleanup:      func(int, string, []string) int { return 0 },
		setState:     func(int, string, string) {},
		rebuild:      func(int) error { return nil },
	}
}

func TestMaintainRemoteBackupsWithRepairsBeforeAssessingChain(t *testing.T) {
	full := maintenanceFileRow(1, "full.tar.gz", "full")
	full.status = "local"
	deps := testRemoteMaintenanceDeps([]remoteMaintenanceRow{full}, map[string]bool{})
	synced, state := false, ""
	deps.localRegular = func(remoteMaintenanceRow) (string, bool) { return "/local/full.tar.gz", true }
	deps.sync = func(row remoteMaintenanceRow, path string) bool {
		synced = row.filename == "full.tar.gz" && path == "/local/full.tar.gz"
		return true
	}
	deps.setState = func(_ int, status, _ string) { state = status }
	deps.rebuild = func(int) error { t.Fatal("rebuild must not run after successful repair"); return nil }

	changed, err := maintainRemoteBackupsWith(false, deps)
	if err != nil || changed != 1 || !synced || state != "healthy" {
		t.Fatalf("maintenance = changed=%d synced=%v state=%q err=%v", changed, synced, state, err)
	}
}

func TestMaintainRemoteBackupsWithRebuildsOnlyWhenAllowed(t *testing.T) {
	full := maintenanceFileRow(1, "missing-full.tar.gz", "full")
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "low_peak"}[allow], func(t *testing.T) {
			deps := testRemoteMaintenanceDeps([]remoteMaintenanceRow{full}, map[string]bool{})
			rebuilt := false
			var states []string
			deps.rebuild = func(siteID int) error { rebuilt = true; return nil }
			deps.setState = func(_ int, status, _ string) { states = append(states, status) }
			changed, err := maintainRemoteBackupsWith(allow, deps)
			if err != nil {
				t.Fatal(err)
			}
			if rebuilt != allow {
				t.Fatalf("rebuilt=%v, want %v", rebuilt, allow)
			}
			wantChanged := 0
			if allow {
				wantChanged = 1
			}
			if changed != wantChanged || len(states) == 0 || states[0] != "rebuild_required" {
				t.Fatalf("changed=%d states=%v, want changed=%d first=rebuild_required", changed, states, wantChanged)
			}
			if allow && states[len(states)-1] != "healthy" {
				t.Fatalf("final state=%q, want healthy", states[len(states)-1])
			}
		})
	}
}

func TestMaintainRemoteBackupsWithFailedRepairDoesNotRebuild(t *testing.T) {
	full := maintenanceFileRow(1, "full.tar.gz", "full")
	full.status = "failed"
	deps := testRemoteMaintenanceDeps([]remoteMaintenanceRow{full}, map[string]bool{})
	deps.localRegular = func(remoteMaintenanceRow) (string, bool) { return "/local/full.tar.gz", true }
	deps.sync = func(remoteMaintenanceRow, string) bool { return false }
	state, message := "", ""
	deps.setState = func(_ int, status, detail string) { state, message = status, detail }
	deps.rebuild = func(int) error { t.Fatal("repairable local backup must not trigger a full rebuild"); return nil }

	changed, err := maintainRemoteBackupsWith(true, deps)
	if err != nil || changed != 0 || state != "repair_pending" || !strings.Contains(message, "本地副本仍在") {
		t.Fatalf("maintenance = changed=%d state=%q message=%q err=%v", changed, state, message, err)
	}
}

func TestMaintainRemoteBackupsWithPartialLocalChainStillRebuilds(t *testing.T) {
	full := maintenanceFileRow(1, "full.tar.gz", "full")
	inc := maintenanceFileRow(2, "inc.tar.gz", "incremental")
	deps := testRemoteMaintenanceDeps([]remoteMaintenanceRow{full, inc}, map[string]bool{})
	deps.localRegular = func(row remoteMaintenanceRow) (string, bool) {
		return "/local/" + row.filename, row.filename == "full.tar.gz"
	}
	deps.sync = func(remoteMaintenanceRow, string) bool { return false }
	rebuilt := false
	deps.rebuild = func(int) error { rebuilt = true; return nil }

	if _, err := maintainRemoteBackupsWith(true, deps); err != nil {
		t.Fatal(err)
	}
	if !rebuilt {
		t.Fatal("chain with a locally missing incremental backup must rebuild")
	}
}

func TestMaintainRemoteBackupsWithPersistsRebuildFailure(t *testing.T) {
	full := maintenanceFileRow(1, "missing-full.tar.gz", "full")
	deps := testRemoteMaintenanceDeps([]remoteMaintenanceRow{full}, map[string]bool{})
	var states, messages []string
	deps.rebuild = func(int) error { return errors.New("disk full") }
	deps.setState = func(_ int, status, message string) {
		states = append(states, status)
		messages = append(messages, message)
	}
	changed, err := maintainRemoteBackupsWith(true, deps)
	if err != nil || changed != 0 || len(states) != 2 || states[1] != "rebuild_required" || !strings.Contains(messages[1], "disk full") {
		t.Fatalf("maintenance failure = changed=%d states=%v messages=%v err=%v", changed, states, messages, err)
	}
}

func TestMaintainRemoteBackupsWithDoesNotCleanupDuringManualRun(t *testing.T) {
	oldFull := maintenanceFileRow(1, "old-full.tar.gz", "full")
	newFull := maintenanceFileRow(2, "new-full.tar.gz", "full")
	keys := map[string]bool{
		"example.com/files/old-full.tar.gz": true,
		"example.com/files/new-full.tar.gz": true,
	}
	deps := testRemoteMaintenanceDeps([]remoteMaintenanceRow{oldFull, newFull}, keys)
	cleaned := false
	state := ""
	deps.cleanup = func(int, string, []string) int { cleaned = true; return 1 }
	deps.setState = func(_ int, status, _ string) { state = status }
	if _, err := maintainRemoteBackupsWith(false, deps); err != nil {
		t.Fatal(err)
	}
	if cleaned || state != "cleanup_pending" {
		t.Fatalf("manual cleanup=%v state=%q, want false/cleanup_pending", cleaned, state)
	}
}

func TestMaintainRemoteBackupsRejectsConcurrentRun(t *testing.T) {
	remoteMaintenanceMu.Lock()
	defer remoteMaintenanceMu.Unlock()
	if _, err := MaintainRemoteBackups(false); err == nil || err.Error() != "远程备份维护任务正在运行" {
		t.Fatalf("MaintainRemoteBackups concurrent error=%v", err)
	}
}
