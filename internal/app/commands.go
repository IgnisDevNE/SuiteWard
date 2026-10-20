package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

const (
	commandBatch         = 100              // candidates one run takes from the ledger
	commandListingMargin = 10 * time.Second // covers GitHub's listing lag and the one-second resolution of Date (D-COMMANDS, M1.7c)
	commandTolerance     = 1                // TOTP steps either side of the comment's creation (D-COMMANDS)
	maxCommandFailures   = 5                // consecutive failed attempts of one candidate before it is recorded as failed
)

// errLedger marks a failure to read or write the command ledger: the only failure that fails the job.
var errLedger = errors.New("command ledger failed")

// commandsJob is the github_commands job: it decides the owner's PR command comments one at a time, in the order of
// D-COMMANDS, once every enrolled repository has synchronized past them (docs/contracts/runtime.md, "Command and
// bootstrap jobs (M1.6a)"). Every step is safe to repeat after a crash.
type commandsJob struct {
	ledger   commands.Store
	owners   owner.Store
	suites   governance.UnitOfWork
	syncs    repositoryReader // the observation store: each repository's last successful cycle
	repos    []string         // the configured repositories (GITHUB_REPOSITORIES)
	bot      *botAccount
	reader   commandsReader           // the default branch head and the trees that /suiteward propose reads
	content  governance.ContentWriter // where a proposed baseline's files are stored
	contents governance.ContentReader // where /suiteward suggest reads the canonical's declaration after the first contract
	pulls    pullLister               // the observed pull requests: a command's head and head repository
	tier     string                   // the configured INSTALLATION_TIER
	logger   *slog.Logger
	now      func() time.Time

	failing  commandFailure  // the candidate that failed last, in this process; runs never overlap
	recorded bool            // the current run recorded a ledger entry
	warned   map[string]bool // enrolled repositories already reported as not configured
}

// commandsReader is the part of the GitHub reader that /suiteward propose and /suiteward suggest use.
type commandsReader interface {
	BranchHead(ctx context.Context, repo, branch string) (string, error)
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
	pullFileReader // the scope review of a suggest
}

// commandFailure counts the consecutive failed attempts of one candidate.
type commandFailure struct {
	repository, comment int64
	attempts            int
}

// run decides the pending comments and wakes the jobs that read what it recorded: the checks, which show the owner's
// consent, and the bootstrap, which promotes an approved baseline (docs/contracts/runtime.md, "Queues and wake-ups").
// A run that failed after recording an entry still wakes them.
func (j *commandsJob) run(ctx context.Context) (river.Wake, error) {
	j.recorded = false
	err := j.decidePending(ctx)
	if j.recorded {
		return river.Wake{river.ChecksKind, river.BootstrapKind}, err
	}
	return nil, err
}

// decidePending decides the pending comments up to the watermark. A candidate whose decision fails stops the run, so a
// later one never goes ahead of it; the next run retries it, and its fifth consecutive failure records it as failed.
// Only a ledger failure fails the job.
func (j *commandsJob) decidePending(ctx context.Context) error {
	enrollment, enrolled, err := j.owners.Current(ctx)
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled {
		return nil
	}
	horizon, ready, err := j.horizon(ctx, enrollment)
	if err != nil || !ready {
		return err
	}
	pending, err := j.ledger.Pending(ctx, horizon, commandBatch)
	if err != nil {
		return fmt.Errorf("%w: read the pending comments: %w", errLedger, err)
	}
	if len(pending) == 0 {
		return nil
	}
	botID, err := j.bot.ID(ctx)
	if err != nil {
		if cancelled := ctx.Err(); cancelled != nil {
			return cancelled
		}
		failure := readFailure(err)
		j.logger.WarnContext(ctx, "github commands stopped: the bot account could not be resolved", "kind", string(failure.Kind), "status", failure.Status)
		return nil
	}
	for _, candidate := range pending {
		failure := j.decide(ctx, enrollment, botID, candidate)
		if failure == nil {
			j.failing = commandFailure{}
			continue
		}
		if cancelled := ctx.Err(); cancelled != nil {
			return cancelled
		}
		if j.failing.repository != candidate.RepositoryID || j.failing.comment != candidate.Comment.CommentID {
			j.failing = commandFailure{repository: candidate.RepositoryID, comment: candidate.Comment.CommentID}
		}
		j.failing.attempts++
		j.logger.WarnContext(ctx, "github command attempt failed", "repo", candidate.Repo, "comment", candidate.Comment.CommentID, "attempt", j.failing.attempts, "error", failure.Error())
		if j.failing.attempts < maxCommandFailures {
			if errors.Is(failure, errLedger) {
				return failure
			}
			return nil
		}
		if err := j.recordFailed(ctx, enrollment, candidate); err != nil {
			return err
		}
		j.logger.ErrorContext(ctx, "github command recorded as failed", "repo", candidate.Repo, "comment", candidate.Comment.CommentID, "error", failure.Error())
		j.failing = commandFailure{}
	}
	return nil
}

// horizon is the sync watermark (D-COMMANDS, "The sync watermark in GitHub's clock", M1.7c): the earliest
// CycleStartedAt among the enrolled repositories, GitHub's own clock, minus commandListingMargin. ready is false,
// with a Warn line (every command waits), while an enrolled repository has no recorded cycle start: nothing can be
// decided before it has been seen. JOB_TIMEOUT and the former clock skew no longer enter the horizon.
func (j *commandsJob) horizon(ctx context.Context, enrollment owner.State) (horizon time.Time, ready bool, err error) {
	var earliest time.Time
	for _, project := range enrollment.Projects {
		// Fail closed: an enrolled repository holds the watermark back even after it left GITHUB_REPOSITORIES, so say why.
		if !slices.Contains(j.repos, project.Repo) && !j.warned[project.Repo] {
			if j.warned == nil {
				j.warned = map[string]bool{}
			}
			j.warned[project.Repo] = true
			j.logger.WarnContext(ctx, "github commands wait for an enrolled repository that is not configured in GITHUB_REPOSITORIES", "repo", project.Repo)
		}
		state, _, err := j.syncs.Repository(ctx, project.Repo)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("read the synchronization state of %s: %w", project.Repo, err)
		}
		if state.CycleStartedAt.IsZero() {
			j.logger.WarnContext(ctx, "github commands wait: an enrolled repository has not synchronized yet", "repo", project.Repo)
			return time.Time{}, false, nil
		}
		if earliest.IsZero() || state.CycleStartedAt.Before(earliest) {
			earliest = state.CycleStartedAt
		}
	}
	return earliest.Add(-commandListingMargin), true, nil
}

