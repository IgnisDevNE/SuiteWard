package run

import (
	"context"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

// testID names one test of a side: Package is the directory of its package relative to the repository root ("." for the root), Name the
// test and File the declaring file, a slash path relative to the root. Each profile's package has a TestID of its own that its adapter converts.
type testID struct{ Package, Name, File string }

// sideFiles is what a profile reads of a side before any container runs: the side and the bytes of the files that the profile asked for, by path.
type sideFiles struct {
	side
	files map[string][]byte
}

// sidePlan is what a profile decided of a side before its first container ran. Only the profile that made it reads it.
type sidePlan any

// profile is what the runner asks of a language (docs/decisions/0030, "The profile seam"). It is cut at "run one side", not at "a command and
// its parse": Go runs a side as several invocations in several containers, and a profile that needs one container for the whole side can.
type profile interface {
	// image is the container image that the profile runs in.
	image() string
	// plan reads a side and refuses what the profile cannot run, before any container: a toolchain that is too old, dependencies past a limit.
	plan(s sideFiles, l client.Limits, toolchain string) (sidePlan, error)
	// run runs the tests on a side, in the containers of box, and gives each test its observations in the order of the runs.
	run(ctx context.Context, box invoker, p sidePlan, tests []testID) (map[testID][]execution.Observation, error)
}

// invocation is one container of a side, as its profile chooses it: the standard input is the side's tree, then the entries that extra adds.
type invocation struct {
	image   string
	argv    []string
	env     []string
	workdir string
	// extra writes the profile's own entries to the tar, after the tree and before the completion marker.
	// It runs while the container reads its standard input, so what it fetches is streamed and not kept.
	extra func(ctx context.Context, app *source.Appender) error
}

// output is what one container said, once the invoker has found that it was not killed, cut short or dropped.
type output struct {
	stdout   []byte
	exitCode int
}

// invoker runs one container of a side. It is the only place that decides what an out-of-band signal means: a container that was killed for
// its memory, a status of 137, output that passed the bound and work that was dropped are errors here, before the profile reads a byte.
type invoker interface {
	invoke(ctx context.Context, in invocation) (output, error)
}
