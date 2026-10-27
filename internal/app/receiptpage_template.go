package app

import "html/template"

// receiptTemplate is the page of GET /receipts/{owner}/{repo}/{pull}: one document with inline style, no script and nothing loaded from elsewhere (the guard of the route
// sets a policy that allows nothing else). html/template escapes every string; the strings that hold repository text were also made visible by commandtext.Visible.
var receiptTemplate = template.Must(template.New("receipt").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>SuiteWard receipt of pull request {{.Pull}}</title>
<style>
:root { --ink: #12202F; --green: #3DD6A0; --paper: #F4F1EA; --bg: #F4F1EA; --fg: #12202F; --muted: #55606c; --rule: #d6d0c2; --card: #fbf9f4; --accent: #12202F; }
@media (prefers-color-scheme: dark) { :root { --bg: #12202F; --fg: #F4F1EA; --muted: #a8b1bb; --rule: #2b3d50; --card: #172a3b; --accent: #3DD6A0; } }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.5 system-ui, sans-serif; }
header, main { max-width: 64rem; margin: 0 auto; padding: 1rem 1.25rem; }
header { border-bottom: 3px solid var(--green); }
.brand { margin: 0; color: var(--accent); font-weight: 700; letter-spacing: .04em; }
h1 { margin: .25rem 0; font-size: 1.6rem; }
h2 { margin: 1.5rem 0 .5rem; font-size: 1.2rem; color: var(--accent); }
h3 { margin: 1rem 0 .25rem; font-size: 1rem; }
.note { color: var(--muted); font-size: .9rem; }
section { margin: 1rem 0; padding: .25rem 1rem .5rem; border: 1px solid var(--rule); border-radius: .4rem; background: var(--card); }
code, pre { font-family: ui-monospace, monospace; font-size: .85rem; }
pre { overflow-x: auto; border: 1px solid var(--rule); border-radius: .3rem; padding: .5rem; white-space: pre; }
li { overflow-wrap: anywhere; }
</style>
</head>
<body>
<header>
<p class="brand">SuiteWard</p>
<h1>Receipt of pull request {{.Pull}}</h1>
<p>{{.Repo}}: the execution results of the current head of this pull request.</p>
</header>
<main>
<ul>
<li>Head: {{.Head}}</li>
<li>Base branch head: {{.Base}}</li>
<li>Installation tier: {{.Tier}}</li>
</ul>
{{if not .Open}}<p>This pull request is not open: the results are those of the last head recorded for it.</p>
{{end}}<p class="note">The results below are evidence reported by an executor, not a verdict.</p>
{{if .Notice}}<p class="note">{{.Notice}}</p>
{{end}}{{range .Empty}}<p>{{.}}</p>
{{end}}{{range .Runs}}<section>
<h2>{{.Title}}</h2>
<ul>
{{range .Facts}}<li>{{.}}</li>
{{end}}</ul>
{{range .Notes}}<p>{{.}}</p>
{{end}}{{if .Counts}}<ul>
{{range .Counts}}<li>{{.}}</li>
{{end}}</ul>
{{end}}{{if .Modes}}<h3>Mode summaries</h3>
<ul>
{{range .Modes}}<li>{{.}}</li>
{{end}}</ul>
{{end}}{{range .Lists}}<h3>{{.Heading}}</h3>
<pre>{{range .Lines}}{{.}}
{{end}}</pre>
{{end}}</section>
{{end}}<p>{{.Preview}}</p>
</main>
</body>
</html>
`))
