package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

type fakeReports struct {
	report revisionReport
	err    error
	asked  []contract.ProposalReference
}

func (f *fakeReports) revisionReport(_ context.Context, ref contract.ProposalReference) (revisionReport, error) {
	f.asked = append(f.asked, ref)
	return f.report, f.err
}

func enrolledOwners(t *testing.T) *ownertest.Memory {
	t.Helper()
	owners := ownertest.NewMemory()
	err := owners.Enroll(t.Context(), owner.Enrollment{Owner: owner.Owner{AccountID: bootOwnerID, Login: "owner"}, Secret: make([]byte, 20),
		Projects: []owner.ProjectPolicy{{RepositoryID: bootRepoID, Repo: bootRepo, TestCheck: "ci"}}, Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime})
	if err != nil {
		t.Fatal(err)
	}
	return owners
}

func reportAPI(t *testing.T, reports pageReports) (*api, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	return &api{owners: enrolledOwners(t), reports: reports, logger: slog.New(slog.NewTextHandler(logs, nil))}, logs
}

const reportPath = "/reports/acme/widgets/P2-R1"

func getReport(a *api, host, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	newRouter(a).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://"+host+path, nil))
	return recorder
}

func smallReport(t *testing.T) report.Report {
	t.Helper()
	built, err := report.Build([]report.FileChange{
		{Path: "tests/a_test.go", Status: report.Modified,
			Old: []byte("package a\n\nfunc TestA(t *testing.T) {\n\tassert.Equal(t, 1, f())\n}\n"),
			New: []byte("package a\n\nfunc TestA(t *testing.T) {\n\tassert.Equal(t, 2, f())\n\tt.Skip(\"later\")\n}\n")},
		{Path: "tests/new_test.go", Status: report.Added, New: []byte("package a\n\nvar x = 1\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestTheReportPageStatesTheRevisionTheBaseTheNumbersAndEveryFile(t *testing.T) {
	reports := &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}}
	a, _ := reportAPI(t, reports)

	response := getReport(a, "127.0.0.1:8080", reportPath)

	body := html.UnescapeString(response.Body.String()) // what a browser shows
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET %s = %d %s, want 200 text/html\n%s", reportPath, response.Code, response.Header().Get("Content-Type"), body)
	}
	if want := (contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: defaultSuite, ProposalID: "P2", RevisionID: "R1"}); len(reports.asked) != 1 || reports.asked[0] != want {
		t.Fatalf("the report of %v was asked, want of %v", reports.asked, want)
	}
	for _, w := range []string{
		"P2-R1", "acme/widgets", "V1",
		"Files: 1 added, 1 modified, 0 removed", "Lines: 5 added, 1 removed (counted in 2 of 2 files)", "Marker lines seen: 1 removed assertion line, 1 added skip line",
		"tests/a_test.go", "tests/new_test.go", "+2/−1", "+3/−0", "Removed assertion line", "Added skip line", "assert.Equal(t, 1, f())", "var x = 1",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("the page lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"unsafe", "weaken", "suspicious", "dangerous"} {
		if strings.Contains(strings.ToLower(body), w) {
			t.Errorf("the page judges the change (%q)", w)
		}
	}
}

func TestTheReportPageNamesTheRepositoryAsEnrolledWhateverTheCaseOfTheURL(t *testing.T) {
	reports := &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}}
	a, _ := reportAPI(t, reports)

	response := getReport(a, "localhost:8080", "/reports/ACME/Widgets/P2-R1")

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "acme/widgets") || strings.Contains(response.Body.String(), "ACME") {
		t.Fatalf("GET = %d, want 200 and the repository named as enrolled\n%s", response.Code, response.Body.String())
	}
}

