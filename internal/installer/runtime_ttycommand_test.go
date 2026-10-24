package installer

import (
	"context"
	"slices"
	"testing"
)

// The command a caller runs in a terminal of its own is the command ExecTTY runs, engine flags included.
func TestExecTTYCommandIsWhatExecTTYRuns(t *testing.T) {
	argv := []string{"/suiteward", "setup", "--owner", "alice"}
	for name, build := range map[string]func(*fakeEngine) Runtime{
		"podman with a connection": func(f *fakeEngine) Runtime { return newRootlessPodman(t, f, "pm") },
		"docker on Windows": func(f *fakeEngine) Runtime {
			return newDocker(t, f, dockerDesktopServer, `["name=seccomp,profile=builtin"]`, DockerOptions{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake("windows")
			rt := build(f)
			if err := rt.ExecTTY(context.Background(), ContainerServe, argv); err != nil {
				t.Fatal(err)
			}
			cmdName, cmdArgs := rt.ExecTTYCommand(ContainerServe, argv)
			if len(f.tty) != 1 || f.tty[0].name != cmdName || !slices.Equal(f.tty[0].args, cmdArgs) {
				t.Fatalf("ExecTTY ran %+v, ExecTTYCommand says %s %v", f.tty, cmdName, cmdArgs)
			}
		})
	}
}
