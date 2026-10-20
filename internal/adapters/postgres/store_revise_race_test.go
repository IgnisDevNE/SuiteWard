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
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const headRaceCarrier = contract.ApprovalCarrierID("github-pr:1/2")

// headRaceHead is one head of the pull request and the tree it carries.
type headRaceHead struct {
	head string
	tree mapTree
}

func assessHeadOfRace(t *testing.T, w *pgWorld, head string, tree mapTree) (governance.HeadResult, error) {
	t.Helper()
	return governance.AssessHead(t.Context(), w.store, w.content, w.content, governance.HeadRequest{Project: fxProject, Suite: fxSuite, Carrier: headRaceCarrier,
		Head: contract.SourceRevision(head), Tree: tree, Revision: noRevision})
}

// assessHeadsTogether assesses every head at the same moment and returns their results, in the order of heads.
func assessHeadsTogether(t *testing.T, w *pgWorld, heads []headRaceHead) []governance.HeadResult {
	t.Helper()
	results := make([]governance.HeadResult, len(heads))
	failures := make([]error, len(heads))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i, h := range heads {
		workers.Go(func() {
			<-start
			results[i], failures[i] = assessHeadOfRace(t, w, h.head, h.tree)
		})
	}
	close(start)
	workers.Wait()
	for i, err := range failures {
		if err != nil {
			t.Fatalf("head %s: %v", heads[i].head, err)
		}
		if results[i].Reference == nil {
			t.Fatalf("head %s has no proposal revision: %+v", heads[i].head, results[i])
		}
	}
	return results
}

// requireGaplessSequence checks the revision, the stored revisions and the sequence numbers of the proposal after the
// race, that every head's revision and assessment is stored, and that the heads got different revisions.
func requireGaplessSequence(t *testing.T, w *pgWorld, heads []headRaceHead, results []governance.HeadResult, before contract.StateRevision, revisions, wantRevisions int) {
	t.Helper()
	if *results[0].Reference == *results[1].Reference {
		t.Fatalf("both heads got the revision %v", results[0].Reference)
	}

	// A revision and an assessment per head.
	if got := w.revision(); got != before+4 {
		t.Fatalf("suite revision = %d, want %d", got, before+4)
	}
	if got := w.count("proposal_revisions"); got != revisions+2 {
		t.Fatalf("stored revisions = %d, want %d", got, revisions+2)
	}
	proposalID := results[0].Reference.ProposalID
	rows, err := w.pool.Query(t.Context(), "SELECT seq FROM proposal_revisions WHERE proposal_id = $1 ORDER BY seq", string(proposalID))
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
		t.Fatalf("sequence numbers of %s = %v, want %v", proposalID, seqs, want)
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, proposalID)
		if err != nil {
			return err
		}
		for i, result := range results {
			if _, err := proposal.Lookup(*result.Reference, headRaceCarrier); err != nil {
				t.Errorf("revision %v of head %s is not stored: %v", result.Reference, heads[i].head, err)
			}
			if _, found, err := tx.Assessment(ctx, *result.Reference, contract.SourceRevision(heads[i].head)); err != nil || !found {
				t.Errorf("assessment of head %s: found=%v err=%v", heads[i].head, found, err)
			}
		}
		return nil
	})
}

// assessHeadsInOneRound runs one round of the race on a fresh world, for a pull request that has a change proposal
// already (existing) or not.
func assessHeadsInOneRound(t *testing.T, existing bool) {
	w := newPGWorld(t)
	w.seedMergeBaseline(w.treeCandidate("p0", "", treeOf("baseline")))
	wantRevisions := 2
	if existing {
		if _, err := assessHeadOfRace(t, w, "head-0", treeOf("first")); err != nil {
			t.Fatal(err)
		}
		wantRevisions = 3
	}
	before, revisions := w.revision(), w.count("proposal_revisions")

	// Different coverage per head, so neither is an unchanged repeat of the other.
	heads := []headRaceHead{{"head-a", treeOf("second")}, {"head-b", treeOf("third")}}
	results := assessHeadsTogether(t, w, heads)
	requireGaplessSequence(t, w, heads, results, before, revisions, wantRevisions)
}

// TestConcurrentHeadAssessmentsKeepOneGaplessSequence has two heads of one pull request, with different protected
// content, assessed at the same moment, for a pull request that has a change proposal already and for one that has not.
// The Suite lock serializes them: each head gets its own revision and assessment, and the sequence of the proposal has
// no duplicate and no gap.
func TestConcurrentHeadAssessmentsKeepOneGaplessSequence(t *testing.T) {
	for _, existing := range []bool{true, false} {
		name := "new proposal"
		if existing {
			name = "existing proposal"
		}
		t.Run(name, func(t *testing.T) {
			for round := range 5 {
				t.Run("round "+strconv.Itoa(round+1), func(t *testing.T) { assessHeadsInOneRound(t, existing) })
			}
		})
	}
}
