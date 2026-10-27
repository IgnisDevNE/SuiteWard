package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// What the checks job wants (docs/contracts/runtime.md, "What the checks job does for execution", steps 2 and 3).

func execSpec(w *cjJobWorld, plan execution.Plan, head string, pull int) execution.Spec {
	return execution.Spec{RepositoryID: bootRepoID, PullNumber: pull, Plan: plan, Profile: "go", HeadSHA: head, GoverningDigest: w.canonicalManifest(), GoverningSource: "V1",
		DeclarationDigest: artifact.Hash([]byte(execDecl)), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelRequired}
}

func TestChecksPlanTheCanonicalAndReceiptJobsOfAProtectedHead(t *testing.T) {
	w := newExecWorld(t, execDecl)

	w.execPull(execHeadFiles(execDecl, "v2"))

	canonical := execSpec(w, execution.PlanCanonical, cjHead2, 5)
	requireSpec(t, w.onlyJob(5, cjHead2, execution.PlanCanonical), canonical)
	receipt := execSpec(w, execution.PlanReceipt, cjHead2, 5)
	receipt.BaseSHA, receipt.ChangedPaths, receipt.ChangedTotal = execMergeBase, []string{"tests/a_test.go"}, 1
	got := w.onlyJob(5, cjHead2, execution.PlanReceipt)
	requireSpec(t, got, receipt)
	if got.State != execution.StatePending {
		t.Errorf("the receipt job is %s, want pending", got.State)
	}
}

func TestChecksPlanOnlyTheCanonicalJobOfAHeadWithNoRevision(t *testing.T) {
	w := newExecWorld(t, execDecl)

	w.execPull(execHeadFiles(execDecl, "v2"))

	requireSpec(t, w.onlyJob(4, bootHead, execution.PlanCanonical), execSpec(w, execution.PlanCanonical, bootHead, 4))
	w.noJobs(4, bootHead, execution.PlanReceipt)
}

func TestChecksGovernTheCanonicalJobByTheConsentedRevisionOnlyWhileTheHeadIsReady(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPull(execHeadFiles(execDecl, "v2"))
	first := w.onlyJob(5, cjHead2, execution.PlanCanonical)

	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.execRun()

	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	if len(jobs) != 2 {
		t.Fatalf("the head has %d canonical jobs, want the first and one governed by the revision\nlogs: %s", len(jobs), w.logs)
	}
	if old := w.jobByID(first.ID); old.State != execution.StateCancelled || old.Reason != execution.ReasonObsolete {
		t.Errorf("the job of the canonical's manifest is %s/%s, want cancelled/obsolete once the revision governs", old.State, old.Reason)
	}
	governed := execSpec(w, execution.PlanCanonical, cjHead2, 5)
	governed.GoverningDigest, governed.GoverningSource = w.revisionManifest(2, 1), "P2-R1"
	requireSpec(t, jobs[1], governed)
	if receipt := w.onlyJob(5, cjHead2, execution.PlanReceipt); receipt.State != execution.StatePending || receipt.GoverningSource != "V1" {
		t.Errorf("the receipt job is %s governed by %s, want pending and the canonical version it expects", receipt.State, receipt.GoverningSource)
	}

	w.consent(contract.RevokeConsent, 202, 2, 1, 5)
	w.execRun()

	jobs = w.headJobs(5, cjHead2, execution.PlanCanonical)
	if len(jobs) != 3 {
		t.Fatalf("the head has %d canonical jobs after the revocation, want a fresh one for the canonical's manifest", len(jobs))
	}
	if jobs[1].State != execution.StateCancelled || jobs[1].Reason != execution.ReasonObsolete {
		t.Errorf("the job of the revision is %s/%s, want cancelled/obsolete", jobs[1].State, jobs[1].Reason)
	}
	if jobs[2].Key != first.Key || jobs[2].ID == first.ID || jobs[2].State != execution.StatePending {
		t.Errorf("the fresh job is %+v, want a pending job with the key of the cancelled first one (a cancelled job does not keep its key)", jobs[2])
	}
}