// record writes the decision for the candidate, with its acknowledgment in the same transaction. Its error is a ledger failure.
func (j *commandsJob) record(ctx context.Context, c commands.Candidate, d commands.Disposition, operation contract.OperationID, ack *governance.OutboxMessage) error {
	m := c.Comment
	entry := commands.Entry{RepositoryID: c.RepositoryID, CommentID: m.CommentID, PullNumber: c.PullNumber, AuthorID: m.AuthorID, CreatedAt: m.CreatedAt,
		BodyDigest: m.BodyDigest, Disposition: d, Operation: operation, DecidedAt: j.now()}
	if err := j.ledger.Record(ctx, entry, ack); err != nil {
		return &opaque{what: "record the command", cause: errors.Join(errLedger, err)}
	}
	j.recorded = true
	return nil
}

// decide applies the disposition steps of the contract to one candidate, in order. Its errors never carry comment text
// or a code: the log and river_job.errors keep only the step that failed.
func (j *commandsJob) decide(ctx context.Context, enrollment owner.State, botID int64, c commands.Candidate) error {
	m := c.Comment
	parsed := commandtext.Parse(m.Body)
	if d, ignored := ignoredDisposition(enrollment, botID, m, parsed); ignored {
		return j.record(ctx, c, d, "", nil)
	}
	reject := func(d commands.Disposition, reason commandtext.Reason, reference string) error {
		ack, err := newRejectedAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, m.CommentID, enrollment.Owner.Login, d, reason, reference)
		if err != nil {
			return &opaque{what: "build the acknowledgment", cause: err}
		}
		return j.record(ctx, c, d, "", &ack)
	}
	switch {
	case !m.UpdatedAt.Equal(m.CreatedAt): // edited before it was first observed: not a command, and no step is used
		return reject(commands.EditedFirst, 0, "")
	case parsed.Kind == commandtext.KindMalformed:
		return reject(commands.Malformed, parsed.Reason, "")
	}
	switch command := parsed.Command; command.Verb {
	case commandtext.Propose: // no code, no reference
		return j.propose(ctx, enrollment, c)
	case commandtext.Suggest: // no code, no reference: it reads no step and grants nothing
		return j.suggest(ctx, enrollment, c)
	default:
		return j.consent(ctx, enrollment, c, command, reject)
	}
}

// ignoredDisposition is the disposition of a comment that is not a command of the owner's, and whether it is one.
func ignoredDisposition(enrollment owner.State, botID int64, m githubsync.CommentVersion, parsed commandtext.Parsed) (commands.Disposition, bool) {
	switch {
	case m.CreatedAt.Before(enrollment.EnrolledAt):
		return commands.BeforeEnrollment, true
	case m.AuthorID == botID || parsed.Kind == commandtext.KindNotCommand:
		return commands.NotCommand, true
	case m.AuthorID != enrollment.Owner.AccountID:
		return commands.IgnoredAuthor, true
	}
	return "", false
}

// rejectFunc records a command as rejected with its acknowledgment.
type rejectFunc func(d commands.Disposition, reason commandtext.Reason, reference string) error

// consent decides an approve, a revoke or a skip: it ignores one first observed after its pull request stopped being open, refuses an approve or a revoke
// for a proposal that another pull request carries, and otherwise verifies and consumes the code, then applies the command.
func (j *commandsJob) consent(ctx context.Context, enrollment owner.State, c commands.Candidate, command commandtext.Command, reject rejectFunc) error {
	pull, err := j.observedPull(ctx, c)
	if err != nil {
		return err
	}
	// A command the sync first observed while the pull request was listed open is still decided after it closes. Both times are the
	// sync's own clock (the cycle's At), so GitHub's created_at never enters; equal times count as closed. No step, no consent, no acknowledgment.
	if !pull.NotOpenSince.IsZero() && !c.Comment.FirstSeen.Before(pull.NotOpenSince) {
		return j.record(ctx, c, commands.NotCommand, "", nil)
	}
	var reference string // a skip names no proposal
	if command.Verb != commandtext.Skip {
		reference = commandtext.Reference(command.Proposal, command.Revision)
		elsewhere, err := j.carriedElsewhere(ctx, c, command)
		if err != nil {
			return err
		}
		if elsewhere {
			return reject(commands.OtherPull, 0, reference)
		}
	}
	source := commentSource(c.Comment.CommentID)
	if verified, err := j.verifyCode(ctx, enrollment, c, command, source, reference, reject); !verified {
		return err
	}
	if command.Verb == commandtext.Skip {
		return j.skip(ctx, enrollment, c)
	}
	return j.process(ctx, enrollment, c, command, source, reference, reject)
}

// carriedElsewhere reports whether the proposal the command names exists and is carried by another pull request than the
// comment's. A missing Suite or proposal is not: the consent that follows answers it.
func (j *commandsJob) carriedElsewhere(ctx context.Context, c commands.Candidate, command commandtext.Command) (elsewhere bool, err error) {
	err = j.suites.Do(ctx, owner.ProjectID(c.RepositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, proposalID(command.Proposal))
		if err != nil {
			return err
		}
		elsewhere = proposal.Current().Carrier() != carrierID(c.RepositoryID, c.PullNumber)
		return nil
	})
	if errors.Is(err, governance.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, &opaque{what: "look for the proposal", cause: err}
	}
	return elsewhere, nil
}

