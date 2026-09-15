package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type fakeAIDevelopmentSystem struct {
	passwd       aiDevelopmentPasswd
	calls        []string
	errAt        string
	onCall       func(string)
	configureErr func(force bool) error
}

func (f *fakeAIDevelopmentSystem) record(name string) error {
	f.calls = append(f.calls, name)
	if f.onCall != nil {
		f.onCall(name)
	}
	if f.errAt == name {
		return errors.New("injected failure")
	}
	return nil
}

func (f *fakeAIDevelopmentSystem) LookupPasswd(context.Context, string) (aiDevelopmentPasswd, error) {
	return f.passwd, f.record("lookup")
}
func (f *fakeAIDevelopmentSystem) Configure(_ context.Context, _ AIDevelopmentSite, _, _ string, force bool) error {
	name := "configure"
	if force {
		name = "configure-force"
	}
	if err := f.record(name); err != nil {
		return err
	}
	if f.configureErr != nil {
		return f.configureErr(force)
	}
	return nil
}
func (f *fakeAIDevelopmentSystem) UpdateHandoff(context.Context, AIDevelopmentSite, string) error {
	return f.record("handoff")
}
func (f *fakeAIDevelopmentSystem) RevokeKey(context.Context, string) error {
	return f.record("revoke")
}
func (f *fakeAIDevelopmentSystem) InstallKey(context.Context, string, string) error {
	return f.record("install")
}
func (f *fakeAIDevelopmentSystem) TerminateSessions(context.Context, string) error {
	return f.record("terminate")
}
func (f *fakeAIDevelopmentSystem) Restore(context.Context, string, aiDevelopmentPasswd) error {
	return f.record("restore")
}
func (f *fakeAIDevelopmentSystem) RemoveHome(string) error { return f.record("remove-home") }

func openAIDevelopmentTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE website_ai_development_access (
		site_id INTEGER PRIMARY KEY, status TEXT NOT NULL, operation TEXT NOT NULL DEFAULT '',
		system_user TEXT NOT NULL, web_root TEXT NOT NULL, original_shell TEXT NOT NULL,
		original_home TEXT NOT NULL, public_key TEXT NOT NULL DEFAULT '', key_fingerprint TEXT NOT NULL DEFAULT '',
		requested_by TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', enabled_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE websites (
		id INTEGER PRIMARY KEY, domain TEXT NOT NULL, system_user TEXT NOT NULL,
		web_root TEXT NOT NULL, log_dir TEXT NOT NULL DEFAULT '', php_pool_path TEXT NOT NULL DEFAULT '',
		nginx_conf_path TEXT NOT NULL DEFAULT '', db_name TEXT NOT NULL, db_user TEXT NOT NULL
	);
	CREATE TABLE site_migration_locks (
		id INTEGER PRIMARY KEY AUTOINCREMENT, domain TEXT NOT NULL, site_id INTEGER,
		migration_site_id TEXT NOT NULL, direction TEXT NOT NULL, status TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRefreshEnabledHandoffsUsesCurrentServerDocuments(t *testing.T) {
	db := openAIDevelopmentTestDB(t)
	if _, err := db.Exec(`INSERT INTO websites(id,domain,system_user,web_root,db_name,db_user)
		VALUES (7,'example.com','wp_example','/var/www/example','db_example','db_example'),
		       (8,'disabled.example','wp_disabled','/var/www/disabled','db_disabled','db_disabled');
		INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint)
		VALUES (7,'enabled','wp_example','/var/www/example','/usr/sbin/nologin','/nonexistent','key','fingerprint'),
		       (8,'error','wp_disabled','/var/www/disabled','/usr/sbin/nologin','/nonexistent','','');`); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAIDevelopmentSystem{}
	service := &AIDevelopmentAccessService{db: db, system: fake}
	if err := service.RefreshEnabledHandoffs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"handoff"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls=%v want=%v", fake.calls, want)
	}
}

func TestAIDevelopmentCredentialRotationTerminatesOldSessions(t *testing.T) {
	db := openAIDevelopmentTestDB(t)
	_, err := db.Exec(`INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint)
		VALUES (7,'enabled','wp_example','/var/www/example','/usr/sbin/nologin','/nonexistent','old','old-fingerprint')`)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAIDevelopmentSystem{}
	service := &AIDevelopmentAccessService{db: db, system: fake}
	if err := service.Rotate(context.Background(), AIDevelopmentSite{ID: 7, Domain: "example.com", SystemUser: "wp_example", WebRoot: "/var/www/example"}, "ssh-ed25519 new", "new-fingerprint"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"revoke", "terminate", "handoff", "install"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls=%v want=%v", fake.calls, want)
	}
	var status, operation, key string
	if err := db.QueryRow(`SELECT status,operation,public_key FROM website_ai_development_access WHERE site_id=7`).Scan(&status, &operation, &key); err != nil {
		t.Fatal(err)
	}
	if status != "enabled" || operation != "" || key != "ssh-ed25519 new" {
		t.Fatalf("unexpected final state: status=%q operation=%q key=%q", status, operation, key)
	}
}

