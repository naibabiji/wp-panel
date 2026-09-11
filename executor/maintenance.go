package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrMaintenanceValidation       = errors.New("maintenance validation failed")
	ErrMaintenanceFrozen           = errors.New("maintenance verification frozen")
	ErrMaintenanceBusy             = errors.New("maintenance operation unavailable")
	ErrMaintenanceUnknown          = errors.New("maintenance state unknown")
	ErrMaintenancePasswordRequired = errors.New("maintenance password required")
	ErrMaintenanceLockMode         = errors.New("maintenance requires standard or strict file lock")
)

// Private JSON column; never embed this structure in a public response.
type maintenanceSecurity struct {
	Enabled        bool                       `json:"enabled"`
	Minutes        int                        `json:"minutes"`
	Hash           string                     `json:"hash,omitempty"`
	Failures       []int64                    `json:"failures,omitempty"`
	FrozenUntil    int64                      `json:"frozen_until,omitempty"`
	Window         *maintenanceWindow         `json:"window,omitempty"`
	Closed         string                     `json:"closed,omitempty"`
	ClosedReason   string                     `json:"closed_reason,omitempty"`
	FailedRequests []maintenanceFailedRequest `json:"failed_requests,omitempty"`
}

type maintenanceFailedRequest struct {
	ID string `json:"id"`
	At int64  `json:"at"`
}

