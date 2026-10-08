package handlers

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/middleware"
	"golang.org/x/crypto/bcrypt"
)

func setupRootPasswordHandlerTest(t *testing.T, change func(context.Context, string) error) *SettingsHandler {
	t.Helper()
	setupBackupOverviewTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("panel-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO admin_users(id,username,password_hash) VALUES(1,'admin',?)`, string(hash)); err != nil {
		t.Fatal(err)
	}
	return &SettingsHandler{ChangeRootPassword: change}
}

func changeRootPasswordRequest(t *testing.T, handler *SettingsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/settings/root-password", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateRootPassword(ctx)
	return recorder
}

func TestUpdateRootPasswordRequiresCurrentPanelPassword(t *testing.T) {
	called := false
	handler := setupRootPasswordHandlerTest(t, func(context.Context, string) error { called = true; return nil })
	recorder := changeRootPasswordRequest(t, handler, `{"current_panel_password":"wrong","new_password":"new-password","confirm_password":"new-password"}`)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
}

func TestUpdateRootPasswordRejectsMissingCurrentPasswordAndMalformedJSON(t *testing.T) {
	for _, body := range []string{
		`{"new_password":"new-password","confirm_password":"new-password"}`,
		`{"current_panel_password":`,
	} {
		called := false
		handler := setupRootPasswordHandlerTest(t, func(context.Context, string) error { called = true; return nil })
		recorder := changeRootPasswordRequest(t, handler, body)
		if recorder.Code != http.StatusBadRequest || called {
			t.Fatalf("body=%q status=%d called=%t response=%s", body, recorder.Code, called, recorder.Body.String())
		}
	}
}

func TestUpdateRootPasswordValidatesSecretBeforeExecution(t *testing.T) {
	tests := []string{
		`{"current_panel_password":"panel-password","new_password":"short","confirm_password":"short"}`,
		`{"current_panel_password":"panel-password","new_password":"new-password","confirm_password":"different"}`,
		`{"current_panel_password":"panel-password","new_password":"bad:password","confirm_password":"bad:password"}`,
		`{"current_panel_password":"panel-password","new_password":"bad\npassword","confirm_password":"bad\npassword"}`,
	}
	for _, body := range tests {
		called := false
		handler := setupRootPasswordHandlerTest(t, func(context.Context, string) error { called = true; return nil })
		recorder := changeRootPasswordRequest(t, handler, body)
		if recorder.Code != http.StatusBadRequest || called {
			t.Fatalf("body=%s status=%d called=%t response=%s", body, recorder.Code, called, recorder.Body.String())
		}
	}
}

func TestUpdateRootPasswordEnforcesByteLengthBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantCode int
		wantCall bool
	}{
		{"eight bytes", "12345678", http.StatusOK, true},
		{"four Chinese characters", "密码密码", http.StatusOK, true},
		{"128 bytes", strings.Repeat("a", 128), http.StatusOK, true},
		{"129 bytes", strings.Repeat("a", 129), http.StatusBadRequest, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := setupRootPasswordHandlerTest(t, func(context.Context, string) error { called = true; return nil })
			body := `{"current_panel_password":"panel-password","new_password":"` + test.password + `","confirm_password":"` + test.password + `"}`
			recorder := changeRootPasswordRequest(t, handler, body)
			if recorder.Code != test.wantCode || called != test.wantCall {
				t.Fatalf("status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
			}
		})
	}
}

func TestUpdateRootPasswordSucceedsWithoutPersistingSecret(t *testing.T) {
	const secret = "new-root-password"
	var received string
	handler := setupRootPasswordHandlerTest(t, func(_ context.Context, password string) error { received = password; return nil })
	session := middleware.GlobalSessionStore.Create("admin")
	t.Cleanup(middleware.GlobalSessionStore.DeleteAll)
	recorder := changeRootPasswordRequest(t, handler, `{"username":"not-root","current_panel_password":"panel-password","new_password":"`+secret+`","confirm_password":"`+secret+`"}`)
	if recorder.Code != http.StatusOK || received != secret {
		t.Fatalf("status=%d received=%q body=%s", recorder.Code, received, recorder.Body.String())
	}
	var operation, target, message string
	if err := database.GetDB().QueryRow(`SELECT operation,target,message FROM operation_logs ORDER BY id DESC LIMIT 1`).Scan(&operation, &target, &message); err != nil {
		t.Fatal(err)
	}
	if operation != "root_password_change" || target != "root" || strings.Contains(message, secret) || strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("unsafe audit/response operation=%q target=%q message=%q body=%s", operation, target, message, recorder.Body.String())
	}
	if middleware.GlobalSessionStore.Get(session.Token) == nil {
		t.Fatal("root password change revoked the panel session")
	}
}

func TestUpdateRootPasswordReturnsGenericExecutionFailure(t *testing.T) {
	const secret = "new-root-password"
	var logs bytes.Buffer
	originalLogWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalLogWriter) })
	handler := setupRootPasswordHandlerTest(t, func(context.Context, string) error { return errors.New("failure " + secret) })
	recorder := changeRootPasswordRequest(t, handler, `{"current_panel_password":"panel-password","new_password":"`+secret+`","confirm_password":"`+secret+`"}`)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("secret leaked to log: %s", logs.String())
	}
	var count int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM operation_logs WHERE operation='root_password_change'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestUpdateRootPasswordDetachesFromRequestCancellation(t *testing.T) {
	called := false
	handler := setupRootPasswordHandlerTest(t, func(ctx context.Context, _ string) error {
		called = true
		if ctx.Err() != nil {
			t.Fatalf("executor inherited canceled request context: %v", ctx.Err())
		}
		return nil
	})
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/api/settings/root-password", bytes.NewBufferString(`{"current_panel_password":"panel-password","new_password":"new-password","confirm_password":"new-password"}`)).WithContext(requestContext)
	ginContext.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateRootPassword(ginContext)
	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
}

func TestUpdateRootPasswordRequiresCSRF(t *testing.T) {
	called := false
	handler := setupRootPasswordHandlerTest(t, func(context.Context, string) error { called = true; return nil })
	router := gin.New()
	router.POST("/api/settings/root-password", middleware.CSRF(), handler.UpdateRootPassword)
	body := `{"current_panel_password":"panel-password","new_password":"new-root-password","confirm_password":"new-root-password"}`

	req := httptest.NewRequest(http.MethodPost, "/api/settings/root-password", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("missing CSRF status=%d called=%t", recorder.Code, called)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/settings/root-password", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "matching-token")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "matching-token"})
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("matching CSRF status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
}
