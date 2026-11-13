package execution_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// jobOf is a canonical job spec that holds the profile label, and nothing else that Validate refuses.
func jobOf(profile string) execution.Spec {
	return execution.Spec{RepositoryID: 10, PullNumber: 1, Plan: execution.PlanCanonical, Profile: profile, HeadSHA: strings.Repeat("a", 40),
		GoverningDigest: artifact.Hash([]byte("manifest")), GoverningSource: "V1", DeclarationDigest: artifact.Hash([]byte("declaration")),
		CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff}
}

// specError is what a job that is created with the profile label is refused with, nil when it is accepted.
func specError(profile string) error {
	return execution.NewJob{At: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), Spec: jobOf(profile)}.Validate()
}

// A job runs the profiles of its declaration, and its profile is their label: the ids joined by "-" in strictly increasing byte order (docs/contracts/runtime.md,
// "The label"). The rows come from the closed set of ids, so that a profile added to it needs no edit here.
func TestSpecAcceptsAProfileLabel(t *testing.T) {
	ids := scope.ProfileIDs()
	labels := append([]string{"go", strings.Join(ids, "-")}, ids...)
	for _, label := range labels {
		if err := specError(label); err != nil {
			t.Errorf("the label %q: %v; want it accepted", label, err)
		}
	}
}

// The label is made of ids of the closed set: another language, no profile, a different case, and the constant list that an M2.1 runner reads for a list
// declaration, which is never a label (docs/contracts/runtime.md, "The lease, the result and the versions (M2.2)").
func TestSpecRefusesAnUnknownProfile(t *testing.T) {
	for _, label := range []string{"cpp" /* until X18 adds it to the closed set */, "python", "", "Go", "list", "go-python", "python-go"} {
		if err := specError(label); !errors.Is(err, execution.ErrInvalidRequest) {
			t.Errorf("the label %q: %v; want ErrInvalidRequest", label, err)
		}
	}
}

// The schema cannot say that the ids of a label are sorted and unique, so the application does (docs/contracts/persistence.md, "Migration 00023"). A label is at
// most 32 bytes: with one id in the closed set the rows over the cap are refused for the repeated id as well.
func TestSpecRefusesALabelThatIsNotSortedAndUnique(t *testing.T) {
	for _, label := range []string{"go-go", "go-", "-go", "go--go", "-", "go-go-go", strings.Repeat("go-", 11) + "go"} {
		if err := specError(label); !errors.Is(err, execution.ErrInvalidRequest) {
			t.Errorf("the label %q: %v; want ErrInvalidRequest", label, err)
		}
	}
	if ids := scope.ProfileIDs(); len(ids) > 1 {
		if reversed := ids[1] + "-" + ids[0]; specError(reversed) == nil {
			t.Errorf("the label %q is not sorted and was accepted", reversed)
		}
	}
}

// unavailableResult is a well-formed result of an executor that could not run the job, with the reason.
func unavailableResult(reason execution.Reason) execution.Result {
	started := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	return execution.Result{LeaseID: strings.Repeat("b", 32), Report: execution.Report{BodyDigest: artifact.Hash([]byte("body")), Status: execution.StateUnavailable, Reason: reason,
		ExecutorVersion: "v0.6.0", ExecutorSHA256: artifact.Hash([]byte("executor")), StartedAt: started, EndedAt: started.Add(time.Second)}}
}

// A runner that lacks the image of a profile names that profile, and the profile is one of the job's: a segment of its label, never a part of one.
func TestImageMissingNamesAProfileOfTheJob(t *testing.T) {
	for name, c := range map[string]struct {
		label  string
		reason execution.Reason
		want   bool
	}{
		"the profile of a go job":                      {"go", execution.ImageMissing("go"), true},
		"a well formed id that is not the job's":       {"go", execution.ImageMissing("cpp"), false},
		"an empty id":                                  {"go", "image-missing-", false},
		"an id in capitals":                            {"go", "image-missing-Go", false},
		"two ids":                                      {"go", "image-missing-go-cpp", false},
		"an id that starts with a digit":               {"go", "image-missing-1go", false},
		"an id of 17 bytes":                            {"go", execution.Reason("image-missing-" + strings.Repeat("a", 17)), false},
		"another reason is as it was":                  {"go", execution.ReasonToolchainMismatch, true},
		"the first profile of two":                     {"cpp-go", execution.ImageMissing("cpp"), true},
		"the second profile of two":                    {"cpp-go", execution.ImageMissing("go"), true},
		"a part of a segment is no profile":            {"cpp-go", execution.ImageMissing("c"), false},
		"a profile of neither":                         {"cpp-go", execution.ImageMissing("python"), false},
		"the label is not a profile":                   {"cpp-go", "image-missing-cpp-go", false},
		"an id as long as the pattern allows is known": {"abcdefghijklmnop", execution.ImageMissing("abcdefghijklmnop"), true},
	} {
		err := unavailableResult(c.reason).Validate(jobOf(c.label))
		if c.want && err != nil {
			t.Errorf("%s: %q for the job %q: %v; want it valid", name, c.reason, c.label, err)
		}
		if !c.want && !errors.Is(err, execution.ErrInvalidRequest) {
			t.Errorf("%s: %q for the job %q: %v; want ErrInvalidRequest", name, c.reason, c.label, err)
		}
	}

	complete := unavailableResult(execution.ImageMissing("go"))
	complete.Status, complete.ImageDigest, complete.Toolchain, complete.Runtime = execution.StateComplete, artifact.Hash([]byte("image")), "go1.27.4", "podman 6.1.4"
	if err := complete.Validate(jobOf("go")); !errors.Is(err, execution.ErrInvalidRequest) {
		t.Errorf("a complete result with the reason: %v; want ErrInvalidRequest", err)
	}
}
