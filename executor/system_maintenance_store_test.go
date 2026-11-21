package executor

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestMaintenanceSuccessfulBudgetIsCompactAndBounded(t *testing.T) {
	enableMaintenanceForTest(t)
	db := database.GetDB()
	for inode := uint64(10); inode < 13; inode++ {
		e := maintenanceTestEvidence(inode)
		a, ok, err := claimMaintenanceAction(db, "service", "nginx.service", []maintenanceRuntimeEvidence{e}, "boot", time.Now())
		if err != nil || !ok {
			t.Fatalf("claim %d: %v %v", inode, ok, err)
		}
		var stored []map[string]json.RawMessage
		if err := json.Unmarshal([]byte(a.EvidenceJSON), &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored) != 1 {
			t.Fatalf("unexpected budget size: %d", len(stored))
		}
		for _, key := range []string{"pid", "start", "current"} {
			if _, ok := stored[0][key]; ok {
				t.Fatalf("transient process field persisted: %s", key)
			}
		}
		if err := finishMaintenanceAction(db, a, "succeeded", "verified", time.Now()); err != nil {
			t.Fatal(err)
		}
		var pending, completed string
		if err := db.QueryRow(`SELECT evidence_json,last_success_json FROM system_maintenance_actions WHERE id=?`, a.ID).Scan(&pending, &completed); err != nil {
			t.Fatal(err)
		}
		if pending != "[]" {
			t.Fatalf("success retained failed union: %s", pending)
		}
		var keys []maintenanceStoredEvidence
		if err := json.Unmarshal([]byte(completed), &keys); err != nil || len(keys) != 1 {
			t.Fatalf("success history accumulated: %v %v", keys, err)
		}
		if _, ok, err := claimMaintenanceAction(db, "service", "nginx.service", []maintenanceRuntimeEvidence{e}, "boot", time.Now()); err != nil || ok {
			t.Fatalf("same completed evidence reauthorized: %v %v", ok, err)
		}
	}
}

func TestMaintenanceRejectsEvidenceForDifferentService(t *testing.T) {
	enableMaintenanceForTest(t)
	if _, _, err := claimMaintenanceAction(database.GetDB(), "service", "redis-server.service", []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}, "boot", time.Now()); err == nil {
		t.Fatal("mismatched unit authorized")
	}
	e := maintenanceTestEvidence(1)
	e.Unit = "nginx"
	if _, _, err := claimMaintenanceAction(database.GetDB(), "service", "nginx", []maintenanceRuntimeEvidence{e}, "boot", time.Now()); err == nil {
		t.Fatal("incomplete service name authorized")
	}
}

func TestMaintenanceBudgetDeduplicatesLargeWorkerInput(t *testing.T) {
	enableMaintenanceForTest(t)
	var evidence []maintenanceRuntimeEvidence
	for pid := 1; pid <= 10000; pid++ {
		e := maintenanceTestEvidence(1)
		e.PID = pid
		evidence = append(evidence, e)
	}
	a, ok, err := claimMaintenanceAction(database.GetDB(), "service", "nginx.service", evidence, "boot", time.Now())
	if err != nil || !ok {
		t.Fatalf("worker count blocked action: %v %v", ok, err)
	}
	var stored []maintenanceStoredEvidence
	if err := json.Unmarshal([]byte(a.EvidenceJSON), &stored); err != nil || len(stored) != 1 {
		t.Fatalf("worker facts not compacted: %d %v", len(stored), err)
	}
}

func TestMaintenanceCollectorBoundsWorkersWithoutLosingOtherUnits(t *testing.T) {
	snapshot := maintenanceRuntimeSnapshot{UnknownUnits: map[string]bool{}}
	c := newMaintenanceEvidenceCollector(&snapshot)
	for pid := 10000; pid > 0; pid-- {
		e := maintenanceTestEvidence(1)
		e.PID = pid
		c.add(e)
	}
	got := c.evidence()
	if len(got) != maintenanceMaxRepresentatives || len(snapshot.UnknownUnits) != 0 {
		t.Fatalf("worker multiplicity invalidated snapshot: %d %v", len(got), snapshot.UnknownUnits)
	}
	for _, e := range got {
		if e.PID > maintenanceMaxRepresentatives {
			t.Fatalf("representative not deterministic: %d", e.PID)
		}
	}
	other := maintenanceTestEvidence(9000)
	other.Unit = "redis-server.service"
	c.add(other)
	for i := 0; i <= maintenanceMaxUnitFiles; i++ {
		e := maintenanceTestEvidence(uint64(i + 100))
		e.Unit = "nginx.service"
		c.add(e)
	}
	got = c.evidence()
	if !snapshot.UnknownUnits["nginx.service"] || snapshot.UnknownUnits["redis-server.service"] || len(got) != 1 || got[0].Unit != "redis-server.service" {
		t.Fatalf("unit overflow invalidated other unit: %+v %+v", snapshot, got)
	}
}

func maintenanceTestEvidence(inode uint64) maintenanceRuntimeEvidence {
	return maintenanceRuntimeEvidence{Unit: "nginx.service", PID: 123, Start: 456,
		Path: "/usr/lib/example.so", Old: maintenanceFileIdentity{1, inode},
		Current: maintenanceFileIdentity{1, inode + 100}, Package: "example:amd64"}
}

