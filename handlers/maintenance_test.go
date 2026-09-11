package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

func TestMaintenanceHandlersIdentitySchemaAndSecrets(t *testing.T) {
	setupCacheHelperTestDB(t)
	key := strings.Repeat("a", 64)
	_, err := database.GetDB().Exec(`UPDATE websites SET plugin_api_key=?,file_lock_enabled=1,file_lock_mode='standard',file_lock_apply_status='ready'`, key)
	if err != nil {
		t.Fatal(err)
	}
	m := executor.NewMaintenanceManager(database.GetDB())
	if err := m.Configure(1, true, 5, "long-maintenance-secret"); err != nil {
		t.Fatal(err)
	}
	h := &MaintenanceHandler{Manager: m}
	r := gin.New()
	r.GET("/maintenance", h.Plugin)
	r.POST("/maintenance/:action", h.Plugin)
	r.GET("/panel/:id", h.Panel)
	call := func(method, path, body, remote, credential string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = remote
		req.Header.Set("X-WP-Panel-Key", credential)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	for _, remote := range []string{"127.0.0.1:2345", "[::1]:2345"} {
		w := call("GET", "/maintenance", "", remote, key)
		if w.Code != 200 || strings.Contains(w.Body.String(), "hash") || strings.Contains(w.Body.String(), "long-maintenance-secret") {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
	}
	if w := call("GET", "/maintenance", "", "198.51.100.1:2345", key); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("GET", "/maintenance", "", "127.0.0.1:2345", strings.Repeat("b", 64)); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"site_id":2}`, `{"password":"x"} {}`, `{"password":"` + strings.Repeat("x", 5000) + `"}`} {
		w := call("POST", "/maintenance/unlock", body, "127.0.0.1:2345", key)
		if w.Code == 200 {
			t.Fatal("invalid body accepted")
		}
	}
	for i := 0; i < 6; i++ {
		body, _ := json.Marshal(map[string]any{"request_id": uuid.NewString(), "password": "wrong", "actor": "1"})
		w := call("POST", "/maintenance/unlock", string(body), "127.0.0.1:2345", key)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "verification_failed") {
			t.Fatalf("failure %d %s", w.Code, w.Body.String())
		}
	}
	var n int
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM operation_logs WHERE operation='wp_maintenance_unlock'`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("failure log %d %v", n, err)
	}
	w := call("GET", "/panel/1", "", "127.0.0.1:2345", "")
	if strings.Contains(w.Body.String(), "$2") || strings.Contains(w.Body.String(), "hash") {
		t.Fatal("hash leaked")
	}
}

func TestMaintenanceHandlerCrossSiteWindowAndFreeze(t *testing.T) {
	setupCacheHelperTestDB(t)
	key := strings.Repeat("c", 64)
	_, err := database.GetDB().Exec(`UPDATE websites SET plugin_api_key=?`, key)
	if err != nil {
		t.Fatal(err)
	}
	h := &MaintenanceHandler{Manager: executor.NewMaintenanceManager(database.GetDB())}
	keyB, windowB := strings.Repeat("d", 64), uuid.NewString()
	_, err = database.GetDB().Exec(`INSERT INTO websites(name,domain,site_type,status,plugin_api_key,system_user,web_root,log_dir,db_name,db_user,php_pool_path,nginx_conf_path) VALUES ('B','b.example','wordpress','active',?,'wp_b','/www/wwwroot/b.example','','','','','')`, keyB)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Manager.Configure(2, true, 5, "another-long-maintenance-password"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	window, _ := json.Marshal(map[string]any{"id": windowB, "state": "unlocked", "started": now, "expires": now + 300, "verified_until": now + 1800, "revision": 0})
	_, err = database.GetDB().Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.window',json(?)) WHERE id=2`, string(window))
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetDB().Exec(`UPDATE websites SET maintenance_security=json_set(maintenance_security,'$.frozen_until',?) WHERE id=1`, now+600)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.POST("/maintenance/:action", h.Plugin)
	r.GET("/maintenance", h.Plugin)
	requestID := uuid.NewString()
	for _, credential := range []string{key, keyB} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/maintenance/extend", strings.NewReader(`{"window_id":"`+windowB+`","request_id":"`+requestID+`","minutes":5}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-WP-Panel-Key", credential)
		r.ServeHTTP(w, req)
		want := 409
		if credential == keyB {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("cross-site status %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	s, err := h.Manager.Status(2)
	if err != nil || s.ExpiresAt != now+600 || s.Revision != 1 {
		t.Fatalf("B state %+v %v", s, err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/maintenance?site_id=2", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-WP-Panel-Key", key)
	r.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), windowB) {
		t.Fatal("A query selected B")
	}
}
