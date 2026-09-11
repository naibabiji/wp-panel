package executor

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMaintenanceFailedRequestReplay(t *testing.T) {
	for _, operation := range []string{"unlock", "extend"} {
		t.Run(operation, func(t *testing.T) {
			m, id, now, _, _ := maintenanceFixture(t)
			req := MaintenanceRequest{RequestID: uuid.NewString(), Password: "wrong", Minutes: 1}
			if operation == "extend" {
				s := maintenanceUnlock(t, m, id)
				_, state, raw, _ := m.load(id)
				state.Window.Expires = state.Window.VerifiedUntil
				if err := m.save(id, state, &raw, ""); err != nil {
					t.Fatal(err)
				}
				req.WindowID = s.WindowID
			}
			invoke := func(manager *MaintenanceManager, r MaintenanceRequest) error {
				if operation == "unlock" {
					return manager.Unlock(id, r)
				}
				return manager.Extend(id, r)
			}
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); _ = invoke(m, req) }()
			}
			wg.Wait()
			next := NewMaintenanceManager(m.db)
			next.now = m.now
			next.unlock = m.unlock
			next.lock = m.lock
			next.alert = m.alert
			req.Password = testMaintenancePassword
			if err := invoke(next, req); !errors.Is(err, ErrMaintenanceValidation) {
				t.Fatalf("replay with changed password: %v", err)
			}
			_, state, _, _ := m.load(id)
			if len(state.Failures) != 1 || len(state.FailedRequests) != 1 || state.FrozenUntil != 0 {
				t.Fatalf("replay counted: %+v", state.Failures)
			}
			for i := 0; i < 4; i++ {
				req.RequestID = uuid.NewString()
				req.Password = "wrong"
				want := ErrMaintenanceValidation
				if i == 3 {
					want = ErrMaintenanceFrozen
				}
				if err := invoke(next, req); !errors.Is(err, want) {
					t.Fatalf("failure %d: %v", i+2, err)
				}
			}
			_, state, _, _ = m.load(id)
			if len(state.Failures) != 5 || state.FrozenUntil != now.Unix()+600 {
				t.Fatal("distinct requests must still freeze")
			}
			deadline := state.FrozenUntil
			*now = now.Add(time.Minute)
			for _, replay := range []bool{true, false} {
				if !replay {
					req.RequestID = uuid.NewString()
				}
				req.Password = testMaintenancePassword
				if err := invoke(next, req); !errors.Is(err, ErrMaintenanceFrozen) {
					t.Fatalf("correct password during freeze: %v", err)
				}
			}
			_, state, _, _ = m.load(id)
			if state.FrozenUntil != deadline || len(state.Failures) != 5 || len(state.FailedRequests) != 5 {
				t.Fatal("frozen requests changed deadline or counters")
			}
			*now = now.Add(601 * time.Second)
			if operation == "unlock" {
				req.RequestID = uuid.NewString()
				req.Password = testMaintenancePassword
				if err := invoke(next, req); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMaintenanceRejectsLegacyBeforePasswordOrFilesystem(t *testing.T) {
	for _, mode := range []string{"legacy", "", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			m, id, _, unlocks, _ := maintenanceFixture(t)
			if _, err := m.db.Exec(`UPDATE websites SET file_lock_mode=? WHERE id=?`, mode, id); err != nil {
				t.Fatal(err)
			}
			if err := m.Configure(id, true, 5, ""); !errors.Is(err, ErrMaintenanceLockMode) {
				t.Fatal(err)
			}
			if err := m.Unlock(id, MaintenanceRequest{RequestID: uuid.NewString(), Password: testMaintenancePassword}); !errors.Is(err, ErrMaintenanceLockMode) {
				t.Fatal(err)
			}
			_, state, _, _ := m.load(id)
			if *unlocks != 0 || state.Window != nil || len(state.Failures) != 0 {
				t.Fatal("legacy changed state")
			}
		})
	}
}
