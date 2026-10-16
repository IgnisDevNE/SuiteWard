package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The reasons of the checks (docs/contracts/runtime.md, "Check job and publisher (M1.6b)") and the problem codes /status shows.
const (
	reasonBootstrapPending  = "bootstrap-pending"
	reasonUnchanged         = "unchanged"
	reasonReady             = "ready"
	reasonBootstrapReady    = "bootstrap-ready"
	reasonApprovalMissing   = "approval-missing"
	reasonNotCurrent        = "not-current"
	reasonAssessmentPending = "assessment-pending"
	reasonSourceUnavailable = "source-unavailable"
	reasonInvalidSource     = "invalid-source"
	reasonIntegrityFailed   = "integrity-failed"
	reasonForkUnsupported   = "fork-unsupported"
	reasonOwnerCommand      = "owner-command-rejected"

	problemReadFailed = "read-failed" // a GitHub read failed; the next run tries again
	problemConflict   = "conflict"    // the canonical moved under the assessment; the next run tries again
	problemFailed     = "failed"      // a store error that failed the run
)

// changeCodes are the classification codes a record keeps in Change.
var changeCodes = map[inventory.Change]string{inventory.Unchanged: "unchanged", inventory.ProtectedChange: "protected-change", inventory.ScopeReduction: "scope-reduction"}

// checkReader is the part of the GitHub reader the check job uses.
type checkReader interface {
	BranchHead(ctx context.Context, repo, branch string) (string, error)
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
}

// ownerCommands is the part of the command ledger the check job reads.
type ownerCommands interface {
	LatestOwnerCommand(ctx context.Context, repositoryID int64, pull int, ownerID int64) (commands.Entry, bool, error)
}

type checkPull struct {
	repositoryID int64
	number       int
}

// unchangedAt is the head and canonical an unchanged assessment was made for.
type unchangedAt struct {
	head      string
	canonical contract.SuiteVersionID
}

// checksJob is the github_checks job, the only writer of check states (D-CHECKS): for each open pull request of an
// enrolled, configured repository it decides the state of the SuiteWard / Contract check and publishes it through the
// check ledger, which writes the outbox message when the state changed. Every step is safe to repeat.
type checksJob struct {
	repos     []string // the configured repositories
	reader    checkReader
	owners    owner.Store
	suites    governance.UnitOfWork
	contents  governance.ContentReader
	content   governance.ContentWriter
	ledger    checks.Store
	integrity integrity.Store // the verdict of the canonical bytes
	commands  ownerCommands
	pulls     pullLister         // the synchronization store: which pull requests are open and at which head
	tier      string             // the configured INSTALLATION_TIER
	problems  *bootstrapProblems // shown in /status (github[].checks)
	logger    *slog.Logger
	now       func() time.Time

	// unchanged remembers, in memory, the heads found unchanged against a canonical: a later Suite write for another
	// pull request does not make such a head read its tree again (consent cannot change an unchanged result).
	unchanged map[checkPull]unchangedAt
	// protected remembers, in memory, whether the tree of a commit has protected files, so the default branch and the
	// heads that were looked at as bootstrap carriers are each read once (a verdict is a fact about the commit).
	protected map[headKey]bool
}

// headKey names a commit of a repository.
type headKey struct{ repo, sha string }

// bootstrapPlan is what a repository whose Suite has no canonical follows (docs/contracts/runtime.md, "github_checks
// without a canonical"), decided once per run for all its open pull requests.
type bootstrapPlan struct {
	pending bool // the default branch has protected files, or cannot be read as a contract: the branch path owns the bootstrap
	wait    bool // a GitHub read failed: the stored checks stay and a new head says assessment-pending
	carrier int  // the pull request that carries the bootstrap; 0 when none does
}

// run decides the checks of every enrolled, configured project. A GitHub read failure keeps the stored check; any other
// failure fails the run after every project and pull request was tried.
func (j *checksJob) run(ctx context.Context) error {
	enrollment, enrolled, err := j.owners.Current(ctx)
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled { // D-CHECKS: nothing is published without an owner; /status says why
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
			failures = append(failures, fmt.Errorf("checks %s: %w", project.Repo, err))
		}
	}
	return errors.Join(failures...)
}

