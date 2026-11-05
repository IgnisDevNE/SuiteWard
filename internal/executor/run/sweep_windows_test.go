//go:build windows

package run

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// The commands of the sweep are the container CLI too, and are started without a window like the ones of the sandbox package
// (TestTheRuntimeCLIIsStartedWithoutAWindow says why).
func TestTheSweepStartsTheRuntimeCLIWithoutAWindow(t *testing.T) {
	var started *exec.Cmd
	previous := commandContext
	commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		started = startHelper(ctx, os.Args[0], "-test.run=^TestHelperPrintEnv$", "--", "print-env")
		return started
	}
	t.Cleanup(func() { commandContext = previous })
	if _, err := osRun(context.Background(), "docker", "ps"); err != nil {
		t.Fatalf("osRun: %v", err)
	}
	if started.SysProcAttr == nil || started.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("the CLI command does not carry CREATE_NO_WINDOW (its SysProcAttr is %+v)", started.SysProcAttr)
	}
}
