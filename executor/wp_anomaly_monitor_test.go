package executor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

func anomalyFixture(t *testing.T) (*WPAnomalyMonitor, int, *time.Time) {
	t.Helper()
	store, id := newWPUpdateStoreTest(t)
	m := NewWPAnomalyMonitor(store.db, nil)
	now := time.Unix(1800000000, 0)
	m.now = func() time.Time { return now }
	m.notify = func(string, string) {}
	return m, id, &now
}
func anomalyAdmin(id int) WPAnomalyAdmin {
	return WPAnomalyAdmin{ID: id, Login: "user", Roles: []string{"administrator"}, EmailHash: strings.Repeat("a", 64)}
}
func anomalySample(admins ...WPAnomalyAdmin) *WPAnomalySample {
	return &WPAnomalySample{Admins: append([]WPAnomalyAdmin{}, admins...), Removed: []WPAnomalyRemoved{}}
}
func anomalyCheck(t *testing.T, m *WPAnomalyMonitor, id int) WPAnomalyState {
	t.Helper()
	state, err := m.Check(context.Background(), id)
	if err != nil || state.LastError != "" {
		t.Fatalf("check %+v %v", state, err)
	}
	return state
}
func TestWPAnomalyBaselineDedupAndFailure(t *testing.T) {
	m, id, now := anomalyFixture(t)
	state, err := m.Status(id)
	if err != nil || state.Enabled || state.Threshold != 5 {
		t.Fatal(state, err)
	}
	sample := anomalySample(anomalyAdmin(1))
	calls, notifications := 0, 0
	var query wpAnomalyQuery
	m.collect = func(_ context.Context, _ *models.Website, q wpAnomalyQuery) (*WPAnomalySample, error) {
		calls++
		query = q
		return sample, nil
	}
	m.notify = func(string, string) { notifications++ }
	if _, err = m.Check(context.Background(), id); !errors.Is(err, ErrWPAnomalyDisabled) || calls != 0 {
		t.Fatal(err)
	}
	if err = m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	first := anomalyCheck(t, m, id)
	if first.BaselineSince != now.Unix() || query.Since != now.Unix() || notifications != 0 {
		t.Fatal("initial baseline")
	}
	*now = now.Add(time.Hour)
	sample = anomalySample(anomalyAdmin(1), anomalyAdmin(2))
	sample.PostCount = 6
	next := anomalyCheck(t, m, id)
	if notifications != 2 || next.PostCount != 6 || query.Since != first.BaselineSince || !reflect.DeepEqual(query.KnownIDs, []int{1}) {
		t.Fatal(next, notifications, query)
	}
	anomalyCheck(t, m, id)
	if notifications != 2 {
		t.Fatal("duplicate notification")
	}
	sample = &WPAnomalySample{Error: "plugin_required"}
	failed, err := m.Check(context.Background(), id)
	if err != nil || failed.LastError != "plugin_required" || failed.LastSuccess != next.LastSuccess || !reflect.DeepEqual(failed.Admins, next.Admins) || !failed.PostAlerted {
		t.Fatal("lost success state", failed, err)
	}
	// New manager has no process-local event cache to preserve: dedup is in SQLite.
	restarted := NewWPAnomalyMonitor(m.db, nil)
	restarted.now = m.now
	restarted.collect = m.collect
	restarted.notify = m.notify
	sample = anomalySample(anomalyAdmin(1))
	sample.Removed = []WPAnomalyRemoved{{ID: 2, Deleted: false}}
	sample.PostCount = 5
	after := anomalyCheck(t, restarted, id)
	if notifications != 3 || after.PostAlerted {
		t.Fatal("demotion or post rearm", after, notifications)
	}
	sample = anomalySample()
	sample.Removed = []WPAnomalyRemoved{{ID: 1, Deleted: true}}
	sample.PostCount = 6
	anomalyCheck(t, restarted, id)
	if notifications != 5 {
		t.Fatal("delete and new volume event", notifications)
	}
	var n int
	if err = m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type IN ('alert_wp_admin_change','alert_wp_post_volume') AND level='critical'`).Scan(&n); err != nil || n != 5 {
		t.Fatal(n, err)
	}
	// Re-enable establishes a fresh baseline, not old changes during disabled time.
	if err = m.Configure(id, false, 10); err != nil {
		t.Fatal(err)
	}
	if err = m.Configure(id, true, 10); err != nil {
		t.Fatal(err)
	}
	sample = anomalySample(anomalyAdmin(3))
	anomalyCheck(t, m, id)
	if notifications != 5 {
		t.Fatal("re-enable alerted on old accounts")
	}
}

func TestWPAnomalyAtomicEventAndBaseline(t *testing.T) {
	m, id, _ := anomalyFixture(t)
	sample := anomalySample(anomalyAdmin(1))
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	before := anomalyCheck(t, m, id)
	if _, err := m.db.Exec(`CREATE TRIGGER fail_anomaly_event BEFORE INSERT ON alert_log BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	sample = anomalySample(anomalyAdmin(1), anomalyAdmin(2))
	if _, err := m.Check(context.Background(), id); err == nil {
		t.Fatal("transaction failure hidden")
	}
	saved, err := m.Status(id)
	if err != nil || !reflect.DeepEqual(saved.Admins, before.Admins) {
		t.Fatal("baseline advanced without event")
	}
	if _, err := m.db.Exec(`DROP TRIGGER fail_anomaly_event`); err != nil {
		t.Fatal(err)
	}
	anomalyCheck(t, m, id)
	var n int
	m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_admin_change'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}

func TestWPAnomalyAdmissionAndValidation(t *testing.T) {
	m, id, _ := anomalyFixture(t)
	if err := m.Configure(id, true, 0); !errors.Is(err, ErrWPAnomalyInvalid) {
		t.Fatal(err)
	}
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) {
		calls++
		return anomalySample(), nil
	}
	m.mu.Lock()
	_, err := m.Check(context.Background(), id)
	m.mu.Unlock()
	if !errors.Is(err, ErrWPAnomalyBusy) {
		t.Fatal(err)
	}
	if _, err = m.db.Exec(`UPDATE websites SET status='paused' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	state, err := m.Check(context.Background(), id)
	if err != nil || state.LastError != "site_unavailable" || calls != 0 {
		t.Fatal(state, err)
	}
	if _, err = m.db.Exec(`UPDATE websites SET site_type='php' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Status(id); err == nil {
		t.Fatal("PHP site accepted")
	}
	if _, err = m.Status(id + 100); err == nil {
		t.Fatal("unknown site accepted")
	}
	bad := anomalySample(anomalyAdmin(1))
	bad.Admins[0].EmailHash = "raw@example.com"
	if validateAnomalySample(bad, nil) == nil {
		t.Fatal("unhashed email accepted")
	}
	if validateAnomalySample(anomalySample(), []int{1}) == nil {
		t.Fatal("missing former administrator accepted")
	}
	good := anomalySample()
	good.Removed = []WPAnomalyRemoved{{ID: 1, Deleted: true}}
	if validateAnomalySample(good, []int{1}) != nil {
		t.Fatal("valid deletion rejected")
	}
	good.Removed = append(good.Removed, good.Removed[0])
	if validateAnomalySample(good, []int{1}) == nil {
		t.Fatal("duplicate removal accepted")
	}
}

func TestWPAnomalySchedulerCadenceAndSiteIsolation(t *testing.T) {
	m, id, now := anomalyFixture(t)
	result, err := m.db.Exec(`INSERT INTO websites(name,domain,status,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES('B','b.example','active','wp_b','/tmp/b','','','','','')`)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := result.LastInsertId()
	for _, sid := range []int{id, int(second)} {
		if err = m.Configure(sid, true, 5); err != nil {
			t.Fatal(err)
		}
	}
	calls := map[int]int{}
	m.collect = func(_ context.Context, s *models.Website, _ wpAnomalyQuery) (*WPAnomalySample, error) {
		calls[s.ID]++
		return anomalySample(anomalyAdmin(s.ID)), nil
	}
	anomalyDefault.Lock()
	before := anomalyDefault.monitor
	anomalyDefault.monitor = m
	anomalyDefault.Unlock()
	defer func() { anomalyDefault.Lock(); anomalyDefault.monitor = before; anomalyDefault.Unlock() }()
	runWPAnomalyChecks()
	runWPAnomalyChecks()
	if calls[id] != 1 || calls[int(second)] != 1 {
		t.Fatal(calls)
	}
	a, _ := m.Status(id)
	b, _ := m.Status(int(second))
	if a.Admins[0].ID == b.Admins[0].ID {
		t.Fatal("site baseline mixed")
	}
	*now = now.Add(3599 * time.Second)
	runWPAnomalyChecks()
	if calls[id] != 1 {
		t.Fatal("early sample")
	}
	*now = now.Add(time.Second)
	if err = m.Configure(int(second), false, 5); err != nil {
		t.Fatal(err)
	}
	runWPAnomalyChecks()
	if calls[id] != 2 || calls[int(second)] != 1 {
		t.Fatal(calls)
	}
}
