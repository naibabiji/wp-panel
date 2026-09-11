package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

type WPAnomalyAdmin struct {
	ID        int      `json:"id"`
	Login     string   `json:"login"`
	Roles     []string `json:"roles"`
	EmailHash string   `json:"email_hash"`
}
type WPAnomalyRemoved struct {
	ID      int  `json:"id"`
	Deleted bool `json:"deleted"`
}
type WPAnomalySample struct {
	Admins    []WPAnomalyAdmin   `json:"admins"`
	Removed   []WPAnomalyRemoved `json:"removed"`
	PostCount int                `json:"post_count"`
	Error     string             `json:"error,omitempty"`
}
type wpAnomalyQuery struct {
	Since    int64 `json:"since"`
	Until    int64 `json:"until"`
	KnownIDs []int `json:"known_ids"`
}
type WPAnomalyState struct {
	Enabled       bool             `json:"enabled"`
	Threshold     int              `json:"threshold"`
	BaselineSince int64            `json:"baseline_since"`
	LastSuccess   int64            `json:"last_success"`
	NextCheck     int64            `json:"next_check"`
	LastError     string           `json:"last_error"`
	Admins        []WPAnomalyAdmin `json:"admins"`
	PostCount     int              `json:"post_count"`
	PostAlerted   bool             `json:"-"`
}
type WPAnomalyMonitor struct {
	db      *sql.DB
	cfg     *config.Config
	mu      sync.Mutex // Bounded synchronous checks; reject overlap instead of queuing jobs.
	now     func() time.Time
	collect func(context.Context, *models.Website, wpAnomalyQuery) (*WPAnomalySample, error)
	notify  func(string, string)
}

var anomalyDefault struct {
	sync.Mutex
	monitor *WPAnomalyMonitor
}

func DefaultWPAnomalyMonitor(cfg *config.Config) *WPAnomalyMonitor {
	anomalyDefault.Lock()
	defer anomalyDefault.Unlock()
	if anomalyDefault.monitor == nil || anomalyDefault.monitor.db != database.GetDB() {
		anomalyDefault.monitor = NewWPAnomalyMonitor(database.GetDB(), cfg)
	}
	return anomalyDefault.monitor
}

func NewWPAnomalyMonitor(db *sql.DB, cfg *config.Config) *WPAnomalyMonitor {
	m := &WPAnomalyMonitor{db: db, cfg: cfg, now: time.Now}
	m.collect = func(ctx context.Context, site *models.Website, query wpAnomalyQuery) (*WPAnomalySample, error) {
		runner, err := NewWPInventoryRunner()
		if err != nil {
			return nil, err
		}
		runner.anomalyQuery = &query
		result, err := runner.Collect(ctx, cfg, site, false)
		if err != nil {
			return nil, err
		}
		return result.Inventory.Anomaly, nil
	}
	m.notify = func(key, message string) {
		smtp := GetSMTPConfig()
		deliverAlertNotification(key, message, false, smtp != nil && smtp.Host != "" && smtp.AdminEmail != "", webhookConfigured(GetWebhookConfig()))
	}
	return m
}

func (m *WPAnomalyMonitor) site(id int) (*models.Website, error) {
	site := &models.Website{}
	err := m.db.QueryRow(`SELECT id,domain,web_root,system_user,site_type,status FROM websites WHERE id=? AND site_type='wordpress'`, id).
		Scan(&site.ID, &site.Domain, &site.WebRoot, &site.SystemUser, &site.SiteType, &site.Status)
	return site, err
}

func (m *WPAnomalyMonitor) Status(id int) (WPAnomalyState, error) {
	state := WPAnomalyState{Threshold: 5, Admins: []WPAnomalyAdmin{}}
	if _, err := m.site(id); err != nil {
		return state, err
	}
	var admins string
	err := m.db.QueryRow(`SELECT enabled,threshold,baseline_since,last_success,next_check,last_error,admins,post_count,post_alerted FROM site_wp_anomaly_state WHERE site_id=?`, id).
		Scan(&state.Enabled, &state.Threshold, &state.BaselineSince, &state.LastSuccess, &state.NextCheck, &state.LastError, &admins, &state.PostCount, &state.PostAlerted)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(admins), &state.Admins); err != nil {
		return state, err
	}
	return state, nil
}

var ErrWPAnomalyBusy = errors.New("anomaly_busy")
var ErrWPAnomalyInvalid = errors.New("anomaly_invalid")
var ErrWPAnomalyDisabled = errors.New("anomaly_disabled")

