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
	ID             int      `json:"id"`
	Login          string   `json:"login"`
	Roles          []string `json:"roles"`
	EmailHash      string   `json:"email_hash"`
	DisplayHash    string   `json:"display_hash"`
	CredentialHash string   `json:"credential_hash"`
}
type WPAnomalyRemoved struct {
	ID      int  `json:"id"`
	Deleted bool `json:"deleted"`
}
type WPAnomalySample struct {
	Version              int                            `json:"version"`
	Admins               []WPAnomalyAdmin               `json:"admins"`
	ApplicationPasswords []WPAnomalyApplicationPassword `json:"application_passwords"`
	DatabaseObjects      []WPAnomalyDatabaseObject      `json:"database_objects"`
	Removed              []WPAnomalyRemoved             `json:"removed"`
	PostCount            int                            `json:"post_count"`
	Content              []WPAnomalyContent             `json:"content"`
	Options              WPAnomalyCriticalOptions       `json:"options"`
	Error                string                         `json:"error,omitempty"`
}
type WPAnomalyApplicationPassword struct {
	AdminID     int    `json:"admin_id"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
	HasAppID    bool   `json:"has_app_id"`
	Created     int64  `json:"created"`
	LastUsed    int64  `json:"last_used"`
	LastIP      string `json:"last_ip"`
}
type WPAnomalyDatabaseObject struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	Fingerprint string `json:"fingerprint"`
}
type WPAnomalyContent struct {
	ID          int    `json:"id"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
}
type WPAnomalyCriticalOptions struct {
	SiteURL          string `json:"siteurl"`
	Home             string `json:"home"`
	UsersCanRegister bool   `json:"users_can_register"`
	DefaultRole      string `json:"default_role"`
	FrontPageID      int    `json:"front_page_id"`
}
type WPAnomalyContentChange struct {
	ID         int   `json:"id"`
	DetectedAt int64 `json:"detected_at"`
}
type wpAnomalyQuery struct {
	Since    int64 `json:"since"`
	Until    int64 `json:"until"`
	KnownIDs []int `json:"known_ids"`
}
type WPAnomalyState struct {
	Enabled                         bool                           `json:"enabled"`
	Threshold                       int                            `json:"threshold"`
	BaselineSince                   int64                          `json:"baseline_since"`
	LastSuccess                     int64                          `json:"last_success"`
	NextCheck                       int64                          `json:"next_check"`
	LastError                       string                         `json:"last_error"`
	Admins                          []WPAnomalyAdmin               `json:"admins"`
	PostCount                       int                            `json:"post_count"`
	PostAlerted                     bool                           `json:"-"`
	Content                         []WPAnomalyContent             `json:"-"`
	Options                         WPAnomalyCriticalOptions       `json:"-"`
	ContentChanges                  []WPAnomalyContentChange       `json:"-"`
	ContentAlerted                  bool                           `json:"-"`
	ApplicationPasswords            []WPAnomalyApplicationPassword `json:"-"`
	ApplicationPasswordsInitialized bool                           `json:"-"`
	DatabaseObjects                 []WPAnomalyDatabaseObject      `json:"-"`
	DatabaseObjectsInitialized      bool                           `json:"-"`
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
	state := WPAnomalyState{Threshold: 5, Admins: []WPAnomalyAdmin{}, Content: []WPAnomalyContent{}, ContentChanges: []WPAnomalyContentChange{}, ApplicationPasswords: []WPAnomalyApplicationPassword{}, DatabaseObjects: []WPAnomalyDatabaseObject{}}
	if _, err := m.site(id); err != nil {
		return state, err
	}
	var admins, content, options, contentChanges, applicationPasswords, databaseObjects string
	err := m.db.QueryRow(`SELECT enabled,threshold,baseline_since,last_success,next_check,last_error,admins,post_count,post_alerted,content_items,critical_options,content_changes,content_alerted,application_passwords,database_objects FROM site_wp_anomaly_state WHERE site_id=?`, id).
		Scan(&state.Enabled, &state.Threshold, &state.BaselineSince, &state.LastSuccess, &state.NextCheck, &state.LastError, &admins, &state.PostCount, &state.PostAlerted, &content, &options, &contentChanges, &state.ContentAlerted, &applicationPasswords, &databaseObjects)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(admins), &state.Admins); err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(content), &state.Content); err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(options), &state.Options); err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(contentChanges), &state.ContentChanges); err != nil {
		return state, err
	}
	if applicationPasswords != "" {
		if err = json.Unmarshal([]byte(applicationPasswords), &state.ApplicationPasswords); err != nil {
			return state, err
		}
		state.ApplicationPasswordsInitialized = true
	}
	if databaseObjects != "" {
		if err = json.Unmarshal([]byte(databaseObjects), &state.DatabaseObjects); err != nil {
			return state, err
		}
		state.DatabaseObjectsInitialized = true
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
	content_items=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN '[]' ELSE content_items END,
	critical_options=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN '{}' ELSE critical_options END,
	content_changes=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN '[]' ELSE content_changes END,
	content_alerted=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN 0 ELSE content_alerted END,
	application_passwords=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN '' ELSE application_passwords END,
	database_objects=CASE WHEN site_wp_anomaly_state.enabled=0 AND excluded.enabled=1 THEN '' ELSE database_objects END,
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
	if err != nil {
		var runnerError *WPInventoryRunError
		if errors.As(err, &runnerError) && (runnerError.Code == WPInventoryProtocolLimitExceeded || runnerError.Code == WPInventoryInventoryLimitExceeded) {
			return fail("sample_too_large")
		}
		return fail("sample_failed")
	}
	if sample == nil {
		return fail("sample_failed")
	}
	if sample.Error != "" {
		switch sample.Error {
		case "plugin_required", "multisite_unsupported", "sample_malformed", "sample_too_large":
			return fail(sample.Error)
		default:
			return fail("sample_failed")
		}
	}
	if sample.Version != 4 {
		return fail("plugin_required")
	}
	if err := validateAnomalySample(sample, query.KnownIDs); err != nil {
		return fail("sample_malformed")
	}
	messages := []string{}
	if state.LastSuccess != 0 {
		messages = anomalyAdminChanges(state.Admins, sample)
	}
	applicationPasswordMessages, revokedApplicationPasswords := anomalyApplicationPasswordChanges(state.Admins, sample.Admins, state.ApplicationPasswords, sample.ApplicationPasswords, state.ApplicationPasswordsInitialized)
	databaseObjectMessages, removedDatabaseObjects := anomalyDatabaseObjectChanges(state.DatabaseObjects, sample.DatabaseObjects, state.DatabaseObjectsInitialized)
	contentMessages := []string{}
	optionMessages := []string{}
	// Rows created by schema 1.0.59 have no content/options baseline. Their first
	// 1.0.60 sample extends the baseline without reporting existing site state.
	if state.LastSuccess != 0 && state.Options.SiteURL != "" {
		state.ContentChanges, contentMessages = anomalyContentChanges(state.Content, sample.Content, state.Options.FrontPageID, sample.Options.FrontPageID, state.ContentChanges, now)
		optionMessages = anomalyOptionChanges(state.Options, sample.Options)
	} else {
		state.ContentChanges = []WPAnomalyContentChange{}
		state.ContentAlerted = false
	}
	if state.LastSuccess == 0 {
		state.BaselineSince = now
		state.PostAlerted = false
		state.ContentChanges = []WPAnomalyContentChange{}
		state.ContentAlerted = false
	}
	above := sample.PostCount > state.Threshold
	postAlert := state.LastSuccess != 0 && above && !state.PostAlerted
	state.PostAlerted = above
	contentAbove := len(state.ContentChanges) > state.Threshold
	contentVolumeAlert := state.LastSuccess != 0 && contentAbove && !state.ContentAlerted
	state.ContentAlerted = contentAbove
	state.Admins, state.PostCount = sample.Admins, sample.PostCount
	state.ApplicationPasswords = sample.ApplicationPasswords
	state.ApplicationPasswordsInitialized = true
	state.DatabaseObjects = sample.DatabaseObjects
	state.DatabaseObjectsInitialized = true
	state.Content, state.Options = sample.Content, sample.Options
	state.LastSuccess, state.NextCheck, state.LastError = now, m.now().Unix()+3600, ""
	admins, _ := json.Marshal(state.Admins)
	content, _ := json.Marshal(state.Content)
	options, _ := json.Marshal(state.Options)
	contentChanges, _ := json.Marshal(state.ContentChanges)
	applicationPasswords, _ := json.Marshal(state.ApplicationPasswords)
	databaseObjects, _ := json.Marshal(state.DatabaseObjects)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE site_wp_anomaly_state SET baseline_since=?,last_success=?,next_check=?,last_error='',admins=?,post_count=?,post_alerted=?,content_items=?,critical_options=?,content_changes=?,content_alerted=?,application_passwords=?,database_objects=? WHERE site_id=?`,
		state.BaselineSince, state.LastSuccess, state.NextCheck, string(admins), state.PostCount, state.PostAlerted, string(content), string(options), string(contentChanges), state.ContentAlerted, string(applicationPasswords), string(databaseObjects), id); err != nil {
		return state, err
	}
	type event struct{ key, message string }
	events := []event{}
	if len(messages) > 0 {
		events = append(events, event{"alert_wp_admin_change", fmt.Sprintf("%s 管理员变化：%s。请在 WordPress 用户页面核查。", site.Domain, strings.Join(messages, "；"))})
	}
	if postAlert {
		events = append(events, event{"alert_wp_post_volume", fmt.Sprintf("%s 最近 24 小时（不早于本轮监控起点）发布文章或页面 %d 篇，超过阈值 %d。请核查是否为正常发布或导入。", site.Domain, state.PostCount, state.Threshold)})
	}
	if len(contentMessages) > 0 {
		events = append(events, event{"alert_wp_content_change", fmt.Sprintf("%s 存量内容异常：%s。请核查 WordPress 文章和页面。", site.Domain, strings.Join(contentMessages, "；"))})
	}
	if contentVolumeAlert {
		events = append(events, event{"alert_wp_content_volume", fmt.Sprintf("%s 最近 24 小时检测到 %d 篇既有文章或页面内容被修改，超过阈值 %d。请核查是否为正常批量编辑。", site.Domain, len(state.ContentChanges), state.Threshold)})
	}
	if len(optionMessages) > 0 {
		events = append(events, event{"alert_wp_setting_change", fmt.Sprintf("%s WordPress 关键设置变化：%s。请核查是否为授权操作。", site.Domain, strings.Join(optionMessages, "；"))})
	}
	if len(applicationPasswordMessages) > 0 {
		events = append(events, event{"alert_wp_application_password", fmt.Sprintf("%s WordPress 应用程序密码异常：%s。请在 WordPress 用户个人资料中核查并撤销未知凭据。", site.Domain, summarizeAnomalyChanges(applicationPasswordMessages, 20))})
	}
	if len(databaseObjectMessages) > 0 {
		events = append(events, event{"alert_wp_database_object", fmt.Sprintf("%s WordPress 数据库持久化对象异常：%s。请核查是否为授权创建或修改。", site.Domain, summarizeAnomalyChanges(databaseObjectMessages, 20))})
	}
	for _, event := range events {
		if _, err = tx.Exec(`INSERT INTO alert_log(alert_type,level,message,resolved) VALUES(?,'critical',?,1)`, event.key, event.message); err != nil {
			return state, err
		}
	}
	if len(revokedApplicationPasswords) > 0 {
		message := fmt.Sprintf("%s WordPress 应用程序密码已撤销：%s。", site.Domain, summarizeAnomalyChanges(revokedApplicationPasswords, 20))
		if _, err = tx.Exec(`INSERT INTO alert_log(alert_type,level,message,resolved) VALUES(?,'info',?,1)`, "alert_wp_application_password", message); err != nil {
			return state, err
		}
	}
	if len(removedDatabaseObjects) > 0 {
		message := fmt.Sprintf("%s WordPress 数据库持久化对象已移除：%s。", site.Domain, summarizeAnomalyChanges(removedDatabaseObjects, 20))
		if _, err = tx.Exec(`INSERT INTO alert_log(alert_type,level,message,resolved) VALUES(?,'info',?,1)`, "alert_wp_database_object", message); err != nil {
			return state, err
		}
	}
	if len(events) > 0 || len(revokedApplicationPasswords) > 0 || len(removedDatabaseObjects) > 0 {
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
	if sample.Version != 4 || sample.Admins == nil || sample.ApplicationPasswords == nil || sample.DatabaseObjects == nil || sample.Removed == nil || sample.Content == nil || len(sample.Admins) > 100 || len(sample.ApplicationPasswords) > 1000 || len(sample.DatabaseObjects) > 100 || len(sample.Removed) > 100 || len(sample.Content) > 5000 || sample.PostCount < 0 || sample.PostCount > 1000000000 {
		return ErrWPAnomalyInvalid
	}
	seen := map[int]bool{}
	for _, admin := range sample.Admins {
		if admin.ID < 1 || seen[admin.ID] || admin.Login == "" || validateShort(admin.Login, 240) != nil || !wpInventoryHashPattern.MatchString(admin.EmailHash) || !wpInventoryHashPattern.MatchString(admin.DisplayHash) || !wpInventoryHashPattern.MatchString(admin.CredentialHash) || len(admin.Roles) > 20 || !sort.StringsAreSorted(admin.Roles) || hasAdjacentDuplicate(admin.Roles) {
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
	applicationPasswordSeen := map[string]bool{}
	previousAdminID, previousFingerprint := 0, ""
	for _, password := range sample.ApplicationPasswords {
		key := fmt.Sprintf("%d:%s", password.AdminID, password.Fingerprint)
		if !seen[password.AdminID] || applicationPasswordSeen[key] || !wpInventoryHashPattern.MatchString(password.Fingerprint) || password.Name == "" || validateShort(password.Name, 255) != nil || password.Created < 1 || password.LastUsed < 0 || validateShort(password.LastIP, 64) != nil || (password.LastUsed == 0 && password.LastIP != "") || password.AdminID < previousAdminID || (password.AdminID == previousAdminID && password.Fingerprint <= previousFingerprint) {
			return ErrWPAnomalyInvalid
		}
		applicationPasswordSeen[key] = true
		previousAdminID, previousFingerprint = password.AdminID, password.Fingerprint
	}
	previousDatabaseObjectKey := ""
	for _, object := range sample.DatabaseObjects {
		key := object.Kind + ":" + object.Name
		if (object.Kind != "trigger" && object.Kind != "event" && object.Kind != "procedure" && object.Kind != "function") || object.Name == "" || validateShort(object.Name, 256) != nil || validateShort(object.Target, 256) != nil || validateShort(object.Action, 64) != nil || validateShort(object.Status, 64) != nil || !wpInventoryHashPattern.MatchString(object.Fingerprint) || key <= previousDatabaseObjectKey {
			return ErrWPAnomalyInvalid
		}
		if object.Kind == "trigger" && (object.Target == "" || object.Action == "") {
			return ErrWPAnomalyInvalid
		}
		previousDatabaseObjectKey = key
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
	contentSeen := map[int]bool{}
	for _, item := range sample.Content {
		if item.ID < 1 || contentSeen[item.ID] || (item.Type != "post" && item.Type != "page") || !wpInventoryHashPattern.MatchString(item.Fingerprint) {
			return ErrWPAnomalyInvalid
		}
		contentSeen[item.ID] = true
	}
	if validateShort(sample.Options.SiteURL, 2048) != nil || validateShort(sample.Options.Home, 2048) != nil || validateShort(sample.Options.DefaultRole, 64) != nil || sample.Options.SiteURL == "" || sample.Options.Home == "" || sample.Options.DefaultRole == "" || sample.Options.FrontPageID < 0 {
		return ErrWPAnomalyInvalid
	}
	sort.Slice(sample.Admins, func(i, j int) bool { return sample.Admins[i].ID < sample.Admins[j].ID })
	sort.Slice(sample.Content, func(i, j int) bool { return sample.Content[i].ID < sample.Content[j].ID })
	return nil
}

func anomalyApplicationPasswordChanges(previousAdmins, currentAdmins []WPAnomalyAdmin, previous, current []WPAnomalyApplicationPassword, initialized bool) ([]string, []string) {
	if !initialized {
		if len(current) == 0 {
			return nil, nil
		}
		admins := map[int]bool{}
		for _, password := range current {
			admins[password.AdminID] = true
		}
		return []string{fmt.Sprintf("首次基线发现 %d 个存量凭据，涉及 %d 个管理员", len(current), len(admins))}, nil
	}
	if applicationPasswordFingerprintsRotated(previous, current) {
		return []string{fmt.Sprintf("%d 个凭据的指纹基线整体变化，可能由站点密钥轮换引起，也可能同时包含凭据增删或资料变化", len(current))}, nil
	}
	previousAdminIDs := make(map[int]bool, len(previousAdmins))
	currentAdminIDs := make(map[int]bool, len(currentAdmins))
	for _, admin := range previousAdmins {
		previousAdminIDs[admin.ID] = true
	}
	for _, admin := range currentAdmins {
		currentAdminIDs[admin.ID] = true
	}
	old := make(map[string]WPAnomalyApplicationPassword, len(previous))
	for _, password := range previous {
		old[fmt.Sprintf("%d:%s", password.AdminID, password.Fingerprint)] = password
	}
	critical, revoked := []string{}, []string{}
	newAdminCredentialCounts := map[int]int{}
	for _, password := range current {
		key := fmt.Sprintf("%d:%s", password.AdminID, password.Fingerprint)
		before, exists := old[key]
		if !exists {
			if previousAdminIDs[password.AdminID] {
				appID := "未提供 app_id"
				if password.HasAppID {
					appID = "带 app_id"
				}
				critical = append(critical, fmt.Sprintf("管理员 ID %d 新增凭据 %q（创建于 %s UTC，%s）", password.AdminID, password.Name, time.Unix(password.Created, 0).UTC().Format("2006-01-02 15:04:05"), appID))
			} else {
				newAdminCredentialCounts[password.AdminID]++
			}
		} else if before.LastUsed == 0 && password.LastUsed > 0 {
			critical = append(critical, fmt.Sprintf("管理员 ID %d 的凭据 %q 首次被使用，来源 %q", password.AdminID, password.Name, password.LastIP))
		} else if password.LastUsed > before.LastUsed && password.LastIP != before.LastIP {
			critical = append(critical, fmt.Sprintf("管理员 ID %d 的凭据 %q 使用来源由 %q 变为 %q", password.AdminID, password.Name, before.LastIP, password.LastIP))
		}
		delete(old, key)
	}
	newAdminIDs := make([]int, 0, len(newAdminCredentialCounts))
	for adminID := range newAdminCredentialCounts {
		newAdminIDs = append(newAdminIDs, adminID)
	}
	sort.Ints(newAdminIDs)
	for _, adminID := range newAdminIDs {
		critical = append(critical, fmt.Sprintf("新增/提权管理员 ID %d 当前发现 %d 个凭据，已建立新基线", adminID, newAdminCredentialCounts[adminID]))
	}
	for _, password := range old {
		if currentAdminIDs[password.AdminID] {
			revoked = append(revoked, fmt.Sprintf("管理员 ID %d 的凭据 %q", password.AdminID, password.Name))
		}
	}
	sort.Strings(revoked)
	return critical, revoked
}

func applicationPasswordFingerprintsRotated(previous, current []WPAnomalyApplicationPassword) bool {
	if len(previous) == 0 || len(previous) != len(current) {
		return false
	}
	oldKeys := make(map[string]bool, len(previous))
	for _, password := range previous {
		oldKeys[fmt.Sprintf("%d:%s", password.AdminID, password.Fingerprint)] = true
	}
	for _, password := range current {
		if oldKeys[fmt.Sprintf("%d:%s", password.AdminID, password.Fingerprint)] {
			return false
		}
	}
	return true
}

func anomalyDatabaseObjectChanges(previous, current []WPAnomalyDatabaseObject, initialized bool) ([]string, []string) {
	if !initialized {
		if len(current) == 0 {
			return nil, nil
		}
		changes := make([]string, 0, len(current))
		for _, object := range current {
			changes = append(changes, "首次基线发现 "+databaseObjectDescription(object))
		}
		return changes, nil
	}
	if databaseObjectFingerprintsRotated(previous, current) {
		return []string{fmt.Sprintf("%d 个数据库对象的指纹基线整体变化，可能由站点密钥轮换引起，也可能是对象定义被修改", len(current))}, nil
	}
	old := make(map[string]WPAnomalyDatabaseObject, len(previous))
	for _, object := range previous {
		old[object.Kind+":"+object.Name] = object
	}
	critical, removed := []string{}, []string{}
	for _, object := range current {
		key := object.Kind + ":" + object.Name
		before, exists := old[key]
		if !exists {
			critical = append(critical, "新增"+databaseObjectDescription(object))
		} else if before != object {
			critical = append(critical, "修改"+databaseObjectDescription(object))
		}
		delete(old, key)
	}
	for _, object := range old {
		removed = append(removed, databaseObjectDescription(object))
	}
	sort.Strings(removed)
	return critical, removed
}

func databaseObjectFingerprintsRotated(previous, current []WPAnomalyDatabaseObject) bool {
	if len(previous) == 0 || len(previous) != len(current) {
		return false
	}
	old := make(map[string]WPAnomalyDatabaseObject, len(previous))
	for _, object := range previous {
		old[object.Kind+":"+object.Name] = object
	}
	for _, object := range current {
		before, exists := old[object.Kind+":"+object.Name]
		if !exists || before.Target != object.Target || before.Action != object.Action || before.Status != object.Status || before.Fingerprint == object.Fingerprint {
			return false
		}
	}
	return true
}

func databaseObjectDescription(object WPAnomalyDatabaseObject) string {
	kind := map[string]string{"trigger": "触发器", "event": "定时事件", "procedure": "存储过程", "function": "函数"}[object.Kind]
	description := fmt.Sprintf("%s %q", kind, object.Name)
	if object.Target != "" {
		description += fmt.Sprintf("（目标表 %q", object.Target)
		if object.Action != "" {
			description += "，" + object.Action
		}
		description += "）"
	} else if object.Action != "" || object.Status != "" {
		description += "（"
		parts := []string{}
		if object.Action != "" {
			parts = append(parts, object.Action)
		}
		if object.Status != "" {
			parts = append(parts, object.Status)
		}
		description += strings.Join(parts, "，") + "）"
	}
	return description
}

func summarizeAnomalyChanges(changes []string, limit int) string {
	if len(changes) <= limit {
		return strings.Join(changes, "；")
	}
	return fmt.Sprintf("%s；另有 %d 条（共 %d 条）", strings.Join(changes[:limit], "；"), len(changes)-limit, len(changes))
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
		} else if anomalyAdminChanged(before, admin) {
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

func anomalyAdminChanged(before, after WPAnomalyAdmin) bool {
	if before.ID != after.ID || before.Login != after.Login || !reflect.DeepEqual(before.Roles, after.Roles) || before.EmailHash != after.EmailHash {
		return true
	}
	// Empty hashes are the on-disk 1.0.59 shape. Populate them on the first
	// compatible sample without turning a schema upgrade into an incident.
	return (before.DisplayHash != "" && before.DisplayHash != after.DisplayHash) ||
		(before.CredentialHash != "" && before.CredentialHash != after.CredentialHash)
}

func anomalyContentChanges(previous, current []WPAnomalyContent, oldFrontPageID, newFrontPageID int, recent []WPAnomalyContentChange, now int64) ([]WPAnomalyContentChange, []string) {
	old := make(map[int]WPAnomalyContent, len(previous))
	cur := make(map[int]WPAnomalyContent, len(current))
	for _, item := range previous {
		old[item.ID] = item
	}
	for _, item := range current {
		cur[item.ID] = item
	}
	changes := map[int]WPAnomalyContentChange{}
	for _, change := range recent {
		if change.DetectedAt > now-86400 && change.DetectedAt <= now {
			changes[change.ID] = change
		}
	}
	messages := []string{}
	deleted := 0
	homeChanged := oldFrontPageID != newFrontPageID
	for id, before := range old {
		after, exists := cur[id]
		if !exists {
			deleted++
			delete(changes, id)
			if id == oldFrontPageID {
				homeChanged = true
			}
			continue
		}
		if before.Fingerprint != after.Fingerprint {
			changes[id] = WPAnomalyContentChange{ID: id, DetectedAt: now}
			if id == oldFrontPageID || id == newFrontPageID {
				homeChanged = true
			}
		}
	}
	if deleted > 0 {
		messages = append(messages, fmt.Sprintf("%d 篇已发布文章或页面被删除、移入回收站或取消发布", deleted))
	}
	if homeChanged {
		messages = append(messages, "首页对应页面或首页内容发生变化")
	}
	result := make([]WPAnomalyContentChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, messages
}

func anomalyOptionChanges(before, after WPAnomalyCriticalOptions) []string {
	changes := []string{}
	if before.SiteURL != after.SiteURL {
		changes = append(changes, "WordPress 地址（siteurl）被修改")
	}
	if before.Home != after.Home {
		changes = append(changes, "站点地址（home）被修改")
	}
	if !before.UsersCanRegister && after.UsersCanRegister {
		changes = append(changes, "任何人可以注册已开启")
	}
	if before.DefaultRole != after.DefaultRole {
		changes = append(changes, fmt.Sprintf("新用户默认角色由 %q 改为 %q", before.DefaultRole, after.DefaultRole))
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
