//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// mapTree is an in-memory source tree of regular files.
type mapTree map[string]string

func (m mapTree) Entries(context.Context) ([]inventory.Entry, error) {
	entries := make([]inventory.Entry, 0, len(m))
	for path := range m {
		entries = append(entries, inventory.Entry{Path: path, Kind: inventory.File})
	}
	return entries, nil
}

func (m mapTree) Read(_ context.Context, path string) ([]byte, error) {
	content, found := m[path]
	if !found {
		return nil, fmt.Errorf("no file %q", path)
	}
	return []byte(content), nil
}

// TestBaselineProposalStoresTheBytesThatBootstrapVerifies proposes the tree an
// inventory yields as the first proposal of a Suite: the bytes land in the
// content store, the owner approves, the evidence of the pinned origin is
// recorded, and the bootstrap that follows has RecordPromotion verify the
// bytes. Proposing the same tree again under a new revision id writes nothing.
func TestBaselineProposalStoresTheBytesThatBootstrapVerifies(t *testing.T) {
	w := newPGWorld(t)
	tree := mapTree{
		scope.DeclarationPath:      "version: 1\ninclude: [/tests/**]\n",
		".github/workflows/ci.yml": "on: push\n",
		"tests/a_test.go":          "package tests\n",
		"src/main.go":              "package main\n",
	}
	inv := must(inventory.Build(t.Context(), tree))
	// Names the baseline proposal and its carrier and origin; it is not seeded.
	baseline := newCandidate("p1", "", must(inv.Contract()))
	w.seedSuite(fxProject, fxSuite)
	entries := inv.Manifest().Entries()
	for _, entry := range entries {
		if err := w.content.Verify(t.Context(), entry.Content); err == nil {
			t.Fatalf("%s is stored before the proposal; the test would not show who stored it", entry.Path)
		}
	}
	before := w.revision()
	request := governance.BaselineRequest{Reference: baseline.current(), Carrier: baseline.carrier, Origin: baseline.origin, OriginKind: contract.OriginBranch, Tree: tree}

	result, err := governance.ProposeBaseline(t.Context(), w.store, w.content, request)

	if err != nil {
		t.Fatalf("propose baseline: %v", err)
	}
	if !result.Revise.Committed || result.Revise.Current != baseline.current() || w.revision() != before+1 {
		t.Fatalf("result = %+v at revision %d, want a committed %v and one write", result.Revise, w.revision(), baseline.current())
	}
	for _, entry := range entries {
		if err := w.content.Verify(t.Context(), entry.Content); err != nil {
			t.Fatalf("the bytes of %s were not stored: %v", entry.Path, err)
		}
	}
	again := request
	again.Reference.RevisionID = "revision-2"
	if second, err := governance.ProposeBaseline(t.Context(), w.store, w.content, again); err != nil || second.Revise.Committed || w.revision() != before+1 {
		t.Fatalf("same tree again = %+v, %v at revision %d, want no write", second.Revise, err, w.revision())
	}

	w.approve(baseline)
	w.recordAssessment(baseline.current(), baseline.origin, baseline.proposal.Current().Binding(), contract.IntegrityPassed)
	bootstrap := baseline.baselineRequest("bootstrap-p1", "v1")
	bootstrap.Proposed = result.Contract
	promoted, err := governance.Bootstrap(t.Context(), w.store, bootstrap)

	if err != nil || promoted.Outcome != contract.PromotionProposed || promoted.Duplicate || w.currentVersion() != "v1" {
		t.Fatalf("bootstrap = %+v err=%v current=%q, want a new first canonical", promoted, err, w.currentVersion())
	}
}

// recordAssessment records evidence from the service emitter for a revision of the default Suite.
func (w *pgWorld) recordAssessment(reference contract.ProposalReference, source contract.SourceRevision, binding contract.ApprovalBinding, outcome contract.IntegrityOutcome) {
	w.t.Helper()
	evidence := must(contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), source, binding, outcome))
	err := w.store.Do(w.t.Context(), fxProject, fxSuite, func(ctx context.Context, tx governance.Tx) error {
		return tx.RecordAssessment(ctx, governance.AssessmentWrite{Reference: reference, Source: source, Evidence: evidence})
	})
	if err != nil {
		w.t.Fatalf("record assessment: %v", err)
	}
}
