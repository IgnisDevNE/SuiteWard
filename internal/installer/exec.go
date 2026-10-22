package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// Exec runs a command and returns its standard output. Every container CLI call goes through one, so tests inject a fake and
// check the argument list of each call. A command that fails returns what it printed together with the error.
type Exec func(ctx context.Context, name string, args []string, stdin io.Reader) (stdout []byte, err error)

// Interactive runs a command with the real stdin, stdout and stderr connected and returns when it exits. It never captures
// output: setup prints the TOTP enrollment URI and secret there.
type Interactive func(ctx context.Context, name string, args []string) error

// Deps is what the runtimes need from the machine.
type Deps struct {
	Exec        Exec
	Interactive Interactive
	GOOS        string
	Getenv      func(string) string
	// UserSID is the Windows SID of the current user, which restrictPath grants; only a Windows run asks for it.
	UserSID func() (string, error)
}

// validate refuses a Deps with a missing function: a nil one would fail later, at the first container command.
func (d Deps) validate() error {
	if d.Exec == nil || d.Interactive == nil || d.Getenv == nil {
		return errors.New("the container runtime needs Exec, Interactive and Getenv")
	}
	return nil
}

// OSDeps runs real commands on this machine.
func OSDeps() Deps {
	return Deps{Exec: OSExec, Interactive: OSInteractive, GOOS: runtime.GOOS, Getenv: os.Getenv}
}

// OSExec runs name with args. When name is an absolute path its directory is put first on the child's PATH, because
// Docker Desktop's credential helper sits next to its CLI. A podman call that selects a connection with --connection also
// drops CONTAINER_HOST from the child's environment, so the caller's own host setting cannot override the selection.
func OSExec(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
	cmd := commandContext(ctx, name, args...)
	cmd.Env = childEnv(os.Environ(), name, args)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// The output is returned beside the error (a failing check prints its report on stdout) but is not in the error: a
		// command may print a secret. A caller that wants a tail of it asks for it (stdoutTail). Every caller reads the error first.
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", describe(name, args), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// OSInteractive runs name with args on this process's stdin, stdout and stderr.
func OSInteractive(ctx context.Context, name string, args []string) error {
	cmd := commandContext(ctx, name, args...)
	cmd.Env = childEnv(os.Environ(), name, args)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", describe(name, args), err)
	}
	return nil
}

// stdoutTailBytes bounds how much of a command's standard output a caller repeats in an error.
const stdoutTailBytes = 2048

// stdoutTail is the end of a command's standard output, where a command says why it stopped (migrate logs there). Only a
// caller that knows its command prints nothing secret asks for it.
func stdoutTail(stdout []byte) string {
	if len(stdout) > stdoutTailBytes {
		stdout = stdout[len(stdout)-stdoutTailBytes:]
		for len(stdout) > 0 && !utf8.RuneStart(stdout[0]) {
			stdout = stdout[1:]
		}
	}
	return strings.TrimSpace(string(stdout))
}

// describe names a command in an error without repeating a long script argument.
func describe(name string, args []string) string {
	line := filepath.Base(name) + " " + strings.Join(args, " ")
	if len(line) > 120 {
		line = line[:120] + "..."
	}
	return line
}

// childEnv is the environment OSExec gives the child.
func childEnv(environ []string, name string, args []string) []string {
	dropHost := len(args) > 0 && args[0] == "--connection"
	dir := ""
	if filepath.IsAbs(name) {
		dir = filepath.Dir(name)
	}
	out := make([]string, 0, len(environ)+1)
	pathSet := false
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		switch {
		case dropHost && strings.EqualFold(key, "CONTAINER_HOST"):
			continue
		case dir != "" && strings.EqualFold(key, "PATH"):
			kv = key + "=" + dir
			if value != "" {
				kv += string(os.PathListSeparator) + value
			}
			pathSet = true
		}
		out = append(out, kv)
	}
	if dir != "" && !pathSet {
		out = append(out, "PATH="+dir)
	}
	return out
}

// commandContext is exec.CommandContext. The installer runs the container CLI on purpose, with argument lists it builds
// itself from constants and its settings, so gosec's G204 (a subprocess launched with a variable) does not apply.
var commandContext = exec.CommandContext
