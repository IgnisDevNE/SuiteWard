// Package commands is the port of the PR command ledger: the first observed
// version of every PR comment SuiteWard considered, and what it decided
// (docs/contracts/persistence.md, "PR commands and bootstrap"; D-COMMANDS,
// D-COMMENT-EDIT). The ledger is the record of that decision, never the
// authority: consent lives in the governance tables.
package commands

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var (
	// ErrConflict marks an entry for a comment that already has another entry.
	ErrConflict = errors.New("commands: the comment already has another ledger entry")
	// ErrInvalid marks an entry the ledger cannot record (it fails Validate) and a Pending request without a horizon or a positive limit.
	ErrInvalid = errors.New("commands: invalid request")
)

// Disposition is what SuiteWard decided about one comment.
type Disposition string

// The dispositions (D-COMMANDS).
const (
	NotCommand       Disposition = "not-command"       // no /suiteward line, or SuiteWard's own comment
	BeforeEnrollment Disposition = "before-enrollment" // created before the current enrollment's enrolled_at; never acknowledged
	NoSuite          Disposition = "no-suite"          // owner-authored command for a project whose Suite does not exist yet
	EditedFirst      Disposition = "edited-first"      // owner-authored, first observed version already edited (updated_at != created_at); no step used
	Failed           Disposition = "failed"            // a candidate that kept failing (bounded retries, logged); never acknowledged
	IgnoredAuthor    Disposition = "ignored-author"    // a /suiteward command from an account other than the owner
	Malformed        Disposition = "malformed"         // owner-authored, unparseable command line
	InvalidCode      Disposition = "invalid-code"      // owner-authored, code does not verify within the tolerance
	ReplayedCode     Disposition = "replayed-code"     // owner-authored, the step was used by another command
	UnknownProposal  Disposition = "unknown-proposal"  // owner-authored, valid code, for a proposal that does not exist on an existing Suite
	Processed        Disposition = "processed"         // passed to ProcessConsent; the receipt holds the outcome
	Proposed         Disposition = "proposed"          // owner-authored /suiteward propose that created or reused a bootstrap proposal; no operation id
	ProposeRefused   Disposition = "propose-refused"   // owner-authored /suiteward propose that was refused; no operation id
	Skipped          Disposition = "skipped"           // owner-authored /suiteward skip with a good code: the PR may merge before the first contract; no operation id
	SkipRefused      Disposition = "skip-refused"      // owner-authored /suiteward skip that was refused; no operation id
	Suggested        Disposition = "suggested"         // owner-authored /suiteward suggest that was answered, or that found no Suite yet (the acknowledgment says the bootstrap is pending); no operation id, never a command for the check
	SuggestRefused   Disposition = "suggest-refused"   // owner-authored /suiteward suggest on a fork or a closed pull request, or from a source it cannot answer from; always acknowledged; no operation id, never a command for the check
)

var dispositions = []Disposition{NotCommand, BeforeEnrollment, NoSuite, EditedFirst, Failed, IgnoredAuthor, Malformed, InvalidCode, ReplayedCode, UnknownProposal, Processed, Proposed, ProposeRefused, Skipped, SkipRefused, Suggested, SuggestRefused}

// Dispositions returns every disposition.
func Dispositions() []Disposition { return slices.Clone(dispositions) }

// Entry is the decision about the first observed version of one comment.
type Entry struct {
	RepositoryID int64
	CommentID    int64
	PullNumber   int
	AuthorID     int64
	CreatedAt    time.Time
	BodyDigest   artifact.Digest // of the first observed version
	Disposition  Disposition
	Operation    contract.OperationID // set when, and only when, Processed
	DecidedAt    time.Time
}

// Candidate is the first observed version of one comment.
type Candidate struct {
	RepositoryID int64
	Repo         string // owner/name, for the GitHub calls
	PullNumber   int
	Comment      githubsync.CommentVersion
}

