// Package run is the executor: the loop that leases a job from the service, prepares it (the source check, the dependency prefetch, the
// stdin tar of each sandbox), runs it in disposable containers, and posts the result that docs/contracts/runtime.md defines.
package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/prefetch"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

// The reasons that an executor gives for an unavailable run (docs/contracts/runtime.md, "Unavailable reasons": the rows set by the executor).
const (
	reasonExecutorError      = "executor-error"
	reasonSandboxUnavailable = "sandbox-unavailable"
	reasonArchiveUnavailable = "archive-unavailable"
	reasonGitHubUnavailable  = "github-unavailable"
	reasonFetchFailed        = "fetch-failed"
	reasonInstallFailed      = "install-failed"
	reasonResourceLimit      = "resource-limit"
	reasonToolchainMismatch  = "toolchain-mismatch"
	reasonSourceMismatch     = "source-mismatch"
	reasonTooLarge           = "too-large"
	reasonProfileUnsupported = "profile-unsupported"
)

// reasonError is a failure whose reason the executor itself decided (a resource kill, a toolchain that is too old, a limit passed).
type reasonError struct {
	reason string
	err    error
}

func (e *reasonError) Error() string { return e.reason + ": " + e.err.Error() }
func (e *reasonError) Unwrap() error { return e.err }

// refuse is a failure with the reason that the executor decided.
func refuse(reason, format string, args ...any) error {
	return &reasonError{reason: reason, err: fmt.Errorf(format, args...)}
}

// classify says what a failed attempt means: the reason of an unavailable result, or that the attempt is abandoned and nothing is posted
// (the lease is lost, or the context of the work has ended). The order matters:
//   - a lost lease first: a 409 comes up through Compose and Run wrapped by each, whatever else the chain says;
//   - a failed fetch before the context, because a client timeout of the prefetch also matches context.DeadlineExceeded, and it is a
//     failure to count and retry, never the end of the job's own time;
//   - then the context itself (never errors.Is of a deadline), which says whether the time or the shutdown ended the work;
//   - the prefetch refusals before the source ones, and a stream that broke before a mismatch.
func classify(ctx context.Context, err error) (reason string, abandon bool) {
	var decided *reasonError
	switch {
	case errors.Is(err, client.ErrLeaseLost):
		return "", true
	case errors.Is(err, prefetch.ErrFetchFailed):
		return reasonFetchFailed, false
	case ctx.Err() != nil:
		return "", true
	case errors.As(err, &decided):
		return decided.reason, false
	case errors.Is(err, prefetch.ErrInstallFailed):
		return reasonInstallFailed, false
	case errors.Is(err, prefetch.ErrTooLarge):
		return reasonTooLarge, false
	case errors.Is(err, source.ErrGitHubUnavailable):
		return reasonGitHubUnavailable, false
	case errors.Is(err, source.ErrTooLarge):
		return reasonTooLarge, false
	case errors.Is(err, source.ErrSourceMismatch), errors.Is(err, artifact.ErrInvalidPath), errors.Is(err, artifact.ErrDuplicatePath):
		return reasonSourceMismatch, false
	case errors.Is(err, source.ErrArchiveUnreadable):
		return reasonArchiveUnavailable, false
	case errors.Is(err, sandbox.ErrSetupFailed), errors.Is(err, sandbox.ErrUnavailable):
		return reasonSandboxUnavailable, false
	}
	return reasonExecutorError, false
}
