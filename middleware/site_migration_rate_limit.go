package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	siteMigrationFailureWindow = time.Minute
	siteMigrationFailureLimit  = 20
)

type siteMigrationFailureEntry struct {
	startedAt    time.Time
	failures     int
	blockedUntil time.Time
}

type siteMigrationFailureTracker struct {
	mu          sync.Mutex
	now         func() time.Time
	lastCleanup time.Time
	entries     map[string]*siteMigrationFailureEntry
}

func newSiteMigrationFailureTracker() *siteMigrationFailureTracker {
	return &siteMigrationFailureTracker{now: time.Now, entries: make(map[string]*siteMigrationFailureEntry)}
}

func (t *siteMigrationFailureTracker) isBlocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[ip]
	return entry != nil && t.now().Before(entry.blockedUntil)
}

func (t *siteMigrationFailureTracker) recordFailure(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	if t.lastCleanup.IsZero() || now.Sub(t.lastCleanup) >= siteMigrationFailureWindow {
		for key, entry := range t.entries {
			if now.Sub(entry.startedAt) >= siteMigrationFailureWindow && !now.Before(entry.blockedUntil) {
				delete(t.entries, key)
			}
		}
		t.lastCleanup = now
	}

	entry := t.entries[ip]
	if entry == nil || now.Sub(entry.startedAt) >= siteMigrationFailureWindow {
		entry = &siteMigrationFailureEntry{startedAt: now}
		t.entries[ip] = entry
	}
	entry.failures++
	if entry.failures >= siteMigrationFailureLimit {
		entry.blockedUntil = now.Add(siteMigrationFailureWindow)
	}
}

func (t *siteMigrationFailureTracker) clear(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, ip)
}

// SiteMigrationFailureLimit throttles repeated authentication failures without
// imposing a request-rate ceiling on authenticated migration transfers.
func SiteMigrationFailureLimit() gin.HandlerFunc {
	tracker := newSiteMigrationFailureTracker()
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if tracker.isBlocked(ip) {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}

		c.Next()
		switch c.Writer.Status() {
		case http.StatusUnauthorized:
			tracker.recordFailure(ip)
		case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
			tracker.clear(ip)
		}
	}
}