// Store is the command ledger. Implementations run the shared conformance
// suite in commandstest.
type Store interface {
	// Pending returns the first observed version of every comment that has no ledger entry, across all enrolled
	// repositories, ordered by created_at then comment id, at most limit, and only comments created before
	// horizon. The caller computes horizon from the sync watermark (D-COMMANDS): the earliest last successful
	// cycle among the enrolled repositories, minus JOB_TIMEOUT and 30 s.
	Pending(ctx context.Context, horizon time.Time, limit int) ([]Candidate, error)
	// Record stores the decision for one comment and, when ack is not nil, its acknowledgment outbox message in
	// the same transaction (for dispositions that never reach ProcessConsent). That message is a system outbox
	// row with no project or Suite, written the way EnqueueSystem writes the probe's. A second Record of the same
	// (repository, comment) with the same entry is success and writes nothing; with another entry it is
	// ErrConflict. Entries are never updated or deleted.
	Record(ctx context.Context, e Entry, ack *governance.OutboxMessage) error
	// LatestOwnerCommand returns the entry of the command with the greatest comment id on the pull among the
	// owner's ledger entries whose disposition can set or clear the check (invalid-code, replayed-code, processed;
	// see AffectsCheck), only those below the comment id before when it is not 0. Other dispositions never count,
	// so a wrong command that is no wrong code, and an ordinary comment, never hide an earlier one.
	// ErrInvalid for a non-positive id, a negative before or a pull out of range.
	LatestOwnerCommand(ctx context.Context, repositoryID int64, pull int, ownerID int64, before int64) (Entry, bool, error)
	// Skipped reports whether the pull has a `skipped` entry whose comment was created at or after since (the current
	// enrollment's enrolled_at). ErrInvalid for a non-positive id or a pull out of range.
	Skipped(ctx context.Context, repositoryID int64, pull int, since time.Time) (bool, error)
	// Instance returns this installation's random identifier, created by migration 00013 and never changed.
	Instance(ctx context.Context) (string, error)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Validate reports why the entry cannot be recorded, as ErrInvalid.
func (e Entry) Validate() error {
	switch {
	case e.RepositoryID <= 0 || e.CommentID <= 0 || e.AuthorID <= 0:
		return invalid("the repository, comment and author ids must be positive")
	case e.PullNumber < 1 || e.PullNumber > math.MaxInt32:
		return invalid("pull number %d is outside 1 to %d", e.PullNumber, math.MaxInt32)
	case e.CreatedAt.IsZero() || e.DecidedAt.IsZero():
		return invalid("the creation and decision times are required")
	case e.BodyDigest.IsZero():
		return invalid("the body digest is required")
	case !slices.Contains(dispositions, e.Disposition):
		return invalid("unknown disposition %q", e.Disposition)
	case (e.Disposition == Processed) != (e.Operation != ""):
		return invalid("an operation is required for the disposition %q and only for it", Processed)
	}
	return nil
}

// ValidateLatest reports why LatestOwnerCommand cannot answer, as ErrInvalid.
func ValidateLatest(repositoryID int64, pull int, ownerID, before int64) error {
	if repositoryID <= 0 || ownerID <= 0 || before < 0 || pull < 1 || pull > math.MaxInt32 {
		return invalid("the repository and owner ids must be positive, before not negative, and the pull number between 1 and %d", math.MaxInt32)
	}
	return nil
}

// ValidateSkipped reports why Skipped cannot answer, as ErrInvalid.
func ValidateSkipped(repositoryID int64, pull int) error {
	if repositoryID <= 0 || pull < 1 || pull > math.MaxInt32 {
		return invalid("the repository id must be positive and the pull number between 1 and %d", math.MaxInt32)
	}
	return nil
}

// AffectsCheck reports whether the disposition can set or clear the check of the pull request: a wrong code sets
// it, a processed command clears it. Every other wrong command has no effect (D-COMMANDS, "A wrong command has no effect").
func (d Disposition) AffectsCheck() bool {
	switch d {
	case InvalidCode, ReplayedCode, Processed:
		return true
	}
	return false
}

// ValidatePending reports why Pending cannot answer, as ErrInvalid.
func ValidatePending(horizon time.Time, limit int) error {
	if horizon.IsZero() || limit < 1 {
		return invalid("Pending needs a horizon and a positive limit")
	}
	return nil
}

// AtStoredPrecision is the entry with its times at microsecond precision, which is all PostgreSQL stores: a retry
// that carries the clock's nanoseconds is still the same entry.
func (e Entry) AtStoredPrecision() Entry {
	e.CreatedAt, e.DecidedAt = e.CreatedAt.Truncate(time.Microsecond), e.DecidedAt.Truncate(time.Microsecond)
	return e
}
