package app

import (
	"fmt"
	"strconv"
)

// The tables and the lists of the pull request page, and the budgets that keep the page below its cap (docs/contracts/runtime.md, "The pull request page", "The size
// rule"). The template writes every row and every entry, and a budget counts the bytes that the very same template block writes for one of them, after the escaping that
// html/template does: rows are kept in order until the budget of the table or the list is spent, and the rest are counted, never named. No string is ever made markup
// by hand, so the escaping holds whatever the budget cuts.

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

// table is a head and the rows of a table that fit its budget. The zero table is no table.
type table struct {
	Head []string
	Rows [][]string
}

// pageLink is a link of a list: where it goes, and what it says.
type pageLink struct{ Href, Text string }

// counter counts the bytes written to it.
type counter int

func (c *counter) Write(p []byte) (int, error) {
	*c += counter(len(p))
	return len(p), nil
}

// sizeOf is the number of bytes that the block of the page template called name writes for data.
func sizeOf(name string, data any) (int, error) {
	var n counter
	if err := pullTemplate.ExecuteTemplate(&n, name, data); err != nil {
		return 0, err
	}
	return int(n), nil
}

// fit says how many of count entries, written in order, fit room, and how much room is left: entry i takes size(i) bytes and a line break. It asks the size of an entry only
// while the entries fit, so that those left out are never written.
func fit(count int, size func(i int) (int, error), room int) (shown, left int, err error) {
	for ; shown < count; shown++ {
		n, err := size(shown)
		if err != nil {
			return 0, 0, err
		}
		if n+1 > room {
			break
		}
		room -= n + 1
	}
	return shown, room, nil
}

// tableOf is a table with a head and the rows that fit its budget, and the number of rows left out. The head and the tags count against the budget.
func tableOf(head []string, rows [][]string, budget int) (kept table, hidden int, err error) {
	fixed, err := sizeOf("table", table{Head: head})
	if err != nil {
		return table{}, 0, err
	}
	shown, _, err := fit(len(rows), func(i int) (int, error) { return sizeOf("row", rows[i]) }, budget-fixed)
	if err != nil {
		return table{}, 0, err
	}
	return table{Head: head, Rows: rows[:shown]}, len(rows) - shown, nil
}

// group is the table of the tests of one state, folded under a summary that names the state and counts its tests.
type group struct {
	Summary string
	Table   table
}

// stateTests are the rows of the tests of one state, in the order of their table, and the state in the words of the table of the tests by state.
type stateTests struct {
	state string
	rows  [][]string
}

// groupsOf writes the tests of a run as one group for each state, in the order given, within one budget for all of them. The groups and the rows are kept in order until the
// budget is spent: the rows that did not fit, and every group after the one that ran out, are counted, never named, and a group with no row that fits is not written. The summary
// counts every test of its state, shown or not, so that the counts of the page include the rows that a budget cut.
func groupsOf(head []string, states []stateTests, budget int) (kept []group, hidden int, err error) {
	room := budget
	for i, s := range states {
		summary := s.state + " (" + strconv.Itoa(len(s.rows)) + ")"
		fixed, err := sizeOf("group", group{Summary: summary, Table: table{Head: head}})
		if err != nil {
			return nil, 0, err
		}
		shown, left, err := fit(len(s.rows), func(j int) (int, error) { return sizeOf("row", s.rows[j]) }, room-fixed)
		if err != nil {
			return nil, 0, err
		}
		if shown > 0 {
			kept = append(kept, group{Summary: summary, Table: table{Head: head, Rows: s.rows[:shown]}})
		}
		if shown < len(s.rows) {
			hidden = len(s.rows) - shown
			for _, rest := range states[i+1:] {
				hidden += len(rest.rows)
			}
			return kept, hidden, nil
		}
		room = left
	}
	return kept, 0, nil
}

// textList is the entries of a list of text that fit its budget, and the number left out.
func textList(entries []string, budget int) (kept []string, hidden int, err error) {
	return fitList("list", "item", entries, budget)
}

// linkList is the links of a list that fit its budget, and the number left out.
func linkList(links []pageLink, budget int) (kept []pageLink, hidden int, err error) {
	return fitList("links", "link", links, budget)
}

// fitList keeps the entries of the list block (whose entries are the item block) that fit the budget. A list with no entry is written as nothing by the page.
func fitList[T any](list, item string, entries []T, budget int) (kept []T, hidden int, err error) {
	if len(entries) == 0 {
		return nil, 0, nil
	}
	fixed, err := sizeOf(list, []T(nil))
	if err != nil {
		return nil, 0, err
	}
	shown, _, err := fit(len(entries), func(i int) (int, error) { return sizeOf(item, entries[i]) }, budget-fixed)
	if err != nil {
		return nil, 0, err
	}
	return entries[:shown], len(entries) - shown, nil
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
