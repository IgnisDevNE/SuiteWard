package app

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// What the pull request page reads. The job of each plan is the one a gate reads, through the execution reader; the rows and the evidence of a job are read here, for
// display only. Every string that the repository or a test runner chose goes through commandtext.Visible before it reaches the document, and html/template escapes what
// is left.

// pageDeps is what the handler gives the page beside its stores: the reports of the process, the one record of when the test runner was last seen, and the logger.
type pageDeps struct {
	reports  pageReports
	liveness *execution.Liveness
	logger   *slog.Logger
}

// pageRun is one run of the head as the page reads it: its job, its state, its evidence, what the owner did to it and, for a complete run, its tests.
type pageRun struct {
	plan      execution.Plan
	label     string // how the page names the run
	job       execution.Job
	found     bool
	status    domain.RunStatus
	result    execution.StoredResult
	ran       bool             // a test runner's result is stored: the run has an evidence binding
	unblocked bool             // an unblock row since the enrollment names the job
	ended     bool             // the command that unblocked it ended the job
	tests     []execution.Test // of a complete run
}

const (
	canonicalLabel = "Approved tests"
	receiptLabel   = "Tests this change adds or edits"
)

// readRun reads the run of the job that gateJob found for the plan, if it found one: the job is never read twice.
func (p *receiptPages) readRun(ctx context.Context, reader executionReader, plan execution.Plan, job execution.Job, found bool, unblocks []execution.JobUnblock) (pageRun, error) {
	status, _, err := reader.runOf(job, found)
	if err != nil {
		return pageRun{}, err
	}
	run := pageRun{plan: plan, label: canonicalLabel, job: job, found: found, status: status}
	if plan == execution.PlanReceipt {
		run.label = receiptLabel
	}
	if !found {
		return run, nil
	}
	run.unblocked = slices.ContainsFunc(unblocks, func(u execution.JobUnblock) bool { return u.JobID == job.ID })
	run.ended = job.EndedBy(unblocks)
	result, stored, err := p.executions.Result(ctx, job.ID)
	if err != nil {
		return pageRun{}, fmt.Errorf("read the result of job %d: %w", job.ID, err)
	}
	run.result, run.ran = result, stored && result.Origin == execution.OriginExecutor
	if status == domain.RunComplete {
		if run.tests, err = p.executions.Tests(ctx, job.ID); err != nil {
			return pageRun{}, fmt.Errorf("read the tests of job %d: %w", job.ID, err)
		}
	}
	return run, nil
}

// complete says that the run has its results.
func (r pageRun) complete() bool { return r.found && r.status == domain.RunComplete }

// open says that the run has not ended: its job is pending or leased.
func (r pageRun) open() bool { return r.found && r.status == domain.RunPending }

