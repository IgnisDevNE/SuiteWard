package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

const (
	chkHead  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	chkAppID = int64(4711)
)

// fakeChecks is the check runs of one head: a posted run is listed afterwards, as GitHub does.
type fakeChecks struct {
	mu      sync.Mutex
	runs    []github.CheckRunSummary
	posted  []github.CheckRun
	listed  []string // repo, sha, name and app id of every readback
	postErr error
	runsErr error
}

func (f *fakeChecks) Post(_ context.Context, _ string, run github.CheckRun) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return 0, f.postErr
	}
	f.posted = append(f.posted, run)
	id := int64(100 + len(f.runs))
	f.runs = append(f.runs, github.CheckRunSummary{ID: id, Status: run.Status, Conclusion: run.Conclusion, ExternalID: run.ExternalID})
	return id, nil
}

func (f *fakeChecks) Runs(_ context.Context, repo, sha, name string, appID int64) ([]github.CheckRunSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, fmt.Sprintf("%s %s %s %d", repo, sha, name, appID))
	if f.runsErr != nil {
		return nil, f.runsErr
	}
	return append([]github.CheckRunSummary(nil), f.runs...), nil
}

func (f *fakeChecks) posts() []github.CheckRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]github.CheckRun(nil), f.posted...)
}

type fakeLedger struct {
	record checks.Record
	found  bool
	err    error
	reads  int
}

func (l *fakeLedger) Current(_ context.Context, repositoryID int64, pull int) (checks.Record, bool, error) {
	l.reads++
	if repositoryID != cmtRepoID || pull != cmtPull {
		return checks.Record{}, false, nil
	}
	return l.record, l.found, l.err
}

type chkWorld struct {
	runner *fakeChecks
	ledger *fakeLedger
	pub    *checkPublisher
	logs   *bytes.Buffer
}

func newChkWorld() *chkWorld {
	w := &chkWorld{runner: &fakeChecks{}, ledger: &fakeLedger{found: true, record: checks.Record{Generation: 3}}, logs: &bytes.Buffer{}}
	w.pub = &checkPublisher{runs: w.runner, ledger: w.ledger, appID: chkAppID, logger: slog.New(slog.NewTextHandler(w.logs, nil)),
		instance: newLazy(func(context.Context) (string, error) { return cmtInstance, nil })}
	return w
}

