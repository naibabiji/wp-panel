package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSoftwareGuardActionRejectsUnknownService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/software/guard/action", (&SoftwareHandler{}).GuardAction)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/software/guard/action", strings.NewReader(`{"service":"not-managed","action":"restart"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
