package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	executor "github.com/IgnisDevNE/SuiteWard/internal/executor/run"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

const (
	settingPrefix    = "SUITEWARD_"
	minExecutorToken = 32 // like MCP_TOKEN: the executor's token is the same value as the service's
)

// serviceCredentials are the settings of the service that hold a secret. The executor holds nothing but its own token, so an environment
// that has one of them, or its _FILE form, is not the executor's.
var serviceCredentials = []string{"DATABASE_URL", "MIGRATION_DATABASE_URL", "GITHUB_APP_KEY", "MCP_TOKEN"}

// executorSettings are the three settings of `suiteward execute` (docs/contracts/runtime.md, "Commands").
type executorSettings struct {
	url     string
	token   config.Secret
	runtime string
}

// readExecutorSettings reads the settings of the executor from env, and refuses an environment that holds a credential of the service. It
// reads these and nothing else, and reports every problem at once, naming the variable and never its value.
func readExecutorSettings(env Env) (executorSettings, error) {
	var errs []error
	for _, name := range serviceCredentials {
		for _, variable := range []string{settingPrefix + name, settingPrefix + name + "_FILE"} {
			if value, _ := env.Lookup(variable); value != "" {
				errs = append(errs, fmt.Errorf("%s is set: the executor holds no credential of the service, so remove it from this environment", variable))
			}
		}
	}
	s := executorSettings{runtime: "podman"}
	if url, _ := env.Lookup(settingPrefix + "EXECUTOR_URL"); !client.ValidOrigin(url) {
		// No cause and no value: the URL may carry user info.
		errs = append(errs, errors.New("SUITEWARD_EXECUTOR_URL must be an https URL, or an http URL for a loopback host, with no path, query or user info"))
	} else {
		s.url = url
	}
	token, err := readExecutorToken(env)
	if err != nil {
		errs = append(errs, err)
	}
	s.token = token
	if runtime, _ := env.Lookup(settingPrefix + "EXECUTOR_RUNTIME"); runtime != "" {
		s.runtime = runtime
	}
	if s.runtime != "podman" && s.runtime != "docker" {
		errs = append(errs, errors.New("SUITEWARD_EXECUTOR_RUNTIME must be podman or docker"))
	}
	if len(errs) > 0 {
		return executorSettings{}, errors.Join(errs...)
	}
	return s, nil
}

// readExecutorToken reads the token from SUITEWARD_EXECUTOR_TOKEN or, as the settings of the service do, from the file that
// SUITEWARD_EXECUTOR_TOKEN_FILE names (the trailing newline trimmed). Both is an error.
func readExecutorToken(env Env) (config.Secret, error) {
	direct, _ := env.Lookup(settingPrefix + "EXECUTOR_TOKEN")
	path, _ := env.Lookup(settingPrefix + "EXECUTOR_TOKEN_FILE")
	switch {
	case direct != "" && path != "":
		return "", errors.New("SUITEWARD_EXECUTOR_TOKEN_FILE must not be set together with SUITEWARD_EXECUTOR_TOKEN")
	case path != "":
		content, err := env.ReadFile(path)
		if err != nil {
			// The path is dropped: an operator may have put the secret itself in the variable.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				err = pathErr.Err
			}
			return "", fmt.Errorf("SUITEWARD_EXECUTOR_TOKEN_FILE: cannot read the file: %w", err)
		}
		direct = strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	case direct == "":
		return "", errors.New("SUITEWARD_EXECUTOR_TOKEN is required")
	}
	if len(direct) < minExecutorToken {
		return "", fmt.Errorf("SUITEWARD_EXECUTOR_TOKEN must be at least %d bytes", minExecutorToken)
	}
	return config.Secret(direct), nil
}

// executorDeps are the parts of `suiteward execute` that the tests replace: the container runtime, the sweep of its leftover containers
// and the digest of the executor's own binary.
type executorDeps struct {
	runtime   func(binary string) sandbox.Runtime
	sweep     func(binary string) func(context.Context) error
	binarySum func() (string, error)
}

func osExecutorDeps() executorDeps {
	return executorDeps{runtime: sandbox.NewCLI, sweep: executor.Sweeper, binarySum: func() (string, error) {
		path, err := os.Executable()
		if err != nil {
			return "", err
		}
		return fileSum(path)
	}}
}

// openFile is os.Open: the file is the executor's own binary, which the process names itself, not a path that a caller gave.
var openFile = os.Open

// fileSum is the SHA-256 of a file, as 64 lowercase hexadecimal digits.
func fileSum(path string) (string, error) {
	f, err := openFile(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runExecute is `suiteward execute`: the executor, a process of the machine that has the container runtime. It reads its three settings
// and runs before any setting of the service is loaded. Exit 0 is a clean stop at the signal, 1 a fatal error (the settings, the refused
// environment, no runtime), 2 a usage error.
func runExecute(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	return executeWith(ctx, args, env, stdout, stderr, osExecutorDeps())
}

func executeWith(ctx context.Context, args []string, env Env, stdout, stderr io.Writer, deps executorDeps) int {
	if len(args) != 0 {
		return usageError(stderr)
	}
	settings, err := readExecutorSettings(env)
	if err != nil {
		reportf(stderr, "invalid settings:\n%v\n", err)
		return 1
	}
	api, err := client.New(settings.url, settings.token)
	if err != nil {
		reportf(stderr, "invalid settings:\n%v\n", err)
		return 1
	}
	sum, err := deps.binarySum()
	if err != nil {
		reportf(stderr, "cannot read the executor binary to name it in the results: %v\n", err)
		return 1
	}
	ex, err := executor.New(executor.Config{
		API: api, Runtime: deps.runtime(settings.runtime), Sweep: deps.sweep(settings.runtime),
		Version: Version, BinarySHA256: sum, Log: newLogger(stdout, slog.LevelInfo),
	})
	if err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	if err := ex.Run(ctx); err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	return 0
}
