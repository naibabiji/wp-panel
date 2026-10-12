package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrManagedMailUnavailable  = errors.New("managed mail unavailable")
	ErrManagedMailUnauthorized = errors.New("managed mail unauthorized")
	ErrManagedMailUnconfirmed  = errors.New("managed mail unconfirmed")
	ErrManagedMailConflict     = errors.New("managed mail plugin conflict")
	ErrManagedMailPaused       = errors.New("managed mail paused")
	ErrManagedMailQuota        = errors.New("managed mail quota exceeded")
	ErrManagedMailValidation   = errors.New("managed mail validation failed")
	ErrManagedMailFromNameLock = errors.New("managed mail from name locked")
)

// managedMailReservationMu prevents concurrent sends inside the single panel
// process from surfacing SQLITE_BUSY while BEGIN IMMEDIATE protects the quota
// read-and-reserve operation at the database boundary.
var managedMailReservationMu sync.Mutex
var managedMailQuotaEmail = SendMail

type ManagedMailSitePolicy struct {
	SiteID             int64      `json:"site_id"`
	Domain             string     `json:"domain"`
	SiteStatus         string     `json:"site_status"`
	Authorized         bool       `json:"authorized"`
	SiteConfirmed      bool       `json:"site_confirmed"`
	FromName           string     `json:"from_name"`
	ReplyTo            string     `json:"reply_to"`
	LockFromName       bool       `json:"lock_from_name"`
	MinuteLimit        int        `json:"minute_limit"`
	HourLimit          int        `json:"hour_limit"`
	DayLimit           int        `json:"day_limit"`
	ConflictPlugin     string     `json:"conflict_plugin"`
	ConflictDetectedAt *time.Time `json:"conflict_detected_at,omitempty"`
	PausedUntil        *time.Time `json:"paused_until,omitempty"`
	PausedReason       string     `json:"paused_reason"`
	SMTPReady          bool       `json:"smtp_ready"`
	FromEmail          string     `json:"from_email"`
	Active             bool       `json:"active"`
}

type ManagedMailGlobalLimits struct {
	MinuteLimit     int `json:"minute_limit"`
	HourLimit       int `json:"hour_limit"`
	DayLimit        int `json:"day_limit"`
	MaxRecipients   int `json:"max_recipients"`
	MaxMessageBytes int `json:"max_message_bytes"`
	MaxAttachments  int `json:"max_attachments"`
	SystemDayLimit  int `json:"system_day_limit"`
}

type ManagedMailUsage struct {
	SiteMinute  int `json:"site_minute"`
	SiteHour    int `json:"site_hour"`
	SiteDay     int `json:"site_day"`
	TotalMinute int `json:"total_minute"`
	TotalHour   int `json:"total_hour"`
	TotalDay    int `json:"total_day"`
}

type managedMailNoticeState struct {
	Day     string          `json:"day"`
	Notices map[string]bool `json:"notices"`
	System  int             `json:"system_count"`
}

type ManagedMailReservation struct {
	RequestID      string           `json:"request_id"`
	Status         string           `json:"status"`
	RecipientCount int              `json:"recipient_count"`
	Existing       bool             `json:"existing"`
	Usage          ManagedMailUsage `json:"usage"`
}

type ManagedMailPolicyService struct {
	db  *sql.DB
	now func() time.Time
}

func NewManagedMailPolicyService(db *sql.DB) *ManagedMailPolicyService {
	return &ManagedMailPolicyService{db: db, now: time.Now}
}

