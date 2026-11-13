package app

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// What the lease says of the profiles of a job (docs/contracts/runtime.md, "The lease, the result and the versions (M2.2)").

const (
	profilesScalar = "version: 1\nexecution:\n  profile: go\n  canonical: required\n"
	// profilesList names its entries out of the contract's order and holds one that is protected only: the lease lists the two that run, root first.
	profilesList = "version: 1\nexecution:\n  profiles:\n    - profile: go\n      folder: tools/gen\n    - profile: go\n    - profile: go\n      folder: docs\n      run: false\n  canonical: required\n"
)

type profilesPart struct {
	ID     string `json:"id"`
	Image  string `json:"image"`
	Folder string `json:"folder"`
}

// profilesAnswer is what these tests read of a lease answer.
type profilesAnswer struct {
	Profile struct {
		ID    string `json:"id"`
		Image string `json:"image"`
	} `json:"profile"`
	Profiles    []profilesPart `json:"profiles"`
	Declaration *string        `json:"declaration"`
}

// profilesJob is a pending canonical job over the canonical declaration canonical, with a governing manifest that holds the declaration governing, or none
// when it is empty. The two differ for an approved pull request that edits .suiteward.yml.
func (e *execTestEnv) profilesJob(canonical, governing string) execution.Job {
	e.t.Helper()
	files := map[string]string{"contract.go": "package contract\n"}
	if governing != "" {
		files[declarationPath] = governing
	}
	return e.enqueue(execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(1), GoverningDigest: e.manifest(files).Digest(),
		GoverningSource: "V1", DeclarationDigest: e.put(canonical), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff})
}

func (e *execTestEnv) profilesLease() profilesAnswer {
	e.t.Helper()
	got := e.lease()
	if got.status != http.StatusOK {
		e.t.Fatalf("lease = %d %s; want 200", got.status, got.text())
	}
	var answer profilesAnswer
	if err := json.Unmarshal(got.body, &answer); err != nil {
		e.t.Fatalf("lease answer: %v", err)
	}
	return answer
}

func TestLeaseListsEachRunningProfile(t *testing.T) {
	e := newExecEnv(t)
	e.profilesJob(profilesList, profilesList)

	got := e.profilesLease()

	want := []profilesPart{{"go", goprofile.Image, "."}, {"go", goprofile.Image, "tools/gen"}}
	if !slices.Equal(got.Profiles, want) {
		t.Errorf("profiles = %+v; want the two running entries in contract order, with the pinned image and the folders, and not the protected-only one: %+v", got.Profiles, want)
	}
}

func TestLeaseOfALegacyDeclarationKeepsProfile(t *testing.T) {
	e := newExecEnv(t)
	e.profilesJob(profilesScalar, profilesScalar)

	got := e.profilesLease()

	if got.Profile.ID != "go" || got.Profile.Image != goprofile.Image {
		t.Errorf("profile = %+v; want go with its pinned image, as an M2.1 service answers", got.Profile)
	}
	if want := []profilesPart{{"go", goprofile.Image, "."}}; !slices.Equal(got.Profiles, want) {
		t.Errorf("profiles = %+v; want the scalar form to list one entry, Go at the root: %+v", got.Profiles, want)
	}
}

func TestLeaseOfAListDeclarationSaysList(t *testing.T) {
	e := newExecEnv(t)
	job := e.profilesJob(profilesList, profilesList)

	got := e.profilesLease()

	if got.Profile.ID != "list" || got.Profile.Image != got.Profiles[0].Image || got.Profile.Image != goprofile.Image {
		t.Errorf("profile = %+v; want list with the image of the first entry of profiles, so that an M2.1 runner refuses it at once", got.Profile)
	}
	if stored := e.job(job.ID); stored.Profile != "go" {
		t.Errorf("stored profile = %q; want the label go: list is never a label", stored.Profile)
	}
}

// The declaration that the lease ships is the governing one, and an M2.1 runner parses it strictly: the list form in it is what that runner cannot read.
func TestLeaseSaysListWhenOnlyTheGoverningDeclarationIsAList(t *testing.T) {
	e := newExecEnv(t)
	e.profilesJob(profilesScalar, profilesList)

	got := e.profilesLease()

	if got.Profile.ID != "list" {
		t.Errorf("profile.id = %q; want list, since the governing declaration is a list", got.Profile.ID)
	}
	if want := []profilesPart{{"go", goprofile.Image, "."}}; !slices.Equal(got.Profiles, want) {
		t.Errorf("profiles = %+v; want those of the canonical declaration, which is the scalar form: %+v", got.Profiles, want)
	}
}

