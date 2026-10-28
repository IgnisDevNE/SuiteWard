package run

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/prefetch"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

func TestClassifyMapsEachFailureToItsReason(t *testing.T) {
	live := context.Background()
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"a failure that nothing names", errors.New("boom"), reasonExecutorError},
		{"a runtime that cannot honor a property", fmt.Errorf("run: %w", sandbox.ErrUnavailable), reasonSandboxUnavailable},
		{"a container that failed before the program", fmt.Errorf("run: %w", sandbox.ErrSetupFailed), reasonSandboxUnavailable},
		{"an archive that is no tar.gz", fmt.Errorf("open: %w", source.ErrArchiveUnreadable), reasonArchiveUnavailable},
		{"a stream that broke short of its limit", fmt.Errorf("open: %w", source.ErrGitHubUnavailable), reasonGitHubUnavailable},
		{"a module that could not be fetched", fmt.Errorf("fetch: %w", prefetch.ErrFetchFailed), reasonFetchFailed},
		{"a module that is refused", fmt.Errorf("plan: %w", prefetch.ErrInstallFailed), reasonInstallFailed},
		{"a module zip whose size cannot be had", fmt.Errorf("fetch: %w", prefetch.ErrTooLarge), reasonTooLarge},
		{"an archive that passed its limit", fmt.Errorf("open: %w", source.ErrTooLarge), reasonTooLarge},
		{"a source that is not the tree", fmt.Errorf("check: %w", source.ErrSourceMismatch), reasonSourceMismatch},
		{"a path that the manifest cannot hold", fmt.Errorf("inspect: %w", artifact.ErrInvalidPath), reasonSourceMismatch},
		{"two entries of one path", fmt.Errorf("inspect: %w", artifact.ErrDuplicatePath), reasonSourceMismatch},
		{"a plan the caller got wrong", fmt.Errorf("compose: %w", source.ErrInvalidPlan), reasonExecutorError},
		{"a spec the runner refuses", fmt.Errorf("run: %w", sandbox.ErrInvalidSpec), reasonExecutorError},
		{"a reason the executor decided", refuse(reasonResourceLimit, "the container was killed"), reasonResourceLimit},
	} {
		got, abandon := classify(live, tc.err)
		if got != tc.reason || abandon {
			t.Errorf("%s: classify = %q, %v; want %q", tc.name, got, abandon, tc.reason)
		}
	}
}

func TestClassifyTestsTheLeaseBeforeAnythingElse(t *testing.T) {
	// A 409 of an archive opener comes up through Compose and Run, wrapped by each of them: the lease is lost, whatever else the chain says.
	for _, err := range []error{
		client.ErrLeaseLost,
		fmt.Errorf("read the standard input of the sandbox: %w", fmt.Errorf("open the head archive: %w", client.ErrLeaseLost)),
		errors.Join(client.ErrLeaseLost, source.ErrSourceMismatch),
		errors.Join(source.ErrGitHubUnavailable, client.ErrLeaseLost),
	} {
		if reason, abandon := classify(context.Background(), err); !abandon || reason != "" {
			t.Errorf("classify(%v) = %q, %v; want the work abandoned", err, reason, abandon)
		}
	}
}

func TestClassifyAbandonsWhenTheContextOfTheWorkHasEnded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range []error{context.Canceled, fmt.Errorf("run: %w", sandbox.ErrUnavailable), errors.New("boom"), refuse(reasonResourceLimit, "killed")} {
		if reason, abandon := classify(ctx, err); !abandon || reason != "" {
			t.Errorf("classify(%v) with an ended context = %q, %v; want the work abandoned", err, reason, abandon)
		}
	}
}

// A client timeout of the prefetch also matches context.DeadlineExceeded. It is a failed fetch, counted and retried, and it is never
// taken for the end of the job's own time, which abandons the work.
func TestClassifyTestsAFailedFetchBeforeAnyDeadlineError(t *testing.T) {
	err := fmt.Errorf("%w: %w", prefetch.ErrFetchFailed, context.DeadlineExceeded)
	if reason, abandon := classify(context.Background(), err); reason != reasonFetchFailed || abandon {
		t.Errorf("classify = %q, %v; want fetch-failed", reason, abandon)
	}
	// A deadline error that no fetch failure wraps, with a context that is still alive, is the executor's own failure, not an abandoned job.
	if reason, abandon := classify(context.Background(), context.DeadlineExceeded); reason != reasonExecutorError || abandon {
		t.Errorf("classify(DeadlineExceeded) = %q, %v; want executor-error", reason, abandon)
	}
}

func TestClassifyPutsTheRefusalsOfThePrefetchBeforeTheSourceSentinels(t *testing.T) {
	// A dependency failure while the tar streams reaches the caller through the sink, which may wrap a source error around it.
	err := fmt.Errorf("%w: adding a file: %w", prefetch.ErrInstallFailed, source.ErrTooLarge)
	if reason, _ := classify(context.Background(), err); reason != reasonInstallFailed {
		t.Errorf("classify = %q; want install-failed", reason)
	}
	if reason, _ := classify(context.Background(), fmt.Errorf("%w: %w", source.ErrGitHubUnavailable, source.ErrSourceMismatch)); reason != reasonGitHubUnavailable {
		t.Errorf("a stream that broke is github-unavailable before it is a mismatch; got %q", reason)
	}
}