func (s *ManagedMailPolicyService) GetSitePolicy(ctx context.Context, siteID int64) (ManagedMailSitePolicy, error) {
	if s == nil || s.db == nil || siteID <= 0 {
		return ManagedMailSitePolicy{}, ErrManagedMailValidation
	}
	var policy ManagedMailSitePolicy
	var authorized, confirmed, locked int
	var conflictAt, pausedUntil sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT w.id,w.domain,w.status,
		COALESCE(m.authorized,0),COALESCE(m.site_confirmed,0),COALESCE(m.from_name,''),
		COALESCE(m.reply_to,''),COALESCE(m.lock_from_name,0),COALESCE(m.minute_limit,3),
		COALESCE(m.hour_limit,15),COALESCE(m.day_limit,50),COALESCE(m.conflict_plugin,''),
		m.conflict_detected_at,m.paused_until,COALESCE(m.paused_reason,'')
		FROM websites w LEFT JOIN managed_mail_sites m ON m.site_id=w.id
		WHERE w.id=? AND w.site_type='wordpress'`, siteID).Scan(
		&policy.SiteID, &policy.Domain, &policy.SiteStatus, &authorized, &confirmed,
		&policy.FromName, &policy.ReplyTo, &locked, &policy.MinuteLimit,
		&policy.HourLimit, &policy.DayLimit, &policy.ConflictPlugin,
		&conflictAt, &pausedUntil, &policy.PausedReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedMailSitePolicy{}, ErrManagedMailUnavailable
	}
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	policy.Authorized = authorized == 1
	policy.SiteConfirmed = confirmed == 1
	policy.LockFromName = locked == 1
	if conflictAt.Valid {
		t := conflictAt.Time
		policy.ConflictDetectedAt = &t
	}
	if pausedUntil.Valid {
		t := pausedUntil.Time
		policy.PausedUntil = &t
	}
	cfg := getSMTPConfig(s.db)
	if cfg != nil {
		policy.FromEmail = strings.TrimSpace(cfg.FromEmail)
		policy.SMTPReady = strings.TrimSpace(cfg.Host) != "" && strings.TrimSpace(cfg.User) != "" && cfg.Pass != "" && validManagedMailAddress(policy.FromEmail)
	}
	now := s.now()
	policy.Active = policy.Authorized && policy.SiteConfirmed && policy.SMTPReady && policy.SiteStatus == "active" && policy.ConflictPlugin == "" && (policy.PausedUntil == nil || !policy.PausedUntil.After(now))
	return policy, nil
}

func (s *ManagedMailPolicyService) SaveSitePolicy(ctx context.Context, input ManagedMailSitePolicy) (ManagedMailSitePolicy, error) {
	if s == nil || s.db == nil || input.SiteID <= 0 || input.MinuteLimit < 1 || input.MinuteLimit > 10000 || input.HourLimit < 1 || input.HourLimit > 100000 || input.DayLimit < 1 || input.DayLimit > 1000000 || input.MinuteLimit > input.HourLimit || input.HourLimit > input.DayLimit {
		return ManagedMailSitePolicy{}, ErrManagedMailValidation
	}
	input.FromName = strings.TrimSpace(input.FromName)
	input.ReplyTo = strings.TrimSpace(input.ReplyTo)
	if len([]rune(input.FromName)) > 100 || strings.ContainsAny(input.FromName, "\r\n") || (input.ReplyTo != "" && !validManagedMailAddress(input.ReplyTo)) {
		return ManagedMailSitePolicy{}, ErrManagedMailValidation
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM websites WHERE id=? AND site_type='wordpress'`, input.SiteID).Scan(&exists); err != nil || exists != 1 {
		if err != nil {
			return ManagedMailSitePolicy{}, err
		}
		return ManagedMailSitePolicy{}, ErrManagedMailUnavailable
	}
	authorized := 0
	if input.Authorized {
		authorized = 1
	}
	locked := 0
	if input.LockFromName {
		locked = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_mail_sites
		(site_id,authorized,site_confirmed,from_name,reply_to,lock_from_name,minute_limit,hour_limit,day_limit,updated_at)
		VALUES (?,?,0,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(site_id) DO UPDATE SET
		authorized=excluded.authorized,
		site_confirmed=CASE WHEN excluded.authorized=0 THEN 0 ELSE managed_mail_sites.site_confirmed END,
		from_name=excluded.from_name,reply_to=excluded.reply_to,lock_from_name=excluded.lock_from_name,
		minute_limit=excluded.minute_limit,hour_limit=excluded.hour_limit,day_limit=excluded.day_limit,
		updated_at=CURRENT_TIMESTAMP`, input.SiteID, authorized, input.FromName, input.ReplyTo, locked, input.MinuteLimit, input.HourLimit, input.DayLimit)
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	return s.GetSitePolicy(ctx, input.SiteID)
}

func (s *ManagedMailPolicyService) ConfirmSite(ctx context.Context, siteID int64, enabled bool) (ManagedMailSitePolicy, error) {
	policy, err := s.GetSitePolicy(ctx, siteID)
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	if enabled && !policy.Authorized {
		return ManagedMailSitePolicy{}, ErrManagedMailUnauthorized
	}
	if enabled && policy.ConflictPlugin != "" {
		return ManagedMailSitePolicy{}, ErrManagedMailConflict
	}
	if enabled && !policy.SMTPReady {
		return ManagedMailSitePolicy{}, ErrManagedMailUnavailable
	}
	confirmed := 0
	if enabled {
		confirmed = 1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_mail_sites SET site_confirmed=?,updated_at=CURRENT_TIMESTAMP WHERE site_id=?`, confirmed, siteID)
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ManagedMailSitePolicy{}, ErrManagedMailUnavailable
	}
	return s.GetSitePolicy(ctx, siteID)
}

