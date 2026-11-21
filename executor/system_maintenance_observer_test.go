package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMaintenanceObserverDiscardsPartialAndBreaksContinuity(t *testing.T) {
	for _, interrupted := range []string{"busy", "scan_error", "timeout"} {
		t.Run(interrupted, func(t *testing.T) {
			var observer maintenanceRuntimeObserver
			now := time.Now()
			sample := maintenanceRuntimeSnapshot{At: now, Evidence: []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}}
			scan := func(context.Context) (maintenanceRuntimeSnapshot, error) { return sample, nil }
			if _, stable, err := observer.observe(context.Background(), func() (bool, error) { return false, nil }, scan); err != nil || len(stable) != 0 {
				t.Fatalf("first sample: %v %v", stable, err)
			}
			sample.At = now.Add(systemMaintenanceObservationInterval)
			if _, stable, err := observer.observe(context.Background(), func() (bool, error) { return false, nil }, scan); err != nil || len(stable) != 1 {
				t.Fatalf("stable sample: %v %v", stable, err)
			}
			sample.At = sample.At.Add(systemMaintenanceObservationInterval)
			calls := 0
			broken := func(context.Context) (maintenanceRuntimeSnapshot, error) {
				calls++
				if interrupted == "timeout" {
					return sample, context.DeadlineExceeded
				}
				return sample, errors.New("partial scan")
			}
			got, stable, err := observer.observe(context.Background(), func() (bool, error) { return interrupted == "busy", nil }, broken)
			if !got.At.IsZero() || len(got.Evidence) != 0 || len(stable) != 0 || !observer.previous.At.IsZero() {
				t.Fatalf("partial result retained: %+v %+v", got, observer.previous)
			}
			if interrupted == "busy" && (calls != 0 || err != nil) {
				t.Fatal("busy interval scanned processes")
			}
			if interrupted != "busy" && err == nil {
				t.Fatal("scan error swallowed")
			}
			sample.At = sample.At.Add(systemMaintenanceObservationInterval)
			if _, stable, err := observer.observe(context.Background(), func() (bool, error) { return false, nil }, scan); err != nil || len(stable) != 0 {
				t.Fatalf("interrupted samples counted as continuous: %v %v", stable, err)
			}
		})
	}
}

func TestMaintenanceObserverRechecksBusyAfterScanning(t *testing.T) {
	for _, variant := range []string{"busy", "query_error"} {
		t.Run(variant, func(t *testing.T) {
			now := time.Now()
			observer := maintenanceRuntimeObserver{previous: maintenanceRuntimeSnapshot{At: now, Evidence: []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}}}
			checks := 0
			gate := func() (bool, error) {
				checks++
				if checks == 1 {
					return false, nil
				}
				if variant == "query_error" {
					return false, errors.New("lock query failed")
				}
				return true, nil
			}
			scan := func(context.Context) (maintenanceRuntimeSnapshot, error) {
				return maintenanceRuntimeSnapshot{At: now.Add(systemMaintenanceObservationInterval), Evidence: []maintenanceRuntimeEvidence{maintenanceTestEvidence(1)}}, nil
			}
			got, stable, err := observer.observe(context.Background(), gate, scan)
			if checks != 2 || !got.At.IsZero() || len(stable) != 0 || !observer.previous.At.IsZero() || (err != nil) != (variant == "query_error") {
				t.Fatalf("post-scan gate ignored: checks=%d snapshot=%+v stable=%v err=%v", checks, got, stable, err)
			}
		})
	}
}