// verifyCode checks the code of the command and consumes its step. verified is false when the command was recorded as rejected, or the step failed:
// err is then the result of that.
func (j *commandsJob) verifyCode(ctx context.Context, enrollment owner.State, c commands.Candidate, command commandtext.Command, source contract.SourceCommandID, reference string, reject rejectFunc) (verified bool, err error) {
	m := c.Comment
	step, err := totp.Verify(enrollment.Secret, command.Code, m.CreatedAt, commandTolerance)
	if errors.Is(err, totp.ErrInvalidCode) {
		j.logRejectedOffset(ctx, enrollment.Secret, command.Code, c)
		return false, reject(commands.InvalidCode, 0, reference)
	}
	if err != nil {
		return false, &opaque{what: "verify the code", cause: err}
	}
	// Logged before the step is consumed, so a replayed code also logs "verified" and is then rejected.
	j.logger.InfoContext(ctx, "github command code verified", "repo", c.Repo, "comment", m.CommentID, "offset", step-totp.Step(m.CreatedAt))
	// The comment's own time keeps a retry after a crash identical to the first call.
	switch err := j.owners.ConsumeStep(ctx, enrollment.Owner.AccountID, step, m.CreatedAt, source); {
	case errors.Is(err, owner.ErrStepUsed):
		return false, reject(commands.ReplayedCode, 0, reference)
	case err != nil:
		return false, &opaque{what: "consume the step", cause: err}
	}
	return true, nil
}

// process applies a verified approve or revoke through ProcessConsent and records the command.
func (j *commandsJob) process(ctx context.Context, enrollment owner.State, c commands.Candidate, command commandtext.Command, source contract.SourceCommandID, reference string, reject rejectFunc) error {
	m := c.Comment
	project, operation := owner.ProjectID(c.RepositoryID), contract.OperationID("consent:"+string(source))
	ack, err := newProcessedAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, m.CommentID, enrollment.Owner.Login, project, defaultSuite, operation)
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	request, err := consentRequest(enrollment, c, command, source, operation)
	if err != nil {
		return err
	}
	request.Outbox = []governance.OutboxMessage{ack}
	_, err = governance.ProcessConsent(ctx, j.suites, request)
	if errors.Is(err, governance.ErrNotFound) {
		// Governance reports a missing Suite and an unknown proposal alike: look at each. Do answers ErrNotFound for a
		// missing Suite, and the unit of work reports a missing proposal through proposalMissing, so the two never mix.
		var proposalMissing bool
		probe := j.suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
			_, _, err := tx.Proposal(ctx, proposalID(command.Proposal))
			proposalMissing = errors.Is(err, governance.ErrNotFound)
			if proposalMissing {
				return nil
			}
			return err
		})
		switch {
		case errors.Is(probe, governance.ErrNotFound):
			return reject(commands.NoSuite, 0, reference)
		case probe != nil:
			return &opaque{what: "look for the suite", cause: probe}
		case proposalMissing:
			return reject(commands.UnknownProposal, 0, reference)
		}
		// Both exist now: the bootstrap wrote them after ProcessConsent read. Fail this attempt; the next run processes it.
	}
	if err != nil {
		return &opaque{what: "process the consent", cause: err}
	}
	return j.record(ctx, c, commands.Processed, operation, nil)
}

// measuredTolerance is how far logRejectedOffset looks, to measure whether commandTolerance is too narrow.
const measuredTolerance = 2

// logRejectedOffset logs, for measurement only, whether a wider window would have accepted a rejected code and at which
// offset in steps from the comment's step. The result never changes the decision, and neither the code nor the secret is logged.
func (j *commandsJob) logRejectedOffset(ctx context.Context, secret []byte, code string, c commands.Candidate) {
	step, err := totp.Verify(secret, code, c.Comment.CreatedAt, measuredTolerance)
	if err != nil {
		j.logger.InfoContext(ctx, "github command code rejected", "repo", c.Repo, "comment", c.Comment.CommentID, "would_match", false)
		return
	}
	j.logger.InfoContext(ctx, "github command code rejected", "repo", c.Repo, "comment", c.Comment.CommentID, "would_match", true, "offset", step-totp.Step(c.Comment.CreatedAt))
}

// consentRequest is the command of the owner's comment with the identifiers of D-COMMANDS.
func consentRequest(enrollment owner.State, c commands.Candidate, command commandtext.Command, source contract.SourceCommandID, operation contract.OperationID) (governance.ConsentRequest, error) {
	actor, err := contract.NewPrincipal(owner.PrincipalID(enrollment.Owner.AccountID), contract.Human)
	if err != nil {
		return governance.ConsentRequest{}, &opaque{what: "build the actor", cause: err}
	}
	action := contract.ApproveConsent
	if command.Verb == commandtext.Revoke {
		action = contract.RevokeConsent
	}
	built, err := contract.NewCommand(contract.CommandInput{OperationID: operation, SourceCommandID: source, Actor: actor, Action: action,
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(c.RepositoryID), SuiteID: defaultSuite, ProposalID: proposalID(command.Proposal),
			RevisionID: contract.ProposalRevisionID("R" + strconv.Itoa(command.Revision))},
		Carrier: carrierID(c.RepositoryID, c.PullNumber), Order: contract.CommandOrder(c.Comment.CommentID)})
	if err != nil {
		return governance.ConsentRequest{}, &opaque{what: "build the command", cause: err}
	}
	return governance.ConsentRequest{Command: built}, nil
}

