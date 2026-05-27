package executor

import (
	"testing"
	"time"
)

func TestAlertRuleSustainedFiring(t *testing.T) {
	start := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	r := &alertRule{thresholdDuration: 5 * time.Minute}

	if r.sustainedFiring(true, start) {
		t.Fatal("first high sample should not alert immediately")
	}
	if r.sustainedFiring(true, start.Add(4*time.Minute+59*time.Second)) {
		t.Fatal("high duration below threshold should not alert")
	}
	if !r.sustainedFiring(true, start.Add(5*time.Minute)) {
		t.Fatal("high duration at threshold should alert")
	}
}

func TestAlertRuleSustainedFiringResets(t *testing.T) {
	start := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	r := &alertRule{thresholdDuration: 5 * time.Minute}

	r.sustainedFiring(true, start)
	if r.sustainedFiring(false, start.Add(2*time.Minute)) {
		t.Fatal("normal sample should not alert")
	}
	if !r.pendingSince.IsZero() {
		t.Fatal("normal sample should reset pending state")
	}
	if r.sustainedFiring(true, start.Add(6*time.Minute)) {
		t.Fatal("new high period should restart the timer")
	}
}
