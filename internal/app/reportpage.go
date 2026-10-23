package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	// reportTimeout bounds a report request: the wait for its turn, the reads and the build, which alone can take about two seconds. It is
	// below the 30 s the server gives a response.
	reportTimeout = 20 * time.Second
	// maxReportPage bounds the page. The report bounds its hunks to 4 MiB of text, which escaping and markup can grow several times over.
	maxReportPage = 8 << 20
)

// reportSource is the part of changeReports the report page reads.
type reportSource interface {
	revisionReport(ctx context.Context, ref contract.ProposalReference) (revisionReport, error)
}

// reportGuard sets the headers of every answer of the report route, errors included: the page is one document with inline style, so it needs nothing
// else; nothing leaves it through a referrer and nothing is kept. It also answers only a request whose Host is the machine that runs the instance, so
// that a page in the owner's browser cannot read a report through a name that it made point at 127.0.0.1 (DNS rebinding).
func reportGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether a Host header names this machine: 127.0.0.1, localhost or [::1], with any port or none.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		host = hostport[1 : len(hostport)-1]
	}
	return slices.Contains([]string{"127.0.0.1", "localhost", "::1"}, strings.ToLower(host))
}

// report serves the change report of GET /reports/{owner}/{repo}/{revision}, the revision being P<n>-R<m>. It reads only what the revision binds: its
// own manifest and that of its base canonical (see changeReports).
func (a *api) report(w http.ResponseWriter, r *http.Request) {
	proposal, revision, ok := commandtext.ParseReference(chi.URLParam(r, "revision"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportTimeout)
	defer cancel()
	state, enrolled, err := a.owners.Current(ctx)
	if err != nil {
		a.reportFailed(w, r, err)
		return
	}
	project, found := projectNamed(state, chi.URLParam(r, "owner")+"/"+chi.URLParam(r, "repo"))
	if !enrolled || !found {
		http.NotFound(w, r)
		return
	}
	built, err := a.reports.revisionReport(ctx, revisionReference(project.RepositoryID, proposal, revision))
	switch {
	case errors.Is(err, governance.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, errReportTooLarge):
		http.Error(w, "the report is too large to show", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		a.reportFailed(w, r, err)
		return
	}
	page := &cappedBuffer{room: maxReportPage}
	if err := reportTemplate.Execute(page, newReportView(project.Repo, commandtext.Reference(proposal, revision), built)); errors.Is(err, errPageTooLarge) {
		http.Error(w, "the report is too large to show", http.StatusRequestEntityTooLarge)
		return
	} else if err != nil {
		a.reportFailed(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(page.Bytes()); err != nil {
		a.logger.DebugContext(r.Context(), "write report", "path", r.URL.Path, "error", err)
	}
}

// reportFailed logs the cause and answers with a fixed text: the cause may quote the store.
func (a *api) reportFailed(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.WarnContext(r.Context(), "report failed", "path", r.URL.Path, "error", err)
	http.Error(w, "the report is unavailable", http.StatusServiceUnavailable)
}

// projectNamed is the enrolled repository called name (owner/name), compared case-insensitively as GitHub names them.
func projectNamed(state owner.State, name string) (owner.ProjectPolicy, bool) {
	i := slices.IndexFunc(state.Projects, func(p owner.ProjectPolicy) bool { return strings.EqualFold(p.Repo, name) })
	if i < 0 {
		return owner.ProjectPolicy{}, false
	}
	return state.Projects[i], true
}

var errPageTooLarge = errors.New("the page is too large")

// cappedBuffer collects a page and refuses what does not fit its room.
type cappedBuffer struct {
	bytes.Buffer
	room int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.room {
		return 0, errPageTooLarge
	}
	b.room -= len(p)
	return b.Buffer.Write(p)
}

// reportView is what the page shows. Every string that comes from the repository goes through commandtext.Visible, which writes the characters
// that hide themselves as escapes; html/template then escapes what is left for the document.
type reportView struct {
	Repo, Reference, Base string
	Facts                 []string
	Truncated             bool
	Files                 []fileView
}

type fileView struct {
	Status, Path, Counts string
	Notes                []string
	Markers              []markerView
	Hunks                []hunkView
}

type markerView struct {
	Kind string
	Line int
	Text string
}

type hunkView struct {
	Header string
	Lines  []lineView
}

type lineView struct {
	Class, Text string
	Cut         bool
}

func newReportView(repo, reference string, built revisionReport) reportView {
	v := reportView{Repo: repo, Reference: reference, Base: string(built.Base), Facts: commandtext.ChangesOf(built.Report).Facts(false), Truncated: built.Report.Truncated}
	for _, f := range built.Report.Files {
		file := fileView{Status: string(f.Status), Path: commandtext.Visible(f.Path), Counts: "no line count", Notes: fileNotes(f)}
		if f.CountsKnown {
			file.Counts = fmt.Sprintf("+%d/−%d", f.LinesAdded, f.LinesRemoved)
		}
		for _, m := range f.Markers {
			file.Markers = append(file.Markers, markerView{Kind: string(m.Kind), Line: m.Line, Text: commandtext.Visible(m.Text)})
		}
		for _, h := range f.Hunks {
			hunk := hunkView{Header: fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)}
			for _, l := range h.Lines {
				hunk.Lines = append(hunk.Lines, lineView{Class: lineClasses[l.Kind], Text: string(rune(l.Kind)) + commandtext.Visible(l.Text), Cut: l.Cut})
				if l.NoNewline {
					hunk.Lines = append(hunk.Lines, lineView{Class: "nonl", Text: `\ No newline at end of file`})
				}
			}
			file.Hunks = append(file.Hunks, hunk)
		}
		v.Files = append(v.Files, file)
	}
	return v
}

var lineClasses = map[report.LineKind]string{report.LineContext: "ctx", report.LineAdded: "add", report.LineRemoved: "del"}

// fileNotes say why a file has no line-by-line diff, or only part of one.
func fileNotes(f report.File) []string {
	var notes []string
	switch f.Limit {
	case report.LimitBinary:
		notes = append(notes, "Binary file, "+sizes(f)+".")
	case report.LimitSize:
		notes = append(notes, "Too large to compare line by line: "+sizes(f)+".")
	case report.LimitEditDistance:
		notes = append(notes, "Too different to compare line by line: the two versions differ by more than 1000 lines.")
	case report.LimitBudget:
		notes = append(notes, "Not compared: the comparison work of this report ran out before this file.")
	}
	if f.Truncated {
		notes = append(notes, "The diff of this file was left out to keep the report within its size budget; the line counts are exact.")
	}
	if f.MarkersTruncated {
		notes = append(notes, "Some marker lines of this file are not listed; the totals above count them all.")
	}
	return notes
}

// sizes are the byte sizes of the sides a file has.
func sizes(f report.File) string {
	switch f.Status {
	case report.Added:
		return strconv.Itoa(f.NewSize) + " bytes"
	case report.Removed:
		return strconv.Itoa(f.OldSize) + " bytes"
	}
	return strconv.Itoa(f.OldSize) + " bytes before and " + strconv.Itoa(f.NewSize) + " bytes after"
}

var reportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>SuiteWard change report {{.Reference}}</title>
<style>
:root { --ink: #12202F; --green: #3DD6A0; --paper: #F4F1EA; --bg: #F4F1EA; --fg: #12202F; --muted: #55606c; --rule: #d6d0c2; --card: #fbf9f4; --accent: #12202F; --add-bg: #d8f3e7; --add-fg: #0a5a3e; --del-bg: #f8dcd7; --del-fg: #86190f; }
@media (prefers-color-scheme: dark) { :root { --bg: #12202F; --fg: #F4F1EA; --muted: #a8b1bb; --rule: #2b3d50; --card: #172a3b; --accent: #3DD6A0; --add-bg: #12382e; --add-fg: #3DD6A0; --del-bg: #402120; --del-fg: #ff9d92; } }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.5 system-ui, sans-serif; }
header, main { max-width: 64rem; margin: 0 auto; padding: 1rem 1.25rem; }
header { border-bottom: 3px solid var(--green); }
.brand { margin: 0; color: var(--accent); font-weight: 700; letter-spacing: .04em; }
h1 { margin: .25rem 0; font-size: 1.6rem; }
h2 { margin: 1.5rem 0 .5rem; font-size: 1.1rem; color: var(--accent); }
.note { color: var(--muted); font-size: .9rem; }
details.file { margin: .75rem 0; border: 1px solid var(--rule); border-radius: .4rem; background: var(--card); }
summary { padding: .5rem .75rem; cursor: pointer; overflow-wrap: anywhere; }
details.file > p, details.file > pre { margin: .25rem .75rem .75rem; }
.status { display: inline-block; min-width: 5.5rem; padding: 0 .4rem; border-radius: .25rem; border: 1px solid var(--rule); font-size: .85rem; text-align: center; }
.status.added { border-color: var(--add-fg); color: var(--add-fg); }
.status.removed { border-color: var(--del-fg); color: var(--del-fg); }
.counts { float: right; margin-left: 1rem; font-family: ui-monospace, monospace; }
.kind { padding: 0 .35rem; border-radius: .25rem; background: var(--rule); font-size: .8rem; }
code, pre { font-family: ui-monospace, monospace; font-size: .85rem; }
pre { overflow-x: auto; border: 1px solid var(--rule); border-radius: .3rem; tab-size: 4; }
pre span { display: block; padding: 0 .5rem; white-space: pre; }
.hh { color: var(--muted); }
.add { background: var(--add-bg); color: var(--add-fg); }
.del { background: var(--del-bg); color: var(--del-fg); }
.nonl, .cut { color: var(--muted); font-style: italic; }
pre .cut { display: inline; padding: 0; }
</style>
</head>
<body>
<header>
<p class="brand">SuiteWard</p>
<h1>Change report {{.Reference}}</h1>
<p>{{.Repo}}: the protected files of this revision against the canonical contract {{.Base}}.</p>
</header>
<main>
<h2>Summary</h2>
<ul>
{{range .Facts}}<li>{{.}}</li>
{{end}}</ul>
<p class="note">SuiteWard counts the changed lines and points out some of them as marker lines. A marker line is a line to look at, not a verdict, and this page is not an approval.</p>
{{if .Truncated}}<p class="note">Some details of this report were left out to keep it within its size budget.</p>
{{end}}<h2>Files</h2>
{{range .Files}}<details class="file" open>
<summary><span class="status {{.Status}}">{{.Status}}</span> <span class="counts">{{.Counts}}</span> <span class="path">{{.Path}}</span></summary>
{{range .Notes}}<p class="note">{{.}}</p>
{{end}}{{range .Markers}}<p class="marker"><span class="kind">{{.Kind}}</span> {{if .Line}}line {{.Line}}: {{end}}<code>{{.Text}}</code></p>
{{end}}{{range .Hunks}}<pre><span class="hh">{{.Header}}</span>{{range .Lines}}<span class="{{.Class}}">{{.Text}}{{if .Cut}}<span class="cut"> … [line cut]</span>{{end}}</span>{{end}}</pre>
{{end}}</details>
{{else}}<p>No protected file differs.</p>
{{end}}</main>
</body>
</html>
`))
