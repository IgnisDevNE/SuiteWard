package app

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// What the pull request page says (docs/contracts/runtime.md, "The pull request page"): the situation of the head and of each run, the tests by state, and the tests of
// each run. The words of a test come from the tables of commandtext: the page states no sentence of a test that they do not.

// pullView is what the page shows. Its tables and lists hold only the rows and the entries that fit their budgets (receiptpage_tables.go).
type pullView struct {
	Pull                              int
	Repo, Head, ShortHead, Base, Tier string
	Open                              bool
	Notice                            string // the reduced assurance of the co-located tier, empty on the isolated one
	Situation, Weakened               string // one sentence of the situation of the head, and the count of the weakened tests
	Empty                             []string
	Notes                             []string // the situation of each run
	States                            stateSection
	Approved, Receipt                 testsSection
	Kinds                             kindsSection
	Evidence                          table
	Report                            reportSection
}

type stateSection struct {
	Show      bool
	Table     table
	Sentences []string
}

type testsSection struct {
	Show   bool
	Lead   string
	Groups []group // the table of the tests, one group for each state that has a test
	More   string
	Lists  []listBlock
}

type listBlock struct {
	Heading string
	Items   []string
	More    string
}

type kindsSection struct {
	Table   table // the zero table when there is no receipt to judge
	Preview string
}

// situate says the situation of the head and of each wanted run. It fills a domain.Set from the jobs, with the level that each was created under, and asks it whether the
// head reads not responding, as the checks job and the commands job do, from the one Liveness of the process read once.
func (p *receiptPages) situate(v *pullView, runs []pageRun, applies bool, deps pageDeps, open bool) error {
	canonical, receipt := runs[0], runs[1]
	// The level of each plan is that of its own job, as it was when the job was created, and none when the plan has no job: an old job of one plan that outlived a change of
	// the declaration must not name the level of the other.
	set := domain.Set{CanonicalLevel: domain.CanonicalLevelOff, ReceiptLevel: domain.ReceiptLevelOff, ReceiptApplies: applies, ReceiptDecided: true}
	if canonical.found {
		set.CanonicalLevel = canonical.job.CanonicalLevel
	}
	if receipt.found {
		set.ReceiptLevel = receipt.job.ReceiptLevel
	}
	var err error
	if set.CanonicalJob, err = jobOf(canonical); err != nil {
		return err
	}
	if set.ReceiptJob, err = jobOf(receipt); err != nil {
		return err
	}
	now := p.now()
	waiting, reading := set.NotResponding(deps.liveness.Silent(now))
	// The silence was decided by the call above; a request that arrived since moves the last sight forward, so the time shown is never under the 30 s that decided it.
	elapsed := max(now.Sub(deps.liveness.LastSeen()), domain.NotRespondingAfter)
	for _, r := range runs {
		if !r.found {
			continue
		}
		switch {
		case reading && waitingJob(waiting, r) != 0:
			v.Notes = append(v.Notes, r.label+": The test runner is not responding. SuiteWard last heard from it about "+ago(elapsed)+" ago and keeps trying.")
		case r.job.State == execution.StateUnavailable:
			sentence, known := commandtext.UnavailableSentence(string(r.job.Reason))
			if !known { // the store keeps only the reasons of the closed set: another is an impossible state, not a word to show
				return fmt.Errorf("a job is unavailable for the reason %q, which no text knows", r.job.Reason)
			}
			v.Notes = append(v.Notes, r.label+" did not run. "+sentence)
		}
	}
	var unblocked []string
	ended := false
	for _, r := range runs {
		if r.found && r.unblocked {
			unblocked = append(unblocked, plansText(r.plan))
			ended = ended || r.ended
		}
	}
	if len(unblocked) > 0 {
		v.Notes = append(v.Notes, "The owner unblocked this commit, so its checks do not wait for "+strings.Join(unblocked, " and ")+".")
	}
	if ended {
		v.Notes = append(v.Notes, "SuiteWard had stopped waiting for the test runner, which was not responding.")
	}
	// The runner is back (the rule of the comment of that name): a job that the owner unblocked ended for no-executor before the runner's last request. The page says it
	// for a pull request that is open, which a new push can still run.
	if open && slices.ContainsFunc(runs, func(r pageRun) bool {
		return r.found && r.unblocked && r.job.State == execution.StateUnavailable && r.job.Reason == execution.ReasonNoExecutor && deps.liveness.HeardSince(r.job.FinishedAt)
	}) {
		v.Notes = append(v.Notes, "The test runner is responding again. A new push runs the tests.")
	}
	v.Situation = headSituation(runs, reading)
	return nil
}

