// Package executiontest provides an in-memory execution.Store and execution.ReceiptLedger and the conformance suite that every
// implementation must pass.
package executiontest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// Env is what the conformance suite needs: the stores under test, the command ledger that Store writes its entries in, the keys of
// the committed outbox messages (the Receipt check messages and the acknowledgments), a way to end a pending message as failed at a
// given time, and the stored approval receipt and unblock rows of a comment.
type Env struct {
	Store       execution.Store
	Receipts    execution.ReceiptLedger
	Ledger      commands.Store
	OutboxKeys  func(ctx context.Context) ([]string, error) // sorted
	FailMessage func(ctx context.Context, key string, finishedAt time.Time) error
	Gate        func(ctx context.Context, repositoryID, commentID int64) (execution.ApprovalGate, bool, error)
	Unblocks    func(ctx context.Context, repositoryID, commentID int64) ([]int64, error) // sorted job ids
}

// The numbers of the job lifecycle, written out so that the suite pins the contract and not the constants of the package.
const (
	leaseTTL      = 60 * time.Second
	firstBackoff  = 30 * time.Second
	secondBackoff = 2 * time.Minute
	githubBackoff = time.Minute
	jobTimeout    = 10 * time.Minute
	githubWait    = 10 * time.Minute
	leaseWait     = 5 * time.Minute
	maxTests      = 10000
)

var origin = time.Date(2026, 10, 26, 12, 0, 0, 123456000, time.UTC) // at microsecond precision, which is all PostgreSQL stores

// at is the time d after the origin.
func at(d time.Duration) time.Time { return origin.Add(d) }

func sha(n int) string { return fmt.Sprintf("%040x", n) }

func digest(s string) artifact.Digest { return artifact.Hash([]byte(s)) }

// canonicalSpec is a canonical job for the head numbered n.
func canonicalSpec(n int) execution.Spec {
	return execution.Spec{RepositoryID: 10, PullNumber: 1, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: sha(n), GoverningDigest: digest("manifest"),
		GoverningSource: "V1", DeclarationDigest: digest("declaration"), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff}
}

// receiptSpec is a receipt job for the head numbered n, with its merge base.
func receiptSpec(n int) execution.Spec {
	s := canonicalSpec(n)
	s.Plan, s.BaseSHA, s.GoverningSource = execution.PlanReceipt, sha(1000+n), "P1-R2"
	s.CanonicalLevel, s.ReceiptLevel = domain.CanonicalLevelOff, domain.ReceiptLevelAdvisory
	s.UnapprovedPaths, s.ChangedPaths, s.ChangedTotal = []string{"docs/unapproved_test.go"}, []string{"a_test.go", "pkg/b_test.go"}, 2
	return s
}

