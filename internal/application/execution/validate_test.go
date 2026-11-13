package execution_test

import (
	"errors"
	"fmt"
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

// completeResult is a well-formed complete result of the image "image" and the toolchain go1.27.4, with no part and no removed test.
func completeResult() execution.Result {
	r := unavailableResult("")
	r.Status, r.Reason, r.ImageDigest, r.Toolchain, r.Runtime = execution.StateComplete, "", artifact.Hash([]byte("image")), "go1.27.4", "podman 6.1.4"
	return r
}

// partOf is a part of the same image and toolchain as completeResult, so that the first part of a list equals the row.
func partOf(profile, folder string) execution.Part {
	return execution.Part{Profile: profile, Folder: folder, ImageDigest: artifact.Hash([]byte("image")), Toolchain: "go1.27.4"}
}

// partsOf is n parts of the profile in the folders f1, f2, and so on, the first in the root.
func partsOf(profile string, n int) []execution.Part {
	parts := make([]execution.Part, n)
	for i := range parts {
		parts[i] = partOf(profile, fmt.Sprintf("f%d", i))
	}
	parts[0].Folder = "."
	return parts
}

func receiptJob(profile string) execution.Spec {
	job := jobOf(profile)
	job.Plan, job.BaseSHA = execution.PlanReceipt, strings.Repeat("b", 40)
	return job
}

func longText(n int) string { return strings.Repeat("a", n) }

// The parts of a complete result are the ones of the job's label, in any number up to 8, and an M2.1 runner sends none (docs/contracts/persistence.md, "Store
// operations", Complete). Two parts may share a profile: two Go folders.
func TestResultAcceptsThePartsOfTheJob(t *testing.T) {
	for name, c := range map[string]struct {
		label string
		parts []execution.Part
	}{
		"no part, as an M2.1 runner sends":        {"go", nil},
		"the one part of a go job":                {"go", partsOf("go", 1)},
		"two parts of one profile in two folders": {"go", partsOf("go", 2)},
		"eight parts, the bound":                  {"go", partsOf("go", 8)},
		"the two profiles of a label":             {"cpp-go", []execution.Part{partOf("cpp", "."), partOf("go", "svc")}},
		"the profiles in another order":           {"cpp-go", []execution.Part{partOf("go", "svc"), partOf("cpp", ".")}},
	} {
		r := completeResult()
		r.Parts = c.parts
		if err := r.Validate(jobOf(c.label)); err != nil {
			t.Errorf("%s: %v; want it valid", name, err)
		}
	}
}

// A result with parts that are not the job's writes nothing: the store checks each against the label and the row (the wire checks the order and the images
// against the declaration). The rule for an empty list needs a label of more than go, which the closed set of profiles has not yet (X18): the two rows with
// no part run Validate with a Spec that is not validated, as the image-missing test above does.
func TestResultRefusesPartsNotOfTheJob(t *testing.T) {
	for name, c := range map[string]struct {
		label  string
		mutate func(*execution.Result)
	}{
		"a part of another profile":               {"go", func(r *execution.Result) { r.Parts = []execution.Part{partOf("cpp", ".")} }},
		"a part of a profile that does not exist": {"cpp-go", func(r *execution.Result) { r.Parts = []execution.Part{partOf("cpp", "."), partOf("python", "x")} }},
		"a part that holds the whole label":       {"cpp-go", func(r *execution.Result) { r.Parts = []execution.Part{partOf("cpp-go", ".")} }},
		"a second part of another profile":        {"go", func(r *execution.Result) { r.Parts = []execution.Part{partOf("go", "."), partOf("cpp", "x")} }},
		"nine parts":                              {"go", func(r *execution.Result) { r.Parts = partsOf("go", 9) }},
		"parts on an unavailable result": {"go", func(r *execution.Result) {
			*r = unavailableResult(execution.ReasonExecutorError)
			r.Parts = partsOf("go", 1)
		}},
		"a first part of another image": {"go", func(r *execution.Result) {
			r.Parts = partsOf("go", 2)
			r.Parts[0].ImageDigest = artifact.Hash([]byte("other"))
		}},
		"a first part of another toolchain":            {"go", func(r *execution.Result) { r.Parts = partsOf("go", 2); r.Parts[0].Toolchain = "go1.27.3" }},
		"a part without an image":                      {"go", func(r *execution.Result) { r.Parts = partsOf("go", 2); r.Parts[1].ImageDigest = artifact.Digest{} }},
		"a part without a toolchain":                   {"go", func(r *execution.Result) { r.Parts = partsOf("go", 2); r.Parts[1].Toolchain = "" }},
		"a toolchain of 65 bytes":                      {"go", func(r *execution.Result) { r.Parts = partsOf("go", 2); r.Parts[1].Toolchain = longText(65) }},
		"a part without a folder":                      {"go", func(r *execution.Result) { r.Parts = partsOf("go", 2); r.Parts[1].Folder = "" }},
		"a folder of 513 bytes":                        {"go", func(r *execution.Result) { r.Parts = partsOf("go", 2); r.Parts[1].Folder = longText(513) }},
		"no part for a job of two profiles":            {"cpp-go", func(r *execution.Result) {}},
		"no part for a job of one profile that is not": {"cpp", func(r *execution.Result) {}},
	} {
		r := completeResult()
		c.mutate(&r)
		if err := r.Validate(jobOf(c.label)); !errors.Is(err, execution.ErrInvalidRequest) {
			t.Errorf("%s: %v; want ErrInvalidRequest", name, err)
		}
	}
	// The limits are accepted: the toolchain of 64 bytes and the folder of 512, in a part that is not the first as in the first.
	r := completeResult()
	r.Parts = partsOf("go", 2)
	r.Parts[1].Toolchain, r.Parts[1].Folder = longText(64), longText(512)
	if err := r.Validate(jobOf("go")); err != nil {
		t.Errorf("a toolchain of 64 bytes and a folder of 512: %v; want it valid", err)
	}
}

// removedOf is n tests that a change removes, which differ by name.
func removedOf(n int) []execution.Removed {
	list := make([]execution.Removed, n)
	for i := range list {
		list[i] = execution.Removed{Package: "pkg", Name: fmt.Sprintf("TestRemoved%04d", i), File: "pkg/removed_test.go"}
	}
	return list
}

// The tests that the change of a receipt removes come with a complete receipt, and with it only (docs/contracts/persistence.md, "Store operations", Complete).
func TestResultRefusesRemovedThatTheJobCannotHave(t *testing.T) {
	test := execution.Test{Package: "pkg", Name: "TestKept", File: "pkg/kept_test.go", Kind: execution.KindAdded, HeadRuns: []string{"pass"}, BaseRuns: []string{"build-error"}, State: "new_api"}
	valid := func() execution.Result {
		r := completeResult()
		r.Tests, r.Removed = []execution.Test{test}, removedOf(2)
		return r
	}
	if err := valid().Validate(receiptJob("go")); err != nil {
		t.Fatalf("a complete receipt with removed tests: %v; want it valid", err)
	}
	for name, c := range map[string]struct {
		job    execution.Spec
		mutate func(*execution.Result)
	}{
		"removed on a canonical job": {jobOf("go"), func(r *execution.Result) {
			r.Tests = []execution.Test{{Package: "pkg", Name: "TestKept", File: "pkg/kept_test.go", Kind: execution.KindDeclared, HeadRuns: []string{"pass"}, State: "pass"}}
		}},
		"removed on an unavailable result": {receiptJob("go"), func(r *execution.Result) {
			*r = unavailableResult(execution.ReasonExecutorError)
			r.Removed = removedOf(1)
		}},
		"a pair twice": {receiptJob("go"), func(r *execution.Result) { r.Removed = append(r.Removed, r.Removed[0]) }},
		"a pair twice with another file": {receiptJob("go"), func(r *execution.Result) {
			r.Removed = append(r.Removed, execution.Removed{Package: r.Removed[0].Package, Name: r.Removed[0].Name, File: "pkg/other_test.go"})
		}},
		"an entry that is a test row": {receiptJob("go"), func(r *execution.Result) {
			r.Removed[1] = execution.Removed{Package: test.Package, Name: test.Name, File: test.File}
		}},
		"1001 entries":           {receiptJob("go"), func(r *execution.Result) { r.Removed = removedOf(1001) }},
		"no package":             {receiptJob("go"), func(r *execution.Result) { r.Removed[1].Package = "" }},
		"a package of 513 bytes": {receiptJob("go"), func(r *execution.Result) { r.Removed[1].Package = longText(513) }},
		"no name":                {receiptJob("go"), func(r *execution.Result) { r.Removed[1].Name = "" }},
		"a name of 201 bytes":    {receiptJob("go"), func(r *execution.Result) { r.Removed[1].Name = longText(201) }},
		"no file":                {receiptJob("go"), func(r *execution.Result) { r.Removed[1].File = "" }},
		"a file of 1025 bytes":   {receiptJob("go"), func(r *execution.Result) { r.Removed[1].File = longText(1025) }},
	} {
		r := valid()
		c.mutate(&r)
		if err := r.Validate(c.job); !errors.Is(err, execution.ErrInvalidRequest) {
			t.Errorf("%s: %v; want ErrInvalidRequest", name, err)
		}
	}
	// The limits are accepted: 1000 entries, and the longest package, name and file.
	r := valid()
	r.Removed = removedOf(1000)
	r.Removed[999] = execution.Removed{Package: longText(512), Name: "T" + longText(199), File: longText(1024)}
	if err := r.Validate(receiptJob("go")); err != nil {
		t.Errorf("1000 entries of the longest fields: %v; want it valid", err)
	}
}