// jobOf is the job of a run as the rules of the comments of a head read it: the zero job for a plan with none.
func jobOf(r pageRun) (domain.Job, error) {
	if !r.found {
		return domain.Job{}, nil
	}
	job, ok := r.job.InSet(r.unblocked)
	if !ok { // the gates never return a cancelled job
		return domain.Job{}, fmt.Errorf("read the %s job %d: it is %s: only a job that is not cancelled is read", r.plan, r.job.ID, r.job.State)
	}
	return job, nil
}

func waitingJob(w domain.Waiting, r pageRun) int64 {
	if r.plan == execution.PlanCanonical {
		return w.Jobs.Canonical
	}
	return w.Jobs.Receipt
}

// plansText names a plan as the sentences of the comments do.
func plansText(plan execution.Plan) string {
	if plan == execution.PlanCanonical {
		return "the approved tests"
	}
	return "the receipt"
}

// headSituation is the one sentence of the situation of the head, the first that matches: not responding, running, unblocked, not run, partly run, run.
func headSituation(runs []pageRun, reading bool) string {
	found := slices.DeleteFunc(slices.Clone(runs), func(r pageRun) bool { return !r.found })
	switch {
	case len(found) == 0:
		return ""
	case reading:
		return "The tests of this commit are waiting for the test runner, which is not responding."
	case slices.ContainsFunc(found, pageRun.open):
		return "The tests of this commit have not finished yet."
	case slices.ContainsFunc(found, func(r pageRun) bool { return r.unblocked }):
		return "The owner unblocked this commit, so its checks do not wait for its tests."
	case !slices.ContainsFunc(found, pageRun.complete):
		return "The tests of this commit did not run."
	case slices.ContainsFunc(found, func(r pageRun) bool { return !r.complete() }):
		return "SuiteWard ran some of the tests for this commit. The others did not run."
	}
	return "SuiteWard ran the tests for this commit."
}

// ago says how long ago in the words of the page: seconds below a minute, rounded to the second, and minutes above it, rounded to the minute.
func ago(d time.Duration) string {
	if s := int(d.Round(time.Second) / time.Second); s < 60 {
		return counted(s, "second")
	}
	return counted(int(d.Round(time.Minute)/time.Minute), "minute")
}