func TestAIDevelopmentRotationFailureRevokesStoredCredential(t *testing.T) {
	db := openAIDevelopmentTestDB(t)
	_, err := db.Exec(`INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint)
		VALUES (7,'enabled','wp_example','/var/www/example','/usr/sbin/nologin','/nonexistent','old','old-fingerprint')`)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAIDevelopmentSystem{errAt: "terminate"}
	service := &AIDevelopmentAccessService{db: db, system: fake}
	if err := service.Rotate(context.Background(), AIDevelopmentSite{ID: 7, Domain: "example.com", SystemUser: "wp_example", WebRoot: "/var/www/example"}, "ssh-ed25519 new", "new-fingerprint"); err == nil {
		t.Fatal("Rotate() succeeded, want failure")
	}
	var status, operation, key string
	if err := db.QueryRow(`SELECT status,operation,public_key FROM website_ai_development_access WHERE site_id=7`).Scan(&status, &operation, &key); err != nil {
		t.Fatal(err)
	}
	if status != "error" || operation != "rotate" || key != "" {
		t.Fatalf("unexpected failure state: status=%q operation=%q key=%q", status, operation, key)
	}
}

func TestAIDevelopmentReconcilePendingFailsClosed(t *testing.T) {
	db := openAIDevelopmentTestDB(t)
	_, err := db.Exec(`INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint)
		VALUES (7,'enabling','wp_example','/var/www/example','/usr/sbin/nologin','/nonexistent','new','new-fingerprint')`)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAIDevelopmentSystem{}
	service := &AIDevelopmentAccessService{db: db, system: fake}
	if err := service.ReconcilePending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"revoke", "terminate", "restore", "remove-home"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls=%v want=%v", fake.calls, want)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM website_ai_development_access WHERE site_id=7`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pending record count=%d", count)
	}
}

