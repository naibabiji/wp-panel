package handlers

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
)

func TestSystemUpdateStatusResponseIncludesRemovalSafetyDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	packages := []string{"old-kernel", "unused-library"}

	response := systemUpdateStatusResponse(c, executor.SystemPackageUpdateStatus{
		ID:              "test",
		Status:          "blocked",
		Stage:           "preflight",
		MessageKey:      "settings.system_update_status_removal_blocked",
		RemovalPackages: packages,
	})

	if response["status"] != "blocked" {
		t.Fatalf("unexpected status: %#v", response["status"])
	}
	if got, ok := response["removal_packages"].([]string); !ok || !reflect.DeepEqual(got, packages) {
		t.Fatalf("unexpected removal packages: %#v", response["removal_packages"])
	}
	if response["message"] == "" {
		t.Fatal("blocked status should include a localized message")
	}
}
