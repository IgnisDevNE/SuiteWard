package governance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
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
