package executor

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

const wpInventoryScheduleNamespace = "wp-inventory-schedule/v1:"

const (
	wpInventoryScheduleRefreshInterval = 6 * time.Hour
	wpInventorySchedulePollInterval    = 15 * time.Minute
	wpInventoryScheduleBatchDelay      = time.Second
	wpInventoryScheduleBatchSize       = 20
	wpInventoryScheduleMaxPerCycle     = 100
	wpInventoryScheduleCandidateLimit  = 300
)

type wpInventoryScheduleSite struct {
	ID                   int
	SiteType             string
	Status               models.WebsiteStatus
	LastSuccess          *time.Time
	LastScheduledRequest *time.Time
}

func wpInventoryScheduleEligible(site wpInventoryScheduleSite) bool {
	if site.ID <= 0 || site.SiteType != "wordpress" {
		return false
	}

	switch site.Status {
	case models.StatusActive, models.StatusPaused, models.StatusError:
		return true
	default:
		return false
	}
}

func wpInventoryScheduleOffset(siteID int, refreshInterval time.Duration) (time.Duration, bool) {
	if siteID <= 0 || refreshInterval <= 0 {
		return 0, false
	}

	digest := sha256.Sum256([]byte(wpInventoryScheduleNamespace + strconv.Itoa(siteID)))
	offset := binary.BigEndian.Uint64(digest[:8]) % uint64(refreshInterval)
	return time.Duration(offset), true
}

func wpInventoryScheduleDue(site wpInventoryScheduleSite, now time.Time, refreshInterval time.Duration) bool {
	if !wpInventoryScheduleEligible(site) || refreshInterval <= 0 || now.IsZero() {
		return false
	}
	anchor := site.LastSuccess
	if site.LastScheduledRequest != nil && !site.LastScheduledRequest.IsZero() &&
		(anchor == nil || anchor.IsZero() || site.LastScheduledRequest.After(*anchor)) {
		anchor = site.LastScheduledRequest
	}
	if anchor == nil || anchor.IsZero() {
		return true
	}

	offset, ok := wpInventoryScheduleOffset(site.ID, refreshInterval)
	if !ok {
		return false
	}
	lastWindow := anchor.Add(-offset).Truncate(refreshInterval)
	currentWindow := now.Add(-offset).Truncate(refreshInterval)
	return currentWindow.After(lastWindow)
}

type wpInventorySchedulerStore interface {
	listScheduleCandidates(context.Context, int) ([]wpInventoryScheduleSite, error)
	enqueueEligibleScheduled(context.Context, int, time.Time) (string, bool, error)
}

type WPInventoryScheduler struct {
	store           wpInventorySchedulerStore
	now             func() time.Time
	wait            func(context.Context, time.Duration) bool
	refreshInterval time.Duration
	pollInterval    time.Duration
	batchDelay      time.Duration
	batchSize       int
	maxPerCycle     int
	candidateLimit  int

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

type wpInventorySchedulerOptions struct {
	store           wpInventorySchedulerStore
	now             func() time.Time
	wait            func(context.Context, time.Duration) bool
	refreshInterval time.Duration
	pollInterval    time.Duration
	batchDelay      time.Duration
	batchSize       int
	maxPerCycle     int
	candidateLimit  int
}

func NewWPInventoryScheduler() (*WPInventoryScheduler, error) {
	store, err := newWPInventoryStore()
	if err != nil {
		return nil, err
	}
	return newWPInventoryScheduler(wpInventorySchedulerOptions{
		store: store, now: time.Now, wait: wpInventoryScheduleWait,
		refreshInterval: wpInventoryScheduleRefreshInterval,
		pollInterval:    wpInventorySchedulePollInterval,
		batchDelay:      wpInventoryScheduleBatchDelay,
		batchSize:       wpInventoryScheduleBatchSize,
		maxPerCycle:     wpInventoryScheduleMaxPerCycle,
		candidateLimit:  wpInventoryScheduleCandidateLimit,
	})
}

func newWPInventoryScheduler(opts wpInventorySchedulerOptions) (*WPInventoryScheduler, error) {
	if opts.store == nil || opts.now == nil || opts.wait == nil || opts.refreshInterval <= 0 ||
		opts.pollInterval <= 0 || opts.batchDelay <= 0 || opts.batchSize <= 0 ||
		opts.maxPerCycle <= 0 || opts.candidateLimit <= 0 {
		return nil, errors.New("invalid wordpress inventory scheduler options")
	}
	return &WPInventoryScheduler{
		store: opts.store, now: opts.now, wait: opts.wait,
		refreshInterval: opts.refreshInterval, pollInterval: opts.pollInterval,
		batchDelay: opts.batchDelay, batchSize: opts.batchSize,
		maxPerCycle: opts.maxPerCycle, candidateLimit: opts.candidateLimit,
	}, nil
}

func (s *WPInventoryScheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("wordpress inventory scheduler already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	go s.run(ctx)
	return nil
}

func (s *WPInventoryScheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	s.cancel()
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *WPInventoryScheduler) run(ctx context.Context) {
	defer close(s.done)
	for {
		_ = s.runCycle(ctx)
		if ctx.Err() != nil || !s.wait(ctx, s.pollInterval) {
			return
		}
	}
}

func (s *WPInventoryScheduler) runCycle(ctx context.Context) error {
	now := s.now().UTC()
	candidates, err := s.store.listScheduleCandidates(ctx, s.candidateLimit)
	if err != nil {
		return err
	}
	due := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		if wpInventoryScheduleDue(candidate, now, s.refreshInterval) {
			due = append(due, candidate.ID)
		}
	}
	batches := wpInventoryScheduleBatches(due, s.batchSize, s.maxPerCycle)
	for batchIndex, batch := range batches {
		if batchIndex > 0 && !s.wait(ctx, s.batchDelay) {
			return ctx.Err()
		}
		for _, siteID := range batch {
			_, _, err := s.store.enqueueEligibleScheduled(ctx, siteID, now)
			if errors.Is(err, errWPInventoryScheduledSiteIneligible) {
				continue
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func wpInventoryScheduleWait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func wpInventoryScheduleBatches(siteIDs []int, batchSize, maxTotal int) [][]int {
	if batchSize <= 0 || maxTotal <= 0 || len(siteIDs) == 0 {
		return nil
	}

	limit := len(siteIDs)
	if limit > maxTotal {
		limit = maxTotal
	}
	batches := make([][]int, 0, (limit+batchSize-1)/batchSize)
	for start := 0; start < limit; start += batchSize {
		end := start + batchSize
		if end > limit {
			end = limit
		}
		batch := append([]int(nil), siteIDs[start:end]...)
		batches = append(batches, batch)
	}
	return batches
}
