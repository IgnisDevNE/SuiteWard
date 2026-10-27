package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The receipt page (docs/contracts/runtime.md, "The receipt page"): the results of the current head of a pull request, rebuilt on every request from the stored rows.

const receiptPath = "/receipts/acme/widgets/5"

var receiptDecl = execDeclaration("advisory", "advisory")

// pagePreviews answers the preview mode of every pull request and remembers what it was asked.
type pagePreviews struct {
	mode  string
	found bool
	err   error
	asked []string
}

func (f *pagePreviews) PreviewMode(_ context.Context, repositoryID int64, pull int, since time.Time) (string, bool, error) {
	f.asked = append(f.asked, fmt.Sprint(repositoryID, " ", pull, " ", since.UTC().Format(time.RFC3339)))
	return f.mode, f.found, f.err
}

// pageWorld is the default canonical world with the receipt page over its stores: pull 5 is open at cjHead2.
type pageWorld struct {
	*cjJobWorld
	previews *pagePreviews
	pages    *receiptPages
	server   *api
	apiLogs  *bytes.Buffer
}

func newPageWorld(t *testing.T) *pageWorld {
	t.Helper()
	w := &pageWorld{cjJobWorld: newExecWorld(t, receiptDecl), previews: &pagePreviews{}, apiLogs: &bytes.Buffer{}}
	w.serve("isolated")
	return w
}

// serve builds the page over the stores of the world, for the configured tier.
func (w *pageWorld) serve(tier string) {
	w.pages = newReceiptPages(w.suites, w.check.contents, w.jobs, w.pulls, w.previews, tier)
	w.server = &api{owners: w.owners, receipts: w.pages, logger: slog.New(slog.NewTextHandler(w.apiLogs, nil))}
}

func (w *pageWorld) get(host, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	newRouter(w.server).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://"+host+path, nil))
	return recorder
}

// text is what a browser shows of the page of pull 5, which must be served.
func (w *pageWorld) text() string {
	w.t.Helper()
	response := w.get("127.0.0.1:8080", receiptPath)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
		w.t.Fatalf("status %d, type %q, body %q\nlogs: %s", response.Code, response.Header().Get("Content-Type"), response.Body.String(), w.apiLogs)
	}
	return html.UnescapeString(response.Body.String())
}

func (w *pageWorld) wantText(page string, phrases ...string) {
	w.t.Helper()
	for _, phrase := range phrases {
		if !strings.Contains(page, phrase) {
			w.t.Errorf("the page lacks %q:\n%s", phrase, page)
		}
	}
}

// completed opens pull 5 with a change to a protected test, and completes its two jobs: the canonical run first, which is the older.
func (w *pageWorld) completed(canonical, receipt []execution.Test, edit ...func(*execution.Result)) (canonicalJob, receiptJob execution.Job) {
	w.t.Helper()
	w.execPull(execHeadFiles(receiptDecl, "v2"))
	canonicalJob, receiptJob = w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.finish(canonicalJob, canonical)
	w.finish(receiptJob, receipt, edit...)
	return canonicalJob, receiptJob
}

func TestReceiptPageIsServedToALoopbackHostOnly(t *testing.T) {
	w := newPageWorld(t)
	w.execPull(execHeadFiles(receiptDecl, "v2"))
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "LOCALHOST", "127.0.0.1"} {
		if got := w.get(host, receiptPath).Code; got != http.StatusOK {
			t.Errorf("Host %q: status %d, want 200", host, got)
		}
	}
	for _, host := range []string{"example.com", "evil.localhost.example:8080", "192.168.1.5:8080", "0.0.0.0:8080", "127.0.0.2:8080", ""} {
		response := w.get(host, receiptPath)
		if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "acme") {
			t.Errorf("Host %q: status %d, body %q, want 403 with nothing of the pull request", host, response.Code, response.Body.String())
		}
	}
}

