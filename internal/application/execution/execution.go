// Package execution is the port of the execution ledger of M2.1: the jobs an executor leases, the results it reports,
// the ledger of the SuiteWard / Receipt check, the owner's unblocks and the receipt state recorded at an approve
// (docs/contracts/persistence.md, "Execution (M2.1; normative)"; ADR 0028).
//
// It records evidence, never authority. Nothing here is read by consent or promotion, and the stores check the shape of what
// they are given (codes, sizes, the plan against the rows) without re-running a decision of internal/domain/execution.
package execution

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

var (
	// ErrInvalidRequest marks a request the store refuses before it writes anything: a value that breaks a constraint of the
	// schema, a job in a state the operation does not accept, or rows that do not belong together.
	ErrInvalidRequest = errors.New("execution: invalid request")
	// ErrNotFound marks a job that does not exist.
	ErrNotFound = errors.New("execution: not found")
	// ErrLeaseLost marks a heartbeat or a result whose lease is not the job's current one, has expired, or whose job is no longer leased.
	ErrLeaseLost = errors.New("execution: the lease was lost")
	// ErrConflict marks a result for a finished job whose body is not the stored one.
	ErrConflict = errors.New("execution: the job finished with another result")
	// ErrGitHubUnavailable is what a Prepare function returns when GitHub could not be read for the job (a failed read, a refused budget
	// or a secondary limit), and what Lease then returns after it recorded the failure: the executor API answers 502.
	ErrGitHubUnavailable = errors.New("execution: GitHub could not be read for the job")
	// ErrTooLarge is what a Prepare function returns for a job whose commit is too large to lease: the job is unavailable for too-large.
	ErrTooLarge = errors.New("execution: the job is too large to lease")
	// ErrJobChanged marks an unblock that names a job whose state or lease id is no longer what the caller observed (it completed, was cancelled, or an
	// executor leased it in between): nothing is written, and the caller reads the head again.
	ErrJobChanged = errors.New("execution: the job changed since it was observed")
)

// The durations of the job lifecycle (docs/contracts/runtime.md, "Job lifecycle"; A0's numbers).
const (
	LeaseTTL      = 60 * time.Second // how long a lease or a heartbeat extends the lease, never beyond the deadline
	GitHubBackoff = time.Minute      // how long a job waits after a GitHub failure
	MaxFailures   = 3                // the job is final at the third failure
	MaxTests      = 10000            // the tests of one result
)

// Plan says what a job runs: the governing tests on the head, or the tests a revision adds or changes on the head and on the base.
type Plan string

// The plans.
const (
	PlanCanonical Plan = "canonical"
	PlanReceipt   Plan = "receipt"
)

// State is the state of a job. The last three are final.
type State string

// The states of a job.
const (
	StatePending     State = "pending"
	StateLeased      State = "leased"
	StateComplete    State = "complete"
	StateUnavailable State = "unavailable"
	StateCancelled   State = "cancelled"
)

// Reason is a code that says why a job is unavailable or cancelled, or why its last attempt failed.
type Reason string

// The reasons of a job that is unavailable, or whose last attempt failed (docs/contracts/runtime.md, "Unavailable reasons").
const (
	ReasonExecutorError      Reason = "executor-error"
	ReasonSandboxUnavailable Reason = "sandbox-unavailable"
	ReasonArchiveUnavailable Reason = "archive-unavailable"
	ReasonLeaseExpired       Reason = "lease-expired"
	ReasonTimeout            Reason = "timeout"
	ReasonGitHubUnavailable  Reason = "github-unavailable"
	ReasonNoExecutor         Reason = "no-executor"
	ReasonFetchFailed        Reason = "fetch-failed"
	ReasonInstallFailed      Reason = "install-failed"
	ReasonResourceLimit      Reason = "resource-limit"
	ReasonToolchainMismatch  Reason = "toolchain-mismatch"
	ReasonSourceMismatch     Reason = "source-mismatch"
	ReasonTooLarge           Reason = "too-large"
	ReasonProfileUnsupported Reason = "profile-unsupported"
	ReasonNotConfigured      Reason = "executor-not-configured"
	ReasonNoMergeBase        Reason = "no-merge-base"
)

// The reasons of a cancelled job: its binding is no longer wanted.
const (
	ReasonHeadMoved Reason = "head-moved"
	ReasonClosed    Reason = "closed"
	ReasonObsolete  Reason = "obsolete"
)

