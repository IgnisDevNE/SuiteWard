package execution

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

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
func (j Job) HasOutcome() bool { return j.State == JobComplete || j.State == JobUnavailable }

// WaitsForRunner says that the job waits for the test runner: it is leased, or pending for a reason other than a failing read of GitHub, which has its own bound.
func (j Job) WaitsForRunner() bool {
	return j.State == JobLeased || (j.State == JobPending && j.Reason != JobReasonGitHubUnavailable)
}

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
func (s Set) Final() bool {
	if s.CanonicalLevel == 0 || s.ReceiptLevel == 0 || !s.ReceiptDecided {
		return false
	}
	jobs := s.wantedJobs()
	return len(jobs) > 0 && !slices.ContainsFunc(jobs, func(j Job) bool { return !j.HasOutcome() })
}

func (s Set) wantsCanonical() bool { return s.CanonicalLevel != CanonicalLevelOff }

func (s Set) wantsReceipt() bool { return s.ReceiptLevel != ReceiptLevelOff && s.ReceiptApplies }

// wantedJobs are the jobs of the plans that the head wants, the canonical one first.
func (s Set) wantedJobs() []Job {
	var jobs []Job
	if s.wantsCanonical() {
		jobs = append(jobs, s.CanonicalJob)
	}
	if s.wantsReceipt() {
		jobs = append(jobs, s.ReceiptJob)
	}
	return jobs
}

// JobPair is the jobs of a set, 0 for a plan with none. Its String is the end of the outbox key of a comment: <canonical job>-<receipt job>.
type JobPair struct{ Canonical, Receipt int64 }

// String is the pair as the key writes it.
func (p JobPair) String() string {
	return strconv.FormatInt(p.Canonical, 10) + "-" + strconv.FormatInt(p.Receipt, 10)
}

// Jobs are the jobs of the set that a results comment names, 0 for a plan that is not wanted.
func (s Set) Jobs() JobPair {
	return JobPair{Canonical: idIf(s.wantsCanonical(), s.CanonicalJob), Receipt: idIf(s.wantsReceipt(), s.ReceiptJob)}
}

func idIf(wanted bool, j Job) int64 {
	if wanted {
		return j.ID
	}
	return 0
}

// Waiting is what a head that reads not responding says: the jobs that wait for the runner, and whether the unblock is offered.
type Waiting struct {
	Jobs    JobPair
	Unblock bool
}

// NotResponding says whether the head reads not responding: the runner is silent (the caller decides it from its clock) and a wanted job of the head waits for it.
// The unblock is offered when a job that waits also holds something back.
func (s Set) NotResponding(silent bool) (Waiting, bool) {
	canonical := s.wantsCanonical() && s.CanonicalJob.WaitsForRunner()
	receipt := s.wantsReceipt() && s.ReceiptJob.WaitsForRunner()
	if !silent || (!canonical && !receipt) {
		return Waiting{}, false
	}
	holds := s.Holds()
	return Waiting{
		Jobs:    JobPair{Canonical: idIf(canonical, s.CanonicalJob), Receipt: idIf(receipt, s.ReceiptJob)},
		Unblock: (canonical && holds.Canonical) || (receipt && holds.Receipt),
	}, true
}

// Lead says what the first sentence of a results comment is about.
type Lead uint8

// The leads, the first that matches. The zero Lead is no set: nothing is wanted, or the set is not final.
const (
	LeadNotRun    Lead = iota + 1 // every wanted run is unavailable
	LeadPartial                   // one wanted run is complete and another is unavailable
	LeadAttention                 // a complete run has a failing, missing or flaky test, or an unparsed file, or a test is weakened
	LeadClean                     // nothing needs attention
)

// Lead is the lead of the set. A set that is not final has none (the zero Lead), so that a waiting head never reads as one that needs no attention.
func (s Set) Lead() Lead {
	if !s.Final() {
		return 0
	}
	jobs := s.wantedJobs()
	unavailable := countJobs(jobs, JobUnavailable)
	switch {
	case unavailable == len(jobs):
		return LeadNotRun
	case unavailable > 0 && countJobs(jobs, JobComplete) > 0:
		return LeadPartial
	case s.needsFix() || s.Weakened > 0:
		return LeadAttention
	}
	return LeadClean
}

func countJobs(jobs []Job, state JobState) int {
	n := 0
	for _, j := range jobs {
		if j.State == state {
			n++
		}
	}
	return n
}

// needsFix says that a wanted run is complete and has a FAIL, FAILS, MISSING or FLAKY test, or a changed test file that does not parse.
func (s Set) needsFix() bool {
	return (s.wantsCanonical() && s.CanonicalJob.State == JobComplete && s.CanonicalCounts.Blocking() > 0) ||
		(s.wantsReceipt() && s.ReceiptJob.State == JobComplete && s.ReceiptCounts.Blocking() > 0)
}

