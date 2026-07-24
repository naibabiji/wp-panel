package handlers

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
)

func setupPasswordResetTestSite(t *testing.T, webRoot, systemUser, initialMode string) *sql.DB {
	t.Helper()
	oldDB := database.DB
	if database.DB != nil {
		_ = database.Close()
	}
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("database.Open(): %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = oldDB
	})
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations(): %v", err)
	}
	if err := database.RunUpgrades(); err != nil {
		t.Fatalf("RunUpgrades(): %v", err)
	}
	if _, err := database.GetDB().Exec(`INSERT INTO websites
		(id, name, domain, status, system_user, web_root, log_dir, db_name, db_user, php_pool_path, nginx_conf_path, site_type, password_reset_mode, file_lock_enabled)
		VALUES (1, 'pr.example.com', 'pr.example.com', 'active', ?, ?, '/tmp/log', 'db', 'dbuser', '/tmp/php.conf', '/tmp/nginx.conf', 'wordpress', ?, 0)`,
		systemUser, webRoot, initialMode); err != nil {
		t.Fatalf("insert site: %v", err)
	}
	return database.GetDB()
}

func newPasswordResetTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &WebsiteHandler{DB: database.GetDB()}
	router.PUT("/api/websites/:id/password-reset", handler.SetPasswordResetMode)
	return router
}

func doSetPasswordResetMode(router http.Handler, id, mode string) *httptest.ResponseRecorder {
	body := bytes.NewBufferString(`{"mode":"` + mode + `"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/websites/"+id+"/password-reset", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func getSitePasswordResetMode(t *testing.T, db *sql.DB) string {
	t.Helper()
	var m string
	if err := db.QueryRow("SELECT password_reset_mode FROM websites WHERE id = 1").Scan(&m); err != nil {
		t.Fatalf("query password_reset_mode: %v", err)
	}
	return m
}

// TestSetPasswordResetModeTransitionConsistency 验证每次模式切换后，DB 记录与磁盘
// mu-plugin 文件内容始终一致（allow 移除文件 / all 隐藏链接 / admin 仅限管理员）。
func TestSetPasswordResetModeTransitionConsistency(t *testing.T) {
	webRoot := t.TempDir()
	db := setupPasswordResetTestSite(t, webRoot, "", "allow")
	router := newPasswordResetTestRouter()

	pluginPath := filepath.Join(webRoot, "wp-content", "mu-plugins", "wp-panel-password-reset.php")

	// allow -> all
	rec := doSetPasswordResetMode(router, "1", "all")
	if rec.Code != http.StatusOK {
		t.Fatalf("allow->all status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getSitePasswordResetMode(t, db); got != "all" {
		t.Fatalf("db mode = %q, want all", got)
	}
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read all mu-plugin: %v", err)
	}
	c := string(data)
	if !strings.Contains(c, "__return_false") || !strings.Contains(c, "login_head") {
		t.Fatalf("all mu-plugin missing expected content: %s", c)
	}

	// all -> admin
	rec = doSetPasswordResetMode(router, "1", "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("all->admin status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getSitePasswordResetMode(t, db); got != "admin" {
		t.Fatalf("db mode = %q, want admin", got)
	}
	data, err = os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read admin mu-plugin: %v", err)
	}
	c = string(data)
	if !strings.Contains(c, "has_cap('administrator')") || strings.Contains(c, "login_head") {
		t.Fatalf("admin mu-plugin unexpected content: %s", c)
	}

	// admin -> allow（文件移除）
	rec = doSetPasswordResetMode(router, "1", "allow")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin->allow status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getSitePasswordResetMode(t, db); got != "allow" {
		t.Fatalf("db mode = %q, want allow", got)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatalf("mu-plugin should be removed in allow mode")
	}
}

// TestSetPasswordResetModeApplyFailureRollsBackToPrev 覆盖评审意见第 1 点：
// apply 写盘成功后若 chown 失败（这里用不存在的系统用户触发），handler 必须按 DB
// 当前已提交模式回滚磁盘文件，使磁盘与 DB 保持一致性，而不是留下新模式的文件。
func TestSetPasswordResetModeApplyFailureRollsBackToPrev(t *testing.T) {
	webRoot := t.TempDir()
	db := setupPasswordResetTestSite(t, webRoot, "no-such-system-user-xyz-12345", "allow")
	router := newPasswordResetTestRouter()

	pluginPath := filepath.Join(webRoot, "wp-content", "mu-plugins", "wp-panel-password-reset.php")

	rec := doSetPasswordResetMode(router, "1", "all")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("apply-failure status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
	// DB 必须仍为 allow（apply 失败时从未落库）。
	if got := getSitePasswordResetMode(t, db); got != "allow" {
		t.Fatalf("db mode = %q, want allow (no DB write on apply failure)", got)
	}
	// 磁盘必须回滚到 allow（文件被移除），与 DB 一致。
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatalf("mu-plugin must be rolled back (removed) on apply failure, but file exists")
	}
}
