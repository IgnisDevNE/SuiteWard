package execution

import (
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The rules of the comments of a head (docs/contracts/runtime.md, "The comments of a head" and "Not responding"): when the set of runs that a head wants is final,
// which jobs it names, what each run holds back, what the next step is and what the lead says. They read counts and codes only: no name, no path and no text of a
// repository, so nothing an executor sends reaches a comment through them.

var (
	// ErrInvalidJobState reports a job state that these rules do not read: a cancelled job is never part of a set.
	ErrInvalidJobState = errors.New("invalid job state")
	// ErrInvalidNext reports a next step that is not one of the five.
	ErrInvalidNext = errors.New("invalid next step")
)

// JobState is the state of a job that is part of a set. A cancelled job is not one: its binding is no longer wanted.
type JobState uint8

// The states of a job in a set. The last two are final: a job that is unavailable has an outcome too, whatever its reason.
const (
	JobPending JobState = iota + 1
	JobLeased
	JobComplete
	JobUnavailable
)

var jobStateNames = map[JobState]string{JobPending: "pending", JobLeased: "leased", JobComplete: "complete", JobUnavailable: "unavailable"}

// ParseJobState reads pending, leased, complete or unavailable, exactly as spelled; cancelled is an error.
func ParseJobState(s string) (JobState, error) {
	return parseEnum(jobStateNames, s, ErrInvalidJobState)
}

// String returns the stored name of the state, or empty for a value that is not one.
func (s JobState) String() string { return jobStateNames[s] }

// JobReasonGitHubUnavailable is the reason of a pending job that waits on GitHub and not on the runner.
const JobReasonGitHubUnavailable = "github-unavailable"

// Job is the part of a job that the comments of a head read. The zero Job is a plan that has no job yet.
type Job struct {
	ID        int64
	State     JobState
	Reason    string          // the code stored with the job: why it is unavailable, or why its last attempt failed; empty for none
	Governing artifact.Digest // the manifest that governs the job
	Unblocked bool            // the owner unblocked the job: an unblock row since the current enrollment names it
}

// HasOutcome says that the job is complete or unavailable.
func (j Job) HasOutcome() bool { return false }

// WaitsForRunner says that the job waits for the test runner: it is leased, or pending for a reason other than a failing read of GitHub, which has its own bound.
func (j Job) WaitsForRunner() bool { return false }

// HeadReason is the reason that the Contract check of a head would have before the canonical gate decides, as far as the comments need it.
type HeadReason uint8

// The head reasons.
const (
	HeadReasonGate            HeadReason = iota + 1 // unchanged or ready: the check would pass, so the canonical gate acts on it
	HeadReasonApprovalMissing                       // the approval is what is missing
	HeadReasonOther                                 // any other reason, which keeps its precedence over the gate
)

// Set is the runs of one head that a comment reports, with what the rules read of the head. The caller fills it from the stored jobs, the unblocks, the counts of the
// complete runs and the policy that was read. CanonicalLevel and ReceiptLevel are zero when the policy was not read, and then no set is final.
type Set struct {
	CanonicalLevel CanonicalLevel
	ReceiptLevel   ReceiptLevel
	ReceiptApplies bool // the receipt applies to the revision of the head
	ReceiptDecided bool // the run decided whether it applies: a head with no revision yet, and a merge base that GitHub cannot give yet, are not decided
	HeadReason     HeadReason

	// The job that each plan has in the set, the zero Job for none. A plan that is not wanted is ignored whatever its job.
	CanonicalJob, ReceiptJob Job
	// Earlier says that the canonical job is not the one the gate reads now: it is the job that came before the approval, and the head has another under the
	// manifest of the revision.
	Earlier bool

	// The tests of a complete run by state, the zero value for a run that is not complete, and the weakened count (0 when unknown).
	CanonicalCounts CanonicalCounts
	ReceiptCounts   ReceiptCounts
	Weakened        int
}

// Final says that the set is ready to be reported: the policy was read, the run decided whether the receipt applies, at least one run is wanted, and every wanted
// run has a job that is complete or unavailable.
func (s Set) Final() bool { return false }

// JobPair is the jobs of a set, 0 for a plan with none. Its String is the end of the outbox key of a comment: <canonical job>-<receipt job>.
type JobPair struct{ Canonical, Receipt int64 }

// String is the pair as the key writes it.
func (p JobPair) String() string { return "" }

// Jobs are the jobs of the set that a results comment names, 0 for a plan that is not wanted.
func (s Set) Jobs() JobPair { return JobPair{} }

// Waiting is what a head that reads not responding says: the jobs that wait for the runner, and whether the unblock is offered.
type Waiting struct {
	Jobs    JobPair
	Unblock bool
}

// NotResponding says whether the head reads not responding: the runner is silent (the caller decides it from its clock) and a wanted job of the head waits for it.
// The unblock is offered when a job that waits also holds something back.
func (s Set) NotResponding(silent bool) (Waiting, bool) { return Waiting{}, false }

// Lead says what the first sentence of a results comment is about.
type Lead uint8

// The leads, the first that matches. The zero Lead is no set: nothing is wanted.
const (
	LeadNotRun    Lead = iota + 1 // every wanted run is unavailable
	LeadPartial                   // one wanted run is complete and another is unavailable
	LeadAttention                 // a complete run has a failing, missing or flaky test, or an unparsed file, or a test is weakened
	LeadClean                     // nothing needs attention
)

// Lead is the lead of the set.
func (s Set) Lead() Lead { return 0 }

// Holds says which runs hold something back, which is only where the gate of the run acts now.
type Holds struct{ Canonical, Receipt bool }

// Holds says what the set holds back: the canonical run holds the head under canonical required when the head would otherwise pass and its job is the one the gate
// reads; the receipt run holds the approve under required, and under before-approval while it is not final.
func (s Set) Holds() Holds { return Holds{} }

// Next is the next step that a results comment names.
type Next uint8

// The next steps, in the order that they are decided.
const (
	NextUnblock Next = iota + 1
	NextBlocked
	NextApprove
	NextFix
	NextNone
)

var nextNames = map[Next]string{NextUnblock: "unblock", NextBlocked: "blocked", NextApprove: "approve", NextFix: "fix", NextNone: "none"}

// ParseNext reads unblock, blocked, approve, fix or none, exactly as spelled.
func ParseNext(s string) (Next, error) { return parseEnum(nextNames, s, ErrInvalidNext) }

// String returns the name of the step as the payload spells it, or empty for a value that is not one.
func (n Next) String() string { return nextNames[n] }

// Next decides the next step of the set, the first that matches.
func (s Set) Next() (Next, error) { return 0, nil }

// EarlierCanonical is the canonical job that came before an approval. gate is the canonical job that the gate reads now; when it is governed by another manifest
// than the canonical version's, the head is approved, and the earlier job is the newest of jobs (the canonical jobs of the head, oldest first, none cancelled) that
// has an outcome and is governed by canonicalManifest. ok is false for a head that is not approved and for one with no such job.
func EarlierCanonical(jobs []Job, gate Job, canonicalManifest artifact.Digest) (job Job, ok bool) {
	return Job{}, false
}

// ResultsSets are the sets whose results comment is written this pass, in order: the earlier set of an approved head when it is final, and the later set when it is
// final and has something to say. earlier is the zero Set for a head with no earlier set. The later set is written unless the leads of both sets are "nothing needs
// attention", so that a later set corrects an earlier one that reported something, a head that is clean before and after keeps one comment, and a head with no
// earlier set always gets its comment.
func ResultsSets(earlier, later Set) []Set { return nil }