func TestChecksTakeThePolicyFromTheCanonicalDeclarationOnly(t *testing.T) {
	w := newExecWorld(t, execDecl)

	w.execPull(execHeadFiles(execDeclaration("off", "off"), "v2")) // the head's own declaration turns both levels off

	requireSpec(t, w.onlyJob(5, cjHead2, execution.PlanCanonical), execSpec(w, execution.PlanCanonical, cjHead2, 5))
	if got := w.onlyJob(5, cjHead2, execution.PlanReceipt); got.CanonicalLevel != domain.CanonicalLevelRequired || got.ReceiptLevel != domain.ReceiptLevelRequired {
		t.Errorf("the receipt job has levels %v/%v, want the canonical declaration's required/required", got.CanonicalLevel, got.ReceiptLevel)
	}
}

func TestChecksPlanOnlyWhatTheLevelsOfTheDeclarationWant(t *testing.T) {
	cases := []struct {
		name, declaration  string
		canonical, receipt bool // a job is wanted for the plan
		canonicalLevel     domain.CanonicalLevel
		receiptLevel       domain.ReceiptLevel
	}{
		{"no execution block", "version: 1\ninclude: [/tests/**]\n", false, false, 0, 0},
		{"a profile with both levels off", execDeclaration("off", "off"), false, false, 0, 0},
		{"a profile alone is advisory for both", "version: 1\ninclude: [/tests/**]\nexecution:\n  profile: go\n", true, true, domain.CanonicalLevelAdvisory, domain.ReceiptLevelAdvisory},
		{"canonical alone", execDeclaration("advisory", "off"), true, false, domain.CanonicalLevelAdvisory, domain.ReceiptLevelOff},
		{"receipt alone", execDeclaration("off", "before-approval"), false, true, domain.CanonicalLevelOff, domain.ReceiptLevelBeforeApproval},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newExecWorld(t, c.declaration)

			w.execPull(execHeadFiles(c.declaration, "v2"))

			canonical, receipt := w.headJobs(5, cjHead2, execution.PlanCanonical), w.headJobs(5, cjHead2, execution.PlanReceipt)
			if (len(canonical) == 1) != c.canonical || len(canonical) > 1 {
				t.Fatalf("%d canonical jobs, want a job: %v", len(canonical), c.canonical)
			}
			if (len(receipt) == 1) != c.receipt || len(receipt) > 1 {
				t.Fatalf("%d receipt jobs, want a job: %v", len(receipt), c.receipt)
			}
			if c.canonical && canonical[0].CanonicalLevel != c.canonicalLevel {
				t.Errorf("the canonical job has the level %v, want %v", canonical[0].CanonicalLevel, c.canonicalLevel)
			}
			if c.receipt && receipt[0].ReceiptLevel != c.receiptLevel {
				t.Errorf("the receipt job has the level %v, want %v", receipt[0].ReceiptLevel, c.receiptLevel)
			}
		})
	}
}

func TestChecksPlanNothingForAForkPullRequest(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.headRepo = map[int]string{5: "stranger/widgets"}

	w.execPull(execHeadFiles(execDecl, "v2"))

	w.noJobs(5, cjHead2, execution.PlanCanonical)
	w.noJobs(5, cjHead2, execution.PlanReceipt)
	w.want(5, "in_progress", "fork-unsupported", 1)
	requireSpec(t, w.onlyJob(4, bootHead, execution.PlanCanonical), execSpec(w, execution.PlanCanonical, bootHead, 4)) // the other pull request is not a fork
}

func TestChecksPlanNothingBeforeTheFirstCanonical(t *testing.T) {
	w := newCJWorld(t, false)
	w.sync(map[int]string{4: bootHead})

	w.mustCheck()

	if jobs := w.openJobs(); len(jobs) != 0 {
		t.Fatalf("jobs %+v exist before the first canonical, want none", jobs)
	}
}