func TestReceiptPageSetsTheFourHeadersOnEveryAnswer(t *testing.T) {
	w := newPageWorld(t)
	w.execPull(execHeadFiles(receiptDecl, "v2"))
	want := map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
	}
	answers := map[string]*httptest.ResponseRecorder{
		"200":  w.get("127.0.0.1:8080", receiptPath),
		"403":  w.get("example.com", receiptPath),
		"404":  w.get("127.0.0.1:8080", "/receipts/acme/widgets/99"),
		"404b": w.get("127.0.0.1:8080", "/receipts/acme/other/5"),
	}
	w.pages.executions = &pageResultsFail{Store: w.jobs}
	answers["503"] = w.get("127.0.0.1:8080", receiptPath)
	for name, response := range answers {
		for header, value := range want {
			if got := response.Header().Get(header); got != value {
				t.Errorf("%s: %s = %q, want %q", name, header, got, value)
			}
		}
	}
	if answers["200"].Code != http.StatusOK || answers["503"].Code != http.StatusServiceUnavailable {
		t.Errorf("the answers are %d and %d", answers["200"].Code, answers["503"].Code)
	}
}

func TestReceiptPageIs404ForWhatIsNotKnown(t *testing.T) {
	w := newPageWorld(t)
	w.execPull(execHeadFiles(receiptDecl, "v2"))
	for name, path := range map[string]string{
		"an unknown pull request":           "/receipts/acme/widgets/99",
		"a repository that is not enrolled": "/receipts/acme/gadgets/5",
		"another owner":                     "/receipts/other/widgets/5",
		"a pull request that is no number":  "/receipts/acme/widgets/five",
		"pull request zero":                 "/receipts/acme/widgets/0",
		"a negative number":                 "/receipts/acme/widgets/-5",
		"a number over the range":           "/receipts/acme/widgets/2147483648",
		"a number with a leading zero":      "/receipts/acme/widgets/05",
		"a number with a plus":              "/receipts/acme/widgets/+5",
	} {
		if got := w.get("127.0.0.1:8080", path).Code; got != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, got)
		}
	}
	w.server.owners = ownertest.NewMemory() // nobody is enrolled yet
	if got := w.get("127.0.0.1:8080", receiptPath).Code; got != http.StatusNotFound {
		t.Errorf("before an owner is enrolled: status %d, want 404", got)
	}
}

func TestReceiptPageOfAPullRequestWithNoJobSaysThatThereAreNoResultsYet(t *testing.T) {
	w := newPageWorld(t)
	w.tree(cjHead2, execHeadFiles(receiptDecl, "v1"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2}) // a pull request that the checks job has not planned yet
	page := w.text()
	w.wantText(page, "No execution results for this pull request yet.", cjHead2)
	if strings.Contains(page, "has no receipt") {
		t.Errorf("a pull request with no revision says that its receipt does not apply:\n%s", page)
	}
}

func TestReceiptPageOfARevisionWithNoFileOfTheApprovedPatternsSaysItHasNoReceipt(t *testing.T) {
	w := newPageWorld(t)
	w.execPull(execHeadFiles(receiptDecl+"# a comment\n", "v1")) // the declaration changes: a revision, and no file of the include
	if jobs := w.headJobs(5, cjHead2, execution.PlanReceipt); len(jobs) != 0 {
		t.Fatalf("a receipt job exists for a revision whose receipt does not apply: %+v", jobs)
	}
	w.wantText(w.text(), "This revision changes no file that the approved patterns match, so it has no receipt.")
}

func TestReceiptPageStatesTheHeadsTheTierAndThatTheResultsAreEvidence(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	page := w.text()
	w.wantText(page, "Head: "+cjHead2, "Base: "+bootHead, "Installation tier: isolated", "evidence reported by an executor", "not a verdict")
	if strings.Contains(page, "assurance is reduced") {
		t.Errorf("the isolated tier states a reduced assurance:\n%s", page)
	}
	w.serve("co-located") // the enrollment recorded isolated: the effective tier is co-located
	w.wantText(w.text(), "Installation tier: co-located", "assurance is reduced")
}

