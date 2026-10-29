package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
)

// The routes of the pull request page (docs/contracts/runtime.md, "The pull request page"): GET /pulls/{owner}/{repo}/{pull} is the page, and the route of the receipt
// page, which every revision comment posted before M2.1b links, answers 308 to it.

const oldReceiptPath = "/receipts/acme/widgets/5"

var fourHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
}

func requireHeaders(t *testing.T, name string, response *httptest.ResponseRecorder) {
	t.Helper()
	for header, value := range fourHeaders {
		if got := response.Header().Get(header); got != value {
			t.Errorf("%s: %s = %q, want %q", name, header, got, value)
		}
	}
}

func TestTheOldReceiptRouteAnswers308ToThePullRequestPage(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	response := w.get("127.0.0.1:8080", oldReceiptPath)
	if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != "/pulls/acme/widgets/5" {
		t.Fatalf("status %d, Location %q, want 308 to the relative /pulls/acme/widgets/5", response.Code, response.Header().Get("Location"))
	}
	if response.Body.Len() != 0 {
		t.Errorf("the redirect has a body of %d bytes: %q", response.Body.Len(), response.Body.String())
	}
	requireHeaders(t, "308", response)
}

// The redirect checks that the pull request is a number and nothing else, so it reveals nothing: the page that it names decides what is known.
func TestTheOldReceiptRouteRevealsNothingOfWhatIsKnown(t *testing.T) {
	w := newPageWorld(t)
	for name, path := range map[string]string{
		"an unknown pull request": "/receipts/acme/widgets/99",
		"another repository":      "/receipts/acme/gadgets/5",
		"another owner":           "/receipts/other/widgets/5",
	} {
		response := w.get("127.0.0.1:8080", path)
		if want := "/pulls/" + strings.TrimPrefix(path, "/receipts/"); response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != want {
			t.Errorf("%s: status %d, Location %q, want 308 to %q", name, response.Code, response.Header().Get("Location"), want)
		}
	}
	w.server.owners = ownertest.NewMemory() // nobody is enrolled: the route needs no store at all
	if got := w.get("127.0.0.1:8080", oldReceiptPath).Code; got != http.StatusPermanentRedirect {
		t.Errorf("the redirect read the store: status %d", got)
	}
}

func TestTheOldReceiptRouteIs404ForAPullRequestThatIsNoPositiveNumberWithoutALeadingZero(t *testing.T) {
	w := newPageWorld(t)
	for name, path := range map[string]string{
		"a pull request that is no number": "/receipts/acme/widgets/five",
		"pull request zero":                "/receipts/acme/widgets/0",
		"a negative number":                "/receipts/acme/widgets/-5",
		"a number over the range":          "/receipts/acme/widgets/2147483648",
		"a number with a leading zero":     "/receipts/acme/widgets/05",
		"a number with a plus":             "/receipts/acme/widgets/+5",
	} {
		response := w.get("127.0.0.1:8080", path)
		if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
			t.Errorf("%s: status %d, Location %q, want 404 and no Location", name, response.Code, response.Header().Get("Location"))
		}
		requireHeaders(t, name, response)
	}
}

func TestTheOldReceiptRouteIsBehindTheLoopbackHostGuard(t *testing.T) {
	w := newPageWorld(t)
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "LOCALHOST"} {
		if got := w.get(host, oldReceiptPath).Code; got != http.StatusPermanentRedirect {
			t.Errorf("Host %q: status %d, want 308", host, got)
		}
	}
	for _, host := range []string{"example.com", "evil.localhost.example:8080", "192.168.1.5:8080", "0.0.0.0:8080", ""} {
		response := w.get(host, oldReceiptPath)
		if response.Code != http.StatusForbidden || response.Header().Get("Location") != "" {
			t.Errorf("Host %q: status %d, Location %q, want 403 and no Location", host, response.Code, response.Header().Get("Location"))
		}
		requireHeaders(t, "Host "+host, response)
	}
}

// The Location is the escaped path of the request, so a character that the request escaped never becomes a raw one in a header, and nothing is escaped twice.
func TestTheLocationOfTheRedirectKeepsTheEscapingOfTheRequest(t *testing.T) {
	w := newPageWorld(t)
	for path, want := range map[string]string{
		"/receipts/acme/wid%20gets/5":    "/pulls/acme/wid%20gets/5",
		"/receipts/ac%0d%0ame/widgets/5": "/pulls/ac%0d%0ame/widgets/5",
		"/receipts/a.b_c-d/w%2Fx/5":      "/pulls/a.b_c-d/w%2Fx/5",
	} {
		response := w.get("127.0.0.1:8080", path)
		if got := response.Header().Get("Location"); response.Code != http.StatusPermanentRedirect || got != want {
			t.Errorf("%s: status %d, Location %q, want 308 to %q", path, response.Code, got, want)
		}
	}
}

func TestThePullRequestPageIsServedUnderPullsAndTheReportRouteIsUnchanged(t *testing.T) {
	w := newPageWorld(t)
	w.execPullFive(execHeadFiles(receiptDecl, "v2"))
	response := w.get("127.0.0.1:8080", pullPath)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, type %q", response.Code, response.Header().Get("Content-Type"))
	}
	requireHeaders(t, "200", response)
	w.server.reports = &fakeReports{err: governance.ErrNotFound}
	if got := w.get("127.0.0.1:8080", "/reports/acme/widgets/P1-R1").Code; got != http.StatusNotFound {
		t.Errorf("the report route answers %d for a revision it does not know, want 404", got)
	}
}