func counted(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// canonicalCounts counts the tests of a complete canonical run by state.
func canonicalCounts(r pageRun) (domain.CanonicalCounts, error) {
	states := make([]domain.CanonicalState, len(r.tests))
	for i, t := range r.tests {
		state, known := canonicalStates[t.State]
		if !known {
			return domain.CanonicalCounts{}, fmt.Errorf("job %d stores the test state %q, which is no state of its plan", r.job.ID, t.State)
		}
		states[i] = state
	}
	counts, err := domain.CountCanonical(states)
	if err != nil {
		return domain.CanonicalCounts{}, fmt.Errorf("count the tests of job %d: %w", r.job.ID, err)
	}
	return counts, nil
}

// receiptCounts counts the tests of a complete receipt by state, with the changed test files that do not parse, which list no test.
func receiptCounts(r pageRun) (domain.ReceiptCounts, error) {
	states := make([]domain.ReceiptState, len(r.tests))
	for i, t := range r.tests {
		state, known := receiptStates[t.State]
		if !known {
			return domain.ReceiptCounts{}, fmt.Errorf("job %d stores the test state %q, which is no state of its plan", r.job.ID, t.State)
		}
		states[i] = state
	}
	counts, err := domain.CountReceipt(states)
	if err != nil {
		return domain.ReceiptCounts{}, fmt.Errorf("count the tests of job %d: %w", r.job.ID, err)
	}
	counts.Unparsed = len(r.result.Unparsed)
	return counts, nil
}

// tables fills the tests by state and the tests of each run.
func (p *receiptPages) tables(v *pullView, canonical, receipt pageRun, weak map[domain.TestID]bool) error {
	var ccounts domain.CanonicalCounts
	var rcounts domain.ReceiptCounts
	var err error
	if canonical.complete() {
		if ccounts, err = canonicalCounts(canonical); err != nil {
			return err
		}
	}
	if receipt.complete() {
		if rcounts, err = receiptCounts(receipt); err != nil {
			return err
		}
	}
	if v.Approved, err = canonicalSection(canonical, ccounts, weak); err != nil {
		return err
	}
	if v.Receipt, err = receiptSection(receipt, rcounts, weak); err != nil {
		return err
	}
	v.States, err = statesOf(canonical, ccounts, receipt, rcounts)
	return err
}

// statesOf is the table of the tests by state, with what each state means, and the sentences that count what the rows do not.
func statesOf(canonical pageRun, cc domain.CanonicalCounts, receipt pageRun, rc domain.ReceiptCounts) (stateSection, error) {
	if !canonical.found && !receipt.found {
		return stateSection{}, nil
	}
	approved := map[string]int{"PASS": cc.Pass, "FAIL": cc.Fail, "SKIPPED": cc.Skipped, "MISSING": cc.Missing, "FLAKY": cc.Flaky, "EXCLUDED": cc.Excluded}
	changed := map[string]int{"DETECTS": rc.Detects, "INERT": rc.Inert, "NEW API": rc.NewAPI, "FAILS": rc.Fails, "SKIPPED": rc.Skipped, "MISSING": rc.Missing, "FLAKY": rc.Flaky, "EXCLUDED": rc.Excluded}
	var rows [][]string
	for _, m := range commandtext.StateMeanings() {
		cells := []string{m.State, m.Meaning, "-", "-"}
		if m.Canonical && canonical.complete() {
			cells[2] = strconv.Itoa(approved[m.State])
		}
		if m.Receipt && receipt.complete() {
			cells[3] = strconv.Itoa(changed[m.State])
		}
		rows = append(rows, cells)
	}
	states, _, err := tableOf([]string{"State", "What it means", canonicalLabel, "This change's tests"}, rows, unbudgeted)
	if err != nil {
		return stateSection{}, err
	}
	s := stateSection{Show: true, Table: states}
	const below = " They are listed below, under the tests of this change."
	if cc.Excluded > 0 {
		s.Sentences = append(s.Sentences, "Approved tests that SuiteWard cannot run, so they are not covered: "+strconv.Itoa(cc.Excluded)+".")
	}
	if rc.Excluded > 0 {
		s.Sentences = append(s.Sentences, "Tests of this change that SuiteWard cannot run, so they are not covered: "+strconv.Itoa(rc.Excluded)+".")
	}
	if n := len(receipt.result.Markers); receipt.complete() && n > 0 {
		s.Sentences = append(s.Sentences, "Lines to look at in the tests of this change: "+strconv.Itoa(n)+"."+below)
	}
	if rc.Unparsed > 0 {
		s.Sentences = append(s.Sentences, "Changed test files that cannot be read: "+strconv.Itoa(rc.Unparsed)+"."+below)
	}
	if n := len(receipt.job.UnapprovedPaths); receipt.found && n > 0 {
		s.Sentences = append(s.Sentences, notEvaluated+": "+unapprovedCount(n)+"."+below)
	}
	return s, nil
}

// testRow is a row of a table of tests, with what orders it: the states by their rank (the rows of a state are one group of the table), then the weakened rows, then the order the
// store gives (package and name). The contract orders a group as the rows with a reason, then the rows with a note, then the others: the reason of a test depends on its state
// alone, so within a group that is the weakened rows first.
type testRow struct {
	weakened bool
	rank     int
	cells    []string
}

// The order of the states of a table, the first being the one to read first: the flagged states by severity, then the skipped and excluded tests, which checked nothing, then
// the passes. For the receipt, INERT shows nothing about the change, so it comes before NEW API and DETECTS.
var (
	canonicalOrder = []domain.CanonicalState{domain.CanonicalFail, domain.CanonicalMissing, domain.CanonicalFlaky, domain.CanonicalSkipped, domain.CanonicalExcluded, domain.CanonicalPass}
	receiptOrder   = []domain.ReceiptState{domain.ReceiptFails, domain.ReceiptMissing, domain.ReceiptFlaky, domain.ReceiptSkipped, domain.ReceiptExcluded, domain.ReceiptInert,
		domain.ReceiptNewAPI, domain.ReceiptDetects}
)

// parseObservations reads the stored runs of a side; a run that is none is an impossible state.
func parseObservations(runs []string) ([]domain.Observation, error) {
	out := make([]domain.Observation, len(runs))
	for i, run := range runs {
		o, err := domain.ParseObservation(run)
		if err != nil {
			return nil, err
		}
		out[i] = o
	}
	return out, nil
}

// reasonText is the reason of a test in words: one sentence for each reason, in order. A reason that no sentence says is an impossible state.
func reasonText(plan domain.PlanKind, reasons []domain.TestReason) (string, error) {
	sentences := make([]string, len(reasons))
	for i, reason := range reasons {
		sentence, known := commandtext.TestReasonSentence(plan, reason)
		if !known {
			return "", fmt.Errorf("no sentence says the reason %q", reason.Code)
		}
		sentences[i] = sentence
	}
	return strings.Join(sentences, " "), nil
}

// testRows are the rows of a complete run, as its table writes them: the state, the test, the package, the file, the reason in words and the weakened marker.
func testRows(r pageRun, weak map[domain.TestID]bool) ([]testRow, error) {
	canonical := r.plan == execution.PlanCanonical
	note, plan := "Weakened: the approved version of this test fails on this commit.", domain.PlanReceipt
	if canonical {
		note, plan = "Weakened: this change edits this test.", domain.PlanCanonical
	}
	rows := make([]testRow, len(r.tests))
	for i, t := range r.tests {
		head, err := parseObservations(t.HeadRuns)
		if err != nil {
			return nil, fmt.Errorf("job %d: the head runs of %s: %w", r.job.ID, t.Name, err)
		}
		var reasons []domain.TestReason
		var rank int
		if canonical {
			state, known := canonicalStates[t.State]
			if !known {
				return nil, fmt.Errorf("job %d stores the test state %q, which is no state of its plan", r.job.ID, t.State)
			}
			rank = slices.Index(canonicalOrder, state)
			reasons, err = domain.CanonicalTestReasons(state, head, t.ExcludedReason)
		} else {
			state, known := receiptStates[t.State]
			if !known {
				return nil, fmt.Errorf("job %d stores the test state %q, which is no state of its plan", r.job.ID, t.State)
			}
			var base []domain.Observation
			if base, err = parseObservations(t.BaseRuns); err != nil {
				return nil, fmt.Errorf("job %d: the base runs of %s: %w", r.job.ID, t.Name, err)
			}
			rank = slices.Index(receiptOrder, state)
			reasons, err = domain.ReceiptTestReasons(state, head, base, t.ExcludedReason)
		}
		if err != nil {
			return nil, fmt.Errorf("job %d: the reason of %s: %w", r.job.ID, t.Name, err)
		}
		reason, err := reasonText(plan, reasons)
		if err != nil {
			return nil, fmt.Errorf("job %d: %w", r.job.ID, err)
		}
		row := testRow{weakened: weak[domain.TestID{Package: t.Package, Name: t.Name}], rank: rank,
			cells: []string{strings.ToUpper(strings.ReplaceAll(t.State, "_", " ")), commandtext.Visible(t.Name), commandtext.Visible(t.Package), commandtext.Visible(t.File), reason, ""}}
		if row.weakened {
			row.cells[5] = note
		}
		rows[i] = row
	}
	slices.SortStableFunc(rows, func(a, b testRow) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if a.weakened != b.weakened {
			if a.weakened {
				return -1
			}
			return 1
		}
		return 0
	})
	return rows, nil
}

