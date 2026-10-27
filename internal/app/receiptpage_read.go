package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// What the receipt page reads. The job of each plan is the one a gate reads, through the execution reader; the rows and the evidence of a job are read here, for display
// only. Every string that the repository or an executor chose goes through commandtext.Visible before it reaches the document.

// pageRun is one run of the page: a title, the facts of its state and its evidence binding, notes, the counts it states, the mode summaries and the lists it shows.
type pageRun struct {
	Title  string
	Facts  []string
	Notes  []string
	Counts []string
	Modes  []string
	Lists  []pageList
}

// pageList is a heading and the lines under it, one for each row, shown as they are.
type pageList struct {
	Heading string
	Lines   []string
}

// receiptPageData is what the page states of one pull request.
type receiptPageData struct {
	Repo      string
	Pull      int
	Head      string
	Base      string
	Open      bool
	Tier      string
	CoLocated bool
	Preview   string   // what the page says of the preview mode
	Empty     []string // the sentences of a pull request with no results
	Runs      []pageRun
}

// page builds the page of a pull request of project, waiting for its turn until ctx ends. A page over its limit is errPageTooLarge, and nothing of it is cut.
func (p *receiptPages) page(ctx context.Context, state owner.State, project owner.ProjectPolicy, number int) ([]byte, error) {
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, err := p.read(ctx, state, project, number)
	if err != nil {
		return nil, err
	}
	page := &cappedBuffer{room: maxReportPage}
	if err := receiptTemplate.Execute(page, data); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}

