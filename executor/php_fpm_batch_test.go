package executor

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func setupPHPFPMBatchTest(t *testing.T) {
	t.Helper()
	oldWrite := writePHPFPMPoolFile
	oldRemove := removePHPFPMPoolFile
	oldTest := testPHPFPMConfig
	oldCheck := checkPHPFPMSocket
	oldAction := runPHPFPMServiceAction
	oldIndividual := applyPHPFPMPoolIndividually
	t.Cleanup(func() {
		writePHPFPMPoolFile = oldWrite
		removePHPFPMPoolFile = oldRemove
		testPHPFPMConfig = oldTest
		checkPHPFPMSocket = oldCheck
		runPHPFPMServiceAction = oldAction
		applyPHPFPMPoolIndividually = oldIndividual
	})
}

func batchTestPlan(t *testing.T, root, domain, oldContent, newContent string) phpFPMPoolBatchPlan {
	t.Helper()
	target := filepath.Join(root, domain+".conf")
	if err := os.WriteFile(target, []byte(oldContent), 0644); err != nil {
		t.Fatal(err)
	}
	return phpFPMPoolBatchPlan{
		domain:        domain,
		configContent: newContent,
		targetPath:    target,
		logDir:        filepath.Join(root, domain+"-logs"),
		socketPath:    filepath.Join(root, domain+".sock"),
	}
}

func TestApplyPHPFPMPoolBatchSkipsUnchangedReadyPools(t *testing.T) {
	setupPHPFPMBatchTest(t)
	root := t.TempDir()
	plans := []phpFPMPoolBatchPlan{
		batchTestPlan(t, root, "one", "same-one", "same-one"),
		batchTestPlan(t, root, "two", "same-two", "same-two"),
	}
	writes, tests, actions := 0, 0, 0
	writePHPFPMPoolFile = func(string, []byte, os.FileMode) error { writes++; return nil }
	testPHPFPMConfig = func() ([]byte, error) { tests++; return nil, nil }
	checkPHPFPMSocket = func(string, int, time.Duration) error { return nil }
	runPHPFPMServiceAction = func(string) error { actions++; return nil }

	result, err := NewTemplateEngine(root).applyPHPFPMPoolBatch(plans)
	if err != nil {
		t.Fatal(err)
	}
	if writes != 0 || tests != 0 || actions != 0 || result.skipped != 2 || result.reloadAttempts != 0 {
		t.Fatalf("result=%+v writes=%d tests=%d actions=%d", result, writes, tests, actions)
	}
}

func TestApplyPHPFPMPoolBatchRestartsUnchangedPoolWithMissingSocket(t *testing.T) {
	setupPHPFPMBatchTest(t)
	root := t.TempDir()
	plan := batchTestPlan(t, root, "one", "same", "same")
	writes, tests := 0, 0
	var actions []string
	checks := 0
	writePHPFPMPoolFile = func(string, []byte, os.FileMode) error { writes++; return nil }
	testPHPFPMConfig = func() ([]byte, error) { tests++; return nil, nil }
	checkPHPFPMSocket = func(string, int, time.Duration) error {
		checks++
		if checks == 1 {
			return errors.New("missing")
		}
		return nil
	}
	runPHPFPMServiceAction = func(action string) error { actions = append(actions, action); return nil }

	result, err := NewTemplateEngine(root).applyPHPFPMPoolBatch([]phpFPMPoolBatchPlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	if writes != 0 || tests != 0 || !reflect.DeepEqual(actions, []string{"restart"}) || result.recovery != 1 || result.reloadAttempts != 0 || result.restartAttempts != 1 {
		t.Fatalf("result=%+v writes=%d tests=%d actions=%v", result, writes, tests, actions)
	}
}

func TestApplyPHPFPMPoolBatchAppliesMultipleChangesWithOneReload(t *testing.T) {
	setupPHPFPMBatchTest(t)
	root := t.TempDir()
	plans := []phpFPMPoolBatchPlan{
		batchTestPlan(t, root, "one", "old-one", "new-one"),
		batchTestPlan(t, root, "two", "old-two", "new-two"),
	}
	writes, tests := 0, 0
	var actions []string
	writePHPFPMPoolFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		return os.WriteFile(path, data, mode)
	}
	testPHPFPMConfig = func() ([]byte, error) { tests++; return nil, nil }
	checkPHPFPMSocket = func(string, int, time.Duration) error { return nil }
	runPHPFPMServiceAction = func(action string) error { actions = append(actions, action); return nil }

	result, err := NewTemplateEngine(root).applyPHPFPMPoolBatch(plans)
	if err != nil {
		t.Fatal(err)
	}
	if writes != 2 || tests != 1 || !reflect.DeepEqual(actions, []string{"reload"}) || result.changed != 2 || result.reloadAttempts != 1 || result.restartAttempts != 0 {
		t.Fatalf("result=%+v writes=%d tests=%d actions=%v", result, writes, tests, actions)
	}
}

