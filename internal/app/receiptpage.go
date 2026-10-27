package app

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The receipt page (docs/contracts/runtime.md, "The receipt page"): GET /receipts/{owner}/{repo}/{pull} shows the execution results of the current head of a pull
// request. It is rebuilt on every request from stored rows, with the rules of the change report page: only a loopback Host is answered, there is no authentication, the
// same four headers are on every answer, and every string is escaped with its hidden characters written as escapes.

// receiptTimeout bounds a receipt request: the wait for its turn, the reads and the build. It is a real deadline, never a fake clock's, and below the 30 s the server
// gives a response.
const receiptTimeout = 20 * time.Second

// errReceiptNotFound says that the pull request is not one that the synchronization observed.
var errReceiptNotFound = errors.New("the pull request is not known")

// receiptSource builds the page of a pull request of an enrolled repository.
type receiptSource interface {
	page(ctx context.Context, state owner.State, project owner.ProjectPolicy, pull int) ([]byte, error)
}

// receipt serves the receipt page of GET /receipts/{owner}/{repo}/{pull}.
func (a *api) receipt(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(chi.URLParam(r, "pull"))
	if err != nil || number < 1 || number > math.MaxInt32 || strconv.Itoa(number) != chi.URLParam(r, "pull") {
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
	page, err := a.receipts.page(ctx, state, project, number)
	switch {
	case errors.Is(err, errReceiptNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, errPageTooLarge):
		http.Error(w, "the receipt is too large to show", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		a.receiptFailed(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(page); err != nil {
		a.logger.DebugContext(r.Context(), "write receipt", "path", r.URL.Path, "error", err)
	}
}

// receiptFailed logs the cause and answers with a fixed text: the cause may quote the store.
func (a *api) receiptFailed(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.WarnContext(r.Context(), "receipt failed", "path", r.URL.Path, "error", err)
	http.Error(w, "the receipt is unavailable", http.StatusServiceUnavailable)
}

// previewReader is the part of the command ledger the page reads: the preview mode of a pull request.
type previewReader interface {
	PreviewMode(ctx context.Context, repositoryID int64, pull int, since time.Time) (string, bool, error)
}

// receiptPages builds the receipt page. One page is built at a time in the process, because a page of two runs of 10,000 tests holds its rows and its markup.
type receiptPages struct {
	suites     governance.UnitOfWork
	contents   governance.ContentReader
	executions execution.Store
	pulls      pullLister
	previews   previewReader
	tier       string // the configured installation tier
	gate       chan struct{}
}

func newReceiptPages(suites governance.UnitOfWork, contents governance.ContentReader, executions execution.Store, pulls pullLister, previews previewReader, tier string) *receiptPages {
	return &receiptPages{suites: suites, contents: contents, executions: executions, pulls: pulls, previews: previews, tier: tier, gate: make(chan struct{}, 1)}
}
