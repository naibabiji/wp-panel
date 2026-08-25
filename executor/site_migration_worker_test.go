package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cancellingMigrationProcessor struct {
	started chan struct{}
	stopped chan struct{}
	cleaned chan struct{}
}

type failingCleanupMigrationProcessor struct {
	cleaned chan string
}

func (p *failingCleanupMigrationProcessor) Process(context.Context, *siteMigrationSite) error {
	return errors.New("stage failed")
}

func (p *failingCleanupMigrationProcessor) HandleFailure(_ context.Context, job *siteMigrationSite, processErr error) error {
	if processErr == nil {
		return errors.New("missing process error")
	}
	p.cleaned <- job.ID
	return nil
}

func (p *cancellingMigrationProcessor) Process(ctx context.Context, _ *siteMigrationSite) error {
	close(p.started)
	<-ctx.Done()
	close(p.stopped)
	return ctx.Err()
}

func (p *cancellingMigrationProcessor) HandleFailure(context.Context, *siteMigrationSite, error) error {
	if p.cleaned != nil {
		close(p.cleaned)
	}
	return nil
}

func TestSiteMigrationWorkerStopDoesNotRunFailureCleanup(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	p := &cancellingMigrationProcessor{started: make(chan struct{}), stopped: make(chan struct{}), cleaned: make(chan struct{})}
	w, err := newSiteMigrationWorker(store, p, "worker_0000000001", time.Millisecond, time.Second, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	<-p.started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.cleaned:
		t.Fatal("worker stop invoked destructive failure cleanup")
	default:
	}
}

func TestSiteMigrationWorkerWaitsForProcessorAfterLeaseLoss(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	now := time.Now().UTC()
	job, err := store.claimNext(context.Background(), "worker_0000000001", now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p := &cancellingMigrationProcessor{started: make(chan struct{}), stopped: make(chan struct{})}
	w, err := newSiteMigrationWorker(store, p, "worker_0000000001", time.Millisecond, 30*time.Millisecond, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.process(context.Background(), job) }()
	<-p.started
	if _, err := store.db.Exec(`UPDATE site_migration_sites SET lease_owner='worker_0000000002' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("process err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after lease loss")
	}
	select {
	case <-p.stopped:
	default:
		t.Fatal("worker returned before processor stopped")
	}
}

func TestSiteMigrationWorkerRunsFailureCleanupAfterRelease(t *testing.T) {
	store, _ := newSiteMigrationStoreTest(t)
	taskID := "migration_0000001"
	p := &failingCleanupMigrationProcessor{cleaned: make(chan string, 1)}
	w, err := newSiteMigrationWorker(store, p, "worker_0000000001", time.Millisecond, time.Second, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-p.cleaned:
		if got != taskID {
			t.Fatalf("task=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("failure cleanup was not called")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var status, owner string
	var queryErr error
	for i := 0; i < 20; i++ {
		queryErr = store.db.QueryRow(`SELECT status,lease_owner FROM site_migration_sites WHERE id=?`, taskID).Scan(&status, &owner)
		if queryErr == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	if status != "failed_retryable" || owner != "" {
		t.Fatalf("status=%q owner=%q", status, owner)
	}
}
