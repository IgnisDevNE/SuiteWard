package executiontest

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func report(status execution.State, reason execution.Reason, body string) execution.Report {
	return execution.Report{Status: status, Reason: reason, ExecutorVersion: "0.4.0-alpha.3", ExecutorSHA256: digest("executor binary"), ImageDigest: digest("image"),
		Toolchain: "go1.25.1", Runtime: "podman 5.5.0", StartedAt: at(time.Second), EndedAt: at(8 * time.Second), BodyDigest: digest(body)}
}

// completeCanonical is a valid complete result of a canonical job. Its tests are not in the order they are read back in.
func completeCanonical(leaseID, body string) execution.Result {
	r := execution.Result{LeaseID: leaseID, Report: report(execution.StateComplete, "", body)}
	r.Submodules = []string{"vendor/sub"}
	r.Tests = []execution.Test{
		{Package: "pkg/b", Name: "TestC", File: "pkg/b/c_test.go", Kind: execution.KindDeclared, ExcludedReason: execution.ExcludedBuildConstraint, State: "excluded"},
		{Package: ".", Name: "TestA", File: "a_test.go", Kind: execution.KindDeclared, HeadRuns: []string{"pass"}, State: "pass"},
		{Package: "pkg/b", Name: "TestB", File: "pkg/b/b_test.go", Kind: execution.KindDeclared, HeadRuns: []string{"pass", "fail", "pass"}, State: "flaky"},
	}
	return r
}

// completeReceipt is a valid complete result of a receipt job, with markers.
func completeReceipt(leaseID, body string) execution.Result {
	r := execution.Result{LeaseID: leaseID, Report: report(execution.StateComplete, "", body)}
	r.Markers = []execution.Marker{{Kind: "env-read", Path: "a_test.go", Line: 12}, {Kind: "testing-import", Path: "pkg/p_test.go", Line: 3}}
	r.Tests = []execution.Test{
		{Package: "pkg", Name: "TestChanged", File: "pkg/p_test.go", Kind: execution.KindChanged, HeadRuns: []string{"pass", "pass"}, BaseRuns: []string{"pass", "skip"}, State: "inert"},
		{Package: ".", Name: "TestNew", File: "a_test.go", Kind: execution.KindAdded, HeadRuns: []string{"pass"}, BaseRuns: []string{"build-error"}, State: "new_api"},
		{Package: "pkg", Name: "TestGuarded", File: "pkg/g_test.go", Kind: execution.KindAdded, ExcludedReason: execution.ExcludedBuildConstraint, State: "excluded"},
	}
	return r
}

// unavailableResult is a result of an attempt that could not be completed.
func unavailableResult(leaseID string, reason execution.Reason, body string) execution.Result {
	return execution.Result{LeaseID: leaseID, Report: report(execution.StateUnavailable, reason, body)}
}

// sortedTests are the tests of a result in the order they are read back in: by package, then name.
func sortedTests(r execution.Result) []execution.Test {
	tests := slices.Clone(r.Tests)
	slices.SortFunc(tests, func(a, b execution.Test) int {
		return cmp.Or(strings.Compare(a.Package, b.Package), strings.Compare(a.Name, b.Name))
	})
	return tests
}

func (e *env) requireTests(id int64, want []execution.Test) {
	e.t.Helper()
	got, err := e.s.Tests(e.ctx, id)
	e.must(err)
	if len(got) != len(want) {
		e.t.Fatalf("Tests(%d) = %d rows; want %d", id, len(got), len(want))
	}
	for i := range want {
		if d := diff(got[i], want[i], fmt.Sprintf("test %d", i)); d != "" {
			e.t.Fatal(d)
		}
	}
}

func (e *env) requireResult(id int64, origin execution.Origin, received time.Duration, r execution.Report) {
	e.t.Helper()
	got, found, err := e.s.Result(e.ctx, id)
	e.must(err)
	want := execution.StoredResult{JobID: id, Origin: origin, ReceivedAt: at(received), Report: r}
	if !found {
		e.t.Fatalf("Result(%d) found nothing", id)
	}
	if d := diff(got, want, "result"); d != "" {
		e.t.Fatal(d)
	}
}

// requireUntouched requires the job to be as it was leased, with no result and no tests.
func (e *env) requireUntouched(leased execution.Job) {
	e.t.Helper()
	e.requireJob(leased)
	e.requireNoResult(leased.ID)
}

