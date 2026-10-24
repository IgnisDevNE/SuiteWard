package installer

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"
)

// windowStarter opens a terminal window of its own that runs the command (setup, for the owner) and returns a function that waits for
// the command to end and reports how it ended. A start error means that no window was opened. The wait gives up when ctx is done and
// returns its error, and takes the window with it where the system lets it.
type windowStarter func(ctx context.Context, name string, args []string) (wait func() error, err error)

// machine is what runSetup asks of the machine it runs on to choose where setup runs; tests replace setupMachine.
type machine struct {
	goos   string
	getenv func(string) string
	window windowStarter // nil where this system has no window to open
}

var setupMachine = machine{goos: runtime.GOOS, getenv: os.Getenv, window: platformSetupWindow()}

func platformSetupWindow() windowStarter { return nil }

// setupWindow is the window setup runs in, or nil when it keeps the terminal.
func (m machine) setupWindow(fullScreen bool) windowStarter { return nil }

// setupPoll and setupWaitLimit bound the wait for a window that the installer cannot wait on (Terminal): it polls for the exit code the
// script leaves, and gives up after the limit. Variables so that a test does not wait for them.
var (
	setupPoll      = 500 * time.Millisecond
	setupWaitLimit = 15 * time.Minute
)

// terminalWindow is the window starter of a system that opens a script in a terminal and cannot wait for it: open runs the script.
func terminalWindow(open func(ctx context.Context, script string) error) windowStarter {
	return func(context.Context, string, []string) (func() error, error) {
		return nil, errors.New("not implemented")
	}
}

// terminalScript is the script a terminal runs for setup.
func terminalScript(bin string, args, environ []string, exitDir string) string { return "" }