func TestChecksPlanNoReceiptWhenTheRevisionChangesNoFileTheCanonicalIncludeMatches(t *testing.T) {
	w := newExecWorld(t, execDecl)
	files := execHeadFiles(execDecl, "v1")
	files[".github/workflows/ci.yml"] = "on: pull_request\n" // protected by default, and no include pattern matches it

	w.execPull(files)

	if record, found := w.record(5); !found || record.Reference != "P2-R1" {
		t.Fatalf("pull 5 has the record %+v (found %v), want a protected change with a revision", record, found)
	}
	w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.noJobs(5, cjHead2, execution.PlanReceipt)
	if record, found := w.receipt(5); found {
		t.Fatalf("a not-applicable receipt has the Receipt check %+v, want none", record)
	}
	if w.reader.baseReads != 0 {
		t.Errorf("%d merge base reads for a receipt that does not apply, want none", w.reader.baseReads)
	}
}

func TestChecksBoundTheChangedPathsOfAReceiptJobAtOneHundredAndCountThem(t *testing.T) {
	w := newExecWorld(t, execDecl)
	files := execHeadFiles(execDecl, "v1")
	var want []string
	for i := range 105 {
		path := fmt.Sprintf("tests/gen/t%03d_test.go", i)
		files[path] = "package gen\n"
		want = append(want, path)
	}

	w.execPull(files)

	got := w.onlyJob(5, cjHead2, execution.PlanReceipt)
	if got.ChangedTotal != 105 || !slices.Equal(got.ChangedPaths, want[:100]) {
		t.Fatalf("the receipt job lists %d paths of %d (first %q), want the first 100 in order of 105", len(got.ChangedPaths), got.ChangedTotal, got.ChangedPaths[:1])
	}
}

func TestChecksListTheFilesOnlyTheHeadsIncludeMatchesAsNotEvaluatedAtMostOneHundred(t *testing.T) {
	w := newExecWorld(t, execDecl)
	headDecl := "version: 1\ninclude: [/tests/**, /pkg/**]\nexecution:\n  profile: go\n  canonical: required\n  receipt: required\n"
	files := execHeadFiles(headDecl, "v2") // tests/a_test.go changes under the canonical include, which makes the receipt apply
	diff := []github.PullFile{{Path: ".suiteward.yml", Status: "modified"}, {Path: "tests/a_test.go", Status: "modified"}, {Path: "pkg/gone_test.go", Status: "removed"}}
	var want []string
	for i := range 105 {
		path := fmt.Sprintf("pkg/x%03d_test.go", i)
		files[path] = "package pkg\n"
		diff = append(diff, github.PullFile{Path: path, Status: "added"})
		want = append(want, path)
	}
	w.reader.files = map[int][]github.PullFile{5: diff}

	w.execPull(files)

	got := w.onlyJob(5, cjHead2, execution.PlanReceipt)
	if !slices.Equal(got.UnapprovedPaths, want[:100]) {
		t.Fatalf("not evaluated paths %d (first %q), want the first 100 files the head's include matches and the canonical's does not", len(got.UnapprovedPaths), got.UnapprovedPaths[:1])
	}
	if !slices.Equal(got.ChangedPaths, []string{"tests/a_test.go"}) {
		t.Errorf("changed paths %q, want only the one the canonical include matches", got.ChangedPaths)
	}
}

