package run

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// handle runs one leased job and posts its result once. The work has its own context, which ends with a cause: the grace after the signal
// that stops the loop (30 s) is over, the deadline of the job less 30 s has come (the only time kill: the service then ends the attempt as
// timeout), or a heartbeat learned that the lease is lost. The two times are waited for through Sleep. Work that ended that way posts
// nothing, and the service sees the job again once its lease expires or its deadline passes. Ending the context of the work stops the
// sandbox, which kills its container, and a container that was killed so is never read as a resource limit.
func (e *Executor) handle(ctx context.Context, lease *client.Lease) {
	log := e.cfg.Log.With("job", lease.JobID, "plan", lease.Plan, "pull", lease.Pull)
	work, drop := context.WithCancelCause(context.WithoutCancel(ctx))
	defer drop(nil)
	if time.Until(lease.DeadlineAt.Add(-e.cfg.Pacing.Margin)) <= 0 {
		// A lease that is already past its deadline less the margin never runs: the service ends the attempt as timeout.
		drop(context.DeadlineExceeded)
		log.Info("the work was dropped and nothing is posted", "cause", context.Cause(work))
		return
	}
	over, finish := context.WithCancel(work) // ends with the work, and when the attempt is over
	timers := make(chan struct{}, 2)
	go func() { e.dropAtDeadline(over, lease.DeadlineAt, drop, log); timers <- struct{}{} }()
	go func() { e.grace(ctx, over, drop, log); timers <- struct{}{} }()
	defer func() { finish(); <-timers; <-timers }()
	beat, stopBeat := context.WithCancel(work)
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		e.heartbeat(beat, lease, func() { drop(client.ErrLeaseLost) }, log)
	}()

	body, abandon := e.attempt(work, lease, log)
	stopBeat()
	<-beating
	if abandon || work.Err() != nil {
		log.Info("the work was dropped and nothing is posted", "cause", context.Cause(work))
		return
	}
	state, err := lease.Result(work, body)
	switch {
	case errors.Is(err, client.ErrLeaseLost):
		log.Info("the lease was lost before the result was posted: the result is discarded")
	case err != nil:
		log.Warn("the result was not accepted: its lease will expire", "error", err)
	default:
		log.Info("the result was posted", "state", state)
	}
}

// dropAtDeadline waits until the deadline of the attempt less the margin, and then drops the work with the deadline as the cause. An attempt
// that ends first ends over, and so ends the wait.
func (e *Executor) dropAtDeadline(over context.Context, deadline time.Time, drop context.CancelCauseFunc, log *slog.Logger) {
	if e.cfg.Sleep(over, time.Until(deadline.Add(-e.cfg.Pacing.Margin))) == nil {
		log.Warn("the attempt is near its deadline: it is dropped, and the service ends it as timeout", "margin", e.cfg.Pacing.Margin)
		drop(context.DeadlineExceeded)
	}
}

// grace gives the job in flight the time after the signal: when it has passed, and the job is still running (over has not ended), the work
// is dropped. A job that ends in time ends over, and so ends the wait.
func (e *Executor) grace(signal, over context.Context, drop context.CancelCauseFunc, log *slog.Logger) {
	select {
	case <-signal.Done():
	case <-over.Done():
		return
	}
	if e.cfg.Sleep(over, e.cfg.Pacing.Grace) == nil {
		log.Warn("the grace after the signal is over: the job is dropped and its lease will expire", "grace", e.cfg.Pacing.Grace)
		drop(errGraceOver)
	}
}

// heartbeat extends the lease at each tick until ctx ends. A lost lease, or a job that the service no longer knows, ends the work.
func (e *Executor) heartbeat(ctx context.Context, lease *client.Lease, lost context.CancelFunc, log *slog.Logger) {
	ticks, stop := e.cfg.Tick(e.cfg.Pacing.Heartbeat)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		switch err := lease.Heartbeat(ctx); {
		case err == nil, ctx.Err() != nil:
		case errors.Is(err, client.ErrLeaseLost), errors.Is(err, client.ErrNotFound):
			log.Info("the lease is gone: the work stops", "error", err)
			lost()
			return
		default:
			log.Warn("a heartbeat failed", "error", err)
		}
	}
}

// attempt runs the job and gives the body of its result: complete when the run completed, and otherwise unavailable with the reason that
// the failure has. abandon says that nothing is to be posted.
func (e *Executor) attempt(ctx context.Context, lease *client.Lease, log *slog.Logger) (body []byte, abandon bool) {
	started := e.cfg.Now()
	done, err := e.execute(ctx, lease)
	if err == nil {
		err = done.check()
	}
	if err == nil {
		body, err = e.marshalComplete(lease, started, done)
		if err == nil && len(body) > maxBodyBytes {
			err = refuse(reasonTooLarge, "the result is %d bytes; at most %d are accepted", len(body), maxBodyBytes)
		}
	}
	if err == nil {
		return body, false
	}
	reason, abandon := classify(ctx, err)
	if abandon {
		log.Info("the attempt ended without a result", "error", err)
		return nil, true
	}
	log.Warn("the run is unavailable", "reason", reason, "error", err)
	body, err = e.marshalUnavailable(lease, started, reason)
	if err != nil {
		log.Error("the result could not be written", "error", err)
		return nil, true
	}
	return body, false
}

// execute runs the plan of the job.
func (e *Executor) execute(ctx context.Context, lease *client.Lease) (completion, error) {
	if lease.Profile.ID != "go" || lease.Profile.Image != goprofile.Image {
		return completion{}, refuse(reasonProfileUnsupported, "the job names the profile %q with the image %q, which this executor lacks", lease.Profile.ID, lease.Profile.Image)
	}
	toolchain, err := e.goVersion(ctx)
	if err != nil {
		return completion{}, err
	}
	a, err := newAttempt(e, lease, toolchain)
	if err != nil {
		return completion{}, err
	}
	if lease.Plan == client.PlanCanonical {
		return a.canonical(ctx)
	}
	return a.receipt(ctx)
}

// A test invocation's timeout is the job's, which is the -timeout of go test.
func (a *attempt) timeout() time.Duration {
	return time.Duration(a.lease.Limits.TestTimeoutSeconds) * time.Second
}

// errGraceOver is the cause of the end of the work that the grace after the signal makes.
var errGraceOver = errors.New("the grace after the signal is over")
