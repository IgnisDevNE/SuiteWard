package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	// mergeBound is how long after a pull request left the open list its merge commit may still be missing, or its
	// tree unreadable, before the merge is decided without it (D-INTEGRATION).
	mergeBound = 30 * time.Minute

	problemWaiting            = "waiting"             // an undecided merge waits for GitHub: no merge commit yet, or its tree is not readable yet
	problemAssessmentConflict = "assessment-conflict" // a merge was blocked because another assessment is stored at its commit
)

// mergeReader is the part of the GitHub reader the merges job uses.
type mergeReader interface {
	Pull(ctx context.Context, repo string, number int) (github.PullDetail, error)
	ClosedPulls(ctx context.Context, repo string) ([]github.PullDetail, error)
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
}

// mergeStore is the part of the observation store the merges job uses.
type mergeStore interface {
	Repository(ctx context.Context, repo string) (githubsync.RepositoryState, bool, error)
	AwaitingResolution(ctx context.Context, repo string) ([]githubsync.PullState, error)
	RecordResolution(ctx context.Context, repo string, r githubsync.Resolution) error
	RecordClosed(ctx context.Context, repo string, closed []githubsync.ClosedPull, at time.Time) error
	Merged(ctx context.Context, repo string) ([]githubsync.MergedPull, error)
	MarkProcessed(ctx context.Context, repo string, number int) error
}

// invalidTree is the tree of a merge commit whose listing GitHub could not give as a source tree (an unknown mode, a
// bad object id): governance records it as an invalid source, which is a fact about the commit.
type invalidTree struct{ err error }

func (t invalidTree) Entries(context.Context) ([]inventory.Entry, error) { return nil, t.err }
func (t invalidTree) Read(context.Context, string) ([]byte, error)       { return nil, t.err }

// mergesJob is the github_merges job, the only writer of merge facts and the only caller of governance.PromoteMerge
// (docs/contracts/runtime.md, "Merges and integrity jobs (M1.7)"): for each enrolled, configured repository whose
// Suite has a canonical it resolves the pull requests that left the open list, decides every merge in merge order
// and stops at the first merge it cannot decide yet. Every step is safe to repeat.
type mergesJob struct {
	repos     []string // the configured repositories
	reader    mergeReader
	owners    owner.Store
	suites    governance.UnitOfWork
	content   governance.ContentWriter
	store     mergeStore
	integrity integrity.Store
	tier      string             // the configured INSTALLATION_TIER
	problems  *bootstrapProblems // shown in /status (github[].merges)
	logger    *slog.Logger
	now       func() time.Time
}

// mergeRun is what one repository's pass shares between its steps.
type mergeRun struct {
	p               owner.ProjectPolicy
	project         contract.ProjectID
	tier            string
	at              time.Time // the injected clock, at microsecond precision
	integrityFailed bool      // the stored bytes of the current canonical failed their verification
}

// run advances the merges of every enrolled, configured repository. A GitHub read failure stops that repository until
// the next run; any other failure fails the run after every repository was tried.
func (j *mergesJob) run(ctx context.Context) error {
	enrollment, enrolled, err := j.owners.Current(ctx)
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled {
		return nil
	}
	var failures []error
	for _, project := range enrollment.Projects {
		if !slices.Contains(j.repos, project.Repo) {
			continue
		}
		if err := j.project(ctx, enrollment, project); err != nil {
			if cancelled := ctx.Err(); cancelled != nil {
				return cancelled
			}
			failures = append(failures, fmt.Errorf("merges %s: %w", project.Repo, err))
		}
	}
	return errors.Join(failures...)
}

// project advances one repository and keeps its last problem for /status.
func (j *mergesJob) project(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy) error {
	problem, err := j.advance(ctx, enrollment, p)
	switch {
	case err != nil:
		j.problems.set(p.Repo, problemFailed)
	case problem != "":
		j.problems.set(p.Repo, problem)
	default:
		j.problems.clear(p.Repo)
	}
	return err
}

