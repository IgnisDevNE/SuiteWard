package execution_test

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The rules of the comments of a head (docs/contracts/runtime.md, "The comments of a head" and "Not responding"). Each test is a table over a head that starts as
// the plainest one: both runs required, both complete and clean, and the check of the head one that the canonical gate acts on.

type edit = func(*execution.Set)

func jobOf(id int64, state execution.JobState, governing artifact.Digest) execution.Job {
	return execution.Job{ID: id, State: state, Governing: governing}
}

func newSet(edits ...edit) execution.Set {
	s := execution.Set{
		CanonicalLevel: execution.CanonicalLevelRequired, ReceiptLevel: execution.ReceiptLevelRequired,
		ReceiptApplies: true, ReceiptDecided: true, HeadReason: execution.HeadReasonGate,
		CanonicalJob: jobOf(10, execution.JobComplete, approved), ReceiptJob: jobOf(11, execution.JobComplete, approved),
	}
	for _, e := range edits {
		e(&s)
	}
	return s
}

func withCanonical(state execution.JobState) edit {
	return func(s *execution.Set) { s.CanonicalJob.State = state }
}
func withReceipt(state execution.JobState) edit {
	return func(s *execution.Set) { s.ReceiptJob.State = state }
}
func withNoCanonicalJob(s *execution.Set)     { s.CanonicalJob = execution.Job{} }
func withNoReceiptJob(s *execution.Set)       { s.ReceiptJob = execution.Job{} }
func withEarlier(s *execution.Set)            { s.Earlier = true }
func withUndecided(s *execution.Set)          { s.ReceiptDecided = false }
func withNoReceipt(s *execution.Set)          { s.ReceiptApplies = false }
func withCanonicalUnblocked(s *execution.Set) { s.CanonicalJob.Unblocked = true }
func withReceiptUnblocked(s *execution.Set)   { s.ReceiptJob.Unblocked = true }
func withHead(reason execution.HeadReason) edit {
	return func(s *execution.Set) { s.HeadReason = reason }
}
func withWeakened(n int) edit { return func(s *execution.Set) { s.Weakened = n } }
func withCanonicalCounts(c execution.CanonicalCounts) edit {
	return func(s *execution.Set) { s.CanonicalCounts = c }
}
func withReceiptCounts(c execution.ReceiptCounts) edit {
	return func(s *execution.Set) { s.ReceiptCounts = c }
}
func withLevels(c execution.CanonicalLevel, r execution.ReceiptLevel) edit {
	return func(s *execution.Set) { s.CanonicalLevel, s.ReceiptLevel = c, r }
}
func withCanonicalJob(id int64, state execution.JobState, governing artifact.Digest) edit {
	return func(s *execution.Set) { s.CanonicalJob = jobOf(id, state, governing) }
}
func onGitHub(job *execution.Job) { job.Reason = execution.JobReasonGitHubUnavailable }