func TestReceiptPageStatesTheEvidenceBindingOfEachRunAndTheReceiptOfThePullRequest(t *testing.T) {
	w := newPageWorld(t)
	canonical, receipt := w.completed(canonicalTests("pass", "fail"), receiptTests("detects", "inert"))
	page := w.text()
	w.wantText(page, "Canonical run", "Receipt",
		"Governing manifest: "+canonical.GoverningDigest.String()+" (source V1)", "Policy levels: canonical advisory, receipt advisory", "Profile: go",
		"Executor: 0.4.0-test", "Executor binary SHA-256: "+artifactHash("binary"), "Runtime: podman 5.5.0", "Toolchain: go1.25.1", "Image digest: "+artifactHash("image"),
		"Started: ", "Ended: ", "Merge base: "+receipt.BaseSHA, "State: complete")
	if strings.Count(page, "Executor: 0.4.0-test") != 2 {
		t.Errorf("each run states its own evidence binding:\n%s", page)
	}
}

func TestReceiptPageStatesAJobThatWasNotRunWithItsReasonAndNoEvidence(t *testing.T) {
	w := newPageWorld(t)
	w.check.executorConfigured = false // the jobs are created already unavailable, and no results row exists
	w.execPull(execHeadFiles(receiptDecl, "v2"))
	page := w.text()
	w.wantText(page, "Not run: no executor is configured for this instance (executor-not-configured).")
	if strings.Contains(page, "Executor: ") || strings.Contains(page, "Governing manifest") && strings.Contains(page, "Image digest") {
		t.Errorf("a run that was not run states an evidence binding:\n%s", page)
	}
}

func TestReceiptPageStatesWhatARunThatWasTriedAgainSaw(t *testing.T) {
	w := newPageWorld(t)
	w.execPull(execHeadFiles(receiptDecl, "v2"))
	canonical := w.onlyJob(5, cjHead2, execution.PlanCanonical) // the older job: it is leased first
	leased, found, err := w.jobs.Lease(t.Context(), execution.LeaseRequest{Now: w.clock, LeaseID: fmt.Sprintf("%032x", 7), JobTimeout: time.Hour, GitHubWait: time.Hour},
		func(context.Context, execution.Job) error { return nil })
	if err != nil || !found || leased.ID != canonical.ID {
		t.Fatalf("lease: job %d, found %v, %v", leased.ID, found, err)
	}
	failed := execution.Result{LeaseID: leased.LeaseID, Report: execution.Report{Status: execution.StateUnavailable, Reason: execution.ReasonExecutorError, ExecutorVersion: "0.4.0-test",
		ExecutorSHA256: artifact.Hash([]byte("binary")), StartedAt: w.clock.Add(-time.Minute), EndedAt: w.clock, BodyDigest: artifact.Hash([]byte("failed"))}}
	if outcome, err := w.jobs.Complete(t.Context(), canonical.ID, failed, w.clock, time.Hour); err != nil || outcome.State != execution.StatePending {
		t.Fatalf("the failed attempt: %+v, %v", outcome, err)
	}

	page := w.text()

	w.wantText(page, "State: pending", "Attempts: 1", "Failures: 1", "Last reason: executor-error")
}

func TestReceiptPageListsTheChangedFilesAtMostOneHundredAndHowManyThereAre(t *testing.T) {
	w := newPageWorld(t)
	files := execHeadFiles(receiptDecl, "v2")
	for i := range 150 {
		files[fmt.Sprintf("tests/n%03d_test.go", i)] = "package a\n"
	}
	w.execPull(files)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects"))
	page := w.text()
	w.wantText(page, "Files that this revision adds or changes under the approved patterns: 151 (100 listed)", "tests/a_test.go", "tests/n097_test.go")
	if strings.Contains(page, "tests/n098_test.go") {
		t.Errorf("more than 100 files are listed:\n%s", page)
	}
}