func (j *mergesJob) advance(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy) (problem string, err error) {
	project := owner.ProjectID(p.RepositoryID)
	canonical, err := j.canonical(ctx, project)
	if err != nil {
		return "", fmt.Errorf("read the suite: %w", err)
	}
	if canonical == "" { // no merge is decided before the baseline is canonical
		return "", nil
	}
	if _, observed, err := j.store.Repository(ctx, p.Repo); err != nil {
		return "", fmt.Errorf("read the synchronization state: %w", err)
	} else if !observed { // the closed listing needs a recorded cycle
		return "", nil
	}
	run := mergeRun{p: p, project: project, at: j.now().Truncate(time.Microsecond)}
	run.tier, _ = effectiveTier(j.tier, enrollment, true)
	verdict, found, err := j.integrity.Latest(ctx, project, defaultSuite)
	if err != nil {
		return "", fmt.Errorf("read the integrity verdict: %w", err)
	}
	run.integrityFailed = found && verdict.Version == canonical && verdict.Verdict == integrity.Failed
	if problem, err = j.closedListing(ctx, run); problem != "" || err != nil {
		return problem, err
	}
	unresolved, problem, err := j.resolve(ctx, run)
	if unresolved || err != nil { // a later merge's tree contains the earlier one: nothing is decided out of order
		return problem, err
	}
	return j.decide(ctx, run)
}

// canonical returns the current version of the project's Suite, empty when it has no Suite or no canonical.
func (j *mergesJob) canonical(ctx context.Context, project contract.ProjectID) (contract.SuiteVersionID, error) {
	var version contract.SuiteVersionID
	err := j.suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		version, _ = state.Canonical.Suite().CurrentVersionID()
		return nil
	})
	if errors.Is(err, governance.ErrNotFound) { // no Suite yet: the bootstrap creates it
		return "", nil
	}
	return version, err
}

// readFailed logs a failed GitHub read by its classification and stops the repository for this run.
func (j *mergesJob) readFailed(ctx context.Context, msg, repo string, err error) (problem string, cancelled error) {
	if cancelled := ctx.Err(); cancelled != nil { // a cancelled read is not a GitHub failure
		return "", cancelled
	}
	logReadFailure(ctx, j.logger, msg, repo, err)
	return problemReadFailed, nil
}

// closedListing records the recently closed pull requests, so a merge the open listing never showed is still seen.
func (j *mergesJob) closedListing(ctx context.Context, run mergeRun) (problem string, err error) {
	closed, err := j.reader.ClosedPulls(ctx, run.p.Repo)
	if err != nil { // an unseen earlier merge would otherwise be decided after a later one
		return j.readFailed(ctx, "github merges closed listing failed", run.p.Repo, err)
	}
	items := make([]githubsync.ClosedPull, len(closed))
	for i, c := range closed {
		items[i] = githubsync.ClosedPull{ObservedPull: githubsync.ObservedPull{Number: c.Number, HeadSHA: c.HeadSHA, HeadRef: c.HeadRef, BaseRef: c.BaseRef, BaseSHA: c.BaseSHA,
			UpdatedAt: c.UpdatedAt, HeadRepo: c.HeadRepo}, MergedAt: c.MergedAt}
	}
	if err := j.store.RecordClosed(ctx, run.p.Repo, items, run.at); err != nil {
		return "", fmt.Errorf("record the closed listing: %w", err)
	}
	return "", nil
}

