package run

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// handle runs one leased job and posts its result once. The work has its own context: it outlives the signal that stops the loop by the
// grace (30 s), ends 30 s before the deadline of the job (the only time kill: the service then ends the attempt as timeout), and ends when a
// heartbeat learns that the lease is lost. Work that ended that way posts nothing, and the service sees the job again once its lease expires
// or its deadline passes. Ending the context of the work stops the sandbox, which kills its container, and a container that was killed so is
// never read as a resource limit.
func (e *Executor) handle(ctx context.Context, lease *client.Lease) {
	log := e.cfg.Log.With("job", lease.JobID, "plan", lease.Plan, "pull", lease.Pull)
	deadline, stopTimer := context.WithDeadline(context.WithoutCancel(ctx), lease.DeadlineAt.Add(-e.cfg.Pacing.Margin))
	defer stopTimer()
	work, lost := context.WithCancel(deadline)
	defer lost()
	graceCtx, stopGrace := context.WithCancel(work)
	graced := make(chan struct{})
	go func() {
		defer close(graced)
		e.grace(ctx, graceCtx, lost, log)
	}()
	defer func() { stopGrace(); <-graced }()
	beat, stopBeat := context.WithCancel(work)
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		e.heartbeat(beat, lease, lost, log)
	}()

	body, abandon := e.attempt(work, lease, log)
	stopBeat()
	<-beating
	if abandon || work.Err() != nil {
		log.Info("the work was dropped and nothing is posted")
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

// grace gives the job in flight the time after the signal: when it has passed, and the job is still running (over has not ended), the work
// is dropped. A job that ends in time ends over, and so ends the wait.
func (e *Executor) grace(signal, over context.Context, drop context.CancelFunc, log *slog.Logger) {
	select {
	case <-signal.Done():
	case <-over.Done():
		return
	}
	if e.cfg.Sleep(over, e.cfg.Pacing.Grace) == nil {
		log.Warn("the grace after the signal is over: the job is dropped and its lease will expire", "grace", e.cfg.Pacing.Grace)
		drop()
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