// keyOf is the key of a job: the SHA-256 of six lines, each ended by a newline.
func keyOf(s execution.Spec) string {
	lines := []string{string(s.Plan), s.Profile, s.HeadSHA, s.BaseSHA, s.GoverningDigest.String(), s.DeclarationDigest.String()}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

type env struct {
	t      *testing.T
	ctx    context.Context
	env    Env
	s      execution.Store
	leases int
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) requireErr(err, want error) {
	e.t.Helper()
	if !errors.Is(err, want) {
		e.t.Fatalf("error = %v; want %v", err, want)
	}
}

// enqueue creates a pending job at the time now and requires that it is a new one.
func (e *env) enqueue(s execution.Spec, now time.Duration) execution.Job {
	e.t.Helper()
	job, created, err := e.s.Enqueue(e.ctx, execution.NewJob{Spec: s, At: at(now)})
	e.must(err)
	if !created {
		e.t.Fatalf("Enqueue of head %s returned the existing job %d; want a new one", s.HeadSHA, job.ID)
	}
	return job
}

// enqueueFinal creates a job that is already unavailable.
func (e *env) enqueueFinal(s execution.Spec, reason execution.Reason, now time.Duration) execution.Job {
	e.t.Helper()
	job, created, err := e.s.Enqueue(e.ctx, execution.NewJob{Spec: s, Unavailable: reason, At: at(now)})
	e.must(err)
	if !created {
		e.t.Fatalf("Enqueue of head %s returned the existing job %d; want a new one", s.HeadSHA, job.ID)
	}
	return job
}

// leaseID is the n-th lease id that newLeaseID hands out.
func (e *env) leaseID(n int) string { return fmt.Sprintf("%032x", n) }

func (e *env) newLeaseID() string {
	e.leases++
	return e.leaseID(e.leases)
}

// leaseRequest asks for a job at the time now, with a lease id that was not used.
func (e *env) leaseRequest(now time.Duration) execution.LeaseRequest {
	return execution.LeaseRequest{Now: at(now), LeaseID: e.newLeaseID(), JobTimeout: jobTimeout, GitHubWait: githubWait}
}

// trees is the Prepare of a lease whose reads all succeed.
func trees(context.Context, execution.Job) error { return nil }

// lease asks for a job at the time now, and reads the trees of the job that it finds.
func (e *env) lease(now time.Duration) (execution.Job, bool) {
	e.t.Helper()
	job, found, err := e.s.Lease(e.ctx, e.leaseRequest(now), trees)
	e.must(err)
	return job, found
}

// mustLease leases the job with this id at the time now.
func (e *env) mustLease(id int64, now time.Duration) execution.Job {
	e.t.Helper()
	job, found := e.lease(now)
	if !found || job.ID != id {
		e.t.Fatalf("Lease at %v = job %d, found %v; want job %d", now, job.ID, found, id)
	}
	return job
}

func (e *env) get(id int64) execution.Job {
	e.t.Helper()
	job, found, err := e.s.Job(e.ctx, id)
	e.must(err)
	if !found {
		e.t.Fatalf("Job(%d) found nothing", id)
	}
	return job
}

// requireJob requires every field of the stored job to be that of want.
func (e *env) requireJob(want execution.Job) {
	e.t.Helper()
	if diff := diffValues(reflect.ValueOf(e.get(want.ID)), reflect.ValueOf(want), "job"); diff != "" {
		e.t.Fatalf("job %d differs: %s", want.ID, diff)
	}
}

func (e *env) requireNoResult(id int64) {
	e.t.Helper()
	if got, found, err := e.s.Result(e.ctx, id); err != nil || found {
		e.t.Fatalf("Result(%d) = %+v, found %v, error %v; want no results row", id, got, found, err)
	}
	if tests, err := e.s.Tests(e.ctx, id); err != nil || len(tests) != 0 {
		e.t.Fatalf("Tests(%d) = %d rows, error %v; want none", id, len(tests), err)
	}
}

func (e *env) requireOutbox(want ...string) {
	e.t.Helper()
	got, err := e.env.OutboxKeys(e.ctx)
	e.must(err)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		e.t.Fatalf("outbox keys = %v; want %v", got, want)
	}
}

// diffValues names the first field in which two values differ, or is empty. Times are compared as instants, and an empty slice
// equals a nil one.
func diffValues(got, want reflect.Value, path string) string {
	switch got.Type() {
	case reflect.TypeOf(time.Time{}):
		if g, w := got.Interface().(time.Time), want.Interface().(time.Time); !g.Equal(w) {
			return fmt.Sprintf("%s = %v; want %v", path, g, w)
		}
		return ""
	case reflect.TypeOf(artifact.Digest{}):
		if g, w := got.Interface().(artifact.Digest), want.Interface().(artifact.Digest); g != w {
			return fmt.Sprintf("%s = %s; want %s", path, g, w)
		}
		return ""
	}
	switch got.Kind() {
	case reflect.Struct:
		for i := range got.NumField() {
			if diff := diffValues(got.Field(i), want.Field(i), path+"."+got.Type().Field(i).Name); diff != "" {
				return diff
			}
		}
		return ""
	case reflect.Slice:
		if got.Len() == 0 && want.Len() == 0 {
			return ""
		}
	}
	if !reflect.DeepEqual(got.Interface(), want.Interface()) {
		return fmt.Sprintf("%s = %v; want %v", path, got.Interface(), want.Interface())
	}
	return ""
}

