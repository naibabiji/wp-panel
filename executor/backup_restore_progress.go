package executor

import (
	"io"
	"sync"
	"time"
)

type SQLRestoreProgress struct {
	BytesRead     int64 `json:"bytes_read"`
	TotalBytes    int64 `json:"total_bytes,omitempty"`
	LastAdvanceAt int64 `json:"last_advance_at,omitempty"`
	Waiting       bool  `json:"waiting,omitempty"`
}

type sqlRestoreProgress struct {
	mu    sync.Mutex
	value *SQLRestoreProgress
}

func (p *sqlRestoreProgress) set(value *SQLRestoreProgress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value = value
}

func (p *sqlRestoreProgress) snapshot() *SQLRestoreProgress {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.value == nil {
		return nil
	}
	value := *p.value
	return &value
}

type sqlRestoreReader struct {
	io.Reader
	progress  *sqlRestoreProgress
	value     SQLRestoreProgress
	published time.Time
}

func (r *sqlRestoreReader) Read(buf []byte) (int, error) {
	n, err := r.Reader.Read(buf)
	if n > 0 {
		now := time.Now()
		r.value.BytesRead += int64(n)
		r.value.LastAdvanceAt = now.Unix()
		if r.value.TotalBytes < r.value.BytesRead {
			r.value.TotalBytes = 0
		}
		if now.Sub(r.published) >= time.Second {
			r.publish(now)
		}
	}
	return n, err
}

func (r *sqlRestoreReader) publish(now time.Time) {
	value := r.value
	r.progress.set(&value)
	r.published = now
}