// page builds the page of a pull request of project, waiting for its turn until ctx ends: the turn of the reports of the process, which the report pages and the builds of
// a revision comment or a check summary wait for too, so that a page of two runs of 10,000 tests and a report of 32 MiB are never built at the same time. A page over its
// limit is errPageTooLarge, and nothing of it is cut.
func (p *receiptPages) page(ctx context.Context, state owner.State, project owner.ProjectPolicy, number int, deps pageDeps) ([]byte, error) {
	release, err := deps.reports.turn(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	view, err := p.read(ctx, state, project, number, deps)
	if err != nil {
		return nil, err
	}
	page := &cappedBuffer{room: p.limit}
	if err := pullTemplate.Execute(page, view); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}

// headRuns is what the page reads of the head of a pull request: the revision that it carries and the run of each plan.
type headRuns struct {
	revised            bool
	expected           bool                         // the revision expects a canonical version: it is not a baseline
	revisions          []contract.ProposalReference // every revision of the proposal that the pull request carries, oldest first
	current            contract.ProposalReference
	applies            bool // the receipt applies to the revision
	canonical, receipt pageRun
}

// readHead reads the revision that the pull request carries and the job of each plan for the head: the ones a gate reads, and the unblocks of the head.
func (p *receiptPages) readHead(ctx context.Context, state owner.State, project owner.ProjectPolicy, number int, head string) (headRuns, error) {
	reader := executionReader{executions: p.executions, contents: p.contents, suites: p.suites, report: wrapRead}
	var h headRuns
	carrier := carrierProposal(project.RepositoryID, number)
	view, err := reader.receiptView(ctx, project.RepositoryID, func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
		proposal, found, err := carrier(ctx, tx)
		if err == nil && found {
			h.current = proposal.Current().Binding().Reference()
			for _, revision := range proposal.Revisions() {
				h.revisions = append(h.revisions, revision.Binding().Reference())
			}
		}
		return proposal, found, err
	})
	if err != nil {
		return headRuns{}, err
	}
	h.revised, h.expected = view.revised, !view.expected.IsZero()
	canonicalJob, hasCanonical, err := reader.gateJob(ctx, project.RepositoryID, number, head, execution.PlanCanonical, artifact.Digest{})
	if err != nil {
		return headRuns{}, err
	}
	// The gates ask whether the receipt applies before they read its job, and so does the page: a revision that expects no canonical, a baseline, has no receipt.
	applies, governing, err := reader.receiptApplies(ctx, view)
	if err != nil {
		return headRuns{}, err
	}
	h.applies = applies
	var receiptJob execution.Job
	var hasReceipt bool
	if applies {
		if receiptJob, hasReceipt, err = reader.gateJob(ctx, project.RepositoryID, number, head, execution.PlanReceipt, governing); err != nil {
			return headRuns{}, err
		}
	}
	unblocks, err := p.executions.HeadUnblocks(ctx, project.RepositoryID, number, head, state.EnrolledAt)
	if err != nil {
		return headRuns{}, fmt.Errorf("read the unblocks of the head: %w", err)
	}
	if h.canonical, err = p.readRun(ctx, reader, execution.PlanCanonical, canonicalJob, hasCanonical, unblocks); err != nil {
		return headRuns{}, err
	}
	h.receipt, err = p.readRun(ctx, reader, execution.PlanReceipt, receiptJob, hasReceipt, unblocks)
	return h, err
}

// read reads what the page states of the pull request: its head as the synchronization last observed it (the last one recorded, for a pull request that is not open),
// the revision it carries, the job of each plan for that head, the unblocks, the silence of the test runner and the preview mode.
func (p *receiptPages) read(ctx context.Context, state owner.State, project owner.ProjectPolicy, number int, deps pageDeps) (pullView, error) {
	pulls, err := p.pulls.Pulls(ctx, project.Repo)
	if err != nil {
		return pullView{}, fmt.Errorf("read the observed pull requests: %w", err)
	}
	i := slices.IndexFunc(pulls, func(s githubsync.PullState) bool { return s.Number == number })
	if i < 0 {
		return pullView{}, errReceiptNotFound
	}
	pull := pulls[i]
	tier, _ := effectiveTier(p.tier, state, true)
	installation := checkTiers[tier]
	if installation == 0 {
		return pullView{}, fmt.Errorf("the installation type %q is not one that SuiteWard knows", tier)
	}
	v := pullView{Pull: number, Repo: commandtext.Visible(project.Repo), Head: pull.HeadSHA, ShortHead: shortSHA(pull.HeadSHA), Base: pull.BaseSHA, Open: pull.NotOpenSince.IsZero(), Tier: commandtext.TierWords(installation)}
	if tier == owner.TierCoLocated {
		v.Notice = commandtext.CoLocatedNotice
	}
	h, err := p.readHead(ctx, state, project, number, pull.HeadSHA)
	if err != nil {
		return pullView{}, err
	}
	if !h.canonical.found && !h.receipt.found {
		v.Empty = append(v.Empty, "No execution results for this pull request yet.")
	}
	if h.revised && h.expected && !h.applies {
		v.Empty = append(v.Empty, "This revision changes no file that the approved patterns match, so it has no receipt.")
	}
	if err := p.situate(&v, []pageRun{h.canonical, h.receipt}, h.applies, deps, v.Open); err != nil {
		return pullView{}, err
	}
	reader := executionReader{executions: p.executions, contents: p.contents, suites: p.suites, report: wrapRead}
	weak, err := p.weakened(ctx, reader, project.RepositoryID, number, pull.HeadSHA, h.canonical, h.receipt)
	if err != nil {
		return pullView{}, err
	}
	v.Weakened = weakenedSentence(len(weak))
	if err := p.tables(&v, h.canonical, h.receipt, weak); err != nil {
		return pullView{}, err
	}
	if v.Kinds, err = kindsOf(h.receipt); err != nil {
		return pullView{}, err
	}
	if v.Kinds.Preview, err = p.previewOf(ctx, state, project, number); err != nil {
		return pullView{}, err
	}
	if v.Evidence, err = evidenceOf(h.canonical, h.receipt); err != nil {
		return pullView{}, err
	}
	if h.revised {
		v.Report, err = reportOf(ctx, deps, project.Repo, h.current, h.revisions)
	}
	return v, err
}