// RunConformance runs the execution conformance suite against the environments made by newEnv.
func RunConformance(t *testing.T, newEnv func(*testing.T) Env) {
	cases := []struct {
		name string
		run  func(*env)
	}{
		{"Enqueue creates a pending job with the key of its six lines", jbEnqueue},
		{"Enqueue is idempotent by key among the current jobs", jbEnqueueIdempotent},
		{"a cancelled job, and one unavailable for executor-not-configured, does not block a new one with its key", jbKeyFreedBy},
		{"a job unavailable for no-executor does not block a new one, and keeps its unblock", jbKeyFreedByNoExecutor},
		{"a receipt job for no-merge-base, executor-not-configured or github-unavailable is created unavailable with no base", jbCreatedFinal},
		{"Enqueue refuses a job that breaks a constraint and writes nothing", jbEnqueueInvalid},
		{"Lease grants the oldest ready job and sets its lease", jbLeaseGrant},
		{"Lease takes the oldest first by creation time and id, and honors not_before", jbLeaseOrder},
		{"Lease is exclusive under concurrency", jbLeaseConcurrent},
		{"a job whose lease expired is queued again with its backoff and the third failure is final", jbLeaseExpiry},
		{"Lease goes on to the next candidate after it expired a lease", jbLeaseNextCandidate},
		{"Lease gives a ready job to prepare, and nil grants the lease", jbLeasePrepare},
		{"a GitHub failure of prepare is recorded as GitHubFailure records it, and ends the call", jbLeasePrepareGitHub},
		{"a GitHub failure of prepare a whole wait after the first ends the job", jbLeasePrepareGitHubBound},
		{"a job that prepare finds too large is unavailable at once, and the call goes on", jbLeasePrepareTooLarge},
		{"any other failure of prepare ends the call with nothing written", jbLeasePrepareAbort},
		{"a failed attempt with a retried reason is queued again, and any other reason is final at once", jbRetriedReasons},
		{"a GitHub failure counts no failure, keeps the job pending and is bounded by the wait", jbGitHubFailure},
		{"a leased job is never ended by the GitHub bound and a lease does not clear github_since", jbGitHubLeased},
		{"an executor that gives a job back as github-unavailable records the failure, and past the wait ends it with an executor row", jbGitHubGiveBack},
		{"no-executor reaches every pending job only when no executor was seen for the wait", jbNoExecutor},
		{"Heartbeat extends the lease up to the deadline and refuses a lease that is not current", jbHeartbeat},
		{"Cancel ends an open job, does nothing to a final one, and Expire counts the jobs it changed", jbCancelAndCounts},
		{"the reads answer by job, key and head", jbReads},
		{"Complete stores the result, the tests and the state together", rsComplete},
		{"Complete queues a job again without storing a result", rsRequeue},
		{"the same body twice is stored once, and another body for a finished job is a conflict", rsSameBody},
		{"Complete refuses a lease that is not current", rsLease},
		{"a canonical job and a receipt job each refuse the rows of the other plan", rsPlanRules},
		{"Complete refuses an invalid result and writes nothing", rsInvalid},
		{"a result holds at most 10,000 tests", rsBound},
		{"the Receipt check ledger follows the rules of the check ledger, with neutral and unblocked", rcRules},
		{"concurrent Publish of the Receipt check writes one message for one generation", rcConcurrent},
		{"an invalid Receipt check is not recorded and a failing message rolls it back", rcInvalid},
		{"an unblock is written with its ledger entry or not at all, and binds to the job", ubUnblock},
		{"an approval receipt is written with its ledger entry or not at all", apApproval},
		{"a canceled context writes nothing", jbCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.run(&env{t: t, ctx: t.Context(), env: e, s: e.Store})
		})
	}
}

// requireInvalid requires every mutation of a valid request to be ErrInvalidRequest for the operation attempt. Each mutation starts
// from a request of its own, so that none builds on another.
func requireInvalid[T any](e *env, valid func() T, bad map[string]func(*T), attempt func(T) error) {
	e.t.Helper()
	for name, mutate := range bad {
		x := valid()
		mutate(&x)
		e.t.Run(name, func(t *testing.T) {
			sub := *e
			sub.t = t
			sub.requireErr(attempt(x), execution.ErrInvalidRequest)
		})
	}
}

func diff(got, want any, path string) string {
	return diffValues(reflect.ValueOf(got), reflect.ValueOf(want), path)
}
