package executor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

type wpPluginConfirmation struct {
	token, username, domain, collectionID, componentKey string
	siteID                                              int
	currentVersion, targetVersion, downloadURL          string
	createdAt, expiresAt                                time.Time
}

type wpPluginConfirmationStore struct {
	mu      sync.Mutex
	records map[string]wpPluginConfirmation
	now     func() time.Time
}

func newWPPluginConfirmationStore() *wpPluginConfirmationStore {
	return &wpPluginConfirmationStore{records: map[string]wpPluginConfirmation{}, now: time.Now}
}

func (s *wpPluginConfirmationStore) create(record wpPluginConfirmation) (wpPluginConfirmation, error) {
	if s == nil || record.username == "" || record.siteID <= 0 || record.collectionID == "" ||
		!validWPPluginComponentKey(record.componentKey) || !wpComponentVersionPattern.MatchString(record.currentVersion) ||
		!wpComponentVersionPattern.MatchString(record.targetVersion) ||
		!validWPPluginDownloadURL(record.downloadURL, componentSlug(record.componentKey), record.targetVersion) {
		return wpPluginConfirmation{}, errors.New("invalid plugin update confirmation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	sessionCount := 0
	for token, existing := range s.records {
		if existing.siteID == record.siteID && existing.componentKey == record.componentKey && existing.username != record.username {
			return wpPluginConfirmation{}, errors.New("plugin update confirmation capacity reached")
		}
		if existing.username == record.username {
			sessionCount++
			if existing.siteID == record.siteID && existing.componentKey == record.componentKey {
				delete(s.records, token)
				sessionCount--
			}
		}
	}
	if len(s.records) >= wpCoreConfirmationGlobalMax || sessionCount >= wpCoreConfirmationSessionMax {
		return wpPluginConfirmation{}, errors.New("plugin update confirmation capacity reached")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return wpPluginConfirmation{}, errors.New("plugin update confirmation unavailable")
	}
	record.token = hex.EncodeToString(raw[:])
	record.createdAt, record.expiresAt = now, now.Add(wpCoreConfirmationTTL)
	s.records[record.token] = record
	return record, nil
}

func (s *wpPluginConfirmationStore) consume(token, username string, siteID int, componentKey, target string) (wpPluginConfirmation, error) {
	if s == nil || token == "" || username == "" || siteID <= 0 || !validWPPluginComponentKey(componentKey) || target == "" {
		return wpPluginConfirmation{}, errors.New("invalid plugin update confirmation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	record, ok := s.records[token]
	if !ok || record.username != username || record.siteID != siteID || record.componentKey != componentKey || record.targetVersion != target {
		return wpPluginConfirmation{}, errors.New("plugin update confirmation rejected")
	}
	delete(s.records, token)
	return record, nil
}

func (s *wpPluginConfirmationStore) pruneLocked(now time.Time) {
	for token, record := range s.records {
		if !record.expiresAt.After(now) {
			delete(s.records, token)
		}
	}
}

func componentSlug(componentKey string) string {
	if !validWPPluginComponentKey(componentKey) {
		return ""
	}
	return strings.SplitN(componentKey, "/", 2)[0]
}
