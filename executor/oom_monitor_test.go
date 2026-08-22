package executor

import (
	"strings"
	"testing"
	"time"
)

func TestParseOOMEvents(t *testing.T) {
	logLine := "1785067200.125000 host kernel: Out of memory: Killed process 4321 (mariadbd) total-vm:123kB"
	events := parseOOMEvents(logLine)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].PID != 4321 || events[0].Process != "mariadbd" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
	if events[0].OccurredAt.Unix() != 1785067200 {
		t.Fatalf("occurred_at = %v", events[0].OccurredAt)
	}
}

func TestRecordOOMEventDeduplicates(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE security_settings (
		skey TEXT PRIMARY KEY,
		svalue TEXT NOT NULL
	)`)
	mustExec(t, db, `INSERT INTO security_settings (skey, svalue) VALUES ('alert_oom', 'true')`)
	mustExec(t, db, `CREATE TABLE system_oom_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_key TEXT NOT NULL UNIQUE,
		process TEXT NOT NULL,
		pid INTEGER NOT NULL,
		message TEXT NOT NULL,
		occurred_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		alert_type TEXT NOT NULL,
		level TEXT NOT NULL,
		message TEXT NOT NULL,
		resolved INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)

	event := oomEvent{
		Key:        "1785067200.125000:4321:mariadbd",
		Process:    "mariadbd",
		PID:        4321,
		OccurredAt: time.Unix(1785067200, 125000000).UTC(),
	}
	if err := recordOOMEvent(event); err != nil {
		t.Fatalf("first recordOOMEvent: %v", err)
	}
	if err := recordOOMEvent(event); err != nil {
		t.Fatalf("duplicate recordOOMEvent: %v", err)
	}

	var eventCount, alertCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM system_oom_events").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM alert_log").Scan(&alertCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || alertCount != 1 {
		t.Fatalf("eventCount=%d alertCount=%d, want 1 and 1", eventCount, alertCount)
	}
	var message string
	var resolved int
	if err := db.QueryRow("SELECT message, resolved FROM alert_log").Scan(&message, &resolved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "MariaDB") || !strings.Contains(message, "PID 4321") {
		t.Fatalf("unexpected alert message: %q", message)
	}
	if resolved != 1 {
		t.Fatalf("OOM event resolved=%d, want 1 for one-time event", resolved)
	}
}

func TestOOMProcessLabel(t *testing.T) {
	if got := oomProcessLabel("php-fpm8.4"); got != "PHP-FPM" {
		t.Fatalf("label = %q", got)
	}
	if got := oomProcessLabel("custom-worker"); got != "custom-worker" {
		t.Fatalf("unknown process label = %q", got)
	}
}

func TestRecordOOMEventPersistsWhenAlertDisabled(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT NOT NULL)`)
	mustExec(t, db, `INSERT INTO security_settings (skey, svalue) VALUES ('alert_oom', 'false')`)
	mustExec(t, db, `CREATE TABLE system_oom_events (
		id INTEGER PRIMARY KEY, event_key TEXT NOT NULL UNIQUE, process TEXT NOT NULL,
		pid INTEGER NOT NULL, message TEXT NOT NULL, occurred_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	mustExec(t, db, `CREATE TABLE alert_log (
		id INTEGER PRIMARY KEY, alert_type TEXT NOT NULL, level TEXT NOT NULL,
		message TEXT NOT NULL, resolved INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)

	event := oomEvent{Key: "disabled-event", Process: "nginx", PID: 99, OccurredAt: time.Now().UTC()}
	if err := recordOOMEvent(event); err != nil {
		t.Fatalf("recordOOMEvent: %v", err)
	}
	var events, alerts int
	_ = db.QueryRow("SELECT COUNT(*) FROM system_oom_events").Scan(&events)
	_ = db.QueryRow("SELECT COUNT(*) FROM alert_log").Scan(&alerts)
	if events != 1 || alerts != 0 {
		t.Fatalf("events=%d alerts=%d, want 1 and 0", events, alerts)
	}
}
