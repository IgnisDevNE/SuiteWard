package installer

import (
	"context"
	"errors"
	"io"
	"time"
)

// Env is what the install command needs from the machine; tests replace it.
type Env struct {
	Deps     Deps
	Term     Terminal
	PortFree func(port int) error // nil when the port can be listened on
	Sleep    func(time.Duration)
}

// OSEnv is this machine and this process's console, printing to stdout.
func OSEnv(stdout io.Writer) Env {
	return Env{}
}

// Run is `suiteward install`: it returns the process exit code and prints a problem to stderr.
func Run(ctx context.Context, args []string, env Env, stderr io.Writer) int {
	return 1
}

// Install runs the install command (args without the command name).
func Install(ctx context.Context, args []string, env Env) error {
	return errors.New("not implemented")
}

// ErrUsage marks a command line that cannot be parsed.
var ErrUsage = errors.New("usage")

const appRole = "suiteward_app"
