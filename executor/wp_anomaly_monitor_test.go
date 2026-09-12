package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return &WPAnomalySample{Version: 4, Admins: append([]WPAnomalyAdmin{}, admins...), ApplicationPasswords: []WPAnomalyApplicationPassword{}, DatabaseObjects: []WPAnomalyDatabaseObject{}, Removed: []WPAnomalyRemoved{}, Content: []WPAnomalyContent{}, Options: WPAnomalyCriticalOptions{SiteURL: "https://example.com/wp", Home: "https://example.com", DefaultRole: "subscriber"}}
}
func anomalyApplicationPassword(adminID int, fingerprint, name string) WPAnomalyApplicationPassword {
	return WPAnomalyApplicationPassword{AdminID: adminID, Fingerprint: strings.Repeat(fingerprint, 64), Name: name, Created: 1700000000}
}
func anomalyDatabaseObject(kind, name, fingerprint string) WPAnomalyDatabaseObject {
	return WPAnomalyDatabaseObject{Kind: kind, Name: name, Target: "custom_posts", Action: "BEFORE UPDATE", Fingerprint: strings.Repeat(fingerprint, 64)}
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

func TestWPAnomalyApplicationPasswordBaselineAndChanges(t *testing.T) {
	m, id, now := anomalyFixture(t)
	sample := anomalySample(anomalyAdmin(1))
	sample.ApplicationPasswords = []WPAnomalyApplicationPassword{anomalyApplicationPassword(1, "a", "Existing")}
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	var notifications []string
	m.notify = func(key, _ string) { notifications = append(notifications, key) }
	state := anomalyCheck(t, m, id)
	if !state.ApplicationPasswordsInitialized || len(state.ApplicationPasswords) != 1 || !reflect.DeepEqual(notifications, []string{"alert_wp_application_password"}) {
		t.Fatal("existing credential baseline", state, notifications)
	}
	anomalyCheck(t, m, id)
	if len(notifications) != 1 {
		t.Fatal("existing credential repeated", notifications)
	}
	*now = now.Add(time.Hour)
	first := sample.ApplicationPasswords[0]
	first.LastUsed, first.LastIP = now.Unix(), "192.0.2.10"
	second := anomalyApplicationPassword(1, "b", "New")
	sample.ApplicationPasswords = []WPAnomalyApplicationPassword{first, second}
	anomalyCheck(t, m, id)
	if len(notifications) != 2 {
		t.Fatal("new and first use should aggregate", notifications)
	}
	*now = now.Add(25 * time.Hour)
	first.LastUsed, first.LastIP = now.Unix(), "198.51.100.20"
	sample.ApplicationPasswords = []WPAnomalyApplicationPassword{first}
	anomalyCheck(t, m, id)
	if len(notifications) != 3 {
		t.Fatal("IP change not notified", notifications)
	}
	var critical, info int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_application_password' AND level='critical'`).Scan(&critical); err != nil {
		t.Fatal(err)
	}
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_application_password' AND level='info'`).Scan(&info); err != nil {
		t.Fatal(err)
	}
	if critical != 3 || info != 1 {
		t.Fatal("unexpected application password events", critical, info)
	}
}

func TestWPAnomalyApplicationPasswordRoleChangeDoesNotClaimRevocationOrAddition(t *testing.T) {
	m, id, _ := anomalyFixture(t)
	password := anomalyApplicationPassword(1, "a", "Existing")
	sample := anomalySample(anomalyAdmin(1))
	sample.ApplicationPasswords = []WPAnomalyApplicationPassword{password}
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	anomalyCheck(t, m, id)
	sample = anomalySample()
	sample.Removed = []WPAnomalyRemoved{{ID: 1}}
	anomalyCheck(t, m, id)
	sample = anomalySample(anomalyAdmin(1))
	sample.ApplicationPasswords = []WPAnomalyApplicationPassword{password}
	anomalyCheck(t, m, id)
	var critical, info int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_application_password' AND level='critical'`).Scan(&critical); err != nil {
		t.Fatal(err)
	}
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_application_password' AND level='info'`).Scan(&info); err != nil {
		t.Fatal(err)
	}
	var messages string
	if err := m.db.QueryRow(`SELECT GROUP_CONCAT(message, ' ') FROM alert_log WHERE alert_type='alert_wp_application_password'`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if critical != 2 || info != 0 || strings.Contains(messages, "新增凭据") || !strings.Contains(messages, "新增/提权管理员 ID 1 当前发现 1 个凭据") {
		t.Fatal("role change misreported application-password lifecycle", critical, info)
	}
}