// read reads what the page states of the pull request: its head as the synchronization last observed it (the last one recorded, for a pull request that is not open),
// the revision it carries, the job of each plan for that head and the preview mode.
func (p *receiptPages) read(ctx context.Context, state owner.State, project owner.ProjectPolicy, number int) (receiptPageData, error) {
	pulls, err := p.pulls.Pulls(ctx, project.Repo)
	if err != nil {
		return receiptPageData{}, fmt.Errorf("read the observed pull requests: %w", err)
	}
	i := slices.IndexFunc(pulls, func(s githubsync.PullState) bool { return s.Number == number })
	if i < 0 {
		return receiptPageData{}, errReceiptNotFound
	}
	pull := pulls[i]
	tier, _ := effectiveTier(p.tier, state, true)
	data := receiptPageData{Repo: commandtext.Visible(project.Repo), Pull: number, Head: pull.HeadSHA, Base: pull.BaseSHA, Open: pull.NotOpenSince.IsZero(), Tier: tier, CoLocated: tier == owner.TierCoLocated}
	reader := executionReader{executions: p.executions, contents: p.contents, suites: p.suites, report: wrapRead}
	view, err := reader.receiptView(ctx, project.RepositoryID, carrierProposal(project.RepositoryID, number))
	if err != nil {
		return receiptPageData{}, err
	}
	canonical, hasCanonical, err := reader.gateJob(ctx, project.RepositoryID, number, pull.HeadSHA, execution.PlanCanonical, artifact.Digest{})
	if err != nil {
		return receiptPageData{}, err
	}
	// The gates ask whether the receipt applies before they read its job, and so does the page: a revision that expects no canonical, a baseline, has no receipt.
	applies, governing, err := reader.receiptApplies(ctx, view)
	if err != nil {
		return receiptPageData{}, err
	}
	var receipt execution.Job
	var hasReceipt bool
	if applies {
		if receipt, hasReceipt, err = reader.gateJob(ctx, project.RepositoryID, number, pull.HeadSHA, execution.PlanReceipt, governing); err != nil {
			return receiptPageData{}, err
		}
	}
	if !hasCanonical && !hasReceipt {
		data.Empty = append(data.Empty, "No execution results for this pull request yet.")
	}
	if view.revised && !view.expected.IsZero() && !applies {
		data.Empty = append(data.Empty, "This revision changes no file that the approved patterns match, so it has no receipt.")
	}
	if hasCanonical {
		run, err := p.canonicalRun(ctx, reader, state, canonical)
		if err != nil {
			return receiptPageData{}, err
		}
		data.Runs = append(data.Runs, run)
	}
	if hasReceipt {
		run, err := p.receiptRun(ctx, reader, state, receipt)
		if err != nil {
			return receiptPageData{}, err
		}
		data.Runs = append(data.Runs, run)
	}
	mode, found, err := p.previews.PreviewMode(ctx, project.RepositoryID, number, state.EnrolledAt)
	if err != nil {
		return receiptPageData{}, fmt.Errorf("read the preview mode: %w", err)
	}
	data.Preview = "No preview mode is recorded for this pull request."
	if found {
		data.Preview = "Preview mode: " + commandtext.Visible(mode) + ". It is a preview: no check and no approval reads it."
	}
	return data, nil
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

// canonicalRun is the canonical run of the head: the state of each declared test, "0 tests ran" when none is declared, and the excluded tests with their reasons.
func (p *receiptPages) canonicalRun(ctx context.Context, reader executionReader, state owner.State, job execution.Job) (pageRun, error) {
	run, err := reader.canonical(ctx, job, true)
	if err != nil {
		return pageRun{}, err
	}
	out, _, err := p.runOf(ctx, state, job, "Canonical run")
	if err != nil || run.status != domain.RunComplete {
		return out, err
	}
	c := run.counts
	out.Counts = strings.Split(commandtext.ExecutionText(commandtext.CheckExecution{Tests: c.Pass + c.Fail + c.Skipped + c.Missing + c.Flaky, Passed: c.Pass, Skipped: c.Skipped, Failed: c.Fail,
		Missing: c.Missing, Flaky: c.Flaky, Excluded: c.Excluded}), "\n")
	tests, err := p.executions.Tests(ctx, job.ID)
	if err != nil {
		return pageRun{}, fmt.Errorf("read the tests of job %d: %w", job.ID, err)
	}
	out.Lists = append(out.Lists, pageList{Heading: "Declared tests", Lines: testLines(tests)})
	return out, nil
}

// receiptRun is the receipt of the head: the files of the revision that it evaluates, the paths that it does not, the state of each test, the counts, the markers and
// the mode summaries.
func (p *receiptPages) receiptRun(ctx context.Context, reader executionReader, state owner.State, job execution.Job) (pageRun, error) {
	run, err := reader.receipt(ctx, job, true)
	if err != nil {
		return pageRun{}, err
	}
	out, result, err := p.runOf(ctx, state, job, "Receipt")
	if err != nil {
		return pageRun{}, err
	}
	listed := fmt.Sprintf("Files that this revision adds or changes under the approved patterns: %d", job.ChangedTotal)
	if job.ChangedTotal > len(job.ChangedPaths) {
		listed += fmt.Sprintf(" (%d listed)", len(job.ChangedPaths))
	}
	out.Lists = append(out.Lists, pageList{Heading: listed, Lines: visibleAll(job.ChangedPaths)})
	if len(job.UnapprovedPaths) > 0 {
		out.Lists = append(out.Lists, pageList{Heading: "Not evaluated: pattern not approved yet", Lines: visibleAll(job.UnapprovedPaths)})
	}
	if run.status != domain.RunComplete {
		return out, nil
	}
	out.Counts = commandtext.ReceiptFacts(run.counts, run.markers)
	if out.Modes, err = modeLines(run.counts); err != nil {
		return pageRun{}, err
	}
	if run.counts.Empty() {
		out.Notes = append(out.Notes, "This receipt has nothing to judge, so an approve needs no mode.")
	}
	if len(result.Submodules) > 0 {
		out.Lists = append(out.Lists, pageList{Heading: "Skipped submodules", Lines: visibleAll(result.Submodules)})
	}
	if len(result.Unparsed) > 0 {
		out.Lists = append(out.Lists, pageList{Heading: "Changed test files that do not parse", Lines: visibleAll(result.Unparsed)})
	}
	if len(result.Markers) > 0 {
		lines := make([]string, len(result.Markers))
		for i, m := range result.Markers {
			lines[i] = fmt.Sprintf("%s: %s line %d", commandtext.Visible(m.Kind), commandtext.Visible(m.Path), m.Line)
		}
		out.Lists = append(out.Lists, pageList{Heading: "Marker lines", Lines: lines})
	}
	tests, err := p.executions.Tests(ctx, job.ID)
	if err != nil {
		return pageRun{}, fmt.Errorf("read the tests of job %d: %w", job.ID, err)
	}
	out.Lists = append(out.Lists, pageList{Heading: "Evaluated tests", Lines: testLines(tests)})
	return out, nil
}

// runOf states what every run has: its state, the attempts that were made, what ended it, the evidence binding of an executor's result, and the owner's unblock. A job
// that has no result of an executor (it ended on the GitHub bound, or was created already final) has no evidence binding: it was not run.
func (p *receiptPages) runOf(ctx context.Context, state owner.State, job execution.Job, title string) (pageRun, execution.StoredResult, error) {
	result, stored, err := p.executions.Result(ctx, job.ID)
	if err != nil {
		return pageRun{}, execution.StoredResult{}, fmt.Errorf("read the result of job %d: %w", job.ID, err)
	}
	unblocked, err := p.executions.Unblocked(ctx, job.ID, state.EnrolledAt)
	if err != nil {
		return pageRun{}, execution.StoredResult{}, fmt.Errorf("read the unblock of job %d: %w", job.ID, err)
	}
	run := pageRun{Title: title, Facts: []string{"State: " + stateOf(job)}}
	if job.Attempts > 1 || job.Failures > 0 {
		run.Facts = append(run.Facts, "Attempts: "+strconv.Itoa(job.Attempts), "Failures: "+strconv.Itoa(job.Failures))
		if job.Reason != "" {
			run.Facts = append(run.Facts, "Last reason: "+commandtext.Visible(string(job.Reason)))
		}
	}
	ran := stored && result.Origin == execution.OriginExecutor
	if ran {
		run.Facts = append(run.Facts, evidenceLines(job, result)...)
	}
	if job.State == execution.StateUnavailable {
		note, err := unavailableNote(job.Reason, ran)
		if err != nil {
			return pageRun{}, execution.StoredResult{}, err
		}
		run.Notes = append(run.Notes, note)
	}
	if unblocked {
		run.Notes = append(run.Notes, "The owner unblocked this run: it was unavailable, so the tests did not run.")
	}
	return run, result, nil
}

// stateOf is the state of a run: a job that is pending or leased is pending, as in the status of ADR 0028.
func stateOf(job execution.Job) string {
	if job.State == execution.StateLeased {
		return string(execution.StatePending)
	}
	return string(job.State)
}

// unavailableNote says why a run is unavailable, in words and by its code: "Not run" for a job that no executor ran.
func unavailableNote(reason execution.Reason, ran bool) (string, error) {
	lead := "Not run: "
	if ran {
		lead = "Unavailable: "
	}
	code := commandtext.Visible(string(reason))
	if words, known := commandtext.UnavailableReason(string(reason)); known {
		return lead + words + " (" + code + ").", nil
	}
	return lead + code + ".", nil
}

// evidenceLines are the evidence binding of a run: the manifest that governed it and where it came from, the levels of the policy, the profile, the executor and its
// binary, the runtime, the toolchain, the image and the times. What an executor said is shown escaped.
func evidenceLines(job execution.Job, r execution.StoredResult) []string {
	lines := []string{
		"Governing manifest: " + job.GoverningDigest.String() + " (source " + commandtext.Visible(string(job.GoverningSource)) + ")",
		"Policy levels: canonical " + job.CanonicalLevel.String() + ", receipt " + job.ReceiptLevel.String(),
		"Profile: " + commandtext.Visible(job.Profile),
		"Executor: " + commandtext.Visible(r.ExecutorVersion),
		"Executor binary SHA-256: " + r.ExecutorSHA256.String(),
	}
	if r.Runtime != "" {
		lines = append(lines, "Runtime: "+commandtext.Visible(r.Runtime))
	}
	if r.Toolchain != "" {
		lines = append(lines, "Toolchain: "+commandtext.Visible(r.Toolchain))
	}
	if !r.ImageDigest.IsZero() {
		lines = append(lines, "Image digest: "+r.ImageDigest.String())
	}
	lines = append(lines, "Started: "+r.StartedAt.UTC().Format("2006-01-02T15:04:05Z"), "Ended: "+r.EndedAt.UTC().Format("2006-01-02T15:04:05Z"))
	if job.BaseSHA != "" {
		lines = append(lines, "Merge base: "+job.BaseSHA)
	}
	return lines
}

// modeLines say, for each of the three modes, whether the summary of the receipt meets it and, when it does not, why.
func modeLines(counts domain.ReceiptCounts) ([]string, error) {
	var lines []string
	for _, mode := range []domain.Mode{domain.ModeFix, domain.ModeFeature, domain.ModeRefactor} {
		summary, err := counts.Summarize(mode)
		if err != nil { // the three modes are modes
			return nil, err
		}
		line, err := modeLine(mode, summary)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// modeLine says whether one summary is met and, when it is not, why.
func modeLine(mode domain.Mode, summary domain.ModeSummary) (string, error) {
	switch summary.Verdict {
	case domain.VerdictMet:
		return mode.String() + ": met", nil
	default:
		words, _ := commandtext.ModeReason(summary.Reason)
		return mode.String() + ": not met: " + words + " (" + string(summary.Reason) + ")", nil
	}
}

// testLines show each test as its state, its package, its name and its file, and for an excluded one its reason.
func testLines(tests []execution.Test) []string {
	lines := make([]string, len(tests))
	for i, t := range tests {
		lines[i] = strings.ToUpper(strings.ReplaceAll(t.State, "_", " ")) + "  " + commandtext.Visible(t.Package) + "  " + commandtext.Visible(t.Name) + "  " + commandtext.Visible(t.File)
		if t.ExcludedReason != "" {
			lines[i] += " (" + commandtext.Visible(t.ExcludedReason) + ")"
		}
	}
	return lines
}

// visibleAll shows each path with its hidden characters written as escapes.
func visibleAll(paths []string) []string {
	lines := make([]string, len(paths))
	for i, path := range paths {
		lines[i] = commandtext.Visible(path)
	}
	return lines
}