// project decides the check of every open pull request of one project and returns the failures that fail the run.
func (j *checksJob) project(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy) error {
	problem, err := j.pullRequests(ctx, enrollment, p)
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

func (j *checksJob) pullRequests(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy) (problem string, err error) {
	pulls, err := j.pulls.Pulls(ctx, p.Repo)
	if err != nil {
		return "", fmt.Errorf("read the observed pull requests: %w", err)
	}
	suite, err := j.suite(ctx, owner.ProjectID(p.RepositoryID))
	if err != nil {
		return "", fmt.Errorf("read the suite: %w", err)
	}
	slices.SortFunc(pulls, func(a, b githubsync.PullState) int { return a.Number - b.Number })
	if suite.exists && suite.canonical == "" && slices.ContainsFunc(pulls, func(pull githubsync.PullState) bool { return pull.NotOpenSince.IsZero() }) {
		if suite.plan, problem, err = j.planBootstrap(ctx, p, suite.target, pulls); err != nil {
			return "", err
		}
	}
	var failures []error
	for _, pull := range pulls {
		if !pull.NotOpenSince.IsZero() {
			continue
		}
		pullProblem, err := j.pull(ctx, enrollment, p, suite, pull)
		if err != nil {
			if cancelled := ctx.Err(); cancelled != nil {
				return "", cancelled
			}
			failures = append(failures, fmt.Errorf("pull %d: %w", pull.Number, err))
		}
		if pullProblem != "" { // the last one is shown
			problem = pullProblem
		}
	}
	return problem, errors.Join(failures...)
}

// suiteView is what the job needs of a project's Suite: its revision, its canonical version (empty without one) and
// whether that version failed its integrity verification.
type suiteView struct {
	exists          bool // the bootstrap job created the Suite
	revision        int64
	target          contract.IntegrationTargetID
	canonical       contract.SuiteVersionID
	integrityFailed bool          // the stored bytes of the canonical failed their verification
	plan            bootstrapPlan // without a canonical: who carries the bootstrap
}

func (j *checksJob) suite(ctx context.Context, project contract.ProjectID) (suiteView, error) {
	var view suiteView
	err := j.suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		view.exists, view.target = true, state.Target
		view.revision = int64(state.Canonical.Suite().Revision())
		view.canonical, _ = state.Canonical.Suite().CurrentVersionID()
		return nil
	})
	if errors.Is(err, governance.ErrNotFound) { // no Suite yet: the bootstrap creates it
		return suiteView{}, nil
	}
	if err != nil || view.canonical == "" {
		return view, err
	}
	// A verdict applies only while its version is the current canonical; "unavailable" and "passed" change no check.
	verdict, found, err := j.integrity.Latest(ctx, project, defaultSuite)
	if err != nil {
		return suiteView{}, fmt.Errorf("read the integrity verdict: %w", err)
	}
	view.integrityFailed = found && verdict.Version == view.canonical && verdict.Verdict == integrity.Failed
	return view, nil
}

