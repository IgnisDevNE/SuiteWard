package execution_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

const (
	headSHA = "0123456789abcdef0123456789abcdef01234567"
	baseSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

func digestOf(s string) artifact.Digest { return artifact.Hash([]byte(s)) }

var started = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// canonicalInput is a valid canonical run.
func canonicalInput() execution.EvidenceInput {
	return execution.EvidenceInput{
		Plan:              execution.PlanCanonical,
		HeadSHA:           headSHA,
		ManifestDigest:    digestOf("manifest"),
		ManifestSource:    "V3",
		DeclarationDigest: digestOf("declaration"),
		Canonical:         execution.CanonicalLevelRequired,
		Receipt:           execution.ReceiptLevelBeforeApproval,
		Profile:           "go",
		ExecutorVersion:   "0.4.0-alpha.3",
		ExecutorSHA256:    digestOf("executor binary"),
		ImageDigest:       digestOf("image"),
		StartedAt:         started,
		FinishedAt:        started.Add(90 * time.Second),
	}
}

// receiptInput is a valid receipt run.
func receiptInput() execution.EvidenceInput {
	in := canonicalInput()
	in.Plan = execution.PlanReceipt
	in.BaseSHA = baseSHA
	in.ManifestSource = "P2-R1"
	return in
}

func TestEvidenceBindingKeepsAValidRun(t *testing.T) {
	for name, in := range map[string]execution.EvidenceInput{"canonical": canonicalInput(), "receipt": receiptInput()} {
		binding, err := execution.NewEvidenceBinding(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if binding.IsZero() {
			t.Errorf("%s: a constructed binding is zero", name)
		}
		if binding.Input() != in {
			t.Errorf("%s: Input() = %+v, want %+v", name, binding.Input(), in)
		}
	}
	if !(execution.EvidenceBinding{}).IsZero() {
		t.Error("the zero binding is not zero")
	}
}

func TestEvidenceBindingKeepsTimesInUTC(t *testing.T) {
	in := canonicalInput()
	zone := time.FixedZone("UTC-3", -3*60*60)
	in.StartedAt, in.FinishedAt = started.In(zone), started.Add(time.Minute).In(zone)
	binding, err := execution.NewEvidenceBinding(in)
	if err != nil {
		t.Fatal(err)
	}
	got := binding.Input()
	if got.StartedAt != started || got.FinishedAt != started.Add(time.Minute) || got.StartedAt.Location() != time.UTC {
		t.Errorf("times = %v, %v; want %v and a minute later, in UTC", got.StartedAt, got.FinishedAt, started)
	}
	in.FinishedAt = in.StartedAt // an instantaneous run is still a run
	if _, err := execution.NewEvidenceBinding(in); err != nil {
		t.Errorf("an end equal to the start: %v", err)
	}
}

func TestEvidenceBindingAcceptsTheSourcesOfAGoverningManifest(t *testing.T) {
	for _, source := range []execution.ManifestSource{"V1", "V12", "V999999999", "P1-R1", "P12-R34", "P999999999-R999999999"} {
		in := canonicalInput()
		in.ManifestSource = source
		if _, err := execution.NewEvidenceBinding(in); err != nil {
			t.Errorf("source %q: %v", source, err)
		}
	}
}

func TestEvidenceBindingRefusesAnEmptyOrMalformedField(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name   string
		base   func() execution.EvidenceInput
		mutate func(*execution.EvidenceInput)
	}{
		{"no plan", canonicalInput, func(in *execution.EvidenceInput) { in.Plan = 0 }},
		{"an unknown plan", canonicalInput, func(in *execution.EvidenceInput) { in.Plan = 9 }},

		{"no head SHA", canonicalInput, func(in *execution.EvidenceInput) { in.HeadSHA = "" }},
		{"a short head SHA", canonicalInput, func(in *execution.EvidenceInput) { in.HeadSHA = headSHA[:39] }},
		{"a long head SHA", canonicalInput, func(in *execution.EvidenceInput) { in.HeadSHA = headSHA + "0" }},
		{"an upper-case head SHA", canonicalInput, func(in *execution.EvidenceInput) { in.HeadSHA = strings.ToUpper(headSHA) }},
		{"a head SHA that is not hexadecimal", canonicalInput, func(in *execution.EvidenceInput) { in.HeadSHA = strings.Repeat("g", 40) }},
		{"a head SHA with a newline", canonicalInput, func(in *execution.EvidenceInput) { in.HeadSHA = headSHA[:39] + "\n" }},

		{"a canonical run with a base SHA", canonicalInput, func(in *execution.EvidenceInput) { in.BaseSHA = baseSHA }},
		{"a receipt without a base SHA", receiptInput, func(in *execution.EvidenceInput) { in.BaseSHA = "" }},
		{"a receipt with a malformed base SHA", receiptInput, func(in *execution.EvidenceInput) { in.BaseSHA = baseSHA[:20] }},

		{"no manifest digest", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestDigest = artifact.Digest{} }},
		{"no declaration digest", canonicalInput, func(in *execution.EvidenceInput) { in.DeclarationDigest = artifact.Digest{} }},
		{"no executor binary digest", canonicalInput, func(in *execution.EvidenceInput) { in.ExecutorSHA256 = artifact.Digest{} }},
		{"no image digest", canonicalInput, func(in *execution.EvidenceInput) { in.ImageDigest = artifact.Digest{} }},

		{"no manifest source", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "" }},
		{"a lower-case version", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "v1" }},
		{"a version of zero", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "V0" }},
		{"a version with a leading zero", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "V01" }},
		{"a negative version", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "V-1" }},
		{"a version of ten digits", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "V1234567890" }},
		{"a version with a trailing space", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "V1 " }},
		{"a version with a trailing newline", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "V1\n" }},
		{"a proposal without a revision", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "P1" }},
		{"a proposal with an empty revision", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "P1-R" }},
		{"a proposal of zero", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "P0-R1" }},
		{"a revision of zero", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "P1-R0" }},
		{"a repeated revision", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "P1-R1-R1" }},
		{"an unknown prefix", canonicalInput, func(in *execution.EvidenceInput) { in.ManifestSource = "X1" }},

		{"no canonical level", canonicalInput, func(in *execution.EvidenceInput) { in.Canonical = 0 }},
		{"an unknown canonical level", canonicalInput, func(in *execution.EvidenceInput) { in.Canonical = 200 }},
		{"no receipt level", canonicalInput, func(in *execution.EvidenceInput) { in.Receipt = 0 }},
		{"an unknown receipt level", canonicalInput, func(in *execution.EvidenceInput) { in.Receipt = 200 }},

		{"no profile", canonicalInput, func(in *execution.EvidenceInput) { in.Profile = "" }},
		{"an upper-case profile", canonicalInput, func(in *execution.EvidenceInput) { in.Profile = "Go" }},
		{"a profile with a space", canonicalInput, func(in *execution.EvidenceInput) { in.Profile = "go " }},
		{"a profile that starts with a digit", canonicalInput, func(in *execution.EvidenceInput) { in.Profile = "1go" }},
		{"a profile with an underscore", canonicalInput, func(in *execution.EvidenceInput) { in.Profile = "go_lang" }},
		{"a profile of 33 characters", canonicalInput, func(in *execution.EvidenceInput) { in.Profile = long(33) }},

		{"no executor version", canonicalInput, func(in *execution.EvidenceInput) { in.ExecutorVersion = "" }},
		{"a blank executor version", canonicalInput, func(in *execution.EvidenceInput) { in.ExecutorVersion = " " }},
		{"an executor version with a space", canonicalInput, func(in *execution.EvidenceInput) { in.ExecutorVersion = "0.4 0" }},
		{"an executor version with a newline", canonicalInput, func(in *execution.EvidenceInput) { in.ExecutorVersion = "0.4.0\n" }},
		{"an executor version that starts with a dash", canonicalInput, func(in *execution.EvidenceInput) { in.ExecutorVersion = "-0.4.0" }},
		{"an executor version of 65 characters", canonicalInput, func(in *execution.EvidenceInput) { in.ExecutorVersion = long(65) }},

		{"no start time", canonicalInput, func(in *execution.EvidenceInput) { in.StartedAt = time.Time{} }},
		{"no end time", canonicalInput, func(in *execution.EvidenceInput) { in.FinishedAt = time.Time{} }},
		{"an end before the start", canonicalInput, func(in *execution.EvidenceInput) { in.FinishedAt = in.StartedAt.Add(-time.Nanosecond) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.base()
			tt.mutate(&in)
			binding, err := execution.NewEvidenceBinding(in)
			if !errors.Is(err, execution.ErrInvalidEvidence) {
				t.Errorf("NewEvidenceBinding error = %v; want ErrInvalidEvidence", err)
			}
			if !binding.IsZero() {
				t.Error("a refused binding is not zero")
			}
		})
	}
}

func TestEvidenceBindingAcceptsTheEdgesOfTheFieldRules(t *testing.T) {
	in := canonicalInput()
	in.Profile = "a" + strings.Repeat("-", 30) + "9"   // 32 characters
	in.ExecutorVersion = "v" + strings.Repeat("1", 63) // 64 characters
	in.Canonical, in.Receipt = execution.CanonicalLevelOff, execution.ReceiptLevelOff
	if _, err := execution.NewEvidenceBinding(in); err != nil {
		t.Errorf("the longest profile and version, with every level off: %v", err)
	}
	for _, version := range []string{"dev", "v1.2.3+build.5", "0.4.0-alpha.3", "1"} {
		in.ExecutorVersion = version
		if _, err := execution.NewEvidenceBinding(in); err != nil {
			t.Errorf("version %q: %v", version, err)
		}
	}
}
