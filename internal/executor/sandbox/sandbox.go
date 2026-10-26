// Package sandbox runs one disposable container through the CLI of a container runtime, with the isolation that ADR 0028
// section 11 requires, and reports what only the runtime can say: the exit status, an out-of-memory kill and the executor's own
// kill. It never runs a weaker sandbox: a Spec that asks for less, or a runtime that cannot honor a property, is an error.
package sandbox

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrUnavailable reports a runtime that cannot build or run the sandbox, or cannot honor a required property (a cgroup
	// controller that is missing, an image it does not hold). The run is unavailable with the reason sandbox-unavailable.
	ErrUnavailable = errors.New("the sandbox runtime is unavailable")
	// ErrInvalidSpec reports a Spec the runner refuses before it runs any command.
	ErrInvalidSpec = errors.New("invalid sandbox spec")
)

// Limits are the resource limits of one sandbox. Every one is required: the runtimes read zero as no limit.
type Limits struct {
	MemoryMiB int // the memory limit; the swap limit is the same, and the tmpfs counts in it
	CPUs      int
	Pids      int
	TmpfsMiB  int           // the size of /work
	KillAfter time.Duration // the executor kills the container this long after it starts
}

// Spec describes one run.
type Spec struct {
	Image       string    // name@sha256:<64 hex>; anything else is refused
	Env         []string  // the exact environment of Argv, as NAME=value; nothing is inherited
	Argv        []string  // started with exec after the stdin tar is extracted under /work
	Workdir     string    // a clean relative directory under /work, "." for /work itself
	Limits      Limits    //
	Stdin       io.Reader // an uncompressed tar, extracted under /work; an error from it fails the run closed
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
	// Run runs one container to its end and removes it. It returns ErrUnavailable when the runtime cannot honor a property.
	Run(ctx context.Context, s Spec) (Outcome, error)
}

var errNotImplemented = errors.New("not implemented")

// execFunc runs one command of the runtime's CLI: the program, its arguments, its standard input (nil for none) and the writer of
// its standard output. It returns the exit status and the program's standard error. A non-zero exit status is not an error: the
// error is for a program that could not run, or that was stopped by the context.
type execFunc func(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer) (exit int, stderr string, err error)

type cli struct {
	binary string
	run    execFunc
}

func newCLI(binary string, run execFunc) *cli { return &cli{binary: binary, run: run} }

// NewCLI drives the container runtime whose command is binary, "docker" or "podman".
func NewCLI(binary string) Runtime { return newCLI(binary, nil) }

func (c *cli) Info(context.Context) (string, string, error) { return "", "", errNotImplemented }

func (c *cli) Run(context.Context, Spec) (Outcome, error) { return Outcome{}, errNotImplemented }
