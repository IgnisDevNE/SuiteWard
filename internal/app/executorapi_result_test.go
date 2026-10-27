package app

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// body is a result as an executor sends it, open to the changes of a test.
type body map[string]any

var (
	execBinary = strings.Repeat("a", 64)
	execImage  = "sha256:" + strings.Repeat("b", 64)
)

func (e *execTestEnv) window(startedAgo, endedAgo time.Duration) (string, string) {
	now := e.clock.Now()
	return now.Add(-startedAgo).UTC().Format(time.RFC3339), now.Add(-endedAgo).UTC().Format(time.RFC3339)
}

// complete is a valid complete result for the job: one test of each plan's kind unless the test is changed.
func (e *execTestEnv) complete(job execution.Job) body {
	started, ended := e.window(2*time.Minute, 0)
	b := body{
		"status": "complete", "head": body{"sha": job.HeadSHA}, "executor": body{"version": "0.1.0", "binarySha256": execBinary},
		"sandbox":   body{"imageDigest": execImage, "toolchain": "go1.27.3", "runtime": "podman 5.5.0"},
		"startedAt": started, "endedAt": ended,
	}
	if job.Plan == execution.PlanReceipt {
		b["base"] = body{"sha": job.BaseSHA}
		b["tests"] = []body{{"package": ".", "name": "TestNew", "file": "new_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"fail"}}}
	} else {
		b["tests"] = []body{{"package": ".", "name": "TestContract", "file": "contract_test.go", "kind": "declared", "head": []string{"pass"}}}
	}
	return b
}

// unavailable is a valid unavailable result with the reason.
func (e *execTestEnv) unavailable(job execution.Job, reason string) body {
	b := e.complete(job)
	b["status"], b["reason"] = "unavailable", reason
	delete(b, "tests")
	delete(b, "sandbox")
	return b
}

// post sends a result for the job with the lease: a body is encoded, bytes and strings go as they are.
func (e *execTestEnv) post(job execution.Job, lease string, payload any, mods ...execMod) execReply {
	e.t.Helper()
	var raw []byte
	switch p := payload.(type) {
	case []byte:
		raw = p
	case string:
		raw = []byte(p)
	default:
		raw = execJSON(e.t, p)
	}
	return e.send(http.MethodPost, e.jobPath(job, "/result"), raw, append([]execMod{withLease(lease)}, mods...)...)
}

func (e *execTestEnv) wantState(r execReply, state string) {
	e.t.Helper()
	if r.status != http.StatusOK || strings.TrimSpace(r.text()) != fmt.Sprintf(`{"state":%q}`, state) {
		e.t.Errorf("answer = %d %s; want 200 {\"state\":%q}", r.status, r.text(), state)
	}
}

// untouched asserts that a refused result left the job leased with its lease, stored nothing and woke nothing.
func (e *execTestEnv) untouched(job execution.Job, leased execution.Job) {
	e.t.Helper()
	stored := e.job(job.ID)
	if stored.State != execution.StateLeased || stored.LeaseID != leased.LeaseID || stored.Failures != 0 {
		e.t.Errorf("job = %+v; want it still leased with its lease", stored)
	}
	if _, found, err := e.store.Result(e.t.Context(), job.ID); err != nil || found {
		e.t.Errorf("result stored = %v, %v; want none", found, err)
	}
	if n := e.wakes.count.Load(); n != 0 {
		e.t.Errorf("wake-ups = %d; want none", n)
	}
}

// --- the order of the checks ---

func TestResultIsRefusedOverFourMiBBeforeTheJobIsLooked(t *testing.T) {
	e := newExecEnv(t)
	unknown := execution.Job{ID: 99}
	if got := e.post(unknown, "ffffffffffffffffffffffffffffffff", bytes.Repeat([]byte(" "), 4<<20)); got.status != http.StatusNotFound {
		t.Errorf("a body of 4 MiB for an unknown job = %d %s; want 404: the size is within the limit", got.status, got.text())
	}
	e.wantError(e.post(unknown, "ffffffffffffffffffffffffffffffff", bytes.Repeat([]byte(" "), 4<<20+1)), http.StatusRequestEntityTooLarge, "too-large")
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	e.wantError(e.post(leased, leased.LeaseID, bytes.Repeat([]byte(" "), 4<<20+1)), http.StatusRequestEntityTooLarge, "too-large")
	e.untouched(leased, leased)
}

func TestResultAnswers404ForAnUnknownJob(t *testing.T) {
	e := newExecEnv(t)
	for _, path := range []string{"/jobs/99/result", "/jobs/0/result", "/jobs/x/result"} {
		e.wantError(e.send(http.MethodPost, path, []byte(`{}`), withLease("ffffffffffffffffffffffffffffffff")), http.StatusNotFound, "not-found")
	}
}

// The lease is checked before the body is decoded: a lost lease is 409 whatever the body is.
func TestResultChecksTheLeaseBeforeDecodingTheBody(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for name, lease := range map[string]string{"another lease": "ffffffffffffffffffffffffffffffff", "no lease": "", "not a lease": "garbage"} {
		for _, payload := range []any{"this is not JSON", e.complete(leased)} {
			var got execReply
			if lease == "" {
				got = e.send(http.MethodPost, e.jobPath(leased, "/result"), execJSONOrRaw(t, payload))
			} else {
				got = e.post(leased, lease, payload)
			}
			if got.status != http.StatusConflict || strings.TrimSpace(got.text()) != `{"error":"lease-lost"}` {
				t.Errorf("%s with %T = %d %s; want 409 lease-lost", name, payload, got.status, got.text())
			}
		}
	}
	e.clock.Advance(execution.LeaseTTL) // the lease runs out
	e.wantError(e.post(leased, leased.LeaseID, e.complete(leased)), http.StatusConflict, "lease-lost")
	e.untouched(leased, leased)
}

func execJSONOrRaw(t *testing.T, payload any) []byte {
	t.Helper()
	if s, ok := payload.(string); ok {
		return []byte(s)
	}
	return execJSON(t, payload)
}

func TestResultRefusesWhatIsNotAStrictObject(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	valid := string(execJSON(t, e.complete(leased)))
	for name, payload := range map[string]string{
		"an empty body": "", "not JSON": "result", "an array": "[]", "null": "null", "a string": `"complete"`, "an unknown field": valid[:len(valid)-1] + `,"extra":1}`,
		"an unknown nested field": strings.Replace(valid, `"head":{"sha"`, `"head":{"tree":[],"sha"`, 1), "a trailing value": valid + valid, "trailing text": valid + "x",
		"a number for a string": strings.Replace(valid, `"status":"complete"`, `"status":1`, 1), "invalid UTF-8": strings.Replace(valid, "go1.27.3", "go1.\xff.3", 1),
	} {
		got := e.post(leased, leased.LeaseID, payload)
		if got.status != http.StatusBadRequest || strings.TrimSpace(got.text()) != `{"error":"invalid"}` {
			t.Errorf("%s = %d %s; want 400 invalid", name, got.status, got.text())
		}
	}
	e.untouched(leased, leased)
}

func TestResultHeadsMustBeTheJobs(t *testing.T) {
	for name, edit := range map[string]func(execution.Job, body){
		"another head":                   func(_ execution.Job, b body) { b["head"] = body{"sha": execSHA(77)} },
		"an upper-case head":             func(j execution.Job, b body) { b["head"] = body{"sha": strings.ToUpper(j.HeadSHA)} },
		"no head":                        func(_ execution.Job, b body) { delete(b, "head") },
		"an empty head":                  func(_ execution.Job, b body) { b["head"] = body{} },
		"a canonical result with a base": func(j execution.Job, b body) { b["base"] = body{"sha": j.HeadSHA} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			leased := e.grant()
			b := e.complete(leased)
			edit(leased, b)
			e.wantError(e.post(leased, leased.LeaseID, b), http.StatusBadRequest, "invalid")
			e.untouched(leased, leased)
		})
	}
	for name, edit := range map[string]func(execution.Job, body){
		"another base":         func(_ execution.Job, b body) { b["base"] = body{"sha": execSHA(77)} },
		"no base":              func(_ execution.Job, b body) { delete(b, "base") },
		"the head as the base": func(j execution.Job, b body) { b["base"] = body{"sha": j.HeadSHA} },
	} {
		t.Run("receipt: "+name, func(t *testing.T) {
			e := newExecEnv(t)
			e.receiptJob(1, defaultFiles)
			leased := e.grant()
			b := e.complete(leased)
			edit(leased, b)
			e.wantError(e.post(leased, leased.LeaseID, b), http.StatusBadRequest, "invalid")
			e.untouched(leased, leased)
		})
	}
}

// --- what a result does ---

func TestResultStoresACompleteCanonicalResultWithTheStatesTheDomainDerives(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	b := e.complete(leased)
	b["tests"] = []body{
		{"package": "z/pkg", "name": "TestPass", "file": "z/pkg/a_test.go", "kind": "declared", "head": []string{"pass"}},
		{"package": ".", "name": "TestFails", "file": "a_test.go", "kind": "declared", "head": []string{"fail", "fail", "fail"}},
		{"package": ".", "name": "TestBuild", "file": "a_test.go", "kind": "declared", "head": []string{"build-error"}},
		{"package": ".", "name": "TestSkips", "file": "a_test.go", "kind": "declared", "head": []string{"skip"}},
		{"package": ".", "name": "TestMissing", "file": "a_test.go", "kind": "declared", "head": []string{"absent", "absent", "absent"}},
		{"package": ".", "name": "TestFlaky", "file": "a_test.go", "kind": "declared", "head": []string{"fail", "pass"}},
		{"package": ".", "name": "TestFlakyAbsent", "file": "a_test.go", "kind": "declared", "head": []string{"absent", "pass"}},
		{"package": ".", "name": "TestTagged", "file": "tagged_test.go", "kind": "declared", "excluded": "build-constraint"},
		{"package": "nested", "name": "TestNested", "file": "nested/n_test.go", "kind": "declared", "excluded": "nested-module"},
	}
	b["submodules"] = []string{"vendor/sub", "third/party"}

	got := e.post(leased, leased.LeaseID, b)

	e.wantState(got, "complete")
	if job := e.job(leased.ID); job.State != execution.StateComplete {
		t.Errorf("job = %+v; want complete", job)
	}
	wantStates := map[string]string{"TestPass": "pass", "TestFails": "fail", "TestBuild": "fail", "TestSkips": "skipped", "TestMissing": "missing", "TestFlaky": "flaky", "TestFlakyAbsent": "flaky",
		"TestTagged": "excluded", "TestNested": "excluded"}
	tests, err := e.store.Tests(t.Context(), leased.ID)
	if err != nil || len(tests) != len(wantStates) {
		t.Fatalf("stored tests = %d, %v; want %d", len(tests), err, len(wantStates))
	}
	for _, test := range tests {
		if test.State != wantStates[test.Name] {
			t.Errorf("%s state = %q; want %q", test.Name, test.State, wantStates[test.Name])
		}
		if test.Name == "TestFlaky" && !slices.Equal(test.HeadRuns, []string{"fail", "pass"}) {
			t.Errorf("TestFlaky head runs = %v; want the observations in run order", test.HeadRuns)
		}
		if test.Name == "TestTagged" && (test.ExcludedReason != "build-constraint" || len(test.HeadRuns) != 0) {
			t.Errorf("TestTagged = %+v; want its reason and no runs", test)
		}
	}
	stored, found, err := e.store.Result(t.Context(), leased.ID)
	if err != nil || !found || stored.Origin != execution.OriginExecutor || stored.Status != execution.StateComplete || stored.ExecutorVersion != "0.1.0" ||
		stored.ExecutorSHA256.String() != "sha256:"+execBinary || stored.ImageDigest.String() != execImage || stored.Toolchain != "go1.27.3" || stored.Runtime != "podman 5.5.0" ||
		!slices.Equal(stored.Submodules, []string{"vendor/sub", "third/party"}) {
		t.Errorf("stored result = %+v, %v, %v; want what the executor said", stored, found, err)
	}
	if want := e.bodyDigest(b); stored.BodyDigest != want {
		t.Errorf("body digest = %s; want the SHA-256 of the body, %s", stored.BodyDigest, want)
	}
	if n := e.wakes.count.Load(); n != 1 {
		t.Errorf("wake-ups = %d; want one after the commit", n)
	}
}

func TestResultStoresAReceiptWithItsMarkersAndUnparsedFiles(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	leased := e.grant()
	b := e.complete(leased)
	b["tests"] = []body{
		{"package": ".", "name": "TestDetects", "file": "a_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"fail"}},
		{"package": ".", "name": "TestInert", "file": "a_test.go", "kind": "changed", "head": []string{"pass"}, "base": []string{"pass"}},
		{"package": "api", "name": "TestNewAPI", "file": "api/a_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"build-error"}},
		{"package": "api", "name": "TestBase", "file": "api/a_test.go", "kind": "added", "head": []string{"fail", "fail"}, "base": []string{"build-error"}},
		{"package": ".", "name": "TestFails", "file": "a_test.go", "kind": "changed", "head": []string{"fail"}, "base": []string{"pass"}},
		{"package": ".", "name": "TestSkips", "file": "a_test.go", "kind": "changed", "head": []string{"skip"}, "base": []string{"pass"}},
		{"package": ".", "name": "TestMissing", "file": "a_test.go", "kind": "changed", "head": []string{"absent"}, "base": []string{"pass"}},
		{"package": ".", "name": "TestMissingOnBase", "file": "a_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"absent"}},
		{"package": ".", "name": "TestFlakyBase", "file": "a_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"fail", "pass"}},
		{"package": ".", "name": "TestFlakyHead", "file": "a_test.go", "kind": "added", "head": []string{"fail", "pass"}, "base": []string{"fail"}},
		{"package": ".", "name": "TestTagged", "file": "tagged_test.go", "kind": "added", "excluded": "build-constraint"},
	}
	b["markers"] = []body{{"kind": "testing-import", "path": "a.go", "line": 3}, {"kind": "forged-event", "path": "pkg/b.go", "line": 10000000}}
	b["unparsed"] = []string{"broken_test.go", "pkg/other_test.go"}

	got := e.post(leased, leased.LeaseID, b)

	e.wantState(got, "complete")
	wantStates := map[string]string{"TestDetects": "detects", "TestInert": "inert", "TestNewAPI": "new_api", "TestBase": "fails", "TestFails": "fails", "TestSkips": "skipped",
		"TestMissing": "missing", "TestMissingOnBase": "missing", "TestFlakyBase": "flaky", "TestFlakyHead": "flaky", "TestTagged": "excluded"}
	tests, err := e.store.Tests(t.Context(), leased.ID)
	if err != nil || len(tests) != len(wantStates) {
		t.Fatalf("stored tests = %d, %v; want %d", len(tests), err, len(wantStates))
	}
	for _, test := range tests {
		if test.State != wantStates[test.Name] {
			t.Errorf("%s state = %q; want %q", test.Name, test.State, wantStates[test.Name])
		}
	}
	stored, _, err := e.store.Result(t.Context(), leased.ID)
	if err != nil || len(stored.Markers) != 2 || stored.Markers[1] != (execution.Marker{Kind: "forged-event", Path: "pkg/b.go", Line: 10000000}) ||
		!slices.Equal(stored.Unparsed, []string{"broken_test.go", "pkg/other_test.go"}) {
		t.Errorf("stored markers and unparsed = %+v, %v; want what the executor said", stored, err)
	}
}

// A package whose base side did not build reports build-error for every test of it: a base that is absent next to one that reports it has no state.
func TestResultRefusesABaseThatIsAbsentInAPackageThatDidNotBuild(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	leased := e.grant()
	b := e.complete(leased)
	b["tests"] = []body{
		{"package": "api", "name": "TestBuilt", "file": "api/a_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"build-error"}},
		{"package": "api", "name": "TestAbsent", "file": "api/a_test.go", "kind": "added", "head": []string{"pass"}, "base": []string{"absent"}},
	}

	e.wantError(e.post(leased, leased.LeaseID, b), http.StatusBadRequest, "invalid")

	e.untouched(leased, leased)
}

func (e *execTestEnv) bodyDigest(b body) artifact.Digest { return artifact.Hash(execJSON(e.t, b)) }

// --- unavailable results ---

func TestResultQueuesAgainAnAttemptThatFailedWithARetriedReason(t *testing.T) {
	for _, reason := range []string{"executor-error", "sandbox-unavailable", "archive-unavailable", "fetch-failed"} {
		t.Run(reason, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			leased := e.grant()

			got := e.post(leased, leased.LeaseID, e.unavailable(leased, reason))

			e.wantState(got, "pending")
			stored := e.job(leased.ID)
			if stored.State != execution.StatePending || stored.Failures != 1 || string(stored.Reason) != reason || !stored.NotBefore.Equal(e.clock.Now().Add(30*time.Second)) {
				t.Errorf("job = %+v; want pending after one failure, with the reason and a 30 s backoff", stored)
			}
			if _, found, err := e.store.Result(t.Context(), leased.ID); err != nil || found {
				t.Errorf("result stored = %v, %v; want none for a job that runs again", found, err)
			}
			if n := e.wakes.count.Load(); n != 0 {
				t.Errorf("wake-ups = %d; want none: nothing the checks read changed", n)
			}
			e.wantError(e.post(leased, leased.LeaseID, e.unavailable(leased, reason)), http.StatusConflict, "lease-lost") // sent again, it finds a job queued again
		})
	}
}

func TestResultEndsTheJobAtTheThirdFailure(t *testing.T) {
	e := newExecEnv(t)
	job := e.canonicalJob(1, defaultFiles)
	for i, backoff := range []time.Duration{30 * time.Second, 2 * time.Minute, 0} {
		leased := e.grant()
		got := e.post(leased, leased.LeaseID, e.unavailable(leased, "executor-error"))
		if i < 2 {
			e.wantState(got, "pending")
			e.clock.Advance(backoff)
			continue
		}
		e.wantState(got, "unavailable")
	}
	stored := e.job(job.ID)
	if stored.State != execution.StateUnavailable || stored.Reason != execution.ReasonExecutorError || stored.Failures != 3 {
		t.Errorf("job = %+v; want unavailable for executor-error after three failures", stored)
	}
	if _, found, _ := e.store.Result(t.Context(), job.ID); !found {
		t.Error("the last result is not stored")
	}
	if n := e.wakes.count.Load(); n != 1 {
		t.Errorf("wake-ups = %d; want one, for the result that was stored", n)
	}
}

func TestResultMakesAFinalReasonFinalAndWakesTheChecks(t *testing.T) {
	for _, reason := range []string{"install-failed", "resource-limit", "toolchain-mismatch", "source-mismatch", "too-large", "profile-unsupported"} {
		t.Run(reason, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			leased := e.grant()
			payload := e.unavailable(leased, reason)

			e.wantState(e.post(leased, leased.LeaseID, payload), "unavailable")

			stored := e.job(leased.ID)
			if stored.State != execution.StateUnavailable || string(stored.Reason) != reason || stored.Failures != 0 {
				t.Errorf("job = %+v; want unavailable for %s with no failure counted", stored, reason)
			}
			if row, found, err := e.store.Result(t.Context(), leased.ID); err != nil || !found || row.Origin != execution.OriginExecutor || string(row.Reason) != reason {
				t.Errorf("result = %+v, %v, %v; want the executor's row with its reason", row, found, err)
			}
			if n := e.wakes.count.Load(); n != 1 {
				t.Errorf("wake-ups = %d; want one", n)
			}
		})
	}
}

// A GitHub failure of the executor is not its failure: the job is queued again, no attempt is counted, and the first failure starts the bound.
func TestResultGivesTheJobBackWithoutCountingAFailureForAGitHubFailure(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()

	got := e.post(leased, leased.LeaseID, e.unavailable(leased, "github-unavailable"))

	e.wantState(got, "pending")
	stored := e.job(leased.ID)
	if stored.State != execution.StatePending || stored.Failures != 0 || stored.Reason != execution.ReasonGitHubUnavailable || !stored.GitHubSince.Equal(e.clock.Now()) ||
		!stored.NotBefore.Equal(e.clock.Now().Add(execution.GitHubBackoff)) {
		t.Errorf("job = %+v; want pending with github-unavailable, no failure, the first failure now and a minute's wait", stored)
	}
	if _, found, _ := e.store.Result(t.Context(), leased.ID); found {
		t.Error("a result is stored for a job given back")
	}
}

// A give-back a whole wait after the first failure is the final state, as an executor's row that keeps its digest; sent again it finds that.
func TestResultEndsAJobGivenBackAfterTheGitHubWait(t *testing.T) {
	e := newExecEnv(t)
	job := e.canonicalJob(1, defaultFiles)
	first := e.grant()
	e.wantState(e.post(first, first.LeaseID, e.unavailable(first, "github-unavailable")), "pending")
	e.clock.Advance(e.times.GitHubWait)
	second := e.grant()
	payload := e.unavailable(second, "github-unavailable")

	e.wantState(e.post(second, second.LeaseID, payload), "unavailable")

	stored := e.job(job.ID)
	if stored.State != execution.StateUnavailable || stored.Reason != execution.ReasonGitHubUnavailable {
		t.Errorf("job = %+v; want unavailable for github-unavailable", stored)
	}
	row, found, err := e.store.Result(t.Context(), job.ID)
	if err != nil || !found || row.Origin != execution.OriginExecutor || row.BodyDigest != e.bodyDigest(payload) {
		t.Errorf("result = %+v, %v, %v; want an executor row with the body digest", row, found, err)
	}
	e.wantState(e.post(second, second.LeaseID, payload), "unavailable") // the same body finds the stored outcome
}

// The reasons that the service sets are not the executor's to give.
func TestResultRefusesAReasonThatTheServiceSets(t *testing.T) {
	for _, reason := range []string{"lease-expired", "timeout", "no-executor", "executor-not-configured", "no-merge-base", "head-moved", "closed", "obsolete", "", "other", "EXECUTOR-ERROR"} {
		t.Run(reason, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			leased := e.grant()
			e.wantError(e.post(leased, leased.LeaseID, e.unavailable(leased, reason)), http.StatusBadRequest, "invalid")
			e.untouched(leased, leased)
		})
	}
}

func TestResultNeedsAReasonExactlyForUnavailable(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	withReason := e.complete(leased)
	withReason["reason"] = "executor-error"
	noReason := e.unavailable(leased, "executor-error")
	delete(noReason, "reason")
	nullReason := e.unavailable(leased, "executor-error")
	nullReason["reason"] = nil
	for name, payload := range map[string]body{"a complete result with a reason": withReason, "an unavailable result with none": noReason, "an unavailable result with null": nullReason} {
		if got := e.post(leased, leased.LeaseID, payload); got.status != http.StatusBadRequest {
			t.Errorf("%s = %d %s; want 400", name, got.status, got.text())
		}
	}
	e.untouched(leased, leased)
}

// --- a job that finished: the same body is recognized, whatever the lease ---

func TestResultSentAgainFindsTheStoredOutcome(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	payload := e.complete(leased)
	e.wantState(e.post(leased, leased.LeaseID, payload), "complete")

	for name, mod := range map[string]execMod{"its lease": withLease(leased.LeaseID), "another lease": withLease("ffffffffffffffffffffffffffffffff"), "no lease": func(r *http.Request) { r.Header.Del(leaseHeader) }} {
		got := e.send(http.MethodPost, e.jobPath(leased, "/result"), execJSON(t, payload), mod)
		if got.status != http.StatusOK || strings.TrimSpace(got.text()) != `{"state":"complete"}` {
			t.Errorf("sent again with %s = %d %s; want 200 complete as before", name, got.status, got.text())
		}
	}
	if n := e.wakes.count.Load(); n != 1 {
		t.Errorf("wake-ups = %d; want the one of the first result only", n)
	}
}

func TestResultOfAnotherBodyForAFinishedJobIsAConflict(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	payload := e.complete(leased)
	e.wantState(e.post(leased, leased.LeaseID, payload), "complete")
	other := e.complete(leased)
	other["executor"] = body{"version": "0.2.0", "binarySha256": execBinary}

	e.wantError(e.post(leased, leased.LeaseID, other), http.StatusConflict, "conflict")
	e.wantError(e.post(leased, "ffffffffffffffffffffffffffffffff", other), http.StatusConflict, "conflict")
	e.wantError(e.post(leased, leased.LeaseID, "not even JSON"), http.StatusConflict, "conflict")
	e.wantError(e.post(leased, leased.LeaseID, bytes.Repeat([]byte(" "), 1000)), http.StatusConflict, "conflict")
	if stored, _, _ := e.store.Result(t.Context(), leased.ID); stored.ExecutorVersion != "0.1.0" {
		t.Errorf("stored version = %q; want the first result kept", stored.ExecutorVersion)
	}
}

// A job that the service ended itself has no executor's result to compare with: any body is a conflict, and none builds an evidence binding.
func TestResultForAJobTheServiceEndedIsAConflict(t *testing.T) {
	t.Run("on the GitHub bound while it was pending", func(t *testing.T) {
		e := newExecEnv(t)
		job := e.canonicalJob(1, defaultFiles)
		for _, at := range []time.Duration{0, e.times.GitHubWait} {
			e.clock.Advance(at)
			if _, err := e.store.GitHubFailure(t.Context(), job.ID, e.clock.Now(), e.times.GitHubWait); err != nil {
				t.Fatal(err)
			}
		}
		if row, found, err := e.store.Result(t.Context(), job.ID); err != nil || !found || row.Origin != execution.OriginService {
			t.Fatalf("result = %+v, %v, %v; want the service's row", row, found, err)
		}
		e.wantError(e.post(job, "ffffffffffffffffffffffffffffffff", e.complete(job)), http.StatusConflict, "conflict")
		e.wantError(e.post(job, "ffffffffffffffffffffffffffffffff", e.unavailable(job, "github-unavailable")), http.StatusConflict, "conflict")
		if n := e.wakes.count.Load(); n != 0 {
			t.Errorf("wake-ups = %d; want none", n)
		}
	})
	t.Run("with no executor seen, which stores no row", func(t *testing.T) {
		e := newExecEnv(t)
		job := e.canonicalJob(1, defaultFiles)
		if _, err := e.store.Expire(t.Context(), execution.ExpireParams{Now: e.clock.Now().Add(time.Hour), LastSeenAt: e.clock.Now(), LeaseWait: e.times.LeaseWait, GitHubWait: e.times.GitHubWait}); err != nil {
			t.Fatal(err)
		}
		if stored := e.job(job.ID); stored.Reason != execution.ReasonNoExecutor {
			t.Fatalf("job = %+v; want no-executor", stored)
		}
		e.wantError(e.post(job, "ffffffffffffffffffffffffffffffff", e.complete(job)), http.StatusConflict, "conflict")
	})
}

// --- the wake-up ---

func TestResultCommitsEvenWhenTheWakeUpFails(t *testing.T) {
	e := newExecEnv(t)
	e.wakes.err = errors.New("insert failed: connection reset")
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()

	got := e.post(leased, leased.LeaseID, e.complete(leased))

	e.wantState(got, "complete")
	if job := e.job(leased.ID); job.State != execution.StateComplete {
		t.Errorf("job = %+v; want complete", job)
	}
	if n := e.wakes.count.Load(); n != 1 {
		t.Errorf("wake-ups = %d; want one attempt", n)
	}
	if !strings.Contains(e.logs.String(), `"level":"WARN"`) || !strings.Contains(e.logs.String(), "wake") {
		t.Errorf("log = %s; want the failed wake-up logged", e.logs.String())
	}
}