func TestWPAnomalyDatabaseObjectBaselineChangesAndRotation(t *testing.T) {
	m, id, _ := anomalyFixture(t)
	sample := anomalySample()
	sample.DatabaseObjects = []WPAnomalyDatabaseObject{anomalyDatabaseObject("trigger", "wds_protect_7095", "a")}
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	state := anomalyCheck(t, m, id)
	if !state.DatabaseObjectsInitialized || len(state.DatabaseObjects) != 1 {
		t.Fatal(state)
	}
	var critical int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_database_object' AND level='critical'`).Scan(&critical); err != nil || critical != 1 {
		t.Fatal(critical, err)
	}
	anomalyCheck(t, m, id)
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_database_object'`).Scan(&critical); err != nil || critical != 1 {
		t.Fatal("duplicate baseline alert", critical, err)
	}
	sample.DatabaseObjects[0].Fingerprint = strings.Repeat("b", 64)
	anomalyCheck(t, m, id)
	var message string
	if err := m.db.QueryRow(`SELECT message FROM alert_log WHERE alert_type='alert_wp_database_object' AND level='critical' ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil || !strings.Contains(message, "指纹基线整体变化") {
		t.Fatal(message, err)
	}
	sample.DatabaseObjects = []WPAnomalyDatabaseObject{{Kind: "event", Name: "restore_spam", Action: "RECURRING", Status: "ENABLED", Fingerprint: strings.Repeat("c", 64)}, sample.DatabaseObjects[0]}
	anomalyCheck(t, m, id)
	sample.DatabaseObjects[0].Status = "DISABLED"
	anomalyCheck(t, m, id)
	if err := m.db.QueryRow(`SELECT message FROM alert_log WHERE alert_type='alert_wp_database_object' AND level='critical' ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil || !strings.Contains(message, "修改定时事件") {
		t.Fatal(message, err)
	}
	sample.DatabaseObjects = sample.DatabaseObjects[:1]
	anomalyCheck(t, m, id)
	var info int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_database_object' AND level='info'`).Scan(&info); err != nil || info != 1 {
		t.Fatal(info, err)
	}
}

func TestValidateWPAnomalyDatabaseObjects(t *testing.T) {
	good := anomalySample()
	good.DatabaseObjects = []WPAnomalyDatabaseObject{
		{Kind: "event", Name: "scheduled", Action: "RECURRING", Status: "ENABLED", Fingerprint: strings.Repeat("a", 64)},
		anomalyDatabaseObject("trigger", "protect", "b"),
	}
	if err := validateAnomalySample(good, nil); err != nil {
		t.Fatal(err)
	}
	bad := *good
	bad.DatabaseObjects = append([]WPAnomalyDatabaseObject{}, good.DatabaseObjects...)
	bad.DatabaseObjects[1].Fingerprint = "SET NEW.post_content='secret'"
	if validateAnomalySample(&bad, nil) == nil {
		t.Fatal("raw definition accepted as fingerprint")
	}
	bad = *good
	bad.DatabaseObjects = []WPAnomalyDatabaseObject{good.DatabaseObjects[1], good.DatabaseObjects[0]}
	if validateAnomalySample(&bad, nil) == nil {
		t.Fatal("unsorted objects accepted")
	}
	exact := anomalySample()
	for i := 0; i < 100; i++ {
		exact.DatabaseObjects = append(exact.DatabaseObjects, WPAnomalyDatabaseObject{Kind: "event", Name: fmt.Sprintf("event_%03d", i), Action: "RECURRING", Status: "ENABLED", Fingerprint: strings.Repeat("a", 64)})
	}
	if err := validateAnomalySample(exact, nil); err != nil {
		t.Fatal("exact object limit rejected", err)
	}
	bad = *exact
	bad.DatabaseObjects = append(append([]WPAnomalyDatabaseObject{}, exact.DatabaseObjects...), WPAnomalyDatabaseObject{})
	if validateAnomalySample(&bad, nil) == nil {
		t.Fatal("over-limit objects accepted")
	}
}

