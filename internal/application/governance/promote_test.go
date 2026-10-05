package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// promotionWorld has one approved candidate.
func promotionWorld(t *testing.T) (*world, candidate) {
	t.Helper()
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	w.approve(c)
	return w, c
}

func TestPromotionAfterMergeRecordsNewVersionAndAdvancesRevision(t *testing.T) {
	w, c := promotionWorld(t)
	before := w.revision()

	result := w.promote(c.mergedRequest(t, "promote-1", "version-1"))

	if result.Outcome != contract.PromotionProposed || !result.Committed || result.Duplicate {
		t.Fatalf("result = %+v, want a committed proposed promotion", result)
	}
	if result.Record.VersionID() != "version-1" || result.Record.OperationID() != "promote-1" {
		t.Fatalf("record = %q by %q", result.Record.VersionID(), result.Record.OperationID())
	}
	w.requireWrites(before, 1)
	state := w.state()
	if current, _ := state.Canonical.Suite().CurrentVersionID(); current != "version-1" {
		t.Fatalf("current version = %q, want version-1", current)
	}
	if !state.Canonical.Contract().Equal(c.protected) {
		t.Fatal("canonical contract is not the promoted contract")
	}
	if receipt, found := w.receipt("promote-1"); !found || receipt.Kind != governance.OperationPromote || receipt.Promotion == nil {
		t.Fatalf("receipt = %+v, found %v", receipt, found)
	}
}

func TestBlockedPromotionWritesNothing(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c) // never approved
	before := w.revision()

	result := w.promote(c.mergedRequest(t, "promote-1", "version-1"))

	if result.Outcome != contract.PromotionBlocked || result.Reason != contract.PromotionReasonApprovalMissing || result.Committed {
		t.Fatalf("result = %+v, want an uncommitted block for missing approval", result)
	}
	w.requireWrites(before, 0)
	if _, found := w.receipt("promote-1"); found {
		t.Fatal("a blocked promotion left a receipt")
	}
}

func TestNoChangePromotionWritesNothing(t *testing.T) {
	first := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	same := newCandidate(t, "p2", "version-1", protectedOf(t, "v1"))
	w := newWorld(t, first, same)
	w.approve(first)
	w.approve(same)
	w.promote(first.mergedRequest(t, "promote-1", "version-1"))
	before := w.revision()

	result := w.promote(same.mergedRequest(t, "promote-2", "version-2"))

	if result.Outcome != contract.PromotionNoChange || result.Committed {
		t.Fatalf("result = %+v, want an uncommitted no-change outcome", result)
	}
	w.requireWrites(before, 0)
	if _, found := w.receipt("promote-2"); found {
		t.Fatal("a no-change promotion left a receipt")
	}
}

func TestPromotionReplayReturnsOriginalRecordWithoutWriting(t *testing.T) {
	w, c := promotionWorld(t)
	request := c.mergedRequest(t, "promote-1", "version-1")
	original := w.promote(request)
	before := w.revision()

	replay := w.promote(request)

	if !replay.Committed || !replay.Duplicate || replay.Outcome != contract.PromotionProposed || replay.Record.VersionID() != original.Record.VersionID() {
		t.Fatalf("replay = %+v, want the original record as a committed duplicate", replay)
	}
	w.requireWrites(before, 0)
}

func TestOperationIDReusedForDifferentPromotionConflicts(t *testing.T) {
	w, c := promotionWorld(t)
	request := c.mergedRequest(t, "promote-1", "version-1")
	w.promote(request)
	before := w.revision()
	otherVersion := request
	otherVersion.NewVersionID = "version-other"
	otherContract := request
	otherContract.Proposed = protectedOf(t, "other")

	for name, attempt := range map[string]func() error{
		"another version id": func() error { _, err := governance.Promote(context.Background(), w.mem, otherVersion); return err },
		"another contract":   func() error { _, err := governance.Promote(context.Background(), w.mem, otherContract); return err },
		"another kind":       func() error { _, err := governance.Bootstrap(context.Background(), w.mem, request); return err },
		"a consent command": func() error {
			command := w.command(c, "revision-1", "promote-1", "comment-other", contract.RevokeConsent, 2)
			_, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: command})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := attempt(); !errors.Is(err, governance.ErrOperationConflict) {
				t.Fatalf("error = %v, want operation conflict", err)
			}
		})
	}
	w.requireWrites(before, 0)
}

func TestFailingWriteRollsBackEverythingInTheUnitOfWork(t *testing.T) {
	boom := errors.New("write failed")

	t.Run("promotion", func(t *testing.T) {
		w, c := promotionWorld(t)
		request := c.mergedRequest(t, "promote-1", "version-1")
		before := w.revision()
		w.mem.FailNext(boom)

		if _, err := governance.Promote(context.Background(), w.mem, request); !errors.Is(err, boom) {
			t.Fatalf("error = %v, want the write failure", err)
		}

		w.requireWrites(before, 0)
		if w.currentVersion() != "" {
			t.Fatal("a failed promotion moved the canonical pointer")
		}
		if _, found := w.receipt("promote-1"); found {
			t.Fatal("a failed promotion left a receipt")
		}
		if retry := w.promote(request); !retry.Committed || retry.Duplicate {
			t.Fatalf("retry = %+v, want a fresh commit", retry)
		}
	})

	t.Run("consent", func(t *testing.T) {
		w, c := consentWorld(t)
		command := w.command(c, "revision-1", "approve-1", "comment-1", contract.ApproveConsent, 1)
		before := w.revision()
		w.mem.FailNext(boom)

		if _, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: command}); !errors.Is(err, boom) {
			t.Fatalf("error = %v, want the write failure", err)
		}

		w.requireWrites(before, 0)
		if _, found := w.receipt("approve-1"); found {
			t.Fatal("a failed consent left a receipt")
		}
		if retry := w.consent(command); retry.Duplicate || !retry.Receipt.CurrentApprovalEligible {
			t.Fatalf("retry = %+v, want a fresh eligible approval", retry)
		}
	})
}
