package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

func TestMaintenanceCollectorGlobalLimitPreservesOtherUnits(t *testing.T) {
	snapshot := maintenanceRuntimeSnapshot{UnknownUnits: map[string]bool{}}
	c := newMaintenanceEvidenceCollector(&snapshot)
	for _, unit := range []string{"nginx.service", "php8.3-fpm.service"} {
		for file := 1; file <= maintenanceMaxUnitFiles; file++ {
			for pid := 1; pid <= maintenanceMaxRepresentatives; pid++ {
				e := maintenanceTestEvidence(uint64(file))
				e.Unit = unit
				e.PID = pid
				c.add(e)
			}
		}
	}
	e := maintenanceTestEvidence(1000)
	e.Unit = "redis-server.service"
	c.add(e)
	if !snapshot.UnknownUnits[e.Unit] || snapshot.UnknownUnits["nginx.service"] || snapshot.UnknownUnits["php8.3-fpm.service"] || len(c.evidence()) != maintenanceMaxSnapshotEvidence {
		t.Fatalf("global overflow lost unrelated units: count=%d unknown=%v", len(c.evidence()), snapshot.UnknownUnits)
	}
}

func TestMaintenanceEvidenceBudgetOverflowIsVisibleAndExplicitRetryRecovers(t *testing.T) {
	enableMaintenanceForTest(t)
	db := database.GetDB()
	now := time.Now()
	a, ok, err := claimMaintenanceAction(db, "service", "nginx.service", []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}, "boot", now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := finishMaintenanceAction(db, a, "failed", "verification_failed", now); err != nil {
		t.Fatal(err)
	}
	sample := maintenanceStoredEvidence{Unit: "nginx.service", Path: "/usr/lib/" + strings.Repeat("a", 3900) + "0000.so", Old: maintenanceFileIdentity{1, 1}, Package: "example"}
	entry, _ := json.Marshal(sample)
	count := ((8 << 20) - 2) / (len(entry) + 1)
	files := make([]maintenanceStoredEvidence, count)
	for i := range files {
		files[i] = sample
		files[i].Path = fmt.Sprintf("/usr/lib/%s%04d.so", strings.Repeat("a", 3900), i)
	}
	encoded, _ := json.Marshal(files)
	if len(encoded) > 8<<20 {
		t.Fatal("fixture should begin below evidence limit")
	}
	if _, err := db.Exec(`UPDATE system_maintenance_actions SET evidence_json=? WHERE id=?`, string(encoded), a.ID); err != nil {
		t.Fatal(err)
	}
	var next []maintenanceRuntimeEvidence
	for i := 0; i < 20; i++ {
		e := maintenanceTestEvidence(uint64(i + 100))
		e.Path = fmt.Sprintf("/usr/lib/%s-%d.so", strings.Repeat("b", 1000), i)
		next = append(next, e)
	}
	if _, ok, err := claimMaintenanceAction(db, "service", "nginx.service", next, "boot", now); err == nil || ok {
		t.Fatal("overflow silently authorized action")
	}
	var code string
	db.QueryRow(`SELECT message_code FROM system_maintenance_actions WHERE id=?`, a.ID).Scan(&code)
	if code != "evidence_budget_exhausted" {
		t.Fatalf("overflow not visible: %s", code)
	}
	if err := requestMaintenanceActionRetry(db, a.ID); err != nil {
		t.Fatal(err)
	}
	b, ok, err := claimMaintenanceAction(db, "service", "nginx.service", next, "boot", now)
	if err != nil || !ok || len(b.EvidenceJSON) > 50000 || b.Attempts != 2 {
		t.Fatalf("manual overflow recovery: claimed=%v attempts=%d bytes=%d err=%v", ok, b.Attempts, len(b.EvidenceJSON), err)
	}
}

func TestMaintenanceBudgetProcessHelper(t *testing.T) {
	path := os.Getenv("WP_PANEL_MAINTENANCE_CLAIM_FIXTURE")
	if path == "" {
		return
	}
	if err := database.Open(path); err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	fmt.Println("ready")
	var token [1]byte
	if _, err := os.Stdin.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	_, claimed, err := claimMaintenanceAction(database.GetDB(), "service", "nginx.service", []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}, "fixture-boot", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		fmt.Println("claimed")
	} else {
		fmt.Println("denied")
	}
}

func TestMaintenanceBudgetIsExclusiveAcrossProcesses(t *testing.T) {
	cfg, _ := setupMaintenanceRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type child struct {
		cmd     *exec.Cmd
		input   io.WriteCloser
		scanner *bufio.Scanner
		done    bool
	}
	children := make([]*child, 0, 2)
	for i := 0; i < 2; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMaintenanceBudgetProcessHelper$")
		cmd.Env = append(os.Environ(), "WP_PANEL_MAINTENANCE_CLAIM_FIXTURE="+cfg.SQLite.Path)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		c := &child{cmd: cmd, input: input, scanner: bufio.NewScanner(out)}
		children = append(children, c)
		t.Cleanup(func() {
			c.input.Close()
			if !c.done {
				_ = c.cmd.Process.Kill()
				_ = c.cmd.Wait()
			}
		})
		if !c.scanner.Scan() || c.scanner.Text() != "ready" {
			t.Fatal("claim fixture did not become ready")
		}
	}
	for _, c := range children {
		if _, err := c.input.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	winners, denied := 0, 0
	for _, c := range children {
		for c.scanner.Scan() {
			switch c.scanner.Text() {
			case "claimed":
				winners++
			case "denied":
				denied++
			}
		}
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("claim process failed: %v", err)
		}
		c.done = true
	}
	if winners != 1 || denied != 1 {
		t.Fatalf("cross-process winners=%d denied=%d", winners, denied)
	}
}