func (s *ManagedMailPolicyService) UpdateSiteSender(ctx context.Context, siteID int64, fromName, replyTo string) (ManagedMailSitePolicy, error) {
	policy, err := s.GetSitePolicy(ctx, siteID)
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	if !policy.Authorized {
		return ManagedMailSitePolicy{}, ErrManagedMailUnauthorized
	}
	fromName = strings.TrimSpace(fromName)
	replyTo = strings.TrimSpace(replyTo)
	if len([]rune(fromName)) > 100 || strings.ContainsAny(fromName, "\r\n") || (replyTo != "" && !validManagedMailAddress(replyTo)) {
		return ManagedMailSitePolicy{}, ErrManagedMailValidation
	}
	if policy.LockFromName && fromName != policy.FromName {
		return ManagedMailSitePolicy{}, ErrManagedMailFromNameLock
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_mail_sites SET from_name=?,reply_to=?,updated_at=CURRENT_TIMESTAMP
		WHERE site_id=? AND authorized=1 AND (lock_from_name=0 OR from_name=?)`, fromName, replyTo, siteID, fromName)
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		current, getErr := s.GetSitePolicy(ctx, siteID)
		if getErr != nil {
			return ManagedMailSitePolicy{}, getErr
		}
		if !current.Authorized {
			return ManagedMailSitePolicy{}, ErrManagedMailUnauthorized
		}
		if current.LockFromName && fromName != current.FromName {
			return ManagedMailSitePolicy{}, ErrManagedMailFromNameLock
		}
		return ManagedMailSitePolicy{}, ErrManagedMailValidation
	}
	return s.GetSitePolicy(ctx, siteID)
}

func (s *ManagedMailPolicyService) ReportConflict(ctx context.Context, siteID int64, plugin string) (ManagedMailSitePolicy, error) {
	plugin = strings.TrimSpace(plugin)
	if len([]rune(plugin)) > 120 || strings.ContainsAny(plugin, "\x00\r\n") {
		return ManagedMailSitePolicy{}, ErrManagedMailValidation
	}
	var result sql.Result
	var err error
	if plugin == "" {
		result, err = s.db.ExecContext(ctx, `UPDATE managed_mail_sites SET conflict_plugin='',conflict_detected_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE site_id=?`, siteID)
	} else {
		result, err = s.db.ExecContext(ctx, `INSERT INTO managed_mail_sites(site_id,conflict_plugin,conflict_detected_at,site_confirmed,updated_at)
			SELECT id,?,CURRENT_TIMESTAMP,0,CURRENT_TIMESTAMP FROM websites WHERE id=? AND site_type='wordpress'
			ON CONFLICT(site_id) DO UPDATE SET conflict_plugin=excluded.conflict_plugin,
			conflict_detected_at=excluded.conflict_detected_at,site_confirmed=0,updated_at=CURRENT_TIMESTAMP`, plugin, siteID)
	}
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 && plugin != "" {
		return ManagedMailSitePolicy{}, ErrManagedMailUnavailable
	}
	return s.GetSitePolicy(ctx, siteID)
}

func (s *ManagedMailPolicyService) GlobalLimits(ctx context.Context) (ManagedMailGlobalLimits, error) {
	if s == nil || s.db == nil {
		return ManagedMailGlobalLimits{}, ErrManagedMailUnavailable
	}
	limits := ManagedMailGlobalLimits{MinuteLimit: 10, HourLimit: 30, DayLimit: 100, MaxRecipients: 10, MaxMessageBytes: 10 * 1024 * 1024, MaxAttachments: 5, SystemDayLimit: 20}
	values := map[string]*int{
		"managed_mail_total_minute_limit": &limits.MinuteLimit,
		"managed_mail_total_hour_limit":   &limits.HourLimit,
		"managed_mail_total_day_limit":    &limits.DayLimit,
		"managed_mail_max_recipients":     &limits.MaxRecipients,
		"managed_mail_max_message_bytes":  &limits.MaxMessageBytes,
		"managed_mail_max_attachments":    &limits.MaxAttachments,
		"managed_mail_system_day_limit":   &limits.SystemDayLimit,
	}
	rows, err := s.db.QueryContext(ctx, `SELECT skey,svalue FROM security_settings WHERE skey LIKE 'managed_mail_%'`)
	if err != nil {
		return ManagedMailGlobalLimits{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if rows.Scan(&key, &value) == nil {
			if target := values[key]; target != nil {
				if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 {
					*target = parsed
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return ManagedMailGlobalLimits{}, err
	}
	if limits.MinuteLimit > limits.HourLimit || limits.HourLimit > limits.DayLimit {
		return ManagedMailGlobalLimits{}, fmt.Errorf("%w: invalid aggregate limits", ErrManagedMailValidation)
	}
	return limits, nil
}

func (s *ManagedMailPolicyService) Reserve(ctx context.Context, siteID int64, requestID string, recipients, messageBytes, attachments int) (ManagedMailReservation, error) {
	requestID = strings.TrimSpace(requestID)
	if s == nil || s.db == nil || siteID <= 0 || len(requestID) < 16 || len(requestID) > 128 || strings.ContainsAny(requestID, "\x00\r\n") || recipients < 1 || messageBytes < 0 || attachments < 0 {
		return ManagedMailReservation{}, ErrManagedMailValidation
	}
	limits, err := s.GlobalLimits(ctx)
	if err != nil {
		return ManagedMailReservation{}, err
	}
	if recipients > limits.MaxRecipients || messageBytes > limits.MaxMessageBytes || attachments > limits.MaxAttachments {
		return ManagedMailReservation{}, ErrManagedMailValidation
	}
	managedMailReservationMu.Lock()
	defer managedMailReservationMu.Unlock()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return ManagedMailReservation{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return ManagedMailReservation{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var existing ManagedMailReservation
	var existingBytes int
	err = conn.QueryRowContext(ctx, `SELECT request_id,status,recipient_count,message_size FROM managed_mail_deliveries WHERE request_id=?`, requestID).Scan(&existing.RequestID, &existing.Status, &existing.RecipientCount, &existingBytes)
	if err == nil {
		var existingSite int64
		if scanErr := conn.QueryRowContext(ctx, `SELECT site_id FROM managed_mail_deliveries WHERE request_id=?`, requestID).Scan(&existingSite); scanErr != nil || existingSite != siteID || existing.RecipientCount != recipients || existingBytes != messageBytes {
			return ManagedMailReservation{}, ErrManagedMailValidation
		}
		existing.Existing = true
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return ManagedMailReservation{}, err
		}
		committed = true
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ManagedMailReservation{}, err
	}

	now := s.now()
	_, _ = conn.ExecContext(ctx, `UPDATE managed_mail_deliveries SET status='unknown',error_code='interrupted',updated_at=? WHERE status='reserved' AND created_at<?`, now.UTC(), now.Add(-30*time.Second).UTC())
	policy, err := getManagedMailPolicyOnConn(ctx, conn, siteID, now)
	if err != nil {
		return ManagedMailReservation{}, err
	}
	if !policy.Authorized {
		return ManagedMailReservation{}, ErrManagedMailUnauthorized
	}
	if !policy.SiteConfirmed {
		return ManagedMailReservation{}, ErrManagedMailUnconfirmed
	}
	if policy.SiteStatus != "active" {
		return ManagedMailReservation{}, ErrManagedMailPaused
	}
	if policy.ConflictPlugin != "" {
		return ManagedMailReservation{}, ErrManagedMailConflict
	}
	if policy.PausedUntil != nil && policy.PausedUntil.After(now) {
		return ManagedMailReservation{}, ErrManagedMailPaused
	}
	cfg := getSMTPConfig(s.db)
	if cfg == nil || strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.User) == "" || cfg.Pass == "" || !validManagedMailAddress(cfg.FromEmail) {
		return ManagedMailReservation{}, ErrManagedMailUnavailable
	}

	usage, err := managedMailUsageOnConn(ctx, conn, siteID, now)
	if err != nil {
		return ManagedMailReservation{}, err
	}
	if usage.SiteMinute+recipients > policy.MinuteLimit || usage.SiteHour+recipients > policy.HourLimit || usage.SiteDay+recipients > policy.DayLimit || usage.TotalMinute+recipients > limits.MinuteLimit || usage.TotalHour+recipients > limits.HourLimit || usage.TotalDay+recipients > limits.DayLimit {
		return ManagedMailReservation{}, ErrManagedMailQuota
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO managed_mail_deliveries(request_id,site_id,status,recipient_count,message_size,created_at,updated_at) VALUES (?,?, 'reserved',?,?,?,?)`, requestID, siteID, recipients, messageBytes, now.UTC(), now.UTC()); err != nil {
		return ManagedMailReservation{}, err
	}
	usage.SiteMinute += recipients
	usage.SiteHour += recipients
	usage.SiteDay += recipients
	usage.TotalMinute += recipients
	usage.TotalHour += recipients
	usage.TotalDay += recipients
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return ManagedMailReservation{}, err
	}
	committed = true
	return ManagedMailReservation{RequestID: requestID, Status: "reserved", RecipientCount: recipients, Usage: usage}, nil
}

