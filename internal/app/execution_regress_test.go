package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
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

// A manifest that the content store lacks is an impossible state, since nothing is deleted from it: the step of that pull request fails with an error that
// says nothing of the store, /status says "failed", and the run goes on with the other pull requests (docs/contracts/runtime.md, "What the checks job does for
// execution", step 6).
func TestChecksFailTheStepOfAPullRequestWhoseRevisionManifestIsNotStoredAndGoOn(t *testing.T) {
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
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.check.liveness.Saw(w.clock.Add(time.Minute))

	err := w.runChecks()

	if err == nil || !strings.Contains(err.Error(), "pull 5") || strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("the run failed with %v, want an opaque failure of pull 5 that says nothing of the store", err)
	}
	if got := w.problems.lookup(bootRepo); got != "failed" {
		t.Errorf("problem %q, want failed", got)
	}
	w.want(4, checks.Success, "unchanged", 1) // the run went on with the other pull request
	w.wantNone(5)                             // and the step that failed published nothing
	w.noJobs(5, cjHead2, execution.PlanReceipt)
}

// The clock of a GitHub failure runs for a head until a job is created for it, whatever the job is: creating one ends it, so a later failure for the same
// head starts a new one (docs/contracts/runtime.md, "What the checks job does for execution", step 5).
func TestChecksEndTheGitHubClockOfAHeadWhenAJobIsCreatedForIt(t *testing.T) {
	headDecl := "version: 1\ninclude: [/tests/**, /pkg/**]\nexecution:\n  profile: go\n  canonical: off\n  receipt: required\n"
	w := newExecWorld(t, execDeclaration("off", "required"))
	w.reader.filesErr = errors.New("github: 502") // the read of the files that are not evaluated fails: the clock of the head runs
	w.execPullFive(execHeadFiles(headDecl, "v2"))
	w.noJobs(5, cjHead2, execution.PlanReceipt)

	w.clock = w.clock.Add(25 * time.Minute)
	w.check.executorConfigured = false
	w.execRun() // without a token the job is created at once, and the clock ends
	first := w.onlyJob(5, cjHead2, execution.PlanReceipt)
	requireFinal(t, first, execution.ReasonNotConfigured)

	w.clock = w.clock.Add(time.Minute)
	w.check.executorConfigured = true
	w.execRun() // the job is replaceable: the read fails again, and a new clock starts
	w.clock = w.clock.Add(5 * time.Minute)
	w.execRun() // more than the wait after the first failure of all, but not after the first of the new clock

	if jobs := w.headJobs(5, cjHead2, execution.PlanReceipt); len(jobs) != 1 || jobs[0].ID != first.ID {
		t.Fatalf("receipt jobs %+v, want only the first: the wait is counted from the failure that followed its creation", jobs)
	}
}

// The files "not evaluated: pattern not approved yet" are the added or changed files that the head's declaration selects (its include less its exclude) and
// the canonical's does not (docs/contracts/runtime.md, "What the checks job does for execution", step 2).
func TestChecksListAsNotEvaluatedTheFilesTheHeadSelectsAndTheCanonicalDoesNot(t *testing.T) {
	const tail = "execution:\n  profile: go\n  canonical: off\n  receipt: required\n"
	cases := []struct {
		name, canonical, head string
		want                  []string
	}{
		{"a file that the head's own exclude leaves out is not listed", "version: 1\ninclude: [/tests/**]\n" + tail,
			"version: 1\ninclude: [/tests/**, /pkg/**]\nexclude: [/pkg/gen/**]\n" + tail, []string{"pkg/a_test.go"}},
		{"a file that the canonical's exclude leaves out is not selected by it", "version: 1\ninclude: [/tests/**]\nexclude: [/tests/gen/**]\n" + tail,
			"version: 1\ninclude: [/tests/**]\n" + tail, []string{"tests/gen/x_test.go"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newExecWorld(t, c.canonical)
			files := execHeadFiles(c.head, "v2") // tests/a_test.go changes under the canonical include, which makes the receipt apply
			diff := []github.PullFile{{Path: ".suiteward.yml", Status: "modified"}, {Path: "tests/a_test.go", Status: "modified"}}
			for _, path := range []string{"pkg/a_test.go", "pkg/gen/b_test.go", "tests/gen/x_test.go"} {
				files[path] = "package x\n"
				diff = append(diff, github.PullFile{Path: path, Status: "added"})
			}
			w.reader.files = map[int][]github.PullFile{5: diff}

			w.execPullFive(files)

			if got := w.onlyJob(5, cjHead2, execution.PlanReceipt); !slices.Equal(got.UnapprovedPaths, c.want) {
				t.Errorf("not evaluated paths %q, want %q", got.UnapprovedPaths, c.want)
			}
		})
	}
}

// A canonical that failed its integrity check governs nothing, so the open jobs of its repository are cancelled as obsolete (docs/contracts/runtime.md, "What the
// checks job does for execution", step 1).
func TestChecksCancelTheOpenJobsOfARepositoryWhoseCanonicalFailedItsIntegrityCheck(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPull(execHeadFiles(execDecl, "v2"))
	open := w.openJobs()
	if len(open) != 3 {
		t.Fatalf("%d open jobs before the failure, want the canonical job of each pull request and the receipt job of pull 5", len(open))
	}

	w.verdict("V1", integrity.Failed)
	w.execRun()

	if left := w.openJobs(); len(left) != 0 {
		t.Fatalf("open jobs %+v after the integrity failure, want none", left)
	}
	for _, job := range open {
		if got := w.jobByID(job.ID); got.State != execution.StateCancelled || got.Reason != execution.ReasonObsolete {
			t.Errorf("job %d is %s/%s, want cancelled/obsolete", job.ID, got.State, got.Reason)
		}
	}
	w.want(4, checks.Failure, "integrity-failed", 2)
}
