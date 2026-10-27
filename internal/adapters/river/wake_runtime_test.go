//go:build integration

package river_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

// The runtime wakes a kind for a caller that is not a job, such as a request that committed work: the same insert as a run's wake-up (one job on the
// queue of the kind, none while one is waiting), with the failure returned to the caller instead of logged.
func TestRuntimeWakeInsertsOneJobOfTheKindOnItsQueue(t *testing.T) {
	w := newWorld(t, 5)
	checks := newGatedChecks(t)
	runtime := startRuntime(t, w, wakeConfig(&logCapture{}, checks, nil, nil))
	waitUntil(t, "the first checks run to block", func() bool { return checks.running.Load() == 1 })

	for range 3 { // the first wake-up queues the follow-up and the others see it waiting
		if err := runtime.Wake(t.Context(), river.Wake{river.ChecksKind}); err != nil {
			t.Fatalf("Wake = %v; want nil", err)
		}
	}

	if got := w.count("kind='github_checks' AND state='available' AND queue='github_checks' AND max_attempts=1"); got != "1" {
		t.Fatalf("%s woken checks jobs available on their queue; want exactly one", got)
	}
	if got := w.count("kind='github_checks'"); got != "2" {
		t.Fatalf("%s checks jobs; want the running one and the wake-up", got)
	}
}

func TestRuntimeWakeOfNothingDoesNothing(t *testing.T) {
	w := newWorld(t, 5)
	runtime := startRuntime(t, w, wakeConfig(&logCapture{}, nil, nil, nil))

	if err := runtime.Wake(t.Context(), nil); err != nil {
		t.Fatalf("Wake(nil) = %v; want nil", err)
	}
	if got := w.count("kind='github_checks'"); got != "0" {
		t.Fatalf("%s checks jobs; want none", got)
	}
}

// A kind that a run may not wake is an error that names it, and nothing is inserted for it; the kinds that can be woken are still woken.
func TestRuntimeWakeReturnsAKindThatCannotBeWoken(t *testing.T) {
	w := newWorld(t, 5)
	checks := newGatedChecks(t)
	runtime := startRuntime(t, w, wakeConfig(&logCapture{}, checks, nil, nil))
	waitUntil(t, "the first checks run to block", func() bool { return checks.running.Load() == 1 })

	err := runtime.Wake(t.Context(), river.Wake{"nonexistent_kind", river.SyncTickKind, river.ChecksKind})

	if err == nil || !strings.Contains(err.Error(), "nonexistent_kind") || !strings.Contains(err.Error(), river.SyncTickKind) {
		t.Fatalf("Wake = %v; want an error naming both kinds that cannot be woken", err)
	}
	if got := w.count("kind='nonexistent_kind'"); got != "0" {
		t.Fatalf("%s jobs of an unknown kind; want none", got)
	}
	if got := w.count("kind='github_sync_tick'"); got != "1" {
		t.Fatalf("%s tick jobs; want only the periodic one", got)
	}
	if got := w.count("kind='github_checks' AND state='available'"); got != "1" {
		t.Fatalf("%s checks wake-ups; want the kind that can be woken to be woken anyway", got)
	}
}

// River refuses to insert a job whose kind no worker is registered for; with no Checks job that is the failing insert.
func TestRuntimeWakeReturnsAnInsertThatFails(t *testing.T) {
	w := newWorld(t, 5)
	runtime := startRuntime(t, w, wakeConfig(&logCapture{}, nil, nil, nil))

	err := runtime.Wake(t.Context(), river.Wake{river.ChecksKind})

	if err == nil || !strings.Contains(err.Error(), river.ChecksKind) {
		t.Fatalf("Wake = %v; want the failed insert, naming the kind", err)
	}
	if got := w.count("kind='github_checks'"); got != "0" {
		t.Fatalf("%s checks jobs; want none", got)
	}
}
