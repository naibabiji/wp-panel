package executor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

type wpThemeConfirmation struct {
	token, riskToken, username, domain, collectionID, componentKey string
	siteID                                                         int
	currentVersion, targetVersion, downloadURL, template           string
	currentTheme                                                   bool
	createdAt, expiresAt                                           time.Time
}

type wpThemeConfirmationStore struct {
	mu      sync.Mutex
	records map[string]wpThemeConfirmation
	now     func() time.Time
}

func newWPThemeConfirmationStore() *wpThemeConfirmationStore {
	return &wpThemeConfirmationStore{records: map[string]wpThemeConfirmation{}, now: time.Now}
}

func (s *wpThemeConfirmationStore) create(record wpThemeConfirmation) (wpThemeConfirmation, error) {
	if s == nil || record.username == "" || record.siteID <= 0 || record.collectionID == "" ||
		!validWPThemeComponentKey(record.componentKey) ||
		(record.template != "" && !validWPThemeComponentKey(record.template)) ||
		!wpComponentVersionPattern.MatchString(record.currentVersion) ||
		!wpComponentVersionPattern.MatchString(record.targetVersion) ||
		!validWPThemeDownloadURL(record.downloadURL, record.componentKey, record.targetVersion) {
		return wpThemeConfirmation{}, errors.New("invalid theme update confirmation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	sessionCount := 0
	for token, existing := range s.records {
		if existing.siteID == record.siteID && existing.componentKey == record.componentKey && existing.username != record.username {
			return wpThemeConfirmation{}, errors.New("theme update confirmation capacity reached")
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
		return wpThemeConfirmation{}, errors.New("theme update confirmation capacity reached")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return wpThemeConfirmation{}, errors.New("theme update confirmation unavailable")
	}
	record.token = hex.EncodeToString(raw[:])
	if record.currentTheme {
		var risk [16]byte
		if _, err := rand.Read(risk[:]); err != nil {
			return wpThemeConfirmation{}, errors.New("theme risk confirmation unavailable")
		}
		record.riskToken = hex.EncodeToString(risk[:])
	}
	record.createdAt, record.expiresAt = now, now.Add(wpCoreConfirmationTTL)
	s.records[record.token] = record
	return record, nil
}

func (s *wpThemeConfirmationStore) consume(token, riskToken, username string, siteID int, componentKey, target string) (wpThemeConfirmation, error) {
	if s == nil || token == "" || username == "" || siteID <= 0 || !validWPThemeComponentKey(componentKey) || target == "" {
		return wpThemeConfirmation{}, errors.New("invalid theme update confirmation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pruneLocked(now)
	record, ok := s.records[token]
	if !ok || record.username != username || record.siteID != siteID || record.componentKey != componentKey ||
		record.targetVersion != target || record.currentTheme && riskToken != record.riskToken ||
		!record.currentTheme && riskToken != "" {
		return wpThemeConfirmation{}, errors.New("theme update confirmation rejected")
	}
	delete(s.records, token)
	return record, nil
}

func (s *wpThemeConfirmationStore) pruneLocked(now time.Time) {
	for token, record := range s.records {
		if !record.expiresAt.After(now) {
			delete(s.records, token)
		}
	}
}
