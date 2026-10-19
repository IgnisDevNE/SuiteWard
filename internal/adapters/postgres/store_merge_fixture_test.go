//go:build integration

package postgres_test

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// treeOf is the protected baseline layout with a protected test file that holds text.
func treeOf(text string) mapTree { return headTree(text, "package main\n") }

// unstoredTreeCandidate is a candidate whose contract is the inventory of tree, the only contract a merge of tree can
// promote, without its bytes in any content store.
func unstoredTreeCandidate(id string, baseline contract.SuiteVersionID, tree mapTree) candidate {
	return newCandidate(id, baseline, must(must(inventory.Build(context.Background(), tree)).Contract()))
}

// treeCandidate is unstoredTreeCandidate with the bytes of the contract in the content store.
func (w *pgWorld) treeCandidate(id string, baseline contract.SuiteVersionID, tree mapTree) candidate {
	w.t.Helper()
	c := unstoredTreeCandidate(id, baseline, tree)
	for _, entry := range c.protected.Manifest().Entries() {
		if err := w.content.Put(w.t.Context(), entry.Content, strings.NewReader(tree[entry.Path])); err != nil {
			w.t.Fatal(err)
		}
	}
	return c
}

// discardWriter is a content writer that stores nothing.
type discardWriter struct{}

func (discardWriter) Put(context.Context, artifact.Digest, io.Reader) error { return nil }

// noOutcomeMessage is the outcome comment of a caller that wants none.
func noOutcomeMessage(governance.MergeOutcome) (governance.OutboxMessage, bool, error) {
	return governance.OutboxMessage{}, false, nil
}

// mergeRequest is the merge of c, whose merge commit holds tree, observed when the first canonical was recorded.
func (c candidate) mergeRequest(tree mapTree) governance.MergeRequest {
	return governance.MergeRequest{Project: fxProject, Suite: fxSuite, Carrier: c.carrier, Source: contract.SourceRevision("merge-sha-" + c.id), Target: fxTarget,
		ObservedAt: fxRecordedAt, Tree: tree, RecordedAt: fxRecordedAt.Add(time.Hour), Message: noOutcomeMessage}
}

// merge records the merge of c and requires the outcome to be the given kind.
func (w *pgWorld) merge(c candidate, tree mapTree, kind governance.MergeKind) governance.MergeResult {
	w.t.Helper()
	result, err := governance.PromoteMerge(w.t.Context(), w.store, w.content, c.mergeRequest(tree))
	if err != nil {
		w.t.Fatalf("promote merge of %s: %v", c.id, err)
	}
	if result.Kind != kind {
		w.t.Fatalf("merge of %s = %+v, want %s", c.id, result.MergeOutcome, kind)
	}
	return result
}
