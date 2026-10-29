package app

import "html/template"

// pullTemplate is the page of GET /pulls/{owner}/{repo}/{pull}: one document with inline style, no script and nothing loaded from elsewhere (the guard of the route sets a
// policy that allows nothing else). html/template escapes every string; the strings that hold repository text were also made visible by commandtext.Visible. The blocks that
// it defines (table, row, list, item, links, link) are the ones that the budgets count the bytes of (receiptpage_tables.go): a table and a list are written by them alone.
var pullTemplate = template.Must(template.New("pull").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>SuiteWard pull request {{.Pull}}</title>
<style>
:root { --ink: #12202F; --green: #3DD6A0; --paper: #F4F1EA; --bg: #F4F1EA; --fg: #12202F; --muted: #55606c; --rule: #d6d0c2; --card: #fbf9f4; --accent: #12202F; }
@media (prefers-color-scheme: dark) { :root { --bg: #12202F; --fg: #F4F1EA; --muted: #a8b1bb; --rule: #2b3d50; --card: #172a3b; --accent: #3DD6A0; } }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.5 system-ui, sans-serif; }
header, main { max-width: 72rem; margin: 0 auto; padding: 1rem 1.25rem; }
header { border-bottom: 3px solid var(--green); }
.brand { margin: 0; color: var(--accent); font-weight: 700; letter-spacing: .04em; }
h1 { margin: .25rem 0; font-size: 1.6rem; }
h2 { margin: 1.75rem 0 .5rem; font-size: 1.2rem; color: var(--accent); }
h3 { margin: 1rem 0 .25rem; font-size: 1rem; }
.note { color: var(--muted); font-size: .9rem; }
.lead { font-weight: 600; }
code { font-family: ui-monospace, monospace; font-size: .85rem; }
table { border-collapse: collapse; width: 100%; margin: .5rem 0; background: var(--card); font-size: .9rem; }
th, td { padding: .3rem .5rem; border: 1px solid var(--rule); text-align: left; vertical-align: top; overflow-wrap: anywhere; }
th { background: var(--bg); }
li { overflow-wrap: anywhere; }
</style>
</head>
<body>
<header>
<p class="brand">SuiteWard</p>
<h1>Pull request {{.Pull}}</h1>
<p>{{.Repo}}: the situation of this pull request and the results for its latest commit.</p>
</header>
<main>
<ul>
<li>Pull request: {{.Pull}}</li>
<li>Repository: {{.Repo}}</li>
<li>Commit: {{.ShortHead}} (full SHA: {{.Head}})</li>
<li>Base branch commit: {{.Base}}</li>
<li>Installation tier: {{.Tier}}</li>
</ul>
{{if not .Open}}<p>This pull request is not open: the results are those of the last commit recorded for it.</p>
{{end}}{{if .Situation}}<p class="lead">{{.Situation}}</p>
{{end}}{{if .Weakened}}<p>{{.Weakened}}</p>
{{end}}{{range .Empty}}<p>{{.}}</p>
{{end}}{{if .Notes}}<h2>Situation</h2>
<ul>
{{range .Notes}}<li>{{.}}</li>
{{end}}</ul>
{{end}}{{if .States.Show}}<h2>Tests by state</h2>
{{template "table" .States.Table}}{{range .States.Sentences}}<p>{{.}}</p>
{{end}}{{end}}{{if .Approved.Show}}<h2>Approved tests</h2>
<p>{{.Approved.Lead}}</p>
{{if .Approved.Table.Head}}{{template "table" .Approved.Table}}{{end}}{{if .Approved.More}}<p>{{.Approved.More}}</p>
{{end}}{{end}}{{if .Receipt.Show}}<h2>Tests this change adds or edits</h2>
<p>{{.Receipt.Lead}}</p>
{{range .Receipt.Lists}}<h3>{{.Heading}}</h3>
{{if .Items}}{{template "list" .Items}}{{end}}{{if .More}}<p>{{.More}}</p>
{{end}}{{end}}{{if .Receipt.Table.Head}}{{template "table" .Receipt.Table}}{{end}}{{if .Receipt.More}}<p>{{.Receipt.More}}</p>
{{end}}{{end}}<h2>The kinds of change</h2>
{{if .Kinds.Table.Head}}{{template "table" .Kinds.Table}}{{else}}<p>No receipt to judge.</p>
{{end}}<p>{{.Kinds.Preview}}</p>
{{if .Evidence.Head}}<h2>How these results were made</h2>
{{template "table" .Evidence}}{{end}}{{if .Report.Show}}<h2>Change report</h2>
{{if .Report.Sentence}}<p>{{.Report.Sentence}}</p>
{{else}}<p>The change report of revision {{.Report.Reference}}, without the changed lines.</p>
<ul>
{{range .Report.Facts}}<li>{{.}}</li>
{{end}}</ul>
{{if .Report.Files.Head}}{{template "table" .Report.Files}}{{end}}{{if .Report.More}}<p>{{.Report.More}}</p>
{{end}}<p><a href="{{.Report.Full.Href}}">{{.Report.Full.Text}}</a></p>
{{if .Report.Earlier}}<h3>{{.Report.EarlierHeading}}</h3>
{{template "links" .Report.Earlier}}{{if .Report.EarlierMore}}<p>{{.Report.EarlierMore}}</p>
{{end}}{{end}}{{end}}{{end}}<p class="note">The results on this page are evidence reported by a test runner, not a verdict.</p>
{{if .Notice}}<p class="note">{{.Notice}}</p>
{{end}}</main>
</body>
</html>
{{define "head"}}<tr>{{range .}}<th>{{.}}</th>{{end}}</tr>{{end}}
{{define "row"}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}
{{define "table"}}<table>
<thead>
{{template "head" .Head}}
</thead>
<tbody>
{{range .Rows}}{{template "row" .}}
{{end}}</tbody>
</table>
{{end}}
{{define "item"}}<li>{{.}}</li>{{end}}
{{define "list"}}<ul>
{{range .}}{{template "item" .}}
{{end}}</ul>
{{end}}
{{define "link"}}<li><a href="{{.Href}}">{{.Text}}</a></li>{{end}}
{{define "links"}}<ul>
{{range .}}{{template "link" .}}
{{end}}</ul>
{{end}}
`))