func TestReceiptPageListsWhatIsNotEvaluatedTheSubmodulesTheMarkersAndTheFilesThatDoNotParse(t *testing.T) {
	w := newPageWorld(t)
	head := execDeclaration("advisory", "advisory") + "# e2e\n"
	head = strings.Replace(head, "include: [/tests/**]", "include: [/tests/**, /e2e/**]", 1)
	files := execHeadFiles(head, "v2")
	files["e2e/x_test.go"] = "package e2e\n"
	w.reader.files = map[int][]github.PullFile{5: {{Path: "e2e/x_test.go", Status: "added"}, {Path: "tests/a_test.go", Status: "modified"}}}
	w.execPull(files)
	w.finish(w.onlyJob(5, cjHead2, execution.PlanCanonical), canonicalTests("pass"))
	w.finish(w.onlyJob(5, cjHead2, execution.PlanReceipt), receiptTests("detects"), func(r *execution.Result) {
		r.Submodules = []string{"vendor/sub"}
		r.Markers = []execution.Marker{{Kind: "env-read", Path: "tests/a_test.go", Line: 12}, {Kind: "testing-import", Path: "tests/b_test.go", Line: 3}}
		r.Unparsed = []string{"tests/broken_test.go"}
	})
	page := w.text()
	w.wantText(page, "Not evaluated: pattern not approved yet", "e2e/x_test.go", "Skipped submodules", "vendor/sub", "Marker lines seen: 2",
		"env-read: tests/a_test.go line 12", "testing-import: tests/b_test.go line 3", "Changed test files that do not parse: 1", "tests/broken_test.go")
}

func TestReceiptPageStatesTheStateOfEachEvaluatedTestWithItsPackageAndFile(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects", "inert", "new_api", "fails", "missing", "flaky", "skipped", "excluded"))
	page := w.text()
	w.wantText(page, "DETECTS  .  TestR00  a_test.go", "INERT  .  TestR01  a_test.go", "NEW API  .  TestR02  a_test.go", "FAILS  .  TestR03  a_test.go",
		"MISSING  .  TestR04  a_test.go", "FLAKY  .  TestR05  a_test.go", "SKIPPED  .  TestR06  a_test.go", "EXCLUDED  .  TestR07  a_test.go (build-constraint)",
		"Tests by state: 1 DETECTS, 1 INERT, 1 NEW API, 1 FAILS, 1 MISSING, 1 FLAKY, 1 SKIPPED", "Excluded tests: 1")
}

func TestReceiptPageStatesWhetherEachModeIsMetAndWhyNot(t *testing.T) {
	for name, tc := range map[string]struct {
		tests []execution.Test
		want  []string
	}{
		"a test that detects": {receiptTests("detects", "inert"), []string{"fix: met", "feature: met", "refactor: not met: a test is not inert (not-inert)"}},
		"only inert tests":    {receiptTests("inert"), []string{"fix: not met: no test detects anything (no-detection)", "feature: not met: no test detects anything (no-detection)", "refactor: met"}},
		"a failing test":      {receiptTests("detects", "fails"), []string{"fix: not met: a test fails, is missing or is flaky, or a changed test file does not parse (blocking-tests)", "feature: not met", "refactor: not met"}},
		"nothing evaluated":   {receiptTests("excluded"), []string{"fix: not met: no test ran, so nothing detects the change (no-evaluated-tests)", "refactor: met", "has nothing to judge"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPageWorld(t)
			w.completed(canonicalTests("pass"), tc.tests)
			w.wantText(w.text(), tc.want...)
		})
	}
}

func TestReceiptPageStatesThePreviewModeAndSaysThatNothingReadsIt(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	w.wantText(w.text(), "No preview mode is recorded for this pull request.")
	w.previews.mode, w.previews.found = "feature", true
	w.previews.asked = nil
	w.wantText(w.text(), "Preview mode: feature", "no check and no approval reads it")
	if want := fmt.Sprint(bootRepoID, " 5 ", bootTime.UTC().Format(time.RFC3339)); len(w.previews.asked) != 1 || w.previews.asked[0] != want {
		t.Errorf("the preview mode was asked as %v, want once as %q: since the current enrollment", w.previews.asked, want)
	}
}

func TestReceiptPageStatesWhatARunOfNothingRanAndTheExcludedTests(t *testing.T) {
	w := newPageWorld(t)
	w.completed(nil, receiptTests("detects"))
	w.wantText(w.text(), "0 tests ran.")

	w = newPageWorld(t)
	w.completed(canonicalTests("pass", "fail", "skipped", "missing", "flaky", "excluded"), receiptTests("detects"))
	w.wantText(w.text(), "5 tests ran: 1 passed, 1 skipped, 1 failed, 1 missing, 1 flaky.", "Excluded tests (not run): 1", "FAIL  .  TestC01  a_test.go", "EXCLUDED  .  TestC05  a_test.go (build-constraint)")
}

