package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

const (
	// sandboxPrefix starts the name of every sandbox container (the sandbox package's prefix, which it does not export): a container that
	// an executor was killed in the middle of leaves one behind.
	sandboxPrefix = "suiteward-sandbox-"
	// removeGroup is how many containers one `rm` names, so that a command line stays short whatever was left behind.
	removeGroup  = 50
	sweepTimeout = 30 * time.Second
)

// sandboxName is what a name that the sweep removes looks like: the prefix and the random text the sandbox appends. Nothing else is removed,
// and nothing that could read as a flag.
var sandboxName = regexp.MustCompile(`^` + sandboxPrefix + `[A-Za-z0-9]+$`)

// commandContext is exec.CommandContext. The sweep runs the container CLI on purpose, with argument lists it builds itself from constants
// and the names that sandboxName accepted, so gosec's G204 (a subprocess launched with a variable) does not apply.
var commandContext = exec.CommandContext

// commandRunner runs one command of the container runtime's CLI and returns its standard output.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func osRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	cmd := commandContext(ctx, name, args...)
	cmd.Env = sandbox.CLIEnv(os.Environ())
	out, err := cmd.Output()
	var failed *exec.ExitError
	if errors.As(err, &failed) {
		return nil, fmt.Errorf("%s %s: %w: %.200s", name, args[0], err, failed.Stderr)
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return out, nil
}

// Sweeper returns the Sweep function that removes, with the runtime CLI binary, every container whose name starts with the sandbox's. An
// executor is the only one on its machine's engine, so none of them is another executor's.
func Sweeper(binary string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := sweep(ctx, osRun, binary)
		return err
	}
}

// sweep removes the leftover sandbox containers, and returns the names it removed.
func sweep(ctx context.Context, run commandRunner, binary string) ([]string, error) {
	out, err := run(ctx, binary, "ps", "--all", "--filter", "name=^"+sandboxPrefix, "--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("list the leftover containers: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); sandboxName.MatchString(name) {
			names = append(names, name)
		}
	}
	var removed []string
	for len(names) > 0 {
		group := names[:min(removeGroup, len(names))]
		if _, err := run(ctx, binary, append([]string{"rm", "--force"}, group...)...); err != nil {
			return removed, fmt.Errorf("remove %d leftover containers: %w", len(group), err)
		}
		removed = append(removed, group...)
		names = names[len(group):]
	}
	return removed, nil
}
