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
			revision, versions, promotions, operations, merges := w.revision(), w.count("suite_versions"), w.count("promotions"), w.count("operations"), w.count("pr_merges")

			const deliveries = 2
			results := make([]governance.MergeResult, len(contenders)*deliveries)
			failures := make([]error, len(results))
			start := make(chan struct{})
			var workers sync.WaitGroup
			for i := range results {
				c, tree := contenders[i/deliveries], trees[i/deliveries]
				workers.Go(func() {
					<-start
					results[i], failures[i] = governance.PromoteMerge(t.Context(), w.store, w.content, c.mergeRequest(tree))
				})
			}
			close(start)
			workers.Wait()
			for i, err := range failures {
				if err != nil {
					t.Fatalf("delivery %d of %s: %v", i%deliveries, contenders[i/deliveries].id, err)
				}
			}

			winner, loser := -1, -1
			for i, c := range contenders {
				fresh, replays := 0, 0
				for _, result := range results[i*deliveries : (i+1)*deliveries] {
					if result.Duplicate {
						replays++
					} else {
						fresh++
					}
					if result.Kind != results[i*deliveries].Kind || result.Version != results[i*deliveries].Version {
						t.Fatalf("the deliveries of %s disagree: %+v", c.id, results[i*deliveries:(i+1)*deliveries])
					}
				}
				if fresh != 1 || replays != deliveries-1 {
					t.Fatalf("%s: %d fresh outcomes and %d replays; want 1 and %d", c.id, fresh, replays, deliveries-1)
				}
				switch outcome := results[i*deliveries].MergeOutcome; {
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
			won, lost := contenders[winner], contenders[loser]

			// The winner writes an assessment and a promotion, the loser an assessment; the outcomes advance nothing.
			if w.revision() != revision+3 || w.count("suite_versions") != versions+1 || w.count("promotions") != promotions+1 || w.count("operations") != operations+1 || w.count("pr_merges") != merges+2 {
				t.Fatalf("revision %d (was %d), %d versions (was %d), %d promotions (was %d), %d operations (was %d), %d outcomes (was %d); want one promotion's rows and two outcomes",
					w.revision(), revision, w.count("suite_versions"), versions, w.count("promotions"), promotions, w.count("operations"), operations, w.count("pr_merges"), merges)
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
		})
	}
}