func (m *WPAnomalyMonitor) Configure(id int, enabled bool, threshold int) error {
	if threshold < 1 || threshold > 10000 {
		return ErrWPAnomalyInvalid
	}
	if !m.mu.TryLock() {
		return ErrWPAnomalyBusy
	}
	defer m.mu.Unlock()
	if _, err := m.site(id); err != nil {
		return err
	}
	// Re-enabling starts a fresh observation period; editing only the threshold
	// does not discard administrator changes or existing publication deduplication.
	_, err := m.db.Exec(`INSERT INTO site_wp_anomaly_state(site_id,enabled,threshold) VALUES(?,?,?)
 ON CONFLICT(site_id) DO UPDATE SET enabled=excluded.enabled,threshold=excluded.threshold,
 baseline_since=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN 0 ELSE baseline_since END,
 last_success=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN 0 ELSE last_success END,
 admins=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN '[]' ELSE admins END,
 post_count=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN 0 ELSE post_count END,
 post_alerted=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN 0 ELSE post_alerted END,
 next_check=0,last_error=''`, id, enabled, threshold)
	return err
}

func (m *WPAnomalyMonitor) Check(ctx context.Context, id int) (WPAnomalyState, error) {
	if !m.mu.TryLock() {
		return WPAnomalyState{}, ErrWPAnomalyBusy
	}
	defer m.mu.Unlock()
	return m.check(ctx, id)
}

func (m *WPAnomalyMonitor) check(ctx context.Context, id int) (WPAnomalyState, error) {
	state, err := m.Status(id)
	if err != nil {
		return state, err
	}
	if !state.Enabled {
		return state, ErrWPAnomalyDisabled
	}
	site, err := m.site(id)
	if err != nil {
		return state, err
	}
	fail := func(code string) (WPAnomalyState, error) {
		_, err := m.db.Exec(`UPDATE site_wp_anomaly_state SET last_error=?,next_check=? WHERE site_id=?`, code, m.now().Unix()+3600, id)
		state.LastError = code
		state.NextCheck = m.now().Unix() + 3600
		return state, err // A completed failed check is visible in the returned state.
	}
	if site.Status != models.StatusActive {
		return fail("site_unavailable")
	}
	var migration bool
	if err := m.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM site_migration_locks WHERE (site_id=? OR domain=?) AND status='active')`, id, site.Domain).Scan(&migration); err != nil {
		return fail("sample_failed")
	}
	if migration || !TryAcquireSiteOpLock(id, "wp_anomaly_check") {
		return fail("site_busy")
	}
	defer ReleaseSiteOpLock(id)
	// Resolve again under the shared site lock, in case a rename/reinstall
	// finished between the earlier lookup and acquiring the lock.
	site, err = m.site(id)
	if err != nil {
		return state, err
	}
	if site.Status != models.StatusActive {
		return fail("site_unavailable")
	}
	now := m.now().Unix()
	query := wpAnomalyQuery{Since: state.BaselineSince, Until: now, KnownIDs: []int{}}
	if state.LastSuccess == 0 {
		query.Since = now
	} else {
		for _, admin := range state.Admins {
			query.KnownIDs = append(query.KnownIDs, admin.ID)
		}
	}
	sample, err := m.collect(ctx, site, query)
	if err != nil || sample == nil {
		return fail("sample_failed")
	}
	if sample.Error != "" {
		switch sample.Error {
		case "plugin_required", "multisite_unsupported":
			return fail(sample.Error)
		default:
			return fail("sample_failed")
		}
	}
	if err := validateAnomalySample(sample, query.KnownIDs); err != nil {
		return fail("sample_failed")
	}
	messages := []string{}
	if state.LastSuccess != 0 {
		messages = anomalyAdminChanges(state.Admins, sample)
	}
	if state.LastSuccess == 0 {
		state.BaselineSince = now
		state.PostAlerted = false
	}
	above := sample.PostCount > state.Threshold
	postAlert := state.LastSuccess != 0 && above && !state.PostAlerted
	state.PostAlerted = above
	state.Admins, state.PostCount = sample.Admins, sample.PostCount
	state.LastSuccess, state.NextCheck, state.LastError = now, m.now().Unix()+3600, ""
	admins, _ := json.Marshal(state.Admins)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE site_wp_anomaly_state SET baseline_since=?,last_success=?,next_check=?,last_error='',admins=?,post_count=?,post_alerted=? WHERE site_id=?`,
		state.BaselineSince, state.LastSuccess, state.NextCheck, string(admins), state.PostCount, state.PostAlerted, id); err != nil {
		return state, err
	}
	type event struct{ key, message string }
	events := []event{}
	if len(messages) > 0 {
		events = append(events, event{"alert_wp_admin_change", fmt.Sprintf("%s 管理员变化：%s。请在 WordPress 用户页面核查。", site.Domain, strings.Join(messages, "；"))})
	}
	if postAlert {
		events = append(events, event{"alert_wp_post_volume", fmt.Sprintf("%s 最近 24 小时（不早于本轮监控起点）发布普通文章 %d 篇，超过阈值 %d。请核查是否为正常发布或导入。", site.Domain, state.PostCount, state.Threshold)})
	}
	for _, event := range events {
		if _, err = tx.Exec(`INSERT INTO alert_log(alert_type,level,message,resolved) VALUES(?,'critical',?,1)`, event.key, event.message); err != nil {
			return state, err
		}
	}
	if len(events) > 0 {
		if _, err = tx.Exec(`DELETE FROM alert_log WHERE created_at < datetime('now','-90 days')`); err != nil {
			return state, err
		}
	}
	if err = tx.Commit(); err != nil {
		return state, err
	}
	// Durable comparison and event first. Notification failure never rewinds the baseline.
	for _, event := range events {
		m.notify(event.key, event.message)
	}
	return state, nil
}

