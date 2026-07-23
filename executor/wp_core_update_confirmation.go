package executor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const (
	wpCoreConfirmationTTL        = 10 * time.Minute
	wpCoreConfirmationGlobalMax  = 128
	wpCoreConfirmationSessionMax = 8
)

type wpCoreConfirmation struct {
	token, username, domain, collectionID              string
	siteID                                             int
	recentBackupID                                     int64
	currentVersion, targetVersion, locale, downloadURL string
	createdAt, expiresAt                               time.Time
}

type wpCoreConfirmationStore struct {
	mu      sync.Mutex
	records map[string]wpCoreConfirmation
	now     func() time.Time
}

func newWPCoreConfirmationStore() *wpCoreConfirmationStore {
	return &wpCoreConfirmationStore{records: map[string]wpCoreConfirmation{}, now: time.Now}
}

func (s *wpCoreConfirmationStore) create(record wpCoreConfirmation) (wpCoreConfirmation, error) {
	if s == nil || record.username == "" || record.siteID <= 0 || record.collectionID == "" || record.currentVersion == "" || record.targetVersion == "" || !validWPCoreUpdatePackageURL(record.downloadURL) {
		return wpCoreConfirmation{}, errors.New("invalid core update confirmation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	sessionCount := 0
	for token, existing := range s.records {
		if existing.siteID == record.siteID && existing.username != record.username {
			return wpCoreConfirmation{}, errors.New("core update confirmation capacity reached")
		}
		if existing.username == record.username {
			sessionCount++
			if existing.siteID == record.siteID {
				delete(s.records, token)
				sessionCount--
			}
		}
	}
	if len(s.records) >= wpCoreConfirmationGlobalMax || sessionCount >= wpCoreConfirmationSessionMax {
		return wpCoreConfirmation{}, errors.New("core update confirmation capacity reached")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return wpCoreConfirmation{}, errors.New("core update confirmation unavailable")
	}
	record.token = hex.EncodeToString(raw[:])
	record.createdAt, record.expiresAt = now, now.Add(wpCoreConfirmationTTL)
	s.records[record.token] = record
	return record, nil
}

func (s *wpCoreConfirmationStore) consume(token, username string, siteID int, target string) (wpCoreConfirmation, error) {
	if s == nil || token == "" || username == "" || siteID <= 0 || target == "" {
		return wpCoreConfirmation{}, errors.New("invalid core update confirmation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	record, ok := s.records[token]
	if !ok || record.username != username || record.siteID != siteID || record.targetVersion != target {
		return wpCoreConfirmation{}, errors.New("core update confirmation rejected")
	}
	delete(s.records, token)
	return record, nil
}

func (s *wpCoreConfirmationStore) pruneLocked(now time.Time) {
	for token, record := range s.records {
		if !record.expiresAt.After(now) {
			delete(s.records, token)
		}
	}
}
