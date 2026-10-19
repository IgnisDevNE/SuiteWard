//go:build integration

package river_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

// periodicJobs are the periodic jobs of the GitHub integration (the synchronization tick has its own tests): set puts a
// job in a Config (nil removes it) and every sets its period.
var periodicJobs = []struct {
	name  string
	kind  string
	set   func(*river.Config, func(context.Context) error)
	every func(*river.Config, time.Duration)
}{
	{"bootstrap", river.BootstrapKind, func(c *river.Config, job func(context.Context) error) { c.Bootstrap = noWake(job) }, func(c *river.Config, d time.Duration) { c.BootstrapInterval = d }},
	{"checks", river.ChecksKind, func(c *river.Config, job func(context.Context) error) { c.Checks = job }, func(c *river.Config, d time.Duration) { c.ChecksInterval = d }},
	{"commands", river.CommandsKind, func(c *river.Config, job func(context.Context) error) { c.Commands = noWake(job) }, func(c *river.Config, d time.Duration) { c.CommandsInterval = d }},
	{"merges", river.MergesKind, func(c *river.Config, job func(context.Context) error) { c.Merges = noWake(job) }, func(c *river.Config, d time.Duration) { c.MergesInterval = d }},
	{"integrity", river.IntegrityKind, func(c *river.Config, job func(context.Context) error) { c.Integrity = job }, func(c *river.Config, d time.Duration) { c.IntegrityInterval = d }},
	{"protection", river.ProtectionCheckKind, func(c *river.Config, job func(context.Context) error) { c.ProtectionCheck = job }, func(c *river.Config, d time.Duration) { c.ProtectionInterval = d }},
}

func TestPeriodicJobsRunAtStartOnceAndWithOneAttempt(t *testing.T) {
	for _, job := range periodicJobs {
		t.Run(job.name, func(t *testing.T) {
			w := newWorld(t, 5)
			var runs atomic.Int32
			config := testConfig(&logCapture{})
			job.set(&config, func(context.Context) error { runs.Add(1); return nil })
			// The period is an hour: a run seen within seconds ran because the periodic job has RunOnStart.
			job.every(&config, time.Hour)
			startRuntime(t, w, config)

			waitUntil(t, "the job to run at start", func() bool { return runs.Load() >= 1 })

			if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + job.kind + "'"); got != "1" {
				t.Fatalf("%s %s jobs after the start; want exactly one", got, job.name)
			}
			if got := w.scalar("SELECT max_attempts::text FROM river_job WHERE kind='" + job.kind + "'"); got != "1" {
				t.Fatalf("max_attempts = %s; want 1: the next run is the retry", got)
			}
		})
	}
}

func TestPeriodicJobRunsDoNotOverlap(t *testing.T) {
	for _, job := range periodicJobs {
		t.Run(job.name, func(t *testing.T) {
			w := newWorld(t, 5)
			release := make(chan struct{})
			var running, peak atomic.Int32
			config := testConfig(&logCapture{})
			job.set(&config, func(context.Context) error {
				peak.Store(max(peak.Load(), running.Add(1)))
				<-release
				running.Add(-1)
				return nil
			})
			job.every(&config, 100*time.Millisecond)
			startRuntime(t, w, config)
			t.Cleanup(func() { close(release) })
			waitUntil(t, "the first run to start", func() bool { return running.Load() == 1 })

			// A negative assertion about a timer: let the periodic job fire a dozen times while the first run still runs.
			time.Sleep(1200 * time.Millisecond)

			if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + job.kind + "'"); got != "1" || peak.Load() != 1 {
				t.Fatalf("%s %s jobs and %d running at once while the first run still ran; want 1 and 1", got, job.name, peak.Load())
			}
		})
	}
}

func TestWithoutAPeriodicJobItsJobIsNotRegistered(t *testing.T) {
	for _, job := range periodicJobs {
		t.Run(job.name, func(t *testing.T) {
			w := newWorld(t, 5)
			var ticks atomic.Int32
			config := testConfig(&logCapture{})
			job.set(&config, nil)
			config.SyncTick, config.SyncRepo = noWake(func(context.Context) error { ticks.Add(1); return nil }), noWakeRepo(noRepo)
			startRuntime(t, w, config)
			waitUntil(t, "the tick to run at start", func() bool { return ticks.Load() >= 1 })

			if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='" + job.kind + "'"); got != "0" {
				t.Fatalf("%s %s jobs without one; want none", got, job.name)
			}
		})
	}
}