// recordFailed records a candidate that kept failing. The owner's own /suiteward propose gets an acknowledgment that
// says nothing was proposed and asks for the command again; any other command is not acknowledged.
func (j *commandsJob) recordFailed(ctx context.Context, enrollment owner.State, c commands.Candidate) error {
	m := c.Comment
	parsed := commandtext.Parse(m.Body)
	ownerPropose := parsed.Kind == commandtext.KindCommand && parsed.Command.Verb == commandtext.Propose && m.AuthorID == enrollment.Owner.AccountID &&
		!m.CreatedAt.Before(enrollment.EnrolledAt) && m.UpdatedAt.Equal(m.CreatedAt) // the same conditions as decide, before it reaches propose
	if !ownerPropose {
		return j.record(ctx, c, commands.Failed, "", nil)
	}
	ack, err := newBootstrapAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, m.CommentID, enrollment.Owner.Login, commands.Failed, "", "")
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	return j.record(ctx, c, commands.Failed, "", &ack)
}

// bootstrapView is what propose and skip read from the Suite in one unit of work.
type bootstrapView struct {
	canonical bool
	target    contract.IntegrationTargetID
	policy    contract.PolicyRevisionID
	latest    contract.ProposalRevision // the current revision of the latest bootstrap proposal, when hasLatest
	hasLatest bool
	next      contract.ProposalID // P<n+1>, free only under the Suite lock: propose uses it in the one proposing call
	pull      githubsync.PullState
}

// targetBranch is the default branch that the Suite targets.
func (v bootstrapView) targetBranch() (string, error) {
	branch, ok := strings.CutPrefix(string(v.target), targetPrefix)
	if !ok {
		return "", errors.New("the suite does not target a branch")
	}
	return branch, nil
}

// bootstrapView reads the Suite and the observed pull request of the candidate. governance.ErrNotFound means the Suite
// does not exist.
func (j *commandsJob) bootstrapView(ctx context.Context, c commands.Candidate) (v bootstrapView, err error) {
	err = j.suites.Do(ctx, owner.ProjectID(c.RepositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return err // Do and Suite report a missing Suite as ErrNotFound; the reads below never do, so they drop the wrapping
		}
		_, v.canonical = state.Canonical.Suite().CurrentVersionID()
		v.target, v.policy = state.Target, state.Policy.RevisionID()
		if v.canonical {
			return nil
		}
		id, found, err := tx.LatestBootstrapProposal(ctx)
		if err != nil {
			return fmt.Errorf("find the latest bootstrap proposal: %v", err)
		}
		if found {
			proposal, _, err := tx.Proposal(ctx, id)
			if err != nil {
				return fmt.Errorf("load the latest bootstrap proposal: %v", err)
			}
			v.latest, v.hasLatest = proposal.Current(), true
		}
		if v.next, err = tx.NextProposalID(ctx); err != nil {
			return fmt.Errorf("number the next proposal: %v", err)
		}
		return nil
	})
	return v, err
}

// observedPull returns the observed state of the candidate's pull request.
func (j *commandsJob) observedPull(ctx context.Context, c commands.Candidate) (githubsync.PullState, error) {
	pulls, err := j.pulls.Pulls(ctx, c.Repo)
	if err != nil {
		return githubsync.PullState{}, &opaque{what: "read the observed pull requests", cause: err}
	}
	i := slices.IndexFunc(pulls, func(p githubsync.PullState) bool { return p.Number == c.PullNumber })
	if i < 0 { // the comment was observed on this pull request, so the observation store must know it
		return githubsync.PullState{}, &opaque{what: "find the observed pull request", cause: errors.New("the pull request is not observed")}
	}
	return pulls[i], nil
}

// gate reads what propose and skip both decide on and records their common refusals: no Suite, a canonical, and a fork.
// done is true when it recorded one, with the acknowledgment of refused (ProposeRefused or SkipRefused).
func (j *commandsJob) gate(ctx context.Context, enrollment owner.State, c commands.Candidate, refused commands.Disposition) (view bootstrapView, done bool, err error) {
	m, target := c.Comment, commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}
	refuse := func(d commands.Disposition, ack governance.OutboxMessage, err error) (bootstrapView, bool, error) {
		if err != nil {
			return bootstrapView{}, false, &opaque{what: "build the acknowledgment", cause: err}
		}
		return bootstrapView{}, true, j.record(ctx, c, d, "", &ack)
	}
	view, err = j.bootstrapView(ctx, c)
	switch {
	case errors.Is(err, governance.ErrNotFound):
		ack, err := newRejectedAck(target, m.CommentID, enrollment.Owner.Login, commands.NoSuite, 0, "")
		return refuse(commands.NoSuite, ack, err)
	case err != nil:
		return bootstrapView{}, false, &opaque{what: "read the suite", cause: err}
	case view.canonical:
		ack, err := newBootstrapAck(target, m.CommentID, enrollment.Owner.Login, refused, reasonCanonicalPresent, "")
		return refuse(refused, ack, err)
	}
	if view.pull, err = j.observedPull(ctx, c); err != nil {
		return bootstrapView{}, false, err
	}
	if isFork(view.pull.HeadRepo, c.Repo) {
		ack, err := newBootstrapAck(target, m.CommentID, enrollment.Owner.Login, refused, reasonFork, "")
		return refuse(refused, ack, err)
	}
	return view, false, nil
}

// skip decides a /suiteward skip whose code was verified and consumed: the pull request may merge before the first
// contract, unless the Suite has a canonical, the pull request is a fork, or it carries the latest bootstrap proposal.
func (j *commandsJob) skip(ctx context.Context, enrollment owner.State, c commands.Candidate) error {
	view, done, err := j.gate(ctx, enrollment, c, commands.SkipRefused)
	if done || err != nil {
		return err
	}
	d, reason := commands.Skipped, ""
	if view.hasLatest && view.latest.Carrier() == carrierID(c.RepositoryID, c.PullNumber) {
		d, reason = commands.SkipRefused, reasonCarrier
	}
	ack, err := newBootstrapAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, d, reason, "")
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	return j.record(ctx, c, d, "", &ack)
}

