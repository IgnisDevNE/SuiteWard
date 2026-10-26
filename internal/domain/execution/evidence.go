package execution

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// ErrInvalidEvidence reports an evidence binding with an empty or malformed field.
var ErrInvalidEvidence = errors.New("invalid evidence binding")

var (
	manifestSourcePattern = regexp.MustCompile(`^(V[1-9][0-9]{0,8}|P[1-9][0-9]{0,8}-R[1-9][0-9]{0,8})$`)
	profilePattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	versionPattern        = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
)

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
	_, canonical := canonicalLevelNames[in.Canonical]
	_, receipt := receiptLevelNames[in.Receipt]
	for _, field := range []struct {
		name string
		ok   bool
	}{
		{"plan", in.Plan == PlanCanonical || in.Plan == PlanReceipt},
		{"head SHA", validSHA1(in.HeadSHA)},
		{"base SHA", validBaseSHA(in.Plan, in.BaseSHA)},
		{"manifest digest", !in.ManifestDigest.IsZero()},
		{"manifest source", manifestSourcePattern.MatchString(string(in.ManifestSource))},
		{"declaration digest", !in.DeclarationDigest.IsZero()},
		{"canonical level", canonical},
		{"receipt level", receipt},
		{"profile", profilePattern.MatchString(in.Profile)},
		{"executor version", versionPattern.MatchString(in.ExecutorVersion)},
		{"executor binary digest", !in.ExecutorSHA256.IsZero()},
		{"image digest", !in.ImageDigest.IsZero()},
		{"start time", !in.StartedAt.IsZero()},
		{"end time", !in.FinishedAt.IsZero() && !in.FinishedAt.Before(in.StartedAt)},
	} {
		if !field.ok {
			return EvidenceBinding{}, fmt.Errorf("%w: %s", ErrInvalidEvidence, field.name)
		}
	}
	in.StartedAt, in.FinishedAt = in.StartedAt.UTC(), in.FinishedAt.UTC()
	return EvidenceBinding{input: in, valid: true}, nil
}

// validBaseSHA is true for a receipt with a valid base SHA, and for a canonical run with none; another plan is not valid
// here, and the plan's own field reports it.
func validBaseSHA(plan PlanKind, sha string) bool {
	if plan == PlanReceipt {
		return validSHA1(sha)
	}
	return sha == ""
}

// Input returns the validated fields.
func (b EvidenceBinding) Input() EvidenceInput { return b.input }

// IsZero reports whether the binding is absent.
func (b EvidenceBinding) IsZero() bool { return !b.valid }
