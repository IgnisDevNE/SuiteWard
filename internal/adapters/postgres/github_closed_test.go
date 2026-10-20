package postgres

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
)

func TestClosedBatchSelectsMergedItemsAtOrAboveTheWatermark(t *testing.T) {
	second := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	item := func(number int, updatedAt time.Time, merged bool) githubsync.ClosedPull {
		pull := githubsync.ClosedPull{ObservedPull: githubsync.ObservedPull{Number: number, UpdatedAt: updatedAt}}
		if merged {
			pull.MergedAt = updatedAt
		}
		return pull
	}
	listing := []githubsync.ClosedPull{
		item(1, second.Add(-time.Second), true), // below the watermark
		item(2, second, true),                   // the same second as the holder of the watermark
		item(3, second, false),                  // not merged
		item(4, second.Add(time.Second), true),  // above
	}
	watermark := pgtype.Timestamptz{Time: second, Valid: true}
	fresh, latest := closedBatch(listing, watermark)
	got := []int{}
	for _, row := range fresh {
		got = append(got, row.Number)
	}
	if want := []int{2, 4}; !reflect.DeepEqual(got, want) || !latest.Equal(second.Add(time.Second)) {
		t.Errorf("closedBatch = %v, latest %v; want %v, %v", got, latest, want, second.Add(time.Second))
	}
	if fresh, latest := closedBatch(listing, pgtype.Timestamptz{}); len(fresh) != 0 || !latest.Equal(second.Add(time.Second)) {
		t.Errorf("without a watermark closedBatch = %v, %v; want nothing fresh and the greatest updated_at", fresh, latest)
	}
}