func validateAnomalySample(sample *WPAnomalySample, known []int) error {
	if sample.Admins == nil || sample.Removed == nil || len(sample.Admins) > 100 || len(sample.Removed) > 100 || sample.PostCount < 0 || sample.PostCount > 1000000000 {
		return ErrWPAnomalyInvalid
	}
	seen := map[int]bool{}
	for _, admin := range sample.Admins {
		if admin.ID < 1 || seen[admin.ID] || admin.Login == "" || validateShort(admin.Login, 240) != nil || !wpInventoryHashPattern.MatchString(admin.EmailHash) || len(admin.Roles) > 20 || !sort.StringsAreSorted(admin.Roles) || hasAdjacentDuplicate(admin.Roles) {
			return ErrWPAnomalyInvalid
		}
		hasAdmin := false
		for _, role := range admin.Roles {
			if validateShort(role, 64) != nil {
				return ErrWPAnomalyInvalid
			}
			if role == "administrator" {
				hasAdmin = true
			}
		}
		if !hasAdmin {
			return ErrWPAnomalyInvalid
		}
		seen[admin.ID] = true
	}
	missing := map[int]bool{}
	for _, id := range known {
		if !seen[id] {
			missing[id] = true
		}
	}
	for _, removed := range sample.Removed {
		if !missing[removed.ID] {
			return ErrWPAnomalyInvalid
		}
		delete(missing, removed.ID)
	}
	if len(missing) != 0 {
		return ErrWPAnomalyInvalid
	}
	sort.Slice(sample.Admins, func(i, j int) bool { return sample.Admins[i].ID < sample.Admins[j].ID })
	return nil
}

func anomalyAdminChanges(previous []WPAnomalyAdmin, sample *WPAnomalySample) []string {
	old := map[int]WPAnomalyAdmin{}
	for _, admin := range previous {
		old[admin.ID] = admin
	}
	changes := []string{}
	for _, admin := range sample.Admins {
		before, exists := old[admin.ID]
		if !exists {
			changes = append(changes, fmt.Sprintf("新增/提权 ID %d (%q)", admin.ID, admin.Login))
		} else if !reflect.DeepEqual(before, admin) {
			changes = append(changes, fmt.Sprintf("资料/角色变更 ID %d (%q)", admin.ID, admin.Login))
		}
	}
	for _, removed := range sample.Removed {
		action := "降权"
		if removed.Deleted {
			action = "删除"
		}
		changes = append(changes, fmt.Sprintf("%s ID %d (%q)", action, removed.ID, old[removed.ID].Login))
	}
	return changes
}

// Called by the existing alert tick, with no separate scheduler or persistent jobs.
func runWPAnomalyChecks() {
	anomalyDefault.Lock()
	m := anomalyDefault.monitor
	anomalyDefault.Unlock()
	if m == nil || !m.mu.TryLock() {
		return
	}
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT a.site_id FROM site_wp_anomaly_state a JOIN websites w ON w.id=a.site_id WHERE a.enabled=1 AND a.next_check<=? AND w.status='active' AND w.site_type='wordpress' ORDER BY a.next_check,a.site_id LIMIT 10`, m.now().Unix())
	if err != nil {
		return
	}
	ids := []int{}
	for rows.Next() {
		var id int
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	scanErr := rows.Err()
	rows.Close()
	if scanErr != nil {
		return
	}
	for _, id := range ids {
		_, _ = m.check(context.Background(), id)
	}
}
