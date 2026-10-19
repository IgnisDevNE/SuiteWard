//go:build integration

package river_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

// gatedChecks is a checks job that blocks until the test ends, so a wake-up of the checks kind meets a running run.
type gatedChecks struct {
	running atomic.Int32
	peak    atomic.Int32
	step    chan struct{} // lets one blocked run finish
	release chan struct{} // lets every run finish; closed when the test ends
}

func newGatedChecks(t *testing.T) *gatedChecks {
	g := &gatedChecks{step: make(chan struct{}, 8), release: make(chan struct{})}
	t.Cleanup(func() { close(g.release) })
	return g
}

func (g *gatedChecks) run(context.Context) error {
	g.peak.Store(max(g.peak.Load(), g.running.Add(1)))
	select {
	case <-g.step:
	case <-g.release:
	}
	g.running.Add(-1)
	return nil
}

// wakeConfig composes a runtime whose github_sync_repo job returns wake and err; checks, when set, is the checks job.
// Every periodic run starts once and then waits an hour, and a job gets one attempt.
func wakeConfig(logs *logCapture, checks *gatedChecks, wake river.Wake, err error) river.Config {
	config := testConfig(logs)
	config.JobMaxAttempts = 1
	config.SyncTick = func(context.Context) (river.Wake, error) { return nil, nil }
	config.SyncRepo = func(context.Context, string) (river.Wake, error) { return wake, err }
	config.SyncInterval, config.ChecksInterval, config.CommandsInterval = time.Hour, time.Hour, time.Hour
	if checks != nil {
		config.Checks = checks.run
	}
	return config
}

func (w *world) count(where string) string {
	w.t.Helper()
	return w.scalar("SELECT count(*)::text FROM river_job WHERE " + where)
}

// wakeFromSyncRepo runs one github_sync_repo job and waits until it is in state.
func wakeFromSyncRepo(t *testing.T, w *world, runtime *river.Runtime, repo, state string) {
	t.Helper()
	if _, err := runtime.InsertSyncRepo(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the sync job of "+repo+" to be "+state, func() bool {
		return w.count("kind='github_sync_repo' AND state='"+state+"' AND args->>'repo'='"+repo+"'") == "1"
	})
}

// logLine returns the first captured line holding every part, or "".
func logLine(logs *logCapture, parts ...string) string {
lines:
	for line := range strings.SplitSeq(logs.String(), "\n") {
		for _, part := range parts {
			if !strings.Contains(line, part) {
				continue lines
			}
		}
		return line
	}
	return ""
}

func TestEachGitHubKindRunsOnItsOwnQueue(t *testing.T) {
	w := newWorld(t, 5)
	var ran atomic.Int32
	run := func(context.Context) (river.Wake, error) { ran.Add(1); return nil, nil }
	config := testConfig(&logCapture{})
	config.SyncTick, config.SyncRepo = run, func(context.Context, string) (river.Wake, error) { return nil, nil }
	config.Commands, config.Merges, config.Bootstrap = run, run, run
	config.Checks = func(context.Context) error { ran.Add(1); return nil }
	config.SyncInterval, config.CommandsInterval, config.ChecksInterval, config.MergesInterval, config.BootstrapInterval = time.Hour, time.Hour, time.Hour, time.Hour, time.Hour
	startRuntime(t, w, config)
	waitUntil(t, "the five periodic runs", func() bool { return ran.Load() >= 5 })

	for _, kind := range []string{river.SyncTickKind, river.CommandsKind, river.ChecksKind, river.MergesKind, river.BootstrapKind} {
		if got := w.scalar("SELECT queue FROM river_job WHERE kind=$1 ORDER BY id LIMIT 1", kind); got != kind {
			t.Errorf("%s runs on queue %q; want a queue of its own", kind, got)
		}
	}
}

func TestARunThatReturnsAWakeInsertsExactlyOneJobOfThatKindOnItsQueue(t *testing.T) {
	w := newWorld(t, 5)
	checks := newGatedChecks(t)
	runtime := startRuntime(t, w, wakeConfig(&logCapture{}, checks, river.Wake{river.ChecksKind}, nil))
	waitUntil(t, "the first checks run to block", func() bool { return checks.running.Load() == 1 })

	wakeFromSyncRepo(t, w, runtime, "acme/widgets", "completed")

	if got := w.count("kind='github_checks' AND state='available' AND queue='github_checks' AND max_attempts=1"); got != "1" {
		t.Fatalf("%s woken checks jobs available on their queue; want exactly one", got)
	}
	if got := w.count("kind='github_checks'"); got != "2" {
		t.Fatalf("%s checks jobs; want the running one and the wake-up", got)
	}
}