// previewOf says what the page states of the preview mode of the pull request.
func (p *receiptPages) previewOf(ctx context.Context, state owner.State, project owner.ProjectPolicy, number int) (string, error) {
	mode, found, err := p.previews.PreviewMode(ctx, project.RepositoryID, number, state.EnrolledAt)
	if err != nil {
		return "", fmt.Errorf("read the preview mode: %w", err)
	}
	if !found {
		return "No preview mode is recorded for this pull request.", nil
	}
	return "Preview mode: " + commandtext.Visible(mode) + ". It is a preview: no check and no approval reads it.", nil
}

// shortSHA is the first seven characters of a commit.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// carrierProposal finds the change proposal that pull request number of the repository carries.
func carrierProposal(repositoryID int64, number int) func(context.Context, governance.Tx) (contract.Proposal, bool, error) {
	return func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
		id, found, err := tx.ChangeProposalFor(ctx, carrierID(repositoryID, number))
		if err != nil || !found {
			return contract.Proposal{}, false, err
		}
		proposal, _, err := tx.Proposal(ctx, id)
		return proposal, err == nil, err
	}
}

// weakened is the set of the weakened tests of the head: the tests that the receipt marks changed whose state in the canonical run is FAIL, MISSING or FLAKY. The
// canonical run it reads is the canonical job of the head that the receipt job's manifest governs, which is not the job the gates read once the head is approved (that one
// runs the revision's manifest), so the signal does not change when the owner approves. The set is empty, and reads as none, when either run is not complete.
func (p *receiptPages) weakened(ctx context.Context, reader executionReader, repositoryID int64, number int, head string, canonical, receipt pageRun) (map[domain.TestID]bool, error) {
	if !receipt.complete() {
		return map[domain.TestID]bool{}, nil
	}
	if receipt.job.GoverningDigest.IsZero() { // every job has a governing manifest: a job without one is a row that nothing filled
		return nil, fmt.Errorf("%w: the receipt job %d", domain.ErrNoGoverningDigest, receipt.job.ID)
	}
	job, found, err := reader.gateJob(ctx, repositoryID, number, head, execution.PlanCanonical, receipt.job.GoverningDigest)
	if err != nil {
		return nil, err
	}
	if !found {
		return map[domain.TestID]bool{}, nil
	}
	approved := canonical
	if !canonical.found || canonical.job.ID != job.ID {
		if approved, err = p.readRun(ctx, reader, execution.PlanCanonical, job, true, nil); err != nil {
			return nil, err
		}
	}
	receiptTests := make([]domain.ReceiptTest, len(receipt.tests))
	for i, t := range receipt.tests {
		receiptTests[i] = domain.ReceiptTest{Package: t.Package, Name: t.Name, Changed: t.Kind == execution.KindChanged}
	}
	canonicalTests := make([]domain.CanonicalTest, len(approved.tests))
	for i, t := range approved.tests {
		state, known := canonicalStates[t.State]
		if !known {
			return nil, fmt.Errorf("job %d stores the test state %q, which is no state of its plan", approved.job.ID, t.State)
		}
		canonicalTests[i] = domain.CanonicalTest{Package: t.Package, Name: t.Name, State: state}
	}
	tests, _, err := domain.Weakened(domain.WeakenedRun{Status: receipt.status, Governing: receipt.job.GoverningDigest}, // a set that is not known is empty: it reads as none
		domain.WeakenedRun{Status: approved.status, Governing: approved.job.GoverningDigest}, receiptTests, canonicalTests)
	if err != nil {
		return nil, err
	}
	set := make(map[domain.TestID]bool, len(tests))
	for _, t := range tests {
		set[t] = true
	}
	return set, nil
}

