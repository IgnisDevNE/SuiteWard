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

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The receipt page (docs/contracts/runtime.md, "The receipt page"): the results of the current head of a pull request, rebuilt on every request from the stored rows.

const pullPath = "/pulls/acme/widgets/5"

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
	at       time.Time // the clock of the page: zero is a moment after the runner was last seen
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
	w.pages.now = w.clockOfThePage
	w.server = &api{owners: w.owners, receipts: w.pages, reports: newChangeReports(w.suites, w.content), executorStatus: &executorStatus{liveness: w.check.liveness},
		logger: slog.New(slog.NewTextHandler(w.apiLogs, nil))}
}

// clockOfThePage is w.at, or five seconds after the runner was last seen: it is responding.
func (w *pageWorld) clockOfThePage() time.Time {
	if w.at.IsZero() {
		return w.check.liveness.LastSeen().Add(5 * time.Second)
	}
	return w.at
}

// runnerSilentFor sets the clock of the page to d after the runner was last seen.
func (w *pageWorld) runnerSilentFor(d time.Duration) { w.at = w.check.liveness.LastSeen().Add(d) }

func (w *pageWorld) get(host, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	newRouter(w.server).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://"+host+path, nil))
	return recorder
}

// text is what a browser shows of the page of pull 5, which must be served.
func (w *pageWorld) text() string {
	w.t.Helper()
	response := w.get("127.0.0.1:8080", pullPath)
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
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	canonicalJob, receiptJob = w.onlyJob(5, cjHead2, execution.PlanCanonical), w.onlyJob(5, cjHead2, execution.PlanReceipt)
	w.finish(canonicalJob, canonical)
	w.finish(receiptJob, receipt, edit...)
	return canonicalJob, receiptJob
}

func TestReceiptPageIsServedToALoopbackHostOnly(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "LOCALHOST", "127.0.0.1"} {
		if got := w.get(host, pullPath).Code; got != http.StatusOK {
			t.Errorf("Host %q: status %d, want 200", host, got)
		}
	}
	for _, host := range []string{"example.com", "evil.localhost.example:8080", "192.168.1.5:8080", "0.0.0.0:8080", "127.0.0.2:8080", ""} {
		response := w.get(host, pullPath)
		if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "acme") {
			t.Errorf("Host %q: status %d, body %q, want 403 with nothing of the pull request", host, response.Code, response.Body.String())
		}
	}
}