// Spec is what identifies a job: a plan on a head, for one binding. The store derives the job's key from it.
type Spec struct {
	RepositoryID      int64
	PullNumber        int
	Plan              Plan
	Profile           string // go
	HeadSHA           string
	BaseSHA           string // the merge base; empty for a canonical job and for a receipt job created already unavailable
	GoverningDigest   artifact.Digest
	GoverningSource   domain.ManifestSource
	DeclarationDigest artifact.Digest
	CanonicalLevel    domain.CanonicalLevel // the effective levels when the job was created
	ReceiptLevel      domain.ReceiptLevel
	UnapprovedPaths   []string // "not evaluated: pattern not approved yet", at most 100; display only
	ChangedPaths      []string // the files the revision adds or changes under the canonical include, at most 100; display only
	ChangedTotal      int      // how many such files there are
}

// NewJob is a job to create: pending, or already unavailable for a reason of service (no-merge-base, executor-not-configured or
// github-unavailable), when Unavailable is set.
type NewJob struct {
	Spec
	Unavailable Reason
	At          time.Time // the creation time; also the first time a lease may be granted and, when unavailable, the end
}

// Job is a stored job. A zero time is an absent one.
type Job struct {
	Spec
	ID             int64
	Key            string // the SHA-256 of the six lines of the job lifecycle, lower-case hexadecimal
	State          State
	Reason         Reason // empty for a complete job and for an open one that failed nothing
	Attempts       int    // the leases granted
	Failures       int    // the attempts that an executor leased and lost or failed with a retried reason
	GitHubSince    time.Time
	NotBefore      time.Time
	LeaseID        string // the latest lease; it stays when the lease ends
	LeaseExpiresAt time.Time
	LeasedAt       time.Time
	DeadlineAt     time.Time
	CreatedAt      time.Time
	FinishedAt     time.Time
}

// LeaseRequest asks for the oldest job that is ready. LeaseID is new for each grant (32 lower-case hexadecimal digits); JobTimeout
// is the time the run of the job may last (EXECUTOR_JOB_TIMEOUT); GitHubWait is EXECUTOR_GITHUB_WAIT, which bounds a job whose
// tree reads keep failing.
type LeaseRequest struct {
	Now        time.Time
	LeaseID    string
	JobTimeout time.Duration
	GitHubWait time.Duration
}

// Prepare is the read that a lease needs before it is granted (the trees of the commits of the job). Lease calls it with a ready job
// while the job is held, so that two leases never read for one job, and it must not call the store. It returns nil to grant the lease,
// an error that wraps ErrGitHubUnavailable or ErrTooLarge, or another error, which ends the call with nothing written.
type Prepare func(ctx context.Context, job Job) error

// ExpireParams are the times that the sweep applies. LastSeenAt is the time an executor was last seen (the start time at
// startup); LeaseWait and GitHubWait are EXECUTOR_LEASE_WAIT and EXECUTOR_GITHUB_WAIT.
type ExpireParams struct {
	Now        time.Time
	LastSeenAt time.Time
	LeaseWait  time.Duration
	GitHubWait time.Duration
}

// Marker is a line that the receipt run flagged. Kind is one of the kinds of the executor API.
type Marker struct {
	Kind string
	Path string
	Line int
}

// Origin says who wrote a result: the executor that ran the job, or the service when it ended a pending job on the GitHub bound.
type Origin string

// The origins.
const (
	OriginExecutor Origin = "executor"
	OriginService  Origin = "service"
)

// The kinds of a test row.
const (
	KindDeclared = "declared"
	KindAdded    = "added"
	KindChanged  = "changed"
)

// The reasons that a test is excluded: a build constraint leaves its file out of the sandbox build, or its file is under a nested go.mod,
// which the root invocation cannot run.
const (
	ExcludedBuildConstraint = "build-constraint"
	ExcludedNestedModule    = "nested-module"
)

// Test is one row of a complete result. HeadRuns and BaseRuns are the observations in run order (pass, fail, build-error,
// skip or absent); State is the lower-case state that was shown (pass, fail, skipped, missing, flaky, excluded for a canonical
// run, and excluded, missing, flaky, skipped, fails, new_api, detects, inert for a receipt). The service derives it with
// internal/domain/execution and the store keeps it.
type Test struct {
	Package        string
	Name           string
	File           string
	Kind           string
	ExcludedReason string
	HeadRuns       []string
	BaseRuns       []string // nil for a canonical run and for an excluded test
	State          string
}