// weakenedSentence is the count sentence of the page's top, or nothing when no test is weakened: the sentence of the comments and the checks, and the line of the page that says the tables mark these tests.
func weakenedSentence(n int) string {
	if n == 0 {
		return ""
	}
	return commandtext.WeakenedSentence(n) + " The tables below mark these tests."
}

// modeRow says whether the summary of mode is met and, when it is not, why, in words. A summary that no text can say is an impossible state, not a word to show.
func modeRow(mode domain.Mode, summary domain.ModeSummary) (met bool, why string, err error) {
	switch summary.Verdict {
	case domain.VerdictMet:
		return true, "", nil
	case domain.VerdictNotMet:
		sentence, known := commandtext.ModeNotMetSentence(mode, summary.Reason)
		if !known { // a reason of the domain that the page has no words for
			return false, "", fmt.Errorf("the summary of the %s mode is not met for the reason %q, which no text knows", mode, summary.Reason)
		}
		return false, sentence, nil
	}
	return false, "", fmt.Errorf("the summary of the %s mode has the verdict %d, which no mode summary has", mode, summary.Verdict)
}

// kindsOf is the table of the kinds of change that the receipt meets, or nothing to judge for a receipt that is not complete or has nothing in it.
func kindsOf(receipt pageRun) (kindsSection, error) {
	if !receipt.complete() {
		return kindsSection{}, nil
	}
	counts, err := receiptCounts(receipt)
	if err != nil || counts.Empty() {
		return kindsSection{}, err
	}
	var rows [][]string
	for _, mode := range []domain.Mode{domain.ModeFix, domain.ModeFeature, domain.ModeRefactor} {
		summary, err := counts.Summarize(mode)
		if err != nil { // the three modes are modes
			return kindsSection{}, err
		}
		met, why, err := modeRow(mode, summary)
		if err != nil {
			return kindsSection{}, err
		}
		row := []string{mode.String(), "Not met", why}
		if met {
			row[1], row[2] = "Met", "-"
		}
		rows = append(rows, row)
	}
	kinds, _, err := tableOf([]string{"Kind", "Met", "Why"}, rows, unbudgeted)
	return kindsSection{Table: kinds}, err
}