func TestWakeUpsOfOneKindQueueOneFollowUpWhileOneRuns(t *testing.T) {
	w := newWorld(t, 5)
	checks := newGatedChecks(t)
	// Each run wakes checks twice, so a wake-up also meets the follow-up its own run just queued.
	runtime := startRuntime(t, w, wakeConfig(&logCapture{}, checks, river.Wake{river.ChecksKind, river.ChecksKind}, nil))
	waitUntil(t, "the first checks run to block", func() bool { return checks.running.Load() == 1 })

	for _, repo := range []string{"acme/widgets", "acme/gadgets", "acme/gizmos"} {
		wakeFromSyncRepo(t, w, runtime, repo, "completed")
	}

	// The run blocks; the first wake-up queued one follow-up and the five others saw it available.
	if got := w.count("kind='github_checks'"); got != "2" {
		t.Fatalf("%s checks jobs after six wake-ups; want the running one and one follow-up", got)
	}
	if got := checks.peak.Load(); got != 1 {
		t.Fatalf("%d checks runs at once; want runs of one kind never to overlap", got)
	}
}

func TestAWakeUpIsInsertedWhenTheRunFailsAndTheErrorIsReturnedUnchanged(t *testing.T) {
	w := newWorld(t, 5)
	checks := newGatedChecks(t)
	runtime := startRuntime(t, w, wakeConfig(&logCapture{}, checks, river.Wake{river.ChecksKind}, errors.New("the run failed late")))
	waitUntil(t, "the first checks run to block", func() bool { return checks.running.Load() == 1 })

	wakeFromSyncRepo(t, w, runtime, "acme/widgets", "discarded")

	if got := w.scalar("SELECT errors[1]->>'error' FROM river_job WHERE kind='github_sync_repo'"); got != "the run failed late" {
		t.Fatalf("the job's error = %q; want the run's error unchanged", got)
	}
	if got := w.count("kind='github_checks' AND state='available'"); got != "1" {
		t.Fatalf("%s wake-ups queued after a failed run; want one", got)
	}
}

func TestAnUnknownWakeKindInsertsNothingAndIsLoggedAtError(t *testing.T) {
	w := newWorld(t, 5)
	logs := &logCapture{}
	// The tick is a real kind but not one a run may wake.
	runtime := startRuntime(t, w, wakeConfig(logs, nil, river.Wake{"nonexistent_kind", river.SyncTickKind}, nil))
	waitUntil(t, "the first tick to complete", func() bool { return w.count("kind='github_sync_tick' AND state='completed'") == "1" })

	wakeFromSyncRepo(t, w, runtime, "acme/widgets", "completed")

	if got := w.count("kind='nonexistent_kind'"); got != "0" {
		t.Fatalf("%s jobs of an unknown kind; want none", got)
	}
	if got := w.count("kind='github_sync_tick'"); got != "1" {
		t.Fatalf("%s tick jobs; want only the periodic one", got)
	}
	for _, kind := range []string{"nonexistent_kind", river.SyncTickKind} {
		if logLine(logs, `"level":"ERROR"`, `"kind":"`+kind+`"`) == "" {
			t.Errorf("no Error line names the kind %q:\n%s", kind, logs.String())
		}
	}
}

// River refuses to insert a job whose kind no worker is registered for; with no Checks job that is the failing insert.
func TestAFailingWakeUpIsLoggedAtWarnWithTheKindOnlyAndDoesNotFailTheRun(t *testing.T) {
	w := newWorld(t, 5)
	logs := &logCapture{}
	runtime := startRuntime(t, w, wakeConfig(logs, nil, river.Wake{river.ChecksKind}, nil))

	wakeFromSyncRepo(t, w, runtime, "acme/widgets", "completed")

	line := logLine(logs, `"level":"WARN"`, `"kind":"`+river.ChecksKind+`"`)
	if line == "" {
		t.Fatalf("no Warn line names the kind %q:\n%s", river.ChecksKind, logs.String())
	}
	if strings.Contains(line, `"error"`) {
		t.Errorf("the Warn line carries more than the kind: %s", line)
	}
	if got := w.count("kind='github_checks'"); got != "0" {
		t.Fatalf("%s checks jobs; want none", got)
	}
}

func TestAWakeUpAndAPeriodicRunQueuedTogetherDoNotOverlap(t *testing.T) {
	w := newWorld(t, 5)
	checks := newGatedChecks(t)
	config := wakeConfig(&logCapture{}, checks, river.Wake{river.ChecksKind}, nil)
	config.ChecksInterval = 100 * time.Millisecond
	runtime := startRuntime(t, w, config)
	waitUntil(t, "the first checks run to block", func() bool { return checks.running.Load() == 1 })
	wakeFromSyncRepo(t, w, runtime, "acme/widgets", "completed")

	// The wake-up has no unique key, so once the first run ends the periodic insert is not blocked by it: the wake-up runs and the next periodic run waits.
	checks.step <- struct{}{}
	waitUntil(t, "a periodic run to queue behind the running wake-up", func() bool {
		return w.count("kind='github_checks' AND state='available' AND unique_key IS NOT NULL") == "1"
	})
	waitUntil(t, "the wake-up to start running", func() bool { return checks.running.Load() == 1 }) // River fetches about once a second; without this the sleep below can end before it
	time.Sleep(500 * time.Millisecond)                                                              // a negative assertion: the queued run must not start beside the running one

	if got, peak := checks.running.Load(), checks.peak.Load(); got != 1 || peak != 1 {
		t.Fatalf("%d checks runs at once, peak %d; want runs of one kind never to overlap", got, peak)
	}
}