func (s *ManagedMailPolicyService) Complete(ctx context.Context, siteID int64, requestID, status, messageID, errorCode string, smtpCode, durationMS int) error {
	if s == nil || s.db == nil || siteID <= 0 || requestID == "" || durationMS < 0 {
		return ErrManagedMailValidation
	}
	switch status {
	case "accepted", "rejected", "temporary_failure", "unknown":
	default:
		return ErrManagedMailValidation
	}
	if len(messageID) > 300 || len(errorCode) > 100 || strings.ContainsAny(messageID+errorCode, "\x00\r\n") {
		return ErrManagedMailValidation
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_mail_deliveries SET status=?,message_id=?,error_code=?,smtp_code=?,duration_ms=?,updated_at=CURRENT_TIMESTAMP WHERE request_id=? AND site_id=? AND status='reserved'`, status, messageID, errorCode, smtpCode, durationMS, requestID, siteID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrManagedMailValidation
	}
	return nil
}

func (s *ManagedMailPolicyService) ApplyDeliveryPolicy(ctx context.Context, siteID int64, status, errorCode string) error {
	if s == nil || s.db == nil || siteID <= 0 {
		return ErrManagedMailValidation
	}
	now := s.now()
	until := now.Add(15 * time.Minute).UTC()
	if errorCode == "authentication_failed" || errorCode == "provider_rate_limited" {
		_, err := s.db.ExecContext(ctx, `UPDATE managed_mail_sites SET paused_until=?,paused_reason=?,updated_at=? WHERE authorized=1`, until, errorCode, now.UTC())
		return err
	}
	if status != "rejected" && status != "temporary_failure" {
		return nil
	}
	var total, failures int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN status IN ('rejected','temporary_failure') THEN 1 ELSE 0 END),0)
		FROM (SELECT status FROM managed_mail_deliveries WHERE site_id=? AND created_at>=? ORDER BY id DESC LIMIT 10)`, siteID, now.Add(-10*time.Minute).UTC()).Scan(&total, &failures)
	if err != nil {
		return err
	}
	if total == 10 && failures == 10 {
		_, err = s.db.ExecContext(ctx, `UPDATE managed_mail_sites SET paused_until=?,paused_reason='consecutive_failures',updated_at=? WHERE site_id=?`, until, now.UTC(), siteID)
	}
	return err
}