// bootstrapSource is the commit and tree a propose proposes as the first baseline.
type bootstrapSource struct {
	kind   contract.OriginKind
	origin string // the commit
	branch string // the default branch, for a branch source
	tree   *githubTree
}

// tree reads a commit's tree and its entries; its errors are fixed text.
func (j *commandsJob) tree(ctx context.Context, repo, sha string) (*githubTree, []inventory.Entry, error) {
	read, err := j.reader.Tree(ctx, repo, sha)
	if err != nil {
		return nil, nil, &opaque{what: "read a tree", cause: githubRead(err)}
	}
	tree := &githubTree{tree: read, files: map[string][]byte{}}
	entries, err := tree.Entries(ctx)
	if err != nil {
		return nil, nil, &opaque{what: "list a tree", cause: err}
	}
	return tree, entries, nil
}

// hasDeclaration reports whether entries hold .suiteward.yml as a file: a link or a submodule of that name is not a declaration.
func hasDeclaration(entries []inventory.Entry) bool {
	return slices.ContainsFunc(entries, func(e inventory.Entry) bool { return e.Path == scope.DeclarationPath && e.Kind == inventory.File })
}

// source picks the tree to propose: the default branch's head when it has .suiteward.yml, else the pull request's head
// when that has it. found is false when neither does, with the suggestion built from the pull request's tree, or the
// default branch's when the pull request's gives no pattern.
func (j *commandsJob) source(ctx context.Context, c commands.Candidate, view bootstrapView) (src bootstrapSource, suggestion inventory.Suggestion, found bool, err error) {
	branch, err := view.targetBranch()
	if err != nil {
		return bootstrapSource{}, inventory.Suggestion{}, false, err
	}
	head, err := j.reader.BranchHead(ctx, c.Repo, branch)
	if err != nil {
		return bootstrapSource{}, inventory.Suggestion{}, false, &opaque{what: "read the default branch head", cause: githubRead(err)}
	}
	mainTree, mainEntries, err := j.tree(ctx, c.Repo, head)
	if err != nil {
		return bootstrapSource{}, inventory.Suggestion{}, false, err
	}
	if hasDeclaration(mainEntries) {
		return bootstrapSource{kind: contract.OriginBranch, origin: head, branch: branch, tree: mainTree}, inventory.Suggestion{}, true, nil
	}
	pullTree, pullEntries, err := j.tree(ctx, c.Repo, view.pull.HeadSHA)
	if err != nil {
		return bootstrapSource{}, inventory.Suggestion{}, false, err
	}
	if hasDeclaration(pullEntries) {
		return bootstrapSource{kind: contract.OriginPull, origin: view.pull.HeadSHA, tree: pullTree}, inventory.Suggestion{}, true, nil
	}
	for _, tree := range []*githubTree{pullTree, mainTree} { // the suggestion is built from their paths, which are untrusted: an invalid one is an invalid source
		if _, err := inventory.ListEntries(ctx, tree); err != nil {
			return bootstrapSource{}, inventory.Suggestion{}, false, err
		}
	}
	if suggestion = inventory.Suggest(pullEntries); len(suggestion.Include)+len(suggestion.Runner) == 0 {
		suggestion = inventory.Suggest(mainEntries)
	}
	return bootstrapSource{}, suggestion, false, nil
}

// reusable reports the reference of the latest bootstrap proposal when it is carried by carrier and its current
// revision has the origin, kind and coverage of src and proposed. The coverage includes the policy revision, as the
// checks job compares it with AssessBootstrapHead: a proposal that call would cancel must not be answered with
// "already proposed".
func (v bootstrapView) reusable(carrier contract.ApprovalCarrierID, src bootstrapSource, proposed contract.ProtectedContract) (string, bool, error) {
	if !v.hasLatest || v.latest.Carrier() != carrier || v.latest.Origin() != contract.SourceRevision(src.origin) || v.latest.OriginKind() != src.kind {
		return "", false, nil
	}
	reference := v.latest.Binding().Reference()
	binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: proposed.Manifest().Digest(), Scope: proposed.ScopeDigest(),
		PolicyRevision: v.policy, CoveredInputs: proposed.CoveredInputs()})
	if err != nil {
		return "", false, err
	}
	if !v.latest.Binding().SameCoverage(binding) {
		return "", false, nil
	}
	return fmt.Sprintf("%s-%s", reference.ProposalID, reference.RevisionID), true, nil
}

// refusePropose records a propose-refused with the reason code of its acknowledgment.
func (j *commandsJob) refusePropose(ctx context.Context, enrollment owner.State, c commands.Candidate, reason string) error {
	ack, err := newBootstrapAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, commands.ProposeRefused, reason, "")
	if err != nil {
		return &opaque{what: "build the acknowledgment", cause: err}
	}
	return j.record(ctx, c, commands.ProposeRefused, "", &ack)
}

