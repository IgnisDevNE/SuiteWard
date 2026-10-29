package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The github_receipt_check publisher (docs/contracts/runtime.md, "The SuiteWard / Receipt check", "Outbox kind github_receipt_check"): it drops a message whose generation
// is no longer the current one, finds or posts the run named SuiteWard / Receipt by its external id, and sends nothing for a state that is no GitHub conclusion.

type fakeReceiptLedger struct {
	record execution.ReceiptRecord
	found  bool
	err    error
	reads  int
}

func (l *fakeReceiptLedger) Current(_ context.Context, repositoryID int64, pull int) (execution.ReceiptRecord, bool, error) {
	l.reads++
	if repositoryID != cmtRepoID || pull != cmtPull {
		return execution.ReceiptRecord{}, false, nil
	}
	return l.record, l.found, l.err
}

type rcpWorld struct {
	runner *fakeChecks
	ledger *fakeReceiptLedger
	pub    *receiptCheckPublisher
	logs   *bytes.Buffer
}

func newRcpWorld() *rcpWorld {
	w := &rcpWorld{runner: &fakeChecks{}, ledger: &fakeReceiptLedger{found: true, record: execution.ReceiptRecord{Generation: 3}}, logs: &bytes.Buffer{}}
	w.pub = &receiptCheckPublisher{runs: w.runner, ledger: w.ledger, appID: chkAppID, logger: slog.New(slog.NewTextHandler(w.logs, nil)),
		instance: newLazy(func(context.Context) (string, error) { return cmtInstance, nil })}
	return w
}

func (w *rcpWorld) deliver(m governance.OutboxMessage) error {
	return w.pub.publish(context.Background(), m.Key, m.Payload)
}

var rcpCounts = receiptCheckCounts{Detects: 3, Inert: 2, NewAPI: 1, Skipped: 4, Excluded: 5, Flaky: 6, Markers: 8}

// rcpDecision is a decided Receipt check of the head, with the counts and the unavailable reason that its state needs.
func rcpDecision(state execution.CheckState, reason string) receiptDecision {
	d := receiptDecision{publish: true, check: execution.ReceiptCheck{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: state, Reason: reason, Reference: "P1-R2"}}
	switch reason {
	case receiptComplete:
		d.counts = rcpCounts
	case receiptFailing:
		d.counts = receiptCheckCounts{Detects: 3, Fails: 2, Missing: 1, Markers: 9, Unparsed: 7}
	case receiptNotRun:
		d.unavailable = execution.ReasonNoExecutor
	}
	return d
}

// rcpMessage is the message of generation 3 for a decided check.
func rcpMessage(t *testing.T, d receiptDecision) governance.OutboxMessage {
	t.Helper()
	m, err := newReceiptCheckMessage(cmtRepo, d, owner.TierIsolated, 3)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func rcpGood(t *testing.T) governance.OutboxMessage {
	t.Helper()
	return rcpMessage(t, rcpDecision(execution.CheckSuccess, receiptComplete))
}

func TestReceiptCheckPublisherPostsEveryState(t *testing.T) {
	for _, tc := range []struct {
		state              execution.CheckState
		reason             string
		title              string
		status, conclusion string
		phrase             string
	}{
		{execution.CheckInProgress, receiptRunning, "Receipt pending", "in_progress", "", "The tests are waiting for the test runner, or they are running"},
		{execution.CheckFailure, receiptFailing, "Receipt: failing or missing tests", "completed", "failure", "Changed test files that cannot be read: 7."},
		{execution.CheckSuccess, receiptComplete, "Receipt ready", "completed", "success", "- Pass before and after, so they show nothing: 2"},
		{execution.CheckNeutral, receiptNotRun, "Receipt unavailable", "completed", "neutral", "The test runner did not respond."},
		{execution.CheckNeutral, receiptNotApplicable, "No receipt for this revision", "completed", "neutral", "This commit has no receipt."},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			w := newRcpWorld()
			m := rcpMessage(t, rcpDecision(tc.state, tc.reason))
			if m.Key != "receipt-check:1001:7:3" || m.Kind != "github_receipt_check" {
				t.Fatalf("key %q, kind %q", m.Key, m.Kind)
			}
			if err := w.deliver(m); err != nil {
				t.Fatal(err)
			}
			posts := w.runner.posts()
			if len(posts) != 1 {
				t.Fatalf("%d posts, want 1", len(posts))
			}
			p := posts[0]
			if p.Name != "SuiteWard / Receipt" || p.HeadSHA != chkHead || p.Status != tc.status || p.Conclusion != tc.conclusion || p.ExternalID != "suiteward:"+cmtInstance+":"+m.Key {
				t.Fatalf("posted %+v", p)
			}
			// A withdrawn receipt observed nothing, so its summary does not call the results evidence.
			evidence := strings.Contains(p.Summary, "These results are what the test runner saw. They are evidence, not a verdict.")
			if p.Title != tc.title || !strings.Contains(p.Summary, tc.phrase) || !strings.Contains(p.Summary, "`isolated`") || evidence != (tc.reason != receiptNotApplicable) {
				t.Fatalf("title %q, summary %q", p.Title, p.Summary)
			}
			if got := w.runner.listed; len(got) != 1 || got[0] != fmt.Sprintf("%s %s SuiteWard / Receipt %d", cmtRepo, chkHead, chkAppID) {
				t.Fatalf("readback %v", got)
			}
		})
	}
}

