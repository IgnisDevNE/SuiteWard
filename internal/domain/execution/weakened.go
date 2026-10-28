package execution

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// ErrNoGoverningDigest reports a job that has no governing manifest: every job has one, so the caller did not fill it.
var ErrNoGoverningDigest = errors.New("a job has no governing manifest")

// TestID names a test of a run: its package and its name.
type TestID struct{ Package, Name string }

// WeakenedRun is a run as the weakened rule reads it: its status, and the digest of the manifest that governs its job.
type WeakenedRun struct {
	Status    RunStatus
	Governing artifact.Digest
}

// ReceiptTest is a test of the receipt run. Changed says that the receipt marks it changed: its declaration differs from the approved version's.
type ReceiptTest struct {
	Package, Name string
	Changed       bool
}

// CanonicalTest is a test of the canonical run with its state.
type CanonicalTest struct {
	Package, Name string
	State         CanonicalState
}

// Weakened is the weakened set of a head: an approved test that this change edits and that fails in its approved version. It is the tests that the receipt
// marks changed whose state in the canonical run is FAIL, MISSING or FLAKY, joined on package and name, sorted by package and then name. An added test has no
// approved version, and an excluded test has no state that counts, so neither is in it.
//
// It is known only when both runs are complete and the canonical run is governed by the manifest that governs the receipt run: a canonical run under the manifest
// of the revision (the head is approved) has no counterpart. Otherwise known is false and the set is empty, which reads as 0 and is never an absence. Every job has
// a governing manifest, so a complete run with none is a caller that did not fill it, and is ErrNoGoverningDigest: two missing digests never compare equal.
func Weakened(receipt, canonical WeakenedRun, receiptTests []ReceiptTest, canonicalTests []CanonicalTest) (tests []TestID, known bool, err error) {
	if receipt.Status != RunComplete || canonical.Status != RunComplete {
		return nil, false, nil
	}
	if receipt.Governing.IsZero() || canonical.Governing.IsZero() {
		return nil, false, fmt.Errorf("%w: a complete run that the weakened set compares", ErrNoGoverningDigest)
	}
	if receipt.Governing != canonical.Governing {
		return nil, false, nil
	}
	failing := map[TestID]bool{}
	for _, t := range canonicalTests {
		failing[TestID{t.Package, t.Name}] = t.State.Flagged()
	}
	for _, t := range receiptTests {
		if id := (TestID{t.Package, t.Name}); t.Changed && failing[id] {
			tests = append(tests, id)
		}
	}
	slices.SortFunc(tests, func(a, b TestID) int { return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.Name, b.Name)) })
	return tests, true, nil
}