// chkMessage is the message of generation 3 for a result in the given state.
func chkMessage(t *testing.T, state checks.State, reason, reference string) governance.OutboxMessage {
	t.Helper()
	version := "V2" // no canonical version before the first contract
	if strings.HasPrefix(reason, "bootstrap-") {
		version = ""
	}
	m, err := newCheckMessage(cmtRepo, checks.Result{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: state, Reason: reason, Reference: reference}, owner.TierIsolated, version, 3)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (w *chkWorld) deliver(m governance.OutboxMessage) error {
	return w.pub.publish(context.Background(), m.Key, m.Payload)
}

func TestNewCheckMessageBuildsTheKeyAndThePayload(t *testing.T) {
	m := chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")
	if m.Key != "check:1001:7:3" || m.Kind != "github_check" {
		t.Fatalf("key %q, kind %q", m.Key, m.Kind)
	}
	var got map[string]any
	if err := json.Unmarshal(m.Payload, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"repositoryId": 1001.0, "repo": cmtRepo, "pull": 7.0, "headSha": chkHead, "generation": 3.0, "state": "in_progress",
		"reason": "approval-missing", "reference": "P1-R2", "tier": "isolated", "version": "V2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload %v, want %v", got, want)
	}
	bare := cmtMust(newCheckMessage(cmtRepo, checks.Result{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: checks.Success, Reason: "unchanged"}, owner.TierCoLocated, "", 1))
	got = nil
	if err := json.Unmarshal(bare.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["reference"]; has || got["tier"] != "co-located" || bare.Key != "check:1001:7:1" {
		t.Fatalf("a check without a reference or a version: key %q, payload %v", bare.Key, got)
	}
	if _, has := got["version"]; has {
		t.Fatalf("an empty version is in the payload: %v", got)
	}
}

func TestNewCheckMessageRefusesWhatCannotBePublished(t *testing.T) {
	good := checks.Result{RepositoryID: cmtRepoID, PullNumber: cmtPull, HeadSHA: chkHead, State: checks.InProgress, Reason: "approval-missing", Reference: "P1-R2"}
	for _, tc := range []struct {
		name       string
		mutate     func(r *checks.Result)
		repo, tier string
		version    string
		generation int64
	}{
		{"an unknown state", func(r *checks.Result) { r.State = "neutral" }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"an empty state", func(r *checks.Result) { r.State = "" }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"an empty reason", func(r *checks.Result) { r.Reason = "" }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"a reason nobody can render", func(r *checks.Result) { r.Reason = "tests-failed" }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"a reason of another state", func(r *checks.Result) { r.State = checks.Failure }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"a short head", func(r *checks.Result) { r.HeadSHA = "abc123" }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"an uppercase head", func(r *checks.Result) { r.HeadSHA = strings.ToUpper(chkHead) }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"a generation of zero", func(*checks.Result) {}, cmtRepo, owner.TierIsolated, "V1", 0},
		{"a negative generation", func(*checks.Result) {}, cmtRepo, owner.TierIsolated, "V1", -1},
		{"no repository id", func(r *checks.Result) { r.RepositoryID = 0 }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"no pull number", func(r *checks.Result) { r.PullNumber = 0 }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"a repository that is not owner/name", func(*checks.Result) {}, "widgets", owner.TierIsolated, "V1", 1},
		{"no tier", func(*checks.Result) {}, cmtRepo, "", "V1", 1},
		{"an unknown tier", func(*checks.Result) {}, cmtRepo, "shared", "V1", 1},
		{"a reference that is not P<n>-R<m>", func(r *checks.Result) { r.Reference = "P1" }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"approval missing without a reference", func(r *checks.Result) { r.Reference = "" }, cmtRepo, owner.TierIsolated, "V1", 1},
		{"a version that is not V<n>", func(*checks.Result) {}, cmtRepo, owner.TierIsolated, "1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			tc.mutate(&r)
			if m, err := newCheckMessage(tc.repo, r, tc.tier, tc.version, tc.generation); err == nil {
				t.Fatalf("built %s", m.Payload)
			}
		})
	}
}

func TestCheckPublisherPostsEveryState(t *testing.T) {
	for _, tc := range []struct {
		state              checks.State
		reason, reference  string
		title              string
		status, conclusion string
	}{
		{checks.Success, "unchanged", "", "No protected change", "completed", "success"},
		{checks.Success, "ready", "P1-R2", "Ready for integration", "completed", "success"},
		{checks.Failure, "invalid-source", "", "Invalid contract source", "completed", "failure"},
		{checks.Failure, "integrity-failed", "P1-R2", "Canonical contract failed its integrity check", "completed", "failure"},
		{checks.Failure, "owner-command-rejected", "P1-R2", "Owner command rejected", "completed", "failure"},
		{checks.InProgress, "bootstrap-awaiting", "", "No canonical contract yet", "in_progress", ""},
		{checks.Success, "bootstrap-skipped", "", "Skipped before the first contract", "completed", "success"},
		{checks.InProgress, "approval-missing", "P1-R2", "Awaiting owner approval", "in_progress", ""},
		{checks.InProgress, "not-current", "P1-R2", "Contract change is not current", "in_progress", ""},
		{checks.InProgress, "assessment-pending", "", "Assessment pending", "in_progress", ""},
		{checks.InProgress, "source-unavailable", "", "Source unavailable", "in_progress", ""},
		{checks.InProgress, "fork-unsupported", "", "Pull requests from forks are not supported", "in_progress", ""},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			w := newChkWorld()
			m := chkMessage(t, tc.state, tc.reason, tc.reference)
			if err := w.deliver(m); err != nil {
				t.Fatal(err)
			}
			posts := w.runner.posts()
			if len(posts) != 1 {
				t.Fatalf("%d posts, want 1", len(posts))
			}
			p := posts[0]
			if p.Name != "SuiteWard / Contract" || p.HeadSHA != chkHead || p.Status != tc.status || p.Conclusion != tc.conclusion {
				t.Fatalf("posted %+v", p)
			}
			if p.ExternalID != "suiteward:"+cmtInstance+":"+m.Key {
				t.Fatalf("external id %q", p.ExternalID)
			}
			if p.Title != tc.title || !strings.Contains(p.Summary, "`isolated`") || !strings.Contains(p.Summary, "does not run tests") {
				t.Fatalf("title %q, summary %q", p.Title, p.Summary)
			}
			if has := strings.Contains(p.Summary, "/suiteward approve P1-R2 <code>"); has != (tc.reason == "approval-missing") {
				t.Fatalf("approve command present = %v:\n%s", has, p.Summary)
			}
			if got := w.runner.listed; len(got) != 1 || got[0] != fmt.Sprintf("%s %s SuiteWard / Contract %d", cmtRepo, chkHead, chkAppID) {
				t.Fatalf("readback %v", got)
			}
		})
	}
}

