package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	enginePodman = "podman"             // what Info says of Podman, which runs the sandbox with one more flag
	namePrefix   = "suiteward-sandbox-" // every sandbox container, so that a leftover can be found and swept
	user         = "65534:65534"        // nobody: both images and both runtimes already know it (spike S0)
	// script extracts the tar that arrives on the standard input, refuses to go on without the completion marker that ends it, goes to
	// the work directory ($1) and replaces the shell with env, which clears the environment and starts the program with the assignments
	// that follow the work directory, so that the container's exit status is the program's. Every step before the exec exits 113 when it
	// fails: neither the runtimes (125) nor the shell (126, 127) use it, so a setup failure is told from the status of Argv. The
	// directory, the assignments and the program are arguments, never script text.
	script            = `{ tar -x -C /work && test -f /work/` + CompleteMarker + ` && cd "/work/$1"; } || exit 113; shift; exec env -i "$@"`
	setupFailedStatus = 113

	commandTimeout = 30 * time.Second       // the bound of each command that is not the run
	killRetry      = 100 * time.Millisecond // a kill that finds no container yet is tried again after this
	waitDelay      = 5 * time.Second        // how long a finished CLI waits for a standard input that does not end
	stderrBytes    = 2048                   // the part of a command's standard error kept for an error message
)

// commandContext is exec.CommandContext. The runner runs the container CLI on purpose, with argument lists it builds itself from
// constants and a validated Spec, so gosec's G204 (a subprocess launched with a variable) does not apply.
var commandContext = exec.CommandContext

// NewCLI drives the container runtime whose command is binary, "docker" or "podman".
func NewCLI(binary string) Runtime { return newCLI(binary, osExec) }

// cli runs a runtime through its command. run is the seam: tests replace it with a fake.
type cli struct {
	binary string
	run    execFunc

	mu     sync.Mutex
	engine string // what Info reported the first time, for the flags that differ between Docker and Podman
}

func newCLI(binary string, run execFunc) *cli { return &cli{binary: binary, run: run} }

// execFunc runs one command of the runtime's CLI: the program, its arguments, its standard input (nil for none) and the writer of
// its standard output. It returns the exit status and the program's standard error. A non-zero exit status is not an error: the
// error is for a program that could not run, or that was stopped by the context.
type execFunc func(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer) (exit int, stderr string, err error)

func osExec(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer) (int, string, error) {
	cmd := commandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	stderr := &capped{limit: stderrBytes}
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return -1, stderr.String(), ctx.Err()
	case errors.As(err, &exit):
		return exit.ExitCode(), stderr.String(), nil
	case errors.Is(err, exec.ErrWaitDelay):
		return 0, stderr.String(), nil // the program ended well; only a standard input that did not end held the command up
	case err != nil:
		return -1, stderr.String(), err
	}
	return 0, stderr.String(), nil
}

// output runs a command that is not the run and returns its standard output. A non-zero exit status is an error.
func (c *cli) output(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var out bytes.Buffer
	exit, stderr, err := c.run(ctx, c.binary, args, nil, &out)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s %s: %w", c.binary, args[0], err)
	case exit != 0:
		return nil, fmt.Errorf("%s %s exited with the status %d: %q", c.binary, args[0], exit, stderr)
	}
	return out.Bytes(), nil
}

