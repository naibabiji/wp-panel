package middleware

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/executor"
)

type LoginAttemptTracker struct {
	DB               *sql.DB
	MaxAttempts      int
	AttemptWindow    time.Duration
	BanDurationHours int
	mu               sync.Mutex
}

func NewLoginAttemptTracker(db *sql.DB, maxAttempts int, windowMinutes int, banHours int) *LoginAttemptTracker {
	return &LoginAttemptTracker{
		DB:               db,
		MaxAttempts:      maxAttempts,
		AttemptWindow:    time.Duration(windowMinutes) * time.Minute,
		BanDurationHours: banHours,
	}
}

func (t *LoginAttemptTracker) RecordAttempt(ip string, attemptType string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	_, _ = t.DB.Exec(
		"INSERT INTO login_attempts (ip_address, attempt_type) VALUES (?, ?)",
		ip, attemptType,
	)

	count := t.countRecent(ip)
	if count >= t.MaxAttempts {
		t.banIP(ip, attemptType)
	}
}

// IsBanned gates access to the panel itself, so it deliberately excludes
// wppanel-login bans: a failed WordPress login on a hosted site produces the
// exact same log signal whether it's an attacker or the admin mistyping their
// own password, unlike the other ban sources (SSH brute force, site-wide
// flood/404 scanning, the panel's own auth failures) which legitimate admin
// activity essentially never triggers. Locking an admin out of the panel for
// typos on a site they'd need the panel to go fix is a worse outcome than
// letting that one signal not also gate panel access; the hosted site itself
// is still protected by the wppanel-login jail at the Nginx/nftables layer.
func (t *LoginAttemptTracker) IsBanned(ip string) bool {
	var count int
	err := t.DB.QueryRow(
		`SELECT COUNT(*) FROM firewall_bans
		 WHERE ip_address = ?
		 AND source_jail != 'wppanel-login'
		 AND unbanned_at IS NULL
		 AND (expires_at IS NULL OR expires_at > datetime('now'))`,
		ip,
	).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

func (t *LoginAttemptTracker) countRecent(ip string) int {
	var count int
	cutoff := time.Now().UTC().Add(-t.AttemptWindow).Format("2006-01-02 15:04:05")
	err := t.DB.QueryRow(
		`SELECT COUNT(*) FROM login_attempts
		 WHERE ip_address = ? AND created_at > ?`,
		ip, cutoff,
	).Scan(&count)
	if err != nil {
		return 0
	}
	return count
}

func (t *LoginAttemptTracker) banIP(ip string, attemptType string) {
	var existing int
	t.DB.QueryRow(`SELECT COUNT(*) FROM firewall_bans WHERE ip_address = ? AND unbanned_at IS NULL AND (expires_at IS NULL OR expires_at > datetime('now'))`, ip).Scan(&existing)
	if existing > 0 {
		return
	}

	reason := fmt.Sprintf("panel_%s: 连续%d次认证失败", attemptType, t.MaxAttempts)
	expiresAt := time.Now().UTC().Add(time.Duration(t.BanDurationHours) * time.Hour).Format("2006-01-02 15:04:05")

	_, _ = t.DB.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, expires_at, ban_count)
		 VALUES (?, 3, ?, 'panel', ?, 1)`,
		ip, reason, expiresAt,
	)

	executor.AddPersistBan(ip)
}

func (t *LoginAttemptTracker) ClearAttempts(ip string) {
	t.DB.Exec("DELETE FROM login_attempts WHERE ip_address = ?", ip)
}

func (t *LoginAttemptTracker) CleanupOldAttempts() {
	cutoff := time.Now().UTC().Add(-t.AttemptWindow).Format("2006-01-02 15:04:05")
	_, _ = t.DB.Exec("DELETE FROM login_attempts WHERE created_at < ?", cutoff)
}