// propose decides a /suiteward propose (docs/contracts/runtime.md, "Explicit bootstrap: propose and skip"). A read of
// GitHub or of the store that fails is an error of this candidate: the retry rule runs it again and, after five
// attempts, records it as failed with the propose-failed acknowledgment. A source that cannot be read as a contract (an
// invalid declaration, a link in the protected files, a truncated tree) is refused with invalid-source.
func (j *commandsJob) propose(ctx context.Context, enrollment owner.State, c commands.Candidate) error {
	view, done, err := j.gate(ctx, enrollment, c, commands.ProposeRefused)
	if done || err != nil {
		return err
	}
	// Before any tree is read: a pull request that is not open, or whose base is not the Suite's target, could never decide V1 by its merge.
	switch {
	case !view.pull.NotOpenSince.IsZero():
		return j.refusePropose(ctx, enrollment, c, reasonNotOpen)
	case contract.IntegrationTargetID(targetPrefix+view.pull.BaseRef) != view.target:
		return j.refusePropose(ctx, enrollment, c, reasonBaseNotTarget)
	}
	src, inv, done, err := j.proposalSource(ctx, enrollment, c, view)
	if done || err != nil {
		return err
	}
	proposed, err := inv.Contract()
	if err != nil {
		return &opaque{what: "contract of the source", cause: err}
	}
	carrier := carrierID(c.RepositoryID, c.PullNumber)
	reference, reused, err := view.reusable(carrier, src, proposed)
	if err != nil {
		return &opaque{what: "compare the proposal", cause: err}
	}
	if reused { // a retry after a crash between the proposal and the ledger, or a repeated command on an unchanged pull request
		ack, err := newBootstrapAck(commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}, c.Comment.CommentID, enrollment.Owner.Login, commands.Proposed, "", reference)
		if err != nil {
			return &opaque{what: "build the acknowledgment", cause: err}
		}
		return j.record(ctx, c, commands.Proposed, "", &ack)
	}
	return j.writeProposal(ctx, enrollment, c, view, src, inv)
}

// proposalSource reads the source a propose proposes and its inventory. done is true when it recorded a refusal instead: a source that cannot be read as a
// contract is a fact about the commit, so it is refused with invalid-source and never retried, and a source with no declaration gets the suggestion of one.
func (j *commandsJob) proposalSource(ctx context.Context, enrollment owner.State, c commands.Candidate, view bootstrapView) (src bootstrapSource, inv inventory.Inventory, done bool, err error) {
	m, login := c.Comment, enrollment.Owner.Login
	target := commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}
	// base and branch name the file that failed when the source was found; none when it could not be read at all.
	refuseSource := func(base, branch string) (bootstrapSource, inventory.Inventory, bool, error) {
		j.logger.WarnContext(ctx, "github propose refused: the source cannot be read as a contract", "repo", c.Repo, "comment", m.CommentID)
		ack, err := newProposeInvalidSourceAck(target, m.CommentID, login, base, branch)
		if err != nil {
			return bootstrapSource{}, inventory.Inventory{}, false, &opaque{what: "build the acknowledgment", cause: err}
		}
		return bootstrapSource{}, inventory.Inventory{}, true, j.record(ctx, c, commands.ProposeRefused, "", &ack)
	}
	src, suggestion, found, err := j.source(ctx, c, view)
	if invalidSource(err) {
		return refuseSource("", "")
	}
	if err != nil {
		return bootstrapSource{}, inventory.Inventory{}, false, err
	}
	if !found {
		ack, err := newDeclarationMissingAck(target, m.CommentID, login, view.pull.HeadRef, suggestion, commands.ProposeRefused)
		if err != nil {
			return bootstrapSource{}, inventory.Inventory{}, false, &opaque{what: "build the acknowledgment", cause: err}
		}
		return bootstrapSource{}, inventory.Inventory{}, true, j.record(ctx, c, commands.ProposeRefused, "", &ack)
	}
	inv, err = inventory.Build(ctx, src.tree)
	if invalidSource(err) { // the source is known: the file that holds the declaration is the one to fix
		if src.kind == contract.OriginBranch {
			return refuseSource(baseDefaultBranch, src.branch)
		}
		return refuseSource(basePull, view.pull.HeadRef)
	}
	if err != nil {
		return bootstrapSource{}, inventory.Inventory{}, false, &opaque{what: "inventory of the source", cause: err}
	}
	// ADR 0010: never manufacture an empty canonical suite. A source holds .suiteward.yml, which covers itself, so its
	// inventory is never empty: this is an impossible state, not the empty-inventory refusal.
	if len(inv.Manifest().Entries()) == 0 {
		return bootstrapSource{}, inventory.Inventory{}, false, &opaque{what: "inventory of the source", cause: governance.ErrInvalidState}
	}
	return src, inv, false, nil
}

// writeProposal proposes the baseline of src as the next proposal, with its bootstrap summary, and records the command.
func (j *commandsJob) writeProposal(ctx context.Context, enrollment owner.State, c commands.Candidate, view bootstrapView, src bootstrapSource, inv inventory.Inventory) error {
	target := commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}
	number, err := strconv.Atoi(strings.TrimPrefix(string(view.next), "P"))
	if err != nil {
		return &opaque{what: "number the proposal", cause: err}
	}
	tier, _ := effectiveTier(j.tier, enrollment, true)
	inputs := bootstrapSummaryInputs{Proposal: number, Revision: 1, Source: sourcePull, Origin: src.origin, Tier: tier}
	if src.kind == contract.OriginBranch {
		inputs.Source, inputs.Branch = sourceBranch, src.branch
	}
	message, err := newSummaryMessage(target, inventoryInputs(inputs, inv))
	if err != nil {
		return &opaque{what: "build the summary", cause: err}
	}
	result, err := governance.ProposeBaseline(ctx, j.suites, j.content, governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(c.RepositoryID), SuiteID: defaultSuite, ProposalID: view.next, RevisionID: firstRevision},
		Carrier:   carrierID(c.RepositoryID, c.PullNumber), Origin: contract.SourceRevision(src.origin), OriginKind: src.kind, Tree: src.tree, Outbox: []governance.OutboxMessage{message}})
	if err != nil {
		return &opaque{what: "propose the baseline", cause: err}
	}
	if !result.Revise.Committed { // a fresh proposal number cannot have the coverage already: the numbering raced with another writer
		return &opaque{what: "propose the baseline", cause: errors.New("the proposal was not written")}
	}
	j.logger.InfoContext(ctx, "github baseline proposed", "repo", c.Repo, "proposal", number, "pull", c.PullNumber, "source", inputs.Source)
	return j.record(ctx, c, commands.Proposed, "", nil)
}

