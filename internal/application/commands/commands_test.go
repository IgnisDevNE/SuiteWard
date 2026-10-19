package commands_test

import (
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
)

// A new disposition must be placed in exactly one of the counted and ignored sets of the conformance suite, so that it cannot
// drift between AffectsCheck and the store's own list.
func TestEveryDispositionIsPlacedInExactlyOneConformanceSet(t *testing.T) {
	placed := map[commands.Disposition]int{}
	for _, d := range slices.Concat(commandstest.CountedDispositions, commandstest.IgnoredDispositions) {
		placed[d]++
	}
	all := commands.Dispositions()
	if len(all) != len(placed) {
		t.Fatalf("Dispositions() has %d codes and the counted and ignored sets %d; want the same codes", len(all), len(placed))
	}
	for _, d := range all {
		if placed[d] != 1 {
			t.Fatalf("disposition %q is in %d of the counted and ignored sets; want exactly one", d, placed[d])
		}
		counted := slices.Contains(commandstest.CountedDispositions, d)
		if d.AffectsCheck() != counted {
			t.Fatalf("AffectsCheck(%q) = %v, but the store counts it: %v", d, d.AffectsCheck(), counted)
		}
	}
}