func rsComplete(e *env) {
	canonical := e.enqueue(canonicalSpec(1), 0)
	l := e.mustLease(canonical.ID, 0)
	r := completeCanonical(l.LeaseID, "canonical")
	out, err := e.s.Complete(e.ctx, canonical.ID, r, at(10*time.Second), githubWait)
	e.must(err)
	if out.State != execution.StateComplete || out.Replayed {
		e.t.Fatalf("outcome = %+v; want complete, not replayed", out)
	}
	e.requireJob(ended(l, execution.StateComplete, "", 10*time.Second))
	e.requireResult(canonical.ID, execution.OriginExecutor, 10*time.Second, r.Report)
	e.requireTests(canonical.ID, sortedTests(r))
	// A complete job is current: a second Enqueue of its key returns it.
	same, created, err := e.s.Enqueue(e.ctx, execution.NewJob{Spec: canonicalSpec(1), At: at(time.Minute)})
	e.must(err)
	if created || same.ID != canonical.ID {
		e.t.Fatalf("Enqueue over a complete job = job %d, created %v; want job %d", same.ID, created, canonical.ID)
	}

	// A receipt job keeps its markers, skipped submodules and both sides of each test. A result clears the first GitHub failure.
	receipt := e.enqueue(receiptSpec(2), time.Minute)
	_, err = e.s.GitHubFailure(e.ctx, receipt.ID, at(time.Minute), githubWait)
	e.must(err)
	rl := e.mustLease(receipt.ID, 2*time.Minute+time.Second)
	rr := completeReceipt(rl.LeaseID, "receipt")
	rr.Submodules = []string{"vendor/a", "vendor/b"}
	_, err = e.s.Complete(e.ctx, receipt.ID, rr, at(3*time.Minute), githubWait)
	e.must(err)
	want := ended(rl, execution.StateComplete, "", 3*time.Minute)
	want.GitHubSince = time.Time{}
	e.requireJob(want)
	e.requireResult(receipt.ID, execution.OriginExecutor, 3*time.Minute, rr.Report)
	e.requireTests(receipt.ID, sortedTests(rr))
	// A complete canonical run with no test is a run.
	empty := e.enqueue(canonicalSpec(3), 4*time.Minute)
	el := e.mustLease(empty.ID, 4*time.Minute)
	none := completeCanonical(el.LeaseID, "empty")
	none.Tests = nil
	_, err = e.s.Complete(e.ctx, empty.ID, none, at(4*time.Minute+30*time.Second), githubWait)
	e.must(err)
	e.requireResult(empty.ID, execution.OriginExecutor, 4*time.Minute+30*time.Second, none.Report)
	e.requireTests(empty.ID, nil)
}

func rsRequeue(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	first := e.mustLease(job.ID, 0)
	failedAttempt := unavailableResult(first.LeaseID, execution.ReasonExecutorError, "first attempt")
	out, err := e.s.Complete(e.ctx, job.ID, failedAttempt, at(time.Second), githubWait)
	e.must(err)
	if out.State != execution.StatePending {
		e.t.Fatalf("outcome = %+v; want pending", out)
	}
	e.requireNoResult(job.ID)
	// A result that was answered pending finds the job queued again: its lease is lost.
	_, err = e.s.Complete(e.ctx, job.ID, failedAttempt, at(2*time.Second), githubWait)
	e.requireErr(err, execution.ErrLeaseLost)
	// The stored result is the last attempt's.
	second := e.mustLease(job.ID, time.Second+firstBackoff)
	r := completeCanonical(second.LeaseID, "second attempt")
	_, err = e.s.Complete(e.ctx, job.ID, r, at(time.Minute), githubWait)
	e.must(err)
	e.requireResult(job.ID, execution.OriginExecutor, time.Minute, r.Report)
	e.requireTests(job.ID, sortedTests(r))
}

