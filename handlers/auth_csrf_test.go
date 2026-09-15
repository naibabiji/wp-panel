package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/middleware"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

func setupLoginCSRFTest(t *testing.T) (*gin.Engine, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE admin_users (username TEXT PRIMARY KEY, password_hash TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO admin_users(username,password_hash) VALUES ('admin',?)`, string(hash)); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	handler := &AuthHandler{DB: db}
	router.POST("/login", middleware.CSRF(), handler.Login)
	return router, db
}

func performLoginCSRFTest(t *testing.T, router http.Handler, cookieToken, headerToken string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	if cookieToken != "" {
		req.AddCookie(&http.Cookie{Name: "csrf_token", Value: cookieToken})
	}
	if headerToken != "" {
		req.Header.Set("X-CSRF-Token", headerToken)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestLoginRequiresMatchingCSRFToken(t *testing.T) {
	router, _ := setupLoginCSRFTest(t)

	if recorder := performLoginCSRFTest(t, router, "", ""); recorder.Code != http.StatusForbidden {
		t.Fatalf("missing token status=%d, want %d", recorder.Code, http.StatusForbidden)
	}
	if recorder := performLoginCSRFTest(t, router, "cookie-token", "other-token"); recorder.Code != http.StatusForbidden {
		t.Fatalf("mismatched token status=%d, want %d", recorder.Code, http.StatusForbidden)
	}
	recorder := performLoginCSRFTest(t, router, "matching-token", "matching-token")
	if recorder.Code != http.StatusOK {
		t.Fatalf("matching token status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(recorder.Result().Cookies()) == 0 {
		t.Fatal("successful login did not set a session cookie")
	}
}