// Report is what an executor says about a run, or what the service says about a job it ended itself: the stored columns of the
// results row, apart from the tests.
type Report struct {
	Status          State // complete or unavailable
	Reason          Reason
	ExecutorVersion string
	ExecutorSHA256  artifact.Digest
	ImageDigest     artifact.Digest
	Toolchain       string
	Runtime         string
	StartedAt       time.Time
	EndedAt         time.Time
	BodyDigest      artifact.Digest // of the request body, so that the same result sent again is recognized
	Markers         []Marker
	Submodules      []string
	Unparsed        []string // the changed test files that go cannot parse, which list no test (a receipt only), at most 100 clean relative paths ending _test.go
}

// Result is a request to store the result of a leased job.
type Result struct {
	LeaseID string
	Report
	Tests []Test
}

// StoredResult is a row of execution_results.
type StoredResult struct {
	JobID      int64
	Origin     Origin
	ReceivedAt time.Time
	Report
}

// Outcome is what a result did: the state the job is in afterwards (pending when it was queued again), and whether the job had
// already finished with the same body, so that nothing was written.
type Outcome struct {
	State    State
	Replayed bool
}

// UnblockJob is a job that an unblock lifts, with what the caller observed of it: its state (unavailable, pending or leased) and its lease id, empty for a job
// that was never leased. The store ends an open job only if it is still as observed.
type UnblockJob struct {
	ID      int64
	State   State
	LeaseID string
}

// RunState is the state of the receipt at an approve.
type RunState string

// The run states of an approval receipt.
const (
	RunNone          RunState = "none" // no job yet
	RunPending       RunState = "pending"
	RunComplete      RunState = "complete"
	RunUnavailable   RunState = "unavailable"
	RunNotApplicable RunState = "not-applicable"
)

// Summary says whether the receipt met the mode of an approve.
type Summary string

// The summaries of a complete receipt.
const (
	SummaryNone   Summary = ""
	SummaryMet    Summary = "met"
	SummaryNotMet Summary = "not-met"
)

// ApprovalGate is the receipt state at the moment of an approve that reached the gate, whether it was refused or not.
type ApprovalGate struct {
	HeadSHA      string
	ReceiptLevel domain.ReceiptLevel // advisory, before-approval or required
	RunState     RunState
	JobID        int64 // the job that the state shows; 0 for none and not-applicable
	Summary      Summary
	Unblocked    bool // the gate passed an unavailable job because the owner unblocked it
}

