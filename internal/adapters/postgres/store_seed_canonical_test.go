//go:build integration

package postgres_test

import (
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestSeedRejectsACanonicalThatContradictsItsHistoryEntry(t *testing.T) {
	w := newPGWorld(t)
	base := newCandidate("p0", "", w.protected("baseline"))
	other := newCandidate("p9", "", w.protected("another baseline"))
	next := newCandidate("p1", "v0", w.protected("change"))
	historyOf := func(c candidate, source contract.SourceRevision, operation contract.OperationID) []contract.HistoricalCanonical {
		version := must(contract.NewSuiteVersion(fxProject, fxSuite, "v0", c.protected.Manifest()))
		record := must(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: operation, VersionID: "v0", Binding: c.proposal.Current().Binding(),
			Carrier: c.carrier, Source: source, Target: fxTarget, RecordedAt: fxRecordedAt}))
		return []contract.HistoricalCanonical{must(contract.NewHistoricalCanonical(version, record))}
	}
	cases := []struct {
		name    string
		history []contract.HistoricalCanonical
	}{
		{"a promotion record from another source", historyOf(base, "elsewhere", "seed-promote")},
		{"a promotion record with another operation", historyOf(base, base.merged, "other-operation")},
		{"another manifest and binding for the same version", historyOf(other, other.merged, "seed-promote")},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			seed := w.baselineSeed(base, next)
			seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: other.proposal, Assessments: other.assessments})
			seed.History = tt.history
			if err := w.store.Seed(t.Context(), seed); !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("Seed = %v; want ErrInvalidRequest", err)
			}
			if n := w.count("suites") + w.count("suite_versions") + w.count("promotions"); n != 0 {
				t.Fatalf("%d rows remain after a rejected seed", n)
			}
		})
	}
	t.Run("a consistent history entry is accepted", func(t *testing.T) {
		w.seedWithBaseline(base, next)
	})
}
