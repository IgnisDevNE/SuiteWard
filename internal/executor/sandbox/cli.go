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
	namePrefix = "suiteward-sandbox-" // every sandbox container, so that a leftover can be found and swept
	user       = "65534:65534"        // nobody: both images and both runtimes already know it (spike S0)
	// script extracts the tar that arrives on the standard input, goes to the work directory ($1) and replaces the shell with the
	// program, so that the container's exit status is the program's. The directory and the program are arguments, never script text.
	script = `tar -x -C /work && cd "/work/$1" && shift && exec "$@"`

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
	name := "podman"
	if slices.ContainsFunc(engine.Components, func(component struct{ Name string }) bool { return component.Name == "Engine" }) {
		name = "docker"
	}
	return name, engine.Version, nil
}

// runArgs is the whole of `run`: the properties of ADR 0028 section 11 and nothing else, the same for both runtimes. There is no
// --rm, because the container must exist to be inspected for an out-of-memory kill; the runner removes it.
func runArgs(name string, s Spec) []string {
	l := s.Limits
	memory := strconv.Itoa(l.MemoryMiB) + "m"
	args := []string{
		"run", "--name", name, "-i", "--pull=never", "--network=none", "--read-only",
		"--tmpfs", "/work:rw,exec,nosuid,nodev,size=" + strconv.Itoa(l.TmpfsMiB) + "m",
		"--cap-drop=ALL", "--security-opt", "no-new-privileges", "--user", user,
		"--pids-limit", strconv.Itoa(l.Pids), "--memory", memory, "--memory-swap", memory, "--cpus", strconv.Itoa(l.CPUs),
	}
	for _, entry := range s.Env {
		args = append(args, "--env", entry)
	}
	args = append(args, s.Image, "sh", "-c", script, "sh", s.Workdir)
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
	name := namePrefix + rand.Text()
	stdin, stdout := &failClosed{r: s.Stdin}, &capped{limit: s.StdoutLimit}
	killed, stderr, runErr := c.execute(ctx, name, s, stdin, stdout)
	// The state and the removal do not depend on the caller's context: a cancelled run must still remove its container.
	cleanup := context.WithoutCancel(ctx)
	state, stateErr := c.inspect(cleanup, name)
	_, rmErr := c.output(cleanup, "rm", "--force", name)
	err := verdict(ctx, stdin.failure(), runErr, stderr, state, stateErr)
	if rmErr != nil && stateErr == nil { // with no container to inspect, there was most likely none to remove
		err = errors.Join(err, fmt.Errorf("remove the container %s: %w", name, rmErr))
	}
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{ExitCode: state.ExitCode, OOMKilled: state.OOMKilled, Killed: killed, Truncated: stdout.truncated(), Stdout: stdout.Bytes()}, nil
}

// verdict decides whether a run happened: it names the first reason it did not, and is nil when the container started and ended.
// Only the state of the container decides it. The text on the container's standard error is the candidate's, and never counts.
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
	}
	return nil
}

// execute runs the container and watches the clock. It returns whether the executor killed the container.
func (c *cli) execute(ctx context.Context, name string, s Spec, stdin io.Reader, stdout io.Writer) (killed bool, stderr string, err error) {
	done := make(chan struct{})
	watched := make(chan bool, 1)
	go func() { watched <- c.killAfter(ctx, name, s.Limits.KillAfter, done) }()
	_, stderr, err = c.run(ctx, c.binary, runArgs(name, s), stdin, stdout)
	close(done)
	return <-watched, stderr, err
}

// killAfter kills the container by name once the time is up, because killing the `run` client leaves the container running
// (spike S0). The container may not exist yet, so a kill that fails is tried again until the run ends. It reports whether a kill landed.
func (c *cli) killAfter(ctx context.Context, name string, after time.Duration, done <-chan struct{}) bool {
	timer := time.NewTimer(after)
	defer timer.Stop()
	select {
	case <-done:
		return false
	case <-timer.C:
	}
	for {
		if _, err := c.output(context.WithoutCancel(ctx), "kill", name); err == nil {
			return true
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

// poison is a block that no tar reader takes for a header: its magic is wrong and so is its checksum.
var poison = bytes.Repeat([]byte{0xff}, 512)

// failClosed passes the tar of the caller through and fails the run when the caller's stream breaks. The container reads the tar
// with busybox tar, which takes a stream that stops at a file boundary for a finished tar, so a closed pipe would let the program
// run on a part of the tree. After an error, the container is handed the poison block and then the end, so that tar fails and
// `exec` is never reached, and the error is kept for the caller.
type failClosed struct {
	r    io.Reader
	mu   sync.Mutex
	err  error
	left []byte // the poison not yet handed out
}

func (f *failClosed) Read(p []byte) (int, error) {
	f.mu.Lock()
	if f.err != nil {
		n := copy(p, f.left)
		f.left = f.left[n:]
		f.mu.Unlock()
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
	f.mu.Unlock()
	n, err := f.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		f.mu.Lock()
		f.err, f.left = err, poison
		f.mu.Unlock()
		return n, nil
	}
	return n, err
}

func (f *failClosed) failure() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}