// errAnswerNotRenderable marks a suggestion whose acknowledgment cannot be rendered (a declaration over the limit, a branch the renderer
// refuses): a fact about the source, so it is never retried.
var errAnswerNotRenderable = errors.New("the answer cannot be rendered")

// The codes of the base of a suggest (ackPayload.Base): what the head was compared with.
const (
	basePull          = "pull"           // the head's own .suiteward.yml, before the first contract
	baseDefaultBranch = "default-branch" // the default branch's, before the first contract, when the head has none
	baseContract      = "contract"       // the canonical's, after the first contract
)

// invalidBase says that the file a suggest compares with, base (pull or default-branch), is not a valid declaration. It stays an invalid source
// for invalidSource, and carries only the base: its text is never logged or sent, because it may quote the file.
type invalidBase struct {
	base string
	err  error
}

func (e *invalidBase) Error() string { return "the " + e.base + " declaration is not valid" }
func (e *invalidBase) Unwrap() error { return e.err }

// suggest decides a /suiteward suggest (docs/contracts/runtime.md, "Scope discovery (M1.9)"). It is owner only and takes no code, so it reads
// no step and grants nothing: a fork or a closed pull request is refused, and otherwise the acknowledgment compares the head with a base declaration
// (the canonical's after the first contract, before it the head's own file or else the default branch's) and shows only the difference. Before the
// first contract with no declaration anywhere it is the suggestion of a propose that finds none, in the words of a suggest. A scope review, when the pull
// request's diff gives a line for it, is written before the acknowledgment: the ledger takes one message, and a recorded command is never retried. A GitHub or store
// failure is an error of this candidate, like a propose. A source that cannot be answered from is recorded as suggest-refused with a fixed reason and
// never retried: invalid-source (an invalid tree or declaration, a canonical without a declaration, a notice that cannot be rendered). A declaration that cannot
// be shown (hidden characters, or over the size of a notice) is no refusal: the reply lists the patterns alone. Neither disposition is a command for the check.
func (j *commandsJob) suggest(ctx context.Context, enrollment owner.State, c commands.Candidate) error {
	m, login := c.Comment, enrollment.Owner.Login
	target := commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}
	view, err := j.bootstrapView(ctx, c)
	switch {
	case errors.Is(err, governance.ErrNotFound):
		// The owner is told, as for a propose, that the bootstrap is pending. The entry is Suggested, not NoSuite: NoSuite counts as the
		// owner's latest command and could replace a rejected approve of the check.
		ack, err := newRejectedAck(target, m.CommentID, login, commands.NoSuite, 0, "")
		if err != nil {
			return &opaque{what: "build the acknowledgment", cause: err}
		}
		return j.record(ctx, c, commands.Suggested, "", &ack)
	case err != nil:
		return &opaque{what: "read the suite", cause: err}
	}
	if view.pull, err = j.observedPull(ctx, c); err != nil {
		return err
	}
	// branch is the branch of the file the owner is asked to fix, for the editor link: none for a refusal that asks for no edit.
	refuse := func(reason, base, branch string) error {
		ack, err := newSuggestRefusedAck(target, m.CommentID, login, reason, base, branch)
		if err != nil {
			return &opaque{what: "build the acknowledgment", cause: err}
		}
		return j.record(ctx, c, commands.SuggestRefused, "", &ack)
	}
	switch {
	case isFork(view.pull.HeadRepo, c.Repo):
		return refuse(reasonFork, "", "")
	case !view.pull.NotOpenSince.IsZero():
		return refuse(reasonNotOpen, "", "")
	}
	ack, reviews, err := j.suggestion(ctx, target, c, login, view)
	var invalid *invalidBase
	switch {
	case errors.As(err, &invalid):
		j.logger.InfoContext(ctx, "github suggest refused: the base declaration is not valid", "repo", c.Repo, "comment", m.CommentID, "base", invalid.base)
		branch, err := editBranch(view, invalid.base)
		if err != nil {
			return &opaque{what: "read the default branch", cause: err}
		}
		return refuse(refusalInvalidSource, invalid.base, branch)
	case invalidSource(err) || errors.Is(err, governance.ErrNoCanonicalDeclaration) || errors.Is(err, errAnswerNotRenderable):
		j.logger.InfoContext(ctx, "github suggest refused: the source cannot be answered from", "repo", c.Repo, "comment", m.CommentID)
		return refuse(refusalInvalidSource, "", "")
	case errors.Is(err, errGitHubRead):
		logReadFailure(ctx, j.logger, "github suggest read failed", c.Repo, err)
		return &opaque{what: "read for the suggestion", cause: err}
	case err != nil:
		return &opaque{what: "build the suggestion", cause: err}
	}
	if err := outboxOnce(ctx, j.suites, owner.ProjectID(c.RepositoryID), reviews); err != nil {
		return &opaque{what: "write the scope review", cause: err}
	}
	return j.record(ctx, c, commands.Suggested, "", &ack)
}

