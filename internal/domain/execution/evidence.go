package execution

import (
	"errors"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// ErrInvalidEvidence reports an evidence binding with an empty or malformed field.
var ErrInvalidEvidence = errors.New("invalid evidence binding")

// PlanKind says which plan produced a result.
type PlanKind uint8

// The plans.
const (
	PlanCanonical PlanKind = iota + 1 // the governing tests on the head
	PlanReceipt                       // the evaluated tests on the head and on the base side
)

// ManifestSource names where the governing manifest of a run came from: V<n>, a canonical version, or P<n>-R<m>, the
// revision of a proposal that a consent covers.
type ManifestSource string

// EvidenceInput is everything a stored result is bound to.
type EvidenceInput struct {
	Plan              PlanKind
	HeadSHA           string // the head commit, 40 lowercase hexadecimal digits
	BaseSHA           string // the merge base for a receipt, and empty for a canonical run
	ManifestDigest    artifact.Digest
	ManifestSource    ManifestSource
	DeclarationDigest artifact.Digest // the canonical declaration that classified the files
	Canonical         CanonicalLevel  // the policy levels in force
	Receipt           ReceiptLevel
	Profile           string // the profile id, such as go
	ExecutorVersion   string
	ExecutorSHA256    artifact.Digest // the executor binary
	ImageDigest       artifact.Digest // the sandbox image
	StartedAt         time.Time
	FinishedAt        time.Time
}

// EvidenceBinding is an immutable, validated EvidenceInput: what ran, on which revisions, under which rules. It is
// evidence about a head, never authority and never part of an approval's binding.
type EvidenceBinding struct {
	input EvidenceInput
	valid bool
}

// NewEvidenceBinding validates in and keeps it. Every field is required, a receipt needs a base SHA and a canonical run
// refuses one, and the times are kept in UTC with the end not before the start. Nothing defaults.
func NewEvidenceBinding(in EvidenceInput) (EvidenceBinding, error) {
	return EvidenceBinding{}, errNotImplemented
}

// Input returns the validated fields.
func (b EvidenceBinding) Input() EvidenceInput { return b.input }

// IsZero reports whether the binding is absent.
func (b EvidenceBinding) IsZero() bool { return !b.valid }
