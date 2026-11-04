package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The Contract check under canonical: required publishes the summary of the canonical gate (docs/contracts/runtime.md, "The canonical gate"): the payload carries
// the counts of a complete run and the owner's unblock, and the publisher renders them.

// chkRequired is the message of generation 3 for a result of the canonical gate, with the counts of a complete run (nil for none).
func chkRequired(t *testing.T, state checks.State, reason string, counts *checkExecution, unblocked bool) (governance.OutboxMessage, error) {
	t.Helper()
	r := checks.Result{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: state, Reason: reason, Reference: "P1-R2", Unblocked: unblocked}
	return newCheckMessageOf(cmtRepo, r, owner.TierIsolated, "V2", 3, nil, 0, counts, 0, false, "")
}

var (
	requiredPassed  = &checkExecution{Tests: 12, Passed: 9, Skipped: 3, Excluded: 3}
	requiredBlocked = &checkExecution{Tests: 14, Passed: 9, Skipped: 2, Failed: 1, Missing: 1, Flaky: 1, Excluded: 3}
)

func TestCheckPublisherPostsTheSummaryOfTheCanonicalGate(t *testing.T) {
	for _, tc := range []struct {
		name               string
		state              checks.State
		reason             string
		counts             *checkExecution
		unblocked          bool
		title              string
		status, conclusion string
		phrases            []string
	}{
		{"pending", checks.InProgress, "canonical-pending", nil, false, "Approved tests pending", "in_progress", "", []string{"have not finished on this commit"}},
		{"unavailable", checks.InProgress, "canonical-unavailable", nil, false, "Approved tests could not run", "in_progress", "", []string{"could not run the approved tests", "/suiteward unblock <code>"}},
		{"not responding", checks.InProgress, "canonical-not-responding", nil, false, "Test runner not responding", "in_progress", "",
			[]string{"has not answered for about half a minute", "check that the test runner is running", "/suiteward unblock <code>"}},
		{"failing", checks.Failure, "canonical-failing", requiredBlocked, false, "Approved tests failing", "completed", "failure",
			[]string{"Approved tests: 14 tests ran. 9 passed. 1 failed. 1 gave no result. 1 gave different results when they ran again. 2 were skipped. 3 approved tests cannot be run by SuiteWard and are not covered."}},
		{"awaiting approval", checks.InProgress, "approval-missing", requiredBlocked, false, "Awaiting owner approval", "in_progress", "",
			[]string{"Approved tests: 14 tests ran. 9 passed. 1 failed. 1 gave no result.", "read the edit before you approve", "/suiteward approve P1-R2 <code>"}},
		{"passed", checks.Success, "ready", requiredPassed, false, "Ready for integration", "completed", "success", []string{"Approved tests: 12 tests ran. 9 passed. 0 failed. 0 gave no result. 0 gave different results when they ran again. 3 were skipped."}},
		{"no declared test", checks.Success, "unchanged", &checkExecution{}, false, "No protected change", "completed", "success", []string{"Approved tests: none are declared, so none ran."}},
		{"unblocked", checks.Success, "ready", nil, true, "Ready for integration", "completed", "success",
			[]string{"The owner unblocked this commit. The approved tests could not run on it, so this check passes without them."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := chkRequired(t, tc.state, tc.reason, tc.counts, tc.unblocked)
			if err != nil {
				t.Fatal(err)
			}
			w := newChkWorld()
			if err := w.deliver(m); err != nil {
				t.Fatal(err)
			}
			posts := w.runner.posts()
			if len(posts) != 1 || posts[0].Name != "SuiteWard / Contract" || posts[0].Status != tc.status || posts[0].Conclusion != tc.conclusion || posts[0].Title != tc.title {
				t.Fatalf("posted %+v", posts)
			}
			for _, phrase := range append(tc.phrases, "This check reports on the protected contract and on the run of its approved tests.", "on a separate server") {
				if !strings.Contains(posts[0].Summary, phrase) {
					t.Errorf("the summary lacks %q:\n%s", phrase, posts[0].Summary)
				}
			}
			if strings.Contains(posts[0].Summary, "does not run tests") {
				t.Errorf("the summary says that SuiteWard does not run tests:\n%s", posts[0].Summary)
			}
		})
	}
}

