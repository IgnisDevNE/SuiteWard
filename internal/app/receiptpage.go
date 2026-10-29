package app

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The pull request page (docs/contracts/runtime.md, "The pull request page"): GET /pulls/{owner}/{repo}/{pull} shows the situation and the execution results of the
// current head of a pull request, and the change report of its current revision. It is rebuilt on every request from stored rows, with the rules of the change report
// page: only a loopback Host is answered, there is no authentication, the same four headers are on every answer, and every string is escaped with its hidden characters
// written as escapes. It replaced the receipt page of M2.1, whose route now answers 308 (receiptMoved); the types and the files keep the name of the receipt page.

// receiptTimeout bounds a request for the page: the wait for its turn, the reads and the build. It is a real deadline, never a fake clock's, and below the 30 s the server
// gives a response.
const receiptTimeout = 20 * time.Second

// errReceiptNotFound says that the pull request is not one that the synchronization observed.
var errReceiptNotFound = errors.New("the pull request is not known")

// receiptSource builds the page of a pull request of an enrolled repository.
type receiptSource interface {
	page(ctx context.Context, state owner.State, project owner.ProjectPolicy, pull int, deps pageDeps) ([]byte, error)
}

// pullNumber is the pull request of the route: a positive integer written without a leading zero, as GitHub numbers it.
func pullNumber(r *http.Request) (int, bool) {
	number, err := strconv.Atoi(chi.URLParam(r, "pull"))
	return number, err == nil && number >= 1 && number <= math.MaxInt32 && strconv.Itoa(number) == chi.URLParam(r, "pull")
}

// receiptMoved serves GET /receipts/{owner}/{repo}/{pull}, the route of the receipt page, which every revision comment posted before M2.1b links: it answers 308 with the
// relative address of the pull request page, so that the loopback host stays. It checks only that the pull request is a number: the page decides what is known, so the
// redirect reveals nothing. The Location is the escaped path of the request, which is never a raw character in a header.
func (a *api) receiptMoved(w http.ResponseWriter, r *http.Request) {
	if _, ok := pullNumber(r); !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Location", "/pulls/"+strings.TrimPrefix(r.URL.EscapedPath(), "/receipts/"))
	w.WriteHeader(http.StatusPermanentRedirect)
}

// pull serves the pull request page of GET /pulls/{owner}/{repo}/{pull}.
func (a *api) pull(w http.ResponseWriter, r *http.Request) {
	number, ok := pullNumber(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), receiptTimeout)
	defer cancel()
	state, enrolled, err := a.owners.Current(ctx)
	if err != nil {
		a.receiptFailed(w, r, err)
		return
	}
	project, found := projectNamed(state, chi.URLParam(r, "owner")+"/"+chi.URLParam(r, "repo"))
	if !enrolled || !found {
		http.NotFound(w, r)
		return
	}
	page, err := a.receipts.page(ctx, state, project, number, pageDeps{reports: a.reports, liveness: a.executorStatus.liveness, logger: a.logger})
	switch {
	case errors.Is(err, errReceiptNotFound):
		http.NotFound(w, r)
		return
	case err != nil: // a page that passed its cap is one of them: the budgets keep it unreachable, so it is a failure to build, never a refusal for the size
		a.receiptFailed(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(page); err != nil {
		a.logger.DebugContext(r.Context(), "write pull request page", "path", r.URL.Path, "error", err)
	}
}

// receiptFailed logs the cause and answers with a fixed text: the cause may quote the store.
func (a *api) receiptFailed(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.WarnContext(r.Context(), "pull request page failed", "path", r.URL.Path, "error", err)
	http.Error(w, "the pull request page is unavailable", http.StatusServiceUnavailable)
}

// previewReader is the part of the command ledger the page reads: the preview mode of a pull request.
type previewReader interface {
	PreviewMode(ctx context.Context, repositoryID int64, pull int, since time.Time) (string, bool, error)
}

// receiptPages builds the pull request page. One page is built at a time in the process, because a page of two runs of 10,000 tests holds its rows and its markup.
type receiptPages struct {
	suites     governance.UnitOfWork
	contents   governance.ContentReader
	executions execution.Store
	pulls      pullLister
	previews   previewReader
	tier       string // the configured installation tier
	gate       chan struct{}
	limit      int              // the most bytes a page may hold, a field so that a test can lower it
	now        func() time.Time // the clock of the page, which a test can set
}

func newReceiptPages(suites governance.UnitOfWork, contents governance.ContentReader, executions execution.Store, pulls pullLister, previews previewReader, tier string) *receiptPages {
	return &receiptPages{suites: suites, contents: contents, executions: executions, pulls: pulls, previews: previews, tier: tier, gate: make(chan struct{}, 1), limit: maxPullPage, now: time.Now}
}