func TestLeaseSaysListWhenOnlyTheCanonicalDeclarationIsAList(t *testing.T) {
	e := newExecEnv(t)
	e.profilesJob(profilesList, profilesScalar)

	got := e.profilesLease()

	if got.Profile.ID != "list" {
		t.Errorf("profile.id = %q; want list, since the canonical declaration is a list", got.Profile.ID)
	}
	if want := []profilesPart{{"go", goprofile.Image, "."}, {"go", goprofile.Image, "tools/gen"}}; !slices.Equal(got.Profiles, want) {
		t.Errorf("profiles = %+v; want those of the canonical declaration, not of the declaration bytes that the lease ships: %+v", got.Profiles, want)
	}
}

func TestLeaseOfAManifestWithoutADeclarationIsNotAList(t *testing.T) {
	e := newExecEnv(t)
	e.profilesJob(profilesScalar, "")

	got := e.profilesLease()

	if got.Profile.ID != "go" || got.Declaration != nil || len(got.Profiles) != 1 {
		t.Errorf("profile %+v, declaration %v, profiles %+v; want go, no declaration and one entry", got.Profile, got.Declaration, got.Profiles)
	}
}

// swappedContent answers other bytes than those of a digest, as a store that does not check what it holds would.
type swappedContent struct {
	executorContent
	bytes map[artifact.Digest][]byte
}

func (s *swappedContent) Read(ctx context.Context, digest artifact.Digest) ([]byte, error) {
	if raw, swapped := s.bytes[digest]; swapped {
		return raw, nil
	}
	return s.executorContent.Read(ctx, digest)
}

// What the declarations of a job must be is the checks job's work, so a job that fails these is the service's own fault: a 500, nothing written, the job still ready.
func TestLeaseAnswers500WhenTheDeclarationsOfTheJobAreNotWhatTheServiceKept(t *testing.T) {
	t.Run("the canonical declaration runs nothing", func(t *testing.T) {
		e := newExecEnv(t)
		job := e.profilesJob("version: 1\n", profilesScalar)
		e.wantUnpreparable(job)
	})
	t.Run("the canonical declaration is not in the store", func(t *testing.T) {
		e := newExecEnv(t)
		job := e.enqueue(execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(1), GoverningDigest: e.manifest(defaultFiles).Digest(),
			GoverningSource: "V1", DeclarationDigest: execDigest("a declaration nobody stored"), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff})
		e.wantUnpreparable(job)
	})
	t.Run("the canonical declaration is not what its digest names", func(t *testing.T) {
		swapped := &swappedContent{bytes: map[artifact.Digest][]byte{}}
		e := newExecEnv(t, func(d *executorDeps) { swapped.executorContent, d.content = d.content, swapped })
		job := e.profilesJob(profilesScalar, profilesList)
		swapped.bytes[job.DeclarationDigest] = []byte(profilesList) // a declaration that parses and runs, so that only the digest is wrong
		e.wantUnpreparable(job)
	})
	t.Run("the governing declaration is not a declaration", func(t *testing.T) {
		e := newExecEnv(t)
		job := e.profilesJob(profilesScalar, "execution: {}\n") // no version
		e.wantUnpreparable(job)
	})
}

func (e *execTestEnv) wantUnpreparable(job execution.Job) {
	e.t.Helper()
	e.wantError(e.lease(), http.StatusInternalServerError, "internal")
	if stored := e.job(job.ID); stored.State != execution.StatePending || stored.Attempts != 0 || stored.Reason != "" {
		e.t.Errorf("job = %+v; want it untouched", stored)
	}
	e.wantErrorLoggedForJob(job.ID)
}

// The plan makes the jobs (docs/contracts/runtime.md, "The declaration (M2.2)" and "What the checks job does for execution"); these two hold what the lease relies on.

func TestNoJobWhenEveryProfileIsProtectedOnly(t *testing.T) {
	decl := "version: 1\ninclude: [/tests/**]\nexecution:\n  profiles:\n    - profile: go\n      run: false\n"
	w := newExecWorld(t, decl)

	w.execPull(execHeadFiles(decl, "v2"))

	w.noJobs(5, cjHead2, execution.PlanCanonical)
	w.noJobs(5, cjHead2, execution.PlanReceipt)
	if open := w.openJobs(); len(open) != 0 {
		t.Errorf("%d open jobs (%+v); want none, since no entry runs", len(open), open)
	}
}

// Two Go folders are one label, go. The label of a list that also runs another profile joins their ids, for example cpp-go: X18 adds that case with the profile.
func TestJobLabelIsTheSortedRunningProfiles(t *testing.T) {
	decl := "version: 1\ninclude: [/tests/**]\nexecution:\n  profiles:\n    - profile: go\n      folder: tools/gen\n    - profile: go\n  canonical: required\n  receipt: required\n"
	w := newExecWorld(t, decl)

	w.execPull(execHeadFiles(decl, "v2"))

	for _, plan := range []execution.Plan{execution.PlanCanonical, execution.PlanReceipt} {
		if got := w.onlyJob(5, cjHead2, plan).Profile; got != "go" {
			t.Errorf("the %s job has the profile %q; want the label go", plan, got)
		}
	}
}
