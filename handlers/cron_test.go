package handlers

import "testing"

func TestValidateCronInputRejectsSiteBoundCommandTask(t *testing.T) {
	siteID := 1
	msg := validateCronInput("site command", "0 1 * * *", "echo ok", "command", "", "", &siteID)
	if msg == "" {
		t.Fatal("validateCronInput accepted a command task with site_id, want rejection")
	}
}
