package executor

import (
	"reflect"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

func TestWPInventoryScheduleEligibility(t *testing.T) {
	t.Parallel()

	for _, status := range []models.WebsiteStatus{models.StatusActive, models.StatusPaused, models.StatusError} {
		if !wpInventoryScheduleEligible(wpInventoryScheduleSite{ID: 1, SiteType: "wordpress", Status: status}) {
			t.Fatalf("expected status %q to be eligible", status)
		}
	}
	for _, site := range []wpInventoryScheduleSite{
		{ID: 0, SiteType: "wordpress", Status: models.StatusActive},
		{ID: 1, SiteType: "php", Status: models.StatusActive},
		{ID: 1, SiteType: "wordpress", Status: models.StatusCreating},
		{ID: 1, SiteType: "wordpress", Status: models.StatusDeleting},
		{ID: 1, SiteType: "wordpress", Status: models.WebsiteStatus("future")},
	} {
		if wpInventoryScheduleEligible(site) {
			t.Fatalf("unexpected eligible site: %+v", site)
		}
	}
}

func TestWPInventoryScheduleOffsetIsStableAndBounded(t *testing.T) {
	t.Parallel()

	interval := 6 * time.Hour
	first, ok := wpInventoryScheduleOffset(42, interval)
	if !ok || first < 0 || first >= interval {
		t.Fatalf("invalid offset: %v, ok=%v", first, ok)
	}
	for i := 0; i < 100; i++ {
		got, ok := wpInventoryScheduleOffset(42, interval)
		if !ok || got != first {
			t.Fatalf("offset drifted: got %v want %v", got, first)
		}
	}
	if other, _ := wpInventoryScheduleOffset(43, interval); other == first {
		t.Fatal("adjacent site IDs unexpectedly received the same offset")
	}
	for _, input := range []struct {
		id       int
		interval time.Duration
	}{{0, interval}, {-1, interval}, {1, 0}, {1, -time.Second}} {
		if _, ok := wpInventoryScheduleOffset(input.id, input.interval); ok {
			t.Fatalf("invalid input accepted: %+v", input)
		}
	}
}

func TestWPInventoryScheduleDueUsesStableWindows(t *testing.T) {
	t.Parallel()

	interval := 6 * time.Hour
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	site := wpInventoryScheduleSite{ID: 7, SiteType: "wordpress", Status: models.StatusActive}
	if !wpInventoryScheduleDue(site, now, interval) {
		t.Fatal("site without successful inventory should be due")
	}
	offset, _ := wpInventoryScheduleOffset(site.ID, interval)
	windowStart := now.Add(-offset).Truncate(interval).Add(offset)
	justBefore := windowStart.Add(-time.Nanosecond)
	justAfter := windowStart.Add(time.Nanosecond)
	site.LastSuccess = &justBefore
	if !wpInventoryScheduleDue(site, justAfter, interval) {
		t.Fatal("site should be due after entering the next stable window")
	}
	site.LastSuccess = &justAfter
	if wpInventoryScheduleDue(site, justAfter.Add(time.Second), interval) {
		t.Fatal("site should not be due twice in the same stable window")
	}
	if wpInventoryScheduleDue(site, time.Time{}, interval) || wpInventoryScheduleDue(site, now, 0) {
		t.Fatal("invalid time or interval should not be due")
	}
}

func TestWPInventoryScheduleBatchesBoundariesAndCopies(t *testing.T) {
	t.Parallel()

	for _, count := range []int{0, 1, 100, 101, 300} {
		ids := make([]int, count)
		for i := range ids {
			ids[i] = i + 1
		}
		got := wpInventoryScheduleBatches(ids, 20, 100)
		wantTotal := count
		if wantTotal > 100 {
			wantTotal = 100
		}
		total := 0
		for _, batch := range got {
			if len(batch) == 0 || len(batch) > 20 {
				t.Fatalf("count %d produced invalid batch size %d", count, len(batch))
			}
			total += len(batch)
		}
		if total != wantTotal {
			t.Fatalf("count %d: got total %d want %d", count, total, wantTotal)
		}
	}

	ids := []int{1, 2, 3}
	got := wpInventoryScheduleBatches(ids, 2, 3)
	if !reflect.DeepEqual(got, [][]int{{1, 2}, {3}}) {
		t.Fatalf("unexpected batches: %#v", got)
	}
	ids[0] = 99
	if got[0][0] != 1 {
		t.Fatal("returned batches alias the caller slice")
	}
	if got := wpInventoryScheduleBatches(ids, 0, 3); got != nil {
		t.Fatalf("invalid batch size returned %#v", got)
	}
}