func TestApplyPHPFPMPoolBatchKeepsReloadRestartStartFallbackOrder(t *testing.T) {
	setupPHPFPMBatchTest(t)
	root := t.TempDir()
	plan := batchTestPlan(t, root, "one", "old", "new")
	writePHPFPMPoolFile = os.WriteFile
	testPHPFPMConfig = func() ([]byte, error) { return nil, nil }
	checkPHPFPMSocket = func(string, int, time.Duration) error { return nil }
	var actions []string
	runPHPFPMServiceAction = func(action string) error {
		actions = append(actions, action)
		if action != "start" {
			return errors.New("failed")
		}
		return nil
	}

	result, err := NewTemplateEngine(root).applyPHPFPMPoolBatch([]phpFPMPoolBatchPlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"reload", "restart", "start"}) {
		t.Fatalf("actions=%v", actions)
	}
	if result.reloadAttempts != 1 || result.restartAttempts != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestApplyPHPFPMPoolBatchSyntaxFailureRestoresThenFallsBackPerSite(t *testing.T) {
	setupPHPFPMBatchTest(t)
	root := t.TempDir()
	plans := []phpFPMPoolBatchPlan{
		batchTestPlan(t, root, "one", "old-one", "new-one"),
		batchTestPlan(t, root, "two", "old-two", "new-two"),
	}
	writePHPFPMPoolFile = os.WriteFile
	testPHPFPMConfig = func() ([]byte, error) { return []byte("invalid"), errors.New("invalid") }
	checkPHPFPMSocket = func(string, int, time.Duration) error { return nil }
	runPHPFPMServiceAction = func(string) error { t.Fatal("batch service action must not run after syntax failure"); return nil }
	var fallback []string
	applyPHPFPMPoolIndividually = func(_ *TemplateEngine, plan phpFPMPoolBatchPlan) error {
		content, err := os.ReadFile(plan.targetPath)
		if err != nil {
			return err
		}
		if string(content) != "old-"+plan.domain {
			t.Fatalf("%s content before fallback=%q", plan.domain, content)
		}
		fallback = append(fallback, plan.domain)
		return nil
	}

	result, err := NewTemplateEngine(root).applyPHPFPMPoolBatch(plans)
	if err != nil {
		t.Fatal(err)
	}
	if !result.fallback || !reflect.DeepEqual(fallback, []string{"one", "two"}) {
		t.Fatalf("result=%+v fallback=%v", result, fallback)
	}
}

func TestApplyPHPFPMPoolBatchRestoresOnlySocketFailure(t *testing.T) {
	setupPHPFPMBatchTest(t)
	root := t.TempDir()
	plans := []phpFPMPoolBatchPlan{
		batchTestPlan(t, root, "good", "old-good", "new-good"),
		batchTestPlan(t, root, "bad", "old-bad", "new-bad"),
	}
	writePHPFPMPoolFile = os.WriteFile
	testPHPFPMConfig = func() ([]byte, error) { return nil, nil }
	badChecks := 0
	checkPHPFPMSocket = func(path string, _ int, _ time.Duration) error {
		if path != plans[1].socketPath {
			return nil
		}
		badChecks++
		if badChecks == 1 {
			return errors.New("missing")
		}
		return nil
	}
	var actions []string
	runPHPFPMServiceAction = func(action string) error { actions = append(actions, action); return nil }

	_, err := NewTemplateEngine(root).applyPHPFPMPoolBatch(plans)
	if err == nil {
		t.Fatal("partial socket failure must be reported even after recovery")
	}
	good, _ := os.ReadFile(plans[0].targetPath)
	bad, _ := os.ReadFile(plans[1].targetPath)
	if string(good) != "new-good" || string(bad) != "old-bad" {
		t.Fatalf("good=%q bad=%q", good, bad)
	}
	if !reflect.DeepEqual(actions, []string{"reload", "restart"}) {
		t.Fatalf("actions=%v", actions)
	}
}