func (s *ManagedMailPolicyService) ProcessQuotaNotices(ctx context.Context, siteID int64) error {
	if s == nil || s.db == nil || siteID <= 0 {
		return ErrManagedMailValidation
	}
	managedMailReservationMu.Lock()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		managedMailReservationMu.Unlock()
		return err
	}
	now := s.now()
	policy, err := getManagedMailPolicyOnConn(ctx, conn, siteID, now)
	if err != nil {
		conn.Close()
		managedMailReservationMu.Unlock()
		return err
	}
	usage, err := managedMailUsageOnConn(ctx, conn, siteID, now)
	if err != nil {
		conn.Close()
		managedMailReservationMu.Unlock()
		return err
	}
	limits, err := s.GlobalLimits(ctx)
	if err != nil {
		conn.Close()
		managedMailReservationMu.Unlock()
		return err
	}
	type thresholdUse struct {
		key, label string
		used, max  int
	}
	checks := []thresholdUse{{fmt.Sprintf("site:%d", siteID), policy.Domain, usage.SiteDay, policy.DayLimit}, {"total", "全部网站", usage.TotalDay, limits.DayLimit}}
	emailMessages := make([]string, 0, 2)
	for _, check := range checks {
		if check.max <= 0 {
			continue
		}
		percent := check.used * 100 / check.max
		for _, threshold := range []int{70, 90, 100} {
			if percent >= threshold {
				message, err := s.recordQuotaNotice(ctx, now, check.key, check.label, check.used, check.max, threshold)
				if err != nil {
					conn.Close()
					managedMailReservationMu.Unlock()
					return err
				}
				if message != "" {
					emailMessages = append(emailMessages, message)
				}
			}
		}
	}
	conn.Close()
	managedMailReservationMu.Unlock()
	for _, message := range emailMessages {
		go func(message string) {
			_ = managedMailQuotaEmail("", getPanelTitle()+" WordPress 托管发信额度提醒", "<p>"+html.EscapeString(message)+"</p>")
		}(message)
	}
	return nil
}

