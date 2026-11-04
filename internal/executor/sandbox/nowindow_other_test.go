//go:build !windows

package sandbox

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
)

// Only Windows has a console window to hide: the CLI command of every other system is started as exec made it.
func TestTheRuntimeCLICommandKeepsItsProcessAttributesOffWindows(t *testing.T) {
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
	if started.SysProcAttr != nil {
		t.Errorf("the CLI command has process attributes off Windows: %+v", started.SysProcAttr)
	}
}