func TestWPAnomalyApplicationPasswordFingerprintRotationAndMessageLimit(t *testing.T) {
	admins := []WPAnomalyAdmin{anomalyAdmin(1)}
	previous := []WPAnomalyApplicationPassword{anomalyApplicationPassword(1, "a", "One"), anomalyApplicationPassword(1, "b", "Two")}
	current := []WPAnomalyApplicationPassword{anomalyApplicationPassword(1, "c", "One"), anomalyApplicationPassword(1, "d", "Two")}
	current[0].LastUsed = 1700000100
	current[0].LastIP = "192.0.2.10"
	current[1].Name = "Renamed"
	critical, revoked := anomalyApplicationPasswordChanges(admins, admins, previous, current, true)
	if len(critical) != 1 || !strings.Contains(critical[0], "指纹基线整体变化") || !strings.Contains(critical[0], "也可能同时包含") || len(revoked) != 0 {
		t.Fatal(critical, revoked)
	}
	changes := make([]string, 25)
	for i := range changes {
		changes[i] = "change"
	}
	message := summarizeAnomalyChanges(changes, 20)
	if strings.Count(message, "change") != 20 || !strings.Contains(message, "另有 5 条（共 25 条）") {
		t.Fatal(message)
	}
}

func TestValidateWPAnomalyApplicationPasswords(t *testing.T) {
	valid := func() *WPAnomalySample {
		sample := anomalySample(anomalyAdmin(1))
		sample.ApplicationPasswords = []WPAnomalyApplicationPassword{anomalyApplicationPassword(1, "a", "Valid")}
		return sample
	}
	tests := map[string]func(*WPAnomalySample){
		"short fingerprint": func(sample *WPAnomalySample) { sample.ApplicationPasswords[0].Fingerprint = "abc" },
		"unknown admin":     func(sample *WPAnomalySample) { sample.ApplicationPasswords[0].AdminID = 2 },
		"empty name":        func(sample *WPAnomalySample) { sample.ApplicationPasswords[0].Name = "" },
		"ip without use":    func(sample *WPAnomalySample) { sample.ApplicationPasswords[0].LastIP = "192.0.2.1" },
		"duplicate": func(sample *WPAnomalySample) {
			sample.ApplicationPasswords = append(sample.ApplicationPasswords, sample.ApplicationPasswords[0])
		},
		"unsorted": func(sample *WPAnomalySample) {
			sample.Admins = append(sample.Admins, anomalyAdmin(2))
			sample.ApplicationPasswords = append([]WPAnomalyApplicationPassword{anomalyApplicationPassword(2, "b", "Later")}, sample.ApplicationPasswords...)
		},
		"over limit": func(sample *WPAnomalySample) {
			sample.ApplicationPasswords = make([]WPAnomalyApplicationPassword, 1001)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			sample := valid()
			mutate(sample)
			if err := validateAnomalySample(sample, nil); !errors.Is(err, ErrWPAnomalyInvalid) {
				t.Fatal(err)
			}
		})
	}
}

func TestWPAnomalySampleErrorsRemainDistinct(t *testing.T) {
	for _, test := range []struct {
		name   string
		sample *WPAnomalySample
		err    error
		want   string
	}{
		{"version three", &WPAnomalySample{Version: 3}, nil, "plugin_required"},
		{"malformed", &WPAnomalySample{Error: "sample_malformed"}, nil, "sample_malformed"},
		{"plugin capacity", &WPAnomalySample{Error: "sample_too_large"}, nil, "sample_too_large"},
		{"protocol capacity", nil, runError(WPInventoryProtocolLimitExceeded, WPInventoryStageProtocol, 0, false, errors.New("fixture")), "sample_too_large"},
		{"inventory capacity", nil, runError(WPInventoryInventoryLimitExceeded, WPInventoryStageProtocol, 0, false, errors.New("fixture")), "sample_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, id, _ := anomalyFixture(t)
			m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) {
				return test.sample, test.err
			}
			if err := m.Configure(id, true, 5); err != nil {
				t.Fatal(err)
			}
			state, err := m.Check(context.Background(), id)
			if err != nil || state.LastError != test.want || state.LastSuccess != 0 {
				t.Fatal(state, err)
			}
		})
	}
}