// testGroups is the table of the tests of a run as one group for each state that has a test, within one budget, and the sentence of the rows that it left out.
func testGroups(r pageRun, weak map[domain.TestID]bool) ([]group, string, error) {
	rows, err := testRows(r, weak)
	if err != nil {
		return nil, "", err
	}
	var states []stateTests
	for _, row := range rows {
		if len(states) == 0 || states[len(states)-1].state != row.cells[0] {
			states = append(states, stateTests{state: row.cells[0]})
		}
		last := &states[len(states)-1]
		last.rows = append(last.rows, row.cells)
	}
	groups, hidden, err := groupsOf([]string{"State", "Test", "Package", "File", "Reason", "Note"}, states, testTableBudget)
	return groups, rowsNotShown(hidden, "test", "tests"), err
}

// canonicalSection is the approved tests: what ran, and the table of their tests. A run that did not complete has no table: its situation takes its place.
func canonicalSection(r pageRun, counts domain.CanonicalCounts, weak map[domain.TestID]bool) (testsSection, error) {
	if !r.found {
		return testsSection{}, nil
	}
	s := testsSection{Show: true}
	switch {
	case r.complete():
		var err error
		s.Lead = ranText(counts.Pass + counts.Fail + counts.Skipped + counts.Missing + counts.Flaky)
		s.Groups, s.More, err = testGroups(r, weak)
		return s, err
	case r.open():
		s.Lead = "This run has not finished, so there are no results to show yet."
	default:
		s.Lead = "This run has no results: the tests did not run."
	}
	return s, nil
}

