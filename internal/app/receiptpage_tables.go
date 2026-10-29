package app

import (
	"fmt"
	"html/template"
	"strings"
)

// The tables and the lists of the pull request page, and the budgets that keep the page below its cap (docs/contracts/runtime.md, "The pull request page", "The size
// rule"). A row or an entry is written by html/template into a buffer first, so that its size is the number of bytes the page holds, after the escaping that the
// template does; rows are written in order until the budget of the table or the list is spent, and the rest are counted, never named. Only what html/template wrote is
// ever made template.HTML, so the escaping holds whatever the budget cuts.

// The budgets of the page, in escaped bytes. They add up to 9 MiB with the 2 MiB of the fixed part; the cap is above them, so that no page that a correct build produces
// reaches it.
const (
	maxPullPage     = 12 << 20  // the cap of a page: 503 when it is passed, which the budgets keep unreachable
	testTableBudget = 3 << 20   // each table of tests
	fileTableBudget = 1 << 20   // the table of the files of the change report
	listBudget      = 256 << 10 // each of the six lists of the fixed part
	unbudgeted      = 1 << 30   // a table of fixed texts and values of bounded length: the 512 KiB that the rest of the fixed part is given are not counted out
	maxEarlierLinks = 50        // the earlier revisions that the page links, the newest first
)

var (
	rowTemplate  = template.Must(template.New("row").Parse(`<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>`))
	headTemplate = template.Must(template.New("head").Parse(`<tr>{{range .}}<th>{{.}}</th>{{end}}</tr>`))
	itemTemplate = template.Must(template.New("item").Parse(`<li>{{.}}</li>`))
	linkTemplate = template.Must(template.New("link").Parse(`<li><a href="{{.Href}}">{{.Text}}</a></li>`))
)

// markup is what a template writes for data.
func markup(t *template.Template, data any) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// fit writes entry after entry, each followed by a line break, until the next one would pass room; it returns what it wrote and how many entries it did not write. The
// entries are produced one at a time, so that those that do not fit are never rendered.
func fit(count int, entry func(i int) (string, error), room int) (written string, hidden int, err error) {
	var b strings.Builder
	shown := 0
	for ; shown < count; shown++ {
		text, err := entry(shown)
		if err != nil {
			return "", 0, err
		}
		if len(text)+1 > room {
			break
		}
		room -= len(text) + 1
		b.WriteString(text)
		b.WriteByte('\n')
	}
	return b.String(), count - shown, nil
}

// tableOf is a table with a head and the rows that fit its budget, and the number of rows left out. The head and the tags count against the budget.
func tableOf(head []string, rows [][]string, budget int) (table template.HTML, hidden int, err error) {
	top, err := markup(headTemplate, head)
	if err != nil {
		return "", 0, err
	}
	opening, closing := "<table>\n<thead>\n"+top+"\n</thead>\n<tbody>\n", "</tbody>\n</table>\n"
	body, hidden, err := fit(len(rows), func(i int) (string, error) { return markup(rowTemplate, rows[i]) }, budget-len(opening)-len(closing))
	if err != nil {
		return "", 0, err
	}
	return template.HTML(opening + body + closing), hidden, nil
}

// pageLink is a link of a list: where it goes, and what it says.
type pageLink struct{ Href, Text string }

// listOf is a list of the entries that fit its budget, each written by item (itemTemplate for text, linkTemplate for pageLink), and the number of entries left out. A list
// with no entry is no markup.
func listOf[T any](item *template.Template, entries []T, budget int) (list template.HTML, hidden int, err error) {
	if len(entries) == 0 {
		return "", 0, nil
	}
	opening, closing := "<ul>\n", "</ul>\n"
	body, hidden, err := fit(len(entries), func(i int) (string, error) { return markup(item, entries[i]) }, budget-len(opening)-len(closing))
	if err != nil {
		return "", 0, err
	}
	return template.HTML(opening + body + closing), hidden, nil
}

// rowsNotShown says how many rows a budget left out, or nothing when it left none out. one and many name the rows, which the counts above include.
func rowsNotShown(hidden int, one, many string) string {
	switch hidden {
	case 0:
		return ""
	case 1:
		return "1 more " + one + " is not shown here. The counts above include it."
	}
	return fmt.Sprintf("%d more %s are not shown here. The counts above include them.", hidden, many)
}

// entriesNotShown says how many entries of a list a budget left out, or nothing when it left none out.
func entriesNotShown(hidden int) string {
	switch hidden {
	case 0:
		return ""
	case 1:
		return "1 more is not shown here. The count above includes it."
	}
	return fmt.Sprintf("%d more are not shown here. The count above includes them.", hidden)
}