type maintenanceWindow struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Started       int64  `json:"started"`
	Expires       int64  `json:"expires"`
	VerifiedUntil int64  `json:"verified_until"`
	Mode          string `json:"mode"`
	Root          string `json:"root"`
	User          string `json:"user"`
	Actor         string `json:"actor"`
	Revision      int    `json:"revision"`
	LastRequest   string `json:"last_request,omitempty"`
	LastMinutes   int    `json:"last_minutes,omitempty"`
	RetryAt       int64  `json:"retry_at,omitempty"`
	Alerted       bool   `json:"alerted,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Failure       string `json:"failure,omitempty"`
}

type MaintenanceStatus struct {
	State         string `json:"state"`
	Enabled       bool   `json:"enabled"`
	PasswordSet   bool   `json:"password_set"`
	Minutes       int    `json:"minutes"`
	WindowID      string `json:"window_id"`
	ExpiresAt     int64  `json:"expires_at"`
	VerifiedUntil int64  `json:"verified_until"`
	Revision      int    `json:"revision"`
	ServerTime    int64  `json:"server_time"`
	Notice        string `json:"notice,omitempty"`
}

type MaintenanceRequest struct {
	WindowID  string `json:"window_id"`
	RequestID string `json:"request_id"`
	Revision  int    `json:"revision"`
	Minutes   int    `json:"minutes"`
	Password  string `json:"-"`
	Actor     string `json:"actor"`
}

type MaintenanceManager struct {
	db         *sql.DB
	now        func() time.Time
	unlock     func(*models.Website) error
	lock       func(*models.Website, string) error
	alert      func(int, string)
	mu         sync.Mutex
	uncertain  map[int]bool
	retry      map[int]int64
	alerted    map[int]bool
	restarting bool
}

var maintenanceSingleton struct {
	sync.Mutex
	manager *MaintenanceManager
}

func DefaultMaintenanceManager() *MaintenanceManager {
	maintenanceSingleton.Lock()
	defer maintenanceSingleton.Unlock()
	db := database.GetDB()
	if maintenanceSingleton.manager == nil || maintenanceSingleton.manager.db != db {
		maintenanceSingleton.manager = NewMaintenanceManager(db)
	}
	return maintenanceSingleton.manager
}

func NewMaintenanceManager(db *sql.DB) *MaintenanceManager {
	return &MaintenanceManager{db: db, now: time.Now, unlock: ApplySiteUnlockedPermissions, lock: ApplySiteFileLockMode,
		uncertain: map[int]bool{}, retry: map[int]int64{}, alerted: map[int]bool{},
		alert: func(id int, code string) {
			sendResolvedAlertEvent("alert_wp_maintenance", "WordPress 维护告警", fmt.Sprintf("site=%d event=%s", id, code), "请核查网站维护状态及操作日志。")
		},
	}
}

func (m *MaintenanceManager) isUncertain(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restarting || m.uncertain[id]
}

func (m *MaintenanceManager) markUncertain(id int, value bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value {
		m.uncertain[id] = true
	} else {
		delete(m.uncertain, id)
		delete(m.retry, id)
		delete(m.alerted, id)
	}
}

func (m *MaintenanceManager) load(id int) (*models.Website, maintenanceSecurity, string, error) {
	var site models.Website
	var state maintenanceSecurity
	var raw string
	err := m.db.QueryRow(`SELECT id,domain,web_root,system_user,site_type,status,file_lock_enabled,file_lock_mode,file_lock_apply_status,maintenance_security FROM websites WHERE id=?`, id).
		Scan(&site.ID, &site.Domain, &site.WebRoot, &site.SystemUser, &site.SiteType, &site.Status, &site.FileLockEnabled, &site.FileLockMode, &site.FileLockApplyStatus, &raw)
	if err != nil {
		return nil, state, raw, err
	}
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, state, raw, err
	}
	if state.Minutes == 0 {
		state.Minutes = 5
	}
	return &site, state, raw, nil
}

// Compare the full private record, including window identity and revision. The
// public file-lock flags are committed in the same SQL statement when needed.
func (m *MaintenanceManager) save(id int, state maintenanceSecurity, raw *string, fileState string) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	query := `UPDATE websites SET maintenance_security=?,updated_at=CURRENT_TIMESTAMP`
	args := []any{string(b)}
	switch fileState {
	case "unlocking", "relocking":
		query += `,file_lock_apply_status='applying'`
	case "unlocked":
		query += `,file_lock_enabled=0,file_lock_apply_status=''`
	case "failed":
		query += `,file_lock_apply_status='failed'`
	case "locked":
		query += `,file_lock_enabled=1,file_lock_apply_status='ready',file_lock_mode=?,file_lock_enabled_at=CURRENT_TIMESTAMP`
		args = append(args, state.Window.Mode)
		state.Closed = state.Window.ID
		state.ClosedReason = state.Window.Reason
		state.Window = nil
		b, err = json.Marshal(state)
		if err != nil {
			return err
		}
		args[0] = string(b)
	}
	query += ` WHERE id=? AND maintenance_security=?`
	args = append(args, id, *raw)
	res, err := m.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return ErrMaintenanceUnknown
	}
	*raw = string(b)
	return nil
}

func (m *MaintenanceManager) Status(id int) (MaintenanceStatus, error) {
	site, state, _, err := m.load(id)
	if err != nil {
		return MaintenanceStatus{State: "unknown"}, ErrMaintenanceUnknown
	}
	out := MaintenanceStatus{State: "unlocked_permanent", Enabled: state.Enabled, PasswordSet: state.Hash != "", Minutes: state.Minutes, ServerTime: m.now().Unix()}
	if state.Window == nil {
		out.Notice = state.ClosedReason
	}
	if site.FileLockEnabled {
		out.State = "locked"
	}
	if site.FileLockApplyStatus == "failed" || site.FileLockApplyStatus == "applying" {
		out.State = "unknown"
	}
	if w := state.Window; w != nil {
		out.State, out.WindowID, out.ExpiresAt, out.VerifiedUntil, out.Revision = w.State, w.ID, w.Expires, w.VerifiedUntil, w.Revision
		if w.State == "unlocked" && w.Expires <= out.ServerTime {
			out.State = "relocking"
		}
	}
	if m.isUncertain(id) && out.State != "relock_failed" && out.State != "relocking" {
		out.State = "unknown"
	}
	return out, nil
}

func (m *MaintenanceManager) Configure(id int, enabled bool, minutes int, password string) error {
	if minutes != 3 && minutes != 5 && minutes != 10 {
		return ErrMaintenanceValidation
	}
	if len(password) > 72 || (password != "" && len(password) < 12) {
		return ErrMaintenanceValidation
	}
	if !tryAcquireMaintenanceOp(id) {
		return ErrMaintenanceBusy
	}
	defer ReleaseSiteOpLock(id)
	site, state, raw, err := m.load(id)
	if err != nil {
		return ErrMaintenanceUnknown
	}
	if state.Window != nil || m.isUncertain(id) || site.SiteType != "wordpress" {
		return ErrMaintenanceBusy
	}
	if enabled && site.FileLockEnabled && !maintenanceLockModeAllowed(site) {
		return ErrMaintenanceLockMode
	}
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return ErrMaintenanceValidation
		}
		state.Hash = string(hash)
	}
	if enabled && state.Hash == "" {
		return ErrMaintenanceValidation
	}
	state.Enabled, state.Minutes = enabled, minutes
	return m.save(id, state, &raw, "")
}

func (m *MaintenanceManager) password(id int, state *maintenanceSecurity, raw *string, password, operation, requestID string) error {
	now := m.now().Unix()
	if state.FrozenUntil > now {
		return ErrMaintenanceFrozen
	}
	// At most five failed validations per rolling window. Replays, even with
	// changed passwords, return the same failure without validating/counting again.
	recent := state.FailedRequests[:0]
	for _, request := range state.FailedRequests {
		if request.At > now-600 {
			if request.ID == requestID {
				return ErrMaintenanceValidation
			}
			recent = append(recent, request)
		}
	}
	state.FailedRequests = recent
	if password == "" {
		return ErrMaintenancePasswordRequired
	}
	if len(password) <= 72 && bcrypt.CompareHashAndPassword([]byte(state.Hash), []byte(password)) == nil {
		return nil
	}
	failures := []int64{}
	for _, stamp := range state.Failures {
		if stamp > now-600 {
			failures = append(failures, stamp)
		}
	}
	state.Failures = append(failures, now)
	state.FailedRequests = append(state.FailedRequests, maintenanceFailedRequest{ID: requestID, At: now})
	frozen := len(state.Failures) >= 5
	if frozen {
		state.FrozenUntil = now + 600
	}
	if err := m.save(id, *state, raw, ""); err != nil {
		m.markUncertain(id, true)
		return ErrMaintenanceUnknown
	}
	m.event(id, operation, "verification_failed")
	if frozen {
		m.alert(id, fmt.Sprintf("password_frozen attempts=5 window_seconds=600 frozen_until=%d", state.FrozenUntil))
		return ErrMaintenanceFrozen
	}
	return ErrMaintenanceValidation
}

func (m *MaintenanceManager) conflicts(id int) error {
	var n int
	queries := []string{
		`SELECT COUNT(*) FROM wp_update_tasks WHERE site_id=? AND (status IN ('preparing','queued','running') OR (requires_attention=1 AND manual_disposition=''))`,
		`SELECT COUNT(*) FROM site_migration_locks WHERE site_id=? AND status='active'`,
		`SELECT COUNT(*) FROM website_ai_development_access WHERE site_id=?`,
		`SELECT COUNT(*) FROM site_image_optimization_jobs WHERE site_id=? AND status IN ('queued','running')`,
	}
	for _, query := range queries {
		if err := m.db.QueryRow(query, id).Scan(&n); err != nil {
			return ErrMaintenanceUnknown
		}
		if n != 0 {
			return ErrMaintenanceBusy
		}
	}
	return nil
}

func validMaintenanceID(id string) bool { _, err := uuid.Parse(id); return err == nil && len(id) == 36 }

func maintenanceLockModeAllowed(site *models.Website) bool {
	mode := EffectiveFileLockMode(site)
	return mode == FileLockModeStandard || mode == FileLockModeStrict
}

func (m *MaintenanceManager) Unlock(id int, req MaintenanceRequest) error {
	if !validMaintenanceID(req.RequestID) || len(req.Actor) > 128 {
		return ErrMaintenanceValidation
	}
	if !tryAcquireMaintenanceOp(id) {
		return ErrMaintenanceBusy
	}
	defer ReleaseSiteOpLock(id)
	site, state, raw, err := m.load(id)
	if err != nil || m.isUncertain(id) {
		return ErrMaintenanceUnknown
	}
	if !state.Enabled || site.SiteType != "wordpress" || site.Status != "active" {
		return ErrMaintenanceValidation
	}
	if state.Window != nil {
		if state.Window.ID == req.RequestID && state.Window.State == "unlocked" && state.Window.Expires > m.now().Unix() {
			return nil
		}
		return ErrMaintenanceBusy
	}
	if state.Closed == req.RequestID || !site.FileLockEnabled || site.FileLockApplyStatus != "ready" || wpConfigHasUserFileModsLock(site.WebRoot) {
		return ErrMaintenanceBusy
	}
	if !maintenanceLockModeAllowed(site) {
		return ErrMaintenanceLockMode
	}
	if err := m.conflicts(id); err != nil {
		return err
	}
	if err := m.password(id, &state, &raw, req.Password, "unlock", req.RequestID); err != nil {
		return err
	}
	now := m.now().Unix()
	state.Window = &maintenanceWindow{ID: req.RequestID, State: "unlocking", Started: now, Mode: EffectiveFileLockMode(site), Root: site.WebRoot, User: site.SystemUser, Actor: req.Actor, VerifiedUntil: now + 1800}
	if err := m.save(id, state, &raw, "unlocking"); err != nil {
		m.markUncertain(id, true)
		return ErrMaintenanceUnknown
	}
	if err := m.unlock(site); err != nil {
		m.compensateUnlock(id, site, &state, &raw)
		return ErrMaintenanceUnknown
	}
	state.Window.State, state.Window.Expires = "unlocked", now+int64(state.Minutes*60)
	if err := m.save(id, state, &raw, "unlocked"); err != nil {
		m.compensateUnlock(id, site, &state, &raw)
		return ErrMaintenanceUnknown
	}
	if state.Window.Expires <= m.now().Unix() {
		return m.relockOwned(id, site, &state, &raw)
	}
	m.event(id, "unlock", fmt.Sprintf("success window=%s mode=%s expires=%d actor=site_asserted:%s", state.Window.ID, state.Window.Mode, state.Window.Expires, req.Actor))
	return nil
}

// Already owns the site's operation lock: even an unavailable database must
// not prevent compensation for a partial filesystem unlock.
func (m *MaintenanceManager) compensateUnlock(id int, site *models.Website, state *maintenanceSecurity, raw *string) {
	m.markUncertain(id, true)
	m.event(id, "unlock", "failed; compensating")
	if err := m.lock(site, state.Window.Mode); err != nil {
		retryAt := m.now().Unix() + 60
		m.mu.Lock()
		m.retry[id], m.alerted[id] = retryAt, true
		m.mu.Unlock()
		state.Window.State, state.Window.RetryAt, state.Window.Alerted = "relock_failed", retryAt, true
		state.Window.Failure = "permissions"
		_ = m.save(id, *state, raw, "failed")
		m.alert(id, "relock_failed")
	} else {
		state.Window.State, state.Window.Failure = "unknown", "unlock_not_committed"
		_ = m.save(id, *state, raw, "failed")
	}
	// Keep the durable intent until Tick confirms permissions and commits the
	// terminal state. The failed request itself never reports success.
}

func (m *MaintenanceManager) Extend(id int, req MaintenanceRequest) error {
	if !validMaintenanceID(req.RequestID) || !validMaintenanceID(req.WindowID) || (req.Minutes != 1 && req.Minutes != 3 && req.Minutes != 5) {
		return ErrMaintenanceValidation
	}
	if !tryAcquireMaintenanceOp(id) {
		return ErrMaintenanceBusy
	}
	defer ReleaseSiteOpLock(id)
	site, state, raw, err := m.load(id)
	if err != nil || m.isUncertain(id) {
		return ErrMaintenanceUnknown
	}
	w := state.Window
	if !state.Enabled || site.Status != "active" || w == nil || w.ID != req.WindowID || w.State != "unlocked" || w.Expires <= m.now().Unix() {
		return ErrMaintenanceBusy
	}
	if w.LastRequest == req.RequestID {
		if w.LastMinutes == req.Minutes {
			return nil
		}
		return ErrMaintenanceValidation
	}
	if w.Revision != req.Revision {
		return ErrMaintenanceBusy
	}
	expires := w.Expires + int64(req.Minutes*60)
	if expires > w.VerifiedUntil {
		if err := m.password(id, &state, &raw, req.Password, "extend", req.RequestID); err != nil {
			return err
		}
		w.VerifiedUntil += 1800
	}
	if w.Expires <= m.now().Unix() {
		return ErrMaintenanceBusy
	}
	w.Expires, w.LastRequest, w.LastMinutes = expires, req.RequestID, req.Minutes
	w.Revision++
	if err := m.save(id, state, &raw, ""); err != nil {
		m.markUncertain(id, true)
		return ErrMaintenanceUnknown
	}
	m.event(id, "extend", fmt.Sprintf("minutes=%d expires=%d actor=site_asserted:%s", req.Minutes, expires, req.Actor))
	return nil
}

func (m *MaintenanceManager) Relock(id int, windowID string) error {
	if !validMaintenanceID(windowID) {
		return ErrMaintenanceValidation
	}
	if !tryAcquireMaintenanceOp(id) {
		return ErrMaintenanceBusy
	}
	defer ReleaseSiteOpLock(id)
	site, state, raw, err := m.load(id)
	if err != nil {
		return ErrMaintenanceUnknown
	}
	if state.Window == nil && state.Closed == windowID {
		return nil
	}
	if state.Window == nil || state.Window.ID != windowID {
		return ErrMaintenanceBusy
	}
	return m.relockOwned(id, site, &state, &raw)
}

func (m *MaintenanceManager) relockOwned(id int, site *models.Website, state *maintenanceSecurity, raw *string) error {
	w := state.Window
	now := m.now().Unix()
	m.mu.Lock()
	retry := m.retry[id]
	m.mu.Unlock()
	if w.RetryAt > now || retry > now {
		return ErrMaintenanceBusy
	}
	if site.WebRoot != w.Root || site.SystemUser != w.User {
		m.markUncertain(id, true)
		return ErrMaintenanceUnknown
	}
	if err := m.conflicts(id); err != nil {
		return err
	}
	w.State = "relocking"
	if err := m.save(id, *state, raw, "relocking"); err != nil {
		m.markUncertain(id, true)
		return ErrMaintenanceUnknown
	}
	err := m.lock(site, w.Mode)
	failure := "permissions"
	if err == nil {
		failure = "state_commit"
		if err = m.save(id, *state, raw, "locked"); err == nil {
			m.markUncertain(id, false)
			m.event(id, "relock", fmt.Sprintf("success window=%s mode=%s reason=%s", w.ID, w.Mode, w.Reason))
			return nil
		}
	}
	m.markUncertain(id, true)
	m.mu.Lock()
	retryAt := m.now().Unix() + 60
	m.retry[id] = retryAt
	alert := !w.Alerted && !m.alerted[id]
	m.alerted[id] = true
	m.mu.Unlock()
	w.State, w.RetryAt = "relock_failed", retryAt
	w.Failure = failure
	w.Alerted = true
	if saveErr := m.save(id, *state, raw, "failed"); saveErr != nil {
		log.Printf("maintenance state save failed site=%d", id)
	}
	if alert {
		m.alert(id, "relock_failed")
	}
	m.event(id, "relock", "failed stage="+failure)
	return ErrMaintenanceUnknown
}

func (m *MaintenanceManager) event(id int, op, result string) {
	_, err := m.db.Exec(`INSERT INTO operation_logs(operation,target,status,message) VALUES (?,?,?,?)`, "wp_maintenance_"+op, fmt.Sprint(id), "info", result)
	if err != nil {
		log.Printf("maintenance audit write failed site=%d operation=%s", id, op)
	}
}

// Startup seals every old window before plugin routes are served. Failure keeps
// the service fail-closed; Tick retries startup without losing that intention.
func (m *MaintenanceManager) Start(ctx context.Context) {
	m.mu.Lock()
	m.restarting = true
	m.mu.Unlock()
	m.Tick()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Tick()
			}
		}
	}()
}

func (m *MaintenanceManager) Tick() {
	m.mu.Lock()
	restarting := m.restarting
	m.mu.Unlock()
	if restarting {
		// No filesystem inspection. Persist the intention to relock, retaining
		// all original recovery metadata. Each site is then processed normally.
		_, err := m.db.Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window.state','relocking','$.window.reason','restart','$.window.retry_at',0) WHERE json_extract(maintenance_security,'$.window.id') IS NOT NULL`)
		if err != nil {
			return
		}
		m.mu.Lock()
		m.restarting = false
		m.mu.Unlock()
	}
	rows, err := m.db.Query(`SELECT id FROM websites WHERE json_extract(maintenance_security,'$.window.id') IS NOT NULL`)
	if err != nil {
		return
	}
	var ids []int
	for rows.Next() {
		var id int
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	// Failed intent/password writes may leave no window at all. A successful
	// reread can release that fail-closed latch; no filesystem unlock ran.
	m.mu.Lock()
	for id := range m.uncertain {
		found := false
		for _, candidate := range ids {
			if candidate == id {
				found = true
				break
			}
		}
		if !found {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		if !tryAcquireMaintenanceOp(id) {
			continue
		}
		func() {
			defer ReleaseSiteOpLock(id)
			site, state, raw, err := m.load(id)
			if err != nil {
				return
			}
			if state.Window == nil {
				m.markUncertain(id, false)
				return
			}
			w := state.Window
			if w.State != "unlocked" || w.Expires <= m.now().Unix() || m.isUncertain(id) {
				_ = m.relockOwned(id, site, &state, &raw)
			}
		}()
	}
}