func rsSameBody(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	l := e.mustLease(job.ID, 0)
	r := completeCanonical(l.LeaseID, "body")
	if _, err := e.s.Complete(e.ctx, job.ID, r, at(10*time.Second), githubWait); err != nil {
		e.t.Fatal(err)
	}
	// The same body is recognized, whatever the lease, and nothing is stored again.
	for name, lease := range map[string]string{"the same lease": l.LeaseID, "another lease": e.newLeaseID()} {
		again := r
		again.LeaseID = lease
		out, err := e.s.Complete(e.ctx, job.ID, again, at(time.Hour), githubWait)
		e.must(err)
		if out.State != execution.StateComplete || !out.Replayed {
			e.t.Fatalf("%s: outcome of the same body = %+v; want complete, replayed", name, out)
		}
	}
	e.requireJob(ended(l, execution.StateComplete, "", 10*time.Second))
	e.requireResult(job.ID, execution.OriginExecutor, 10*time.Second, r.Report) // received once
	e.requireTests(job.ID, sortedTests(r))
	// Another body for a finished job conflicts, and so does any result for a job with no stored body.
	other := completeCanonical(l.LeaseID, "another body")
	_, err := e.s.Complete(e.ctx, job.ID, other, at(time.Hour), githubWait)
	e.requireErr(err, execution.ErrConflict)
	e.requireResult(job.ID, execution.OriginExecutor, 10*time.Second, r.Report)
	cancelled := e.enqueue(canonicalSpec(2), time.Hour)
	cl := e.mustLease(cancelled.ID, time.Hour)
	if _, err := e.s.Cancel(e.ctx, cancelled.ID, execution.ReasonHeadMoved, at(time.Hour+time.Second)); err != nil {
		e.t.Fatal(err)
	}
	_, err = e.s.Complete(e.ctx, cancelled.ID, completeCanonical(cl.LeaseID, "late"), at(time.Hour+2*time.Second), githubWait)
	e.requireErr(err, execution.ErrConflict)
	e.requireNoResult(cancelled.ID)
}

func rsLease(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	l := e.mustLease(job.ID, 0)
	r := completeCanonical(l.LeaseID, "body")
	wrong := r
	wrong.LeaseID = e.newLeaseID()
	_, err := e.s.Complete(e.ctx, job.ID, wrong, at(time.Second), githubWait)
	e.requireErr(err, execution.ErrLeaseLost)
	_, err = e.s.Complete(e.ctx, job.ID, r, at(leaseTTL), githubWait) // the lease expired at that instant
	e.requireErr(err, execution.ErrLeaseLost)
	e.requireUntouched(l)
	_, err = e.s.Complete(e.ctx, 987654, r, at(time.Second), githubWait)
	e.requireErr(err, execution.ErrNotFound)
	pending := e.enqueue(canonicalSpec(2), time.Hour)
	_, err = e.s.Complete(e.ctx, pending.ID, r, at(time.Hour), githubWait)
	e.requireErr(err, execution.ErrLeaseLost)
	e.requireUntouched(pending)
	// The lease is checked before the result: a result that is invalid and has no lease is a lost lease.
	invalid := wrong
	invalid.Status = "bogus"
	_, err = e.s.Complete(e.ctx, job.ID, invalid, at(time.Second), githubWait)
	e.requireErr(err, execution.ErrLeaseLost)
	// A lease that is still current completes.
	if _, err := e.s.Complete(e.ctx, job.ID, r, at(leaseTTL-time.Microsecond), githubWait); err != nil {
		e.t.Fatal(err)
	}
}

