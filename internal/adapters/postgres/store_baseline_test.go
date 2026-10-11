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

// TestBaselineProposalStoresTheBytesThatBootstrapVerifies seeds the revision
// and assessment an inventory of the tree yields (assessments have no write
// path until M1.6), then proposes the same tree again under a new revision id:
// nothing is written to the database, yet the bytes land in the content store,
// and the bootstrap that follows has RecordPromotion verify them.
func TestBaselineProposalStoresTheBytesThatBootstrapVerifies(t *testing.T) {
	w := newPGWorld(t)
	tree := mapTree{
		scope.DeclarationPath:      "version: 1\ninclude: [/tests/**]\n",
		".github/workflows/ci.yml": "on: push\n",
		"tests/a_test.go":          "package tests\n",
		"src/main.go":              "package main\n",
	}
	inv := must(inventory.Build(t.Context(), tree))
	baseline := newCandidate("p1", "", must(inv.Contract()))
	w.seedSuite(fxProject, fxSuite, baseline)
	entries := inv.Manifest().Entries()
	for _, entry := range entries {
		if err := w.content.Verify(t.Context(), entry.Content); err == nil {
			t.Fatalf("%s is stored before the proposal; the test would not show who stored it", entry.Path)
		}
	}
	before := w.revision()
	request := governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: fxProject, SuiteID: fxSuite, ProposalID: "p1", RevisionID: "revision-2"},
		Carrier:   baseline.carrier, Origin: baseline.origin, Tree: tree,
	}

	result, err := governance.ProposeBaseline(t.Context(), w.store, w.content, request)

	if err != nil {
		t.Fatalf("propose baseline: %v", err)
	}
	if result.Revise.Committed || result.Revise.Current != baseline.current() {
		t.Fatalf("result = %+v, want the seeded reference %v and no write", result.Revise, baseline.current())
	}
	if got := w.revision(); got != before {
		t.Fatalf("suite revision = %d, want %d", got, before)
	}
	for _, entry := range entries {
		if err := w.content.Verify(t.Context(), entry.Content); err != nil {
			t.Fatalf("the bytes of %s were not stored: %v", entry.Path, err)
		}
	}

	w.approve(baseline)
	bootstrap := baseline.baselineRequest("bootstrap-p1", "v1")
	bootstrap.Proposed = result.Contract
	promoted, err := governance.Bootstrap(t.Context(), w.store, bootstrap)

	if err != nil || promoted.Outcome != contract.PromotionProposed || !promoted.Committed || w.currentVersion() != "v1" {
		t.Fatalf("bootstrap = %+v err=%v current=%q, want a committed first canonical", promoted, err, w.currentVersion())
	}
}
