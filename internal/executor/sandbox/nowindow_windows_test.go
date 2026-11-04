//go:build windows

package sandbox

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// A console program that a process without a console starts gets a console of its own, with a window, unless it is started with
// CREATE_NO_WINDOW. The executor has none when its task was stopped, and it still runs the container CLI then to stop the sandbox of a job in
// flight. Every command of the CLI, the run and the ones that kill, inspect and remove its container, goes through osExec.
func TestTheRuntimeCLIIsStartedWithoutAWindow(t *testing.T) {
	var started *exec.Cmd
	previous := commandContext
	commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		started = startHelper(ctx, os.Args[0], "-test.run=^TestHelperPrintEnv$", "--", "print-env")
		return started
	}
	t.Cleanup(func() { commandContext = previous })
	if exit, stderr, err := osExec(context.Background(), "docker", []string{"version"}, nil, io.Discard); err != nil || exit != 0 {
		t.Fatalf("osExec = %d, %q, %v", exit, stderr, err)
	}
	if started.SysProcAttr == nil || started.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("the CLI command does not carry CREATE_NO_WINDOW (its SysProcAttr is %+v)", started.SysProcAttr)
	}
}
