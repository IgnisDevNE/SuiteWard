//go:build integration

package river_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

func integrityConfig(job func(context.Context) error) river.Config {
	config := testConfig(&logCapture{})
	config.Integrity = job
	return config
}

func TestIntegrityRunsAtStartOnceAndWithOneAttempt(t *testing.T) {
	w := newWorld(t, 5)
	var runs atomic.Int32
	// The period is an hour: a run seen within seconds ran because the periodic job has RunOnStart.
	startRuntime(t, w, integrityConfig(func(context.Context) error { runs.Add(1); return nil }))

	waitUntil(t, "the integrity job to run at start", func() bool { return runs.Load() >= 1 })

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + river.IntegrityKind + "'"); got != "1" {
		t.Fatalf("%s integrity jobs after the start; want exactly one", got)
	}
	if got := w.scalar("SELECT max_attempts::text FROM river_job WHERE kind='" + river.IntegrityKind + "'"); got != "1" {
		t.Fatalf("max_attempts = %s; want 1: the next run is the retry", got)
	}
}

func TestIntegrityRunsDoNotOverlap(t *testing.T) {
	w := newWorld(t, 5)
	release := make(chan struct{})
	var running, peak atomic.Int32
	config := integrityConfig(func(context.Context) error {
		peak.Store(max(peak.Load(), running.Add(1)))
		<-release
		running.Add(-1)
		return nil
	})
	config.IntegrityInterval = 100 * time.Millisecond
	startRuntime(t, w, config)
	t.Cleanup(func() { close(release) })
	waitUntil(t, "the first run to start", func() bool { return running.Load() == 1 })

	// A negative assertion about a timer: let the periodic job fire a dozen times while the first run still runs.
	time.Sleep(1200 * time.Millisecond)

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + river.IntegrityKind + "'"); got != "1" || peak.Load() != 1 {
		t.Fatalf("%s integrity jobs and %d running at once while the first run still ran; want 1 and 1", got, peak.Load())
	}
}

func TestWithoutAnIntegrityJobNoIntegrityJobIsRegistered(t *testing.T) {
	w := newWorld(t, 5)
	var ticks atomic.Int32
	config := integrityConfig(nil)
	config.SyncTick, config.SyncRepo = noWake(func(context.Context) error { ticks.Add(1); return nil }), noWakeRepo(noRepo)
	startRuntime(t, w, config)
	waitUntil(t, "the tick to run at start", func() bool { return ticks.Load() >= 1 })

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + river.IntegrityKind + "'"); got != "0" {
		t.Fatalf("%s integrity jobs without one; want none", got)
	}
}
