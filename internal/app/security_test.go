package app

import (
	"context"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// receiptOf is the consent receipt the commands job wrote for the comment, when there is one.
func (w *cjWorld) receiptOf(comment int64) (governance.ConsentReceipt, bool) {
	w.t.Helper()
	var receipt governance.ConsentReceipt
	var found bool
	err := w.mem.Do(w.t.Context(), owner.ProjectID(cjID1), "default", func(ctx context.Context, tx governance.Tx) error {
		stored, ok, err := tx.Receipt(ctx, cjOperation(comment))
		if ok {
			receipt, found = *stored.Consent, true
		}
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return receipt, found
}

// An approve or a revoke names a proposal by id, and the pull request that hosts the comment is the carrier: P1 is carried by
// pull request 7, so the command on pull request 5 is refused before its step is used, with an acknowledgment that names the
// reference and says where to comment. It is no consent and, like any wrong command, no change to the check.
func TestACommandOnAnotherPullRequestThanTheProposalCarrierIsRefusedBeforeTheStep(t *testing.T) {
	for _, verb := range []string{"approve", "revoke"} {
		t.Run(verb, func(t *testing.T) {
			w := newCjWorld(t)
			comment := ownerCommand(500, cjAt(10), verb, "P1-R2", 0)
			comment.pull = 5
			w.syncAll([]cjComment{comment})
			w.mustRun()

			w.requireRejected(cjID1, 500, commands.OtherPull, 0, "P1-R2")
			if len(w.owners.consumed) != 0 {
				t.Errorf("the job consumed %d steps, want none", len(w.owners.consumed))
			}
			if receipt, found := w.receiptOf(500); found {
				t.Errorf("a consent receipt with outcome %v and reason %v was written, want none", receipt.Result.Outcome(), receipt.Result.Reason())
			}
			if commands.OtherPull.AffectsCheck() {
				t.Errorf("%q affects the check, want it ignored", commands.OtherPull)
			}
		})
	}
}

// An owner approve, revoke or skip with a valid code on a pull request that is closed and unmerged is ignored: it uses no step,
// records no consent and posts no acknowledgment.
func TestACommandOnAClosedPullRequestIsIgnored(t *testing.T) {
	for _, command := range []struct{ verb, reference string }{{"approve", "P1-R2"}, {"revoke", "P1-R2"}, {"skip", ""}} {
		t.Run(command.verb, func(t *testing.T) {
			w := newCjWorld(t)
			w.syncAll([]cjComment{ownerCommand(500, cjAt(10), command.verb, command.reference, 0)})
			w.closePull(cjID1, cjSync.Add(time.Minute))
			w.mustRun()

			if len(w.owners.consumed) != 0 {
				t.Errorf("the job consumed %d steps, want none", len(w.owners.consumed))
			}
			if receipt, found := w.receiptOf(500); found {
				t.Errorf("a consent was recorded with outcome %v, want none", receipt.Result.Outcome())
			}
			if entry, ack, found := w.recorded(cjID1, 500); found && (entry.Disposition == commands.Processed || ack != nil) {
				t.Errorf("the comment was recorded as %q with acknowledgment %v, want it ignored", entry.Disposition, ack)
			}
			if len(w.suites.messages) != 0 {
				t.Errorf("%d acknowledgments went out, want none", len(w.suites.messages))
			}
		})
	}
}