// evidenceOf is the table of the evidence binding of each run: what each was made of, the governing manifest and its source, the policy levels, the profile, the test
// runner and its binary, the container program, the image digest, the times, and what a run that was tried again saw. A row that no run has a value for is not written.
func evidenceOf(canonical, receipt pageRun) (table, error) {
	runs := []pageRun{canonical, receipt}
	if !canonical.found && !receipt.found {
		return table{}, nil
	}
	reasons := map[execution.Plan]string{} // the reason of each run in words: a code that no sentence says is an impossible state
	for _, r := range runs {
		var err error
		if reasons[r.plan], err = reasonWords(r); err != nil {
			return table{}, err
		}
	}
	var rows [][]string
	add := func(label string, value func(r pageRun) string) { // an empty value is none: a run that has none shows a dash
		cells, present := []string{label}, false
		for _, r := range runs {
			text := ""
			if r.found {
				text = value(r)
			}
			present = present || text != ""
			cells = append(cells, cmp.Or(text, "-"))
		}
		if present {
			rows = append(rows, cells)
		}
	}
	ran := func(value func(r pageRun) string) func(r pageRun) string { // a run that was not run has no evidence binding
		return func(r pageRun) string {
			if !r.ran {
				return ""
			}
			return value(r)
		}
	}
	add("State", stateOf)
	if slices.ContainsFunc(runs, func(r pageRun) bool { return r.found && (r.job.Attempts > 1 || r.job.Failures > 0) }) {
		add("Attempts", func(r pageRun) string { return strconv.Itoa(r.job.Attempts) })
		add("Failures", func(r pageRun) string { return strconv.Itoa(r.job.Failures) })
	}
	add("Last reason", func(r pageRun) string { return reasons[r.plan] })
	add("Approved file list (fingerprint)", ran(func(r pageRun) string {
		return r.job.GoverningDigest.String() + " (source " + commandtext.Visible(string(r.job.GoverningSource)) + ")"
	}))
	add("Policy levels", ran(func(r pageRun) string {
		return "canonical " + r.job.CanonicalLevel.String() + ", receipt " + r.job.ReceiptLevel.String()
	}))
	add("Profile", ran(func(r pageRun) string { return commandtext.Visible(r.job.Profile) }))
	add("Test runner version", ran(func(r pageRun) string { return commandtext.Visible(r.result.ExecutorVersion) }))
	add("Test runner program fingerprint (SHA-256)", ran(func(r pageRun) string { return r.result.ExecutorSHA256.String() }))
	add("Container program", ran(func(r pageRun) string { return commandtext.Visible(r.result.Runtime) }))
	add("Go toolchain", ran(func(r pageRun) string { return commandtext.Visible(r.result.Toolchain) }))
	add("Image fingerprint (digest)", ran(func(r pageRun) string {
		if r.result.ImageDigest.IsZero() {
			return ""
		}
		return r.result.ImageDigest.String()
	}))
	add("Started", ran(func(r pageRun) string { return r.result.StartedAt.UTC().Format("2006-01-02T15:04:05Z") }))
	add("Ended", ran(func(r pageRun) string { return r.result.EndedAt.UTC().Format("2006-01-02T15:04:05Z") }))
	add("Common ancestor commit (merge base)", ran(func(r pageRun) string { return r.job.BaseSHA }))
	evidence, _, err := tableOf([]string{"What", canonicalLabel, receiptLabel}, rows, unbudgeted)
	return evidence, err
}

// stateOf is the state of a run as the evidence table says it: a job that is pending or leased is pending, as in the status of ADR 0028, and an unavailable job that no
// test runner ran was not run.
func stateOf(r pageRun) string {
	switch {
	case r.job.State == execution.StateLeased:
		return string(execution.StatePending)
	case r.job.State == execution.StateUnavailable && !r.ran:
		return "not run"
	}
	return string(r.job.State)
}

// githubWaitWords is what a job says that waits on GitHub: it has not ended, and has not run either.
const githubWaitWords = "SuiteWard is waiting for GitHub to answer."

// reasonWords says in words what a run last saw, or nothing: why an unavailable job ended; that a job which has not ended waits on GitHub (which ends nothing: its own bound
// does, and only then is the job unavailable for github-unavailable); and why the last attempt of a job that was tried again failed. A job that has not ended and has failed no
// attempt has nothing to say. A code that no sentence says is an impossible state, not a word to show.
func reasonWords(r pageRun) (string, error) {
	switch {
	case !r.found || r.job.Reason == "":
		return "", nil
	case r.job.State != execution.StateUnavailable && r.job.Reason == execution.ReasonGitHubUnavailable:
		return githubWaitWords, nil
	case r.job.State != execution.StateUnavailable && r.job.Failures == 0:
		return "", nil
	}
	sentence, known := commandtext.UnavailableSentence(string(r.job.Reason))
	if !known {
		return "", fmt.Errorf("job %d has the reason %q, which no text knows", r.job.ID, r.job.Reason)
	}
	return sentence, nil
}