func TestWPAnomalySampleFailurePreservesSuccessfulBaseline(t *testing.T) {
	for _, code := range []string{"sample_malformed", "sample_too_large"} {
		t.Run(code, func(t *testing.T) {
			m, id, _ := anomalyFixture(t)
			sample := anomalySample(anomalyAdmin(1))
			sample.ApplicationPasswords = []WPAnomalyApplicationPassword{anomalyApplicationPassword(1, "a", "Existing")}
			sample.DatabaseObjects = []WPAnomalyDatabaseObject{anomalyDatabaseObject("trigger", "existing", "a")}
			m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
			if err := m.Configure(id, true, 5); err != nil {
				t.Fatal(err)
			}
			before := anomalyCheck(t, m, id)
			sample = &WPAnomalySample{Error: code}
			after, err := m.Check(context.Background(), id)
			if err != nil || after.LastError != code || after.LastSuccess != before.LastSuccess || !reflect.DeepEqual(after.Admins, before.Admins) || !reflect.DeepEqual(after.ApplicationPasswords, before.ApplicationPasswords) || !reflect.DeepEqual(after.DatabaseObjects, before.DatabaseObjects) {
				t.Fatal(after, err)
			}
		})
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

func TestWPAnomalyHomepageChangesMergeUntilSixHoursQuiet(t *testing.T) {
	m, id, now := anomalyFixture(t)
	sample := anomalySample(anomalyAdmin(1))
	sample.Content = []WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("1", 64)}}
	sample.Options.FrontPageID = 10
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	anomalyCheck(t, m, id)
	var notifications []string
	m.notify = func(_ string, message string) { notifications = append(notifications, message) }

	firstAt := now.Add(time.Hour).Unix()
	for i, step := range []time.Duration{time.Hour, 5 * time.Hour, 5 * time.Hour} {
		*now = now.Add(step)
		fingerprint := string(rune('2' + i))
		sample.Content[0].Fingerprint = strings.Repeat(fingerprint, 64)
		state := anomalyCheck(t, m, id)
		if len(state.ContentChanges) != 1 || state.ContentChanges[0].HomepageChangeCount != i+1 || state.ContentChanges[0].HomepageFirstAt != firstAt || state.ContentChanges[0].DetectedAt != now.Unix() {
			t.Fatalf("merged state after change %d: %+v", i+1, state.ContentChanges)
		}
	}
	if len(notifications) != 1 || !strings.Contains(notifications[0], "首页内容发生变化") {
		t.Fatalf("notifications=%q", notifications)
	}
	var critical, info int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_content_change' AND level='critical'`).Scan(&critical); err != nil {
		t.Fatal(err)
	}
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_content_change' AND level='info'`).Scan(&info); err != nil {
		t.Fatal(err)
	}
	if critical != 1 || info != 2 {
		t.Fatalf("history critical=%d info=%d", critical, info)
	}

	*now = now.Add(6 * time.Hour)
	sample.Content[0].Fingerprint = strings.Repeat("5", 64)
	state := anomalyCheck(t, m, id)
	if len(notifications) != 2 || state.ContentChanges[0].HomepageChangeCount != 1 || state.ContentChanges[0].HomepageFirstAt != now.Unix() {
		t.Fatalf("new episode notifications=%q state=%+v", notifications, state.ContentChanges)
	}
}

func TestWPAnomalyHomepageChangeTransactionFailureKeepsEpisode(t *testing.T) {
	m, id, now := anomalyFixture(t)
	sample := anomalySample(anomalyAdmin(1))
	sample.Content = []WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("1", 64)}}
	sample.Options.FrontPageID = 10
	m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
	if err := m.Configure(id, true, 5); err != nil {
		t.Fatal(err)
	}
	anomalyCheck(t, m, id)
	var notifications int
	m.notify = func(string, string) { notifications++ }
	*now = now.Add(time.Hour)
	sample.Content[0].Fingerprint = strings.Repeat("2", 64)
	before := anomalyCheck(t, m, id)
	if notifications != 1 {
		t.Fatalf("notifications=%d", notifications)
	}
	if _, err := m.db.Exec(`CREATE TRIGGER fail_anomaly_event BEFORE INSERT ON alert_log BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	sample.Content[0].Fingerprint = strings.Repeat("3", 64)
	if _, err := m.Check(context.Background(), id); err == nil {
		t.Fatal("transaction failure hidden")
	}
	after, err := m.Status(id)
	if err != nil || !reflect.DeepEqual(after.ContentChanges, before.ContentChanges) {
		t.Fatalf("episode advanced after failed history insert: before=%+v after=%+v err=%v", before.ContentChanges, after.ContentChanges, err)
	}
	var info int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE alert_type='alert_wp_content_change' AND level='info'`).Scan(&info); err != nil || info != 0 {
		t.Fatalf("info=%d err=%v", info, err)
	}
	if notifications != 1 {
		t.Fatalf("failed transaction sent notification: %d", notifications)
	}
}

