package tests

import (
	"os/exec"
	"testing"
)

func TestAnomalyPluginSampling(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP CLI unavailable")
	}
	if out, err := exec.Command(php, "php/anomaly_monitor.php").CombinedOutput(); err != nil {
		t.Fatalf("PHP anomaly sampling checks: %v\n%s", err, out)
	}
}
