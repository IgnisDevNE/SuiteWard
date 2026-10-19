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
		SyncTickKind: 20 * time.Second, ProtectionCheckKind: 15 * time.Minute, BootstrapKind: time.Minute, CommandsKind: 15 * time.Second,
		ChecksKind: 30 * time.Second, MergesKind: 30 * time.Second, IntegrityKind: time.Hour,
	}
	if !maps.Equal(periods, wantPeriods) {
		t.Errorf("default periods %v; want %v", periods, wantPeriods)
	}

	queued := slices.Sorted(maps.Keys(queues(4)))
	if want := []string{"default", "github_bootstrap", "github_checks", "github_commands", "github_merges", "github_sync_tick", outboxQueue}; !slices.Equal(queued, want) {
		t.Errorf("queues %v; want %v", queued, want)
	}
	if configs := queues(4); configs["default"].MaxWorkers != 4 || configs[outboxQueue].MaxWorkers != 1 || configs[ChecksKind].MaxWorkers != 1 {
		t.Errorf("queue workers %v; want 4 on the default queue and one on each other", configs)
	}
	if woken, want := slices.Sorted(maps.Keys(wakeable)), []string{BootstrapKind, ChecksKind, CommandsKind, MergesKind}; !slices.Equal(woken, want) {
		t.Errorf("wakeable kinds %v; want %v", woken, want)
	}
}