func rsPlanRules(e *env) {
	canonical := e.enqueue(canonicalSpec(1), 0)
	cl := e.mustLease(canonical.ID, 0)
	validCanonical := func() execution.Result { return completeCanonical(cl.LeaseID, "canonical") }
	requireInvalid(e, validCanonical, map[string]func(*execution.Result){
		"a canonical row with base runs":     func(r *execution.Result) { r.Tests[1].BaseRuns = []string{"pass"} },
		"a canonical row of a receipt kind":  func(r *execution.Result) { r.Tests[1].Kind = execution.KindAdded },
		"a changed row in a canonical run":   func(r *execution.Result) { r.Tests[1].Kind = execution.KindChanged },
		"a receipt state in a canonical run": func(r *execution.Result) { r.Tests[1].State = "detects" },
		"a canonical run with markers": func(r *execution.Result) {
			r.Markers = []execution.Marker{{Kind: "env-read", Path: "a_test.go", Line: 1}}
		},
	}, func(r execution.Result) error {
		_, err := e.s.Complete(e.ctx, canonical.ID, r, at(10*time.Second), githubWait)
		return err
	})
	e.requireUntouched(cl)

	receipt := e.enqueue(receiptSpec(2), time.Minute)
	rl := e.mustLease(receipt.ID, time.Minute)
	validReceipt := func() execution.Result { return completeReceipt(rl.LeaseID, "receipt") }
	requireInvalid(e, validReceipt, map[string]func(*execution.Result){
		"a receipt row without base runs":           func(r *execution.Result) { r.Tests[0].BaseRuns = nil },
		"a declared row in a receipt":               func(r *execution.Result) { r.Tests[0].Kind = execution.KindDeclared },
		"a canonical state in a receipt":            func(r *execution.Result) { r.Tests[0].State = "pass" },
		"a canonical fail in a receipt":             func(r *execution.Result) { r.Tests[0].State = "fail" },
		"an excluded receipt row with base runs":    func(r *execution.Result) { r.Tests[2].BaseRuns = []string{"pass"} },
		"a receipt row with four base runs":         func(r *execution.Result) { r.Tests[0].BaseRuns = []string{"pass", "pass", "pass", "pass"} },
		"a receipt row with an empty base run list": func(r *execution.Result) { r.Tests[0].BaseRuns = []string{} },
	}, func(r execution.Result) error {
		_, err := e.s.Complete(e.ctx, receipt.ID, r, at(time.Minute+30*time.Second), githubWait)
		return err
	})
	e.requireUntouched(rl)
}

func long(n int) string { return strings.Repeat("a", n) }

