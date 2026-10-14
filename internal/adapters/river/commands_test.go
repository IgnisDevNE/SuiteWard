//go:build integration

package river_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

func commandsConfig(job func(context.Context) error) river.Config {
	config := testConfig(&logCapture{})
	config.Commands = job
	return config
}

func TestCommandsRunsAtStartOnceAndWithOneAttempt(t *testing.T) {
	w := newWorld(t, 5)
	var runs atomic.Int32
	// The period is an hour: a run seen within seconds ran because the periodic job has RunOnStart.
	config := commandsConfig(func(context.Context) error { runs.Add(1); return nil })
	config.CommandsInterval = time.Hour
	startRuntime(t, w, config)

	waitUntil(t, "the commands to run at start", func() bool { return runs.Load() >= 1 })

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + river.CommandsKind + "'"); got != "1" {
		t.Fatalf("%s commands jobs after the start; want exactly one", got)
	}
	if got := w.scalar("SELECT max_attempts::text FROM river_job WHERE kind='" + river.CommandsKind + "'"); got != "1" {
		t.Fatalf("max_attempts = %s; want 1: the next run is the retry", got)
	}
}

func TestCommandsRunsDoNotOverlap(t *testing.T) {
	w := newWorld(t, 5)
	release := make(chan struct{})
	var running, peak atomic.Int32
	config := commandsConfig(func(context.Context) error {
		peak.Store(max(peak.Load(), running.Add(1)))
		<-release
		running.Add(-1)
		return nil
	})
	config.CommandsInterval = 100 * time.Millisecond
	startRuntime(t, w, config)
	t.Cleanup(func() { close(release) })
	waitUntil(t, "the first run to start", func() bool { return running.Load() == 1 })

	// A negative assertion about a timer: let the periodic job fire a dozen times while the first run still runs.
	time.Sleep(1200 * time.Millisecond)

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + river.CommandsKind + "'"); got != "1" || peak.Load() != 1 {
		t.Fatalf("%s commands jobs and %d running at once while the first run still ran; want 1 and 1", got, peak.Load())
	}
}

func TestWithoutCommandsNoCommandsJobIsRegistered(t *testing.T) {
	w := newWorld(t, 5)
	var checks atomic.Int32
	config := commandsConfig(nil)
	config.SyncTick, config.SyncRepo = func(context.Context) error { checks.Add(1); return nil }, noRepo
	startRuntime(t, w, config)
	waitUntil(t, "the tick to run at start", func() bool { return checks.Load() >= 1 })

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + river.CommandsKind + "'"); got != "0" {
		t.Fatalf("%s commands jobs without a commands job; want none", got)
	}
}