// suggestion builds the acknowledgment of a suggest on an open pull request of the repository, and the scope review that goes with it.
// After the first contract the base is the canonical's declaration and nothing else. Before it the base is picked as a propose picks its
// source: the default branch's .suiteward.yml when it has one, else the head's own. This grants nothing: only a propose binds.
func (j *commandsJob) suggestion(ctx context.Context, target commentTarget, c commands.Candidate, login string, view bootstrapView) (governance.OutboxMessage, []governance.OutboxMessage, error) {
	head, headEntries, err := j.tree(ctx, c.Repo, view.pull.HeadSHA)
	if err != nil {
		return governance.OutboxMessage{}, nil, err
	}
	if view.canonical {
		raw, governing, err := canonicalDeclaration(ctx, j.suites, j.contents, owner.ProjectID(c.RepositoryID))
		if err != nil {
			return governance.OutboxMessage{}, nil, err
		}
		return j.compare(ctx, target, c, login, view, head, baseContract, raw, governing)
	}
	main, mainEntries, err := j.defaultBranchTree(ctx, c, view)
	if err != nil {
		return governance.OutboxMessage{}, nil, err
	}
	var base string
	var source *githubTree
	switch {
	case hasDeclaration(mainEntries):
		base, source = baseDefaultBranch, main
	case hasDeclaration(headEntries):
		base, source = basePull, head
	default: // nothing to compare with: the suggestion of a propose that finds no declaration
		for _, tree := range []*githubTree{head, main} { // both are untrusted, and the default branch's is the fallback: an invalid path is an invalid source here too
			if _, err := inventory.ListEntries(ctx, tree); err != nil {
				return governance.OutboxMessage{}, nil, err
			}
		}
		ack, err := declarationMissing(target, c, login, view.pull.HeadRef, headEntries, mainEntries)
		return ack, nil, err
	}
	raw, err := source.Read(ctx, scope.DeclarationPath)
	if err != nil {
		return governance.OutboxMessage{}, nil, err
	}
	declaration, err := scope.ParseDeclaration(raw)
	switch {
	case errors.Is(err, scope.ErrInvalidDeclaration): // invalid-source, naming the file
		return governance.OutboxMessage{}, nil, &invalidBase{base: base, err: err}
	case err != nil:
		return governance.OutboxMessage{}, nil, err
	}
	return j.compare(ctx, target, c, login, view, head, base, raw, scope.NewScope(declaration))
}

// compare answers a suggest by the missing patterns of the base declaration raw, which declares governing: those for the test-like paths of
// the head it does not cover, and the runner patterns it neither lists nor covers, which need no file in the head (live run 3). The review edits the head's file, so it goes only with a base that is that file or the canonical, never with the default branch's.
func (j *commandsJob) compare(ctx context.Context, target commentTarget, c commands.Candidate, login string, view bootstrapView, head *githubTree, base string, raw []byte, governing scope.Scope) (governance.OutboxMessage, []governance.OutboxMessage, error) {
	found, err := discoverUnder(ctx, head, raw, governing)
	if err != nil {
		return governance.OutboxMessage{}, nil, err
	}
	missing, err := inventory.MissingRunner(found.entries, governing)
	if err != nil {
		return governance.OutboxMessage{}, nil, fmt.Errorf("find the missing runner names: %w", err)
	}
	// The names a missing path already brings are no news: only the others, which no uncovered file explains, are part of the review's key.
	fromPaths, err := found.pathsSuggestion(found.Paths)
	if err != nil {
		return governance.OutboxMessage{}, nil, err
	}
	unexplained := slices.DeleteFunc(missing, func(pattern string) bool { return slices.Contains(fromPaths.Runner, pattern) })
	if len(found.Paths) == 0 && len(unexplained) == 0 {
		ack, err := newNothingUncoveredAck(target, c.Comment.CommentID, login, base)
		return ack, nil, err
	}
	suggested, declaration, err := found.declarationWith(fromPaths, unexplained)
	if err != nil {
		return governance.OutboxMessage{}, nil, err
	}
	var defaultBranch string // the reply for the default branch links to that branch's editor
	if base == baseDefaultBranch {
		if defaultBranch, err = editBranch(view, base); err != nil {
			return governance.OutboxMessage{}, nil, err
		}
	}
	ack, err := newScopeNoticeAck(target, c.Comment.CommentID, login, view.pull.HeadRef, base, editableBranch(target, defaultBranch), found.Paths, declaration, suggested, found.Mixed, found.Submodules)
	if err != nil {
		return governance.OutboxMessage{}, nil, fmt.Errorf("%w: %w", errAnswerNotRenderable, err)
	}
	if base == baseDefaultBranch {
		return ack, nil, nil
	}
	reviews, err := scopeReview(ctx, j.reader, j.logger, "github suggest scope review", target, view.pull.HeadSHA, head, found, suggested)
	return ack, reviews, err
}

// editBranch is the branch of the file of base for the editor link: the pull request's head ref, or for the default branch the one the Suite targets.
func editBranch(view bootstrapView, base string) (string, error) {
	if base != baseDefaultBranch {
		return view.pull.HeadRef, nil
	}
	return view.targetBranch()
}

// defaultBranchTree reads the tree of the head of the Suite's target branch, the default branch.
func (j *commandsJob) defaultBranchTree(ctx context.Context, c commands.Candidate, view bootstrapView) (*githubTree, []inventory.Entry, error) {
	branch, err := view.targetBranch()
	if err != nil {
		return nil, nil, err
	}
	sha, err := j.reader.BranchHead(ctx, c.Repo, branch)
	if err != nil {
		return nil, nil, &opaque{what: "read the default branch head", cause: githubRead(err)}
	}
	return j.tree(ctx, c.Repo, sha)
}

// declarationMissing is the answer of a suggest before the first contract when no declaration exists to compare with: the suggestion of
// a propose that finds none, built from the head's tree or else the default branch's.
func declarationMissing(target commentTarget, c commands.Candidate, login, branch string, headEntries, mainEntries []inventory.Entry) (governance.OutboxMessage, error) {
	suggested := inventory.Suggest(headEntries)
	if len(suggested.Include)+len(suggested.Runner) == 0 {
		suggested = inventory.Suggest(mainEntries)
	}
	ack, err := newDeclarationMissingAck(target, c.Comment.CommentID, login, branch, suggested, commands.Suggested)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("%w: %w", errAnswerNotRenderable, err)
	}
	return ack, nil
}
