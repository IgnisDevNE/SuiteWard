package run

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// gate makes the wait of that kind, for the deadline of the attempt or for the grace after the signal, end when ch is closed.
func gate(kind waitKind, ch chan struct{}) func(*Config, *driver) {
	return func(_ *Config, d *driver) {
		if d.sleeps.gates == nil {
			d.sleeps.gates = map[waitKind]chan struct{}{}
		}
		d.sleeps.gates[kind] = ch
	}
}

func closed() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// The only time kill of the executor is at the deadline of the attempt, less the margin (30 s): the context of the work ends with the
// deadline as its cause, the sandbox kills the container with it, nothing is posted, and the service ends the attempt as timeout. The
// executor waits for that time through its Sleep, so the test ends the wait when it wants the drop, and races no clock.
func TestTheAttemptIsDroppedAtItsDeadlineLessTheMargin(t *testing.T) {
	sv, rt := aJob(1) // an hour to run
	rt.gate, rt.entered, rt.stopped = make(chan struct{}), make(chan struct{}, 4), make(chan error, 4)
	svc := newService(t, sv)
	fire := make(chan struct{})
	d := start(t, svc, rt, gate(deadlineWait, fire), func(_ *Config, d *driver) { d.sleeps.reached, d.patience = make(chan struct{}), 10*time.Second })
	wait(d, rt.entered)
	// The executor waits until the deadline less the margin: an hour from the creation of the job, less 30 s.
	var until time.Duration
	for _, w := range d.sleeps.durations() {
		if waitKindOf(w) == deadlineWait {
			until = w
		}
	}
	if until < 59*time.Minute || until > time.Hour-30*time.Second {
		t.Errorf("the executor waited %v for the deadline; want a little under an hour less 30 s", until)
	}
	close(fire)
	if cause := wait(d, rt.stopped); !errors.Is(cause, context.DeadlineExceeded) {
		t.Errorf("the container's context ended with the cause %v; want the deadline", cause)
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
	fire := make(chan struct{})
	for name, tc := range map[string]struct {
		setup func(*Config, *driver)
		drive func(*driver) // what ends the context of the work, once the container runs
		done  func(*driver) // what says that the executor has dropped the attempt
		cause error         // why the context of the work ended
	}{
		"the deadline": {
			gate(deadlineWait, fire), func(*driver) { close(fire) }, func(d *driver) { wait(d, d.sleeps.reached) }, context.DeadlineExceeded,
		},
		"the grace after the signal": {
			gate(graceWait, closed()), func(d *driver) { d.cancel() }, func(d *driver) { _ = d.stopped() }, errGraceOver,
		},
		"a lease that was lost": {
			func(*Config, *driver) {}, func(d *driver) { d.tick() }, func(d *driver) { wait(d, d.sleeps.reached) }, client.ErrLeaseLost,
		},
	} {
		t.Run(name, func(t *testing.T) {
			sv, rt := aJob(1)
			sv.beat = func(int) int { return http.StatusConflict }
			rt.gate, rt.entered, rt.stopped, rt.onStop = make(chan struct{}), make(chan struct{}, 4), make(chan error, 4), killed137
			d := start(t, newService(t, sv), rt, tc.setup, func(_ *Config, d *driver) { d.sleeps.reached, d.patience = make(chan struct{}), 10*time.Second })
			wait(d, rt.entered)
			tc.drive(d)
			if cause := wait(d, rt.stopped); !errors.Is(cause, tc.cause) { // the runtime saw the context end, and said that its container ended with the status 137
				t.Errorf("the context of the work ended with the cause %v; want %v", cause, tc.cause)
			}
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
