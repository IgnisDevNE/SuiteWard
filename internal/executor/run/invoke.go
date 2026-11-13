package run

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

const (
	// stdoutLimit is the most of the report of one invocation that is kept. More than that is never read.
	stdoutLimit = 64 << 20
)

// side is a side of a plan: the commit that it runs and the tree that its container gets.
type side struct {
	which source.Side
	tree  []execution.TreeFile
}

// profile is the profile of the job: Go, until a job names another.
func (a *attempt) profile() profile { return goProfile{proxy: a.e.cfg.Proxy, timeout: a.timeout()} }

// prepare has the profile plan a side before any container runs, so that what it cannot run is refused before the first container of any side.
// files are the files that the profile reads of the side, by path.
func (a *attempt) prepare(s side, files map[string][]byte) (sidePlan, error) {
	return a.profile().plan(sideFiles{side: s, files: files}, a.lease.Limits, a.toolchain)
}

// runSide has the profile run the tests on a side, in the containers of the attempt.
func (a *attempt) runSide(ctx context.Context, s side, p sidePlan, tests []testID) (map[testID][]execution.Observation, error) {
	return a.profile().run(ctx, a.box(s), p, tests)
}

// sideInvoker runs the containers of one side on the runtime of the attempt.
type sideInvoker struct {
	a *attempt
	s side
}

func (a *attempt) box(s side) invoker { return sideInvoker{a, s} }

// invoke runs one invocation of a side in one container, whose standard input is the tree and the profile's entries as one tar. The tar is
// composed while the container reads it, so a failure of the composition is the container's standard input failing, which stops it.
func (b sideInvoker) invoke(ctx context.Context, in invocation) (output, error) {
	l := b.a.lease.Limits
	stdin, finish := b.stream(ctx, in.extra)
	out, err := b.a.e.cfg.Runtime.Run(ctx, sandbox.Spec{
		Image: in.image, Env: in.env, Argv: in.argv, Workdir: in.workdir, Stdin: stdin, StdoutLimit: stdoutLimit,
		// There is no clock of the executor's own: an invocation holds a cold build and many packages, and go test kills a test binary itself, a
		// minute after its -timeout. The one time kill is the end of the context of the work, at the deadline of the attempt.
		Limits: sandbox.Limits{MemoryMiB: l.MemoryMiB, CPUs: l.CPUs, Pids: l.Pids, TmpfsMiB: l.TmpfsMiB},
	})
	finish() // the composition is over, or the runner never read it: either way nothing is left writing
	switch {
	case err != nil:
		return output{}, err
	case ctx.Err() != nil:
		// The work was dropped (its deadline, the grace after the signal, a lost lease): the status of the container that was killed for it
		// is the executor's own doing, and says nothing of the tests.
		return output{}, fmt.Errorf("the work was dropped: %w", ctx.Err())
	case out.OOMKilled || out.ExitCode == 137:
		// Only the out-of-band signals count, and before the report is read: a report of a killed container says nothing.
		return output{}, refuse(reasonResourceLimit, "the container was killed (out of memory %t, status %d)", out.OOMKilled, out.ExitCode)
	case out.Truncated:
		return output{}, refuse(reasonResourceLimit, "the output of the container passed %d bytes, and output that was cut is never read", stdoutLimit)
	}
	return output{stdout: out.Stdout, exitCode: out.ExitCode}, nil
}

// errStdinDone is what a write to the standard input meets when the container has gone.
var errStdinDone = errors.New("the sandbox has stopped reading its standard input")

// stream composes the tar of a side into a pipe, which is the container's standard input. finish stops the composition, if it still runs,
// and waits for it: a runner that failed before it read the input would leave the writer blocked for good.
func (b sideInvoker) stream(ctx context.Context, extra func(context.Context, *source.Appender) error) (io.Reader, func()) {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = pw.CloseWithError(b.compose(ctx, pw, extra))
	}()
	return pr, func() {
		cancel()
		_ = pr.CloseWithError(errStdinDone)
		<-done
	}
}

// compose writes the tar: the tree, the entries of the profile (extra is nil for a profile that has none), and the completion marker.
func (b sideInvoker) compose(ctx context.Context, w io.Writer, extra func(context.Context, *source.Appender) error) error {
	a := b.a
	app, err := source.Compose(ctx, w, b.s.tree, a.archives, a.entries, a.files, a.limits)
	if err != nil {
		return err
	}
	if extra != nil {
		if err := extra(ctx, app); err != nil {
			return err
		}
	}
	return app.Close()
}
