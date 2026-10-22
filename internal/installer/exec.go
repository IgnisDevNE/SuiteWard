package installer

import (
	"context"
	"io"
)

// Exec runs a command and returns its standard output. Every container CLI call goes through one, so tests inject a fake and
// check the argument list of each call.
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
}

// OSDeps runs real commands on this machine.
func OSDeps() Deps { return Deps{} }

// OSExec runs name with args. When name is an absolute path its directory is put first on the child's PATH, because
// Docker Desktop's credential helper sits next to its CLI. A podman call that selects a connection with --connection also
// drops CONTAINER_HOST from the child's environment, so the caller's own host setting cannot override the selection.
func OSExec(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
	return nil, errNotImplemented
}

// OSInteractive runs name with args on this process's stdin, stdout and stderr.
func OSInteractive(ctx context.Context, name string, args []string) error { return errNotImplemented }

// childEnv is the environment OSExec gives the child.
func childEnv(environ []string, name string, args []string) []string { return nil }
