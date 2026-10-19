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
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The reasons of the checks (docs/contracts/runtime.md, "Check job and publisher (M1.6b)") and the problem codes /status shows.
const (
	reasonBootstrapAwaiting = "bootstrap-awaiting"
	reasonBootstrapSkipped  = "bootstrap-skipped"
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
	reasonRunnerUncovered   = "runner-uncovered"

	problemReadFailed = "read-failed" // a GitHub read failed; the next run tries again
	problemConflict   = "conflict"    // the canonical moved under the assessment; the next run tries again
	problemFailed     = "failed"      // a store error that failed the run
)

// changeCodes are the classification codes a record keeps in Change.
var changeCodes = map[inventory.Change]string{inventory.Unchanged: "unchanged", inventory.ProtectedChange: "protected-change", inventory.ScopeReduction: "scope-reduction"}

// checkReader is the part of the GitHub reader the check job uses.
type checkReader interface {
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
	PullFiles(ctx context.Context, repo string, number int) ([]github.PullFile, error)
}

// ownerCommands is the part of the command ledger the check job reads.
type ownerCommands interface {
	LatestOwnerCommand(ctx context.Context, repositoryID int64, pull int, ownerID, before int64) (commands.Entry, bool, error)
	Skipped(ctx context.Context, repositoryID int64, pull int, since time.Time) (bool, error)
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
	// scoped remembers, in memory, the head and canonical a pull request's scope notice was last decided for, so a head is read for
	// it once: a restart costs one more decision for each open pull request, and the outbox key stops a second notice.
	scoped map[checkPull]unchangedAt
	// announced remembers, in memory, the keys of the bootstrap comments written or found written, so a pull request that waits
	// does not try its comment again every run. The outbox key is the idempotency key, so a restart costs one refused insert each.
	// It also holds the sets of uncovered paths that the scope notice decided (scope-set:...).
	announced map[string]bool
	// runners remembers, in memory, the runner files found for the head and canonical of a pull request, so a head is read for them once (docs/contracts/runtime.md, "Check reason runner-uncovered").
	runners map[checkPull]*runnerSet
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
	suite, err := j.suite(ctx, p)
	if err != nil {
		return "", fmt.Errorf("read the suite: %w", err)
	}
	slices.SortFunc(pulls, func(a, b githubsync.PullState) int { return a.Number - b.Number })
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

// suiteView is what the job needs of a project's Suite: its revision, its canonical version (empty without one),
// whether that version failed its integrity verification and, without a canonical, who carries the bootstrap.
type suiteView struct {
	exists          bool // the bootstrap job created the Suite
	revision        int64
	canonical       contract.SuiteVersionID
	integrityFailed bool             // the stored bytes of the canonical failed their verification
	carrier         bootstrapCarrier // without a canonical: the Suite's latest bootstrap proposal
}

// bootstrapCarrier is the Suite's latest bootstrap proposal and the pull request that carries it; found is false when no
// proposal exists (docs/contracts/runtime.md, "github_checks before V1").
type bootstrapCarrier struct {
	found     bool
	pull      int
	branch    bool                       // a branch proposal, which the bootstrap job promotes; otherwise a pull proposal, which the merge of its carrier promotes
	reference contract.ProposalReference // the current revision
}

func (j *checksJob) suite(ctx context.Context, p owner.ProjectPolicy) (suiteView, error) {
	project := owner.ProjectID(p.RepositoryID)
	var view suiteView
	err := j.suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		view.exists = true
		view.revision = int64(state.Canonical.Suite().Revision())
		view.canonical, _ = state.Canonical.Suite().CurrentVersionID()
		if view.canonical != "" {
			return nil
		}
		id, found, err := tx.LatestBootstrapProposal(ctx)
		if err != nil || !found {
			return err
		}
		proposal, _, err := tx.Proposal(ctx, id)
		if err != nil {
			return fmt.Errorf("load the latest bootstrap proposal: %w", err)
		}
		current := proposal.Current()
		pull, err := pullOfCarrier(p.RepositoryID, current.Carrier())
		if err != nil {
			return err
		}
		view.carrier = bootstrapCarrier{found: true, pull: pull, branch: current.OriginKind() == contract.OriginBranch, reference: current.Binding().Reference()}
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

// keptReasons are the reasons of a stored check that a failed read keeps. Not kept: the reasons of rules that may no longer apply
// (integrity-failed, a fork, a skip, a carrier that was none), and any reason the renderer no longer knows, such as the
// bootstrap-pending of an installation from before the M1.7b rework.
var keptReasons = []string{reasonUnchanged, reasonReady, reasonBootstrapReady, reasonApprovalMissing, reasonNotCurrent, reasonAssessmentPending,
	reasonSourceUnavailable, reasonInvalidSource, reasonOwnerCommand, reasonRunnerUncovered}

// pull decides and publishes the check of one open pull request. problem is a code for /status when the decision had
// to wait for the next run.
func (j *checksJob) pull(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy, suite suiteView, pull githubsync.PullState) (problem string, err error) {
	record, found, err := j.ledger.Current(ctx, p.RepositoryID, pull.Number)
	if err != nil {
		return "", fmt.Errorf("read the check: %w", err)
	}
	latest, hasCommand, err := j.commands.LatestOwnerCommand(ctx, p.RepositoryID, pull.Number, enrollment.Owner.AccountID, 0)
	if err != nil {
		return "", fmt.Errorf("read the latest owner command: %w", err)
	}
	sameHead := found && record.HeadSHA == pull.HeadSHA
	tier, _ := effectiveTier(j.tier, enrollment, true)
	base := checks.Result{RepositoryID: p.RepositoryID, PullNumber: pull.Number, HeadSHA: pull.HeadSHA, CommandID: latest.CommentID}
	var (
		result checks.Result
		wait   bool
		source sourceProblem // what made the head invalid, when this run assessed it
	)
	final := false // a result the owner's commands cannot change
	switch {
	case !suite.exists: // the bootstrap job creates the Suite
		result = base
		result.State, result.Reason = checks.InProgress, reasonBootstrapAwaiting
	case suite.canonical == "": // nothing to compare the head with: only the bootstrap carrier is assessed
		if result, final, problem, wait, source, err = j.bootstrap(ctx, enrollment, p, suite, pull, base, record, sameHead, tier); err != nil {
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
		if result, problem, wait, source, err = j.assess(ctx, p, suite, pull, base, tier); err != nil {
			return "", err
		}
	}
	if wait { // keep the stored check of this head, or say that a new head is pending, so it never stays without a check
		result = base
		result.State, result.Reason = checks.InProgress, reasonAssessmentPending
		if sameHead && slices.Contains(keptReasons, record.Reason) {
			result = record.Result
		}
	}
	// A wrong code is a security signal whatever the state, so it fails an awaiting or a skipped pull request too; a fork and an integrity failure are final.
	if !final && sameHead && hasCommand {
		wrongCode, err := j.wrongCode(ctx, p, enrollment.Owner.AccountID, pull.Number, latest, record.HeadSince)
		if err != nil {
			return "", err
		}
		if wrongCode {
			result.State, result.Reason = checks.Failure, reasonOwnerCommand
		}
	}
	// A runner file outside the head's scope blocks a success (docs/contracts/runtime.md, "Check reason runner-uncovered"). It comes after the other reasons,
	// so a rejected command, an approval that is missing and every failure keep precedence. A head whose runner files cannot be read is never success.
	var runners *runnerSet
	if suite.canonical != "" && (result.State == checks.Success || result.Reason == reasonRunnerUncovered) {
		var runnerWait bool
		if runners, runnerWait, err = j.runnerFiles(ctx, p, pull, suite.canonical); err != nil {
			return "", err
		}
		switch {
		case runnerWait:
			result = base
			result.State, result.Reason = checks.InProgress, reasonAssessmentPending
			problem = problemReadFailed
		case len(runners.paths) > 0:
			result.State, result.Reason = checks.InProgress, reasonRunnerUncovered
		case result.Reason == reasonRunnerUncovered: // the stored check cannot say which success it replaced: assess again
			result = base
			result.State, result.Reason = checks.InProgress, reasonAssessmentPending
		}
	}
	// The comment comes before the check, so a run that fails between the two writes it again, and the check never goes without it.
	// It does not depend on the stored check: a pull request that waited before the Suite existed has the same reason and reference.
	if suite.exists && result.Reason == reasonBootstrapAwaiting {
		if err := j.announce(ctx, p, pull.Number, result.Reference); err != nil {
			return "", err
		}
	}
	// Like the bootstrap comment, before the check: a run that fails between the two writes it again. Only the check that says invalid-source
	// gets it, and only when this run assessed the head: an earlier run wrote it for the same head.
	if source.kind != "" && result.Reason == reasonInvalidSource {
		if err := j.explainInvalidSource(ctx, p, pull.Number, pull.HeadRef, source); err != nil {
			return "", err
		}
	}
	// Publishing every run, also an unchanged result, lets the ledger resend a message that ended failed.
	_, _, err = j.ledger.Publish(ctx, result, j.now(), func(generation int64) (governance.OutboxMessage, error) {
		if result.Reason == reasonRunnerUncovered { // the block above decided it from a runner set, so a republish has the files too
			if runners == nil {
				return governance.OutboxMessage{}, errors.New("a runner-uncovered check has no runner set")
			}
			return newCheckMessageWithPaths(p.Repo, result, tier, string(suite.canonical), generation, runners.paths, len(runners.paths))
		}
		return newCheckMessage(p.Repo, result, tier, string(suite.canonical), generation)
	})
	if err != nil {
		return "", fmt.Errorf("publish the check: %w", err)
	}
	// After the check, so a failure here never delays or changes it: the comment is evidence and decides nothing.
	if runners != nil && len(runners.paths) > 0 && result.Reason == reasonRunnerUncovered && !runners.written {
		if err := j.explainRunners(ctx, p, pull, runners); err != nil {
			return "", err
		}
		runners.written = true
	}
	// After the check, so a failure here never delays or changes it: the notice is evidence and decides nothing.
	if suite.canonical != "" && !suite.integrityFailed && !isFork(pull.HeadRepo, p.Repo) {
		if err := j.scopeNotice(ctx, p, pull, suite.canonical); err != nil {
			return "", err
		}
	}
	return problem, nil
}

// bootstrap decides the check of a pull request while the Suite has no canonical (docs/contracts/runtime.md, "github_checks
// before V1"), in the contract's order: a fork, the carrier of the latest bootstrap proposal, a skipped pull request, and any
// other pull request, which waits.
func (j *checksJob) bootstrap(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy, suite suiteView, pull githubsync.PullState, base checks.Result, record checks.Record,
	sameHead bool, tier string) (result checks.Result, final bool, problem string, wait bool, source sourceProblem, err error) {
	result = base
	carrier := suite.carrier
	reference := referenceText(carrier.reference)
	switch {
	case isFork(pull.HeadRepo, p.Repo):
		final = true
		result.State, result.Reason = checks.InProgress, reasonForkUnsupported
	case carrier.found && carrier.pull == pull.Number && carrier.branch: // the bootstrap job promotes V1 after the owner approved it
		result.State, result.Reason, result.Revision, result.Reference = checks.InProgress, reasonApprovalMissing, suite.revision, reference
	// Nothing that governs the carrier's head moved. A record is an assessment of the carrier only when it names the
	// proposal revision: a record that only waited names none.
	case carrier.found && carrier.pull == pull.Number && sameHead && record.Reference == reference && record.Revision == suite.revision && record.CommandID == base.CommandID:
		result = record.Result
	case carrier.found && carrier.pull == pull.Number:
		result, problem, wait, source, err = j.assess(ctx, p, suite, pull, base, tier)
	default:
		var skipped bool // read every run: the fast path above keys on the latest owner command, which ignores a skip
		if skipped, err = j.commands.Skipped(ctx, p.RepositoryID, pull.Number, enrollment.EnrolledAt); err != nil {
			return result, false, "", false, sourceProblem{}, fmt.Errorf("read the skip of the pull request: %w", err)
		}
		result.State, result.Reason = checks.InProgress, reasonBootstrapAwaiting
		if skipped {
			result.State, result.Reason = checks.Success, reasonBootstrapSkipped
		}
	}
	return result, final, problem, wait, source, err
}

// announce writes the comment that goes with a bootstrap-awaiting check, once: the notice (no reference) for the pull request,
// or the cancelled comment for the proposal revision the check names. Its key is the idempotency key, so a key that exists was
// written before and is not an error.
func (j *checksJob) announce(ctx context.Context, p owner.ProjectPolicy, pull int, reference string) error {
	target := commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull}
	var message governance.OutboxMessage
	var err error
	if reference != "" {
		message, err = newCancelledMessage(target, reference)
	} else {
		message, err = newNoticeMessage(target)
	}
	if err != nil {
		return err
	}
	if j.announced[message.Key] {
		return nil
	}
	err = j.suites.Do(ctx, owner.ProjectID(p.RepositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error { return tx.Outbox(ctx, message) })
	if err != nil && !errors.Is(err, governance.ErrOperationConflict) {
		return fmt.Errorf("write the comment %s: %w", message.Key, err)
	}
	if j.announced == nil {
		j.announced = map[string]bool{}
	}
	j.announced[message.Key] = true
	return nil
}

// isFork reports whether a pull request's head repository is observed and is not repo, compared case-insensitively,
// as GitHub names repositories. "(deleted)" is one too; "" is not observed (a row from before M1.7), never a fork.
func isFork(headRepo, repo string) bool {
	return headRepo != "" && !strings.EqualFold(headRepo, repo)
}

// assess reads the head's tree and assesses it against the canonical. wait is true when that cannot be done now (a failed
// GitHub read, a canonical that moved) and problem says why; any other failure is the error. source says what made an invalid head
// invalid, when it can be named.
func (j *checksJob) assess(ctx context.Context, p owner.ProjectPolicy, suite suiteView, pull githubsync.PullState, base checks.Result, tier string) (result checks.Result, problem string, wait bool, source sourceProblem, err error) {
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
	if suite.canonical == "" {
		result.Reference = referenceText(suite.carrier.reference) // the carrier's results name its proposal revision, which is what its fast path keys on
	}
	switch {
	case err == nil && suite.canonical == "":
		result, err = j.bootstrapped(bootstrap, result)
		return result, "", false, sourceProblem{}, err
	case err == nil:
		result, err = j.assessed(head, base)
		return result, "", false, sourceProblem{}, err
	case ctx.Err() != nil:
		return checks.Result{}, "", false, sourceProblem{}, ctx.Err()
	case errors.Is(err, github.ErrTreeTruncated): // a GitHub limit, not the owner's source
		result.State, result.Reason = checks.InProgress, reasonSourceUnavailable
		return result, "", false, sourceProblem{}, nil
	case invalidSource(err):
		result.State, result.Reason = checks.Failure, reasonInvalidSource
		var named bool
		if source, named = sourceProblemOf(err); !named { // the check is the same, only the comment is left out
			j.logger.InfoContext(ctx, "github checks invalid-source comment skipped: the problem cannot be named", "repo", p.Repo, "pull", pull.Number)
		}
		if source.kind == sourceLink {
			if source.declaration, source.noDeclaration, err = j.acceptingDeclaration(ctx, p.Repo, tree, source.path); err != nil {
				return checks.Result{}, "", false, sourceProblem{}, err
			}
		}
		return result, "", false, source, nil
	case errors.Is(err, errGitHubRead):
		logReadFailure(ctx, j.logger, "github checks read failed", p.Repo, err)
		return checks.Result{}, problemReadFailed, true, sourceProblem{}, nil
	case errors.Is(err, governance.ErrCanonicalMoved): // another ErrConflict is deterministic: a retry would repeat it
		j.logger.WarnContext(ctx, "github checks retry: the canonical moved while the head was assessed", "repo", p.Repo, "pull", pull.Number)
		return checks.Result{}, problemConflict, true, sourceProblem{}, nil
	}
	return checks.Result{}, "", false, sourceProblem{}, &opaque{what: "assess the head", cause: err}
}

// sourceProblem is what made the source of a head invalid, as the comment states it: kind is declaration or link, and path
// names a link. Its zero value is no problem.
type sourceProblem struct {
	kind, path    string
	declaration   []byte // a link: the head's declaration that accepts it, when there is one
	noDeclaration bool   // a link: the head has no .suiteward.yml
	uneditable    bool   // the declaration is a link or another entry that is no regular file, which the web editor cannot fix
}

// sourceProblemOf names the problem that err, an invalid-source error, reports. It cannot for an error that holds no path (an unsafe
// entry that is not an UnsafeEntryError) or that is neither kind (an invalid tree, for example).
func sourceProblemOf(err error) (sourceProblem, bool) {
	var unsafe *inventory.UnsafeEntryError
	switch {
	case errors.As(err, &unsafe) && unsafe.Path == scope.DeclarationPath:
		return sourceProblem{kind: sourceDeclaration, uneditable: true}, true // a declaration that is no regular file
	case errors.As(err, &unsafe):
		return sourceProblem{kind: sourceLink, path: unsafe.Path}, true
	case errors.Is(err, scope.ErrInvalidDeclaration):
		return sourceProblem{kind: sourceDeclaration}, true
	}
	return sourceProblem{}, false
}

// acceptingDeclaration is the head's own .suiteward.yml with the anchored path of link added to exclude (D-SCOPE, an accepted link), or
// nil when the head has none (then noFile is true), when the path has a character that exclude reads as a pattern (the pattern would name more than the link),
// or when the file cannot be read or rewritten: the comment is advice, so it then shows the pattern alone and the check is the same.
// A cancelled or timed-out read is the error instead, so the job retries rather than posting the comment without it.
func (j *checksJob) acceptingDeclaration(ctx context.Context, repo string, tree inventory.Tree, link string) (declaration []byte, noFile bool, err error) {
	if strings.ContainsAny(link, `*?[\`) {
		return nil, false, nil
	}
	entries, err := tree.Entries(ctx)
	if err != nil {
		return nil, false, j.declarationReadFailed(ctx, repo, "list the head's files", err)
	}
	if !hasDeclaration(entries) {
		return nil, true, nil
	}
	raw, err := tree.Read(ctx, scope.DeclarationPath)
	if err != nil {
		return nil, false, j.declarationReadFailed(ctx, repo, "read the head's declaration", err)
	}
	if declaration, err = scope.AddExcludes(raw, []string{"/" + link}); err != nil {
		j.logger.WarnContext(ctx, "github checks invalid-source declaration skipped: the exclude cannot be added", "repo", repo) // its text may quote the head's file
		return nil, false, nil
	}
	return declaration, false, nil
}

// declarationReadFailed is the error of a read that was cancelled or timed out, and nil, after a log line, for any other failure.
func (j *checksJob) declarationReadFailed(ctx context.Context, repo, what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", what, err)
	}
	logReadFailure(ctx, j.logger, "github checks invalid-source declaration skipped: "+what+" failed", repo, err)
	return nil
}

// explainInvalidSource writes the comment that explains an invalid-source check, once for each problem of a pull request: the key
// is the idempotency key, so a problem that was written before is not an error.
func (j *checksJob) explainInvalidSource(ctx context.Context, p owner.ProjectPolicy, pull int, branch string, s sourceProblem) error {
	target := commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull}
	if s.uneditable { // the web editor cannot fix it: no link
		branch = ""
	}
	message, err := newInvalidSourceMessage(target, editableBranch(target, branch), s)
	if err != nil {
		j.logger.WarnContext(ctx, "github checks invalid-source comment skipped: it cannot be rendered", "repo", p.Repo, "pull", pull, "reason", err.Error())
		return nil
	}
	return outboxOnce(ctx, j.suites, owner.ProjectID(p.RepositoryID), []governance.OutboxMessage{message})
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

// bootstrapped maps the assessment of a bootstrap carrier's head to its check. A cancelled head (other coverage than the
// proposal's current revision, which base names) waits, and the owner proposes again.
func (j *checksJob) bootstrapped(head governance.BootstrapHeadResult, base checks.Result) (checks.Result, error) {
	result := base
	result.Revision = head.Revision
	if head.Cancelled {
		result.State, result.Reason = checks.InProgress, reasonBootstrapAwaiting
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

// wrongCode reports whether the owner's latest command that can set or clear the check, created at or after since, is a wrong code.
// A wrong command that is no wrong code has no effect (D-COMMANDS, "A wrong command has no effect"): the ledger never lists one, and a
// processed command whose consent result was rejected (read from its receipt) is skipped for the command before it.
func (j *checksJob) wrongCode(ctx context.Context, p owner.ProjectPolicy, ownerID int64, pull int, latest commands.Entry, since time.Time) (bool, error) {
	for !latest.CreatedAt.Before(since) {
		switch latest.Disposition {
		case commands.InvalidCode, commands.ReplayedCode:
			return true, nil
		case commands.Processed:
		default:
			return false, fmt.Errorf("the owner command %d has the disposition %q, which cannot set or clear a check", latest.CommentID, latest.Disposition)
		}
		var rejected bool
		err := j.suites.Do(ctx, owner.ProjectID(p.RepositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
			receipt, found, err := tx.Receipt(ctx, latest.Operation)
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
			return false, fmt.Errorf("read the receipt of comment %d: %w", latest.CommentID, err)
		}
		if !rejected {
			return false, nil
		}
		var found bool
		if latest, found, err = j.commands.LatestOwnerCommand(ctx, p.RepositoryID, pull, ownerID, latest.CommentID); err != nil || !found {
			return false, err
		}
	}
	return false, nil
}