var versionText = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+~-]{0,47}$`)

type engineInfo struct {
	Version    string
	Components []struct{ Name string }
}

// Info names the engine behind the command, and not the command: a `docker` that is Podman's shim says podman. Only Docker names
// a component "Engine"; a local Podman reports its client alone, and a Podman machine reports its engine as the server.
func (c *cli) Info(ctx context.Context) (string, string, error) {
	out, err := c.output(ctx, "version", "--format", "{{json .}}")
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var v struct{ Client, Server *engineInfo }
	if err := json.Unmarshal(out, &v); err != nil {
		return "", "", fmt.Errorf("%w: read the version of %s: %w", ErrUnavailable, c.binary, err)
	}
	engine := v.Server
	if engine == nil {
		engine = v.Client
	}
	if engine == nil || !versionText.MatchString(engine.Version) {
		return "", "", fmt.Errorf("%w: %s reports no usable version", ErrUnavailable, c.binary)
	}
	name := enginePodman
	if slices.ContainsFunc(engine.Components, func(component struct{ Name string }) bool { return component.Name == "Engine" }) {
		name = "docker"
	}
	return name, engine.Version, nil
}

// engineName is the engine behind the command, asked once: the flags of `run` differ between Docker and Podman.
func (c *cli) engineName(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.engine == "" {
		name, _, err := c.Info(ctx)
		if err != nil {
			return "", err
		}
		c.engine = name
	}
	return c.engine, nil
}

// runArgs is the whole of `run`: the properties of ADR 0028 section 11 and nothing else, the same for both engines but one flag.
// There is no --rm, because the container must exist to be inspected for an out-of-memory kill; the runner removes it. There is no
// --env either, because the image has an environment of its own: the script starts Argv through env -i with the assignments that
// follow the work directory, so that Argv sees exactly Spec.Env.
func runArgs(engine, name string, s Spec) []string {
	l := s.Limits
	memory := strconv.Itoa(l.MemoryMiB) + "m"
	args := []string{"run", "--name", name, "-i", "--pull=never", "--network=none", "--read-only"}
	if engine == enginePodman {
		// Under --read-only Podman still mounts a writable tmpfs on /tmp, /var/tmp, /run and /dev/shm unless it is told not to.
		args = append(args, "--read-only-tmpfs=false")
	}
	args = append(args,
		"--tmpfs", "/work:rw,exec,nosuid,nodev,size="+strconv.Itoa(l.TmpfsMiB)+"m",
		"--cap-drop=ALL", "--security-opt", "no-new-privileges", "--user", user,
		"--pids-limit", strconv.Itoa(l.Pids), "--memory", memory, "--memory-swap", memory, "--cpus", strconv.Itoa(l.CPUs),
		s.Image, "sh", "-c", script, "sh", s.Workdir)
	args = append(args, s.Env...)
	return append(args, s.Argv...)
}

// containerState is the part of `inspect` that the runner reads.
type containerState struct {
	ExitCode  int
	OOMKilled bool
	Error     string
	StartedAt time.Time
}

func (c *cli) inspect(ctx context.Context, name string) (containerState, error) {
	out, err := c.output(ctx, "inspect", "--type", "container", "--format", "{{json .State}}", name)
	if err != nil {
		return containerState{}, err
	}
	var state containerState
	if err := json.Unmarshal(out, &state); err != nil {
		return containerState{}, fmt.Errorf("read the state of %s: %w", name, err)
	}
	return state, nil
}

func (c *cli) Run(ctx context.Context, s Spec) (Outcome, error) {
	if err := s.validate(); err != nil {
		return Outcome{}, err
	}
	engine, err := c.engineName(ctx)
	if err != nil {
		return Outcome{}, err
	}
	name := namePrefix + rand.Text()
	stdin, stdout := newGuardedStdin(s.Stdin), &capped{limit: s.StdoutLimit}
	killed, stderr, runErr := c.execute(ctx, name, runArgs(engine, name, s), s.Limits.KillAfter, stdin, stdout)
	// The state and the removal do not depend on the caller's context: a cancelled run must still remove its container.
	cleanup := context.WithoutCancel(ctx)
	state, stateErr := c.inspect(cleanup, name)
	_, rmErr := c.output(cleanup, "rm", "--force", name)
	err = verdict(ctx, stdin.failure(), runErr, stderr, state, stateErr)
	var left error // a container the runtime would not remove is its failure; with none to inspect, there was most likely none to remove
	if rmErr != nil && stateErr == nil {
		left = fmt.Errorf("%w: remove the container %s: %w", ErrUnavailable, name, rmErr)
	}
	if err != nil {
		return Outcome{}, errors.Join(err, left)
	}
	return Outcome{ExitCode: state.ExitCode, OOMKilled: state.OOMKilled, Killed: killed, Truncated: stdout.truncated(), Stdout: stdout.Bytes()}, left
}

// verdict decides whether a run happened: it names the first reason it did not, and is nil when the container started Argv and it
// ended. Only the state of the container decides it. The text on the container's standard error is the candidate's, and never counts.
func verdict(ctx context.Context, stdinErr, runErr error, stderr string, state containerState, stateErr error) error {
	switch {
	case stdinErr != nil:
		return fmt.Errorf("read the standard input of the sandbox: %w", stdinErr)
	case ctx.Err() != nil:
		return ctx.Err()
	case runErr != nil:
		return fmt.Errorf("%w: run the container: %w", ErrUnavailable, runErr)
	case stateErr != nil:
		return fmt.Errorf("%w: the runtime made no container: %w (it said %q)", ErrUnavailable, stateErr, stderr)
	case state.Error != "" || state.StartedAt.IsZero():
		return fmt.Errorf("%w: the runtime did not start the container: %q (it said %q)", ErrUnavailable, state.Error, stderr)
	case state.ExitCode == setupFailedStatus:
		return fmt.Errorf("%w: tar, the completion marker or the work directory failed (status %d)", ErrSetupFailed, setupFailedStatus)
	}
	return nil
}

// execute runs the container and watches it. The container is killed by name when the clock runs out, and as soon as the standard
// input breaks; it returns whether the clock killed it.
func (c *cli) execute(ctx context.Context, name string, args []string, after time.Duration, stdin *guardedStdin, stdout io.Writer) (killed bool, stderr string, err error) {
	done := make(chan struct{})
	watched := make(chan bool, 1)
	go func() { watched <- c.killWhen(ctx, name, after, stdin.broken, done) }()
	_, stderr, err = c.run(ctx, c.binary, args, stdin, stdout)
	close(done)
	return <-watched, stderr, err
}

// killWhen kills the container by name when the time is up or the broken channel closes, because killing the `run` client leaves
// the container running (spike S0). The container may not exist yet, so a kill that fails is tried again until the run ends. It reports
// whether the clock was the cause of a kill that landed.
func (c *cli) killWhen(ctx context.Context, name string, after time.Duration, broken, done <-chan struct{}) bool {
	timer := time.NewTimer(after)
	defer timer.Stop()
	clock := false
	select {
	case <-done:
		return false
	case <-timer.C:
		clock = true
	case <-broken:
	}
	for {
		if _, err := c.output(context.WithoutCancel(ctx), "kill", name); err == nil {
			return clock
		}
		select {
		case <-done:
			return false
		case <-time.After(killRetry):
		}
	}
}

// capped keeps the first limit bytes written to it and takes the rest without error, so that the process that writes never blocks.
type capped struct {
	mu    sync.Mutex
	limit int64
	buf   bytes.Buffer
	cut   bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := c.limit - int64(c.buf.Len()); int64(n) > room {
		p, c.cut = p[:room], true
	}
	c.buf.Write(p)
	return n, nil
}

func (c *capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.buf.Bytes())
}

func (c *capped) String() string { return strings.ToValidUTF8(string(c.Bytes()), "?") }

func (c *capped) truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cut
}

// guardedStdin passes the tar of the caller through and notes when the caller's stream breaks (an error that is not its end). A
// broken stream kills the container at once, and the run fails with the error of the stream. The tar may stop anywhere, even at a
// boundary that busybox tar takes for the end of an archive, and then the container's own refusal to start Argv without the completion
// marker is what keeps a part of the tree from running.
type guardedStdin struct {
	r      io.Reader
	broken chan struct{} // closed at the first error of r
	mu     sync.Mutex
	err    error
}

func newGuardedStdin(r io.Reader) *guardedStdin {
	return &guardedStdin{r: r, broken: make(chan struct{})}
}

func (g *guardedStdin) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		g.mu.Lock()
		if g.err == nil {
			g.err = err
			close(g.broken)
		}
		g.mu.Unlock()
	}
	return n, err
}

func (g *guardedStdin) failure() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}
