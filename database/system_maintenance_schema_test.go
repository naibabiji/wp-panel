package database

import "testing"

func TestSystemMaintenanceMigrationPreservesAdministratorChoice(t *testing.T) {
	for _, old := range []string{"missing", "false", "true"} {
		t.Run(old, func(t *testing.T) {
			openTempDB(t)
			if err := RunMigrations(); err != nil {
				t.Fatal(err)
			}
			if err := RunUpgrades(); err != nil {
				t.Fatal(err)
			}
			// Model an old installation, including the normal migrations-before-
			// upgrades startup order, not just execution of the upgrade SQL.
			if _, err := DB.Exec(`DELETE FROM security_settings WHERE skey LIKE 'system_maintenance_%';
				DROP TABLE system_maintenance_actions; DELETE FROM schema_version;
				INSERT INTO schema_version(version) VALUES('1.0.70')`); err != nil {
				t.Fatal(err)
			}
			if old != "missing" {
				if _, err := DB.Exec(`INSERT INTO security_settings(skey,svalue) VALUES('system_package_auto_update_enabled',?)`, old); err != nil {
					t.Fatal(err)
				}
			}
			if err := RunMigrations(); err != nil {
				t.Fatal(err)
			}
			if err := RunUpgrades(); err != nil {
				t.Fatal(err)
			}
			want := "false"
			if old == "true" {
				want = "true"
			}
			checkMaintenanceSetting(t, "system_maintenance_enabled", want)
			checkMaintenanceSetting(t, "system_maintenance_mode", "scheduled")
			checkMaintenanceSetting(t, "system_maintenance_window", "04:00-06:00")
			var notices int
			if err := DB.QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE kind='notice' AND target='migration'`).Scan(&notices); err != nil {
				t.Fatal(err)
			}
			if (notices == 1) != (old == "true") {
				t.Fatalf("old=%s notices=%d", old, notices)
			}
			if _, err := DB.Exec(`UPDATE security_settings SET svalue='false' WHERE skey='system_maintenance_enabled';
				UPDATE security_settings SET svalue='immediate' WHERE skey='system_maintenance_mode'`); err != nil {
				t.Fatal(err)
			}
			if err := RunMigrations(); err != nil {
				t.Fatal(err)
			}
			if err := RunUpgrades(); err != nil {
				t.Fatal(err)
			}
			checkMaintenanceSetting(t, "system_maintenance_enabled", "false")
			checkMaintenanceSetting(t, "system_maintenance_mode", "immediate")
		})
	}
}

func TestSystemMaintenanceFreshInstallAndConstraints(t *testing.T) {
	openTempDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	checkMaintenanceSetting(t, "system_maintenance_enabled", "false")
	for _, query := range []string{
		`INSERT INTO system_maintenance_actions(kind,target) VALUES('shell','bad')`,
		`INSERT INTO system_maintenance_actions(kind,target,status) VALUES('service','nginx','ignored')`,
		`INSERT INTO system_maintenance_actions(kind,target,attempts) VALUES('service','nginx',-1)`,
	} {
		if _, err := DB.Exec(query); err == nil {
			t.Fatalf("invalid action accepted: %s", query)
		}
	}
	if _, err := DB.Exec(`INSERT INTO system_maintenance_actions(kind,target) VALUES('service','nginx')`); err != nil {
		t.Fatal(err)
	}
	if _, err := DB.Exec(`INSERT INTO system_maintenance_actions(kind,target) VALUES('service','nginx')`); err == nil {
		t.Fatal("duplicate budget owner accepted")
	}
}

func checkMaintenanceSetting(t *testing.T, key, want string) {
	t.Helper()
	var got string
	if err := DB.QueryRow(`SELECT svalue FROM security_settings WHERE skey=?`, key).Scan(&got); err != nil || got != want {
		t.Fatalf("%s=%q want %q, err=%v", key, got, want, err)
	}
}