func TestAIDevelopmentReconcilePendingContinuesAfterSiteFailure(t *testing.T) {
	db := openAIDevelopmentTestDB(t)
	for _, siteID := range []int{7, 8} {
		if _, err := db.Exec(`INSERT INTO website_ai_development_access
			(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint)
			VALUES (?,'enabling',?,?,'/usr/sbin/nologin','/nonexistent','new','new-fingerprint')`,
			siteID, fmt.Sprintf("wp_example_%d", siteID), fmt.Sprintf("/var/www/example-%d", siteID)); err != nil {
			t.Fatal(err)
		}
	}

	revokeCalls := 0
	fake := &fakeAIDevelopmentSystem{errAt: "revoke"}
	fake.onCall = func(name string) {
		if name == "revoke" {
			revokeCalls++
			if revokeCalls == 2 {
				fake.errAt = ""
			}
		}
	}
	service := &AIDevelopmentAccessService{db: db, system: fake}
	err := service.ReconcilePending(context.Background())
	if err == nil || !strings.Contains(err.Error(), "site 7") {
		t.Fatalf("ReconcilePending() error = %v, want site 7 failure", err)
	}
	if want := []string{"revoke", "revoke", "terminate", "restore", "remove-home"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls=%v want=%v", fake.calls, want)
	}
	var firstStatus string
	if err := db.QueryRow(`SELECT status FROM website_ai_development_access WHERE site_id=7`).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "error" {
		t.Fatalf("failed site status=%q want error", firstStatus)
	}
	var secondCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM website_ai_development_access WHERE site_id=8`).Scan(&secondCount); err != nil {
		t.Fatal(err)
	}
	if secondCount != 0 {
		t.Fatalf("later pending record count=%d want 0", secondCount)
	}
}

func TestAIDevelopmentEnableSurfacesBusyErrorAndHonorsForce(t *testing.T) {
	site := AIDevelopmentSite{ID: 9, Domain: "example.com", SystemUser: "wp_example", WebRoot: t.TempDir(), DBName: "db_example", DBUser: "db_example"}
	noopVerify := func(context.Context, AIDevelopmentSite) error { return nil }

	t.Run("busy without force is reported as ErrAIDevelopmentSiteBusy", func(t *testing.T) {
		db := openAIDevelopmentTestDB(t)
		fake := &fakeAIDevelopmentSystem{
			passwd: aiDevelopmentPasswd{Home: "/nonexistent", Shell: "/usr/sbin/nologin", UID: 1, GID: 1},
			configureErr: func(force bool) error {
				if force {
					return nil
				}
				return fmt.Errorf("%w: exit status 8", ErrAIDevelopmentSiteBusy)
			},
		}
		service := &AIDevelopmentAccessService{db: db, system: fake, verify: noopVerify}
		err := service.Enable(context.Background(), site, "ssh-ed25519 key", "fingerprint", "tester", false)
		if !errors.Is(err, ErrAIDevelopmentSiteBusy) {
			t.Fatalf("Enable() err=%v, want ErrAIDevelopmentSiteBusy", err)
		}
		if want := []string{"lookup", "configure", "restore", "remove-home"}; !reflect.DeepEqual(fake.calls, want) {
			t.Fatalf("calls=%v want=%v", fake.calls, want)
		}
	})

	t.Run("force is passed through to Configure and can succeed", func(t *testing.T) {
		db := openAIDevelopmentTestDB(t)
		fake := &fakeAIDevelopmentSystem{
			passwd: aiDevelopmentPasswd{Home: "/nonexistent", Shell: "/usr/sbin/nologin", UID: 1, GID: 1},
		}
		service := &AIDevelopmentAccessService{db: db, system: fake, verify: noopVerify}
		if err := service.Enable(context.Background(), site, "ssh-ed25519 key", "fingerprint", "tester", true); err != nil {
			t.Fatalf("Enable(force=true) failed: %v", err)
		}
		if want := []string{"lookup", "configure-force"}; !reflect.DeepEqual(fake.calls, want) {
			t.Fatalf("calls=%v want=%v", fake.calls, want)
		}
	})
}

func TestAIDevelopmentEnableAndRotateRejectActiveMigration(t *testing.T) {
	for _, operation := range []string{"enable", "rotate"} {
		t.Run(operation, func(t *testing.T) {
			db := openAIDevelopmentTestDB(t)
			if _, err := db.Exec(`INSERT INTO site_migration_locks(domain,site_id,migration_site_id,direction,status)
				VALUES ('example.com',7,'migration_0000001','source','active')`); err != nil {
				t.Fatal(err)
			}
			if operation == "rotate" {
				if _, err := db.Exec(`INSERT INTO website_ai_development_access
					(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint)
					VALUES (7,'enabled','wp_example','/var/www/example','/usr/sbin/nologin','/nonexistent','old','old-fingerprint')`); err != nil {
					t.Fatal(err)
				}
			}
			fake := &fakeAIDevelopmentSystem{passwd: aiDevelopmentPasswd{Home: "/nonexistent", Shell: "/usr/sbin/nologin"}}
			service := &AIDevelopmentAccessService{db: db, system: fake, verify: func(context.Context, AIDevelopmentSite) error { return nil }}
			site := AIDevelopmentSite{ID: 7, Domain: "example.com", SystemUser: "wp_example", WebRoot: t.TempDir(), DBName: "db_example", DBUser: "db_example"}
			var err error
			if operation == "enable" {
				err = service.Enable(context.Background(), site, "ssh-ed25519 key", "fingerprint", "tester", false)
			} else {
				err = service.Rotate(context.Background(), site, "ssh-ed25519 key", "fingerprint")
			}
			if !errors.Is(err, errSiteMigrationBusy) {
				t.Fatalf("error=%v want errSiteMigrationBusy", err)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("system calls=%v", fake.calls)
			}
		})
	}
}

func TestAIDevelopmentRotationDoesNotReportSuccessAfterStateDisappears(t *testing.T) {
	db := openAIDevelopmentTestDB(t)
	_, err := db.Exec(`INSERT INTO website_ai_development_access
		(site_id,status,system_user,web_root,original_shell,original_home,public_key,key_fingerprint)
		VALUES (7,'enabled','wp_example','/var/www/example','/usr/sbin/nologin','/nonexistent','old','old-fingerprint')`)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAIDevelopmentSystem{onCall: func(name string) {
		if name == "install" {
			if _, deleteErr := db.Exec(`DELETE FROM website_ai_development_access WHERE site_id=7`); deleteErr != nil {
				t.Errorf("delete concurrent state: %v", deleteErr)
			}
		}
	}}
	service := &AIDevelopmentAccessService{db: db, system: fake}
	if err := service.Rotate(context.Background(), AIDevelopmentSite{ID: 7, Domain: "example.com", SystemUser: "wp_example", WebRoot: "/var/www/example"}, "ssh-ed25519 new", "new-fingerprint"); err == nil {
		t.Fatal("Rotate() succeeded after its state row disappeared")
	}
}

func TestAIDevelopmentHandoffDescribesSiteAndPanelBoundaries(t *testing.T) {
	handoff := buildAIDevelopmentHandoff(AIDevelopmentSite{
		Domain: "example.com", SystemUser: "wp_example", WebRoot: "/var/www/example",
		LogDir: "/www/wwwlogs/example.com", PHPPoolPath: "/etc/php/8.3/fpm/pool.d/wp_example.conf",
		NginxConfPath: "/etc/nginx/sites-available/wp_example.conf",
	}, "SHA256:test", time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC))
	for _, required := range []string{"Panel version:", "Generated at: 2026-09-15T01:02:03Z", "Site: example.com", "WebRoot: /var/www/example", "Site PHP-FPM pool config: /etc/php/8.3/fpm/pool.d/wp_example.conf (read-only, managed by WP Panel)", "Site Nginx config: /etc/nginx/sites-available/wp_example.conf (read-only, managed by WP Panel)", "Site log directory: /www/wwwlogs/example.com", "php_admin_value", "CLI PHP and the global php.ini", "server-side handoff", "authoritative", "WP-PANEL-CAPABILITIES.md", "aaPanel/BT Panel", "cannot coexist", "ask the user to check the documented WP Panel page"} {
		if !strings.Contains(handoff, required) {
			t.Fatalf("handoff missing %q: %s", required, handoff)
		}
	}
	for _, forbidden := range []string{"AI-CONTEXT.md", "DEVELOPMENT-PLAN.md", "AI-CHANGELOG.md", "Git is optional", "wait for explicit approval"} {
		if strings.Contains(handoff, forbidden) {
			t.Fatalf("handoff unexpectedly contains development guidance %q: %s", forbidden, handoff)
		}
	}
}

func TestAIDevelopmentCapabilitiesDescribePanelWorkflowsAndLimits(t *testing.T) {
	capabilities := buildAIDevelopmentCapabilities(time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC))
	for _, required := range []string{
		"capability directory, not a live status report",
		"Capabilities schema version: 1",
		"Generated at: 2026-09-15T01:02:03Z",
		"test build and cannot be compared",
		"Nginx Custom Configuration",
		"Backup Overview",
		"WordPress inventory and updates",
		"CDN Real IP",
		"Request protection and banned IPs",
		"Managed software and server state",
		"Site migration",
		"WP Panel currently has no confirmed entry",
	} {
		if !strings.Contains(capabilities, required) {
			t.Fatalf("capabilities missing %q: %s", required, capabilities)
		}
	}
	seen := make(map[string]bool, len(aiDevelopmentCapabilities))
	for _, capability := range aiDevelopmentCapabilities {
		if capability.ID == "" || seen[capability.ID] {
			t.Fatalf("invalid or duplicate public capability ID %q", capability.ID)
		}
		seen[capability.ID] = true
		if !strings.Contains(capabilities, "## "+capability.ID+" — ") {
			t.Fatalf("generated capabilities missing stable ID %q", capability.ID)
		}
	}
	for _, requiredID := range []string{"website.provisioning", "website.nginx-custom", "wordpress.inventory", "backup.remote", "security.firewall", "operations.software", "migration.site-transfer", "platform.update"} {
		if !seen[requiredID] {
			t.Fatalf("public projection missing required ID %q", requiredID)
		}
	}
	for _, forbidden := range []string{"unattended updates are supported", "MariaDB root password", "sudo apt"} {
		if strings.Contains(capabilities, forbidden) {
			t.Fatalf("capabilities unexpectedly contain %q", forbidden)
		}
	}
}

func TestReadSSHHostKnownHostsPinsOnlyPublicKeys(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "host.pub")
	if err := os.WriteFile(valid, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE3h7V1s95BrjhMuVZ7oBvP3G4XCNiCBN3qO3U1YQ6Ks server-comment\n"), 0644); err != nil {
		t.Fatal(err)
	}
	knownHosts, err := readSSHHostKnownHosts([]string{filepath.Join(dir, "missing.pub"), valid})
	if err != nil {
		t.Fatal(err)
	}
	if want := "wp-panel-ai-target ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE3h7V1s95BrjhMuVZ7oBvP3G4XCNiCBN3qO3U1YQ6Ks\n"; knownHosts != want {
		t.Fatalf("known_hosts=%q want=%q", knownHosts, want)
	}
}

func TestReadSSHHostKnownHostsFailsClosed(t *testing.T) {
	dir := t.TempDir()
	invalid := filepath.Join(dir, "invalid.pub")
	if err := os.WriteFile(invalid, []byte("not an SSH key\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSSHHostKnownHosts([]string{invalid}); err == nil {
		t.Fatal("invalid host key was accepted")
	}
	if _, err := readSSHHostKnownHosts([]string{filepath.Join(dir, "missing.pub")}); err == nil {
		t.Fatal("missing host keys were accepted")
	}
}
