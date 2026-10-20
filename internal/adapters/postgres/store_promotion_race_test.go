//go:build integration

package postgres_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// mergeDeliveries is how many times each contender's merge is delivered.
const mergeDeliveries = 2

// mergeRaceCounts are the row counts that a merge race changes.
type mergeRaceCounts struct {
	revision                                        contract.StateRevision
	versions, promotions, operations, mergeOutcomes int
}

func (w *pgWorld) mergeRaceCounts() mergeRaceCounts {
	return mergeRaceCounts{w.revision(), w.count("suite_versions"), w.count("promotions"), w.count("operations"), w.count("pr_merges")}
}

// deliverMerges delivers the merge of every contender mergeDeliveries times, all at the same moment, and returns the
// results of contender i at [i*mergeDeliveries, (i+1)*mergeDeliveries).
func deliverMerges(t *testing.T, w *pgWorld, contenders []candidate, trees []mapTree) []governance.MergeResult {
	t.Helper()
	results := make([]governance.MergeResult, len(contenders)*mergeDeliveries)
	failures := make([]error, len(results))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range results {
		c, tree := contenders[i/mergeDeliveries], trees[i/mergeDeliveries]
		workers.Go(func() {
			<-start
			results[i], failures[i] = governance.PromoteMerge(t.Context(), w.store, w.content, c.mergeRequest(tree))
		})
	}
	close(start)
	workers.Wait()
	for i, err := range failures {
		if err != nil {
			t.Fatalf("delivery %d of %s: %v", i%mergeDeliveries, contenders[i/mergeDeliveries].id, err)
		}
	}
	return results
}

// requireOneWinner checks that the deliveries of each contender agree and are one fresh outcome and replays, and
// returns the indexes of the winner and the loser.
func requireOneWinner(t *testing.T, contenders []candidate, results []governance.MergeResult) (winner, loser int) {
	t.Helper()
	winner, loser = -1, -1
	for i, c := range contenders {
		fresh, replays := 0, 0
		for _, result := range results[i*mergeDeliveries : (i+1)*mergeDeliveries] {
			if result.Duplicate {
				replays++
			} else {
				fresh++
			}
			if result.Kind != results[i*mergeDeliveries].Kind || result.Version != results[i*mergeDeliveries].Version {
				t.Fatalf("the deliveries of %s disagree: %+v", c.id, results[i*mergeDeliveries:(i+1)*mergeDeliveries])
			}
		}
		if fresh != 1 || replays != mergeDeliveries-1 {
			t.Fatalf("%s: %d fresh outcomes and %d replays; want 1 and %d", c.id, fresh, replays, mergeDeliveries-1)
		}
		switch outcome := results[i*mergeDeliveries].MergeOutcome; {
		case outcome.Kind == governance.MergePromoted && outcome.Version == "V2":
			if winner != -1 {
				t.Fatalf("two pull requests won: %+v", results)
			}
			winner = i
		case outcome.Kind == governance.MergeBlocked && outcome.Reason == governance.MergeReasonCanonicalChanged && outcome.Version == "V2" && outcome.Reference != nil:
			loser = i
		default:
			t.Fatalf("%s ended with %+v; want one promotion to V2 and one block because the canonical changed", c.id, outcome)
		}
	}
	if winner == -1 || loser == -1 {
		t.Fatalf("results %+v; want exactly one winner and one loser", results)
	}
	return winner, loser
}

// requireOnlyTheWinnerPromoted checks the rows after the race: the winner wrote an assessment and a promotion, the
// loser an assessment, and the outcomes advance nothing.
func requireOnlyTheWinnerPromoted(t *testing.T, w *pgWorld, before mergeRaceCounts, won, lost candidate) {
	t.Helper()
	after := w.mergeRaceCounts()
	if after != (mergeRaceCounts{before.revision + 3, before.versions + 1, before.promotions + 1, before.operations + 1, before.mergeOutcomes + 2}) {
		t.Fatalf("revision %d (was %d), %d versions (was %d), %d promotions (was %d), %d operations (was %d), %d outcomes (was %d); want one promotion's rows and two outcomes",
			after.revision, before.revision, after.versions, before.versions, after.promotions, before.promotions, after.operations, before.operations, after.mergeOutcomes, before.mergeOutcomes)
	}
	if got := w.currentVersion(); got != "V2" {
		t.Fatalf("current version %q; want V2", got)
	}
	w.read(func(ctx context.Context, tx governance.Tx) error {
		if _, found, err := tx.Receipt(ctx, contract.OperationID("merge:"+string(lost.carrier))); err != nil || found {
			t.Fatalf("the blocked pull request left a promotion receipt: found=%v err=%v", found, err)
		}
		if _, found, err := tx.PromotionFor(ctx, lost.current()); err != nil || found {
			t.Fatalf("the blocked pull request's revision was promoted: found=%v err=%v", found, err)
		}
		if _, found, err := tx.Version(ctx, "V3"); err != nil || found {
			t.Fatalf("a second version was written: found=%v err=%v", found, err)
		}
		if record, found, err := tx.PromotionFor(ctx, won.current()); err != nil || !found || record.VersionID() != "V2" {
			t.Fatalf("the winner's promotion = %+v found=%v err=%v", record, found, err)
		}
		return nil
	})
}

// TestTwoApprovedPullRequestsRacingToMergeRecordOneVersion merges two approved pull requests, both bound to V1, at the
// same moment, each delivered twice. The Suite lock lets one commit; the other finds the canonical moved and is
// blocked, and a redelivery answers from the recorded outcome.
func TestTwoApprovedPullRequestsRacingToMergeRecordOneVersion(t *testing.T) {
	for round := range 5 {
		t.Run("round "+strconv.Itoa(round+1), func(t *testing.T) {
			w := newPGWorld(t)
			base := w.treeCandidate("p0", "", treeOf("baseline"))
			trees := []mapTree{treeOf("first change"), treeOf("second change")}
			contenders := []candidate{w.treeCandidate("p1", "V1", trees[0]), w.treeCandidate("p2", "V1", trees[1])}
			w.seedMergeBaseline(base, contenders...)
			for _, c := range contenders {
				w.approve(c)
			}
			before := w.mergeRaceCounts()

			results := deliverMerges(t, w, contenders, trees)
			winner, loser := requireOneWinner(t, contenders, results)
			requireOnlyTheWinnerPromoted(t, w, before, contenders[winner], contenders[loser])
		})
	}
}
