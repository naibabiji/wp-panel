package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBasicAuthRejectsRequestWhenBanStatusCannotBeRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorded := false
	reached := false
	router := gin.New()
	router.Use(BasicAuth(&BasicAuthChecker{
		IsBanned: func(string) (bool, error) {
			return false, errors.New("database unavailable")
		},
		RecordAttempt: func(string, string) {
			recorded = true
		},
	}))
	router.GET("/panel", func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/panel", nil)
	request.SetBasicAuth("admin", "password")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if reached || recorded {
		t.Fatalf("reached=%t recorded=%t, authentication must not continue after ban lookup failure", reached, recorded)
	}
}
