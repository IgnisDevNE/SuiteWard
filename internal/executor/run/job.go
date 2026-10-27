package run

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// handle runs one leased job and posts its result once. The work has its own context: it outlives the signal that stops the loop, ends at the
// deadline of the job (past it the service has ended the attempt), and ends when a heartbeat learns that the lease is lost. Work that ended
// that way posts nothing, and the service sees the job again once its lease expires.
func (e *Executor) handle(ctx context.Context, lease *client.Lease) {
	log := e.cfg.Log.With("job", lease.JobID, "plan", lease.Plan, "pull", lease.Pull)
	deadline, stopTimer := context.WithDeadline(context.WithoutCancel(ctx), lease.DeadlineAt)
	defer stopTimer()
	work, lost := context.WithCancel(deadline)
	defer lost()
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