func TestReceiptCheckPublisherMarksTheOwnersUnblockOfAReceiptThatDidNotRun(t *testing.T) {
	d := rcpDecision(execution.CheckNeutral, receiptNotRun)
	d.check.Unblocked = true
	w := newRcpWorld()
	if err := w.deliver(rcpMessage(t, d)); err != nil {
		t.Fatal(err)
	}
	posts := w.runner.posts()
	if len(posts) != 1 {
		t.Fatalf("%d posts, want 1", len(posts))
	}
	p := posts[0]
	if p.Conclusion != "neutral" || !strings.Contains(p.Summary, "The owner unblocked this commit. Its receipt could not run, so the tests of this change did not run.") {
		t.Fatalf("posted %+v", p)
	}
}

func TestReceiptCheckPublisherDropsAStaleGeneration(t *testing.T) {
	for _, current := range []int64{4, 2} { // the ledger moved on, or is behind
		w := newRcpWorld()
		w.ledger.record.Generation = current
		if err := w.deliver(rcpGood(t)); err != nil {
			t.Fatalf("generation %d: %v", current, err)
		}
		if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 {
			t.Fatalf("generation %d: GitHub was called for a stale message", current)
		}
	}
}

// A stale message is dropped before it is rendered, so a reason that no longer exists does not fail it.
func TestReceiptCheckPublisherDropsAStaleMessageOfAReasonThatNoLongerExists(t *testing.T) {
	w := newRcpWorld()
	w.ledger.record.Generation = 4
	m := rcpGood(t)
	m.Payload = bytes.Replace(m.Payload, []byte("receipt-complete"), []byte("receipt-green"), 1)
	if err := w.deliver(m); err != nil {
		t.Fatalf("a stale message of a removed reason must be dropped: %v", err)
	}
	if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 {
		t.Fatal("GitHub was called for a stale message")
	}
}

func TestReceiptCheckPublisherErrorsWhenTheLedgerCannotConfirmTheGeneration(t *testing.T) {
	m := rcpGood(t)
	for name, set := range map[string]func(*fakeReceiptLedger){
		"no record":    func(l *fakeReceiptLedger) { l.found = false },
		"a read error": func(l *fakeReceiptLedger) { l.err = errors.New("connection reset") },
	} {
		t.Run(name, func(t *testing.T) {
			w := newRcpWorld()
			set(w.ledger)
			err := w.deliver(m)
			if err == nil || !strings.Contains(err.Error(), m.Key) {
				t.Fatalf("err = %v, want one naming the key", err)
			}
			if len(w.runner.posts()) != 0 {
				t.Fatal("a run was posted without a confirmed generation")
			}
		})
	}
}