// pull decides and publishes the check of one open pull request. problem is a code for /status when the decision had
// to wait for the next run.
func (j *checksJob) pull(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy, suite suiteView, pull githubsync.PullState) (problem string, err error) {
	record, found, err := j.ledger.Current(ctx, p.RepositoryID, pull.Number)
	if err != nil {
		return "", fmt.Errorf("read the check: %w", err)
	}
	latest, hasCommand, err := j.commands.LatestOwnerCommand(ctx, p.RepositoryID, pull.Number, enrollment.Owner.AccountID)
	if err != nil {
		return "", fmt.Errorf("read the latest owner command: %w", err)
	}
	sameHead := found && record.HeadSHA == pull.HeadSHA
	tier, _ := effectiveTier(j.tier, enrollment, true)
	base := checks.Result{RepositoryID: p.RepositoryID, PullNumber: pull.Number, HeadSHA: pull.HeadSHA, CommandID: latest.CommentID}
	var (
		result checks.Result
		wait   bool
	)
	final := false // a result the owner's commands cannot change
	switch {
	case !suite.exists: // the bootstrap job creates the Suite
		result = base
		result.State, result.Reason = checks.InProgress, reasonBootstrapPending
	case suite.canonical == "": // nothing to compare the head with: the bootstrap carrier is assessed, every other pull request waits
		if result, final, problem, wait, err = j.bootstrap(ctx, p, suite, pull, base, record, sameHead, tier); err != nil {
			return "", err
		}
	case suite.integrityFailed: // the canonical cannot be trusted: no head is compared with it, and no tree is read
		result, final = base, true
		result.State, result.Reason = checks.Failure, reasonIntegrityFailed
	case isFork(pull.HeadRepo, p.Repo): // D-INTEGRATION: fork pull requests fail closed, with no assessment and no proposal
		result, final = base, true
		result.State, result.Reason = checks.InProgress, reasonForkUnsupported
	case sameHead && record.Revision == suite.revision && record.CommandID == base.CommandID: // nothing that governs this head moved
		result = record.Result
	case sameHead && record.Change == changeCodes[inventory.Unchanged] && j.unchanged[checkPull{p.RepositoryID, pull.Number}] == unchangedAt{pull.HeadSHA, suite.canonical}:
		result = base
		result.State, result.Reason, result.Change, result.Revision = checks.Success, reasonUnchanged, record.Change, suite.revision
	default:
		if result, problem, wait, err = j.assess(ctx, p, suite, pull, base, tier); err != nil {
			return "", err
		}
	}
	if wait { // keep the stored check of this head, or say that a new head is pending, so it never stays without a check
		result = base
		result.State, result.Reason = checks.InProgress, reasonAssessmentPending
		// integrity-failed and fork-unsupported come from rules that may no longer apply, so they are not carried over
		if sameHead && record.Reason != reasonIntegrityFailed && record.Reason != reasonForkUnsupported {
			result = record.Result
		}
	}
	// A pull request that waits for the bootstrap has no command to reject: only a carrier or a change has one.
	if !final && result.Reason != reasonBootstrapPending && sameHead && hasCommand && !latest.CreatedAt.Before(record.HeadSince) {
		rejected, err := j.rejected(ctx, p, latest)
		if err != nil {
			return "", err
		}
		if rejected {
			result.State, result.Reason = checks.Failure, reasonOwnerCommand
		}
	}
	// Publishing every run, also an unchanged result, lets the ledger resend a message that ended failed.
	_, _, err = j.ledger.Publish(ctx, result, j.now(), func(generation int64) (governance.OutboxMessage, error) {
		return newCheckMessage(p.Repo, result, tier, string(suite.canonical), generation)
	})
	if err != nil {
		return "", fmt.Errorf("publish the check: %w", err)
	}
	return problem, nil
}

// bootstrap decides the check of a pull request while the Suite has no canonical (docs/contracts/runtime.md,
// "github_checks without a canonical"): the carrier's head is assessed, every other pull request waits.
func (j *checksJob) bootstrap(ctx context.Context, p owner.ProjectPolicy, suite suiteView, pull githubsync.PullState, base checks.Result, record checks.Record, sameHead bool,
	tier string) (result checks.Result, final bool, problem string, wait bool, err error) {
	result = base
	switch {
	case suite.plan.pending: // the branch path owns the bootstrap, forks included
		result.State, result.Reason = checks.InProgress, reasonBootstrapPending
	case isFork(pull.HeadRepo, p.Repo):
		final = true
		result.State, result.Reason = checks.InProgress, reasonForkUnsupported
	case suite.plan.wait:
		wait = true
	case pull.Number != suite.plan.carrier:
		result.State, result.Reason = checks.InProgress, reasonBootstrapPending
	// Nothing that governs the carrier's head moved. A record of a pull request that only waited is no assessment.
	case sameHead && record.Reason != reasonBootstrapPending && record.Reason != reasonAssessmentPending && record.Revision == suite.revision && record.CommandID == base.CommandID:
		result = record.Result
	default:
		result, problem, wait, err = j.assess(ctx, p, suite, pull, base, tier)
	}
	return result, final, problem, wait, err
}

