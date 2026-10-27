package app

import (
	"context"
	"encoding/json"
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
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
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

// hideManifests makes the content store hold only the files of the test, so that every manifest, which only the revisions of the pull requests name, is missing.
func (w *cjJobWorld) hideManifests(decl string) {
	w.t.Helper()
	allowed := map[artifact.Digest]bool{}
	for _, files := range []map[string]string{execHeadFiles(decl, "v1"), execHeadFiles(decl, "v2")} {
		for _, content := range files {
			allowed[artifact.Hash([]byte(content))] = true
		}
	}
	w.check.contents = notStored{inner: w.content, allowed: allowed}
}

// A manifest that the content store lacks is an impossible state, since nothing is deleted from it: the step of that pull request fails with an error that
// says nothing of the store, /status says "failed", and the run goes on with the other pull requests (docs/contracts/runtime.md, "What the checks job does for
// execution", step 6). The check is held as the wait rule holds it, so that a head always has one and none of them is a success that was not decided: a new head
// reads assessment-pending, and a head that has a check keeps it.
func TestChecksFailTheStepOfAPullRequestWhoseRevisionManifestIsNotStoredAndHoldItsCheck(t *testing.T) {
	decl := execDeclaration("off", "required")
	t.Run("a new head reads assessment-pending", func(t *testing.T) {
		w := newExecWorld(t, decl)
		w.hideManifests(decl)
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
		w.want(4, checks.Success, "unchanged", 1)             // the run went on with the other pull request
		w.want(5, checks.InProgress, "assessment-pending", 1) // and the head that failed has a check that is no success
		w.noJobs(5, cjHead2, execution.PlanReceipt)
	})
	t.Run("a head that has a check keeps it", func(t *testing.T) {
		w := newExecWorld(t, decl)
		w.reader.mergeErrs = map[string]error{cjHead2: errors.New("github: 502")} // no receipt job yet, so the next run reads the manifest of the revision
		w.execPullFive(execHeadFiles(decl, "v2"))
		w.want(5, checks.InProgress, "approval-missing", 1)
		w.noJobs(5, cjHead2, execution.PlanReceipt)
		w.hideManifests(decl)

		w.check.liveness.Saw(w.clock.Add(time.Minute))
		err := w.runChecks()

		if err == nil || !strings.Contains(err.Error(), "pull 5") {
			t.Fatalf("the run failed with %v, want a failure of pull 5", err)
		}
		w.want(5, checks.InProgress, "approval-missing", 1) // the stored check of the head, republished as it was
		if got := w.problems.lookup(bootRepo); got != "failed" {
			t.Errorf("problem %q, want failed", got)
		}
	})
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

// publishedReceipt gives pull 5 a Receipt check for the head cjHead2 as an earlier run would have published it, without going through the job.
func (w *cjJobWorld) publishedReceipt() {
	w.t.Helper()
	first := execution.ReceiptCheck{RepositoryID: bootRepoID, PullNumber: 5, HeadSHA: cjHead2, State: execution.CheckInProgress, Reason: "receipt-running", Reference: "P2-R1"}
	_, _, err := w.receipts.ReceiptMemory.Publish(w.t.Context(), first, w.clock, func(generation int64) (governance.OutboxMessage, error) {
		return governance.OutboxMessage{Key: fmt.Sprintf("receipt-check:%d:5:%d", bootRepoID, generation), Kind: "github_receipt_check", Payload: json.RawMessage(`{}`)}, nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// ciOnly is the files of a head that changes only a file that the protected defaults cover and no include pattern matches, so that its receipt does not apply.
func ciOnly(decl string) map[string]string {
	files := execHeadFiles(decl, "v1")
	files[".github/workflows/ci.yml"] = "on: pull_request\n"
	return files
}

// A head that already has a published Receipt check and whose receipt then does not apply, or whose level is off, gets one last run, neutral, and nothing after it
// while that stays so; a head that never had one gets none (docs/contracts/runtime.md, "The SuiteWard / Receipt check").
func TestChecksPublishOneLastNeutralRunWhenThePublishedReceiptOfAHeadNoLongerApplies(t *testing.T) {
	cases := []struct {
		name string
		decl string
	}{
		{"the revision changes no file the canonical include matches", execDeclaration("off", "required")},
		{"the receipt level is off", execDeclaration("advisory", "off")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newExecWorld(t, c.decl)
			w.tree(cjHead2, ciOnly(c.decl))
			w.sync(map[int]string{5: cjHead2})
			w.publishedReceipt()

			w.execRun()

			record := w.wantReceipt(execution.CheckNeutral, "receipt-not-applicable", 2)
			if record.Reference != "P2-R1" || record.JobID != 0 || record.Unblocked {
				t.Errorf("record %+v, want the revision kept and no job", record)
			}
			payload := w.lastReceiptPayload()
			if payload["state"] != "neutral" || payload["reason"] != "receipt-not-applicable" || payload["unavailable"] != nil || payload["unblocked"] != nil {
				t.Errorf("payload %v, want neutral with the reason receipt-not-applicable", payload)
			}
			wantCounts(t, payload, nil)

			w.execRun()
			w.execRun()
			w.wantReceipt(execution.CheckNeutral, "receipt-not-applicable", 2) // nothing after the last run
			if len(w.receipts.messages) != 1 {
				t.Errorf("%d messages, want the one of the last run", len(w.receipts.messages))
			}
		})
	}
}

func TestChecksWithdrawNoReceiptThatIsNotTheHeadsOrThatTheRunCouldNotDecide(t *testing.T) {
	decl := execDeclaration("off", "required")
	t.Run("a Receipt check of another head", func(t *testing.T) {
		w := newExecWorld(t, decl)
		w.tree(cjHead3, ciOnly(decl))
		w.sync(map[int]string{5: cjHead3})
		w.publishedReceipt() // of cjHead2

		w.execRun()

		w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)
	})
	t.Run("a policy that cannot be read", func(t *testing.T) {
		w := newExecWorld(t, decl)
		w.tree(cjHead2, ciOnly(decl))
		w.sync(map[int]string{5: cjHead2})
		w.publishedReceipt()
		w.check.contents = unreadableContents{}
		w.clock = w.clock.Add(time.Minute)

		if err := w.check.run(t.Context()); err == nil {
			t.Fatal("the run succeeded while the policy could not be read")
		}

		w.wantReceipt(execution.CheckInProgress, "receipt-running", 1)
	})
}

func TestTheReceiptCheckMessageKnowsEveryReasonOfTheCheckAndNoOther(t *testing.T) {
	for reason, state := range map[string]execution.CheckState{"receipt-running": execution.CheckInProgress, "receipt-failing": execution.CheckFailure, "receipt-complete": execution.CheckSuccess,
		"receipt-not-run": execution.CheckNeutral, "receipt-not-applicable": execution.CheckNeutral} {
		d := rcpDecision(state, reason) // a decision whose state, counts and reason fit: the message is rendered when it is built
		d.check.RepositoryID, d.check.PullNumber, d.check.HeadSHA = bootRepoID, 5, cjHead2
		if _, err := newReceiptCheckMessage(bootRepo, d, "isolated", 1); err != nil {
			t.Errorf("reason %s: %v", reason, err)
		}
	}
	check := execution.ReceiptCheck{RepositoryID: bootRepoID, PullNumber: 5, HeadSHA: cjHead2, State: execution.CheckNeutral, Reason: "receipt-unknown"}
	if _, err := newReceiptCheckMessage(bootRepo, receiptDecision{publish: true, check: check}, "isolated", 1); err == nil {
		t.Error("a reason that the check does not have was accepted")
	}
}

// enqueueFails is an execution store that cannot create a job while err is set.
type enqueueFails struct {
	execution.Store
	err error
}

func (s *enqueueFails) Enqueue(ctx context.Context, n execution.NewJob) (execution.Job, bool, error) {
	if s.err != nil {
		return execution.Job{}, false, s.err
	}
	return s.Store.Enqueue(ctx, n)
}

// Creating a job ends the clock of the GitHub failures of its head, so a job that could not be created leaves the clock where it was (docs/contracts/runtime.md,
// "What the checks job does for execution", step 5).
func TestChecksKeepTheGitHubClockOfAHeadWhenItsJobCouldNotBeCreated(t *testing.T) {
	decl := execDeclaration("off", "required")
	w := newExecWorld(t, decl)
	w.reader.mergeErrs = map[string]error{cjHead2: errors.New("github: 502")}
	w.execPullFive(execHeadFiles(decl, "v2")) // the clock of the head starts at its first failure
	w.noJobs(5, cjHead2, execution.PlanReceipt)

	store := &enqueueFails{Store: w.jobs, err: errors.New("the store is down")}
	w.check.executions = store
	delete(w.reader.mergeErrs, cjHead2) // GitHub is back, so the job is to be created, and the store refuses it
	w.clock = w.clock.Add(25 * time.Minute)
	w.check.liveness.Saw(w.clock.Add(time.Minute))
	if err := w.runChecks(); err == nil {
		t.Fatal("the run succeeded though the store could not create the job")
	}

	store.err = nil
	w.reader.mergeErrs[cjHead2] = errors.New("github: 502") // GitHub fails again, a whole wait after the first failure
	w.clock = w.clock.Add(6 * time.Minute)
	w.execRun()

	requireFinal(t, w.onlyJob(5, cjHead2, execution.PlanReceipt), execution.ReasonGitHubUnavailable)
}

// headJobsFail is an execution store that cannot list the jobs of a head, which fails the step of execution of every pull request.
type headJobsFail struct{ execution.Store }

func (headJobsFail) HeadJobs(context.Context, int64, int, string, execution.Plan) ([]execution.Job, error) {
	return nil, errors.New("the store is down")
}

// A step of execution that fails holds the check as the wait rule does, and the wait rule lets a wrong code that was entered since fail it: a failed step never
// hides one, whether the check it holds is a wait, a success or a runner block.
func TestChecksFailTheCheckOnAFreshWrongCodeWhenTheStepFails(t *testing.T) {
	t.Run("over a stored approval-missing, with a manifest that is not stored", func(t *testing.T) {
		decl := execDeclaration("off", "required")
		w := newExecWorld(t, decl)
		w.reader.mergeErrs = map[string]error{cjHead2: errors.New("github: 502")} // no receipt job yet, so the next run reads the manifest of the revision
		w.execPullFive(execHeadFiles(decl, "v2"))
		w.want(5, checks.InProgress, "approval-missing", 1)
		w.entry(5, 302, bootOwnerID, commands.InvalidCode, "")
		w.hideManifests(decl)

		w.check.liveness.Saw(w.clock.Add(time.Minute))
		if err := w.runChecks(); err == nil {
			t.Fatal("the run succeeded though the step failed")
		}

		w.want(5, checks.Failure, "owner-command-rejected", 2)
	})
	t.Run("over a stored success, with a store that is down", func(t *testing.T) {
		w := newExecWorld(t, execDeclaration("required", "off"))
		w.sync(map[int]string{4: bootHead})
		w.execRun()
		w.finish(w.onlyJob(4, bootHead, execution.PlanCanonical), canonicalTests("pass"))
		w.execRun()
		w.want(4, checks.Success, "unchanged", 2)
		w.entry(4, 303, bootOwnerID, commands.InvalidCode, "")
		w.check.executions = headJobsFail{w.jobs}

		w.check.liveness.Saw(w.clock.Add(time.Minute))
		if err := w.runChecks(); err == nil {
			t.Fatal("the run succeeded though the step failed")
		}

		w.want(4, checks.Failure, "owner-command-rejected", 3)
	})
}

func TestChecksRestoreARunnerBlockWithItsRunnerSetWhenTheStepFails(t *testing.T) {
	w := newCJWorld(t, true)
	w.runnerPull(withFiles(bootFiles("v1"), cjJest), added(cjJest)...)
	w.want(5, checks.InProgress, "runner-uncovered", 1)
	if err := w.memory.FailMessage(t.Context(), "check:1001:5:1"); err != nil { // the message of the check is lost, so the next run publishes it again, with its files
		t.Fatal(err)
	}
	w.check.executions = headJobsFail{w.check.executions}

	err := w.runChecks()

	if err == nil || strings.Contains(err.Error(), "no runner set") {
		t.Fatalf("the run failed with %v, want the failure of the step and no complaint of a missing runner set", err)
	}
	w.want(5, checks.InProgress, "runner-uncovered", 2)
}

// The fresh assessment of a run can decide no runner block where the stored check has one (the owner revoked the approval, so the head is approval-missing): the
// first pass has no runner set, and the check that a failed step restores must come out of a second pass that reads one.
func TestChecksRestoreAStoredRunnerBlockWithItsRunnerSetWhenTheFreshAssessmentDoesNotRepeatItAndTheStepFails(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: added(cjJest)}
	w.scopePull(withFiles(bootFiles("v2"), cjJest), bootFiles("v1")) // a protected change, and a runner file
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()
	w.want(5, checks.InProgress, "runner-uncovered", 2)
	w.consent(contract.RevokeConsent, 202, 2, 1, 5)                             // the next assessment says approval-missing
	if err := w.memory.FailMessage(t.Context(), "check:1001:5:2"); err != nil { // the message of the check is lost, so the next run publishes it again, with its files
		t.Fatal(err)
	}
	w.check.executions = headJobsFail{w.check.executions}

	err := w.runChecks()

	if err == nil || strings.Contains(err.Error(), "no runner set") {
		t.Fatalf("the run failed with %v, want the failure of the step and no complaint of a missing runner set", err)
	}
	w.want(5, checks.InProgress, "runner-uncovered", 3)
}

func TestChecksFailARunnerBlockOnAFreshWrongCodeWhenTheStepFails(t *testing.T) {
	w := newCJWorld(t, true)
	w.runnerPull(withFiles(bootFiles("v1"), cjJest), added(cjJest)...)
	w.want(5, checks.InProgress, "runner-uncovered", 1)
	w.entry(5, 304, bootOwnerID, commands.InvalidCode, "")
	w.check.executions = headJobsFail{w.check.executions}

	if err := w.runChecks(); err == nil {
		t.Fatal("the run succeeded though the step failed")
	}

	w.want(5, checks.Failure, "owner-command-rejected", 2)
}

// A policy that was read and has the receipt off decides the receipt whatever the Contract check says of the head: a Receipt check that was published is
// withdrawn also for a head whose source is invalid.
func TestChecksWithdrawThePublishedReceiptWhenTheLevelIsOffWhateverTheContractCheckSays(t *testing.T) {
	decl := execDeclaration("advisory", "off")
	w := newExecWorld(t, decl)
	files := execHeadFiles(decl, "v2")
	files[".suiteward.yml"] = "version: 1\ninclude: [\n" // the head's declaration does not parse: the check is invalid-source
	w.tree(cjHead2, files)
	w.sync(map[int]string{5: cjHead2})
	w.publishedReceipt()

	w.execRun()

	w.want(5, checks.Failure, "invalid-source", 1)
	w.wantReceipt(execution.CheckNeutral, "receipt-not-applicable", 2)
}