func (s *ManagedMailPolicyService) recordQuotaNotice(ctx context.Context, now time.Time, key, label string, used, limit, threshold int) (string, error) {
	var raw string
	_ = s.db.QueryRowContext(ctx, `SELECT svalue FROM security_settings WHERE skey='managed_mail_quota_notice_state'`).Scan(&raw)
	state := managedMailNoticeState{}
	_ = json.Unmarshal([]byte(raw), &state)
	day := now.Format("2006-01-02")
	if state.Day != day || state.Notices == nil {
		state = managedMailNoticeState{Day: day, Notices: map[string]bool{}}
	}
	noticeKey := fmt.Sprintf("%s:%d", key, threshold)
	if state.Notices[noticeKey] {
		return "", nil
	}
	state.Notices[noticeKey] = true
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	message := fmt.Sprintf("WordPress 托管发信额度：%s 当日已使用 %d/%d（%d%% 阈值）", label, used, limit, threshold)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO alert_log(alert_type,level,message,resolved) VALUES('alert_managed_mail_quota',?,?,0)`, map[bool]string{true: "critical", false: "info"}[threshold >= 90], message); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_settings SET svalue=?,updated_at=CURRENT_TIMESTAMP WHERE skey='managed_mail_quota_notice_state'`, string(encoded)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if threshold == 90 {
		return message, nil
	}
	return "", nil
}

