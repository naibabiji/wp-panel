package executor

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"
)

func TestSSLAlertExactRiskStages(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		remaining time.Duration
		failed    bool
		want      int
	}{
		{14*24*time.Hour + time.Second, false, 0},
		{14 * 24 * time.Hour, false, 1},
		{3 * 24 * time.Hour, false, 1},
		{3 * 24 * time.Hour, true, 2},
		{0, false, 3},
		{-time.Second, false, 3},
	} {
		if got := sslAlertStage(now.Add(tc.remaining), now, tc.failed); got != tc.want {
			t.Errorf("%v, renewal failed=%v: stage=%d want %d", tc.remaining, tc.failed, got, tc.want)
		}
	}
}

func TestSSLAlertReplacementDoesNotResolveAnotherSite(t *testing.T) {
	db := openAlertTestDB(t)
	mustExec(t, db, `CREATE TABLE security_settings (skey TEXT PRIMARY KEY, svalue TEXT)`)
	mustExec(t, db, `CREATE TABLE alert_log (id INTEGER PRIMARY KEY, alert_type TEXT, level TEXT, message TEXT, resolved INTEGER, attribution TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	now := time.Now()
	events := map[int]sslAlertEvent{}
	one := sslAlertSite{id: 1, domain: "one.example"}
	two := sslAlertSite{id: 2, domain: "two.example"}
	old := &x509.Certificate{Raw: []byte("old"), NotAfter: now.Add(-time.Minute)}
	evaluatePublishedSSL(one, old, now, events)
	evaluatePublishedSSL(two, old, now, events)
	evaluatePublishedSSL(two, old, now.Add(time.Hour), events)
	replacement := &x509.Certificate{Raw: []byte("replacement"), NotAfter: now.Add(30 * 24 * time.Hour)}
	evaluatePublishedSSL(one, replacement, now, events)
	if len(events) != 1 || events[two.id].Stage != 3 {
		t.Fatalf("site states after a single replacement: %+v", events)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM alert_log WHERE message LIKE ? AND resolved=0`, "%"+two.domain+"%").Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeat records=%d err=%v", count, err)
	}
	var recovered string
	if err := db.QueryRow(`SELECT message FROM alert_log WHERE level='info'`).Scan(&recovered); err != nil || !strings.Contains(recovered, one.domain) {
		t.Fatalf("recovery=%q err=%v", recovered, err)
	}
}

func TestSSLAlertAdoptsLegacyExpiredStageWithoutBackfill(t *testing.T) {
	now := time.Now()
	site := sslAlertSite{id: 1, domain: "one.example"}
	legacy := "other.example 证书 3 天后到期；one.example 证书已过期 4 天"
	events := map[int]sslAlertEvent{site.id: {Stage: legacySSLStage(site.domain, legacy)}}
	cert := &x509.Certificate{Raw: []byte("served"), NotAfter: now.Add(-time.Second)}
	evaluatePublishedSSL(site, cert, now, events)
	observed := events[site.id]
	if observed.Stage != 3 || observed.Fingerprint == "" || observed.MailTarget != "" {
		t.Fatalf("legacy case should adopt stage and never manufacture a receipt: %+v", observed)
	}
}
