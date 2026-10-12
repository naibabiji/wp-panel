package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

func setupManagedMailHandlerTest(t *testing.T) (*gin.Engine, int64, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previous := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "managed-mail-handler.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	t.Cleanup(func() {
		_ = db.Close()
		database.DB = previous
	})
	if err := database.RunMigrations(); err != nil {
		t.Fatal(err)
	}
	if err := database.RunUpgrades(); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 40)
	result, err := db.Exec(`INSERT INTO websites
		(name,domain,status,site_type,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path,plugin_api_key)
		VALUES ('Managed Mail','mail.example.test','active','wordpress','wp_mail','/srv/mail','/var/log/mail','wp_mail','wp_mail','/etc/php/mail.conf','/etc/nginx/mail.conf',?)`, key)
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for setting, value := range map[string]string{
		"smtp_host": "smtp.example.test", "smtp_port": "587", "smtp_encryption": "starttls",
		"smtp_user": "api-user", "smtp_pass": "secret", "smtp_from_email": "noreply@example.test",
	} {
		if _, err := db.Exec(`INSERT INTO security_settings(skey,svalue) VALUES(?,?) ON CONFLICT(skey) DO UPDATE SET svalue=excluded.svalue`, setting, value); err != nil {
			t.Fatal(err)
		}
	}
	handler := &ManagedMailHandler{Service: executor.NewManagedMailPolicyService(db)}
	router := gin.New()
	router.GET("/api/websites/:id/managed-mail", handler.PanelPolicy)
	router.PUT("/api/websites/:id/managed-mail", handler.PanelPolicy)
	router.GET("/api/sites/managed-mail/status", handler.PluginStatus)
	router.POST("/api/sites/managed-mail/confirm", handler.PluginConfirm)
	router.POST("/api/sites/managed-mail/settings", handler.PluginSettings)
	router.POST("/api/sites/managed-mail/conflict", handler.PluginConflict)
	router.POST("/api/sites/managed-mail/send", handler.PluginSend)
	return router, siteID, key
}

func TestManagedMailPluginCanUpdateSenderButCannotBypassPanelLock(t *testing.T) {
	router, siteID, key := setupManagedMailHandlerTest(t)
	update := managedMailHandlerRequest(router, http.MethodPut, "/api/websites/"+managedMailSiteID(siteID)+"/managed-mail",
		`{"authorized":true,"from_name":"Panel Name","reply_to":"panel@example.test","lock_from_name":true,"minute_limit":3,"hour_limit":15,"day_limit":50}`, "")
	if update.Code != http.StatusOK {
		t.Fatalf("panel update status=%d body=%s", update.Code, update.Body.String())
	}
	locked := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/settings", `{"from_name":"Bypass","reply_to":"site@example.test"}`, key)
	if locked.Code != http.StatusConflict || !strings.Contains(locked.Body.String(), `"message":"from_name_locked"`) {
		t.Fatalf("locked update status=%d body=%s", locked.Code, locked.Body.String())
	}
	allowed := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/settings", `{"from_name":"Panel Name","reply_to":"site@example.test"}`, key)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), `"reply_to":"site@example.test"`) {
		t.Fatalf("reply-to update status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	unlock := managedMailHandlerRequest(router, http.MethodPut, "/api/websites/"+managedMailSiteID(siteID)+"/managed-mail",
		`{"authorized":true,"from_name":"Panel Name","reply_to":"site@example.test","lock_from_name":false,"minute_limit":3,"hour_limit":15,"day_limit":50}`, "")
	if unlock.Code != http.StatusOK {
		t.Fatalf("panel unlock status=%d body=%s", unlock.Code, unlock.Body.String())
	}
	changed := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/settings", `{"from_name":"Site Name","reply_to":"owner@example.test"}`, key)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"from_name":"Site Name"`) || !strings.Contains(changed.Body.String(), `"reply_to":"owner@example.test"`) {
		t.Fatalf("sender update status=%d body=%s", changed.Code, changed.Body.String())
	}
}