func TestReceiptCheckPublisherPostsOnceAcrossRedeliveries(t *testing.T) {
	w := newRcpWorld()
	m := rcpGood(t)
	for range 3 {
		if err := w.deliver(m); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(w.runner.posts()); got != 1 {
		t.Fatalf("%d posts after 3 deliveries, want 1", got)
	}
}

func TestReceiptCheckPublisherPostsWhenTheReadbackHoldsAnotherRun(t *testing.T) {
	m := rcpGood(t)
	w := newRcpWorld()
	w.runner.runs = []github.CheckRunSummary{
		{ID: 1, Status: "completed", Conclusion: "success", ExternalID: "suiteward:" + cmtOtherInst + ":" + m.Key},              // another instance, same key
		{ID: 2, Status: "completed", Conclusion: "success", ExternalID: "suiteward:" + cmtInstance + ":receipt-check:1001:7:2"}, // this instance, an earlier generation
		{ID: 3, Status: "completed", Conclusion: "success", ExternalID: "suiteward:" + cmtInstance + ":check:1001:7:3"},         // this instance, the Contract check
		{ID: 4, Status: "in_progress", ExternalID: ""},
	}
	if err := w.deliver(m); err != nil {
		t.Fatal(err)
	}
	if got := len(w.runner.posts()); got != 1 {
		t.Fatalf("%d posts, want 1", got)
	}
}

// A head that no longer exists answers 422 to the readback, which comes first, and to the post.
func TestReceiptCheckPublisherTreatsAVanishedHeadAsDelivered(t *testing.T) {
	for name, set := range map[string]func(*fakeChecks, error){
		"the readback": func(f *fakeChecks, err error) { f.runsErr = err },
		"the post":     func(f *fakeChecks, err error) { f.postErr = err },
	} {
		t.Run(name, func(t *testing.T) {
			w := newRcpWorld()
			set(w.runner, fmt.Errorf("%w: %s", &rest.StatusError{Status: 422}, cmtLeakText))
			m := rcpGood(t)
			if err := w.deliver(m); err != nil {
				t.Fatalf("a 422 is an error: %v", err)
			}
			if len(w.runner.posts()) != 0 {
				t.Fatal("a run was posted for a head that does not exist")
			}
			logged := w.logs.String()
			if !strings.Contains(logged, m.Key) || !strings.Contains(logged, chkHead) || strings.Contains(logged, cmtLeakText) {
				t.Fatalf("the log should name the key and the head, never GitHub's text: %q", logged)
			}
		})
	}
}

func TestReceiptCheckPublisherReturnsGitHubErrorsAsStatusOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(f *fakeChecks, err error)
		err    error
		status string
	}{
		{"post answered with a status", func(f *fakeChecks, err error) { f.postErr = err }, fmt.Errorf("%w: %s", &rest.StatusError{Status: 403}, cmtLeakText), "HTTP 403"},
		{"readback answered with a status", func(f *fakeChecks, err error) { f.runsErr = err }, fmt.Errorf("%w: %s", &rest.StatusError{Status: 502}, cmtLeakText), "HTTP 502"},
		{"rate limited", func(f *fakeChecks, err error) { f.postErr = err }, fmt.Errorf("%w: %s", &rest.RateLimitedError{Status: 429}, cmtLeakText), "rate_limited"},
		{"transport failure", func(f *fakeChecks, err error) { f.runsErr = err }, fmt.Errorf("Get https://api.example/%s: connection refused", cmtLeakText), "transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRcpWorld()
			tc.set(w.runner, tc.err)
			m := rcpGood(t)
			err := w.deliver(m)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			text := err.Error()
			if strings.Contains(text, cmtLeakText) || !strings.Contains(text, tc.status) || !strings.Contains(text, "github_receipt_check "+m.Key) {
				t.Fatalf("the error %q holds text GitHub sent, or lacks the kind, the key or %q", text, tc.status)
			}
			if strings.Contains(w.logs.String(), cmtLeakText) {
				t.Fatalf("the log holds text GitHub sent: %q", w.logs.String())
			}
		})
	}
}

// With no receipt, nothing is published (domain.ConclusionNone is no GitHub value): a message of a state that is none of the four is refused when it is delivered, and
// GitHub is never called.
func TestTheReceiptCheckNeverSendsAStateThatIsNoGitHubConclusion(t *testing.T) {
	for _, state := range []execution.CheckState{"", "none", "cancelled", "skipped", "queued", cmtLeakText} {
		w := newRcpWorld()
		m := rcpGood(t)
		m.Payload = bytes.Replace(m.Payload, []byte(`"state":"success"`), []byte(`"state":"`+string(state)+`"`), 1)
		err := w.deliver(m)
		if err == nil || strings.Contains(err.Error(), cmtLeakText) {
			t.Fatalf("state %q: err = %v, want one that quotes nothing", state, err)
		}
		if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 {
			t.Fatalf("state %q: GitHub was called", state)
		}
	}
}

