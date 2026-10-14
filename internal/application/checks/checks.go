// Package checks is the port of the PR check ledger: the current state of the
// SuiteWard / Contract check of every pull request and its publication
// generation (docs/contracts/persistence.md, "PR checks (M1.6b)"; D-CHECKS).
// The ledger records what was decided and published, never the authority:
// governance decides, and the check only reports.
package checks

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// ErrInvalid marks a result the ledger cannot record (it fails Validate).
var ErrInvalid = errors.New("checks: invalid result")

var (
	headSyntax      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	referenceSyntax = regexp.MustCompile(`^(P[0-9]+-R[0-9]+)?$`)
)

// State is the conclusion published for a head.
type State string

// The three states (D-CHECKS); neutral and skipped are never published.
const (
	Success    State = "success"
	InProgress State = "in_progress"
	Failure    State = "failure"
)

// Result is one decided check state for the head of a pull request.
type Result struct {
	RepositoryID int64
	PullNumber   int
	HeadSHA      string
	State        State
	Reason       string // a stable code, see the runtime contract
	Reference    string // "P<n>-R<m>" or empty
	Change       string // the classification code, or empty when not assessed
	Revision     int64  // the Suite revision of the verdict, 0 without a Suite
	CommandID    int64  // the latest owner command on the pull that was considered, 0 for none
}

// Record is a stored Result with its publication bookkeeping.
type Record struct {
	Result
	HeadSince  time.Time // when this head was first recorded
	Generation int64     // > 0
	DecidedAt  time.Time
}

// Store is the check ledger. Implementations run the shared conformance
// suite in checkstest.
type Store interface {
	Current(ctx context.Context, repositoryID int64, pull int) (Record, bool, error)
	// Publish stores r for its pull. When the head, State, Reason or Reference differ from the current record
	// (or there is none) it advances the generation and writes message(generation) to the outbox in the same
	// transaction, as a system row. Otherwise it updates Change, Revision, CommandID and DecidedAt only and
	// writes no message. It also advances the generation and writes a message when the current generation's
	// message ended `failed` in the outbox, so a lost publication is retried. It returns the stored record and
	// whether a message was written.
	Publish(ctx context.Context, r Result, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (Record, bool, error)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// ValidatePull reports why no pull request has these ids, as ErrInvalid.
func ValidatePull(repositoryID int64, pull int) error {
	if repositoryID <= 0 || pull < 1 || pull > math.MaxInt32 {
		return invalid("the repository id must be positive and the pull number between 1 and %d", math.MaxInt32)
	}
	return nil
}

// Validate reports why the result cannot be recorded, as ErrInvalid.
func (r Result) Validate() error {
	if err := ValidatePull(r.RepositoryID, r.PullNumber); err != nil {
		return err
	}
	switch {
	case !headSyntax.MatchString(r.HeadSHA):
		return invalid("the head must be 40 lowercase hexadecimal digits")
	case r.State != Success && r.State != InProgress && r.State != Failure:
		return invalid("unknown state %q", r.State)
	case !referenceSyntax.MatchString(r.Reference):
		return invalid("the reference %q is not P<n>-R<m>", r.Reference)
	case r.Revision < 0 || r.CommandID < 0:
		return invalid("the revision and the command id must not be negative")
	}
	return nil
}

// ValidatePublish reports why Publish cannot record r at time at, as ErrInvalid.
func ValidatePublish(r Result, at time.Time) error {
	if at.IsZero() {
		return invalid("the decision time is required")
	}
	return r.Validate()
}

// Next is the record stored when r is published at time at over current (found false: there is none) and
// whether it carries a new generation, hence a new message: when the head, state, reason or reference changed,
// or when lost says the current generation's message ended failed. Otherwise only Change, Revision, CommandID
// and the decision time move. Times are kept at microseconds, all PostgreSQL stores.
func Next(current Record, found bool, r Result, at time.Time, lost bool) (next Record, advance bool) {
	at = at.UTC().Truncate(time.Microsecond)
	if !found {
		return Record{Result: r, HeadSince: at, Generation: 1, DecidedAt: at}, true
	}
	next = Record{Result: r, HeadSince: current.HeadSince, Generation: current.Generation, DecidedAt: at}
	if r.HeadSHA != current.HeadSHA {
		next.HeadSince = at
	}
	if lost || r.HeadSHA != current.HeadSHA || r.State != current.State || r.Reason != current.Reason || r.Reference != current.Reference {
		next.Generation++
		advance = true
	}
	return next, advance
}
