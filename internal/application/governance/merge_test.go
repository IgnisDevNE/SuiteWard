package governance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func noMessage(governance.MergeOutcome) (governance.OutboxMessage, bool, error) {
	return governance.OutboxMessage{}, false, nil
}

func validMergeRequest() governance.MergeRequest {
	return governance.MergeRequest{Project: projectID, Suite: suiteID, Carrier: prA, Source: "merge-sha", Target: target,
		ObservedAt: recordedAt, Tree: headTree("v2", "package main\n"), RecordedAt: recordedAt, Message: noMessage}
}

func TestPromoteMergeRejectsAnInvalidRequest(t *testing.T) {
	for name, edit := range map[string]func(*governance.MergeRequest){
		"blank project":                  func(r *governance.MergeRequest) { r.Project = " " },
		"blank suite":                    func(r *governance.MergeRequest) { r.Suite = "" },
		"blank carrier":                  func(r *governance.MergeRequest) { r.Carrier = "\t" },
		"blank target":                   func(r *governance.MergeRequest) { r.Target = "" },
		"blank source without a refusal": func(r *governance.MergeRequest) { r.Source = "" },
		"blank source with another refusal": func(r *governance.MergeRequest) {
			r.Source, r.Refusal, r.Tree = "", governance.MergeReasonForkUnsupported, nil
		},
		"no tree without a refusal":        func(r *governance.MergeRequest) { r.Tree = nil },
		"no message function":              func(r *governance.MergeRequest) { r.Message = nil },
		"zero observed time":               func(r *governance.MergeRequest) { r.ObservedAt = time.Time{} },
		"zero recorded time":               func(r *governance.MergeRequest) { r.RecordedAt = time.Time{} },
		"sub-microsecond recorded time":    func(r *governance.MergeRequest) { r.RecordedAt = recordedAt.Add(time.Nanosecond) },
		"a use-case reason as the refusal": func(r *governance.MergeRequest) { r.Refusal = governance.MergeReasonUnapprovedChange },
		"an unknown refusal":               func(r *governance.MergeRequest) { r.Refusal = "bogus" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			before := w.revision()
			request := validMergeRequest()
			edit(&request)

			_, err := governance.PromoteMerge(context.Background(), w.mem, newFakeWriter(), request)

			if !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
			w.requireWrites(before, 0)
		})
	}
}

func TestPromoteMergeNeedsAUnitOfWorkAndAContentWriter(t *testing.T) {
	w := newWorld(t)
	if _, err := governance.PromoteMerge(context.Background(), nil, newFakeWriter(), validMergeRequest()); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("without a unit of work: error = %v, want ErrInvalidRequest", err)
	}
	if _, err := governance.PromoteMerge(context.Background(), w.mem, nil, validMergeRequest()); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("without a content writer: error = %v, want ErrInvalidRequest", err)
	}
	if _, _, err := governance.MergeOutcomeOf(context.Background(), nil, projectID, suiteID, prA); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("outcome without a unit of work: error = %v, want ErrInvalidRequest", err)
	}
	if _, _, err := governance.MergeOutcomeOf(context.Background(), w.mem, projectID, suiteID, " "); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("outcome of a blank carrier: error = %v, want ErrInvalidRequest", err)
	}
}

func TestPromoteMergeStoresTheContentOfAnIntegratedTreeBeforeThePromotion(t *testing.T) {
	e := newHeadEnvFirst(t, "version-1", headTree("v1", "package main\n"))
	request := validMergeRequest()
	request.Carrier, request.Tree = prA, headTree("v1", "package main\n") // the canonical's own content: no change, content is still stored
	writer := newFakeWriter()

	// The head environment's canonical is version-1, which a merge outcome cannot succeed (V<n> ids): the error is the point,
	// and the content stored before the unit of work stays, harmless.
	_, err := governance.PromoteMerge(context.Background(), e.mem, writer, request)

	if !errors.Is(err, governance.ErrInvalidState) {
		t.Fatalf("error = %v, want ErrInvalidState for a canonical that is not V<n>", err)
	}
	if len(writer.stored) == 0 {
		t.Fatal("no content was stored before the unit of work")
	}
}

func TestPromoteMergeOfAnUnknownSuiteIsNotFound(t *testing.T) {
	w := newWorld(t)
	request := validMergeRequest()
	request.Suite = "nowhere"

	if _, err := governance.PromoteMerge(context.Background(), w.mem, newFakeWriter(), request); !errors.Is(err, governance.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
}

// The first merge moves the canonical, so the second approved pull request is bound to a canonical that is gone: its
// next head gets a new revision, and only an approval of that exact revision lets its merge promote it.
func TestRebasedPullRequestIsPromotedOnceItsNewRevisionIsApproved(t *testing.T) {
	second := newCandidate(t, "pr2", "V1", treeContract(t, headTree("v2", "package main\n")))
	e := newHeadEnv(t, second)
	e.approve(e.other)
	e.approve(second)
	e.merge(e.other, headTree("other", "package main\n"), governance.MergePromoted)

	rebased := e.assess(second.carrier, "head-rebased", headTree("v2", "package main\n"))

	e.requireChange(rebased, inventory.ProtectedChange, "pr2-R2", false, contract.PromotionReasonApprovalMissing)
	e.approveRef(*rebased.Reference, second.carrier)
	e.requireChange(e.assess(second.carrier, "head-rebased", headTree("v2", "package main\n")), inventory.ProtectedChange, "pr2-R2", true, contract.PromotionReasonNone)
	before := e.revision()

	merged := e.merge(second, headTree("v2", "package main\n"), governance.MergePromoted)

	if merged.Version != "V3" || merged.Reference == nil || *merged.Reference != *rebased.Reference {
		t.Fatalf("merge = %+v, want V3 promoted from %v", merged.MergeOutcome, rebased.Reference)
	}
	e.requireWrites(before, 2) // the assessment of the merge commit and the promotion
	if e.currentVersion() != "V3" {
		t.Fatalf("current version = %q, want V3", e.currentVersion())
	}
}