// resolve asks GitHub what became of every pull request that left the open list. unresolved is true while a pull
// request that may have been merged cannot be told yet (a failed read, a merge commit still missing): the outcomes
// wait. A pull request GitHub still reports open is not one: the next cycle upserts it.
func (j *mergesJob) resolve(ctx context.Context, run mergeRun) (unresolved bool, problem string, err error) {
	pulls, err := j.store.AwaitingResolution(ctx, run.p.Repo)
	if err != nil {
		return false, "", fmt.Errorf("read the unresolved pull requests: %w", err)
	}
	for _, pull := range pulls {
		detail, err := j.reader.Pull(ctx, run.p.Repo, pull.Number)
		if err != nil {
			readProblem, cancelled := j.readFailed(ctx, "github merges pull read failed", run.p.Repo, err)
			if cancelled != nil {
				return false, "", cancelled
			}
			unresolved, problem = true, readProblem
			continue
		}
		if detail.State == "open" {
			continue
		}
		if detail.Merged && detail.MergeCommitSHA == "" && run.at.Before(pull.NotOpenSince.Add(mergeBound)) {
			unresolved = true
			if problem == "" {
				problem = problemWaiting
			}
			continue
		}
		resolution := githubsync.Resolution{Number: pull.Number, NotOpenSince: pull.NotOpenSince, Merged: detail.Merged, MergedAt: detail.MergedAt, MergeCommitSHA: detail.MergeCommitSHA,
			HeadSHA: detail.HeadSHA, HeadRef: detail.HeadRef, BaseRef: detail.BaseRef, BaseSHA: detail.BaseSHA, HeadRepo: detail.HeadRepo, ResolvedAt: run.at}
		if err := j.store.RecordResolution(ctx, run.p.Repo, resolution); err != nil && !errors.Is(err, githubsync.ErrStaleResolution) { // a stale one reopened: the next cycle sees it
			return false, "", fmt.Errorf("record the resolution of pull %d: %w", pull.Number, err)
		}
	}
	return unresolved, problem, nil
}

// decide decides every merge that has no outcome yet, in merge order, and stops at the first it cannot decide.
func (j *mergesJob) decide(ctx context.Context, run mergeRun) (problem string, err error) {
	merged, err := j.store.Merged(ctx, run.p.Repo)
	if err != nil {
		return "", fmt.Errorf("read the merged pull requests: %w", err)
	}
	shared := map[string]int{} // how many merged pull requests, decided or not, each merge commit belongs to
	for _, m := range merged {
		shared[m.MergeCommitSHA]++
	}
	for _, m := range merged {
		if m.Processed {
			continue
		}
		pullProblem, stop, err := j.decideMerge(ctx, run, m, m.MergeCommitSHA != "" && shared[m.MergeCommitSHA] > 1)
		if err != nil {
			return "", err
		}
		if stop {
			return pullProblem, nil
		}
		if pullProblem != "" { // a decided merge that needs the operator's eye; the last one is shown
			problem = pullProblem
		}
	}
	return problem, nil
}

