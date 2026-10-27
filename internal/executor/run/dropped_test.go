package run

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// nearDeadline gives the executor a margin that leaves the deadline of an hour-long attempt a second away.
func nearDeadline(cfg *Config, _ *driver) {
	cfg.Pacing = DefaultPacing
	cfg.Pacing.Margin = time.Hour - time.Second
}

// The only time kill of the executor is at the deadline of the attempt, less the margin (30 s): the context of the work ends, the sandbox
// kills the container with it, nothing is posted, and the service ends the attempt as timeout.
func TestTheAttemptIsDroppedAtItsDeadlineLessTheMargin(t *testing.T) {
	sv, rt := aJob(1) // an hour to run
	rt.gate, rt.entered, rt.stopped = make(chan struct{}), make(chan struct{}, 4), make(chan error, 4)
	svc := newService(t, sv)
	d := start(t, svc, rt, nearDeadline, func(_ *Config, d *driver) { d.sleeps.reached, d.patience = make(chan struct{}), 10*time.Second })
	wait(d, rt.entered)
	if err := wait(d, rt.stopped); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the container's context ended with %v; want the deadline", err)
	}
	wait(d, d.sleeps.reached) // the work was dropped and the executor asked for the next job
	d.noResult()
	if n := sv.results.Load(); n != 0 {
		t.Errorf("%d results were posted by an attempt that was dropped at its deadline", n)
	}
}

func TestTheMarginBeforeTheDeadlineIsThirtySeconds(t *testing.T) {
	if DefaultPacing.Margin != 30*time.Second {
		t.Errorf("the margin is %v; want 30 s", DefaultPacing.Margin)
	}
}

// A container that the executor killed (its deadline, the grace after the signal, a lease that was lost, or its own report of a kill) is
// never read as a resource limit, whatever status the runtime says the killed container ended with: the attempt is dropped and nothing is
// posted, so that the service ends it as timeout, or the lease expires. Only an out-of-memory kill, a status 137 that the executor did not
// cause, and output past the bound are resource limits.
func TestAContainerThatTheExecutorKilledIsNeverAResourceLimit(t *testing.T) {
	killed137 := func() (sandbox.Outcome, error) { return sandbox.Outcome{ExitCode: 137}, nil }
	for name, tc := range map[string]struct {
		setup func(*Config, *driver)
		drive func(*driver) // what ends the context of the work, once the container runs
		done  func(*driver) // what says that the executor has dropped the attempt
	}{
		"the deadline": {nearDeadline, func(*driver) {}, func(d *driver) { wait(d, d.sleeps.reached) }},
		"the grace after the signal": {
			func(_ *Config, d *driver) { d.sleeps.immediate = 1 }, func(d *driver) { d.cancel() }, func(d *driver) { _ = d.stopped() },
		},
		"a lease that was lost": {
			func(*Config, *driver) {}, func(d *driver) { d.tick() }, func(d *driver) { wait(d, d.sleeps.reached) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			sv, rt := aJob(1)
			sv.beat = func(int) int { return http.StatusConflict }
			rt.gate, rt.entered, rt.stopped, rt.onStop = make(chan struct{}), make(chan struct{}, 4), make(chan error, 4), killed137
			d := start(t, newService(t, sv), rt, tc.setup, func(_ *Config, d *driver) { d.sleeps.reached, d.patience = make(chan struct{}), 10*time.Second })
			wait(d, rt.entered)
			tc.drive(d)
			wait(d, rt.stopped)
			tc.done(d)
			d.noResult()
			if n := sv.results.Load(); n != 0 {
				t.Errorf("%d results were posted for a container that the executor killed", n)
			}
		})
	}
}

func TestAContainerThatTheSandboxSaysTheExecutorKilledIsDroppedNotPosted(t *testing.T) {
	sv, _ := aJob(1)
	rt := newRuntime(func(c call) (sandbox.Outcome, error) {
		out := goTest(c, pass)
		out.Killed, out.ExitCode = true, 137
		return out, nil
	})
	d := start(t, newService(t, sv), rt, func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
	wait(d, d.sleeps.reached)
	d.noResult()
	if n := sv.results.Load(); n != 0 {
		t.Errorf("%d results were posted for a container that the executor killed", n)
	}
}