func getManagedMailPolicyOnConn(ctx context.Context, conn *sql.Conn, siteID int64, now time.Time) (ManagedMailSitePolicy, error) {
	var p ManagedMailSitePolicy
	var authorized, confirmed int
	var paused sql.NullTime
	err := conn.QueryRowContext(ctx, `SELECT w.id,w.domain,w.status,m.authorized,m.site_confirmed,m.minute_limit,m.hour_limit,m.day_limit,m.conflict_plugin,m.paused_until,m.paused_reason FROM websites w JOIN managed_mail_sites m ON m.site_id=w.id WHERE w.id=? AND w.site_type='wordpress'`, siteID).Scan(&p.SiteID, &p.Domain, &p.SiteStatus, &authorized, &confirmed, &p.MinuteLimit, &p.HourLimit, &p.DayLimit, &p.ConflictPlugin, &paused, &p.PausedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedMailSitePolicy{}, ErrManagedMailUnavailable
	}
	if err != nil {
		return ManagedMailSitePolicy{}, err
	}
	p.Authorized = authorized == 1
	p.SiteConfirmed = confirmed == 1
	if paused.Valid {
		t := paused.Time
		p.PausedUntil = &t
	}
	return p, nil
}

func managedMailUsageOnConn(ctx context.Context, conn *sql.Conn, siteID int64, now time.Time) (ManagedMailUsage, error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UTC()
	counted := `status IN ('reserved','accepted','unknown')`
	var usage ManagedMailUsage
	queries := []struct {
		target *int
		query  string
		args   []any
	}{
		{&usage.SiteMinute, `SELECT COALESCE(SUM(recipient_count),0) FROM managed_mail_deliveries WHERE site_id=? AND ` + counted + ` AND created_at>=?`, []any{siteID, now.Add(-time.Minute).UTC()}},
		{&usage.SiteHour, `SELECT COALESCE(SUM(recipient_count),0) FROM managed_mail_deliveries WHERE site_id=? AND ` + counted + ` AND created_at>=?`, []any{siteID, now.Add(-time.Hour).UTC()}},
		{&usage.SiteDay, `SELECT COALESCE(SUM(recipient_count),0) FROM managed_mail_deliveries WHERE site_id=? AND ` + counted + ` AND created_at>=?`, []any{siteID, dayStart}},
		{&usage.TotalMinute, `SELECT COALESCE(SUM(recipient_count),0) FROM managed_mail_deliveries WHERE ` + counted + ` AND created_at>=?`, []any{now.Add(-time.Minute).UTC()}},
		{&usage.TotalHour, `SELECT COALESCE(SUM(recipient_count),0) FROM managed_mail_deliveries WHERE ` + counted + ` AND created_at>=?`, []any{now.Add(-time.Hour).UTC()}},
		{&usage.TotalDay, `SELECT COALESCE(SUM(recipient_count),0) FROM managed_mail_deliveries WHERE ` + counted + ` AND created_at>=?`, []any{dayStart}},
	}
	for _, item := range queries {
		if err := conn.QueryRowContext(ctx, item.query, item.args...).Scan(item.target); err != nil {
			return ManagedMailUsage{}, err
		}
	}
	return usage, nil
}

func validManagedMailAddress(value string) bool {
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	return err == nil && parsed.Address == strings.TrimSpace(value)
}