func enableMaintenanceForTest(t *testing.T) {
	t.Helper()
	setupWPPackageSchedulerTestDB(t)
	if err := SaveSystemMaintenancePolicy(database.GetDB(), SystemMaintenancePolicy{true, "scheduled", "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceBudgetShrinkingEvidenceDoesNotRetry(t *testing.T) {
	enableMaintenanceForTest(t)
	db := database.GetDB()
	now := time.Now().UTC()
	claim := func(e []maintenanceRuntimeEvidence) (systemMaintenanceAction, bool) {
		t.Helper()
		a, ok, err := claimMaintenanceAction(db, "service", "nginx.service", e, "boot-one", now)
		if err != nil {
			t.Fatal(err)
		}
		return a, ok
	}
	one, two := maintenanceTestEvidence(1), maintenanceTestEvidence(2)
	a, ok := claim([]maintenanceRuntimeEvidence{one, two})
	if !ok {
		t.Fatal("first action not claimed")
	}
	if _, ok := claim([]maintenanceRuntimeEvidence{one, two}); ok {
		t.Fatal("duplicate requested action claimed")
	}
	if err := finishMaintenanceAction(db, a, "failed", "health_failed", now); err != nil {
		t.Fatal(err)
	}
	if _, ok := claim([]maintenanceRuntimeEvidence{one}); ok {
		t.Fatal("shrinking old evidence restored automatic budget")
	}
	if err := requestMaintenanceActionRetry(db, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := requestMaintenanceActionRetry(db, a.ID); err == nil {
		t.Fatal("double click added another retry authorization")
	}
	b, ok := claim([]maintenanceRuntimeEvidence{one})
	if !ok || b.Attempts != 2 {
		t.Fatal("explicit retry not consumed exactly once")
	}
	if err := finishMaintenanceAction(db, a, "succeeded", "stale_completion", now); err == nil {
		t.Fatal("old attempt completed a later action")
	}
	if err := finishMaintenanceAction(db, b, "unknown", "interrupted", now); err != nil {
		t.Fatal(err)
	}
	if _, ok := claim([]maintenanceRuntimeEvidence{two}); ok {
		t.Fatal("lost evidence reappearing restored budget")
	}
	c, ok := claim([]maintenanceRuntimeEvidence{maintenanceTestEvidence(3)})
	if !ok || c.Attempts != 3 {
		t.Fatal("genuinely new replaced file did not obtain a new budget")
	}
}

func TestMaintenanceRetryDisabledAndRequestedRebootCannotDuplicate(t *testing.T) {
	enableMaintenanceForTest(t)
	db := database.GetDB()
	now := time.Now().UTC()
	a, ok, err := claimMaintenanceAction(db, "reboot", "kernel/6.12.117-amd64", nil, "boot-one", now)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := requestMaintenanceActionRetry(db, a.ID); err == nil {
		t.Fatal("requested reboot granted a second authorization")
	}
	if err := finishMaintenanceAction(db, a, "failed", "request_failed", now); err != nil {
		t.Fatal(err)
	}
	if err := requestMaintenanceActionRetry(db, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{false, "scheduled", "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	if err := requestMaintenanceActionRetry(db, a.ID); err == nil {
		t.Fatal("disabled maintenance accepted manual retry")
	}
	if _, _, err := claimMaintenanceAction(db, "reboot", "kernel/new", nil, "boot-one", now); err == nil {
		t.Fatal("disabled maintenance granted automatic action")
	}
	if err := SaveSystemMaintenancePolicy(db, SystemMaintenancePolicy{true, "scheduled", "04:00-06:00"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := claimMaintenanceAction(db, "reboot", a.Target, nil, "boot-one", now); err != nil || ok {
		t.Fatalf("re-enable resurrected unused manual authorization: %v %v", ok, err)
	}
}

func TestMaintenanceConcurrentClaimsHaveOneBudgetOwner(t *testing.T) {
	enableMaintenanceForTest(t)
	db := database.GetDB()
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := claimMaintenanceAction(db, "service", "nginx.service", []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}, "boot-one", time.Now())
			results <- ok
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	claimed := 0
	for ok := range results {
		if ok {
			claimed++
		}
	}
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed=%d", claimed)
	}
}

func TestMaintenanceEvidenceNeedsStableProcessAndKnownSamples(t *testing.T) {
	now := time.Now().UTC()
	e := maintenanceTestEvidence(10)
	previous := maintenanceRuntimeSnapshot{At: now, Evidence: []maintenanceRuntimeEvidence{e}}
	current := maintenanceRuntimeSnapshot{At: now.Add(10 * time.Minute), Evidence: []maintenanceRuntimeEvidence{e}}
	if len(persistentMaintenanceEvidence(previous, current)) != 1 {
		t.Fatal("stable process evidence lost")
	}
	current.Evidence = append([]maintenanceRuntimeEvidence(nil), current.Evidence...)
	current.Evidence[0].Start++ // PID reused by a different process.
	if len(persistentMaintenanceEvidence(previous, current)) != 0 {
		t.Fatal("PID reuse counted as persistent")
	}
	current.Evidence[0] = e
	current.UnknownUnits = map[string]bool{"nginx.service": true}
	if len(persistentMaintenanceEvidence(previous, current)) != 0 {
		t.Fatal("unknown unit authorized action")
	}
	current.UnknownUnits = map[string]bool{"*": true}
	if len(persistentMaintenanceEvidence(previous, current)) != 0 {
		t.Fatal("unclassified read error authorized action")
	}
	current.UnknownUnits = nil
	current.At = now.Add(time.Second)
	if len(persistentMaintenanceEvidence(previous, current)) != 0 {
		t.Fatal("short interval authorized action")
	}
}
