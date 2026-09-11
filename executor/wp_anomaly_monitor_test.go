package executor

import (
	"context"
	"encoding/json"
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
	return WPAnomalyAdmin{ID: id, Login: "user", Roles: []string{"administrator"}, EmailHash: strings.Repeat("a", 64), DisplayHash: strings.Repeat("b", 64), CredentialHash: strings.Repeat("c", 64)}
}
func anomalySample(admins ...WPAnomalyAdmin) *WPAnomalySample {
	return &WPAnomalySample{Version: 2, Admins: append([]WPAnomalyAdmin{}, admins...), Removed: []WPAnomalyRemoved{}, Content: []WPAnomalyContent{}, Options: WPAnomalyCriticalOptions{SiteURL: "https://example.com/wp", Home: "https://example.com", DefaultRole: "subscriber"}}
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

func TestWPAnomalyContentAndCriticalOptionAlerts(t *testing.T) {
	m, id, now := anomalyFixture(t)
	sample := anomalySample(anomalyAdmin(1))
	sample.Content = []WPAnomalyContent{
		{ID: 10, Type: "page", Fingerprint: strings.Repeat("1", 64)},
		{ID: 20, Type: "post", Fingerprint: strings.Repeat("2", 64)},
	}
	sample.Options.FrontPageID = 10
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	anomalyCheck(t, m, id)
	var notifications []string
	m.notify = func(_ string, message string) { notifications = append(notifications, message) }
	*now = now.Add(time.Hour)
	sample = anomalySample(anomalyAdmin(1))
	sample.Content = []WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("3", 64)}}
	sample.Options.FrontPageID = 10
	sample.Options.UsersCanRegister = true
	sample.Options.DefaultRole = "administrator"
	anomalyCheck(t, m, id)
	if len(notifications) != 2 || !strings.Contains(strings.Join(notifications, " "), "首页") || !strings.Contains(strings.Join(notifications, " "), "取消发布") || !strings.Contains(strings.Join(notifications, " "), "任何人可以注册") {
		t.Fatalf("notifications=%q", notifications)
	}
	// The deletion and homepage change advance with the baseline and do not repeat.
	anomalyCheck(t, m, id)
	if len(notifications) != 2 {
		t.Fatalf("duplicate content alert: %q", notifications)
	}
}

func TestWPAnomalyAlertLabels(t *testing.T) {
	for key, want := range map[string]string{
		"alert_wp_content_change": "WordPress 存量内容异常",
		"alert_wp_content_volume": "WordPress 内容修改量异常",
		"alert_wp_setting_change": "WordPress 关键设置变化",
	} {
		if got := alertLabel(key); got != want {
			t.Fatalf("alertLabel(%q)=%q, want %q", key, got, want)
		}
	}
}

func TestWPAnomalyUpgradeEstablishesEnhancedBaselineWithoutAlert(t *testing.T) {
	m, id, _ := anomalyFixture(t)
	legacyAdmin, _ := json.Marshal([]WPAnomalyAdmin{{ID: 1, Login: "user", Roles: []string{"administrator"}, EmailHash: strings.Repeat("a", 64)}})
	if _, err := m.db.Exec(`INSERT INTO site_wp_anomaly_state(site_id,enabled,threshold,last_success,admins,critical_options) VALUES(?,1,5,?,?, '{}')`, id, int64(1), string(legacyAdmin)); err != nil {
		t.Fatal(err)
	}
	sample := anomalySample(anomalyAdmin(1))
	sample.Content = []WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("1", 64)}}
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	n := 0
	m.notify = func(string, string) { n++ }
	state := anomalyCheck(t, m, id)
	if n != 0 || state.Options.SiteURL == "" || len(state.Content) != 1 || state.Admins[0].DisplayHash == "" {
		t.Fatal("upgrade baseline", n, state)
	}
}

func TestWPAnomalyContentVolumeRollingWindowAndRearm(t *testing.T) {
	m, id, now := anomalyFixture(t)
	sample := anomalySample()
	for i := 1; i <= 6; i++ {
		sample.Content = append(sample.Content, WPAnomalyContent{ID: i, Type: "post", Fingerprint: strings.Repeat("a", 64)})
	}
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	anomalyCheck(t, m, id)
	n := 0
	m.notify = func(key, _ string) {
		if key == "alert_wp_content_volume" {
			n++
		}
	}
	*now = now.Add(time.Hour)
	firstHashes := []string{"b", "c", "d", "e", "f", "0"}
	for i := range sample.Content {
		sample.Content[i].Fingerprint = strings.Repeat(firstHashes[i], 64)
	}
	state := anomalyCheck(t, m, id)
	if n != 1 || len(state.ContentChanges) != 6 || !state.ContentAlerted {
		t.Fatal(n, state.ContentChanges, state.ContentAlerted)
	}
	anomalyCheck(t, m, id)
	if n != 1 {
		t.Fatal("duplicate volume alert", n)
	}
	*now = now.Add(25 * time.Hour)
	state = anomalyCheck(t, m, id)
	if state.ContentAlerted || len(state.ContentChanges) != 0 {
		t.Fatal("rolling window did not rearm", state)
	}
	*now = now.Add(time.Hour)
	secondHashes := []string{"1", "2", "3", "4", "5", "6"}
	for i := range sample.Content {
		sample.Content[i].Fingerprint = strings.Repeat(secondHashes[i], 64)
	}
	anomalyCheck(t, m, id)
	if n != 2 {
		t.Fatal("rearmed volume alert", n)
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
	bad = anomalySample()
	bad.Content = []WPAnomalyContent{{ID: 1, Type: "product", Fingerprint: strings.Repeat("a", 64)}}
	if validateAnomalySample(bad, nil) == nil {
		t.Fatal("unsupported content accepted")
	}
	bad = anomalySample()
	bad.Options.SiteURL = ""
	if validateAnomalySample(bad, nil) == nil {
		t.Fatal("empty critical option accepted")
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