// Store is the execution ledger. Implementations run the shared conformance suite in executiontest. Every time is truncated to
// the microseconds that PostgreSQL stores, and every operation that takes a time takes it from the caller: the store never reads
// a clock.
type Store interface {
	// Enqueue creates the job, pending or already unavailable, and reports true. A current job with the same key (one that is not
	// cancelled, and not unavailable for executor-not-configured or no-executor) is returned instead, with false, and nothing is
	// written. A job that breaks a constraint is ErrInvalidRequest.
	Enqueue(ctx context.Context, n NewJob) (Job, bool, error)
	// Lease grants the oldest ready job (by creation time, then id): pending with its not_before reached, or leased with an expired
	// lease. An expired lease counts as a failure (lease-expired, or timeout past the deadline) first, and the call goes on to the
	// next candidate. It returns false when nothing is ready. A ready job is first given to prepare, which is required (nil is
	// ErrInvalidRequest): when it fails with ErrGitHubUnavailable the store records the failure as GitHubFailure does, with no attempt,
	// and Lease returns an error that wraps ErrGitHubUnavailable and goes no further; when it fails with ErrTooLarge the job becomes
	// unavailable for too-large, with no attempt and no results row, and the call goes on to the next candidate; any other error ends
	// the call with nothing written.
	Lease(ctx context.Context, req LeaseRequest, prepare Prepare) (Job, bool, error)
	// Heartbeat extends the lease of a leased job, never beyond its deadline, and returns the new expiry. ErrNotFound for a job
	// that does not exist, ErrLeaseLost for any other job than one that is leased with this lease and not yet expired.
	Heartbeat(ctx context.Context, jobID int64, leaseID string, now time.Time) (time.Time, error)
	// Complete stores the result of a leased job, or queues it again (see the runtime contract, "The executor API"). A job that
	// has finished answers the same body with its outcome, whatever the lease, and another body with ErrConflict.
	Complete(ctx context.Context, jobID int64, r Result, now time.Time, githubWait time.Duration) (Outcome, error)
	// GitHubFailure records that a read for a pending job failed: it counts no failure, keeps the job pending with the reason
	// github-unavailable and waits GitHubBackoff, and ends the job (with a results row of origin service) when the first failure is
	// older than githubWait. It returns the state of the job afterwards.
	GitHubFailure(ctx context.Context, jobID int64, now time.Time, githubWait time.Duration) (State, error)
	// Cancel moves a pending or leased job to cancelled (head-moved, closed or obsolete) and does nothing to a final job. It
	// reports whether it changed the job.
	Cancel(ctx context.Context, jobID int64, reason Reason, now time.Time) (bool, error)
	// Expire applies the expiry of leases, no-executor and the GitHub bound to every open job and returns the number of jobs it changed.
	Expire(ctx context.Context, p ExpireParams) (int, error)

	// Job reads a job by id.
	Job(ctx context.Context, jobID int64) (Job, bool, error)
	// CurrentJob reads the current job of a key.
	CurrentJob(ctx context.Context, key string) (Job, bool, error)
	// HeadJobs reads the jobs of a head for one plan, oldest first (a head can have more than one over its life).
	HeadJobs(ctx context.Context, repositoryID int64, pull int, headSHA string, plan Plan) ([]Job, error)
	// OpenJobs reads the pending and leased jobs of a repository, oldest first: what the checks job cancels when a pull request closes,
	// moves to another head or no longer wants the binding. It takes no lock.
	OpenJobs(ctx context.Context, repositoryID int64) ([]Job, error)
	// Result reads the results row of a job that has one.
	Result(ctx context.Context, jobID int64) (StoredResult, bool, error)
	// Tests reads the tests of a complete result, ordered by package then name.
	Tests(ctx context.Context, jobID int64) ([]Test, error)
	// Counts reads the number of jobs in each state, with zero for a state that has none.
	Counts(ctx context.Context) (map[State]int, error)

	// Unblocked reports whether a row names the job and its command entry was created at or after since (the current enrollment's
	// enrolled_at). The row binds to the job and not to the head.
	Unblocked(ctx context.Context, jobID int64, since time.Time) (bool, error)
	// RecordApproval records the ledger entry of an approve that reached the receipt gate (processed or receipt-refused) and its
	// gate record, with the acknowledgment when there is one, in one transaction. The same entry and gate again is success; another
	// entry or gate for the comment is commands.ErrConflict.
	RecordApproval(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, gate ApprovalGate) error
	// RecordUnblock records the ledger entry of an unblock (unblocked) and one unblock row for each lifted job, with the
	// acknowledgment when there is one, in one transaction. Each job must be of the pull and the head. The store locks the jobs in the
	// order of their ids and compares each with what the caller observed: one whose state or lease id differs fails the call with
	// ErrJobChanged and writes nothing. It then ends each open job (pending or leased) as unavailable for no-executor, as the sweep ends a
	// pending one, with finished_at and the decided_at of its row both at; a job that was unavailable already stays as it is. The store
	// does not judge whether a runner responds: the caller passes only the jobs it may end. The same entry, jobs and time again is
	// success; another entry, other jobs or another time for the comment is commands.ErrConflict.
	RecordUnblock(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, headSHA string, jobs []UnblockJob, at time.Time) error
}

// CheckState is the state published for the Receipt check of a head.
type CheckState string

// The states of the Receipt check.
const (
	CheckSuccess    CheckState = "success"
	CheckInProgress CheckState = "in_progress"
	CheckFailure    CheckState = "failure"
	CheckNeutral    CheckState = "neutral"
)

// ReceiptCheck is one decided state of the Receipt check for the head of a pull request.
type ReceiptCheck struct {
	RepositoryID int64
	PullNumber   int
	HeadSHA      string
	State        CheckState
	Reason       string // receipt-running, receipt-not-responding, receipt-failing, receipt-complete or receipt-not-run
	Reference    string // "P<n>-R<m>" or empty
	JobID        int64  // the receipt job that the state shows, 0 for none
	Unblocked    bool   // the owner unblocked the head's receipt job and it is unavailable
}

// ReceiptRecord is a stored ReceiptCheck with its publication bookkeeping.
type ReceiptRecord struct {
	ReceiptCheck
	HeadSince  time.Time
	Generation int64 // > 0
	DecidedAt  time.Time
}

// ReceiptLedger is the ledger of the Receipt check, with the rules of checks.Store: one row for each pull request, updated in
// place, with the publications in the outbox.
type ReceiptLedger interface {
	Current(ctx context.Context, repositoryID int64, pull int) (ReceiptRecord, bool, error)
	// Publish stores r for its pull. A new head, state, reason, reference or unblocked advances the generation by one and writes
	// message(generation) to the outbox in the same transaction. A result that changes none of them updates JobID and the decision
	// time only. A message that ended failed is sent again under the next generation once checks.RepublishCooldown has passed.
	Publish(ctx context.Context, r ReceiptCheck, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (ReceiptRecord, bool, error)
}
