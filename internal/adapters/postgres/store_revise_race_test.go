//go:build integration

package postgres_test

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// TestConcurrentProposalRevisionsKeepOneGaplessSequence has two goroutines
// revise one proposal at the same moment, with distinct revision ids over the
// same base. The Suite lock serializes them: every revision is stored once
// and the sequence has no duplicate and no gap.
func TestConcurrentProposalRevisionsKeepOneGaplessSequence(t *testing.T) {
	for _, existing := range []bool{true, false} {
		name := "new proposal"
		if existing {
			name = "existing proposal"
		}
		t.Run(name, func(t *testing.T) {
			for round := range 5 {
				t.Run("round "+strconv.Itoa(round+1), func(t *testing.T) {
					w := newPGWorld(t)
					c := newCandidate("p1", "", w.protected("first"))
					if existing {
						w.seedSuite(fxProject, fxSuite, c)
					} else {
						w.seedSuite(fxProject, fxSuite)
					}
					wantRevisions := 2
					if existing {
						wantRevisions = 3
					}
					before, revisions := w.revision(), w.count("proposal_revisions")

					// Different coverage per revision, so neither is an unchanged repeat of the other.
					next := []candidate{
						newCandidate("p1", "", w.protected("second"), "revision-a"),
						newCandidate("p1", "", w.protected("third"), "revision-b"),
					}
					results := make([]governance.ReviseResult, len(next))
					failures := make([]error, len(next))
					start := make(chan struct{})
					var workers sync.WaitGroup
					for i, n := range next {
						workers.Go(func() {
							<-start
							results[i], failures[i] = governance.ReviseProposal(t.Context(), w.store, governance.ReviseRequest{Revision: n.proposal.Current()})
						})
					}
					close(start)
					workers.Wait()
					for i, err := range failures {
						if err != nil {
							t.Fatalf("revision %s: %v", next[i].current().RevisionID, err)
						}
						if !results[i].Committed {
							t.Fatalf("revision %s was not committed: %+v", next[i].current().RevisionID, results[i])
						}
					}

					if got := w.revision(); got != before+2 {
						t.Fatalf("suite revision = %d, want %d", got, before+2)
					}
					if got := w.count("proposal_revisions"); got != revisions+2 {
						t.Fatalf("stored revisions = %d, want %d", got, revisions+2)
					}
					rows, err := w.pool.Query(t.Context(), "SELECT seq FROM proposal_revisions WHERE proposal_id = 'p1' ORDER BY seq")
					if err != nil {
						t.Fatal(err)
					}
					seqs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
					if err != nil {
						t.Fatal(err)
					}
					var want []int64
					for i := range wantRevisions {
						want = append(want, int64(i+1))
					}
					if !slices.Equal(seqs, want) {
						t.Fatalf("sequence numbers = %v, want %v", seqs, want)
					}
					w.read(func(ctx context.Context, tx governance.Tx) error {
						proposal, _, err := tx.Proposal(ctx, "p1")
						if err != nil {
							return err
						}
						for _, n := range next {
							if _, err := proposal.Lookup(n.current(), n.carrier); err != nil {
								t.Errorf("revision %s is not stored: %v", n.current().RevisionID, err)
							}
						}
						return nil
					})
				})
			}
		})
	}
}
