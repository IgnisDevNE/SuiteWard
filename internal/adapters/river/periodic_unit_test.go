package river

import (
	"maps"
	"slices"
	"testing"
	"time"
)

func TestPeriodicSpecsDefineThePeriodsQueuesAndWakeUps(t *testing.T) {
	periods := map[string]time.Duration{}
	for _, s := range periodicSpecs {
		periods[s.args.Kind()] = s.defaultEvery
	}
	wantPeriods := map[string]time.Duration{
		"github_sync_tick": 20 * time.Second, "github_protection_check": 15 * time.Minute, "github_bootstrap": time.Minute, "github_commands": 15 * time.Second,
		"github_checks": 30 * time.Second, "github_merges": 30 * time.Second, "canonical_integrity": time.Hour,
	}
	if !maps.Equal(periods, wantPeriods) {
		t.Errorf("default periods %v; want %v", periods, wantPeriods)
	}

	queued := slices.Sorted(maps.Keys(queues(4)))
	if want := []string{"default", "github_bootstrap", "github_checks", "github_commands", "github_merges", "github_sync_tick", outboxQueue}; !slices.Equal(queued, want) {
		t.Errorf("queues %v; want %v", queued, want)
	}
	if configs := queues(4); configs["default"].MaxWorkers != 4 || configs[outboxQueue].MaxWorkers != 1 || configs["github_checks"].MaxWorkers != 1 {
		t.Errorf("queue workers %v; want 4 on the default queue and one on each other", configs)
	}
	if woken, want := slices.Sorted(maps.Keys(wakeable)), []string{"github_bootstrap", "github_checks", "github_commands", "github_merges"}; !slices.Equal(woken, want) {
		t.Errorf("wakeable kinds %v; want %v", woken, want)
	}
}