func TestWPAnomalyHomepageChangeFromLegacyStateStartsNewEpisode(t *testing.T) {
	var legacy []WPAnomalyContentChange
	if err := json.Unmarshal([]byte(`[{"id":10,"detected_at":1799996400}]`), &legacy); err != nil {
		t.Fatal(err)
	}
	result, messages, homepage := anomalyContentChanges(
		[]WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("1", 64)}},
		[]WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("2", 64)}},
		10,
		10,
		legacy,
		1800000000,
	)
	if len(messages) != 0 || !homepage.Changed || !homepage.Notify || homepage.Count != 1 || homepage.FirstAt != 1800000000 {
		t.Fatalf("messages=%q homepage=%+v", messages, homepage)
	}
	if len(result) != 1 || result[0].HomepageChangeCount != 1 || result[0].HomepageFirstAt != 1800000000 {
		t.Fatalf("result=%+v", result)
	}
}

func TestWPAnomalyHomepageHighRiskChangesBypassMerge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*WPAnomalySample)
		want   string
	}{
		{"front page switched", func(sample *WPAnomalySample) {
			sample.Content = append(sample.Content, WPAnomalyContent{ID: 20, Type: "page", Fingerprint: strings.Repeat("2", 64)})
			sample.Options.FrontPageID = 20
		}, "首页显示由“页面 10”改为“页面 20”"},
		{"front page switched to posts", func(sample *WPAnomalySample) {
			sample.Options.FrontPageID = 0
		}, "首页显示由“页面 10”改为“最新文章”"},
		{"front page unpublished", func(sample *WPAnomalySample) { sample.Content = []WPAnomalyContent{} }, "原静态首页"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, id, now := anomalyFixture(t)
			sample := anomalySample(anomalyAdmin(1))
			sample.Content = []WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("1", 64)}}
			sample.Options.FrontPageID = 10
			m.collect = func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error) { return sample, nil }
			if err := m.Configure(id, true, 5); err != nil {
				t.Fatal(err)
			}
			anomalyCheck(t, m, id)
			*now = now.Add(time.Hour)
			sample.Content[0].Fingerprint = strings.Repeat("3", 64)
			anomalyCheck(t, m, id)
			var notifications []string
			m.notify = func(_ string, message string) { notifications = append(notifications, message) }
			*now = now.Add(time.Hour)
			tc.mutate(sample)
			anomalyCheck(t, m, id)
			if len(notifications) != 1 || !strings.Contains(notifications[0], tc.want) {
				t.Fatalf("notifications=%q", notifications)
			}
			if tc.name == "front page unpublished" && strings.Count(notifications[0], "取消发布") != 1 {
				t.Fatalf("homepage removal described more than once: %q", notifications[0])
			}
		})
	}
}

func TestWPAnomalyHomepageTargetLabels(t *testing.T) {
	content := []WPAnomalyContent{{ID: 10, Type: "page", Fingerprint: strings.Repeat("1", 64)}}
	_, _, homepage := anomalyContentChanges(content, content, 0, 10, nil, 1800000000)
	if homepage.Reason != "首页显示由“最新文章”改为“页面 10”" {
		t.Fatalf("reason=%q", homepage.Reason)
	}
	_, _, homepage = anomalyContentChanges(content, content, 10, 0, nil, 1800000000)
	if homepage.Reason != "首页显示由“页面 10”改为“最新文章”" {
		t.Fatalf("reason=%q", homepage.Reason)
	}
}

func TestWPAnomalyAlertLabels(t *testing.T) {
	for key, want := range map[string]string{
		"alert_wp_content_change":       "WordPress 内容变化",
		"alert_wp_content_volume":       "WordPress 内容修改量异常",
		"alert_wp_setting_change":       "WordPress 关键设置变化",
		"alert_wp_application_password": "WordPress 应用程序密码异常",
		"alert_wp_database_object":      "WordPress 数据库持久化异常",
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
