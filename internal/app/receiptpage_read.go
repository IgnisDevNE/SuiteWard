package app

import (
	"context"
	"fmt"
	"html/template"
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
	reports  reportSource
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

// page builds the page of a pull request of project, waiting for its turn until ctx ends. A page over its limit is errPageTooLarge, and nothing of it is cut.
func (p *receiptPages) page(ctx context.Context, state owner.State, project owner.ProjectPolicy, number int, deps pageDeps) ([]byte, error) {
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
	v := pullView{Pull: number, Repo: commandtext.Visible(project.Repo), Head: pull.HeadSHA, ShortHead: shortSHA(pull.HeadSHA), Base: pull.BaseSHA, Open: pull.NotOpenSince.IsZero(), Tier: tier}
	if tier == owner.TierCoLocated {
		v.Notice = commandtext.CoLocatedNotice
	}
	reader := executionReader{executions: p.executions, contents: p.contents, suites: p.suites, report: wrapRead}
	var revisions []contract.ProposalReference // every revision of the proposal that the pull request carries, oldest first
	var current contract.ProposalReference
	carrier := carrierProposal(project.RepositoryID, number)
	rview, err := reader.receiptView(ctx, project.RepositoryID, func(ctx context.Context, tx governance.Tx) (contract.Proposal, bool, error) {
		proposal, found, err := carrier(ctx, tx)
		if err == nil && found {
			current = proposal.Current().Binding().Reference()
			for _, revision := range proposal.Revisions() {
				revisions = append(revisions, revision.Binding().Reference())
			}
		}
		return proposal, found, err
	})
	if err != nil {
		return pullView{}, err
	}
	canonicalJob, hasCanonical, err := reader.gateJob(ctx, project.RepositoryID, number, pull.HeadSHA, execution.PlanCanonical, artifact.Digest{})
	if err != nil {
		return pullView{}, err
	}
	// The gates ask whether the receipt applies before they read its job, and so does the page: a revision that expects no canonical, a baseline, has no receipt.
	applies, governing, err := reader.receiptApplies(ctx, rview)
	if err != nil {
		return pullView{}, err
	}
	var receiptJob execution.Job
	var hasReceipt bool
	if applies {
		if receiptJob, hasReceipt, err = reader.gateJob(ctx, project.RepositoryID, number, pull.HeadSHA, execution.PlanReceipt, governing); err != nil {
			return pullView{}, err
		}
	}
	unblocks, err := p.executions.HeadUnblocks(ctx, project.RepositoryID, number, pull.HeadSHA, state.EnrolledAt)
	if err != nil {
		return pullView{}, fmt.Errorf("read the unblocks of the head: %w", err)
	}
	canonical, err := p.readRun(ctx, reader, execution.PlanCanonical, canonicalJob, hasCanonical, unblocks)
	if err != nil {
		return pullView{}, err
	}
	receipt, err := p.readRun(ctx, reader, execution.PlanReceipt, receiptJob, hasReceipt, unblocks)
	if err != nil {
		return pullView{}, err
	}
	if !canonical.found && !receipt.found {
		v.Empty = append(v.Empty, "No execution results for this pull request yet.")
	}
	if rview.revised && !rview.expected.IsZero() && !applies {
		v.Empty = append(v.Empty, "This revision changes no file that the approved patterns match, so it has no receipt.")
	}
	runs := []pageRun{canonical, receipt}
	if err := p.situate(&v, runs, applies, deps, pull.NotOpenSince.IsZero()); err != nil {
		return pullView{}, err
	}
	weak, err := p.weakened(ctx, reader, project.RepositoryID, number, pull.HeadSHA, canonical, receipt)
	if err != nil {
		return pullView{}, err
	}
	v.Weakened = weakenedSentence(len(weak))
	if err := p.tables(&v, canonical, receipt, weak); err != nil {
		return pullView{}, err
	}
	if v.Kinds, err = kindsOf(receipt); err != nil {
		return pullView{}, err
	}
	mode, found, err := p.previews.PreviewMode(ctx, project.RepositoryID, number, state.EnrolledAt)
	if err != nil {
		return pullView{}, fmt.Errorf("read the preview mode: %w", err)
	}
	v.Kinds.Preview = "No preview mode is recorded for this pull request."
	if found {
		v.Kinds.Preview = "Preview mode: " + commandtext.Visible(mode) + ". It is a preview: no check and no approval reads it."
	}
	if v.Evidence, err = evidenceOf(canonical, receipt); err != nil {
		return pullView{}, err
	}
	if rview.revised {
		if v.Report, err = reportOf(ctx, deps, project.Repo, current, revisions); err != nil {
			return pullView{}, err
		}
	}
	return v, nil
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
		return nil, nil
	}
	if receipt.job.GoverningDigest.IsZero() { // every job has a governing manifest: a job without one is a row that nothing filled
		return nil, fmt.Errorf("%w: the receipt job %d", domain.ErrNoGoverningDigest, receipt.job.ID)
	}
	job, found, err := reader.gateJob(ctx, repositoryID, number, head, execution.PlanCanonical, receipt.job.GoverningDigest)
	if err != nil || !found {
		return nil, err
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
	tests, known, err := domain.Weakened(domain.WeakenedRun{Status: receipt.status, Governing: receipt.job.GoverningDigest},
		domain.WeakenedRun{Status: approved.status, Governing: approved.job.GoverningDigest}, receiptTests, canonicalTests)
	if err != nil || !known {
		return nil, err
	}
	set := make(map[domain.TestID]bool, len(tests))
	for _, t := range tests {
		set[t] = true
	}
	return set, nil
}

// weakenedSentence is the count sentence of the page's top, or nothing when no test is weakened.
func weakenedSentence(n int) string {
	const tail = " A test that is edited can pass by checking less. Read the edit before you approve. The tables below mark these tests."
	switch n {
	case 0:
		return ""
	case 1:
		return "1 approved test that this change edits fails, is missing or is flaky on this commit, in the version that was approved." + tail
	}
	return strconv.Itoa(n) + " approved tests that this change edits fail, are missing or are flaky on this commit, in the version that was approved." + tail
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
	table, _, err := tableOf([]string{"Kind", "Met", "Why"}, rows, unbudgeted)
	return kindsSection{Table: table}, err
}

// evidenceOf is the table of the evidence binding of each run: what each was made of, the governing manifest and its source, the policy levels, the profile, the test
// runner and its binary, the container program, the image digest, the times, and what a run that was tried again saw. A row that no run has a value for is not written.
func evidenceOf(canonical, receipt pageRun) (template.HTML, error) {
	runs := []pageRun{canonical, receipt}
	if !canonical.found && !receipt.found {
		return "", nil
	}
	var rows [][]string
	add := func(label string, value func(r pageRun) (string, bool)) {
		cells, any := []string{label}, false
		for _, r := range runs {
			text, ok := "-", false
			if r.found {
				text, ok = value(r)
				if !ok {
					text = "-"
				}
			}
			any = any || ok
			cells = append(cells, text)
		}
		if any {
			rows = append(rows, cells)
		}
	}
	triedAgain := slices.ContainsFunc(runs, func(r pageRun) bool { return r.found && (r.job.Attempts > 1 || r.job.Failures > 0) })
	add("State", func(r pageRun) (string, bool) { return stateOf(r), true })
	if triedAgain {
		add("Attempts", func(r pageRun) (string, bool) { return strconv.Itoa(r.job.Attempts), true })
		add("Failures", func(r pageRun) (string, bool) { return strconv.Itoa(r.job.Failures), true })
	}
	add("Last reason", func(r pageRun) (string, bool) {
		return commandtext.Visible(string(r.job.Reason)), r.job.Reason != ""
	})
	evidence := func(value func(r pageRun) string) func(r pageRun) (string, bool) { // a run that was not run has no evidence binding
		return func(r pageRun) (string, bool) {
			text := ""
			if r.ran {
				text = value(r)
			}
			return text, text != ""
		}
	}
	add("Governing manifest", evidence(func(r pageRun) string {
		return r.job.GoverningDigest.String() + " (source " + commandtext.Visible(string(r.job.GoverningSource)) + ")"
	}))
	add("Policy levels", evidence(func(r pageRun) string {
		return "canonical " + r.job.CanonicalLevel.String() + ", receipt " + r.job.ReceiptLevel.String()
	}))
	add("Profile", evidence(func(r pageRun) string { return commandtext.Visible(r.job.Profile) }))
	add("Test runner version", evidence(func(r pageRun) string { return commandtext.Visible(r.result.ExecutorVersion) }))
	add("Test runner binary SHA-256", evidence(func(r pageRun) string { return r.result.ExecutorSHA256.String() }))
	add("Container program", evidence(func(r pageRun) string { return commandtext.Visible(r.result.Runtime) }))
	add("Go toolchain", evidence(func(r pageRun) string { return commandtext.Visible(r.result.Toolchain) }))
	add("Image digest", evidence(func(r pageRun) string {
		if r.result.ImageDigest.IsZero() {
			return ""
		}
		return r.result.ImageDigest.String()
	}))
	add("Started", evidence(func(r pageRun) string { return r.result.StartedAt.UTC().Format("2006-01-02T15:04:05Z") }))
	add("Ended", evidence(func(r pageRun) string { return r.result.EndedAt.UTC().Format("2006-01-02T15:04:05Z") }))
	add("Merge base", evidence(func(r pageRun) string { return r.job.BaseSHA }))
	table, _, err := tableOf([]string{"What", canonicalLabel, receiptLabel}, rows, unbudgeted)
	return table, err
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
