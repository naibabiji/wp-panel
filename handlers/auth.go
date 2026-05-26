package handlers

import (
	"database/sql"
	"net/http"

	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/middleware"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func setSessionCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "wp_session",
		Value:    token,
		MaxAge:   maxAge,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

type AuthHandler struct {
	DB     *sql.DB
	Prefix string
}

func (h *AuthHandler) LoginPage(c *gin.Context) {
	c.HTML(http.StatusOK, "login.html", gin.H{
		"Title":        "登录",
		"RandomSuffix": h.Prefix,
		"Active":       "login",
		"AssetPrefix":  "/" + h.Prefix + "/assets",
		"CSRFToken":    getCSRFTokenFromCookie(c),
	})
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("请提供用户名和密码"))
		return
	}

	var hash string
	err := h.DB.QueryRow(
		"SELECT password_hash FROM admin_users WHERE username = ?", req.Username,
	).Scan(&hash)

	if err != nil {
		recordWebLoginAttempt(c.ClientIP(), h.DB)
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("用户名或密码错误"))
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		recordWebLoginAttempt(c.ClientIP(), h.DB)
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("用户名或密码错误"))
		return
	}

	session := middleware.GlobalSessionStore.Create(req.Username)
	setSessionCookie(c, session.Token, 1800)

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"username": req.Username,
	}))
}

func (h *AuthHandler) Logout(c *gin.Context) {
	token, err := c.Cookie("wp_session")
	if err == nil && token != "" {
		middleware.GlobalSessionStore.Delete(token)
	}
	setSessionCookie(c, "", -1)
	c.JSON(http.StatusOK, models.SuccessResponse(nil))
}

func (h *AuthHandler) Check(c *gin.Context) {
	username, exists := c.Get("session_username")
	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse("未登录"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"username": username,
	}))
}

func (h *AuthHandler) CSRFToken(c *gin.Context) {
	token, err := c.Cookie("csrf_token")
	if err != nil || token == "" {
		c.JSON(http.StatusOK, models.ErrorResponse("无CSRF token"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"token": token,
	}))
}

func recordWebLoginAttempt(ip string, db *sql.DB) {
	_, _ = db.Exec(
		"INSERT INTO login_attempts (ip_address, attempt_type) VALUES (?, 'web_login')",
		ip,
	)

	var count int
	_ = db.QueryRow(
		`SELECT COUNT(*) FROM login_attempts
		 WHERE ip_address = ? AND attempt_type = 'web_login'
		 AND created_at > datetime('now', '-5 minutes')`,
		ip,
	).Scan(&count)

	if count >= 5 {
		_, _ = db.Exec(
			`INSERT INTO firewall_bans (ip_address, ban_level, reason, source_jail, expires_at, ban_count)
			 VALUES (?, 3, 'panel_web_login: 连续5次登录失败', 'panel', datetime('now', '+24 hours'), 1)`,
			ip,
		)
		executor.AddPersistBan(ip)
	}
}

func getCSRFTokenFromCookie(c *gin.Context) string {
	token, _ := c.Cookie("csrf_token")
	return token
}