func managedMailHandlerRequest(router *gin.Engine, method, target, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("X-WP-Panel-Key", key)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestManagedMailAPIDoubleOptInAndPluginViewHidesSMTPIdentity(t *testing.T) {
	router, siteID, key := setupManagedMailHandlerTest(t)
	update := managedMailHandlerRequest(router, http.MethodPut, "/api/websites/"+managedMailSiteID(siteID)+"/managed-mail",
		`{"authorized":true,"from_name":"Site A","reply_to":"support@example.test","lock_from_name":true,"minute_limit":3,"hour_limit":15,"day_limit":50}`, "")
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"authorized":true`) || !strings.Contains(update.Body.String(), `"site_confirmed":false`) {
		t.Fatalf("panel update status=%d body=%s", update.Code, update.Body.String())
	}

	status := managedMailHandlerRequest(router, http.MethodGet, "/api/sites/managed-mail/status", "", key)
	if status.Code != http.StatusOK {
		t.Fatalf("plugin status=%d body=%s", status.Code, status.Body.String())
	}
	for _, secretField := range []string{"smtp.example.test", "api-user", "secret", "noreply@example.test", "from_email"} {
		if strings.Contains(status.Body.String(), secretField) {
			t.Fatalf("plugin policy leaked SMTP identity %q: %s", secretField, status.Body.String())
		}
	}
	confirm := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/confirm", `{"enabled":true}`, key)
	if confirm.Code != http.StatusOK || !strings.Contains(confirm.Body.String(), `"active":true`) {
		t.Fatalf("plugin confirm status=%d body=%s", confirm.Code, confirm.Body.String())
	}

	revoke := managedMailHandlerRequest(router, http.MethodPut, "/api/websites/"+managedMailSiteID(siteID)+"/managed-mail",
		`{"authorized":false,"from_name":"Site A","reply_to":"support@example.test","lock_from_name":true,"minute_limit":3,"hour_limit":15,"day_limit":50}`, "")
	if revoke.Code != http.StatusOK || !strings.Contains(revoke.Body.String(), `"site_confirmed":false`) {
		t.Fatalf("panel revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
}

func TestManagedMailPluginConflictCanPrecedeAuthorizationAndNeverAutoResumes(t *testing.T) {
	router, siteID, key := setupManagedMailHandlerTest(t)
	conflict := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/conflict", `{"plugin":"post-smtp/postman-smtp.php"}`, key)
	if conflict.Code != http.StatusOK || !strings.Contains(conflict.Body.String(), "post-smtp") {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	update := managedMailHandlerRequest(router, http.MethodPut, "/api/websites/"+managedMailSiteID(siteID)+"/managed-mail",
		`{"authorized":true,"from_name":"Site A","reply_to":"","lock_from_name":false,"minute_limit":3,"hour_limit":15,"day_limit":50}`, "")
	if update.Code != http.StatusOK {
		t.Fatalf("panel update status=%d body=%s", update.Code, update.Body.String())
	}
	blocked := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/confirm", `{"enabled":true}`, key)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), `"message":"smtp_plugin_conflict"`) {
		t.Fatalf("conflicted confirm status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	cleared := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/conflict", `{"plugin":""}`, key)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"site_confirmed":false`) || strings.Contains(cleared.Body.String(), `"active":true`) {
		t.Fatalf("clear conflict status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	confirmed := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/confirm", `{"enabled":true}`, key)
	if confirmed.Code != http.StatusOK || !strings.Contains(confirmed.Body.String(), `"active":true`) {
		t.Fatalf("explicit re-confirm status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
}

func TestManagedMailPluginAPIRejectsInvalidIdentityAndStrictJSON(t *testing.T) {
	router, siteID, key := setupManagedMailHandlerTest(t)
	unauthorized := managedMailHandlerRequest(router, http.MethodGet, "/api/sites/managed-mail/status", "", strings.Repeat("x", 40))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("invalid identity status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	invalid := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/confirm", `{"enabled":true,"unexpected":1}`, key)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON field status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	update := managedMailHandlerRequest(router, http.MethodPut, "/api/websites/"+managedMailSiteID(siteID)+"/managed-mail",
		`{"authorized":true,"from_name":"Site","reply_to":"","lock_from_name":false,"minute_limit":3,"hour_limit":15,"day_limit":50}`, "")
	if update.Code != http.StatusOK {
		t.Fatalf("panel update status=%d body=%s", update.Code, update.Body.String())
	}
	invalidSettings := managedMailHandlerRequest(router, http.MethodPost, "/api/sites/managed-mail/settings", `{"from_name":"Site","reply_to":"bad-address"}`, key)
	if invalidSettings.Code != http.StatusBadRequest {
		t.Fatalf("invalid sender settings status=%d body=%s", invalidSettings.Code, invalidSettings.Body.String())
	}
}

type fakeManagedMailSender struct {
	siteID  int64
	message executor.ManagedMailMessage
	result  executor.ManagedMailSendResult
	err     error
}

func (f *fakeManagedMailSender) Send(_ context.Context, siteID int64, message executor.ManagedMailMessage) (executor.ManagedMailSendResult, error) {
	f.siteID, f.message = siteID, message
	return f.result, f.err
}

func TestManagedMailSendAPIUsesKeyIdentityAndStructuredPayload(t *testing.T) {
	_, siteID, key := setupManagedMailHandlerTest(t)
	fake := &fakeManagedMailSender{result: executor.ManagedMailSendResult{RequestID: "request-send-api-001", Status: "accepted", RecipientCount: 1, MessageSize: 123}}
	// Replace only the send route to prove handler injection without opening a network connection.
	sendRouter := gin.New()
	sendRouter.POST("/api/sites/managed-mail/send", (&ManagedMailHandler{Sender: fake}).PluginSend)
	body := `{"request_id":"request-send-api-001","from_name":"Site A","to":["user@example.test"],"subject":"Test","text_body":"Body"}`
	response := managedMailHandlerRequest(sendRouter, http.MethodPost, "/api/sites/managed-mail/send", body, key)
	if response.Code != http.StatusOK || fake.siteID != siteID || fake.message.Subject != "Test" || fake.message.To[0] != "user@example.test" {
		t.Fatalf("status=%d body=%s site=%d message=%+v", response.Code, response.Body.String(), fake.siteID, fake.message)
	}
	if !strings.Contains(response.Body.String(), `"status":"accepted"`) {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func managedMailSiteID(value int64) string {
	return strconv.FormatInt(value, 10)
}