func TestTheReportPageExplainsEveryReasonAFileHasNoDiff(t *testing.T) {
	built := report.Report{
		Files: []report.File{
			{Path: "a.bin", Status: report.Modified, OldSize: 2, NewSize: 3, Limit: report.LimitBinary},
			{Path: "b.bin", Status: report.Added, NewSize: 7, Limit: report.LimitBinary},
			{Path: "c.json", Status: report.Modified, OldSize: 2_000_000, NewSize: 2_000_001, Limit: report.LimitSize},
			{Path: "d.txt", Status: report.Modified, Limit: report.LimitEditDistance},
			{Path: "e.txt", Status: report.Modified, Limit: report.LimitBudget},
			{Path: "f.txt", Status: report.Modified, CountsKnown: true, LinesAdded: 3, LinesRemoved: 4, Truncated: true},
			{Path: "g.txt", Status: report.Modified, CountsKnown: true, LinesAdded: 1, LinesRemoved: 1, MarkersTruncated: true},
			{Path: "h.bin", Status: report.Removed, OldSize: 9, Limit: report.LimitBinary},
		},
		Totals:    report.Totals{Files: map[report.Status]int{report.Modified: 6, report.Added: 1, report.Removed: 1}, LinesAdded: 4, LinesRemoved: 5, Markers: map[report.MarkerKind]int{}},
		Truncated: true,
	}
	a, _ := reportAPI(t, &fakeReports{report: revisionReport{Base: "V3", Report: built}})

	body := html.UnescapeString(getReport(a, "127.0.0.1:8080", reportPath).Body.String()) // what a browser shows

	for _, w := range []string{
		"Binary file, 2 bytes before and 3 bytes after.", "Binary file, 7 bytes.", "Binary file, 9 bytes.", "Too large to compare line by line: 2000000 bytes before and 2000001 bytes after.",
		"Too different to compare line by line", "Not compared: the report had used up its limit on comparison work before this file.",
		"The changed lines of this file are not shown, to keep the report small. The line counts are exact.",
		"Some marker lines of this file are not listed. The totals above count them all.",
		"Some details of this report were left out to keep it small.",
		"Lines: 4 added, 5 removed (counted in 2 of 8 files)", "Not shown line by line: 7 files",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("the page lacks %q:\n%s", w, body)
		}
	}
}

// hostileReport holds, in every place a report holds repository text, something that would act if it were not escaped or shown.
func hostileReport() report.Report {
	const script = `</pre><script>alert(1)</script><img src=x onerror=alert(1)>"'&`
	path := "tests/" + script + "\x01\u202e\n.go"
	return report.Report{
		Files: []report.File{{
			Path: path, Status: report.Modified, CountsKnown: true, LinesAdded: 2, LinesRemoved: 1,
			Hunks: []report.Hunk{{OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 3, Lines: []report.Line{
				{Kind: report.LineContext, Text: script},
				{Kind: report.LineRemoved, Text: "removed \x00 \u202e \xff\r", NoNewline: true},
				{Kind: report.LineAdded, Text: strings.Repeat("x", 500), Cut: true},
				{Kind: report.LineAdded, Text: "\ttabbed"},
			}}},
			Markers: []report.Marker{{Kind: report.MarkAddedSkip, Path: path, Line: 3, Text: script + "\u202e"}},
		}},
		Totals: report.Totals{Files: map[report.Status]int{report.Modified: 1}, LinesAdded: 2, LinesRemoved: 1, Markers: map[report.MarkerKind]int{report.MarkAddedSkip: 1}},
	}
}

func TestTheReportPageEscapesWhatTheRepositoryHolds(t *testing.T) {
	a, _ := reportAPI(t, &fakeReports{report: revisionReport{Base: "V1", Report: hostileReport()}})

	response := getReport(a, "127.0.0.1:8080", reportPath)

	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("GET = %d\n%s", response.Code, body)
	}
	for _, active := range []string{"<script", "javascript:", "<img", "<iframe", "<object", "<embed", "<link", "<base", "<form", "<svg"} {
		if strings.Contains(strings.ToLower(body), active) {
			t.Errorf("the page holds %q unescaped:\n%s", active, body)
		}
	}
	for _, hidden := range []string{"\x00", "\x01", "\u202e", "\xff", "\r"} {
		if strings.Contains(body, hidden) {
			t.Errorf("the page shows %q as it is, not as an escape", hidden)
		}
	}
	for _, shown := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", `\x01`, `\u202e`, `\x00`, `\xff`, `\r`, `\n`, "[line cut]", "No newline at end of file", "\ttabbed"} {
		if !strings.Contains(body, shown) {
			t.Errorf("the page lacks %q:\n%s", shown, body)
		}
	}
	if strings.Count(body, "<pre") != strings.Count(body, "</pre>") {
		t.Errorf("a line of the repository closed a <pre> that it did not open")
	}
}

func TestTheReportPageIsStaticAndLocked(t *testing.T) {
	a, _ := reportAPI(t, &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}})

	body := getReport(a, "127.0.0.1:8080", reportPath).Body.String()

	for _, w := range []string{"<script", "<link", "<img", "<iframe", "src=", "href=", "@import", "url("} {
		if strings.Contains(body, w) {
			t.Errorf("the page contains %q: it must be one document with inline style, no script and nothing to load", w)
		}
	}
	for _, w := range []string{"#12202F", "#3DD6A0", "#F4F1EA", "prefers-color-scheme: dark"} {
		if !strings.Contains(body, w) {
			t.Errorf("the page lacks the brand colour or the dark scheme %q", w)
		}
	}
}

