package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSiteMigrationFailureLimitBlocksAfterTwentyUnauthorizedResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SiteMigrationFailureLimit())
	router.POST("/machine", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })

	for i := 0; i < siteMigrationFailureLimit; i++ {
		if status := performMigrationLimitRequest(router); status != http.StatusUnauthorized {
			t.Fatalf("failure %d status=%d, want %d", i+1, status, http.StatusUnauthorized)
		}
	}
	if status := performMigrationLimitRequest(router); status != http.StatusTooManyRequests {
		t.Fatalf("blocked status=%d, want %d", status, http.StatusTooManyRequests)
	}
}

func TestSiteMigrationFailureLimitSuccessClearsFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	succeed := false
	router := gin.New()
	router.Use(SiteMigrationFailureLimit())
	router.POST("/machine", func(c *gin.Context) {
		if succeed {
			c.Status(http.StatusOK)
			return
		}
		c.Status(http.StatusUnauthorized)
	})

	for i := 0; i < siteMigrationFailureLimit-1; i++ {
		performMigrationLimitRequest(router)
	}
	succeed = true
	if status := performMigrationLimitRequest(router); status != http.StatusOK {
		t.Fatalf("success status=%d, want %d", status, http.StatusOK)
	}
	succeed = false
	if status := performMigrationLimitRequest(router); status != http.StatusUnauthorized {
		t.Fatalf("failure after success status=%d, want %d", status, http.StatusUnauthorized)
	}
}

func performMigrationLimitRequest(handler http.Handler) int {
	req := httptest.NewRequest(http.MethodPost, "/machine", nil)
	req.RemoteAddr = "203.0.113.25:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}
