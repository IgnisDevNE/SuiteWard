//go:build integration

package river_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// fastRetry retries almost at once, so tests do not wait for River's default backoff.
type fastRetry struct{}

func (fastRetry) NextRetry(*rivertype.JobRow) time.Time { return time.Now().Add(50 * time.Millisecond) }

func testConfig(logs *logCapture) river.Config {
	return river.Config{Logger: logs.logger(), Workers: 4, JobTimeout: 30 * time.Second, RetryPolicy: fastRetry{},
		OutboxMaxAttempts: 8, OutboxPollInterval: 200 * time.Millisecond, OutboxLease: time.Minute}
}

// startRuntime starts a runtime and stops it when the test ends.
func startRuntime(t *testing.T, w *world, config river.Config) *river.Runtime {
	t.Helper()
	runtime, err := river.NewRuntime(w.pool, w.store, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.StopAndCancel(ctx) // cleanup path: stopping an already stopped runtime reports an error
	})
	return runtime
}

// waitStatus waits until a system job and message reach the given states.
func waitStatus(t *testing.T, w *world, id int64, key string, want postgres.SystemStatus) {
	t.Helper()
	waitStatusWithin(t, w, id, key, want, 40*time.Second)
}

func waitStatusWithin(t *testing.T, w *world, id int64, key string, want postgres.SystemStatus, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var got postgres.SystemStatus
	for time.Now().Before(deadline) {
		var err error
		if got, err = w.store.SystemStatus(t.Context(), id, key); err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("status = %+v after %v, want %+v", got, within, want)
}

func TestProbeJobAndMessageAreProcessed(t *testing.T) {
	w := newWorld(t, 5)
	logs := &logCapture{}
	startRuntime(t, w, testConfig(logs))

	id := w.enqueue("probe-1", river.ProbeKind, river.ProbeKind, time.Time{})

	waitStatus(t, w, id, "probe-1", postgres.SystemStatus{JobState: "completed", OutboxState: "delivered"})
}

func TestCustomHandlersAndPublishersAreDispatchedByKind(t *testing.T) {
	w := newWorld(t, 5)
	var handled, published atomic.Int32
	config := testConfig(&logCapture{})
	config.Handlers = map[string]river.Handler{"demo.job": func(_ context.Context, args json.RawMessage) error {
		if string(args) == "{}" {
			handled.Add(1)
		}
		return nil
	}}
	config.Publishers = map[string]river.Publisher{"demo.message": func(_ context.Context, key string, _ json.RawMessage) error {
		if key == "m1" {
			published.Add(1)
		}
		return nil
	}}
	startRuntime(t, w, config)

	id := w.enqueue("m1", "demo.job", "demo.message", time.Time{})

	waitStatus(t, w, id, "m1", postgres.SystemStatus{JobState: "completed", OutboxState: "delivered"})
	if handled.Load() != 1 || published.Load() != 1 {
		t.Fatalf("handled %d and published %d times, want once each", handled.Load(), published.Load())
	}
}

func TestAlwaysFailingJobIsDiscardedAfterItsAttempts(t *testing.T) {
	w := newWorld(t, 3) // JOB_MAX_ATTEMPTS
	var attempts atomic.Int32
	config := testConfig(&logCapture{})
	config.Handlers = map[string]river.Handler{"flaky": func(context.Context, json.RawMessage) error {
		attempts.Add(1)
		return errors.New("always fails")
	}}
	startRuntime(t, w, config)

	id := w.enqueue("m1", "flaky", river.ProbeKind, time.Time{})

	waitStatus(t, w, id, "m1", postgres.SystemStatus{JobState: "discarded", OutboxState: "delivered"})
	if attempts.Load() != 3 {
		t.Fatalf("the handler ran %d times, want the 3 attempts of JOB_MAX_ATTEMPTS", attempts.Load())
	}
}

func TestScheduledJobSurvivesARestart(t *testing.T) {
	w := newWorld(t, 5)
	id := w.enqueue("m1", river.ProbeKind, river.ProbeKind, time.Now().Add(4*time.Second))
	first := startRuntime(t, w, testConfig(&logCapture{}))
	time.Sleep(time.Second)
	if err := first.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	// River may already have moved a job due within its scheduling horizon to available; it must not have run.
	if status, err := w.store.SystemStatus(t.Context(), id, "m1"); err != nil || (status.JobState != "scheduled" && status.JobState != "available") {
		t.Fatalf("after the stop: %+v, %v; want the job waiting for its time", status, err)
	}

	startRuntime(t, w, testConfig(&logCapture{}))

	waitStatus(t, w, id, "m1", postgres.SystemStatus{JobState: "completed", OutboxState: "delivered"})
}

// A process that is killed hard leaves its running job behind. The rescuer of
// the next runtime re-runs it once RescueStuckJobsAfter has passed.
func TestJobOfAKilledProcessIsRescued(t *testing.T) {
	w := newWorld(t, 5)
	id := w.enqueue("m1", river.ProbeKind, river.ProbeKind, time.Time{})
	if _, err := w.pool.Exec(t.Context(), "UPDATE river_job SET state='running', attempt=1, attempted_at=now()-interval '1 hour', attempted_by=ARRAY['killed-process'] WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	config := testConfig(&logCapture{})
	config.JobTimeout, config.RescueStuckJobsAfter = 500*time.Millisecond, time.Second
	startRuntime(t, w, config)

	waitStatus(t, w, id, "m1", postgres.SystemStatus{JobState: "completed", OutboxState: "delivered"})
	if got := w.scalar("SELECT attempt::text FROM river_job WHERE id=$1", id); got != "2" {
		t.Fatalf("attempt = %s, want the rescued job to run as attempt 2", got)
	}
}

// A handler that never returns stands in for a process killed during a job:
// its attempt is rescued once RescueStuckJobsAfter has passed and runs again.
// River's rescuer ticks every 30 seconds, which this test has to wait for.
func TestJobWhoseHandlerNeverReturnsIsRescued(t *testing.T) {
	w := newWorld(t, 5)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int32
	config := testConfig(&logCapture{})
	config.JobTimeout, config.RescueStuckJobsAfter = 500*time.Millisecond, time.Second
	config.Handlers = map[string]river.Handler{"stuck": func(context.Context, json.RawMessage) error {
		if calls.Add(1) == 1 {
			<-release // ignores its context: the attempt of a dead process
		}
		return nil
	}}
	startRuntime(t, w, config)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) }) // runs before the runtime stops
	id := w.enqueue("m1", "stuck", river.ProbeKind, time.Time{})

	waitStatusWithin(t, w, id, "m1", postgres.SystemStatus{JobState: "completed", OutboxState: "delivered"}, 90*time.Second)
	releaseOnce.Do(func() { close(release) })
	time.Sleep(500 * time.Millisecond) // the stale attempt reports its end
	if got := w.scalar("SELECT state::text || ' ' || attempt FROM river_job WHERE id=$1", id); got != "completed 2" || calls.Load() != 2 {
		t.Fatalf("job %q after %d handler calls, want completed on attempt 2 after two calls, untouched by the stale first attempt", got, calls.Load())
	}
}

