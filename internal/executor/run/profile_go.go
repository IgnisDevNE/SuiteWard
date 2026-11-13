package run

import (
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/prefetch"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

// goProfile is the Go adapter of the seam: a side runs as one go test invocation for each chunk of its tests, in a container each, and then
// again for the tests that failed or reported nothing.
type goProfile struct {
	proxy   Proxy         // the module proxy that the dependency prefetch reads
	timeout time.Duration // the -timeout of go test: the job's
}

// goPlan is what goProfile plans of a side: its module, which go has nothing to run without, and the dependencies that the module needs.
type goPlan struct {
	which   source.Side
	module  string // the module path of the root go.mod; empty when go has no module to run
	modules prefetch.Plan
	limits  prefetch.Limits
	reruns  int
}

func (goProfile) image() string { return goprofile.Image }

// plan reads the module of a side before any container runs. A side without a go.mod, or one that declares no module, has nothing that go
// can run: every test of it reads build-error, as go itself would say. Otherwise its Go must not be newer than the image's, and its modules
// are planned, which refuses what the contract refuses before anything is fetched.
func (goProfile) plan(s sideFiles, l client.Limits, toolchain string) (sidePlan, error) {
	goMod, goSum := s.files["go.mod"], s.files["go.sum"]
	p := goPlan{which: s.which, limits: prefetch.Limits{Modules: l.PrefetchModules, Bytes: l.PrefetchBytes}, reruns: l.Reruns}
	if len(goMod) == 0 {
		return p, nil
	}
	if needsNewerGo(goMod, toolchain) {
		return nil, refuse(reasonToolchainMismatch, "the go.mod of the %s asks for a Go newer than %s", s.which, toolchain)
	}
	vendors := false
	for _, f := range s.tree {
		vendors = vendors || f.Path == "vendor/modules.txt"
	}
	modules, err := prefetch.PlanSide(goMod, goSum, vendors, p.limits)
	if err != nil {
		return nil, err
	}
	p.modules, p.module = modules, modfile.ModulePath(goMod)
	return p, nil
}

// run runs the tests on a side: one invocation of go test for each chunk of them, then another for the tests that failed or reported
// nothing, up to the job's reruns. It gives each test its observations in the order of the runs.
func (g goProfile) run(ctx context.Context, box invoker, p sidePlan, tests []testID) (map[testID][]execution.Observation, error) {
	plan, ok := p.(goPlan)
	if !ok {
		return nil, fmt.Errorf("the plan of a side that the Go profile runs is a %T", p)
	}
	runs := make(map[testID][]execution.Observation, len(tests))
	if plan.module == "" {
		for _, id := range tests {
			runs[id] = []execution.Observation{execution.ObservedBuildError}
		}
		return runs, nil
	}
	pending := toGo(tests)
	for round := 1; len(pending) > 0; round++ {
		latest := map[goprofile.TestID]execution.Observation{}
		for _, chunk := range chunks(pending, g.timeout, plan.modules.Vendor) {
			got, err := g.runChunk(ctx, box, plan, chunk)
			if err != nil {
				return nil, err
			}
			maps.Copy(latest, got)
		}
		for id, o := range latest {
			runs[testID(id)] = append(runs[testID(id)], o)
		}
		pending = goprofile.Reruns(latest, round, 1+plan.reruns)
	}
	return runs, nil
}

// runChunk runs one go test invocation of a side in one container. The invoker has found by then that the container was not killed, cut
// short or dropped, so what it gives is a report to read.
func (g goProfile) runChunk(ctx context.Context, box invoker, p goPlan, tests []goprofile.TestID) (map[goprofile.TestID]execution.Observation, error) {
	argv, env := goprofile.Command(tests, g.timeout, p.modules.Vendor)
	out, err := box.invoke(ctx, invocation{image: g.image(), argv: argv, env: env, workdir: goprofile.TreeDir, extra: g.entries(p)})
	if err != nil {
		return nil, err
	}
	return goprofile.Parse(out.stdout, out.exitCode, p.module, tests)
}

// entries are the tar entries that go needs beside the tree: a placeholder in go's HOME and TMPDIR (go stops when TMPDIR does not exist, and
// tar makes the directories of the files it extracts) and the prefetched module files. The size check is here: the module zips are counted as
// they are added, and their unpacked size is charged once they have passed, before the marker that lets go test start.
func (g goProfile) entries(p goPlan) func(context.Context, *source.Appender) error {
	return func(ctx context.Context, app *source.Appender) error {
		for _, dir := range []string{goprofile.HomeDir, goprofile.TmpDir} {
			if err := app.Add(dir+"/.keep", 0, strings.NewReader("")); err != nil {
				return err
			}
		}
		if len(p.modules.Files) > 0 {
			stats, err := prefetch.Fetch(ctx, g.proxy.Client, g.proxy.Base, p.modules, proxySink{app}, p.limits)
			if err != nil {
				return fmt.Errorf("prefetch the modules of the %s: %w", p.which, err)
			}
			if err := app.Spend(stats.Unpacked); err != nil {
				return err
			}
		}
		return nil
	}
}

// proxySink puts the module files of the prefetch under the module proxy's directory.
type proxySink struct{ app *source.Appender }

func (p proxySink) Add(name string, size int64, r io.Reader) error {
	return p.app.Add(goprofile.ProxyDir+"/"+name, size, r)
}

// fromGo and toGo convert the tests between the run's own type and goprofile's, whose underlying types are the same.
func fromGo(ids []goprofile.TestID) []testID {
	out := make([]testID, len(ids))
	for i, id := range ids {
		out[i] = testID(id)
	}
	return out
}

func toGo(ids []testID) []goprofile.TestID {
	out := make([]goprofile.TestID, len(ids))
	for i, id := range ids {
		out[i] = goprofile.TestID(id)
	}
	return out
}
