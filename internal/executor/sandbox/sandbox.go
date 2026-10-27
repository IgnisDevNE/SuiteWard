// Package sandbox runs one disposable container through the CLI of a container runtime, with the isolation that ADR 0028
// section 11 requires, and reports what only the runtime can say: the exit status, an out-of-memory kill and the executor's own
// kill. It never runs a weaker sandbox: a Spec that asks for less, or a runtime that cannot honor a property, is an error.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var (
	// ErrUnavailable reports a runtime that cannot build or run the sandbox, or cannot honor a required property (a cgroup
	// controller that is missing, an image it does not hold). The run is unavailable with the reason sandbox-unavailable.
	ErrUnavailable = errors.New("the sandbox runtime is unavailable")
	// ErrInvalidSpec reports a Spec the runner refuses before it runs any command.
	ErrInvalidSpec = errors.New("invalid sandbox spec")
	// ErrSetupFailed reports a container that failed before Argv started: tar could not extract the standard input, the completion marker
	// was missing from it, or the work directory is not in the tree. The container exited with the reserved status 113, so an Argv that
	// exits 113 reads the same.
	ErrSetupFailed = errors.New("the sandbox failed before the program started")
)

// Limits are the resource limits of one sandbox. Every one but KillAfter is required: the runtimes read zero as no limit.
type Limits struct {
	MemoryMiB int // the memory limit; the swap limit is the same, and the tmpfs counts in it
	CPUs      int
	Pids      int
	TmpfsMiB  int           // the size of /work
	KillAfter time.Duration // the runner kills the container this long after it starts; zero is no clock (the executor passes none: its bound is the context), kept for the tagged integration tests of this package and of source
}

// Spec describes one run.
type Spec struct {
	Image       string    // name@sha256:<64 hex>; anything else is refused
	Env         []string  // the exact environment of Argv, as NAME=value; nothing is inherited
	Argv        []string  // started with exec after the stdin tar is extracted under /work
	Workdir     string    // a clean relative directory under /work, "." for /work itself
	Limits      Limits    // every limit is required
	Stdin       io.Reader // an uncompressed tar that ends with CompleteMarker, extracted under /work; an error from it fails the run
	StdoutLimit int64     // the most of the container's standard output that is kept
}

// Outcome is what a run reports. ExitCode is the container's, which is that of Argv, because it runs through exec.
type Outcome struct {
	ExitCode  int
	OOMKilled bool // the runtime's .State.OOMKilled: the memory limit killed a process of the container
	Killed    bool // the executor's own kill, KillAfter after the start
	Truncated bool // the output passed StdoutLimit
	Stdout    []byte
}

// Runtime runs sandboxes.
type Runtime interface {
	// Info names the runtime and its version, for the evidence of a result.
	Info(ctx context.Context) (name, version string, err error)
	// Run runs one container to its end and removes it. It returns ErrUnavailable when the runtime cannot honor a property, and
	// ErrSetupFailed when the container failed before Argv started. When s.Stdin fails, the container is killed at once and Run returns
	// that error. A container that the runtime cannot remove is reported with the Outcome and an error that wraps ErrUnavailable.
	Run(ctx context.Context, s Spec) (Outcome, error)
}

const maxImageBytes = 300 // the longest image reference the runtime contract allows

var (
	imagePin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	envName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(format, args...))
}

// validate refuses a Spec that would run a weaker sandbox, or that would let the runtime read an argument as a flag or inherit a
// variable. It runs before any command.
func (s Spec) validate() error {
	switch {
	case len(s.Image) > maxImageBytes || !imagePin.MatchString(s.Image):
		return invalid("the image %q is not name@sha256:<64 lowercase hex digits>", s.Image)
	case s.Workdir != "." && !artifact.ValidPath(s.Workdir):
		return invalid("the work directory %q is not a clean relative path", s.Workdir)
	case len(s.Argv) == 0 || s.Argv[0] == "" || s.Argv[0][0] == '-' || strings.Contains(s.Argv[0], "="):
		return invalid("the program to run is missing, or it reads as a flag or as an assignment of env")
	case slices.ContainsFunc(s.Argv, func(arg string) bool { return strings.ContainsRune(arg, 0) }):
		return invalid("an argument holds a NUL")
	case s.Stdin == nil:
		return invalid("no standard input: the tree arrives as a tar on it")
	case s.StdoutLimit <= 0:
		return invalid("the output limit %d must be positive", s.StdoutLimit)
	}
	for _, entry := range s.Env {
		if name, value, ok := strings.Cut(entry, "="); !ok || !envName.MatchString(name) || strings.ContainsRune(value, 0) {
			return invalid("the environment entry %q is not NAME=value", name)
		}
	}
	return s.Limits.validate()
}

func (l Limits) validate() error {
	if l.MemoryMiB <= 0 || l.CPUs <= 0 || l.Pids <= 0 || l.TmpfsMiB <= 0 || l.KillAfter < 0 {
		return invalid("every limit is required and positive, and the kill deadline is not negative: %+v", l)
	}
	return nil
}

// CompleteMarker is the file, relative to /work, that must be the last entry of the standard input tar: the container refuses to
// start Argv when tar has not extracted it, so a stream that was cut short is never run, wherever the cut falls.
const CompleteMarker = ".suiteward/complete"

// CLIEnv is the environment of the runtime's CLI, which the executor starts: the executor's own, without a SUITEWARD_ variable. The
// executor token is in it when it is not given as a file, and a CLI has no use for it. The other variables stay: the engine's address
// and its config, and the proxy variables, which a rootless engine may need to pull an image (the no-proxy rule is for the executor's own
// HTTP clients).
func CLIEnv(environ []string) []string {
	return environ
}
