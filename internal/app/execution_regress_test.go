package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The receipt needs only the revision of the head and the canonical it expects, never whether the revision is ready: a check that another rule decided
// (a wrong code) does not stop the receipt.
func TestChecksDecideTheReceiptOfAHeadWhoseContractCheckAWrongCodeFailed(t *testing.T) {
	w := receiptWorld(t, "required")
	w.entry(5, 302, bootOwnerID, commands.InvalidCode, "") // a wrong code
	w.execRun()
	w.want(5, checks.Failure, "owner-command-rejected", 2)
	w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects"))

	w.execRun() // the stored check is the failure: nothing says whether the revision is ready

	w.want(5, checks.Failure, "owner-command-rejected", 2)
	w.wantReceipt(execution.CheckSuccess, "receipt-complete", 2)
}

// Without an executor token the job is created already unavailable with no merge base read, so that a run, a gate, a check and an unblock always find a
// job: that holds when the display-only read of the files that are not evaluated fails too.
func TestChecksCreateTheReceiptJobForAMissingExecutorTokenWhateverGitHubSaysOfTheUnapprovedFiles(t *testing.T) {
	headDecl := "version: 1\ninclude: [/tests/**, /pkg/**]\nexecution:\n  profile: go\n  canonical: off\n  receipt: required\n" // the head's declaration is not the canonical's
	canonicalDecl := execDeclaration("off", "required")
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint("executor configured: ", configured), func(t *testing.T) {
			w := newExecWorld(t, canonicalDecl)
			w.check.executorConfigured = configured
			w.reader.filesErr = errors.New("github: 502")

			w.execPullFive(execHeadFiles(headDecl, "v2"))

			jobs := w.headJobs(5, cjHead2, execution.PlanReceipt)
			if !configured {
				if len(jobs) != 1 {
					t.Fatalf("%d receipt jobs, want the one created unavailable in the same run", len(jobs))
				}
				requireFinal(t, jobs[0], execution.ReasonNotConfigured)
				if len(jobs[0].UnapprovedPaths) != 0 || jobs[0].BaseSHA != "" || w.reader.baseReads != 0 {
					t.Errorf("job %+v after %d merge base reads, want no paths, no base and no read", jobs[0], w.reader.baseReads)
				}
				w.wantReceipt(execution.CheckNeutral, "receipt-not-run", 1)
				return
			}
			if len(jobs) != 0 { // with a token, the job waits for GitHub
				t.Fatalf("%d receipt jobs, want none while GitHub fails and the wait has not passed", len(jobs))
			}
			w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)
		})
	}
}

// notStored is a content store that holds only what the test names: any other object, a manifest included, is not there.
type notStored struct {
	inner interface {
		Read(context.Context, artifact.Digest) ([]byte, error)
	}
	allowed map[artifact.Digest]bool
}

func (n notStored) Read(ctx context.Context, d artifact.Digest) ([]byte, error) {
	if !n.allowed[d] {
		return nil, fmt.Errorf("inspect artifact object: %w", fs.ErrNotExist)
	}
	return n.inner.Read(ctx, d)
}

// A revision whose manifest was never stored (one from before manifests were kept) leaves no receipt to decide, and never stops the Contract check.
func TestChecksKeepTheContractCheckFlowingWhenTheManifestOfTheRevisionIsNotStored(t *testing.T) {
	decl := execDeclaration("off", "required")
	w := newExecWorld(t, decl)
	allowed := map[artifact.Digest]bool{}
	for _, files := range []map[string]string{execHeadFiles(decl, "v1"), execHeadFiles(decl, "v2")} {
		for _, content := range files {
			allowed[artifact.Hash([]byte(content))] = true
		}
	}
	w.check.contents = notStored{inner: w.content, allowed: allowed}
	w.tree(cjHead2, execHeadFiles(decl, "v2"))
	w.sync(map[int]string{5: cjHead2})

	w.execRun()

	w.want(5, checks.InProgress, "approval-missing", 1)
	w.noJobs(5, cjHead2, execution.PlanReceipt)
	if !strings.Contains(w.logs.String(), "manifest of the revision is not stored") {
		t.Errorf("logs do not say that the receipt was skipped for a manifest that is not stored: %s", w.logs)
	}
}
