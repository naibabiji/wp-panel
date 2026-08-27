package executor

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type fakeAIDevelopmentSystem struct {
	passwd aiDevelopmentPasswd
	calls  []string
	errAt  string
	onCall func(string)
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
func (f *fakeAIDevelopmentSystem) Configure(context.Context, AIDevelopmentSite, string, string) error {
	return f.record("configure")
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
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
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

func TestAIDevelopmentHandoffRequiresDiscoveryPlanAndApproval(t *testing.T) {
	handoff := buildAIDevelopmentHandoff(AIDevelopmentSite{
		Domain: "example.com", SystemUser: "wp_example", WebRoot: "/var/www/example",
	}, "SHA256:test")
	for _, required := range []string{"read-only discovery", "AI-CONTEXT.md", "DEVELOPMENT-PLAN.md", "AI-CHANGELOG.md", "wait for explicit approval", "Do not automatically create backups"} {
		if !strings.Contains(handoff, required) {
			t.Fatalf("handoff missing %q: %s", required, handoff)
		}
	}
}
