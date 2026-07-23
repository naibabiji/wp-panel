package executor

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

type fakeWPPluginRunnerSession struct {
	active bool
	calls  []string
	err    error
}

func (f *fakeWPPluginRunnerSession) Observe(context.Context) (bool, error) {
	f.calls = append(f.calls, "observe")
	return f.active, f.err
}
func (f *fakeWPPluginRunnerSession) Update(context.Context) error {
	f.calls = append(f.calls, "update")
	return f.err
}
func (f *fakeWPPluginRunnerSession) Reactivate(context.Context) error {
	f.calls = append(f.calls, "reactivate")
	return f.err
}
func (f *fakeWPPluginRunnerSession) Check(_ context.Context, version string, active bool) error {
	f.calls = append(f.calls, "check:"+version+":"+strconv.FormatBool(active))
	return f.err
}
func (f *fakeWPPluginRunnerSession) Journal() (wpPluginUpdateJournalReport, error) {
	return wpPluginUpdateJournalReport{}, f.err
}
func (f *fakeWPPluginRunnerSession) Close() error { f.calls = append(f.calls, "close"); return f.err }

func TestWPPluginSystemOperationsInactiveFlow(t *testing.T) {
	store, _ := newWPUpdateStoreTest(t)
	packagePath := writeComponentZIP(t, []componentZIPEntry{{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")}})
	session := &fakeWPPluginRunnerSession{}
	linted, probed := 0, 0
	ops, err := newWPPluginSystemOperations(store,
		func(context.Context, wpPluginUpdateExecution) (wpPluginRunnerSessionAPI, error) { return session, nil },
		func(context.Context, string, string) error { return nil }, func(context.Context, wpPluginUpdateExecution) error { return nil },
		func(context.Context, string) error { probed++; return nil }, func(context.Context, wpPluginUpdateExecution) error { linted++; return nil },
		func(context.Context, wpPluginUpdateExecution, ZIPInspection) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	execution := wpPluginUpdateExecution{Task: WPUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", ComponentType: "plugin", ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "2.0.0", VerificationLevel: "structure_only"}, PackagePath: packagePath, Domain: "example.com"}
	active, err := ops.Prepare(context.Background(), execution)
	if err != nil || active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if err := ops.ApplyPluginUpdate(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if err := ops.CheckTargetHealth(context.Background(), execution, false); err != nil {
		t.Fatal(err)
	}
	if linted != 1 || probed != 1 {
		t.Fatalf("linted=%d probed=%d", linted, probed)
	}
	want := []string{"observe", "update", "check:2.0.0:false"}
	if !reflect.DeepEqual(session.calls, want) {
		t.Fatalf("calls=%v", session.calls)
	}
}

func TestWPPluginSystemOperationsPreservesUncertainSession(t *testing.T) {
	store, _ := newWPUpdateStoreTest(t)
	packagePath := writeComponentZIP(t, []componentZIPEntry{{name: "sample/sample.php", body: pluginHeader("Sample", "2.0.0")}})
	session := &fakeWPPluginRunnerSession{err: errWPPluginScopeSupervisionUncertain}
	ops, _ := newWPPluginSystemOperations(store, func(context.Context, wpPluginUpdateExecution) (wpPluginRunnerSessionAPI, error) { return session, nil }, func(context.Context, string, string) error { return nil }, func(context.Context, wpPluginUpdateExecution) error { return nil }, func(context.Context, string) error { return nil }, func(context.Context, wpPluginUpdateExecution) error { return nil }, func(context.Context, wpPluginUpdateExecution, ZIPInspection) error { return nil })
	execution := wpPluginUpdateExecution{Task: WPUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", ComponentType: "plugin", ComponentKey: "sample/sample.php", CurrentVersion: "1.0.0", TargetVersion: "2.0.0", VerificationLevel: "structure_only"}, PackagePath: packagePath}
	if _, err := ops.Prepare(context.Background(), execution); !errors.Is(err, errWPPluginScopeSupervisionUncertain) {
		t.Fatalf("err=%v", err)
	}
	if _, err := ops.session(execution.Task.ID); err != nil {
		t.Fatal("uncertain session was discarded")
	}
	if reflect.DeepEqual(session.calls, []string{"observe", "close"}) {
		t.Fatal("uncertain session was closed")
	}
}