func TestReceiptPageStatesWhichJobsTheOwnerUnblocked(t *testing.T) {
	w := newPageWorld(t)
	w.check.executorConfigured = false
	w.execPull(execHeadFiles(receiptDecl, "v2"))
	canonical, receipt := w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.wantText(w.text(), "Not run:")
	if strings.Contains(w.text(), "The owner unblocked") {
		t.Errorf("a run that nobody unblocked says so:\n%s", w.text())
	}
	w.unblock(5, 600, cjHead2, receipt)
	page := w.text()
	if strings.Count(page, "The owner unblocked this run") != 1 {
		t.Errorf("the page does not say which run was unblocked, once:\n%s", page)
	}
	w.unblock(5, 601, cjHead2, canonical)
	if got := strings.Count(w.text(), "The owner unblocked this run"); got != 2 {
		t.Errorf("%d runs are said to be unblocked, want 2", got)
	}
}

func TestReceiptPageOfAClosedPullRequestShowsTheLastHeadRecorded(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	w.sync(map[int]string{4: bootHead}) // pull 5 is no longer open
	page := w.text()
	w.wantText(page, "Head: "+cjHead2, "not open", "State: complete")
}

// Everything that comes from the repository or from the executor is shown escaped: the names of tests, packages and files, the paths of markers, submodules and files that
// do not parse, and the text of the evidence binding. A character that hides itself is shown as an escape.
func TestReceiptPageEscapesWhatTheRepositoryAndTheExecutorSay(t *testing.T) {
	w := newPageWorld(t)
	hostile := []execution.Test{
		{Package: "pkg\n<b>pk</b>", Name: "Test<img src=x onerror=alert(1)>", File: "a\u202e_test.go", Kind: execution.KindAdded, State: "detects", HeadRuns: []string{"pass"}, BaseRuns: []string{"fail"}},
		{Package: ".", Name: "Test\\n\u200bZero", File: "b_test.go", Kind: execution.KindAdded, State: "inert", HeadRuns: []string{"pass"}, BaseRuns: []string{"pass"}},
	}
	w.completed(canonicalTests("pass"), hostile, func(r *execution.Result) {
		r.Submodules = []string{"sub<script>"}
		r.Markers = []execution.Marker{{Kind: "env-read", Path: "x\ny<i>.go", Line: 1}}
		r.Unparsed = []string{"<u>_test.go"}
		r.Runtime = "podman <5>"
	})
	raw := w.get("127.0.0.1:8080", receiptPath).Body.String()
	for _, banned := range []string{"<img", "<b>", "<script>", "<i>", "<u>", "\u202e", "\u200b", "pkg\n", "x\ny"} {
		if strings.Contains(raw, banned) {
			t.Errorf("the page holds %q unescaped", banned)
		}
	}
	page := html.UnescapeString(raw)
	w.wantText(page, `pkg\n<b>pk</b>`, `Test<img src=x onerror=alert(1)>`, `a\u202e_test.go`, `Test\\n\u200bZero`, `sub<script>`, `x\ny<i>.go`, `<u>_test.go`, `podman <5>`)
}

// A page that passes 8 MiB is refused whole: 413 with a fixed text, and nothing of it is cut.
func TestReceiptPageIsRefusedWholeWhenItPassesItsLimit(t *testing.T) {
	w := newPageWorld(t)
	long := func(n int, c string) string { return strings.Repeat(c, n) }
	tests := make([]execution.Test, execution.MaxTests)
	for i := range tests {
		tests[i] = execution.Test{Package: long(500, "p") + fmt.Sprint(i), Name: fmt.Sprintf("Test%0190d", i), File: long(1000, "f") + "_test.go", Kind: execution.KindAdded, State: "inert",
			HeadRuns: []string{"pass"}, BaseRuns: []string{"pass"}}
	}
	w.completed(canonicalTests("pass"), tests)
	response := w.get("127.0.0.1:8080", receiptPath)
	if response.Code != http.StatusRequestEntityTooLarge || response.Body.String() != "the receipt is too large to show\n" {
		t.Fatalf("status %d, body of %d bytes, want 413 and the fixed text", response.Code, response.Body.Len())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Error("the refusal lacks the headers")
	}

	small := newPageWorld(t)
	small.completed(canonicalTests("pass"), receiptTests("inert"))
	if got := small.get("127.0.0.1:8080", receiptPath).Code; got != http.StatusOK {
		t.Errorf("a small page is %d", got)
	}
}