func TestReceiptPageSetsTheFourHeadersOnEveryAnswer(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	want := map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
	}
	answers := map[string]*httptest.ResponseRecorder{
		"200":  w.get("127.0.0.1:8080", pullPath),
		"403":  w.get("example.com", pullPath),
		"404":  w.get("127.0.0.1:8080", "/pulls/acme/widgets/99"),
		"404b": w.get("127.0.0.1:8080", "/pulls/acme/other/5"),
	}
	w.pages.executions = &pageResultsFail{Store: w.jobs}
	answers["503"] = w.get("127.0.0.1:8080", pullPath)
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
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	for name, path := range map[string]string{
		"an unknown pull request":           "/pulls/acme/widgets/99",
		"a repository that is not enrolled": "/pulls/acme/gadgets/5",
		"another owner":                     "/pulls/other/widgets/5",
		"a pull request that is no number":  "/pulls/acme/widgets/five",
		"pull request zero":                 "/pulls/acme/widgets/0",
		"a negative number":                 "/pulls/acme/widgets/-5",
		"a number over the range":           "/pulls/acme/widgets/2147483648",
		"a number with a leading zero":      "/pulls/acme/widgets/05",
		"a number with a plus":              "/pulls/acme/widgets/+5",
	} {
		if got := w.get("127.0.0.1:8080", path).Code; got != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, got)
		}
	}
	w.server.owners = ownertest.NewMemory() // nobody is enrolled yet
	if got := w.get("127.0.0.1:8080", pullPath).Code; got != http.StatusNotFound {
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
	w.execPullFive(execHeadFiles(receiptDecl+"# a comment\n", "v1")) // the declaration changes: a revision, and no file of the include
	if jobs := w.headJobs(5, cjHead2, execution.PlanReceipt); len(jobs) != 0 {
		t.Fatalf("a receipt job exists for a revision whose receipt does not apply: %+v", jobs)
	}
	w.wantText(w.text(), "This revision changes no file that the approved patterns match, so it has no receipt.")
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
	raw := w.get("127.0.0.1:8080", pullPath).Body.String()
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
	response := w.get("127.0.0.1:8080", pullPath)
	if response.Code != http.StatusRequestEntityTooLarge || response.Body.String() != "the receipt is too large to show\n" {
		t.Fatalf("status %d, body of %d bytes, want 413 and the fixed text", response.Code, response.Body.Len())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Error("the refusal lacks the headers")
	}

	small := newPageWorld(t)
	small.completed(canonicalTests("pass"), receiptTests("inert"))
	if got := small.get("127.0.0.1:8080", pullPath).Code; got != http.StatusOK {
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
	response := w.get("127.0.0.1:8080", pullPath)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "the pull request page is unavailable\n" {
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
		response := w.get("127.0.0.1:8080", pullPath)
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
	go func() { first <- w.get("127.0.0.1:8080", pullPath).Code }()
	select {
	case <-pulls.entered: // the first request is building
	case <-time.After(5 * time.Second):
		t.Fatal("the first request never started to build")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond) // a real deadline: the page waits for its turn until it ends
	defer cancel()
	second := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+pullPath, nil).WithContext(ctx)
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

// pageReasonsUnknown is an execution store whose unavailable jobs have a reason that no text knows: an impossible state of the store.
type pageReasonsUnknown struct{ execution.Store }

func (s pageReasonsUnknown) HeadJobs(ctx context.Context, repositoryID int64, pull int, head string, plan execution.Plan) ([]execution.Job, error) {
	jobs, err := s.Store.HeadJobs(ctx, repositoryID, pull, head, plan)
	for i := range jobs {
		if jobs[i].State == execution.StateUnavailable {
			jobs[i].Reason = "the-moon"
		}
	}
	return jobs, err
}

// A reason that no text knows is an impossible state of the store, not a word to show: the page is not built, as for every other read that fails.
func TestReceiptPageIs503ForAnUnavailableJobWhoseReasonNoTextKnows(t *testing.T) {
	w := newPageWorld(t)
	w.check.executorConfigured = false
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	w.pages.executions = pageReasonsUnknown{Store: w.jobs}

	response := w.get("127.0.0.1:8080", pullPath)

	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "the pull request page is unavailable\n" {
		t.Fatalf("status %d, body %q, want 503 and the fixed text", response.Code, response.Body.String())
	}
	if !strings.Contains(w.apiLogs.String(), "the-moon") {
		t.Errorf("the cause is not logged: %s", w.apiLogs)
	}
}

func TestModeLineRefusesASummaryNoTextCanSay(t *testing.T) {
	for name, summary := range map[string]domain.ModeSummary{
		"a reason that no words exist for": {Verdict: domain.VerdictNotMet, Reason: "a-reason-from-the-future"},
		"no reason at all":                 {Verdict: domain.VerdictNotMet},
		"a verdict that is none":           {},
		"the verdict of no mode":           {Verdict: domain.VerdictNotJudged},
	} {
		if line, err := modeLine(domain.ModeFix, summary); err == nil {
			t.Errorf("%s: %q", name, line)
		}
	}
	for summary, want := range map[domain.ModeSummary]string{
		{Verdict: domain.VerdictMet}:                                      "fix: met",
		{Verdict: domain.VerdictNotMet, Reason: domain.ReasonNoDetection}: "fix: not met: no test detects anything (no-detection)",
	} {
		if line, err := modeLine(domain.ModeFix, summary); err != nil || line != want {
			t.Errorf("modeLine(%+v) = %q, %v; want %q", summary, line, err, want)
		}
	}
}

// The page asks whether the receipt of the revision applies before it reads the job of the plan, as the gates do: a job that is under the manifest the revision expects, but
// belongs to a revision whose receipt applies, is not the receipt of one whose receipt does not.
func TestReceiptPageAsksWhetherTheReceiptAppliesBeforeItReadsItsJob(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl+"# a comment\n", "v1")) // a revision whose receipt does not apply: the declaration changes, no file of the include
	w.noJobs(5, cjHead2, execution.PlanReceipt)
	w.seedJob(execution.Spec{RepositoryID: bootRepoID, PullNumber: 5, Plan: execution.PlanReceipt, Profile: "go", HeadSHA: cjHead2, BaseSHA: execMergeBase,
		GoverningDigest: w.canonicalManifest(), GoverningSource: "V1", DeclarationDigest: artifact.Hash([]byte(receiptDecl)),
		CanonicalLevel: domain.CanonicalLevelAdvisory, ReceiptLevel: domain.ReceiptLevelAdvisory})

	response := w.get("127.0.0.1:8080", pullPath)

	page := html.UnescapeString(response.Body.String())
	if response.Code != http.StatusOK || strings.Contains(page, "<h2>Tests this change adds or edits</h2>") {
		t.Errorf("status %d: the page shows the receipt of a revision whose receipt does not apply:\n%s", response.Code, page)
	}
	w.wantText(page, "This revision changes no file that the approved patterns match, so it has no receipt.")
}