// Holds says which runs hold something back, which is only where the gate of the run acts now.
type Holds struct{ Canonical, Receipt bool }

// Holds says what the set holds back: the canonical run holds the head under canonical required when the head would otherwise pass and its job is the one the gate
// reads; the receipt run holds the approve under required, and under before-approval while it is not final.
func (s Set) Holds() Holds {
	return Holds{
		Canonical: s.wantsCanonical() && !s.Earlier && s.CanonicalLevel == CanonicalLevelRequired && s.HeadReason == HeadReasonGate,
		Receipt:   s.wantsReceipt() && (s.ReceiptLevel == ReceiptLevelRequired || (s.ReceiptLevel == ReceiptLevelBeforeApproval && !s.ReceiptJob.HasOutcome())),
	}
}

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
func (s Set) Next() (Next, error) {
	holds := s.Holds()
	if (holds.Canonical && lost(s.CanonicalJob)) || (holds.Receipt && lost(s.ReceiptJob)) {
		return NextUnblock, nil
	}
	if s.HeadReason == HeadReasonApprovalMissing {
		blocked, err := s.approveBlocked()
		switch {
		case err != nil:
			return 0, err
		case blocked:
			return NextBlocked, nil
		}
		return NextApprove, nil
	}
	if s.needsFix() {
		return NextFix, nil
	}
	return NextNone, nil
}

// lost says that a run is unavailable and the owner has not unblocked it.
func lost(j Job) bool { return j.State == JobUnavailable && !j.Unblocked }

// approveBlocked says that every approve would be refused for its mode (mode-not-met) and would use its code: the receipt is required, complete and not empty, and
// none of fix, feature and refactor is met.
func (s Set) approveBlocked() (bool, error) {
	if !s.wantsReceipt() || s.ReceiptLevel != ReceiptLevelRequired || s.ReceiptJob.State != JobComplete || s.ReceiptCounts.Empty() {
		return false, nil
	}
	for _, mode := range []Mode{ModeFix, ModeFeature, ModeRefactor} {
		reason, err := s.ReceiptLevel.ApproveGate(RunComplete, s.ReceiptCounts, mode, false)
		if err != nil {
			return false, fmt.Errorf("decide the approve of mode %s: %w", mode, err)
		}
		if reason == ReasonNone {
			return false, nil
		}
	}
	return true, nil
}

// EarlierCanonical is the canonical job that came before an approval. gate is the canonical job that the gate reads now; when it is governed by another manifest
// than the canonical version's, the head is approved, and the earlier job is the newest of jobs (the canonical jobs of the head, oldest first, none cancelled) that
// has an outcome and is governed by canonicalManifest. ok is false for a head that is not approved (a gate with no job yet is not) and for one with no such job.
// A canonical version has a manifest and a job always has a governing one, so a missing digest is a caller that did not fill it, and is ErrNoGoverningDigest: two
// missing digests never compare equal.
func EarlierCanonical(jobs []Job, gate Job, canonicalManifest artifact.Digest) (job Job, ok bool, err error) {
	switch {
	case canonicalManifest.IsZero():
		return Job{}, false, fmt.Errorf("%w: the canonical version", ErrNoGoverningDigest)
	case gate.ID == 0:
		return Job{}, false, nil
	case gate.Governing.IsZero():
		return Job{}, false, fmt.Errorf("%w: the canonical job %d that the gate reads", ErrNoGoverningDigest, gate.ID)
	case gate.Governing == canonicalManifest:
		return Job{}, false, nil
	}
	for _, j := range slices.Backward(jobs) {
		if j.HasOutcome() && j.Governing == canonicalManifest {
			return j, true, nil
		}
	}
	return Job{}, false, nil
}

// ResultsSets are the sets whose results comment is written this pass, in order: the earlier set of an approved head when it is final, and the later set when it is
// final and has something to say. earlier is the zero Set for a head with no earlier set. The later set is written unless the leads of both sets are "nothing needs
// attention", so that a later set corrects an earlier one that reported something, a head that is clean before and after keeps one comment, and a head with no
// earlier set always gets its comment.
func ResultsSets(earlier, later Set) []Set {
	var sets []Set
	hasEarlier := earlier.Earlier && earlier.Final()
	if hasEarlier {
		sets = append(sets, earlier)
	}
	if later.Final() && (!hasEarlier || earlier.Lead() != LeadClean || later.Lead() != LeadClean) {
		sets = append(sets, later)
	}
	return sets
}
