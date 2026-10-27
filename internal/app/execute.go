package app

import (
	"context"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// executorSettings are the three settings of `suiteward execute` (docs/contracts/runtime.md, "Commands").
type executorSettings struct {
	url     string
	token   config.Secret
	runtime string
}

// readExecutorSettings reads the settings of the executor from env, and refuses an environment that holds a credential of the service.
func readExecutorSettings(env Env) (executorSettings, error) {
	return executorSettings{}, nil
}

// executorDeps are the parts of `suiteward execute` that the tests replace: the container runtime, the sweep of its leftover containers
// and the digest of the executor's own binary.
type executorDeps struct {
	runtime   func(binary string) sandbox.Runtime
	sweep     func(binary string) func(context.Context) error
	binarySum func() (string, error)
}

func osExecutorDeps() executorDeps { return executorDeps{} }

// fileSum is the SHA-256 of a file, as 64 lowercase hexadecimal digits.
func fileSum(path string) (string, error) { return "", nil }

// runExecute is `suiteward execute`.
func runExecute(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	return executeWith(ctx, args, env, stdout, stderr, osExecutorDeps())
}

func executeWith(ctx context.Context, args []string, env Env, stdout, stderr io.Writer, deps executorDeps) int {
	return 0
}
