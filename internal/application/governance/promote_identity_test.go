package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestPromotionOperationIDReusedWithAnotherIdentityConflicts(t *testing.T) {
	w, c := promotionWorld(t)
	request := c.mergedRequest(t, "promote-1", "version-1")
	w.promote(request)
	before := w.revision()
	otherIntegration := must(contract.NewIntegration(projectID, target, "merged-other", c.carrier, contract.IntegrationMergedChange))

	for name, change := range map[string]func(*governance.PromoteRequest){
		"revision":          func(r *governance.PromoteRequest) { r.Reference.RevisionID = "revision-2" },
		"proposal":          func(r *governance.PromoteRequest) { r.Reference.ProposalID = "p-other" },
		"carrier":           func(r *governance.PromoteRequest) { r.Carrier = "carrier-other" },
		"assessment source": func(r *governance.PromoteRequest) { r.AssessmentSource = "merged-other" },
		"integration":       func(r *governance.PromoteRequest) { r.Integration = otherIntegration },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			change(&changed)
			if _, err := governance.Promote(context.Background(), w.mem, changed); !errors.Is(err, governance.ErrOperationConflict) {
				t.Fatalf("error = %v, want operation conflict", err)
			}
		})
	}
	w.requireWrites(before, 0)
}

func TestPromotionReplayAnswersFromTheReceiptAfterAuthorityMoved(t *testing.T) {
	w, first, second, fix := correctionWorld(t)
	correction := governance.CorrectionRequest{Promotion: fix.mergedRequest(t, "correct-1", "version-3"), TargetVersionID: "version-1"}
	w.correct(correction)
	before := w.revision()

	for _, tc := range []struct {
		request governance.PromoteRequest
		version contract.SuiteVersionID
	}{
		{first.mergedRequest(t, "promote-1", "version-1"), "version-1"},
		{second.mergedRequest(t, "promote-2", "version-2"), "version-2"},
	} {
		replay := w.promote(tc.request)
		if !replay.Duplicate || !replay.Committed || replay.Record.VersionID() != tc.version {
			t.Fatalf("replay of %s = %+v, want the original record of %s", tc.request.OperationID, replay, tc.version)
		}
	}
	if replay := w.correct(correction); !replay.Duplicate || replay.Record.VersionID() != "version-3" {
		t.Fatalf("correction replay = %+v, want the original record", replay)
	}
	other := correction
	other.TargetVersionID = "version-2"
	if _, err := governance.Correct(context.Background(), w.mem, other); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("correction of another version under the same operation: error = %v, want operation conflict", err)
	}
	w.requireWrites(before, 0)
}

func TestReusedVersionIDIsAVersionConflictAndWritesNothing(t *testing.T) {
	w, _, _, fix := correctionWorld(t)
	before := w.revision()

	_, err := governance.Promote(context.Background(), w.mem, fix.mergedRequest(t, "promote-3", "version-1"))

	if !errors.Is(err, governance.ErrVersionConflict) {
		t.Fatalf("error = %v, want version conflict", err)
	}
	w.requireWrites(before, 0)
	if w.currentVersion() != "version-2" {
		t.Fatalf("current version = %q, want version-2", w.currentVersion())
	}
	if _, found := w.receipt("promote-3"); found {
		t.Fatal("a conflicting version left a receipt")
	}
	if _, active := w.state().Schedule.Active(); !active {
		t.Fatal("a conflicting version changed the schedule")
	}
}