func TestEveryAnswerOfTheReportRouteCarriesTheSecurityHeaders(t *testing.T) {
	for name, tc := range map[string]struct {
		host, path string
		source     *fakeReports
		code       int
	}{
		"a report":         {"127.0.0.1:8080", reportPath, &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}}, 200},
		"another host":     {"example.com", reportPath, &fakeReports{}, 403},
		"an unknown repo":  {"127.0.0.1:8080", "/reports/acme/other/P2-R1", &fakeReports{}, 404},
		"an unknown rev":   {"127.0.0.1:8080", reportPath, &fakeReports{err: governance.ErrNotFound}, 404},
		"a revision as P2": {"127.0.0.1:8080", "/reports/acme/widgets/P2", &fakeReports{}, 404},
		"a failure":        {"127.0.0.1:8080", reportPath, &fakeReports{err: errors.New("down")}, 503},
		"too large":        {"127.0.0.1:8080", reportPath, &fakeReports{err: errReportTooLarge}, 413},
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := reportAPI(t, tc.source)
			response := getReport(a, tc.host, tc.path)
			if response.Code != tc.code {
				t.Fatalf("GET = %d, want %d\n%s", response.Code, tc.code, response.Body.String())
			}
			for header, want := range map[string]string{
				"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'", "X-Content-Type-Options": "nosniff",
				"Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
			} {
				if got := response.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

func TestTheReportRouteIsAnsweredOnlyToTheMachineThatRunsTheInstance(t *testing.T) {
	for host, allowed := range map[string]bool{
		"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true, "127.0.0.1": true, "localhost": true, "[::1]": true, "LocalHost:18080": true,
		"example.com": false, "example.com:8080": false, "127.0.0.1.example.com:8080": false, "localhost.example.com": false, "0.0.0.0:8080": false,
		"192.168.1.5:8080": false, "[::2]:8080": false, "127.0.0.2:8080": false, "10.0.0.1": false, "127.0.0.1:8080@example.com": false,
	} {
		t.Run(host, func(t *testing.T) {
			reports := &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}}
			a, _ := reportAPI(t, reports)
			response := getReport(a, host, reportPath)
			if allowed && response.Code != http.StatusOK {
				t.Fatalf("Host %q: GET = %d, want 200", host, response.Code)
			}
			if !allowed && (response.Code != http.StatusForbidden || len(reports.asked) != 0 || strings.Contains(response.Body.String(), "tests/a_test.go")) {
				t.Fatalf("Host %q: GET = %d (report asked %d times), want 403 and no report", host, response.Code, len(reports.asked))
			}
		})
	}
}

func TestTheOtherEndpointsAreNotGuardedByHost(t *testing.T) {
	a := &api{owners: enrolledOwners(t), store: fakeStore{version: 10}, logger: quietLogger()}
	recorder := httptest.NewRecorder()
	newRouter(a).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://suiteward.internal:8080/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /healthz with another Host = %d, want 200: the probes of the container come with their own", recorder.Code)
	}
}

func TestTheReportRouteIsNotFoundForWhatIsNotAReportOfThisInstance(t *testing.T) {
	const leak = "SECRET-FROM-THE-STORE"
	for name, tc := range map[string]struct {
		path string
		err  error
	}{
		"an unknown repository":      {path: "/reports/acme/other/P2-R1"},
		"another owner":              {path: "/reports/evil/widgets/P2-R1"},
		"a reference without a dash": {path: "/reports/acme/widgets/P2R1"},
		"a proposal alone":           {path: "/reports/acme/widgets/P2"},
		"a zero revision":            {path: "/reports/acme/widgets/P2-R0"},
		"a zero proposal":            {path: "/reports/acme/widgets/P0-R1"},
		"a sign":                     {path: "/reports/acme/widgets/P+2-R1"},
		"a leading zero":             {path: "/reports/acme/widgets/P02-R1"},
		"text after the reference":   {path: "/reports/acme/widgets/P2-R1x"},
		"a path":                     {path: "/reports/acme/widgets/..%2fP2-R1"},
		"an unknown revision":        {path: reportPath, err: fmt.Errorf("%w: %s", governance.ErrNotFound, leak)},
	} {
		t.Run(name, func(t *testing.T) {
			reports := &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}, err: tc.err}
			a, _ := reportAPI(t, reports)
			response := getReport(a, "127.0.0.1:8080", tc.path)
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), leak) || strings.Contains(response.Body.String(), "tests/a_test.go") {
				t.Fatalf("GET %s = %d %q, want 404 and nothing of the report", tc.path, response.Code, response.Body.String())
			}
		})
	}
}

