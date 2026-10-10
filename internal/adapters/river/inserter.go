// Package river runs background work on River: the job inserter used inside
// governance units of work, the dispatcher worker, the outbox relay and the
// diagnostic probe.
package river

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	riverqueue "github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// dynamicArgs carries a job whose kind and JSON arguments are chosen at run
// time, so one dispatcher can serve every registered kind.
type dynamicArgs struct {
	kind string
	args json.RawMessage
}

func (a dynamicArgs) Kind() string                 { return a.kind }
func (a dynamicArgs) MarshalJSON() ([]byte, error) { return a.args, nil }

// Inserter inserts jobs through a caller's transaction with an insert-only
// River client, so inserting does not depend on which workers a process runs.
type Inserter struct {
	client *riverqueue.Client[pgx.Tx]
}

// NewInserter returns an Inserter whose jobs get maxAttempts attempts.
func NewInserter(maxAttempts int) (*Inserter, error) {
	client, err := riverqueue.NewClient(riverpgxv5.New(nil), &riverqueue.Config{MaxAttempts: maxAttempts})
	if err != nil {
		return nil, fmt.Errorf("create the River insert client: %w", err)
	}
	return &Inserter{client: client}, nil
}

// Insert adds job to tx and returns its id. The job runs only when tx commits.
func (i *Inserter) Insert(ctx context.Context, tx pgx.Tx, job governance.Job) (int64, error) {
	result, err := i.client.InsertTx(ctx, tx, dynamicArgs{kind: job.Kind, args: job.Args}, &riverqueue.InsertOpts{ScheduledAt: job.ScheduledAt})
	if err != nil {
		return 0, fmt.Errorf("insert job %q: %w", job.Kind, err)
	}
	return result.Job.ID, nil
}
