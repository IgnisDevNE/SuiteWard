package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/prefetch"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// Pacing is how long the executor waits: after a lease request that found no job, between the lease requests that follow an error (the
// wait doubles from Backoff up to MaxBackoff and starts again after an answer), and between heartbeats.
type Pacing struct {
	Idle       time.Duration
	Backoff    time.Duration
	MaxBackoff time.Duration
	Heartbeat  time.Duration
	Grace      time.Duration // how long a job in flight has after the signal that stops the loop
}

// DefaultPacing is the contract's: 5 s after a 204, a backoff of 5 s up to 60 s after an error, a heartbeat about every 20 s, and 30 s for a job
// in flight after the signal.
var DefaultPacing = Pacing{Idle: 5 * time.Second, Backoff: 5 * time.Second, MaxBackoff: 60 * time.Second, Heartbeat: 20 * time.Second, Grace: 30 * time.Second}

// Proxy is the module proxy that the dependency prefetch reads, and the client that reads it.
type Proxy struct {
	Base   string // https://proxy.golang.org
	Client *http.Client
}

const (
	proxyBase = "https://proxy.golang.org"
	// proxyTimeout bounds one request of the prefetch, and the request covers streaming its body into the sandbox's tar: a module that the
	// container reads as fast as it extracts it takes seconds, so ten minutes (the longest a download of the service may last) is a stall.
	proxyTimeout = 10 * time.Minute
)

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

var (
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	binaryPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Executor leases jobs and runs them.
type Executor struct {
	cfg Config

	runtime string // the name and version of the container runtime, as every result says

	mu        sync.Mutex
	toolchain string // what the image says its Go is, asked once
}

// New checks the dependencies of an executor, which are a trust boundary of the build, and fills the defaults.
func New(cfg Config) (*Executor, error) {
	switch {
	case cfg.API == nil:
		return nil, errors.New("run: the API client is required")
	case cfg.Runtime == nil:
		return nil, errors.New("run: the container runtime is required")
	case cfg.Sweep == nil:
		return nil, errors.New("run: the sweep of the leftover containers is required")
	case cfg.Log == nil:
		return nil, errors.New("run: a logger is required")
	case !versionPattern.MatchString(cfg.Version):
		return nil, errors.New("run: the version is not 1 to 64 characters of letters, digits and ._+-")
	case !binaryPattern.MatchString(cfg.BinarySHA256):
		return nil, errors.New("run: the binary digest is not 64 lowercase hexadecimal digits")
	}
	if cfg.Pacing == (Pacing{}) {
		cfg.Pacing = DefaultPacing
	}
	if cfg.Proxy.Base == "" {
		cfg.Proxy.Base = proxyBase
	}
	if cfg.Proxy.Client == nil {
		cfg.Proxy.Client = prefetch.Client(proxyTimeout)
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepFor
	}
	if cfg.Tick == nil {
		cfg.Tick = tickEvery
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Executor{cfg: cfg}, nil
}

func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func tickEvery(d time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(d)
	return ticker.C, ticker.Stop
}

// Run checks the runtime, sweeps the leftover containers and leases jobs until ctx ends. A clean stop is nil; an error is fatal. The
// signal that ends ctx stops the leasing at once, and the job in flight is let to finish (the supervisor's stop timeout decides how long: a
// job that it kills leaves a lease that expires, and the job is leased again).
func (e *Executor) Run(ctx context.Context) error {
	name, version, err := e.cfg.Runtime.Info(ctx)
	if err != nil {
		return fmt.Errorf("no container runtime: %w", err)
	}
	e.runtime = name + " " + version
	if !printable(e.runtime) {
		return fmt.Errorf("no container runtime: %q is not 1 to 64 printable ASCII bytes", e.runtime)
	}
	if err := e.cfg.Sweep(ctx); err != nil {
		e.cfg.Log.Warn("the leftover containers were not swept", "error", err)
	}
	e.cfg.Log.Info("the executor runs", "runtime", e.runtime, "version", e.cfg.Version)
	wait := e.cfg.Pacing.Backoff
	for ctx.Err() == nil {
		lease, err := e.cfg.API.Lease(ctx)
		switch {
		case err == nil:
			wait = e.cfg.Pacing.Backoff
			e.handle(ctx, lease)
		case errors.Is(err, client.ErrNoJob):
			wait = e.cfg.Pacing.Backoff
			_ = e.cfg.Sleep(ctx, e.cfg.Pacing.Idle)
		case ctx.Err() != nil:
		default:
			e.cfg.Log.Warn("no lease", "error", err, "retry_in", wait)
			_ = e.cfg.Sleep(ctx, wait)
			wait = min(2*wait, e.cfg.Pacing.MaxBackoff)
		}
	}
	return nil
}

// printable is true for 1 to 64 printable ASCII bytes.
func printable(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}
