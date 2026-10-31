package database

import "testing"

func TestManagedMailSchemaFreshInstallAndUpgrade(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	checkManagedMailSchema(t)

	if _, err := DB.Exec(`DROP TABLE managed_mail_deliveries;
DROP TABLE managed_mail_sites;
DELETE FROM security_settings WHERE skey='smtp_from_email';
DELETE FROM schema_version;
INSERT INTO schema_version(version) VALUES('1.0.67')`); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	checkManagedMailSchema(t)

	// CREATE/INSERT statements are deliberately idempotent for an interrupted
	// upgrade retry and for the migrations pass on every panel start.
	for _, statement := range managedMailSchemaStatements {
		if _, err := DB.Exec(statement); err != nil {
			t.Fatalf("idempotent statement failed: %v", err)
		}
	}
}

func checkManagedMailSchema(t *testing.T) {
	t.Helper()
	for _, table := range []string{"managed_mail_sites", "managed_mail_deliveries"} {
		var count int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	var settings int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM security_settings WHERE skey IN (
		'smtp_from_email','managed_mail_limit_preset','managed_mail_total_minute_limit',
		'managed_mail_total_hour_limit','managed_mail_total_day_limit','managed_mail_max_recipients',
		'managed_mail_max_message_bytes','managed_mail_max_attachments','managed_mail_system_day_limit',
		'managed_mail_quota_notice_state')`).Scan(&settings); err != nil || settings != 10 {
		t.Fatalf("managed mail settings=%d err=%v", settings, err)
	}
	if LatestVersion() != "1.0.69" {
		t.Fatalf("latest version=%s", LatestVersion())
	}
}
