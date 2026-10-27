//go:build integration

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// mergeBaseReader is the reader of the job's tests with a merge base for every head.
type mergeBaseReader struct{ *fakeBootReader }

func (mergeBaseReader) MergeBase(context.Context, string, string, string) (string, error) {
	return execMergeBase, nil
}

// TestChecksJobOnPostgreSQLGatesTheContractCheckAndDecidesTheReceipt runs the checks job against the real stores with canonical: required and receipt:
// required: the canonical job holds the check of an unchanged pull request until its run completes, the receipt of a protected change is decided from
// the stored result and its tests, and the jobs of a pull request that closes are cancelled.
func TestChecksJobOnPostgreSQLGatesTheContractCheckAndDecidesTheReceipt(t *testing.T) {
	databaseURL := newDatabase(t)
	env := testEnv(serveVars(t, databaseURL))
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	pool, store, artifacts, err := connectWithArtifacts(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	enrollment := owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: make([]byte, 20), Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime,
		Projects: []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}}}
	if err := store.Enroll(t.Context(), enrollment); err != nil {
		t.Fatal(err)
	}
	cycle := func(at time.Time, heads map[int]string) {
		t.Helper()
		c := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: at, StartedAt: at, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
		for number, head := range heads {
			c.OpenPulls = append(c.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime})
			c.Comments[number] = nil
		}
		if _, err := store.RecordCycle(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	cycle(bootTime, map[int]string{4: bootHead})
	declaration := execDeclaration("required", "required")
	baseline, changed := execHeadFiles(declaration, "v1"), execHeadFiles(declaration, "v2")
	repository := newBootRepository(bootRepo, bootRepoID)
	repository.trees[bootHead], repository.trees[cjHead2] = &bootTree{files: baseline}, &bootTree{files: changed}
	reader := &fakeBootReader{repos: map[string]*bootRepository{bootRepo: repository}, calls: map[string]int{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bootstrap := &bootstrapJob{repos: []string{bootRepo}, reader: reader, owners: store, suites: store, problems: &bootstrapProblems{},
		logger: logger, now: func() time.Time { return bootTime.Add(time.Hour) }}
	if _, err := bootstrap.run(t.Context()); err != nil { // the Suite
		t.Fatal(err)
	}
	proposeBaseline(t, store, artifacts, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, baseline) // P1-R1, as the commands job would
	command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: "consent:github-comment:101", SourceCommandID: "github-comment:101", Actor: bootOwnerPrincipal(),
		Action: contract.ApproveConsent, Order: testOrder(101), Carrier: carrierID(bootRepoID, 4),
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: proposalID(1), RevisionID: "R1"}}))
	if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: command}); err != nil {
		t.Fatal(err)
	}
	for range 3 { // the integrity assessment and the first canonical
		if _, err := bootstrap.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	clock := bootTime.Add(2 * time.Hour)
	job := (&checksJob{repos: []string{bootRepo}, reader: mergeBaseReader{reader}, owners: store, suites: store, contents: artifacts, content: artifacts, ledger: store.Checks(), integrity: store.Integrity(),
		commands: store, pulls: store, tier: owner.TierIsolated, problems: &bootstrapProblems{}, logger: logger, now: func() time.Time { return clock }}).
		withExecution(store.Execution(), store.ReceiptChecks(), clock)
	step := func() {
		t.Helper()
		clock = clock.Add(time.Minute)
		job.liveness.Saw(clock) // an executor is at work
		if err := job.run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	check := func(pull int) string {
		t.Helper()
		return scalar(t, databaseURL, "SELECT state || '/' || reason FROM pr_checks WHERE repository_id = $1 AND pull_number = $2", bootRepoID, pull)
	}
	receipt := func(pull int) string {
		t.Helper()
		return scalar(t, databaseURL, "SELECT state || '/' || reason FROM pr_receipt_checks WHERE repository_id = $1 AND pull_number = $2", bootRepoID, pull)
	}
	finish := func(plan execution.Plan, pull int, tests []execution.Test) {
		t.Helper()
		jobs, err := store.Execution().HeadJobs(t.Context(), bootRepoID, pull, map[int]string{4: bootHead, 5: cjHead2}[pull], plan)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("jobs of pull %d: %v, error %v", pull, jobs, err)
		}
		completeOnPostgreSQL(t, store.Execution(), jobs[0], tests, clock)
	}

	cycle(clock, map[int]string{4: bootHead, 5: cjHead2})
	step()
	if got := check(4); got != "in_progress/canonical-pending" {
		t.Fatalf("pull 4 is %s, want in_progress/canonical-pending while its canonical run is pending", got)
	}
	if got := check(5); got != "in_progress/approval-missing" {
		t.Fatalf("pull 5 is %s, want in_progress/approval-missing", got)
	}
	if got := receipt(5); got != "in_progress/receipt-running" {
		t.Fatalf("the receipt of pull 5 is %s, want in_progress/receipt-running", got)
	}
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM execution_jobs WHERE state = 'pending'"); got != "3" {
		t.Fatalf("%s pending jobs, want the canonical job of each pull request and the receipt job of pull 5", got)
	}

	finish(execution.PlanCanonical, 4, canonicalTests("pass", "skipped"))
	finish(execution.PlanReceipt, 5, receiptTests("detects", "inert"))
	step()
	if got := check(4); got != "success/unchanged" {
		t.Fatalf("pull 4 is %s, want success/unchanged once its canonical run completed", got)
	}
	if got := receipt(5); got != "success/receipt-complete" {
		t.Fatalf("the receipt of pull 5 is %s, want success/receipt-complete", got)
	}
	if got := scalar(t, databaseURL, "SELECT payload->'counts'->>'detects' FROM outbox WHERE kind = 'github_receipt_check' ORDER BY created_at DESC, key DESC LIMIT 1"); got != "1" {
		t.Fatalf("the latest Receipt check message counts %s DETECTS, want 1", got)
	}

	cycle(clock.Add(time.Second), map[int]string{4: bootHead}) // pull 5 closes: its canonical job is still pending
	step()
	if got := scalar(t, databaseURL, "SELECT count(*)::text FROM execution_jobs WHERE state = 'cancelled' AND reason = 'closed' AND pull_number = 5"); got != "1" {
		t.Fatalf("%s jobs cancelled for a closed pull request, want the one pending canonical job of pull 5", got)
	}
}