func TestReceiptCheckPublisherRefusesAPayloadThatDoesNotMatchItsKey(t *testing.T) {
	good := rcpGood(t)
	with := func(change func(map[string]any)) json.RawMessage {
		var m map[string]any
		if err := json.Unmarshal(good.Payload, &m); err != nil {
			t.Fatal(err)
		}
		change(m)
		return cmtMust(json.Marshal(m))
	}
	same := with(func(map[string]any) {})
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"an unknown field", good.Key, with(func(m map[string]any) { m["body"] = cmtLeakText })},
		{"a payload that is not JSON", good.Key, json.RawMessage(`{"repositoryId": ` + cmtLeakText)},
		{"a payload that is not an object", good.Key, json.RawMessage(`"` + cmtLeakText + `"`)},
		{"trailing data", good.Key, json.RawMessage(string(same) + cmtLeakText)},
		{"a tier that is not a tier", good.Key, with(func(m map[string]any) { m["tier"] = cmtLeakText })},
		{"a head that is not a SHA", good.Key, with(func(m map[string]any) { m["headSha"] = cmtLeakText })},
		{"a generation of zero", "receipt-check:1001:7:0", with(func(m map[string]any) { m["generation"] = 0 })},
		{"a repository that is not owner/name", good.Key, with(func(m map[string]any) { m["repo"] = cmtLeakText })},
		{"a count that is missing", good.Key, with(func(m map[string]any) { delete(m["counts"].(map[string]any), "unparsed") })},
		{"every count missing", good.Key, with(func(m map[string]any) { delete(m, "counts") })},
		{"a count that is not a number", good.Key, with(func(m map[string]any) { m["counts"].(map[string]any)["detects"] = cmtLeakText })},
		{"an unknown count", good.Key, with(func(m map[string]any) { m["counts"].(map[string]any)["passes"] = 1 })},
		{"another repository in the key", "receipt-check:1002:7:3", same},
		{"another pull in the key", "receipt-check:1001:8:3", same},
		{"another generation in the key", "receipt-check:1001:7:2", same},
		{"a check key", "check:1001:7:3", same},
		{"a key with three parts", "receipt-check:1001:7", same},
		{"a key with five parts", "receipt-check:1001:7:3:1", same},
		{"a generation that is not a number", "receipt-check:1001:7:x", same},
		{"a number with a leading zero", "receipt-check:1001:07:3", same},
		{"an empty key", "", same},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRcpWorld()
			err := w.pub.publish(context.Background(), tc.key, tc.payload)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			for _, leak := range []string{cmtLeakText, "SECRET", cmtRepo, chkHead} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the error %q holds payload text %q", err, leak)
				}
			}
			if tc.key != "" && !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("the error %q does not name the key %q", err, tc.key)
			}
			if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 || w.ledger.reads != 0 {
				t.Fatal("GitHub or the ledger was called for a payload that was refused")
			}
		})
	}
}

// A reason nobody can render, or one that does not fit the state or the counts, is refused only for the current generation, after the ledger confirmed it.
func TestReceiptCheckPublisherRefusesACurrentMessageThatCannotBeRendered(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"a reason nobody can render": func(m map[string]any) { m["reason"] = cmtLeakText },
		"a reason of another state":  func(m map[string]any) { m["state"] = "failure" },
		"an unavailable reason that is none": func(m map[string]any) {
			m["reason"], m["state"], m["unavailable"] = "receipt-not-run", "neutral", cmtLeakText
		},
		"counts that a running receipt has": func(m map[string]any) { m["reason"], m["state"] = "receipt-running", "in_progress" },
		"an unblock of a complete receipt":  func(m map[string]any) { m["unblocked"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			w := newRcpWorld()
			m := rcpGood(t)
			var payload map[string]any
			if err := json.Unmarshal(m.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			edit(payload)
			m.Payload = cmtMust(json.Marshal(payload))
			err := w.deliver(m)
			if err == nil || strings.Contains(err.Error(), cmtLeakText) || !strings.Contains(err.Error(), m.Key) {
				t.Fatalf("err = %v, want one that names the key and quotes nothing", err)
			}
			if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 {
				t.Fatal("GitHub was called for a payload that was refused")
			}
		})
	}
}

func TestReceiptCheckPublisherResolvesTheInstanceOnceAndRetriesAFailure(t *testing.T) {
	w := newRcpWorld()
	calls := 0
	w.pub.instance = newLazy(func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("database unreachable")
		}
		return cmtInstance, nil
	})
	m := rcpGood(t)
	if err := w.deliver(m); err == nil || !strings.Contains(err.Error(), m.Key) {
		t.Fatalf("err = %v, want one naming the key", err)
	}
	if len(w.runner.posts()) != 0 {
		t.Fatal("a run was posted without an instance id")
	}
	for range 2 {
		if err := w.deliver(m); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("the instance id was read %d times, want a retry and then one cached read", calls)
	}
}