func TestSoftStopLetsARunningJobFinish(t *testing.T) {
	w := newWorld(t, 5)
	started, release := make(chan struct{}), make(chan struct{})
	config := testConfig(&logCapture{})
	config.Handlers = map[string]river.Handler{"slow": func(context.Context, json.RawMessage) error {
		close(started)
		<-release
		return nil
	}}
	runtime := startRuntime(t, w, config)
	id := w.enqueue("m1", "slow", river.ProbeKind, time.Time{})
	<-started

	stopped := make(chan error, 1)
	go func() { stopped <- runtime.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) while a job was still running", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if status, err := w.store.SystemStatus(t.Context(), id, "m1"); err != nil || status.JobState != "completed" {
		t.Fatalf("after Stop: %+v, %v; want the running job completed", status, err)
	}
}

func TestStopAndCancelInterruptsARunningJob(t *testing.T) {
	w := newWorld(t, 5)
	started := make(chan struct{})
	interrupted := make(chan error, 1)
	config := testConfig(&logCapture{})
	config.Handlers = map[string]river.Handler{"slow": func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-ctx.Done()
		interrupted <- ctx.Err()
		return ctx.Err()
	}}
	runtime := startRuntime(t, w, config)
	w.enqueue("m1", "slow", river.ProbeKind, time.Time{})
	<-started

	if err := runtime.StopAndCancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-interrupted; !errors.Is(err, context.Canceled) {
		t.Fatalf("job context error = %v, want canceled", err)
	}
}

func TestNewRuntimeChecksItsDependencies(t *testing.T) {
	w := newWorld(t, 5)
	config := testConfig(&logCapture{})
	if _, err := river.NewRuntime(nil, w.store, config); err == nil {
		t.Error("NewRuntime accepted a nil pool")
	}
	if _, err := river.NewRuntime(w.pool, nil, config); err == nil {
		t.Error("NewRuntime accepted a nil outbox store")
	}
	config.Workers = 0
	if _, err := river.NewRuntime(w.pool, w.store, config); err == nil {
		t.Error("NewRuntime accepted no workers")
	}
	config = testConfig(&logCapture{})
	config.Logger = nil
	if _, err := river.NewRuntime(w.pool, w.store, config); err == nil {
		t.Error("NewRuntime accepted a nil logger")
	}
	config = testConfig(&logCapture{})
	config.Handlers = map[string]river.Handler{"Bad Kind": func(context.Context, json.RawMessage) error { return nil }}
	if _, err := river.NewRuntime(w.pool, w.store, config); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Errorf("NewRuntime with an invalid handler kind = %v, want ErrInvalidRequest", err)
	}
	config.Handlers = map[string]river.Handler{"demo": nil}
	if _, err := river.NewRuntime(w.pool, w.store, config); err == nil {
		t.Error("NewRuntime accepted a nil handler")
	}
	config = testConfig(&logCapture{})
	config.OutboxMaxAttempts = 0
	if _, err := river.NewRuntime(w.pool, w.store, config); err == nil {
		t.Error("NewRuntime accepted an outbox without attempts")
	}
}

func TestNewInserterRejectsANegativeAttemptLimit(t *testing.T) {
	if _, err := river.NewInserter(-1); err == nil {
		t.Fatal("NewInserter accepted a negative attempt limit")
	}
}
