package executor

import (
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestMaintenanceRebootReasonShrinkingCannotResetBudget(t *testing.T) {
	enableMaintenanceForTest(t)
	db := database.GetDB()
	now := time.Now()
	marker := "marker/" + strings.Repeat("a", 64)
	combined := "kernel/6.12.117-amd64/" + marker
	a, ok, err := claimMaintenanceAction(db, "reboot", combined, nil, "boot-A", now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := finishMaintenanceAction(db, a, "failed", "verification_failed", now); err != nil {
		t.Fatal(err)
	}
	for _, subset := range []string{"kernel/6.12.117-amd64", marker} {
		if _, ok, err := claimMaintenanceAction(db, "reboot", subset, nil, "boot-B", now); err != nil || ok {
			t.Fatalf("smaller reason reset reboot budget: %s %v %v", subset, ok, err)
		}
	}
	// A genuinely new marker package version is a new target, unlike removal
	// of one of the old reboot reasons.
	if _, ok, err := claimMaintenanceAction(db, "reboot", "marker/"+strings.Repeat("b", 64), nil, "boot-B", now); err != nil || !ok {
		t.Fatalf("new marker target blocked: %v %v", ok, err)
	}
}

func TestMaintenanceRebootFilesUseCurrentBootButKernelBudgetSpansBoots(t *testing.T) {
	enableMaintenanceForTest(t)
	db := database.GetDB()
	now := time.Now()
	e := maintenanceTestEvidence(1)
	a, ok, err := claimMaintenanceAction(db, "reboot", "kernel/6.12.117-amd64", []maintenanceRuntimeEvidence{e}, "boot-A", now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := finishMaintenanceAction(db, a, "failed", "verification_failed", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := claimMaintenanceAction(db, "reboot", "runtime", []maintenanceRuntimeEvidence{e}, "boot-A", now); err != nil || ok {
		t.Fatalf("runtime-only subset retried same files: %v %v", ok, err)
	}
	if _, ok, err := claimMaintenanceAction(db, "reboot", "kernel/6.12.117-amd64", nil, "boot-B", now); err != nil || ok {
		t.Fatalf("boot change retried old kernel goal: %v %v", ok, err)
	}
	// Two fresh samples from a new boot cannot contain a surviving deleted
	// mapping from the old boot. Reused inode numbers must not suppress a
	// genuinely new replacement observed on that boot.
	if _, ok, err := claimMaintenanceAction(db, "reboot", "runtime", []maintenanceRuntimeEvidence{e}, "boot-B", now); err != nil || !ok {
		t.Fatalf("new-boot file observation blocked by old inode: %v %v", ok, err)
	}
}

func TestMaintenanceMarkerDigestIgnoresOrderingAndDuplicatePackageFacts(t *testing.T) {
	a := maintenanceMarkerDigest([]string{"reboot-required", "linux=1", "libc=2", "libc=2"})
	b := maintenanceMarkerDigest([]string{"libc=2", "linux=1", "reboot-required"})
	if a != b {
		t.Fatal("duplicate marker facts created a new target")
	}
	if a == maintenanceMarkerDigest([]string{"libc=3", "linux=1", "reboot-required"}) {
		t.Fatal("new package version did not change target")
	}
}
