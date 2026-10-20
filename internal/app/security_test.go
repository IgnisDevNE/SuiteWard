package app

import (
	"bytes"
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

			_, ack := w.requireDisposition(cjID1, 500, commands.OtherPull)
			want := cjMust(newRejectedAck(commentTarget{RepositoryID: cjID1, Repo: cjRepo1, Pull: 5}, 500, cjLogin, commands.OtherPull, 0, "P1-R2"))
			if ack == nil || ack.Key != want.Key || ack.Kind != want.Kind || !bytes.Equal(ack.Payload, want.Payload) {
				t.Errorf("acknowledgment %+v, want %+v", ack, want)
			}
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

// A command posted while the pull request was open is processed as before, even when the job decides it after the pull
// request closed: the owner's approval or revocation still lands, and a merged carrier answers with the promotion outcome.
func TestACommandPostedWhileThePullRequestWasOpenIsProcessedAfterItClosed(t *testing.T) {
	for _, verb := range []string{"approve", "revoke"} {
		t.Run(verb, func(t *testing.T) {
			w := newCjWorld(t)
			w.syncAll([]cjComment{ownerCommand(500, cjAt(10), verb, "P1-R2", 0)})
			w.closePull(cjID1, cjSync.Add(time.Minute))
			w.mustRun()

			w.requireProcessed(cjID1, 500)
			if len(w.owners.consumed) != 1 {
				t.Errorf("the job consumed %d steps, want 1", len(w.owners.consumed))
			}
		})
	}
}

// An owner approve, revoke or skip whose comment was created at or after the moment the pull request was first seen not open
// is ignored: it uses no step, records no consent and posts no acknowledgment. The comment time is GitHub's clock and
// not_open_since is the sync's own, so this happens when the sync's clock runs behind GitHub's; equal times count as closed.
func TestACommandCreatedAfterThePullRequestClosedIsIgnored(t *testing.T) {
	closedAt := cjSync.Add(10 * time.Minute)
	for _, created := range []struct {
		name string
		at   time.Time
	}{{"after", closedAt.Add(time.Minute)}, {"at the same time", closedAt}} {
		for _, command := range []struct{ verb, reference string }{{"approve", "P1-R2"}, {"revoke", "P1-R2"}, {"skip", ""}} {
			t.Run(created.name+"/"+command.verb, func(t *testing.T) {
				w := newCjWorld(t)
				w.observe(cjID1, cjSync, ownerCommand(500, created.at, command.verb, command.reference, 0))
				w.observe(cjID2, cjSync.Add(20*time.Minute))
				w.closePull(cjID1, closedAt)
				w.closePull(cjID1, cjSync.Add(20*time.Minute))
				w.mustRun()

				if len(w.owners.consumed) != 0 {
					t.Errorf("the job consumed %d steps, want none", len(w.owners.consumed))
				}
				if receipt, found := w.receiptOf(500); found {
					t.Errorf("a consent was recorded with outcome %v, want none", receipt.Result.Outcome())
				}
				if entry, ack, found := w.recorded(cjID1, 500); !found || entry.Disposition != commands.NotCommand || ack != nil {
					t.Errorf("the comment was recorded %v as %q with acknowledgment %v, want it ignored as %q", found, entry.Disposition, ack, commands.NotCommand)
				}
				if len(w.suites.messages) != 0 {
					t.Errorf("%d acknowledgments went out, want none", len(w.suites.messages))
				}
			})
		}
	}
}