func TestCheckPublisherDropsAStaleGeneration(t *testing.T) {
	for _, current := range []int64{4, 2} { // the ledger moved on, or is behind
		w := newChkWorld()
		w.ledger.record.Generation = current
		if err := w.deliver(chkMessage(t, checks.InProgress, "assessment-pending", "")); err != nil {
			t.Fatalf("generation %d: %v", current, err)
		}
		if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 {
			t.Fatalf("generation %d: GitHub was called for a stale message", current)
		}
	}
}

// An upgrade can leave a pending message of a reason the renderer no longer knows (bootstrap-pending was removed in the M1.7b
// rework): once a newer generation superseded it, it is dropped, not failed.
func TestCheckPublisherDropsAStaleMessageOfAReasonThatNoLongerExists(t *testing.T) {
	w := newChkWorld()
	w.ledger.record.Generation = 4
	m := chkMessage(t, checks.InProgress, "assessment-pending", "")
	m.Payload = bytes.Replace(m.Payload, []byte("assessment-pending"), []byte("bootstrap-pending"), 1)
	if err := w.deliver(m); err != nil {
		t.Fatalf("a stale message of a removed reason must be dropped: %v", err)
	}
	if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 {
		t.Fatal("GitHub was called for a stale message")
	}
}

func TestCheckPublisherErrorsWhenTheLedgerCannotConfirmTheGeneration(t *testing.T) {
	m := chkMessage(t, checks.InProgress, "assessment-pending", "")
	for name, set := range map[string]func(*fakeLedger){
		"no record":    func(l *fakeLedger) { l.found = false },
		"a read error": func(l *fakeLedger) { l.err = errors.New("connection reset") },
	} {
		t.Run(name, func(t *testing.T) {
			w := newChkWorld()
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

func TestCheckPublisherPostsOnceAcrossRedeliveries(t *testing.T) {
	w := newChkWorld()
	m := chkMessage(t, checks.Success, "unchanged", "")
	for range 3 {
		if err := w.deliver(m); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(w.runner.posts()); got != 1 {
		t.Fatalf("%d posts after 3 deliveries, want 1", got)
	}
}

func TestCheckPublisherPostsWhenTheReadbackHoldsAnotherRun(t *testing.T) {
	m := chkMessage(t, checks.InProgress, "assessment-pending", "")
	w := newChkWorld()
	w.runner.runs = []github.CheckRunSummary{
		{ID: 1, Status: "completed", Conclusion: "success", ExternalID: "suiteward:" + cmtOtherInst + ":" + m.Key},      // another instance, same key
		{ID: 2, Status: "completed", Conclusion: "success", ExternalID: "suiteward:" + cmtInstance + ":check:1001:7:2"}, // this instance, an earlier generation
		{ID: 3, Status: "in_progress", ExternalID: ""},
	}
	if err := w.deliver(m); err != nil {
		t.Fatal(err)
	}
	if got := len(w.runner.posts()); got != 1 {
		t.Fatalf("%d posts, want 1", got)
	}
}

// A head that no longer exists answers 422 to the readback, which comes first, and to the post.
func TestCheckPublisherTreatsAVanishedHeadAsDelivered(t *testing.T) {
	for name, set := range map[string]func(*fakeChecks, error){
		"the readback": func(f *fakeChecks, err error) { f.runsErr = err },
		"the post":     func(f *fakeChecks, err error) { f.postErr = err },
	} {
		t.Run(name, func(t *testing.T) {
			w := newChkWorld()
			set(w.runner, fmt.Errorf("%w: %s", &rest.StatusError{Status: 422}, cmtLeakText))
			m := chkMessage(t, checks.InProgress, "assessment-pending", "")
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

func TestCheckPublisherReturnsGitHubErrorsAsStatusOnly(t *testing.T) {
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
			w := newChkWorld()
			tc.set(w.runner, tc.err)
			m := chkMessage(t, checks.InProgress, "assessment-pending", "")
			err := w.deliver(m)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			text := err.Error()
			if strings.Contains(text, cmtLeakText) || !strings.Contains(text, tc.status) || !strings.Contains(text, "github_check "+m.Key) {
				t.Fatalf("the error %q holds text GitHub sent, or lacks the kind, the key or %q", text, tc.status)
			}
			if strings.Contains(w.logs.String(), cmtLeakText) {
				t.Fatalf("the log holds text GitHub sent: %q", w.logs.String())
			}
		})
	}
}

func TestCheckPublisherRefusesAPayloadThatDoesNotMatchItsKey(t *testing.T) {
	good := chkMessage(t, checks.InProgress, "approval-missing", "P1-R2")
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
		{"an unknown state", good.Key, with(func(m map[string]any) { m["state"] = cmtLeakText })},
		{"a tier that is not a tier", good.Key, with(func(m map[string]any) { m["tier"] = cmtLeakText })},
		{"a head that is not a SHA", good.Key, with(func(m map[string]any) { m["headSha"] = cmtLeakText })},
		{"another repository in the key", "check:1002:7:3", same},
		{"another pull in the key", "check:1001:8:3", same},
		{"another generation in the key", "check:1001:7:2", same},
		{"a comment key", "ack:1001:7:3", same},
		{"a key with three parts", "check:1001:7", same},
		{"a key with five parts", "check:1001:7:3:1", same},
		{"a generation that is not a number", "check:1001:7:x", same},
		{"a number with a leading zero", "check:1001:07:3", same},
		{"an empty key", "", same},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newChkWorld()
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

// A reason nobody can render is refused only for the current generation, after the ledger confirmed it.
func TestCheckPublisherRefusesACurrentMessageWhoseReasonNobodyCanRender(t *testing.T) {
	w := newChkWorld()
	m := chkMessage(t, checks.InProgress, "assessment-pending", "")
	m.Payload = bytes.Replace(m.Payload, []byte("assessment-pending"), []byte(cmtLeakText), 1)
	err := w.deliver(m)
	if err == nil {
		t.Fatal("publish succeeded")
	}
	if strings.Contains(err.Error(), cmtLeakText) || !strings.Contains(err.Error(), m.Key) {
		t.Fatalf("the error %q holds payload text or does not name the key", err)
	}
	if len(w.runner.posts()) != 0 || len(w.runner.listed) != 0 {
		t.Fatal("GitHub was called for a payload that was refused")
	}
}

func TestCheckPublisherResolvesTheInstanceOnceAndRetriesAFailure(t *testing.T) {
	w := newChkWorld()
	calls := 0
	w.pub.instance = newLazy(func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("database unreachable")
		}
		return cmtInstance, nil
	})
	m := chkMessage(t, checks.InProgress, "assessment-pending", "")
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
