package database

import (
	"fmt"
	"testing"
)

func TestFirewallBanHistoryKeepsLatestThousandWithoutTouchingCurrentBans(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO firewall_bans
		(ip_address,ban_level,reason,source_jail,expires_at)
		VALUES ('203.0.113.10',2,'current','wppanel',datetime('now','+1 hour'))`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1005; i++ {
		if _, err := DB.Exec(`INSERT INTO firewall_ban_history
			(ip_address,ban_level,reason,source_jail,banned_at,duration_seconds)
			VALUES (?,2,'history','wppanel',datetime('now',?),600)`,
			fmt.Sprintf("198.51.100.%d", i), fmt.Sprintf("+%d seconds", i)); err != nil {
			t.Fatal(err)
		}
	}

	var historyCount, currentCount, oldestID int
	if err := DB.QueryRow(`SELECT COUNT(*),MIN(id) FROM firewall_ban_history`).Scan(&historyCount, &oldestID); err != nil {
		t.Fatal(err)
	}
	if err := DB.QueryRow(`SELECT COUNT(*) FROM firewall_bans`).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != 1000 || oldestID != 6 || currentCount != 1 {
		t.Fatalf("history=%d oldest=%d current=%d", historyCount, oldestID, currentCount)
	}
	if _, err := DB.Exec(`UPDATE firewall_ban_history SET ip_address='192.0.2.200'`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`DELETE FROM firewall_ban_history`); err != nil {
		t.Fatal(err)
	}
	if err := DB.QueryRow(`SELECT COUNT(*) FROM firewall_bans
		WHERE ip_address='203.0.113.10' AND unbanned_at IS NULL`).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if currentCount != 1 {
		t.Fatalf("editing or clearing history changed current bans: %d", currentCount)
	}
}
