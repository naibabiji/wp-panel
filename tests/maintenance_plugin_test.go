package tests

import (
	"os/exec"
	"testing"
)

func TestMaintenancePluginPermissionsAndTransport(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP CLI unavailable")
	}
	if out, err := exec.Command(php, "php/maintenance.php").CombinedOutput(); err != nil {
		t.Fatalf("PHP maintenance checks: %v\n%s", err, out)
	}
}

func TestMaintenancePluginRequestRetryIdentity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	for _, tc := range []struct{ dir, script string }{
		{".", "js/maintenance_requests.cjs"},
		{"..", "tests/js/maintenance_requests.cjs"},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			cmd := exec.Command(node, tc.script)
			cmd.Dir = tc.dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("maintenance request checks: %v\n%s", err, out)
			}
		})
	}
}
