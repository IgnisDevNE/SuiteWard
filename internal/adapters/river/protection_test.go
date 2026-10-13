//go:build integration

package river_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
)

func protectionConfig(check func(context.Context) error) river.Config {
	config := testConfig(&logCapture{})
	config.ProtectionCheck = check
	return config
}

func TestProtectionCheckRunsAtStartOnceAndWithOneAttempt(t *testing.T) {
	w := newWorld(t, 5)
	var checks atomic.Int32
	// The default period is 15 minutes: a check seen within seconds ran because the periodic job has RunOnStart.
	startRuntime(t, w, protectionConfig(func(context.Context) error { checks.Add(1); return nil }))

	waitUntil(t, "the check to run at start", func() bool { return checks.Load() >= 1 })

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_protection_check'"); got != "1" {
		t.Fatalf("%s protection jobs after the start; want exactly one", got)
	}
	if got := w.scalar("SELECT max_attempts::text FROM river_job WHERE kind='github_protection_check'"); got != "1" {
		t.Fatalf("max_attempts = %s; want 1: the next run is the retry", got)
	}
}

func TestProtectionChecksDoNotOverlap(t *testing.T) {
	w := newWorld(t, 5)
	release := make(chan struct{})
	var running, peak atomic.Int32
	config := protectionConfig(func(context.Context) error {
		peak.Store(max(peak.Load(), running.Add(1)))
		<-release
		running.Add(-1)
		return nil
	})
	config.ProtectionInterval = 100 * time.Millisecond
	startRuntime(t, w, config)
	t.Cleanup(func() { close(release) })
	waitUntil(t, "the first check to start", func() bool { return running.Load() == 1 })

	// A negative assertion about a timer: let the periodic job fire a dozen times while the first check still runs.
	time.Sleep(1200 * time.Millisecond)

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_protection_check'"); got != "1" || peak.Load() != 1 {
		t.Fatalf("%s protection jobs and %d running at once while the first check still ran; want 1 and 1", got, peak.Load())
	}
}

func TestWithoutAProtectionCheckNoProtectionJobIsRegistered(t *testing.T) {
	w := newWorld(t, 5)
	var checks atomic.Int32
	config := protectionConfig(nil)
	config.SyncTick, config.SyncRepo = func(context.Context) error { checks.Add(1); return nil }, noRepo
	startRuntime(t, w, config)
	waitUntil(t, "the tick to run at start", func() bool { return checks.Load() >= 1 })

	if got := w.scalar("SELECT count(*)::text FROM river_job WHERE kind='github_protection_check'"); got != "0" {
		t.Fatalf("%s protection jobs without a check; want none", got)
	}
}
