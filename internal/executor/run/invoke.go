package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/prefetch"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

const (
	// stdoutLimit is the most of the report of one invocation that is kept. More than that is never read.
	stdoutLimit = 64 << 20
)

// side is a side of a plan: the commit that it runs, the tree that its container gets, and what its module needs.
type side struct {
	which  source.Side
	tree   []execution.TreeFile
	module string // the module path of the root go.mod; empty when go has no module to run
	plan   prefetch.Plan
}

// prepare reads the module of a side before any container runs. A side without a go.mod, or one that declares no module, has nothing that go
// can run: every test of it reads build-error, as go itself would say. Otherwise its Go must not be newer than the image's, and its modules
// are planned, which refuses what the contract refuses before anything is fetched.
func (a *attempt) prepare(s *side, goMod, goSum []byte) error {
	if len(goMod) == 0 {
		return nil
	}
	if needsNewerGo(goMod, a.toolchain) {
		return refuse(reasonToolchainMismatch, "the go.mod of the %s asks for a Go newer than %s", s.which, a.toolchain)
	}
	vendors := false
	for _, f := range s.tree {
		vendors = vendors || f.Path == "vendor/modules.txt"
	}
	plan, err := prefetch.PlanSide(goMod, goSum, vendors, prefetch.Limits{Modules: a.lease.Limits.PrefetchModules, Bytes: a.lease.Limits.PrefetchBytes})
	if err != nil {
		return err
	}
	s.plan, s.module = plan, modfile.ModulePath(goMod)
	return nil
}

// runSide runs the tests on a side: one invocation of go test for each chunk of them, then another for the tests that failed or reported
// nothing, up to the job's reruns. It gives each test its observations in the order of the runs.
func (a *attempt) runSide(ctx context.Context, s *side, tests []goprofile.TestID) (map[goprofile.TestID][]execution.Observation, error) {
	runs := make(map[goprofile.TestID][]execution.Observation, len(tests))
	if s.module == "" {
		for _, id := range tests {
			runs[id] = []execution.Observation{execution.ObservedBuildError}
		}
		return runs, nil
	}
	pending := tests
	for round := 1; len(pending) > 0; round++ {
		latest := map[goprofile.TestID]execution.Observation{}
		for _, chunk := range chunks(pending, a.timeout(), s.plan.Vendor) {
			got, err := a.invoke(ctx, s, chunk)
			if err != nil {
				return nil, err
			}
			maps.Copy(latest, got)
		}
		for id, o := range latest {
			runs[id] = append(runs[id], o)
		}
		pending = goprofile.Reruns(latest, round, 1+a.lease.Limits.Reruns)
	}
	return runs, nil
}

// invoke runs one go test invocation of a side in one container, whose standard input is the tree and the module files as one tar. The tar
// is composed while the container reads it, so a failure of the composition is the container's standard input failing, which stops it.
func (a *attempt) invoke(ctx context.Context, s *side, tests []goprofile.TestID) (map[goprofile.TestID]execution.Observation, error) {
	argv, env := goprofile.Command(tests, a.timeout(), s.plan.Vendor)
	l := a.lease.Limits
	stdin, finish := a.stream(ctx, s)
	out, err := a.e.cfg.Runtime.Run(ctx, sandbox.Spec{
		Image: goprofile.Image, Env: env, Argv: argv, Workdir: goprofile.TreeDir, Stdin: stdin, StdoutLimit: stdoutLimit,
		// There is no clock of the executor's own: an invocation holds a cold build and many packages, and go test kills a test binary itself, a
		// minute after its -timeout. The one time kill is the end of the context of the work, at the deadline of the attempt.
		Limits: sandbox.Limits{MemoryMiB: l.MemoryMiB, CPUs: l.CPUs, Pids: l.Pids, TmpfsMiB: l.TmpfsMiB},
	})
	finish() // the composition is over, or the runner never read it: either way nothing is left writing
	switch {
	case err != nil:
		return nil, err
	case ctx.Err() != nil:
		// The work was dropped (its deadline, the grace after the signal, a lost lease): the status of the container that was killed for it
		// is the executor's own doing, and says nothing of the tests.
		return nil, fmt.Errorf("the work was dropped: %w", ctx.Err())
	case out.OOMKilled || out.ExitCode == 137:
		// Only the out-of-band signals count, and before the report is read: a report of a killed container says nothing.
		return nil, refuse(reasonResourceLimit, "the container was killed (out of memory %t, status %d)", out.OOMKilled, out.ExitCode)
	case out.Truncated:
		return nil, refuse(reasonResourceLimit, "the output of go test passed %d bytes, and output that was cut is never read", stdoutLimit)
	}
	return goprofile.Parse(out.Stdout, out.ExitCode, s.module, tests)
}

// errStdinDone is what a write to the standard input meets when the container has gone.
var errStdinDone = errors.New("the sandbox has stopped reading its standard input")

// stream composes the tar of a side into a pipe, which is the container's standard input. finish stops the composition, if it still runs,
// and waits for it: a runner that failed before it read the input would leave the writer blocked for good.
func (a *attempt) stream(ctx context.Context, s *side) (io.Reader, func()) {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = pw.CloseWithError(a.compose(ctx, pw, s))
	}()
	return pr, func() {
		cancel()
		_ = pr.CloseWithError(errStdinDone)
		<-done
	}
}

// compose writes the tar: the tree, a placeholder in go's HOME and TMPDIR (go stops when TMPDIR does not exist, and tar makes the directories
// of the files it extracts), the prefetched module files, and the completion marker. The size check is here: the tree and the module zips are
// counted as they are added, and the unpacked size of the zips is charged once they have passed, before the marker that lets go test start.
func (a *attempt) compose(ctx context.Context, w io.Writer, s *side) error {
	app, err := source.Compose(ctx, w, s.tree, a.archives, a.entries, a.files, a.limits)
	if err != nil {
		return err
	}
	for _, dir := range []string{goprofile.HomeDir, goprofile.TmpDir} {
		if err := app.Add(dir+"/.keep", 0, strings.NewReader("")); err != nil {
			return err
		}
	}
	if len(s.plan.Files) > 0 {
		l := a.lease.Limits
		stats, err := prefetch.Fetch(ctx, a.e.cfg.Proxy.Client, a.e.cfg.Proxy.Base, s.plan, proxySink{app}, prefetch.Limits{Modules: l.PrefetchModules, Bytes: l.PrefetchBytes})
		if err != nil {
			return fmt.Errorf("prefetch the modules of the %s: %w", s.which, err)
		}
		if err := app.Spend(stats.Unpacked); err != nil {
			return err
		}
	}
	return app.Close()
}

// proxySink puts the module files of the prefetch under the module proxy's directory.
type proxySink struct{ app *source.Appender }

func (p proxySink) Add(name string, size int64, r io.Reader) error {
	return p.app.Add(goprofile.ProxyDir+"/"+name, size, r)
}
