package execution

import "github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"

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

// Weakened is the weakened set of a head.
func Weakened(receipt, canonical WeakenedRun, receiptTests []ReceiptTest, canonicalTests []CanonicalTest) (tests []TestID, known bool) {
	return nil, false
}