// completeOnPostgreSQL leases the oldest ready jobs until it has job, which it completes with the tests; the jobs it leased on the way stay leased.
func completeOnPostgreSQL(t *testing.T, store execution.Store, job execution.Job, tests []execution.Test, now time.Time) {
	t.Helper()
	var leased execution.Job
	for lease := int64(0); leased.ID != job.ID; lease++ {
		var found bool
		var err error
		leased, found, err = store.Lease(t.Context(), execution.LeaseRequest{Now: now, LeaseID: fmt.Sprintf("%032x", job.ID*100+lease), JobTimeout: time.Hour, GitHubWait: time.Hour},
			func(context.Context, execution.Job) error { return nil })
		if err != nil || !found {
			t.Fatalf("lease for job %d: found %v, error %v", job.ID, found, err)
		}
	}
	result := execution.Result{LeaseID: leased.LeaseID, Tests: tests, Report: execution.Report{Status: execution.StateComplete, ExecutorVersion: "0.4.0-test",
		ExecutorSHA256: artifact.Hash([]byte("binary")), ImageDigest: artifact.Hash([]byte("image")), Toolchain: "go1.25.1", Runtime: "podman 5.5.0",
		StartedAt: now.Add(-2 * time.Minute), EndedAt: now.Add(-time.Minute), BodyDigest: artifact.Hash([]byte(fmt.Sprint("body", job.ID)))}}
	if _, err := store.Complete(t.Context(), job.ID, result, now, time.Hour); err != nil {
		t.Fatalf("complete job %d: %v", job.ID, err)
	}
}