// The Receipt check is a check run of its own: the readback looks for its name, and the Contract check's runs are no reason to skip it.
func TestReceiptCheckPublisherAsksForTheRunsOfItsOwnName(t *testing.T) {
	w := newRcpWorld()
	if err := w.deliver(rcpGood(t)); err != nil {
		t.Fatal(err)
	}
	posts := w.runner.posts()
	if len(posts) != 1 || posts[0].Name != github.ReceiptCheckName {
		t.Fatalf("posted %+v, want one run named %q", posts, github.ReceiptCheckName)
	}
}

// A message that the renderer refuses is refused when it is built, as every github_check message is: the checks step fails, instead of every delivery of the message.
func TestNewReceiptCheckMessageRefusesWhatTheRendererRefuses(t *testing.T) {
	for name, edit := range map[string]func(*receiptDecision){
		"a state that is none":                   func(d *receiptDecision) { d.check.State = "" },
		"a state that is no GitHub conclusion":   func(d *receiptDecision) { d.check.State = "none" },
		"a reason of another state":              func(d *receiptDecision) { d.check.State = execution.CheckFailure },
		"a reason nobody can render":             func(d *receiptDecision) { d.check.Reason = "receipt-green" },
		"counts on a running receipt":            func(d *receiptDecision) { d.check.State, d.check.Reason = execution.CheckInProgress, receiptRunning },
		"a complete receipt with a failing test": func(d *receiptDecision) { d.counts.Fails = 1 },
		"a failing receipt that blocks nothing": func(d *receiptDecision) {
			d.check.State, d.check.Reason, d.counts = execution.CheckFailure, receiptFailing, receiptCheckCounts{Detects: 1}
		},
		"a receipt that was not run, with no reason": func(d *receiptDecision) {
			d.check.State, d.check.Reason, d.counts = execution.CheckNeutral, receiptNotRun, receiptCheckCounts{}
		},
		"a receipt that was not run, for a reason that is none": func(d *receiptDecision) {
			d.check.State, d.check.Reason, d.counts, d.unavailable = execution.CheckNeutral, receiptNotRun, receiptCheckCounts{}, "the-moon"
		},
		"an unblock of a complete receipt":  func(d *receiptDecision) { d.check.Unblocked = true },
		"a negative count":                  func(d *receiptDecision) { d.counts.Inert = -1 },
		"a reference that is not P<n>-R<m>": func(d *receiptDecision) { d.check.Reference = "P1" },
	} {
		t.Run(name, func(t *testing.T) {
			d := rcpDecision(execution.CheckSuccess, receiptComplete)
			edit(&d)
			if m, err := newReceiptCheckMessage(cmtRepo, d, owner.TierIsolated, 3); err == nil {
				t.Fatalf("built %s", m.Payload)
			}
		})
	}
}

// A receipt that waits for a runner that does not respond: the decision says whether the receipt gate would refuse an approve, and the summary offers the unblock then
// and only then.
func TestReceiptCheckPublisherPostsTheSummaryOfARunnerThatDoesNotRespond(t *testing.T) {
	for _, offers := range []bool{true, false} {
		t.Run(fmt.Sprintf("offers the unblock %v", offers), func(t *testing.T) {
			d := rcpDecision(execution.CheckInProgress, "receipt-not-responding")
			d.offersUnblock = offers
			w := newRcpWorld()
			if err := w.deliver(rcpMessage(t, d)); err != nil {
				t.Fatal(err)
			}
			posts := w.runner.posts()
			if len(posts) != 1 || posts[0].Name != "SuiteWard / Receipt" || posts[0].Status != "in_progress" || posts[0].Title != "Receipt waiting: test runner not responding" {
				t.Fatalf("posted %+v", posts)
			}
			for _, phrase := range []string{"has not answered for about half a minute", "They are evidence, not a verdict.", "`isolated`"} {
				if !strings.Contains(posts[0].Summary, phrase) {
					t.Errorf("the summary lacks %q:\n%s", phrase, posts[0].Summary)
				}
			}
			if offered := strings.Contains(posts[0].Summary, "/suiteward unblock <code>"); offered != offers {
				t.Errorf("the summary offers the unblock = %v, want %v:\n%s", offered, offers, posts[0].Summary)
			}
		})
	}
}
