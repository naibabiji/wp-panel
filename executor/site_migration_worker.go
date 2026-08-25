package executor

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const (
	siteMigrationWorkerPollInterval = time.Second
	siteMigrationWorkerLease        = time.Minute
)

type siteMigrationProcessor interface {
	Process(context.Context, *siteMigrationSite) error
}

type siteMigrationFailureProcessor interface {
	HandleFailure(context.Context, *siteMigrationSite, error) error
}

type siteMigrationG1Processor struct{}

func (siteMigrationG1Processor) Process(context.Context, *siteMigrationSite) error {
	return errors.New("site migration execution is not available in G1")
}

type SiteMigrationWorker struct {
	store        *siteMigrationStore
	processor    siteMigrationProcessor
	owner        string
	pollInterval time.Duration
	lease        time.Duration
	now          func() time.Time

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewSiteMigrationWorker(cfg *config.Config, version string) (*SiteMigrationWorker, error) {
	store, err := newSiteMigrationStore(database.GetDB())
	if err != nil {
		return nil, err
	}
	id, err := newWPInventoryJobID()
	if err != nil {
		return nil, err
	}
	pairing, err := NewSiteMigrationPairingService(database.GetDB(), version, cfg.Panel.TLSCertPath)
	if err != nil {
		return nil, err
	}
	processor, err := newSiteMigrationStageProcessor(database.GetDB(), cfg, pairing)
	if err != nil {
		return nil, err
	}
	return newSiteMigrationWorker(store, processor, "migration-worker-"+id, siteMigrationWorkerPollInterval, siteMigrationWorkerLease, time.Now)
}

func newSiteMigrationWorker(store *siteMigrationStore, processor siteMigrationProcessor, owner string, pollInterval, lease time.Duration, now func() time.Time) (*SiteMigrationWorker, error) {
	if store == nil || processor == nil || !validSiteMigrationID(owner) || pollInterval <= 0 || lease/3 <= 0 || now == nil {
		return nil, errors.New("invalid site migration worker options")
	}
	return &SiteMigrationWorker{store: store, processor: processor, owner: owner, pollInterval: pollInterval, lease: lease, now: now}, nil
}

func (w *SiteMigrationWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("site migration worker already started")
	}
	if _, err := w.store.recoverExpired(context.Background(), w.now().UTC()); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	go w.run(ctx)
	return nil
}

func (w *SiteMigrationWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	w.cancel()
	done := w.done
	w.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *SiteMigrationWorker) run(ctx context.Context) {
	defer close(w.done)
	for {
		job, err := w.store.claimNext(ctx, w.owner, w.now().UTC(), w.lease)
		if err == nil && job != nil {
			processErr := w.process(ctx, job)
			if processErr != nil && ctx.Err() == nil {
				log.Printf("网站搬家任务处理失败 task=%s: %v", job.ID, processErr)
			}
			if releaseErr := w.store.releaseClaim(context.Background(), job.ID, w.owner, processErr, w.now().UTC()); releaseErr != nil {
				log.Printf("网站搬家任务租约释放失败 task=%s: %v", job.ID, releaseErr)
			}
			if processErr != nil && ctx.Err() == nil {
				if cleaner, ok := w.processor.(siteMigrationFailureProcessor); ok {
					if cleanupErr := cleaner.HandleFailure(context.Background(), job, processErr); cleanupErr != nil {
						log.Printf("网站搬家失败自动清理失败 task=%s: %v", job.ID, cleanupErr)
					}
				}
			}
			continue
		}
		timer := time.NewTimer(w.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (w *SiteMigrationWorker) process(ctx context.Context, job *siteMigrationSite) error {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- w.processor.Process(jobCtx, job) }()
	ticker := time.NewTicker(w.lease / 3)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return err
		case <-ticker.C:
			if err := w.store.heartbeat(context.Background(), job.ID, w.owner, w.now().UTC(), w.lease); err != nil {
				cancel()
				<-result
				return err
			}
		case <-ctx.Done():
			cancel()
			<-result
			return ctx.Err()
		}
	}
}

func SiteMigrationLocked(ctx context.Context, siteID int, domain string) (bool, error) {
	store, err := newSiteMigrationStore(database.GetDB())
	if err != nil {
		return false, err
	}
	return store.isLocked(ctx, siteID, domain)
}

// SiteMigrationDeleteBlocked keeps ordinary deletion closed while migration
// work is active, but allows a source website after the user has explicitly
// marked that migration completed. Other site operations remain locked.
func SiteMigrationDeleteBlocked(ctx context.Context, siteID int, domain string) (bool, error) {
	var count int
	err := database.GetDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_locks ml
		WHERE ml.status='active' AND (ml.site_id=? OR ml.domain=?) AND NOT (
			ml.direction='source' AND EXISTS (
				SELECT 1 FROM site_migration_sites ms WHERE ms.id=ml.migration_site_id
				AND ms.source_site_id=? AND ms.status='completed' AND ms.stage='completed'
			)
		)`, siteID, strings.ToLower(strings.TrimSpace(domain)), siteID).Scan(&count)
	return count > 0, err
}
