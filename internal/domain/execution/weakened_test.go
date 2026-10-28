package execution_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The weakened set (docs/contracts/runtime.md, "The weakened signal"): the tests of the receipt that are marked changed and whose state in the canonical run of
// the same head, governed by the same manifest, is FAIL, MISSING or FLAKY, joined on package and name. It is known only when both runs are complete.

var (
	approved = artifact.Hash([]byte("the canonical version"))
	revision = artifact.Hash([]byte("the manifest of the revision"))
)

func runOf(status execution.RunStatus, governing artifact.Digest) execution.WeakenedRun {
	return execution.WeakenedRun{Status: status, Governing: governing}
}

func TestWeakened(t *testing.T) {
	complete := runOf(execution.RunComplete, approved)
	// One test for each way that a changed test can meet the canonical run.
	receiptTests := []execution.ReceiptTest{
		{Package: "b/pkg", Name: "TestFails", Changed: true},
		{Package: "a/pkg", Name: "TestMissing", Changed: true},
		{Package: "a/pkg", Name: "TestFlaky", Changed: true},
		{Package: "a/pkg", Name: "TestPasses", Changed: true},
		{Package: "a/pkg", Name: "TestSkipped", Changed: true},
		{Package: "a/pkg", Name: "TestExcluded", Changed: true},
		{Package: "a/pkg", Name: "TestAdded", Changed: false}, // an added test has no approved version
		{Package: "a/pkg", Name: "TestNoApprovedRow", Changed: true},
		{Package: "c/pkg", Name: "TestFails", Changed: true}, // the same name in another package is another test
	}
	canonicalTests := []execution.CanonicalTest{
		{Package: "b/pkg", Name: "TestFails", State: execution.CanonicalFail},
		{Package: "a/pkg", Name: "TestMissing", State: execution.CanonicalMissing},
		{Package: "a/pkg", Name: "TestFlaky", State: execution.CanonicalFlaky},
		{Package: "a/pkg", Name: "TestPasses", State: execution.CanonicalPass},
		{Package: "a/pkg", Name: "TestSkipped", State: execution.CanonicalSkipped},
		{Package: "a/pkg", Name: "TestExcluded", State: execution.CanonicalExcluded},
		{Package: "a/pkg", Name: "TestAdded", State: execution.CanonicalFail},
		{Package: "c/pkg", Name: "TestFails", State: execution.CanonicalPass},
	}
	flagged := []execution.TestID{{Package: "a/pkg", Name: "TestFlaky"}, {Package: "a/pkg", Name: "TestMissing"}, {Package: "b/pkg", Name: "TestFails"}}
	tests := []struct {
		name               string
		receipt, canonical execution.WeakenedRun
		changed            []execution.ReceiptTest
		states             []execution.CanonicalTest
		want               []execution.TestID
		known              bool
	}{
		{"fail, missing and flaky are weakened, sorted by package then name", complete, complete, receiptTests, canonicalTests, flagged, true},
		{"nothing is weakened when no changed test fails", complete, complete, receiptTests[3:6], canonicalTests[3:6], nil, true},
		{"a receipt with no test weakens nothing", complete, complete, nil, canonicalTests, nil, true},
		{"the canonical job of the revision's manifest has no counterpart: unknown", complete, runOf(execution.RunComplete, revision), receiptTests, canonicalTests, nil, false},
		{"a pending receipt is unknown", runOf(execution.RunPending, approved), complete, receiptTests, canonicalTests, nil, false},
		{"an unavailable receipt is unknown", runOf(execution.RunUnavailable, approved), complete, receiptTests, canonicalTests, nil, false},
		{"a receipt that does not apply is unknown", runOf(execution.RunNotApplicable, approved), complete, receiptTests, canonicalTests, nil, false},
		{"a pending canonical run is unknown", complete, runOf(execution.RunPending, approved), receiptTests, canonicalTests, nil, false},
		{"an unavailable canonical run is unknown", complete, runOf(execution.RunUnavailable, approved), receiptTests, canonicalTests, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, known, err := execution.Weakened(tc.receipt, tc.canonical, tc.changed, tc.states)
			if err != nil || known != tc.known || !slices.Equal(got, tc.want) {
				t.Errorf("Weakened = %v, %v, %v; want %v, %v", got, known, err, tc.want, tc.known)
			}
		})
	}
}

// Every job has a governing manifest, so a complete run with none is a caller that did not fill it: an error, never an unknown set, and two runs with none must
// not compare equal. A run that is not complete is not compared, so it needs no digest.
func TestWeakenedRefusesACompleteRunWithNoGoverningManifest(t *testing.T) {
	complete, noDigest := runOf(execution.RunComplete, approved), runOf(execution.RunComplete, artifact.Digest{})
	tests := []struct {
		name               string
		receipt, canonical execution.WeakenedRun
		wantErr            bool
	}{
		{"the receipt has none", noDigest, complete, true},
		{"the canonical run has none", complete, noDigest, true},
		{"both have none: they do not compare equal", noDigest, noDigest, true},
		{"a pending run with none is not compared", runOf(execution.RunPending, artifact.Digest{}), complete, false},
		{"a complete run beside an unavailable one with none is not compared", complete, runOf(execution.RunUnavailable, artifact.Digest{}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set, known, err := execution.Weakened(tc.receipt, tc.canonical, []execution.ReceiptTest{{Package: "p", Name: "TestA", Changed: true}}, []execution.CanonicalTest{{Package: "p", Name: "TestA", State: execution.CanonicalFail}})
			if errors.Is(err, execution.ErrNoGoverningDigest) != tc.wantErr || (err != nil && !tc.wantErr) || set != nil || known {
				t.Errorf("Weakened = %v, %v, %v; want an error %v and no set", set, known, err, tc.wantErr)
			}
		})
	}
}