// planBootstrap decides, for the open pull requests of a repository whose Suite has no canonical, whether the branch
// path is pending (the default branch has protected files) and which pull request carries the bootstrap: the lowest
// open non-fork one that already has a bootstrap proposal, else the lowest whose head has protected files. problem is
// a code for /status.
func (j *checksJob) planBootstrap(ctx context.Context, p owner.ProjectPolicy, target contract.IntegrationTargetID, pulls []githubsync.PullState) (plan bootstrapPlan, problem string, err error) {
	branch, ok := strings.CutPrefix(string(target), targetPrefix)
	if !ok {
		return plan, "", errors.New("the suite does not target a branch")
	}
	waiting := func(err error) (bootstrapPlan, string, error) {
		if cancelled := ctx.Err(); cancelled != nil {
			return plan, "", cancelled
		}
		logReadFailure(ctx, j.logger, "github checks read failed", p.Repo, err)
		return bootstrapPlan{wait: true}, problemReadFailed, nil
	}
	head, err := j.reader.BranchHead(ctx, p.Repo, branch)
	if err != nil {
		return waiting(githubRead(err))
	}
	protected, err := j.hasProtected(ctx, p.Repo, head)
	switch {
	case err == nil:
	case errors.Is(err, github.ErrTreeTruncated): // GitHub's limit: no pull request can carry the bootstrap, and the same head stays truncated
		j.remember(headKey{p.Repo, head}, true)
		return bootstrapPlan{pending: true}, "", nil // the bootstrap job reports invalid-source in /status
	case errors.Is(err, errGitHubRead):
		return waiting(err)
	case invalidSource(err): // the default branch's contract cannot be read, so no pull request can carry the bootstrap
		return bootstrapPlan{pending: true}, "", nil // the bootstrap job reports it in /status
	default:
		return plan, "", &opaque{what: "read the default branch", cause: err}
	}
	if protected {
		return bootstrapPlan{pending: true}, "", nil
	}
	var open []githubsync.PullState // the pull requests that may carry it, lowest first
	for _, pull := range pulls {
		if pull.NotOpenSince.IsZero() && !isFork(pull.HeadRepo, p.Repo) {
			open = append(open, pull)
		}
	}
	err = j.suites.Do(ctx, owner.ProjectID(p.RepositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		id, found, err := tx.LatestBootstrapProposal(ctx)
		if err != nil || !found {
			return err
		}
		proposal, _, err := tx.Proposal(ctx, id)
		if err != nil {
			return fmt.Errorf("load the latest bootstrap proposal: %w", err)
		}
		if proposal.Current().OriginKind() != contract.OriginPull {
			return nil
		}
		for _, pull := range open {
			if proposal.Current().Carrier() == carrierID(p.RepositoryID, pull.Number) {
				plan.carrier = pull.Number
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return bootstrapPlan{}, "", &opaque{what: "find the bootstrap carrier", cause: err}
	}
	for _, pull := range open {
		if plan.carrier != 0 {
			break
		}
		protected, err := j.hasProtected(ctx, p.Repo, pull.HeadSHA)
		switch {
		case err == nil && !protected:
		case err == nil, invalidSource(err): // a head that cannot be read as a source is assessed as the carrier, which says why
			plan.carrier = pull.Number
		case errors.Is(err, errGitHubRead): // the lowest pull request must not be skipped because GitHub failed once
			return waiting(err)
		default:
			return bootstrapPlan{}, "", &opaque{what: "read a pull request head", cause: err}
		}
	}
	return plan, "", nil
}

// hasProtected reports whether the tree of a commit has protected files, reading it once per commit.
func (j *checksJob) hasProtected(ctx context.Context, repo, sha string) (bool, error) {
	key := headKey{repo, sha}
	if protected, known := j.protected[key]; known {
		return protected, nil
	}
	tree, err := j.reader.Tree(ctx, repo, sha)
	if err != nil {
		return false, githubRead(err)
	}
	inv, err := inventory.Build(ctx, &githubTree{tree: tree, files: map[string][]byte{}})
	if err != nil {
		return false, err
	}
	protected := len(inv.Manifest().Entries()) > 0
	j.remember(key, protected)
	return protected, nil
}

// remember caches the verdict of a commit's tree.
func (j *checksJob) remember(key headKey, protected bool) {
	if j.protected == nil {
		j.protected = map[headKey]bool{}
	}
	j.protected[key] = protected
}

// isFork reports whether a pull request's head repository is observed and is not repo, compared case-insensitively,
// as GitHub names repositories. "(deleted)" is one too; "" is not observed (a row from before M1.7), never a fork.
func isFork(headRepo, repo string) bool {
	return headRepo != "" && !strings.EqualFold(headRepo, repo)
}

// assess reads the head's tree and assesses it against the canonical. wait is true when that cannot be done now (a failed
// GitHub read, a canonical that moved) and problem says why; any other failure is the error.
func (j *checksJob) assess(ctx context.Context, p owner.ProjectPolicy, suite suiteView, pull githubsync.PullState, base checks.Result, tier string) (result checks.Result, problem string, wait bool, err error) {
	var (
		head      governance.HeadResult
		bootstrap governance.BootstrapHeadResult
	)
	target := commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull.Number}
	tree, err := j.reader.Tree(ctx, p.Repo, pull.HeadSHA)
	switch {
	case err != nil:
		err = githubRead(err)
	case suite.canonical == "": // the bootstrap carrier: its head becomes the first canonical only through its merge
		bootstrap, err = governance.AssessBootstrapHead(ctx, j.suites, governance.BootstrapHeadRequest{Project: owner.ProjectID(p.RepositoryID), Suite: defaultSuite,
			Carrier: carrierID(p.RepositoryID, pull.Number), Head: contract.SourceRevision(pull.HeadSHA), Tree: &githubTree{tree: tree, files: map[string][]byte{}}})
	default:
		head, err = governance.AssessHead(ctx, j.suites, j.contents, j.content, governance.HeadRequest{Project: owner.ProjectID(p.RepositoryID), Suite: defaultSuite,
			Carrier: carrierID(p.RepositoryID, pull.Number), Head: contract.SourceRevision(pull.HeadSHA), Tree: &githubTree{tree: tree, files: map[string][]byte{}},
			Revision: func(n governance.RevisionNotice) (governance.OutboxMessage, bool, error) {
				message, err := newRevisionMessage(target, n, tier)
				return message, err == nil, err
			}})
	}
	result = base
	result.Revision = suite.revision // what the result held at, for a head that cannot be assessed
	switch {
	case err == nil && suite.canonical == "":
		result, err = j.bootstrapped(bootstrap, base)
		return result, "", false, err
	case err == nil:
		result, err = j.assessed(head, base)
		return result, "", false, err
	case ctx.Err() != nil:
		return checks.Result{}, "", false, ctx.Err()
	case errors.Is(err, github.ErrTreeTruncated): // a GitHub limit, not the owner's source
		result.State, result.Reason = checks.InProgress, reasonSourceUnavailable
		return result, "", false, nil
	case invalidSource(err):
		result.State, result.Reason = checks.Failure, reasonInvalidSource
		return result, "", false, nil
	case errors.Is(err, errGitHubRead):
		logReadFailure(ctx, j.logger, "github checks read failed", p.Repo, err)
		return checks.Result{}, problemReadFailed, true, nil
	case errors.Is(err, governance.ErrCanonicalMoved): // another ErrConflict is deterministic: a retry would repeat it
		j.logger.WarnContext(ctx, "github checks retry: the canonical moved while the head was assessed", "repo", p.Repo, "pull", pull.Number)
		return checks.Result{}, problemConflict, true, nil
	}
	return checks.Result{}, "", false, &opaque{what: "assess the head", cause: err}
}

// assessed maps the assessment of a head to its check (docs/contracts/runtime.md, the state and reason table) and
// remembers an unchanged head.
func (j *checksJob) assessed(head governance.HeadResult, base checks.Result) (checks.Result, error) {
	result := base
	result.Revision = head.Revision
	key := checkPull{base.RepositoryID, base.PullNumber}
	delete(j.unchanged, key)
	code, known := changeCodes[head.Change]
	if !known {
		return checks.Result{}, fmt.Errorf("the head has the unknown classification %d", head.Change)
	}
	result.Change = code
	if head.Change == inventory.Unchanged {
		if j.unchanged == nil {
			j.unchanged = map[checkPull]unchangedAt{}
		}
		j.unchanged[key] = unchangedAt{base.HeadSHA, head.Canonical}
		result.State, result.Reason = checks.Success, reasonUnchanged
		return result, nil
	}
	if head.Reference == nil {
		return checks.Result{}, errors.New("a protected change has no proposal reference")
	}
	result.Reference = referenceText(*head.Reference)
	return readiness(result, head.Ready, head.Reason, reasonReady)
}

// bootstrapped maps the assessment of a bootstrap carrier's head to its check. A head with no protected files has no
// bootstrap to approve: the pull request waits like any other.
func (j *checksJob) bootstrapped(head governance.BootstrapHeadResult, base checks.Result) (checks.Result, error) {
	result := base
	result.Revision = head.Revision
	if head.Cancelled {
		result.State, result.Reason = checks.InProgress, reasonBootstrapPending
		return result, nil
	}
	if head.Reference == nil {
		return checks.Result{}, errors.New("a bootstrap head with protected files has no proposal reference")
	}
	result.Change = changeCodes[inventory.ProtectedChange]
	result.Reference = referenceText(*head.Reference)
	return readiness(result, head.Ready, head.Reason, reasonBootstrapReady)
}

// readiness maps the readiness of a revision to its check: ready with readyReason, or in progress until it is approved.
func readiness(result checks.Result, ready bool, reason contract.PromotionReason, readyReason string) (checks.Result, error) {
	if ready {
		result.State, result.Reason = checks.Success, readyReason
		return result, nil
	}
	switch reason {
	case contract.PromotionReasonApprovalMissing:
		result.State, result.Reason = checks.InProgress, reasonApprovalMissing
	case contract.PromotionReasonProposalNotCurrent, contract.PromotionReasonContextMismatch, contract.PromotionReasonCanonicalChanged,
		contract.PromotionReasonPolicyChanged:
		result.State, result.Reason = checks.InProgress, reasonNotCurrent
	default: // including IntegrityNotPassed and AssessmentMismatch: the binding is built from the head's own inventory, so neither can happen
		return checks.Result{}, fmt.Errorf("the head is not ready for the unexpected reason %d", reason)
	}
	return result, nil
}

// rejected reports whether the owner's command was rejected: a disposition that never reached governance, or a
// processed command whose consent result was rejected (read from its receipt).
func (j *checksJob) rejected(ctx context.Context, p owner.ProjectPolicy, command commands.Entry) (bool, error) {
	switch command.Disposition {
	case commands.Malformed, commands.InvalidCode, commands.ReplayedCode, commands.UnknownProposal, commands.EditedFirst:
		return true, nil
	case commands.Processed:
		var rejected bool
		err := j.suites.Do(ctx, owner.ProjectID(p.RepositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
			receipt, found, err := tx.Receipt(ctx, command.Operation)
			switch {
			case err != nil:
				return err
			case !found || receipt.Kind != governance.OperationConsent || receipt.Consent == nil:
				return errors.New("the processed command has no consent receipt")
			}
			rejected = receipt.Consent.Result.Outcome() == contract.ConsentRejected
			return nil
		})
		if err != nil {
			return false, fmt.Errorf("read the receipt of comment %d: %w", command.CommentID, err)
		}
		return rejected, nil
	}
	return false, nil // no-suite and the dispositions that are no command are no rejection
}
