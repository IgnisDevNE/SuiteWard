//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance/governancetest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// TestPostgresConformance runs the shared persistence semantics against the
// PostgreSQL store. Every subtest gets its own freshly migrated schema, because
// operation ids repeat between subtests and history cannot be truncated.
func TestPostgresConformance(t *testing.T) {
	governancetest.RunConformance(t, func(t *testing.T) governancetest.Store {
		w := newPGWorld(t)
		for _, blob := range governancetest.ConformanceBlobs() {
			if err := w.content.Put(t.Context(), artifact.Hash(blob), bytes.NewReader(blob)); err != nil {
				t.Fatal(err)
			}
		}
		return &failingStore{Store: governancetest.Store(w.store), pool: w.pool}
	})
}

// failingStore adds the conformance suite's failure injection around the real
// store, so no test hook exists in production code. The injected error belongs
// to the next unit of work; its first write executes against PostgreSQL and
// then reports the failure, which the unit of work must roll back.
type failingStore struct {
	governancetest.Store
	pool *pgxpool.Pool
	mu   sync.Mutex
	next error
}

var (
	_ governancetest.FailNexter     = (*failingStore)(nil)
	_ governancetest.QueueInspector = (*failingStore)(nil)
)

// column returns one text column of every committed row, sorted.
func (s *failingStore) column(ctx context.Context, query string) ([]string, error) {
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s *failingStore) QueuedJobKinds(ctx context.Context) ([]string, error) {
	return s.column(ctx, "SELECT kind FROM river_job ORDER BY kind")
}

func (s *failingStore) OutboxKeys(ctx context.Context) ([]string, error) {
	return s.column(ctx, "SELECT key FROM outbox ORDER BY key")
}

func (s *failingStore) FailNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = err
}

func (s *failingStore) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	s.mu.Lock()
	injected := s.next
	s.next = nil
	s.mu.Unlock()
	return s.Store.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error {
		return fn(ctx, &failingTx{Tx: tx, failure: injected})
	})
}

type failingTx struct {
	governance.Tx
	failure error
}

// afterWrite reports the injected failure once, after a write has executed.
func (t *failingTx) afterWrite(err error) error {
	if err != nil || t.failure == nil {
		return err
	}
	failure := t.failure
	t.failure = nil
	return failure
}

func (t *failingTx) AppendConsent(ctx context.Context, write governance.ConsentWrite) error {
	return t.afterWrite(t.Tx.AppendConsent(ctx, write))
}

func (t *failingTx) Enqueue(ctx context.Context, job governance.Job) error {
	return t.afterWrite(t.Tx.Enqueue(ctx, job))
}

func (t *failingTx) Outbox(ctx context.Context, message governance.OutboxMessage) error {
	return t.afterWrite(t.Tx.Outbox(ctx, message))
}

func (t *failingTx) AppendProposalRevision(ctx context.Context, write governance.ProposalWrite) error {
	return t.afterWrite(t.Tx.AppendProposalRevision(ctx, write))
}

func (t *failingTx) RecordPromotion(ctx context.Context, write governance.PromotionWrite) error {
	return t.afterWrite(t.Tx.RecordPromotion(ctx, write))
}