func rsInvalid(e *env) {
	job := e.enqueue(canonicalSpec(1), 0)
	l := e.mustLease(job.ID, 0)
	valid := func() execution.Result { return completeCanonical(l.LeaseID, "body") }
	test := func(mutate func(*execution.Test)) func(*execution.Result) {
		return func(r *execution.Result) { mutate(&r.Tests[1]) } // the second test runs and is declared
	}
	submodules := func(n, size int) []string {
		list := make([]string, n)
		for i := range list {
			list[i] = long(size)
		}
		return list
	}
	failedWith := func(reason execution.Reason) func(*execution.Result) {
		return func(r *execution.Result) { r.Status, r.Reason, r.Tests = execution.StateUnavailable, reason, nil }
	}
	bad := map[string]func(*execution.Result){
		"no status":                              func(r *execution.Result) { r.Status = "" },
		"a status of the job":                    func(r *execution.Result) { r.Status = execution.StateCancelled },
		"a complete result with a reason":        func(r *execution.Result) { r.Reason = execution.ReasonExecutorError },
		"an unavailable result without a reason": failedWith(""),
		"an unavailable reason of the lease":     failedWith(execution.ReasonLeaseExpired),
		"an unavailable reason of the timeout":   failedWith(execution.ReasonTimeout),
		"an unavailable reason of no executor":   failedWith(execution.ReasonNoExecutor),
		"an unavailable reason of no config":     failedWith(execution.ReasonNotConfigured),
		"an unavailable reason of no base":       failedWith(execution.ReasonNoMergeBase),
		"an unknown unavailable reason":          failedWith("bogus"),
		"an unavailable result with tests": func(r *execution.Result) {
			r.Status, r.Reason = execution.StateUnavailable, execution.ReasonExecutorError
		},
		"no body digest":                              func(r *execution.Result) { r.BodyDigest = artifact.Digest{} },
		"no executor version":                         func(r *execution.Result) { r.ExecutorVersion = "" },
		"an executor version that starts with a dash": func(r *execution.Result) { r.ExecutorVersion = "-1" },
		"an executor version of 65 bytes":             func(r *execution.Result) { r.ExecutorVersion = long(65) },
		"no executor digest":                          func(r *execution.Result) { r.ExecutorSHA256 = artifact.Digest{} },
		"a complete result without an image":          func(r *execution.Result) { r.ImageDigest = artifact.Digest{} },
		"a complete result without a toolchain":       func(r *execution.Result) { r.Toolchain = "" },
		"a toolchain of 65 bytes":                     func(r *execution.Result) { r.Toolchain = long(65) },
		"a complete result without a runtime":         func(r *execution.Result) { r.Runtime = "" },
		"a runtime of 65 bytes":                       func(r *execution.Result) { r.Runtime = long(65) },
		"no start":                                    func(r *execution.Result) { r.StartedAt = time.Time{} },
		"no end":                                      func(r *execution.Result) { r.EndedAt = time.Time{} },
		"an end before the start":                     func(r *execution.Result) { r.EndedAt = r.StartedAt.Add(-time.Microsecond) },
		"101 submodules":                              func(r *execution.Result) { r.Submodules = submodules(101, 1) },
		"a submodule of 4097 bytes":                   func(r *execution.Result) { r.Submodules = submodules(1, 4097) },
		"an unknown state":                            test(func(t *execution.Test) { t.State = "green" }),
		"an unknown observation":                      test(func(t *execution.Test) { t.HeadRuns = []string{"ok"} }),
		"excluded as an observation":                  test(func(t *execution.Test) { t.HeadRuns = []string{"excluded"} }),
		"four head runs":                              test(func(t *execution.Test) { t.HeadRuns = []string{"pass", "pass", "pass", "pass"} }),
		"no head runs on a test that ran":             test(func(t *execution.Test) { t.HeadRuns = nil }),
		"an unknown kind":                             test(func(t *execution.Test) { t.Kind = "other" }),
		"an excluded reason without the state":        test(func(t *execution.Test) { t.ExcludedReason = execution.ExcludedBuildConstraint }),
		"the excluded state without a reason":         test(func(t *execution.Test) { t.State = "excluded" }),
		"an unknown excluded reason":                  func(r *execution.Result) { r.Tests[0].ExcludedReason = "tag" },
		"an excluded test with head runs":             func(r *execution.Result) { r.Tests[0].HeadRuns = []string{"skip"} },
		"no package":                                  test(func(t *execution.Test) { t.Package = "" }),
		"a package of 513 bytes":                      test(func(t *execution.Test) { t.Package = long(513) }),
		"no name":                                     test(func(t *execution.Test) { t.Name = "" }),
		"a name of 201 bytes":                         test(func(t *execution.Test) { t.Name = long(201) }),
		"no file":                                     test(func(t *execution.Test) { t.File = "" }),
		"a file of 1025 bytes":                        test(func(t *execution.Test) { t.File = long(1025) }),
		"a duplicate package and name":                func(r *execution.Result) { r.Tests[2].Package, r.Tests[2].Name = r.Tests[1].Package, r.Tests[1].Name },
	}
	requireInvalid(e, valid, bad, func(r execution.Result) error {
		_, err := e.s.Complete(e.ctx, job.ID, r, at(10*time.Second), githubWait)
		return err
	})
	e.requireUntouched(l)

	// The limits themselves are accepted: the longest names, three runs and a runtime of 64 bytes.
	edges := completeCanonical(l.LeaseID, "edges")
	edges.Runtime, edges.Toolchain, edges.ExecutorVersion = long(64), long(64), "v"+strings.Repeat("1", 63)
	edges.Tests[1].Package, edges.Tests[1].Name, edges.Tests[1].File = long(512), "T"+long(199), long(1024)
	edges.Submodules = submodules(100, 4096)
	if _, err := e.s.Complete(e.ctx, job.ID, edges, at(10*time.Second), githubWait); err != nil {
		e.t.Fatal(err)
	}
}

func rsBound(e *env) {
	tests := func(n int) []execution.Test {
		list := make([]execution.Test, n)
		for i := range list {
			list[i] = execution.Test{Package: "p", Name: fmt.Sprintf("Test%05d", i), File: "p/p_test.go", Kind: execution.KindDeclared, HeadRuns: []string{"pass"}, State: "pass"}
		}
		return list
	}
	job := e.enqueue(canonicalSpec(1), 0)
	l := e.mustLease(job.ID, 0)
	over := completeCanonical(l.LeaseID, "over")
	over.Tests = tests(maxTests + 1)
	_, err := e.s.Complete(e.ctx, job.ID, over, at(10*time.Second), githubWait)
	e.requireErr(err, execution.ErrInvalidRequest)
	e.requireUntouched(l)
	exact := completeCanonical(l.LeaseID, "exact")
	exact.Tests = tests(maxTests)
	_, err = e.s.Complete(e.ctx, job.ID, exact, at(10*time.Second), githubWait)
	e.must(err)
	got, err := e.s.Tests(e.ctx, job.ID)
	e.must(err)
	if len(got) != maxTests {
		e.t.Fatalf("Tests = %d rows; want %d", len(got), maxTests)
	}
}
