//go:build integration

package river_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

func syncConfig(tick func(context.Context) error, repo func(context.Context, string) error) river.Config {
	config := testConfig(&logCapture{})
	config.SyncTick, config.SyncRepo = noWake(tick), noWakeRepo(repo)
	return config
}

func noRepo(context.Context, string) error { return nil }

// waitUntil polls cond until it holds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSyncTickRunsAtStart(t *testing.T) {
	w := newWorld(t, 5)
	var ticks atomic.Int32
	// The default period is a minute: a tick seen within seconds ran because the periodic job has RunOnStart.
	startRuntime(t, w, syncConfig(func(context.Context) error { ticks.Add(1); return nil }, noRepo))

	waitUntil(t, "the tick to run at start", func() bool { return ticks.Load() >= 1 })
	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_sync_tick'"); got != "1" {
		t.Fatalf("%s tick jobs after the start; want exactly one", got)
	}
}

func TestSyncTicksDoNotOverlap(t *testing.T) {
	w := newWorld(t, 5)
	release := make(chan struct{})
	var running, peak atomic.Int32
	config := syncConfig(func(context.Context) error {
		peak.Store(max(peak.Load(), running.Add(1)))
		<-release
		running.Add(-1)
		return nil
	}, noRepo)
	config.SyncInterval = 100 * time.Millisecond
	startRuntime(t, w, config)
	t.Cleanup(func() { close(release) })
	waitUntil(t, "the first tick to start", func() bool { return running.Load() == 1 })

	// A negative assertion about a timer: let the periodic job fire a dozen times while the first tick still runs.
	time.Sleep(1200 * time.Millisecond)

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_sync_tick'"); got != "1" || peak.Load() != 1 {
		t.Fatalf("%s tick jobs and %d running at once while the first tick still ran; want 1 and 1", got, peak.Load())
	}
}

// A sync job and a tick may list one repository at different moments; the later commit would then write the older
// listing over the newer one (a pull request listed by one reads as not open in the other), so they never run together.
func TestASyncRepoJobWaitsForARunningTick(t *testing.T) {
	w := newWorld(t, 5)
	release := make(chan struct{})
	var ticking atomic.Bool
	var syncedDuringTick atomic.Int32
	var synced atomic.Int32
	config := syncConfig(func(context.Context) error {
		ticking.Store(true)
		<-release
		ticking.Store(false)
		return nil
	}, func(context.Context, string) error {
		if ticking.Load() {
			syncedDuringTick.Add(1)
		}
		synced.Add(1)
		return nil
	})
	runtime := startRuntime(t, w, config)
	t.Cleanup(func() { close(release) })
	waitUntil(t, "the tick to start", ticking.Load)

	if inserted, err := runtime.InsertSyncRepo(t.Context(), "acme/widgets"); err != nil || !inserted {
		t.Fatalf("InsertSyncRepo = %v, %v", inserted, err)
	}
	// A negative assertion about a worker: give an idle one time to take the job.
	time.Sleep(1200 * time.Millisecond)

	if got := syncedDuringTick.Load(); got != 0 {
		t.Fatalf("the sync job ran %d times while the tick ran; want it to wait for the tick", got)
	}
	release <- struct{}{}
	waitUntil(t, "the sync job to run after the tick", func() bool { return synced.Load() == 1 })
}

func TestSyncRepoJobsCoalesceWhilePending(t *testing.T) {
	w := newWorld(t, 5)
	var mu sync.Mutex
	var ran []string
	config := syncConfig(func(context.Context) error { return nil }, func(_ context.Context, repo string) error {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, repo)
		return nil
	})
	runtime, err := river.NewRuntime(w.pool, w.store, config) // not started: its jobs stay pending
	if err != nil {
		t.Fatal(err)
	}

	for _, request := range []struct {
		repo     string
		inserted bool
	}{{"acme/widgets", true}, {"acme/widgets", false}, {"acme/gadgets", true}, {"acme/widgets", false}} {
		inserted, err := runtime.InsertSyncRepo(t.Context(), request.repo)
		if err != nil || inserted != request.inserted {
			t.Fatalf("InsertSyncRepo(%s) = %v, %v; want inserted %v", request.repo, inserted, err, request.inserted)
		}
	}
	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_sync_repo'"); got != "2" {
		t.Fatalf("%s sync jobs pending; want one per repository", got)
	}

	startRuntime(t, w, config)
	waitUntil(t, "both sync jobs to complete", func() bool {
		return w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_sync_repo' AND state='completed'") == "2"
	})
	mu.Lock()
	got := append([]string(nil), ran...)
	mu.Unlock()
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("the handler ran for %v; want each repository once", got)
	}
	// A completed job no longer covers a new request.
	if inserted, err := runtime.InsertSyncRepo(t.Context(), "acme/widgets"); err != nil || !inserted {
		t.Fatalf("InsertSyncRepo after completion = %v, %v; want a new job", inserted, err)
	}
}

func TestASyncRepoJobIsRetriedUpToJobMaxAttempts(t *testing.T) {
	w := newWorld(t, 3)
	var attempts atomic.Int32
	config := syncConfig(func(context.Context) error { return nil }, func(_ context.Context, repo string) error {
		if repo != "acme/widgets" {
			return errors.New("wrong repository")
		}
		if attempts.Add(1) < 2 {
			return errors.New("transient")
		}
		return nil
	})
	config.JobMaxAttempts = 3
	runtime := startRuntime(t, w, config)

	if inserted, err := runtime.InsertSyncRepo(t.Context(), "acme/widgets"); err != nil || !inserted {
		t.Fatalf("InsertSyncRepo = %v, %v", inserted, err)
	}

	waitUntil(t, "the retried job to complete", func() bool {
		return w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_sync_repo' AND state='completed'") == "1"
	})
	if got := w.scalar("SELECT max_attempts::text FROM river_job WHERE kind='github_sync_repo'"); got != "3" {
		t.Fatalf("max_attempts = %s; want JOB_MAX_ATTEMPTS", got)
	}
}

func TestSyncJobsNeedBothHandlersAndAreAbsentWithoutThem(t *testing.T) {
	w := newWorld(t, 5)
	for name, mutate := range map[string]func(*river.Config){
		"a tick without a repo handler": func(c *river.Config) {
			c.SyncTick, c.SyncRepo = noWake(func(context.Context) error { return nil }), nil
		},
		"a repo handler without a tick": func(c *river.Config) { c.SyncTick, c.SyncRepo = nil, noWakeRepo(noRepo) },
	} {
		config := testConfig(&logCapture{})
		mutate(&config)
		if _, err := river.NewRuntime(w.pool, w.store, config); err == nil {
			t.Errorf("%s: NewRuntime accepted the configuration", name)
		}
	}

	plain, err := river.NewRuntime(w.pool, w.store, testConfig(&logCapture{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.InsertSyncRepo(t.Context(), "acme/widgets"); err == nil {
		t.Error("InsertSyncRepo on a runtime without the synchronization jobs inserted a job nothing would run")
	}
}

// noWake adapts a run that wakes nothing; a nil run stays nil, which registers no job.
func noWake(run func(context.Context) error) func(context.Context) (river.Wake, error) {
	if run == nil {
		return nil
	}
	return func(ctx context.Context) (river.Wake, error) { return nil, run(ctx) }
}

func noWakeRepo(run func(context.Context, string) error) func(context.Context, string) (river.Wake, error) {
	return func(ctx context.Context, repo string) (river.Wake, error) { return nil, run(ctx, repo) }
}