func ranText(n int) string {
	if n == 1 {
		return "1 test ran."
	}
	return strconv.Itoa(n) + " tests ran."
}

// namedList is a heading and the entries under it.
type namedList struct {
	heading string
	entries []string
}

// receiptSection is the tests that the change adds or edits: the files that the receipt covers, the paths not evaluated, the table of the tests and the lists of the run.
func receiptSection(r pageRun, counts domain.ReceiptCounts, weak map[domain.TestID]bool) (testsSection, error) {
	if !r.found {
		return testsSection{}, nil
	}
	s := testsSection{Show: true}
	listed := "Files that this revision adds or changes under the approved patterns: " + strconv.Itoa(r.job.ChangedTotal)
	if r.job.ChangedTotal > len(r.job.ChangedPaths) {
		listed += " (" + strconv.Itoa(len(r.job.ChangedPaths)) + " listed)"
	}
	unapproved := notEvaluated + ": " + unapprovedCount(len(r.job.UnapprovedPaths))
	if len(r.job.UnapprovedPaths) >= maxExecutionPaths {
		unapproved += " (" + strconv.Itoa(len(r.job.UnapprovedPaths)) + " listed)"
	}
	lists := []namedList{{listed, visibleAll(r.job.ChangedPaths)}, {unapproved, visibleAll(r.job.UnapprovedPaths)}}
	switch {
	case r.complete():
		s.Lead = "Tests this change adds or edits: " + strconv.Itoa(counts.Evaluated()) + "."
		if counts.Empty() {
			s.Lead = "This change adds or edits no test that SuiteWard can run."
		}
		markers := make([]string, len(r.result.Markers))
		for i, m := range r.result.Markers {
			markers[i] = markerLabel(commandtext.Visible(m.Kind)) + ": " + commandtext.Visible(m.Path) + " line " + strconv.Itoa(m.Line)
		}
		lists = append(lists, namedList{countedHeading("Skipped submodules", len(r.result.Submodules)), visibleAll(r.result.Submodules)},
			namedList{countedHeading("Changed test files that cannot be read", len(r.result.Unparsed)), visibleAll(r.result.Unparsed)},
			namedList{countedHeading("Lines to look at", len(markers)), markers})
		var err error
		if s.Groups, s.More, err = testGroups(r, weak); err != nil {
			return testsSection{}, err
		}
	case r.open():
		s.Lead = "This run has not finished, so there are no results to show yet."
	default:
		s.Lead = "This run has no results: the tests did not run."
	}
	for i, l := range lists {
		if i > 0 && len(l.entries) == 0 { // the files that the receipt covers are always counted, even when none: the other lists only when they have entries
			continue
		}
		items, hidden, err := textList(l.entries, listBudget)
		if err != nil {
			return testsSection{}, err
		}
		s.Lists = append(s.Lists, listBlock{Heading: l.heading, Items: items, More: entriesNotShown(hidden)})
	}
	return s, nil
}

// visibleAll shows each path with its hidden characters written as escapes.
func visibleAll(paths []string) []string {
	lines := make([]string, len(paths))
	for i, path := range paths {
		lines[i] = commandtext.Visible(path)
	}
	return lines
}

// notEvaluated says that a file is not evaluated because its pattern is not approved yet.
const notEvaluated = "Files not evaluated because their pattern is not approved yet"

// countedHeading is the heading of a list with the number of its entries, so that "the count above includes them" is true of a list that a budget cuts.
func countedHeading(label string, n int) string { return label + ": " + strconv.Itoa(n) }

// unapprovedCount says how many files are not evaluated. The job keeps at most maxExecutionPaths of them and stores no total, so a list that holds that many says "at least".
func unapprovedCount(n int) string {
	if n >= maxExecutionPaths {
		return "at least " + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
