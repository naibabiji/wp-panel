package handlers

import (
	"errors"
	"testing"

	"github.com/naibabiji/wp-panel/executor"
)

func TestWPAdministratorErrorKey(t *testing.T) {
	tests := map[string]string{
		"non_transactional_engine":     "website.wp_admin_non_transactional_engine",
		"transaction_failed":           "website.wp_admin_transaction_failed",
		"verification_failed":          "website.wp_admin_verification_failed",
		"password_verification_failed": "website.wp_admin_verification_failed",
		"commit_failed":                "website.wp_admin_write_failed",
		"login_exists":                 "website.wp_admin_login_exists",
	}
	for code, want := range tests {
		if got := wpAdministratorErrorKey(&executor.WPAdminManagerError{Code: code}); got != want {
			t.Fatalf("code %q: got %q want %q", code, got, want)
		}
	}
	if got := wpAdministratorErrorKey(errors.New("other")); got != "website.wp_admin_update_failed" {
		t.Fatalf("fallback = %q", got)
	}
}