func TestTheReportRouteIsNotFoundBeforeAnOwnerIsEnrolled(t *testing.T) {
	a := &api{owners: ownertest.NewMemory(), reports: &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}}, logger: quietLogger()}
	if response := getReport(a, "127.0.0.1:8080", reportPath); response.Code != http.StatusNotFound {
		t.Fatalf("GET = %d, want 404", response.Code)
	}
}

func TestTheReportRouteSaysWhyItCannotBuildWithoutLeakingTheCause(t *testing.T) {
	const leak = "dial tcp db.internal:5432: password=hunter2"
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"the store failed":          {errors.New(leak), http.StatusServiceUnavailable},
		"a file is missing":         {fmt.Errorf("read the file: %w: %s", fs.ErrNotExist, leak), http.StatusServiceUnavailable},
		"the build ran out of time": {context.DeadlineExceeded, http.StatusServiceUnavailable},
		"the revision is too large": {fmt.Errorf("%w: %s", errReportTooLarge, leak), http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			a, logs := reportAPI(t, &fakeReports{err: tc.err})
			response := getReport(a, "127.0.0.1:8080", reportPath)
			if response.Code != tc.code || strings.Contains(response.Body.String(), "hunter2") || strings.Contains(response.Body.String(), "db.internal") {
				t.Fatalf("GET = %d %q, want %d and no cause", response.Code, response.Body.String(), tc.code)
			}
			if tc.code == http.StatusServiceUnavailable && !strings.Contains(logs.String(), "report") {
				t.Errorf("the failure is not logged: %q", logs.String())
			}
		})
	}
}

// A report that holds more than the page may hold is refused as a whole, with a fixed answer and no part of the page.
func TestTheReportPageIsBounded(t *testing.T) {
	text := strings.Repeat("x", 120)
	lines := make([]report.Line, 150_000)
	for i := range lines {
		lines[i] = report.Line{Kind: report.LineAdded, Text: text}
	}
	huge := report.Report{
		Files:  []report.File{{Path: "big.txt", Status: report.Added, CountsKnown: true, LinesAdded: len(lines), Hunks: []report.Hunk{{NewStart: 1, NewLines: len(lines), Lines: lines}}}},
		Totals: report.Totals{Files: map[report.Status]int{report.Added: 1}, LinesAdded: len(lines), Markers: map[report.MarkerKind]int{}},
	}
	a, _ := reportAPI(t, &fakeReports{report: revisionReport{Base: "V1", Report: huge}})

	response := getReport(a, "127.0.0.1:8080", reportPath)

	if response.Code != http.StatusRequestEntityTooLarge || response.Body.Len() > 1024 {
		t.Fatalf("GET = %d with %d bytes, want 413 and a short answer", response.Code, response.Body.Len())
	}
}

func TestTheReportPageOfAnOldRevisionIsRebuiltAfterALaterOneArrived(t *testing.T) {
	w, reports := reportWorld(t)
	w.tree(cjHead3, bootFiles("v3"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()
	a := &api{owners: w.owners, reports: reports, logger: quietLogger()}

	first := getReport(a, "127.0.0.1:8080", "/reports/acme/widgets/P2-R1")
	second := getReport(a, "127.0.0.1:8080", "/reports/acme/widgets/P2-R2")

	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "v2") || strings.Contains(first.Body.String(), "v3") || !strings.Contains(first.Body.String(), "V1") {
		t.Errorf("P2-R1 = %d, want the page of the change to v2 against V1\n%s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), "v3") || strings.Contains(second.Body.String(), "v2") {
		t.Errorf("P2-R2 = %d, want the page of the change to v3\n%s", second.Code, second.Body.String())
	}
	if gone := getReport(a, "127.0.0.1:8080", "/reports/acme/widgets/P2-R3"); gone.Code != http.StatusNotFound {
		t.Errorf("P2-R3 = %d, want 404: there is no such revision", gone.Code)
	}
}

func TestTheReportRouteIsUnavailableWhenTheOwnerCannotBeRead(t *testing.T) {
	const leak = "dial tcp db.internal:5432: password=hunter2"
	a, logs := reportAPI(t, &fakeReports{report: revisionReport{Base: "V1", Report: smallReport(t)}})
	a.owners = failingOwners{errors.New(leak)}

	response := getReport(a, "127.0.0.1:8080", reportPath)

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "hunter2") || !strings.Contains(logs.String(), "report failed") {
		t.Fatalf("GET = %d %q, want 503 with no cause, and the cause in the log %q", response.Code, response.Body.String(), logs.String())
	}
}

func (f *fakeReports) turn(context.Context) (func(), error) { return func() {}, nil }

func (f *fakeReports) build(ctx context.Context, ref contract.ProposalReference) (revisionReport, error) {
	return f.revisionReport(ctx, ref)
}
