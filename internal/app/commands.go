package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

const (
	commandBatch       = 100              // candidates one run takes from the ledger
	commandSkew        = 30 * time.Second // clock skew the watermark allows, on top of the job timeout (D-COMMANDS)
	commandTolerance   = 1                // TOTP steps either side of the comment's creation (D-COMMANDS)
	maxCommandFailures = 5                // consecutive failed attempts of one candidate before it is recorded as failed
)

// errLedger marks a failure to read or write the command ledger: the only failure that fails the job.
var errLedger = errors.New("command ledger failed")

// commandsJob is the github_commands job: it decides the owner's PR command comments one at a time, in the order of
// D-COMMANDS, once every enrolled repository has synchronized past them (docs/contracts/runtime.md, "Command and
// bootstrap jobs (M1.6a)"). Every step is safe to repeat after a crash.
type commandsJob struct {
	ledger     commands.Store
	owners     owner.Store
	suites     governance.UnitOfWork
	syncs      repositoryReader // the observation store: each repository's last successful cycle
	repos      []string         // the configured repositories (GITHUB_REPOSITORIES)
	bot        *botAccount
	jobTimeout time.Duration
	logger     *slog.Logger
	now        func() time.Time

	failing commandFailure // the candidate that failed last, in this process; runs never overlap
}

// commandFailure counts the consecutive failed attempts of one candidate.
type commandFailure struct {
	repository, comment int64
	attempts            int
}

// run decides the pending comments up to the watermark. A candidate whose decision fails stops the run, so a later one
// never goes ahead of it; the next run retries it, and its fifth consecutive failure records it as failed. Only a
// ledger failure fails the job.
func (j *commandsJob) run(ctx context.Context) error {
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
		j.logger.WarnContext(ctx, "github commands stopped: the bot account could not be resolved")
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
		if err := j.record(ctx, candidate, commands.Failed, "", nil); err != nil {
			return err
		}
		j.logger.ErrorContext(ctx, "github command recorded as failed", "repo", candidate.Repo, "comment", candidate.Comment.CommentID, "error", failure.Error())
		j.failing = commandFailure{}
	}
	return nil
}

// horizon is the sync watermark (D-COMMANDS): the earliest last successful cycle among the enrolled repositories,
// minus the job timeout and the clock skew. ready is false, with a Warn line (every command waits), while an enrolled repository has no
// successful cycle: nothing can be decided before it has been seen.
func (j *commandsJob) horizon(ctx context.Context, enrollment owner.State) (horizon time.Time, ready bool, err error) {
	var earliest time.Time
	for _, project := range enrollment.Projects {
		state, _, err := j.syncs.Repository(ctx, project.Repo)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("read the synchronization state of %s: %w", project.Repo, err)
		}
		if state.LastSuccess.IsZero() {
			j.logger.WarnContext(ctx, "github commands wait: an enrolled repository has not synchronized yet", "repo", project.Repo)
			return time.Time{}, false, nil
		}
		if earliest.IsZero() || state.LastSuccess.Before(earliest) {
			earliest = state.LastSuccess
		}
	}
	return earliest.Add(-(j.jobTimeout + commandSkew)), true, nil
}

// record writes the decision for the candidate, with its acknowledgment in the same transaction. Its error is a ledger failure.
func (j *commandsJob) record(ctx context.Context, c commands.Candidate, d commands.Disposition, operation contract.OperationID, ack *governance.OutboxMessage) error {
	m := c.Comment
	entry := commands.Entry{RepositoryID: c.RepositoryID, CommentID: m.CommentID, PullNumber: c.PullNumber, AuthorID: m.AuthorID, CreatedAt: m.CreatedAt,
		BodyDigest: m.BodyDigest, Disposition: d, Operation: operation, DecidedAt: j.now()}
	if err := j.ledger.Record(ctx, entry, ack); err != nil {
		return &opaque{what: "record the command", cause: errors.Join(errLedger, err)}
	}
	return nil
}

// decide applies the disposition steps of the contract to one candidate, in order. Its errors never carry comment text
// or a code: the log and river_job.errors keep only the step that failed.
func (j *commandsJob) decide(ctx context.Context, enrollment owner.State, botID int64, c commands.Candidate) error {
	m := c.Comment
	if m.CreatedAt.Before(enrollment.EnrolledAt) {
		return j.record(ctx, c, commands.BeforeEnrollment, "", nil)
	}
	parsed := commandtext.Parse(m.Body)
	if m.AuthorID == botID || parsed.Kind == commandtext.KindNotCommand {
		return j.record(ctx, c, commands.NotCommand, "", nil)
	}
	if m.AuthorID != enrollment.Owner.AccountID {
		return j.record(ctx, c, commands.IgnoredAuthor, "", nil)
	}
	target := commentTarget{RepositoryID: c.RepositoryID, Repo: c.Repo, Pull: c.PullNumber}
	reject := func(d commands.Disposition, reason commandtext.Reason, reference string) error {
		ack, err := newRejectedAck(target, m.CommentID, enrollment.Owner.Login, d, reason, reference)
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
	command := parsed.Command
	reference := commandtext.Reference(command.Proposal, command.Revision)
	step, err := totp.Verify(enrollment.Secret, command.Code, m.CreatedAt, commandTolerance)
	if errors.Is(err, totp.ErrInvalidCode) {
		return reject(commands.InvalidCode, 0, reference)
	}
	if err != nil {
		return &opaque{what: "verify the code", cause: err}
	}
	source := commentSource(m.CommentID)
	// The comment's own time keeps a retry after a crash identical to the first call.
	switch err := j.owners.ConsumeStep(ctx, enrollment.Owner.AccountID, step, m.CreatedAt, source); {
	case errors.Is(err, owner.ErrStepUsed):
		return reject(commands.ReplayedCode, 0, reference)
	case err != nil:
		return &opaque{what: "consume the step", cause: err}
	}
	project, operation := owner.ProjectID(c.RepositoryID), contract.OperationID("consent:"+string(source))
	ack, err := newProcessedAck(target, m.CommentID, enrollment.Owner.Login, project, defaultSuite, operation)
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
