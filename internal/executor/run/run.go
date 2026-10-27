package run

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// Pacing is how long the executor waits: after a lease request that found no job, between the lease requests that follow an error (the
// wait doubles from Backoff up to MaxBackoff and starts again after an answer), and between heartbeats.
type Pacing struct {
	Idle       time.Duration
	Backoff    time.Duration
	MaxBackoff time.Duration
	Heartbeat  time.Duration
}

// DefaultPacing is the contract's: 5 s after a 204, a backoff of 5 s up to 60 s after an error, a heartbeat about every 20 s.
var DefaultPacing = Pacing{Idle: 5 * time.Second, Backoff: 5 * time.Second, MaxBackoff: 60 * time.Second, Heartbeat: 20 * time.Second}

// Proxy is the module proxy that the dependency prefetch reads, and the client that reads it.
type Proxy struct {
	Base   string // https://proxy.golang.org
	Client *http.Client
}

// Config is what an Executor needs. The fields that are optional have the defaults of the field's comment.
type Config struct {
	API     *client.Client
	Runtime sandbox.Runtime
	// Sweep removes the sandbox containers that an earlier executor left behind; it runs once, before the first lease.
	Sweep func(context.Context) error
	// Proxy defaults to proxy.golang.org with a client that reads no proxy setting.
	Proxy Proxy
	// Version and BinarySHA256 name this executor in every result: the build version and the SHA-256 of its binary, 64 lowercase hex digits.
	Version, BinarySHA256 string
	Log                   *slog.Logger
	// Pacing defaults to DefaultPacing. Sleep waits for a duration or the end of the context (default: a timer); Tick makes the heartbeat's
	// ticker (default: time.NewTicker); Now is the clock (default: time.Now). They are the seams of the tests.
	Pacing Pacing
	Sleep  func(context.Context, time.Duration) error
	Tick   func(time.Duration) (<-chan time.Time, func())
	Now    func() time.Time
}

// Executor leases jobs and runs them.
type Executor struct{ cfg Config }

// New checks the dependencies of an executor.
func New(cfg Config) (*Executor, error) {
	return &Executor{cfg: cfg}, nil
}

// Run sweeps the leftover containers, checks the runtime and leases jobs until ctx ends. A clean stop is nil; an error is fatal.
func (e *Executor) Run(ctx context.Context) error {
	return errors.New("not implemented")
}

// Sweeper returns the Sweep function that removes, with the runtime CLI binary, every container whose name starts with the sandbox's.
func Sweeper(binary string) func(context.Context) error {
	return func(context.Context) error { return errors.New("not implemented") }
}

// commandRunner runs one command of the container runtime's CLI and returns its standard output.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// sweep removes the leftover sandbox containers, and returns their names.
func sweep(ctx context.Context, run commandRunner, binary string) ([]string, error) {
	return nil, errors.New("not implemented")
}