func TestJobWaitsForRunner(t *testing.T) {
	tests := []struct {
		name string
		job  execution.Job
		want bool
	}{
		{"pending", execution.Job{ID: 1, State: execution.JobPending}, true},
		{"leased", execution.Job{ID: 1, State: execution.JobLeased}, true},
		{"pending again after a failed attempt", execution.Job{ID: 1, State: execution.JobPending, Reason: "lease-expired"}, true},
		{"pending on GitHub", execution.Job{ID: 1, State: execution.JobPending, Reason: execution.JobReasonGitHubUnavailable}, false},
		{"complete", execution.Job{ID: 1, State: execution.JobComplete}, false},
		{"unavailable because no test runner answered", execution.Job{ID: 1, State: execution.JobUnavailable, Reason: "no-executor"}, false},
		{"no job", execution.Job{}, false},
	}
	for _, tc := range tests {
		if got := tc.job.WaitsForRunner(); got != tc.want {
			t.Errorf("%s: WaitsForRunner() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJobHasOutcome(t *testing.T) {
	for state, want := range map[execution.JobState]bool{execution.JobPending: false, execution.JobLeased: false, execution.JobComplete: true, execution.JobUnavailable: true, 0: false} {
		if got := (execution.Job{ID: 1, State: state}).HasOutcome(); got != want {
			t.Errorf("a job in state %q: HasOutcome() = %v, want %v", state, got, want)
		}
	}
}

func TestParseJobState(t *testing.T) {
	for text, want := range map[string]execution.JobState{"pending": execution.JobPending, "leased": execution.JobLeased, "complete": execution.JobComplete, "unavailable": execution.JobUnavailable} {
		got, err := execution.ParseJobState(text)
		if err != nil || got != want || got.String() != text {
			t.Errorf("ParseJobState(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	// A cancelled job is never part of a set.
	for _, text := range []string{"cancelled", "", "Pending", "done", "pending "} {
		if got, err := execution.ParseJobState(text); !errors.Is(err, execution.ErrInvalidJobState) {
			t.Errorf("ParseJobState(%q) = %v, %v; want ErrInvalidJobState", text, got, err)
		}
	}
}

func TestParseNext(t *testing.T) {
	for text, want := range map[string]execution.Next{"unblock": execution.NextUnblock, "blocked": execution.NextBlocked, "approve": execution.NextApprove, "fix": execution.NextFix, "none": execution.NextNone} {
		got, err := execution.ParseNext(text)
		if err != nil || got != want || got.String() != text {
			t.Errorf("ParseNext(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	for _, text := range []string{"", "Fix", "approved", "none "} {
		if got, err := execution.ParseNext(text); !errors.Is(err, execution.ErrInvalidNext) {
			t.Errorf("ParseNext(%q) = %v, %v; want ErrInvalidNext", text, got, err)
		}
	}
}

func TestSetFinal(t *testing.T) {
	const (
		required = execution.CanonicalLevelRequired
		off      = execution.CanonicalLevelOff
	)
	tests := []struct {
		name  string
		edits []edit
		want  bool
	}{
		{"both runs complete", nil, true},
		{"the canonical run is unavailable", []edit{withCanonical(execution.JobUnavailable)}, true},
		{"both runs are unavailable", []edit{withCanonical(execution.JobUnavailable), withReceipt(execution.JobUnavailable)}, true},
		{"an unavailable run that the owner unblocked", []edit{withCanonical(execution.JobUnavailable), withCanonicalUnblocked}, true},
		{"the canonical run is pending", []edit{withCanonical(execution.JobPending)}, false},
		{"the canonical run is leased", []edit{withCanonical(execution.JobLeased)}, false},
		{"the receipt is pending at the approval", []edit{withReceipt(execution.JobPending)}, false},
		{"the receipt is leased", []edit{withReceipt(execution.JobLeased)}, false},
		{"the canonical plan has no job yet", []edit{withNoCanonicalJob}, false},
		{"the receipt plan has no job yet", []edit{withNoReceiptJob}, false},
		{"the run has not decided whether the receipt applies", []edit{withUndecided}, false},
		{"the receipt level is off: the canonical run alone, whatever the receipt job is", []edit{withLevels(required, execution.ReceiptLevelOff), withReceipt(execution.JobPending)}, true},
		{"the receipt does not apply: the canonical run alone", []edit{withNoReceipt, withNoReceiptJob}, true},
		{"the canonical level is off: the receipt alone", []edit{withLevels(off, execution.ReceiptLevelAdvisory), withNoCanonicalJob}, true},
		{"nothing is wanted: no canonical run and a receipt that does not apply", []edit{withLevels(off, execution.ReceiptLevelRequired), withNoReceipt}, false},
		{"nothing is wanted: both levels are off", []edit{withLevels(off, execution.ReceiptLevelOff)}, false},
		{"the policy was not read", []edit{withLevels(0, 0)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := newSet(tc.edits...).Final(); got != tc.want {
				t.Errorf("Final() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJobPair(t *testing.T) {
	for pair, want := range map[execution.JobPair]string{{Canonical: 10, Receipt: 11}: "10-11", {Canonical: 0, Receipt: 11}: "0-11", {Canonical: 10, Receipt: 0}: "10-0"} {
		if got := pair.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", pair, got, want)
		}
	}
	// The key of a comment is at most 200 bytes: the pair adds 19 + 1 + 19.
	if got := (execution.JobPair{Canonical: math.MaxInt64, Receipt: math.MaxInt64}).String(); len(got) != 39 {
		t.Errorf("the longest pair is %q, %d bytes; want 39", got, len(got))
	}
}

func TestSetJobs(t *testing.T) {
	tests := []struct {
		name  string
		edits []edit
		want  execution.JobPair
	}{
		{"both plans are wanted", nil, execution.JobPair{Canonical: 10, Receipt: 11}},
		{"the receipt level is off: its job is not named", []edit{withLevels(execution.CanonicalLevelRequired, execution.ReceiptLevelOff)}, execution.JobPair{Canonical: 10}},
		{"the receipt does not apply: its job is not named", []edit{withNoReceipt}, execution.JobPair{Canonical: 10}},
		{"the canonical level is off: its job is not named", []edit{withLevels(execution.CanonicalLevelOff, execution.ReceiptLevelRequired)}, execution.JobPair{Receipt: 11}},
		{"the earlier set names its own canonical job", []edit{withEarlier, withCanonicalJob(7, execution.JobComplete, approved)}, execution.JobPair{Canonical: 7, Receipt: 11}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := newSet(tc.edits...).Jobs(); got != tc.want {
				t.Errorf("Jobs() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSetNotResponding(t *testing.T) {
	const (
		pending = execution.JobPending
		leased  = execution.JobLeased
	)
	tests := []struct {
		name        string
		edits       []edit
		silent      bool
		wantReading bool
		wantJobs    execution.JobPair
		wantUnblock bool
	}{
		{"the canonical run waits and the runner is silent", []edit{withCanonical(pending)}, true, true, execution.JobPair{Canonical: 10}, true},
		{"the runner is not silent", []edit{withCanonical(pending)}, false, false, execution.JobPair{}, false},
		{"a leased canonical run of an approval-missing head holds nothing to unblock", []edit{withCanonical(leased), withHead(execution.HeadReasonApprovalMissing)}, true, true, execution.JobPair{Canonical: 10}, false},
		{"a waiting canonical run under advisory", []edit{withCanonical(pending), withLevels(execution.CanonicalLevelAdvisory, execution.ReceiptLevelRequired)}, true, true, execution.JobPair{Canonical: 10}, false},
		{"a waiting canonical run of a head with another reason", []edit{withCanonical(pending), withHead(execution.HeadReasonOther)}, true, true, execution.JobPair{Canonical: 10}, false},
		{"the receipt waits under required", []edit{withReceipt(pending)}, true, true, execution.JobPair{Receipt: 11}, true},
		{"the receipt runs under before-approval: it refuses the approve while it runs", []edit{withReceipt(leased), withLevels(execution.CanonicalLevelAdvisory, execution.ReceiptLevelBeforeApproval)}, true, true, execution.JobPair{Receipt: 11}, true},
		{"the receipt waits under advisory", []edit{withReceipt(pending), withLevels(execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory)}, true, true, execution.JobPair{Receipt: 11}, false},
		{"both runs wait", []edit{withCanonical(pending), withReceipt(leased)}, true, true, execution.JobPair{Canonical: 10, Receipt: 11}, true},
		{"a pending run again after a failed attempt waits", []edit{func(s *execution.Set) {
			s.CanonicalJob = execution.Job{ID: 10, State: pending, Reason: "lease-expired"}
		}}, true, true, execution.JobPair{Canonical: 10}, true},
		{"a canonical run that waits on GitHub waits for GitHub", []edit{withCanonical(pending), func(s *execution.Set) { onGitHub(&s.CanonicalJob) }}, true, false, execution.JobPair{}, false},
		{"only the receipt waits for the runner when the canonical run waits on GitHub", []edit{withCanonical(pending), withReceipt(pending), func(s *execution.Set) { onGitHub(&s.CanonicalJob) }}, true, true, execution.JobPair{Receipt: 11}, true},
		{"nothing waits", nil, true, false, execution.JobPair{}, false},
		{"a receipt job that is not wanted is ignored", []edit{withReceipt(pending), withLevels(execution.CanonicalLevelRequired, execution.ReceiptLevelOff)}, true, false, execution.JobPair{}, false},
		{"a receipt that does not apply is ignored", []edit{withReceipt(pending), withNoReceipt}, true, false, execution.JobPair{}, false},
		{"a canonical job that is not wanted is ignored", []edit{withCanonical(pending), withLevels(execution.CanonicalLevelOff, execution.ReceiptLevelRequired)}, true, false, execution.JobPair{}, false},
		{"an unavailable run waits for nothing", []edit{withCanonical(execution.JobUnavailable)}, true, false, execution.JobPair{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reading := newSet(tc.edits...).NotResponding(tc.silent)
			if reading != tc.wantReading || got.Jobs != tc.wantJobs || got.Unblock != tc.wantUnblock {
				t.Errorf("NotResponding(%v) = %+v, %v; want jobs %+v, unblock %v, reading %v", tc.silent, got, reading, tc.wantJobs, tc.wantUnblock, tc.wantReading)
			}
		})
	}
}

func TestSetLead(t *testing.T) {
	unavailable := execution.JobUnavailable
	tests := []struct {
		name  string
		edits []edit
		want  execution.Lead
	}{
		{"every wanted run is unavailable", []edit{withCanonical(unavailable), withReceipt(unavailable)}, execution.LeadNotRun},
		{"the one wanted run is unavailable", []edit{withCanonical(unavailable), withNoReceipt}, execution.LeadNotRun},
		{"the canonical run is complete and the receipt unavailable", []edit{withReceipt(unavailable)}, execution.LeadPartial},
		{"the canonical run is unavailable and the receipt complete", []edit{withCanonical(unavailable)}, execution.LeadPartial},
		{"both complete and clean", nil, execution.LeadClean},
		{"an approved test fails", []edit{withCanonicalCounts(execution.CanonicalCounts{Pass: 3, Fail: 1})}, execution.LeadAttention},
		{"an approved test is missing", []edit{withCanonicalCounts(execution.CanonicalCounts{Missing: 1})}, execution.LeadAttention},
		{"an approved test is flaky", []edit{withCanonicalCounts(execution.CanonicalCounts{Flaky: 1})}, execution.LeadAttention},
		{"a test of the receipt fails", []edit{withReceiptCounts(execution.ReceiptCounts{Fails: 1})}, execution.LeadAttention},
		{"a test of the receipt is missing", []edit{withReceiptCounts(execution.ReceiptCounts{Missing: 1})}, execution.LeadAttention},
		{"a test of the receipt is flaky", []edit{withReceiptCounts(execution.ReceiptCounts{Flaky: 1})}, execution.LeadAttention},
		{"a changed test file does not parse", []edit{withReceiptCounts(execution.ReceiptCounts{Unparsed: 1})}, execution.LeadAttention},
		{"the receipt has tests that need no attention", []edit{withReceiptCounts(execution.ReceiptCounts{Detects: 2, Inert: 1, NewAPI: 1, Skipped: 3, Excluded: 4})}, execution.LeadClean},
		{"the approved tests have a skip and an excluded test", []edit{withCanonicalCounts(execution.CanonicalCounts{Pass: 5, Skipped: 1, Excluded: 2})}, execution.LeadClean},
		{"an approved test that the change edits fails: weakened", []edit{withWeakened(2)}, execution.LeadAttention},
		{"weakened does not outrank a run that did not finish", []edit{withWeakened(2), withReceipt(unavailable)}, execution.LeadPartial},
		{"weakened does not outrank runs that did not run", []edit{withWeakened(2), withCanonical(unavailable), withReceipt(unavailable)}, execution.LeadNotRun},
		{"the canonical run alone, clean", []edit{withNoReceipt, withNoReceiptJob}, execution.LeadClean},
		{"the canonical run alone, failing", []edit{withNoReceipt, withNoReceiptJob, withCanonicalCounts(execution.CanonicalCounts{Fail: 1})}, execution.LeadAttention},
		{"a failing receipt that is not wanted is not read", []edit{withNoReceipt, withReceiptCounts(execution.ReceiptCounts{Fails: 4})}, execution.LeadClean},
		{"nothing is wanted: no set", []edit{withLevels(execution.CanonicalLevelOff, execution.ReceiptLevelOff)}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := newSet(tc.edits...).Lead(); got != tc.want {
				t.Errorf("Lead() = %d, want %d", got, tc.want)
			}
		})
	}
}

// A set that is not final says nothing yet: its lead is none, so that no caller reads "nothing needs your attention" on a head that is still waiting.
func TestSetLeadOfASetThatIsNotFinalIsNone(t *testing.T) {
	tests := []struct {
		name  string
		edits []edit
	}{
		{"the canonical run is pending", []edit{withCanonical(execution.JobPending)}},
		{"the canonical run is leased and the receipt failed", []edit{withCanonical(execution.JobLeased), withReceiptCounts(execution.ReceiptCounts{Fails: 1})}},
		{"the receipt is pending and the canonical run is clean", []edit{withReceipt(execution.JobPending)}},
		{"the receipt is pending and weakened is counted", []edit{withReceipt(execution.JobPending), withWeakened(2)}},
		{"the canonical plan has no job yet", []edit{withNoCanonicalJob}},
		{"the receipt plan has no job yet", []edit{withNoReceiptJob}},
		{"the run has not decided whether the receipt applies", []edit{withUndecided}},
		{"the policy was not read", []edit{withLevels(0, 0)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSet(tc.edits...)
			if got := s.Lead(); s.Final() || got != 0 {
				t.Errorf("Lead() = %d, want 0 (the set is final: %v, and must not be)", got, s.Final())
			}
		})
	}
}

func TestSetHolds(t *testing.T) {
	const (
		advisory       = execution.CanonicalLevelAdvisory
		beforeApproval = execution.ReceiptLevelBeforeApproval
	)
	tests := []struct {
		name  string
		edits []edit
		want  execution.Holds
	}{
		{"required runs of a head that the gate acts on", nil, execution.Holds{Canonical: true, Receipt: true}},
		{"an approval-missing head: the approval is what is missing", []edit{withHead(execution.HeadReasonApprovalMissing)}, execution.Holds{Receipt: true}},
		{"a head with another reason keeps it", []edit{withHead(execution.HeadReasonOther)}, execution.Holds{Receipt: true}},
		{"canonical advisory", []edit{withLevels(advisory, execution.ReceiptLevelRequired)}, execution.Holds{Receipt: true}},
		{"canonical off", []edit{withLevels(execution.CanonicalLevelOff, execution.ReceiptLevelRequired)}, execution.Holds{Receipt: true}},
		{"the earlier set of an approved head: its canonical job is not the one the gate reads", []edit{withEarlier}, execution.Holds{Receipt: true}},
		{"receipt advisory", []edit{withLevels(execution.CanonicalLevelRequired, execution.ReceiptLevelAdvisory)}, execution.Holds{Canonical: true}},
		{"receipt off", []edit{withLevels(execution.CanonicalLevelRequired, execution.ReceiptLevelOff)}, execution.Holds{Canonical: true}},
		{"a receipt that does not apply", []edit{withNoReceipt}, execution.Holds{Canonical: true}},
		{"required receipt, unavailable", []edit{withReceipt(execution.JobUnavailable)}, execution.Holds{Canonical: true, Receipt: true}},
		{"required receipt, pending", []edit{withReceipt(execution.JobPending)}, execution.Holds{Canonical: true, Receipt: true}},
		{"before-approval, complete", []edit{withLevels(execution.CanonicalLevelRequired, beforeApproval)}, execution.Holds{Canonical: true}},
		{"before-approval, unavailable: an unblock of it changes nothing", []edit{withLevels(execution.CanonicalLevelRequired, beforeApproval), withReceipt(execution.JobUnavailable)}, execution.Holds{Canonical: true}},
		{"before-approval, pending", []edit{withLevels(execution.CanonicalLevelRequired, beforeApproval), withReceipt(execution.JobPending)}, execution.Holds{Canonical: true, Receipt: true}},
		{"before-approval, leased", []edit{withLevels(execution.CanonicalLevelRequired, beforeApproval), withReceipt(execution.JobLeased)}, execution.Holds{Canonical: true, Receipt: true}},
		{"before-approval, no job yet", []edit{withLevels(execution.CanonicalLevelRequired, beforeApproval), withNoReceiptJob}, execution.Holds{Canonical: true, Receipt: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := newSet(tc.edits...).Holds(); got != tc.want {
				t.Errorf("Holds() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSetNext(t *testing.T) {
	missing := withHead(execution.HeadReasonApprovalMissing)
	beforeApproval := withLevels(execution.CanonicalLevelRequired, execution.ReceiptLevelBeforeApproval)
	tests := []struct {
		name  string
		edits []edit
		want  execution.Next
	}{
		{"a clean head", nil, execution.NextNone},
		{"the canonical run is unavailable and not unblocked", []edit{withCanonical(execution.JobUnavailable)}, execution.NextUnblock},
		{"the canonical run is unavailable and unblocked", []edit{withCanonical(execution.JobUnavailable), withCanonicalUnblocked}, execution.NextNone},
		{"the receipt is unavailable under required", []edit{withReceipt(execution.JobUnavailable)}, execution.NextUnblock},
		{"the receipt is unavailable under required and unblocked", []edit{withReceipt(execution.JobUnavailable), withReceiptUnblocked}, execution.NextNone},
		{"an approval-missing head with an unavailable canonical run: the approval is what is missing", []edit{missing, withCanonical(execution.JobUnavailable)}, execution.NextApprove},
		{"an approval-missing head with an unavailable receipt under required: it holds the approve", []edit{missing, withReceipt(execution.JobUnavailable)}, execution.NextUnblock},
		{"before-approval with an unavailable receipt, approval missing", []edit{missing, beforeApproval, withReceipt(execution.JobUnavailable)}, execution.NextApprove},
		{"before-approval with an unavailable receipt, nothing missing", []edit{beforeApproval, withReceipt(execution.JobUnavailable)}, execution.NextNone},
		{"before-approval with an unavailable receipt and a failing approved test", []edit{beforeApproval, withReceipt(execution.JobUnavailable), withCanonicalCounts(execution.CanonicalCounts{Fail: 1})}, execution.NextFix},
		{"a complete receipt that meets no mode, approval missing", []edit{missing, withReceiptCounts(execution.ReceiptCounts{Detects: 1, Fails: 1})}, execution.NextBlocked},
		{"a receipt of one file that does not parse blocks every mode", []edit{missing, withReceiptCounts(execution.ReceiptCounts{Unparsed: 1})}, execution.NextBlocked},
		{"a receipt of skipped tests meets no mode", []edit{missing, withReceiptCounts(execution.ReceiptCounts{Skipped: 2})}, execution.NextBlocked},
		{"a receipt that meets fix", []edit{missing, withReceiptCounts(execution.ReceiptCounts{Detects: 1})}, execution.NextApprove},
		{"a receipt that meets only refactor", []edit{missing, withReceiptCounts(execution.ReceiptCounts{Inert: 2})}, execution.NextApprove},
		{"a receipt that meets only feature", []edit{missing, withReceiptCounts(execution.ReceiptCounts{NewAPI: 1, Skipped: 1})}, execution.NextApprove},
		{"an empty receipt has nothing to judge", []edit{missing, withReceiptCounts(execution.ReceiptCounts{Excluded: 3})}, execution.NextApprove},
		{"a failing receipt blocks only under required", []edit{missing, beforeApproval, withReceiptCounts(execution.ReceiptCounts{Fails: 1})}, execution.NextApprove},
		{"a failing receipt under advisory", []edit{missing, withLevels(execution.CanonicalLevelRequired, execution.ReceiptLevelAdvisory), withReceiptCounts(execution.ReceiptCounts{Fails: 1})}, execution.NextApprove},
		{"a receipt that does not apply", []edit{missing, withNoReceipt, withNoReceiptJob}, execution.NextApprove},
		{"a receipt that is not complete is not judged", []edit{missing, withReceipt(execution.JobUnavailable), withReceiptUnblocked, withReceiptCounts(execution.ReceiptCounts{Fails: 1})}, execution.NextApprove},
		{"approval missing comes before fix", []edit{missing, withCanonicalCounts(execution.CanonicalCounts{Fail: 1})}, execution.NextApprove},
		{"canonical-failing: required, complete and an approved test fails", []edit{withCanonicalCounts(execution.CanonicalCounts{Pass: 2, Fail: 1})}, execution.NextFix},
		{"a test of the receipt fails", []edit{withReceiptCounts(execution.ReceiptCounts{Fails: 1})}, execution.NextFix},
		{"a changed test file does not parse", []edit{withReceiptCounts(execution.ReceiptCounts{Unparsed: 1})}, execution.NextFix},
		{"an approved test is flaky", []edit{withCanonicalCounts(execution.CanonicalCounts{Flaky: 1})}, execution.NextFix},
		{"a weakened test alone does not ask for a fix", []edit{withWeakened(1)}, execution.NextNone},
		{"the earlier set of an approved head: its unavailable canonical job holds nothing", []edit{withEarlier, withCanonical(execution.JobUnavailable)}, execution.NextNone},
		{"a head with another reason: nothing holds", []edit{withHead(execution.HeadReasonOther), withCanonical(execution.JobUnavailable)}, execution.NextNone},
		{"canonical advisory: nothing holds", []edit{withLevels(execution.CanonicalLevelAdvisory, execution.ReceiptLevelAdvisory), withCanonical(execution.JobUnavailable)}, execution.NextNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := newSet(tc.edits...).Next(); err != nil || got != tc.want {
				t.Errorf("Next() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestEarlierCanonical(t *testing.T) {
	later := jobOf(12, execution.JobComplete, revision)
	tests := []struct {
		name     string
		jobs     []execution.Job
		gate     execution.Job
		manifest artifact.Digest
		want     int64 // the id of the earlier job, 0 for none
	}{
		{"the job that came before the approval", []execution.Job{jobOf(10, execution.JobComplete, approved), later}, later, approved, 10},
		{"the newest job with an outcome wins", []execution.Job{jobOf(8, execution.JobUnavailable, approved), jobOf(10, execution.JobComplete, approved), later}, later, approved, 10},
		{"an unavailable job is an outcome", []execution.Job{jobOf(8, execution.JobComplete, approved), jobOf(10, execution.JobUnavailable, approved), later}, later, approved, 10},
		{"an open job is skipped", []execution.Job{jobOf(8, execution.JobComplete, approved), jobOf(10, execution.JobPending, approved), later}, later, approved, 8},
		{"no job but an open one", []execution.Job{jobOf(10, execution.JobLeased, approved), later}, later, approved, 0},
		{"a head that is not approved: the gate reads the canonical version's job", []execution.Job{jobOf(8, execution.JobComplete, approved), jobOf(10, execution.JobComplete, approved)}, jobOf(10, execution.JobComplete, approved), approved, 0},
		{"the first job was cancelled while pending: no earlier set", []execution.Job{later}, later, approved, 0},
		{"no job at all", nil, later, approved, 0},
		{"the gate has no canonical job yet: the head is not approved", []execution.Job{jobOf(10, execution.JobComplete, approved)}, execution.Job{}, approved, 0},
		{"jobs of another manifest only", []execution.Job{jobOf(10, execution.JobComplete, digestOf("another")), later}, later, approved, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := execution.EarlierCanonical(tc.jobs, tc.gate, tc.manifest)
			if err != nil || ok != (tc.want != 0) || got.ID != tc.want {
				t.Errorf("EarlierCanonical = %+v, %v, %v; want job %d", got, ok, err, tc.want)
			}
		})
	}
}

// A canonical version has a manifest and a job always has a governing one, so a missing digest is a caller that did not fill it: an error, never "no earlier set",
// and two digests that are both missing must not compare equal.
func TestEarlierCanonicalRefusesAMissingManifest(t *testing.T) {
	older := jobOf(10, execution.JobComplete, approved)
	tests := []struct {
		name     string
		gate     execution.Job
		manifest artifact.Digest
	}{
		{"no manifest of the canonical version", jobOf(12, execution.JobComplete, revision), artifact.Digest{}},
		{"a gate job with no manifest", jobOf(12, execution.JobComplete, artifact.Digest{}), approved},
		{"both are missing: they do not compare equal", jobOf(12, execution.JobComplete, artifact.Digest{}), artifact.Digest{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok, err := execution.EarlierCanonical([]execution.Job{older}, tc.gate, tc.manifest); !errors.Is(err, execution.ErrNoGoverningDigest) || ok || got != (execution.Job{}) {
				t.Errorf("EarlierCanonical = %+v, %v, %v; want ErrNoGoverningDigest", got, ok, err)
			}
		})
	}
}

// laterSet is the set of an approved head: its canonical job is under the manifest of the revision. earlierSet is the set of the job that came before.
func laterSet(edits ...edit) execution.Set {
	return newSet(append([]edit{withCanonicalJob(12, execution.JobComplete, revision)}, edits...)...)
}

func earlierSet(edits ...edit) execution.Set {
	return newSet(append([]edit{withEarlier, withCanonicalJob(10, execution.JobComplete, approved)}, edits...)...)
}

func TestResultsSets(t *testing.T) {
	failing := withCanonicalCounts(execution.CanonicalCounts{Pass: 2, Fail: 1})
	tests := []struct {
		name           string
		earlier, later execution.Set
		want           []int64 // the canonical jobs of the sets to write, in order
	}{
		{"a head with no earlier set gets its comment, however clean", execution.Set{}, laterSet(), []int64{12}},
		{"a head with no earlier set whose set is not final", execution.Set{}, laterSet(withCanonical(execution.JobPending)), nil},
		{"both sets final in one pass, and the earlier one reports a failing test", earlierSet(failing), laterSet(), []int64{10, 12}},
		{"both sets final and both clean: one comment", earlierSet(), laterSet(), []int64{10}},
		{"the earlier set is clean and an approved test fails after the approval", earlierSet(), laterSet(failing), []int64{10, 12}},
		{"the earlier canonical job unavailable after the approval, then a clean later set", earlierSet(withCanonical(execution.JobUnavailable)), laterSet(), []int64{10, 12}},
		{"the earlier set has a weakened test and the later one is clean", earlierSet(withWeakened(1)), laterSet(), []int64{10, 12}},
		{"both sets report the same failing test", earlierSet(failing), laterSet(failing), []int64{10, 12}},
		{"the receipt is pending at the approval: neither set is final", earlierSet(withReceipt(execution.JobPending)), laterSet(withReceipt(execution.JobPending)), nil},
		{"the earlier set is final and the later canonical job still runs", earlierSet(), laterSet(withCanonical(execution.JobPending)), []int64{10}},
		{"the later canonical job has no job yet", earlierSet(), laterSet(withNoCanonicalJob), []int64{10}},
		{"an earlier set that is not final does not hold the later one back", earlierSet(withReceipt(execution.JobPending)), laterSet(), []int64{12}},
		{"the later set is unavailable and the earlier one clean", earlierSet(), laterSet(withCanonical(execution.JobUnavailable)), []int64{10, 12}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []int64
			for _, s := range execution.ResultsSets(tc.earlier, tc.later) {
				got = append(got, s.CanonicalJob.ID)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ResultsSets wrote the sets of the canonical jobs %v, want %v", got, tc.want)
			}
		})
	}
}

// The earlier set and the later one share the receipt job and the earlier canonical job has an outcome, so a later set that is final has an earlier set that is
// final: the later one is never written for a head whose earlier one could not be.
func TestALaterSetThatIsFinalHasAnEarlierSetThatIsFinal(t *testing.T) {
	states := []execution.JobState{0, execution.JobPending, execution.JobLeased, execution.JobComplete, execution.JobUnavailable}
	for _, canonical := range states {
		for _, receipt := range states {
			for _, decided := range []bool{true, false} {
				edits := []edit{withReceipt(receipt), func(s *execution.Set) { s.ReceiptDecided = decided }}
				if receipt == 0 {
					edits[0] = withNoReceiptJob
				}
				later := laterSet(append(edits, withCanonical(canonical))...)
				if earlier := earlierSet(edits...); later.Final() && !earlier.Final() {
					t.Errorf("canonical %q, receipt %q, decided %v: the later set is final and the earlier one is not", canonical, receipt, decided)
				}
			}
		}
	}
}