// pageResultsFail is an execution store that cannot read a result.
type pageResultsFail struct{ execution.Store }

func (pageResultsFail) Result(context.Context, int64) (execution.StoredResult, bool, error) {
	return execution.StoredResult{}, false, errors.New("SECRET-STORE-TEXT: connection reset")
}

func TestReceiptPageThatCannotBeBuiltIs503WithAFixedTextAndTheCauseInTheLog(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	w.pages.executions = &pageResultsFail{Store: w.jobs}
	response := w.get("127.0.0.1:8080", receiptPath)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "the receipt is unavailable\n" {
		t.Fatalf("status %d, body %q, want 503 and the fixed text", response.Code, response.Body.String())
	}
	if !strings.Contains(w.apiLogs.String(), "SECRET-STORE-TEXT") {
		t.Errorf("the cause is not logged: %s", w.apiLogs)
	}
	for name, edit := range map[string]func(*pageWorld){
		"the preview mode":   func(w *pageWorld) { w.previews.err = errors.New("SECRET-STORE-TEXT") },
		"the observed pulls": func(w *pageWorld) { w.pages.pulls = pagePullsFail{} },
		"the revision": func(w *pageWorld) {
			w.pages.suites = &erroringSuites{UnitOfWork: w.suites, project: w.project(), err: errors.New("SECRET-STORE-TEXT")}
		},
	} {
		w := newPageWorld(t)
		w.completed(canonicalTests("pass"), receiptTests("detects"))
		edit(w)
		response := w.get("127.0.0.1:8080", receiptPath)
		if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "SECRET") {
			t.Errorf("%s: status %d, body %q, want 503 and the fixed text", name, response.Code, response.Body.String())
		}
	}
}

type pagePullsFail struct{}

func (pagePullsFail) Pulls(context.Context, string) ([]githubsync.PullState, error) {
	return nil, errors.New("SECRET-STORE-TEXT")
}

// pagePullsBlock holds every read of the observed pull requests until it is released, and counts them.
type pagePullsBlock struct {
	pullLister
	mu      sync.Mutex
	started int
	entered chan struct{}
	release chan struct{}
}

func (b *pagePullsBlock) Pulls(ctx context.Context, repo string) ([]githubsync.PullState, error) {
	b.mu.Lock()
	b.started++
	b.mu.Unlock()
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.pullLister.Pulls(ctx, repo)
}

// One page is built at a time: a second request waits for the first, and gives up when its own time ends, with the fixed text.
func TestOnlyOneReceiptPageIsBuiltAtATime(t *testing.T) {
	w := newPageWorld(t)
	w.completed(canonicalTests("pass"), receiptTests("detects"))
	pulls := &pagePullsBlock{pullLister: w.pulls, entered: make(chan struct{}, 2), release: make(chan struct{})}
	w.pages.pulls = pulls

	first := make(chan int, 1)
	go func() { first <- w.get("127.0.0.1:8080", receiptPath).Code }()
	<-pulls.entered // the first request is building

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond) // a real deadline: the page waits for its turn until it ends
	defer cancel()
	second := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+receiptPath, nil).WithContext(ctx)
		newRouter(w.server).ServeHTTP(recorder, request)
		second <- recorder.Code
	}()
	if got := <-second; got != http.StatusServiceUnavailable {
		t.Errorf("the request that waited is %d, want 503", got)
	}
	pulls.mu.Lock()
	started := pulls.started
	pulls.mu.Unlock()
	if started != 1 {
		t.Errorf("%d builds started while one was running, want 1", started)
	}
	close(pulls.release)
	if got := <-first; got != http.StatusOK {
		t.Errorf("the first request is %d", got)
	}
}

func artifactHash(text string) string { return artifact.Hash([]byte(text)).String() }
