package handlers

import (
	"net/http"
	"testing"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/middleware"
	"golang.org/x/crypto/bcrypt"
)

func setupSettingsSessionTest(t *testing.T) (string, string) {
	t.Helper()
	setupBackupOverviewTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO admin_users(id, username, password_hash) VALUES(1, 'admin', ?)`, string(hash)); err != nil {
		t.Fatal(err)
	}
	middleware.GlobalSessionStore.DeleteAll()
	t.Cleanup(middleware.GlobalSessionStore.DeleteAll)
	return middleware.GlobalSessionStore.Create("admin").Token, middleware.GlobalSessionStore.Create("admin").Token
}

func requireSessions(t *testing.T, tokens []string, present bool) {
	t.Helper()
	for _, token := range tokens {
		if got := middleware.GlobalSessionStore.Get(token); (got != nil) != present {
			t.Fatalf("session %s present=%t, want %t", token, got != nil, present)
		}
	}
}

func TestUpdateSettingsRevokesAllSessionsAfterUsernameChange(t *testing.T) {
	first, second := setupSettingsSessionTest(t)
	recorder := updateSystemSetting(t, `{"username":"renamed-admin"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	requireSessions(t, []string{first, second}, false)
}

func TestUpdateSettingsKeepsSessionsWhenUsernameIsUnchanged(t *testing.T) {
	first, second := setupSettingsSessionTest(t)
	recorder := updateSystemSetting(t, `{"username":"admin"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	requireSessions(t, []string{first, second}, true)
}

func TestUpdateSettingsRevokesAllSessionsAfterPasswordChange(t *testing.T) {
	first, second := setupSettingsSessionTest(t)
	recorder := updateSystemSetting(t, `{"old_password":"old-password","new_password":"new-password"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	requireSessions(t, []string{first, second}, false)
}

func TestUpdateSettingsKeepsSessionsWhenPasswordIsUnchanged(t *testing.T) {
	first, second := setupSettingsSessionTest(t)
	recorder := updateSystemSetting(t, `{"old_password":"old-password","new_password":"old-password"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	requireSessions(t, []string{first, second}, true)
}

func TestUpdateSettingsKeepsSessionsWhenPasswordUpdateFails(t *testing.T) {
	first, second := setupSettingsSessionTest(t)
	recorder := updateSystemSetting(t, `{"old_password":"wrong-password","new_password":"new-password"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	requireSessions(t, []string{first, second}, true)
}
