package middleware

import (
	"fmt"
	"testing"
	"time"
)

func insertTestBan(t *testing.T, tracker *LoginAttemptTracker, ip, jail string) {
	t.Helper()
	expires := time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")
	if _, err := tracker.DB.Exec(
		`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, expires_at, ban_count)
		 VALUES (?, 2, 'test', ?, ?, 1)`,
		ip, jail, expires,
	); err != nil {
		t.Fatalf("insert test ban: %v", err)
	}
}

func TestIsBannedIgnoresWppanelLoginSource(t *testing.T) {
	db := newScanDefenseTestDB(t)
	tracker := &LoginAttemptTracker{DB: db}
	insertTestBan(t, tracker, "203.0.113.10", "wppanel-login")

	if tracker.IsBanned("203.0.113.10") {
		t.Fatal("a wppanel-login-only ban must not block panel access")
	}
}

func TestIsBannedHonorsOtherSources(t *testing.T) {
	db := newScanDefenseTestDB(t)
	tracker := &LoginAttemptTracker{DB: db}

	jails := []string{"wppanel", "wppanel-404", "wppanel-sshd", "panel", "panel_scan", "manual"}
	for i, jail := range jails {
		ip := fmt.Sprintf("203.0.113.%d", i+1)
		insertTestBan(t, tracker, ip, jail)
		if !tracker.IsBanned(ip) {
			t.Fatalf("a %s ban should still block panel access", jail)
		}
	}
}