func TestChecksCreateOneJobForEachKeyAndNoSecond(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPull(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	if receipt.BaseSHA != execMergeBase {
		t.Fatalf("the receipt job has the base %q, want the merge base", receipt.BaseSHA)
	}

	w.execRun()
	w.execRun()

	if got := w.onlyJob(5, cjHead2, execution.PlanCanonical); got.ID != canonical.ID {
		t.Errorf("canonical job %d, want %d", got.ID, canonical.ID)
	}
	if got := w.onlyJob(5, cjHead2, execution.PlanReceipt); got.ID != receipt.ID {
		t.Errorf("receipt job %d, want %d", got.ID, receipt.ID)
	}
	if w.reader.baseReads != 1 {
		t.Errorf("%d merge base reads, want one: a head that has its receipt job is not read again", w.reader.baseReads)
	}
}

func TestChecksCreateTheJobsUnavailableWithoutAnExecutorTokenAndReplaceThemOnceOneIsConfigured(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.check.executorConfigured = false

	w.execPull(execHeadFiles(execDecl, "v2"))

	oldCanonical, oldReceipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	requireFinal(t, oldCanonical, execution.ReasonNotConfigured)
	requireFinal(t, oldReceipt, execution.ReasonNotConfigured)
	if oldReceipt.BaseSHA != "" || w.reader.baseReads != 0 {
		t.Fatalf("the receipt job has the base %q after %d merge base reads, want none of either", oldReceipt.BaseSHA, w.reader.baseReads)
	}
	w.execRun()
	w.onlyJob(5, cjHead2, execution.PlanCanonical)
	w.onlyJob(5, cjHead2, execution.PlanReceipt) // no second job while there is still no executor

	w.check.executorConfigured = true
	w.execRun()

	canonicals, receipts := w.headJobs(5, cjHead2, execution.PlanCanonical), w.headJobs(5, cjHead2, execution.PlanReceipt)
	if len(canonicals) != 2 || len(receipts) != 2 {
		t.Fatalf("%d canonical and %d receipt jobs once a token is configured, want a fresh job over each", len(canonicals), len(receipts))
	}
	if canonicals[1].State != execution.StatePending || canonicals[1].Key != oldCanonical.Key {
		t.Errorf("the fresh canonical job is %s with the key %s, want pending with the same key", canonicals[1].State, canonicals[1].Key)
	}
	if receipts[1].State != execution.StatePending || receipts[1].BaseSHA != execMergeBase {
		t.Errorf("the fresh receipt job is %s with the base %q, want pending with the merge base", receipts[1].State, receipts[1].BaseSHA)
	}
	requireFinal(t, w.jobByID(oldCanonical.ID), execution.ReasonNotConfigured) // the old rows keep their reason
	requireFinal(t, w.jobByID(oldReceipt.ID), execution.ReasonNotConfigured)
}

func TestChecksReplaceANoExecutorJobOnlyWhenARealExecutorRequestIsLater(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.tree(cjHead2, execHeadFiles(execDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck() // jobs, and no executor is ever seen
	w.clock = w.clock.Add(10 * time.Minute)

	w.mustCheck() // no executor for the wait: the pending jobs are final

	old := w.onlyJob(5, cjHead2, execution.PlanCanonical)
	requireFinal(t, old, execution.ReasonNoExecutor)
	w.mustCheck()
	w.mustCheck() // only the start time says an executor was seen: it never replaces a job
	if got := w.onlyJob(5, cjHead2, execution.PlanCanonical); got.ID != old.ID {
		t.Fatalf("a job replaced the no-executor job %d with only the startup clock to show an executor", old.ID)
	}

	w.check.liveness.Saw(old.FinishedAt) // a request that is not later than the end of the job
	w.mustCheck()
	if got := w.headJobs(5, cjHead2, execution.PlanCanonical); len(got) != 1 {
		t.Fatalf("%d canonical jobs after a request at the end of the old job, want it alone", len(got))
	}

	w.check.liveness.Saw(old.FinishedAt.Add(time.Second)) // a later one
	w.mustCheck()

	jobs := w.headJobs(5, cjHead2, execution.PlanCanonical)
	if len(jobs) != 2 || jobs[1].State != execution.StatePending {
		t.Fatalf("canonical jobs %+v after a later executor request, want a fresh pending job next to the old one", jobs)
	}
	if receipts := w.headJobs(5, cjHead2, execution.PlanReceipt); len(receipts) != 2 || receipts[1].State != execution.StatePending {
		t.Fatalf("receipt jobs %+v, want a fresh pending job too", receipts)
	}
}

func TestChecksCreateTheReceiptJobUnavailableWhenGitHubHasNoMergeBase(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.reader.mergeErrs = map[string]error{cjHead2: fmt.Errorf("compare: %w", rest.ErrNotFound)}

	w.execPull(execHeadFiles(execDecl, "v2"))

	receipt := w.onlyJob(5, cjHead2, execution.PlanReceipt)
	requireFinal(t, receipt, execution.ReasonNoMergeBase)
	if receipt.BaseSHA != "" {
		t.Errorf("the job has the base %q, want none", receipt.BaseSHA)
	}
	w.onlyJob(5, cjHead2, execution.PlanCanonical)
	reads := w.reader.baseReads
	w.execRun()
	w.execRun()
	if got := w.onlyJob(5, cjHead2, execution.PlanReceipt); got.ID != receipt.ID || w.reader.baseReads != reads {
		t.Errorf("job %d after %d more merge base reads, want the final job of the head and no read", got.ID, w.reader.baseReads-reads)
	}
}

func TestChecksWaitForTheMergeBaseThenCreateTheReceiptJobUnavailableForGitHub(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.reader.mergeErrs = map[string]error{cjHead2: errors.New("github: 502"), cjHead3: errors.New("github: 502")}
	w.execPull(execHeadFiles(execDecl, "v2"))
	w.noJobs(5, cjHead2, execution.PlanReceipt) // nothing yet: the receipt reads as pending
	w.onlyJob(5, cjHead2, execution.PlanCanonical)

	w.clock = w.clock.Add(25 * time.Minute)
	w.execRun()
	w.noJobs(5, cjHead2, execution.PlanReceipt) // not yet a whole wait since the first failure

	w.tree(cjHead3, execHeadFiles(execDecl, "v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3}) // another head starts its own clock, though the pull request has waited longer
	w.execRun()
	w.clock = w.clock.Add(10 * time.Minute)
	w.execRun()
	w.noJobs(5, cjHead3, execution.PlanReceipt)

	w.clock = w.clock.Add(25 * time.Minute)
	w.execRun()
	requireFinal(t, w.onlyJob(5, cjHead3, execution.PlanReceipt), execution.ReasonGitHubUnavailable)
	if got := w.onlyJob(5, cjHead3, execution.PlanReceipt); got.BaseSHA != "" {
		t.Errorf("the job created for the GitHub bound has the base %q, want none", got.BaseSHA)
	}
}

func TestChecksSweepNoExecutorFromTheTimeAnExecutorWasLastSeenAndNeverFromTheAgeOfAJob(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.sync(map[int]string{4: bootHead})
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)

	for range 10 { // ten minutes, with an executor seen at every run: the job is older than the wait and stays pending
		w.execRun()
	}
	if got := w.jobByID(job.ID); got.State != execution.StatePending {
		t.Fatalf("the job is %s after the executor was seen at every run, want pending", got.State)
	}

	w.clock = w.clock.Add(10 * time.Minute)
	w.mustCheck() // nobody was seen for longer than the wait

	requireFinal(t, w.jobByID(job.ID), execution.ReasonNoExecutor)
}

func TestChecksSweepTheGitHubBoundOfAPendingJobWhoseReadFailed(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.sync(map[int]string{4: bootHead})
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	if _, err := w.jobs.GitHubFailure(t.Context(), job.ID, w.clock, w.check.executor.GitHubWait); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(20 * time.Minute)
	w.execRun()
	if got := w.jobByID(job.ID); got.State != execution.StatePending {
		t.Fatalf("the job is %s a part of the wait after its first GitHub failure, want pending", got.State)
	}

	w.clock = w.clock.Add(11 * time.Minute)
	w.execRun()

	requireFinal(t, w.jobByID(job.ID), execution.ReasonGitHubUnavailable)
	if row, found, err := w.jobs.Result(t.Context(), job.ID); err != nil || !found || row.Origin != execution.OriginService {
		t.Errorf("results row %+v (found %v, error %v), want one written by the service", row, found, err)
	}
}

func TestChecksSweepALeaseThatRanOutWithNoResult(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.sync(map[int]string{4: bootHead})
	w.execRun()
	job := w.onlyJob(4, bootHead, execution.PlanCanonical)
	leased, found, err := w.jobs.Lease(t.Context(), execution.LeaseRequest{Now: w.clock, LeaseID: fmt.Sprintf("%032x", 1), JobTimeout: time.Hour, GitHubWait: time.Hour},
		func(context.Context, execution.Job) error { return nil })
	if err != nil || !found || leased.ID != job.ID {
		t.Fatalf("lease: job %d, found %v, error %v", leased.ID, found, err)
	}
	w.clock = w.clock.Add(2 * time.Minute) // the lease lasts a minute

	w.execRun()

	got := w.jobByID(job.ID)
	if got.State != execution.StatePending || got.Failures != 1 || got.Reason != execution.ReasonLeaseExpired {
		t.Fatalf("the job is %s with %d failures and the reason %q, want pending, one failure, lease-expired", got.State, got.Failures, got.Reason)
	}
}

func TestChecksCancelTheJobsOfAPullRequestThatClosed(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPull(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)

	w.sync(map[int]string{4: bootHead})
	w.execRun()

	for _, id := range []int64{canonical.ID, receipt.ID} {
		if got := w.jobByID(id); got.State != execution.StateCancelled || got.Reason != execution.ReasonClosed {
			t.Errorf("job %d is %s/%s, want cancelled/closed", id, got.State, got.Reason)
		}
	}
	if open := w.openJobs(); len(open) != 1 || open[0].PullNumber != 4 {
		t.Errorf("open jobs %+v, want only the job of pull 4", open)
	}
}

func TestChecksCancelTheJobsOfAHeadThatMoved(t *testing.T) {
	w := newExecWorld(t, execDecl)
	w.execPull(execHeadFiles(execDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)

	w.tree(cjHead3, execHeadFiles(execDecl, "v3"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.execRun()

	for _, id := range []int64{canonical.ID, receipt.ID} {
		if got := w.jobByID(id); got.State != execution.StateCancelled || got.Reason != execution.ReasonHeadMoved {
			t.Errorf("job %d is %s/%s, want cancelled/head-moved", id, got.State, got.Reason)
		}
	}
	w.onlyJob(5, cjHead3, execution.PlanCanonical)
	w.onlyJob(5, cjHead3, execution.PlanReceipt)
}

// A policy whose receipt level is off wants no receipt job, also for a head whose check says nothing of its revision (an invalid source): a receipt job of an
// older policy that is still open is cancelled as obsolete, so that no executor leases it. With the receipt on, the head's receipt may still be wanted once its
// revision can be read, and a canonical job of a head with no known basis stays in both cases.
func TestChecksCancelTheOpenReceiptJobsOfAHeadWhoseCheckSaysNothingOfItsRevisionOnlyWhenTheReceiptIsOff(t *testing.T) {
	cases := []struct {
		receiptLevel string
		want         execution.State
		reason       execution.Reason
	}{
		{"off", execution.StateCancelled, execution.ReasonObsolete},
		{"required", execution.StatePending, ""},
	}
	for _, c := range cases {
		t.Run("receipt: "+c.receiptLevel, func(t *testing.T) {
			decl := execDeclaration("advisory", c.receiptLevel)
			w := newExecWorld(t, decl)
			files := execHeadFiles(decl, "v2")
			files[".suiteward.yml"] = "version: 1\ninclude: [\n" // the head's declaration does not parse: the check is invalid-source and says nothing of a revision
			w.tree(cjHead2, files)
			w.sync(map[int]string{5: cjHead2})
			canonical := w.seedJob(execSpec(w, execution.PlanCanonical, cjHead2, 5))
			receiptSpec := execSpec(w, execution.PlanReceipt, cjHead2, 5)
			receiptSpec.BaseSHA, receiptSpec.ChangedPaths, receiptSpec.ChangedTotal = execMergeBase, []string{"tests/a_test.go"}, 1
			receipt := w.seedJob(receiptSpec)

			w.execRun()

			w.want(5, checks.Failure, "invalid-source", 1)
			if got := w.jobByID(receipt.ID); got.State != c.want || got.Reason != c.reason {
				t.Errorf("the receipt job is %s/%q, want %s/%q", got.State, got.Reason, c.want, c.reason)
			}
			if got := w.jobByID(canonical.ID); got.State != execution.StatePending {
				t.Errorf("the canonical job is %s, want pending: a head with no known basis keeps it", got.State)
			}
		})
	}
}

// seedJob creates an open job as an earlier run of the checks job would have.
func (w *cjJobWorld) seedJob(spec execution.Spec) execution.Job {
	w.t.Helper()
	job, _, err := w.jobs.Enqueue(w.t.Context(), execution.NewJob{Spec: spec, At: w.clock})
	if err != nil {
		w.t.Fatal(err)
	}
	return job
}

// A policy whose canonical level is off wants no canonical job, whatever the check says of the head: an open canonical job of an earlier policy is cancelled as obsolete,
// so that no executor leases it, also when the check does not settle whether the revision is ready (an invalid source, or the failure of a wrong code that the head
// keeps while its receipt is still wanted).
func TestChecksCancelTheOpenCanonicalJobOfAHeadWhoseCheckDoesNotSettleReadinessWhenTheCanonicalLevelIsOff(t *testing.T) {
	t.Run("a head with an invalid source", func(t *testing.T) {
		decl := execDeclaration("off", "required")
		w := newExecWorld(t, decl)
		files := execHeadFiles(decl, "v2")
		files[".suiteward.yml"] = "version: 1\ninclude: [\n" // the head's declaration does not parse: the check says nothing of a revision
		w.tree(cjHead2, files)
		w.sync(map[int]string{5: cjHead2})
		canonical := w.seedJob(execSpec(w, execution.PlanCanonical, cjHead2, 5)) // of the policy that had the level on

		w.execRun()

		w.want(5, checks.Failure, "invalid-source", 1)
		if got := w.jobByID(canonical.ID); got.State != execution.StateCancelled || got.Reason != execution.ReasonObsolete {
			t.Errorf("the canonical job is %s/%q, want cancelled/obsolete", got.State, got.Reason)
		}
	})
	t.Run("a head with a revision whose stored check is a wrong code", func(t *testing.T) {
		w := receiptWorld(t, "required")
		w.entry(5, 302, bootOwnerID, commands.InvalidCode, "") // a wrong code
		w.execRun()
		w.want(5, checks.Failure, "owner-command-rejected", 2)
		canonical := w.seedJob(execSpec(w, execution.PlanCanonical, cjHead2, 5)) // of the policy that had the level on

		w.execRun() // nothing moved: the stored check is the failure, which names the revision and not whether it is ready

		w.want(5, checks.Failure, "owner-command-rejected", 2)
		if got := w.jobByID(canonical.ID); got.State != execution.StateCancelled || got.Reason != execution.ReasonObsolete {
			t.Errorf("the canonical job is %s/%q, want cancelled/obsolete", got.State, got.Reason)
		}
		if jobs := w.headJobs(5, cjHead2, execution.PlanReceipt); len(jobs) != 1 || jobs[0].State != execution.StatePending {
			t.Errorf("receipt jobs %+v, want the pending one: the receipt is still wanted", jobs)
		}
	})
}

// The Suite a run reads and the policy it plans from are one canonical: a promotion between the two reads must not key a job with the manifest of V1 and the levels of
// V2, which the evidence binding of the job would then record as levels that held.
func TestChecksPlanFromThePolicyOfTheCanonicalOfTheSuiteTheyRead(t *testing.T) {
	w := newMgWorldOf(t, newExecWorld(t, gateDecl)) // V1 requires the canonical run
	enrollment, _, err := w.owners.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := enrollment.Projects[0]
	view, err := w.check.suite(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	w.approved(5, cjHead2, "v2", 2, 201)
	w.leave(5)
	w.merged(5, cjHead2, mgSHA5, "v2", time.Minute)
	w.advance(time.Minute)
	w.mustMerge() // V2 is promoted from a tree whose declaration has no execution block
	if version, _ := w.canonical(); version != "V2" {
		t.Fatalf("canonical %s, want V2", version)
	}

	x, err := w.check.executionRunOf(t.Context(), p, view)

	if err != nil || !x.known || x.policy.canonical != domain.CanonicalLevelRequired || x.policy.declaration != artifact.Hash([]byte(gateDecl)) {
		t.Fatalf("executionRunOf = %+v, %v; want the policy of V1, which the view holds: canonical required, the declaration of V1", x.policy, err)
	}
}
