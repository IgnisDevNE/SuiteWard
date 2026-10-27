// Package run is the executor: the loop that leases a job from the service, prepares it (the source check, the dependency prefetch, the
// stdin tar of each sandbox), runs it in disposable containers, and posts the result that docs/contracts/runtime.md defines.
package run

import (
	"context"
	"fmt"
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
// (the lease is lost, or the context of the work has ended).
func classify(ctx context.Context, err error) (reason string, abandon bool) {
	return "", false
}
