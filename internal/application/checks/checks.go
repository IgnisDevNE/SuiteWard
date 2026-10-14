// Package checks is the port of the PR check ledger: the current state of the
// SuiteWard / Contract check of every pull request and its publication
// generation (docs/contracts/persistence.md, "PR checks (M1.6b)"; D-CHECKS).
// The ledger records what was decided and published, never the authority:
// governance decides, and the check only reports.
package checks

import (
	"context"
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// ErrInvalid marks a result the ledger cannot record (it fails Validate).
var ErrInvalid = errors.New("checks: invalid result")

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

// Validate reports why the result cannot be recorded, as ErrInvalid.
func (r Result) Validate() error {
	return nil
}