// decideMerge decides one merge. stop is true when it cannot be decided yet, with the problem that says why.
func (j *mergesJob) decideMerge(ctx context.Context, run mergeRun, m githubsync.MergedPull, sharesCommit bool) (problem string, stop bool, err error) {
	number, resolution := m.PullState.Number, m.Resolution
	carrier := carrierID(run.p.RepositoryID, number)
	if _, recorded, err := governance.MergeOutcomeOf(ctx, j.suites, run.project, defaultSuite, carrier); err != nil {
		return "", false, fmt.Errorf("read the merge outcome of pull %d: %w", number, err)
	} else if recorded { // an earlier run decided it and could not mark it
		return "", false, j.mark(ctx, run, number)
	}
	target := commentTarget{RepositoryID: run.p.RepositoryID, Repo: run.p.Repo, Pull: number}
	request := governance.MergeRequest{Project: run.project, Suite: defaultSuite, Carrier: carrier, Source: contract.SourceRevision(resolution.MergeCommitSHA),
		Target: contract.IntegrationTargetID(targetPrefix + resolution.BaseRef), ObservedAt: resolution.NotOpenSince, RecordedAt: run.at,
		Message: func(o governance.MergeOutcome) (governance.OutboxMessage, bool, error) {
			return newMergeMessage(target, o, run.tier)
		}}
	// Without a merge commit no other refusal could name one, so that comes first.
	switch {
	case resolution.MergeCommitSHA == "":
		request.Refusal = governance.MergeReasonIntegrationUnreadable
	case isFork(resolution.HeadRepo, run.p.Repo):
		request.Refusal = governance.MergeReasonForkUnsupported
	case sharesCommit:
		request.Refusal = governance.MergeReasonSharedMergeCommit
	case run.integrityFailed:
		request.Refusal = governance.MergeReasonIntegrityFailed
	default:
		tree, err := j.integratedTree(ctx, run.p.Repo, resolution.MergeCommitSHA)
		switch {
		case err == nil:
			request.Tree = tree
		case ctx.Err() != nil:
			return "", false, ctx.Err()
		case unreadableYet(err):
			if run.at.Before(resolution.NotOpenSince.Add(mergeBound)) {
				return problemWaiting, true, nil
			}
			request.Refusal = governance.MergeReasonIntegrationUnreadable
		case errors.Is(err, errGitHubRead):
			problem, cancelled := j.readFailed(ctx, "github merges tree read failed", run.p.Repo, err)
			return problem, true, cancelled
		case errors.Is(err, inventory.ErrInvalidTree): // a fact about the commit
			request.Tree = invalidTree{err: err}
		default:
			return "", false, &opaque{what: fmt.Sprintf("read the tree of pull %d", number), cause: err}
		}
	}
	result, err := governance.PromoteMerge(ctx, j.suites, j.content, request)
	if blobUnreadableYet(err) && !run.at.Before(resolution.NotOpenSince.Add(mergeBound)) { // the bound of the tree applies to its blobs
		request.Refusal, request.Tree = governance.MergeReasonIntegrationUnreadable, nil
		result, err = governance.PromoteMerge(ctx, j.suites, j.content, request)
	}
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return "", false, ctx.Err()
	case blobUnreadableYet(err): // the bound is not reached
		return problemWaiting, true, nil
	case errors.Is(err, errGitHubRead): // a blob of the tree: the commit is not known to be invalid, so nothing is decided
		problem, cancelled := j.readFailed(ctx, "github merges blob read failed", run.p.Repo, err)
		return problem, true, cancelled
	default:
		return "", false, &opaque{what: fmt.Sprintf("decide the merge of pull %d", number), cause: err}
	}
	j.logger.InfoContext(ctx, "github merge decided", "repo", run.p.Repo, "pull", number, "kind", string(result.Kind), "reason", string(result.Reason), "duplicate", result.Duplicate)
	if result.Reason == governance.MergeReasonAssessmentConflict {
		problem = problemAssessmentConflict
	}
	return problem, false, j.mark(ctx, run, number)
}

// mark records that the merge has its outcome, which only saves the next run a read.
func (j *mergesJob) mark(ctx context.Context, run mergeRun, number int) error {
	if err := j.store.MarkProcessed(ctx, run.p.Repo, number); err != nil {
		return fmt.Errorf("mark pull %d processed: %w", number, err)
	}
	return nil
}

// integratedTree reads the tree of a merge commit and its entries now. GitHub's answer for a tree it cut is an
// invalid tree to the inventory, which governance records for good: the job handles that case before governance.
func (j *mergesJob) integratedTree(ctx context.Context, repo, sha string) (inventory.Tree, error) {
	tree, err := j.reader.Tree(ctx, repo, sha)
	if err != nil {
		return nil, githubRead(err)
	}
	cached := &githubTree{tree: tree, files: map[string][]byte{}}
	if _, err := cached.Entries(ctx); err != nil {
		return nil, err
	}
	return cached, nil
}

// blobUnreadableYet reports whether a blob of a merge commit's tree may still become readable: GitHub answered 404 or
// an answer that is not a blob. Other failures (rate limit, budget, 5xx, transport) are transient and unbounded.
func blobUnreadableYet(err error) bool {
	return errors.Is(err, errGitHubRead) && (errors.Is(err, rest.ErrNotFound) || errors.Is(err, github.ErrInvalidResponse))
}

// unreadableYet reports whether the tree of a merge commit may still appear: GitHub truncated it, or answered 404 or 422
// for a commit it has not made readable yet.
func unreadableYet(err error) bool {
	if errors.Is(err, github.ErrTreeTruncated) {
		return true
	}
	if !errors.Is(err, errGitHubRead) {
		return false
	}
	kind := readFailure(err).Kind
	return kind == githubsync.FailureNotFound || kind == githubsync.FailureUnprocessable
}