func TestCheckMessageOfTheCanonicalGateCarriesTheCountsAndTheUnblockInItsPayload(t *testing.T) {
	m, err := chkRequired(t, checks.Failure, "canonical-failing", requiredBlocked, false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(m.Payload, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"tests": 14.0, "passed": 9.0, "skipped": 2.0, "failed": 1.0, "missing": 1.0, "flaky": 1.0, "excluded": 3.0}
	if !reflect.DeepEqual(got["execution"], want) || got["reason"] != "canonical-failing" {
		t.Fatalf("payload %v, want the counts %v", got, want)
	}
	if _, has := got["unblocked"]; has {
		t.Fatalf("a head that was not unblocked says so: %v", got)
	}
	m, err = chkRequired(t, checks.Success, "unchanged", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(m.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["execution"]; has || got["unblocked"] != true {
		t.Fatalf("payload %v, want unblocked and no counts", got)
	}
}

// The message of the canonical gate is rendered when it is written, as every other one is, so a combination that cannot be rendered is never queued.
func TestCheckMessageOfTheCanonicalGateRefusesWhatCannotBeRendered(t *testing.T) {
	for name, tc := range map[string]struct {
		state     checks.State
		reason    string
		counts    *checkExecution
		unblocked bool
	}{
		"a failing run without counts":       {checks.Failure, "canonical-failing", nil, false},
		"a failing run that blocks nothing":  {checks.Failure, "canonical-failing", requiredPassed, false},
		"counts on a pending run":            {checks.InProgress, "canonical-pending", requiredPassed, false},
		"a success with a blocking test":     {checks.Success, "ready", requiredBlocked, false},
		"counts that do not add up":          {checks.Success, "ready", &checkExecution{Tests: 5, Passed: 1}, false},
		"an unblock with counts":             {checks.Success, "ready", requiredPassed, true},
		"an unblock of an approval missing":  {checks.InProgress, "approval-missing", nil, true},
		"counts on a rejected owner command": {checks.Failure, "owner-command-rejected", requiredPassed, false},
		"a pending run that failed":          {checks.Failure, "canonical-pending", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if m, err := chkRequired(t, tc.state, tc.reason, tc.counts, tc.unblocked); err == nil {
				t.Fatalf("built %s", m.Payload)
			}
		})
	}
}

// chkUnavailable is the message of generation 3 for a check of reason, with the reason code of the canonical job that it reports (empty for none).
func chkUnavailable(reason, unavailable string) (governance.OutboxMessage, error) {
	r := checks.Result{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: checks.InProgress, Reason: reason, Reference: "P1-R2"}
	return newCheckMessageOf(cmtRepo, r, owner.TierIsolated, "V2", 3, nil, 0, nil, 0, false, unavailable)
}

// The next step of a canonical-unavailable check follows from the reason of its job (M2.1c, SW-28, part 3): a message with no reason, such as one that was queued before the field
// existed or one that a wait sends again, says what the check said before.
func TestCheckPublisherPostsTheNextStepOfAnUnavailableRunByItsReason(t *testing.T) {
	const push = "Next: push a new commit to run the approved tests again. To let this commit pass without the approved tests, the owner comments:"
	for name, tc := range map[string]struct{ unavailable, next string }{
		"the test runner did not answer": {"no-executor", "Next: SuiteWard runs the approved tests when the test runner answers again. To let this commit pass without the approved tests, the owner comments:"},
		"no test runner is set up":       {"executor-not-configured", "Next: SuiteWard runs the approved tests once the test runner is set up. To let this commit pass without the approved tests, the owner comments:"},
		"another reason":                 {"timeout", push},
		"no reason":                      {"", push},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := chkUnavailable("canonical-unavailable", tc.unavailable)
			if err != nil {
				t.Fatal(err)
			}
			w := newChkWorld()
			if err := w.deliver(m); err != nil {
				t.Fatal(err)
			}
			posts := w.runner.posts()
			if len(posts) != 1 || !strings.Contains(posts[0].Summary, tc.next) || !strings.Contains(posts[0].Summary, "/suiteward unblock <code>") {
				t.Fatalf("posted %+v, want the next step %q and the unblock", posts, tc.next)
			}
		})
	}
}

func TestCheckMessageOfAnUnavailableRunCarriesItsReasonOnlyWhenItHasOneAndRefusesWhatDoesNotFit(t *testing.T) {
	for unavailable, want := range map[string]bool{"no-executor": true, "": false} {
		m, err := chkUnavailable("canonical-unavailable", unavailable)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(m.Payload, &got); err != nil {
			t.Fatal(err)
		}
		if _, has := got["unavailable"]; has != want || (want && got["unavailable"] != unavailable) {
			t.Errorf("payload %v, want the reason %q carried = %v", got, unavailable, want)
		}
	}
	for name, tc := range map[string]struct{ reason, unavailable string }{
		"a code outside the closed set":                  {"canonical-unavailable", "bogus"},
		"a reason on a pending run":                      {"canonical-pending", "no-executor"},
		"a reason on a runner that does not respond":     {"canonical-not-responding", "no-executor"},
		"a reason on a check that the run does not hold": {"approval-missing", "no-executor"},
	} {
		t.Run(name, func(t *testing.T) {
			if m, err := chkUnavailable(tc.reason, tc.unavailable); err == nil {
				t.Fatalf("built %s", m.Payload)
			}
		})
	}
}
