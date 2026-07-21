package executor

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

const wpInventoryScheduleNamespace = "wp-inventory-schedule/v1:"

type wpInventoryScheduleSite struct {
	ID          int
	SiteType    string
	Status      models.WebsiteStatus
	LastSuccess *time.Time
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
	if site.LastSuccess == nil || site.LastSuccess.IsZero() {
		return true
	}

	offset, ok := wpInventoryScheduleOffset(site.ID, refreshInterval)
	if !ok {
		return false
	}
	lastWindow := site.LastSuccess.Add(-offset).Truncate(refreshInterval)
	currentWindow := now.Add(-offset).Truncate(refreshInterval)
	return currentWindow.After(lastWindow)
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
