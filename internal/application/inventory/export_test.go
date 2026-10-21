package inventory

import (
	"context"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// Evaluate resolves the manifest of tree under the governing scope alone, as the tests use it.
func Evaluate(ctx context.Context, tree Tree, governing scope.Scope) (artifact.Manifest, error) {
	covered, err := evaluate(ctx, tree, governing, governing)
	if err != nil {
		return artifact.Manifest{}, err
	}
	return manifestOfFiles(ctx, tree, covered, map[string][]byte{})
}

// RunnerGroups exposes the runner families to the test that proves every glob parses.
func RunnerGroups() [][]string { return runnerGroups }
