// Package ownertest provides an in-memory owner.Store and the conformance
// suite that every owner.Store must pass.
package ownertest

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// SeedSuite creates a Suite of project governed by revision, which the owner
// store cannot do itself: Suites belong to governance. The conformance suite
// calls it only for a project of the current enrollment.
type SeedSuite func(project contract.ProjectID, revision contract.PolicyRevisionID) error

const (
	ownerA = int64(884695)
	ownerB = int64(5150)

	srcA = contract.SourceCommandID("github-comment:10/1001")
	srcB = contract.SourceCommandID("github-comment:20/2002")
)

// otTime is a time at microsecond precision, which is all PostgreSQL stores,
// n minutes after a fixed origin.
func otTime(n int) time.Time {
	return time.Date(2026, 10, 13, 12, 0, 0, 123456000, time.UTC).Add(time.Duration(n) * time.Minute)
}

func otSecret(b byte) owner.Secret { return bytes.Repeat([]byte{b}, 20) }

func otProject(id int64, check string) owner.ProjectPolicy {
	return owner.ProjectPolicy{RepositoryID: id, Repo: "acme/repo-" + string(rune('a'+id)), TestCheck: check}
}

func otEnrollment(account int64, secret byte, step int64, projects ...owner.ProjectPolicy) owner.Enrollment {
	return owner.Enrollment{Owner: owner.Owner{AccountID: account, Login: "login"}, Secret: otSecret(secret), Projects: projects, Step: step,
		Tier: owner.TierIsolated, EnrolledAt: otTime(1)}
}

func otState(e owner.Enrollment, generation int64) owner.State {
	projects := append([]owner.ProjectPolicy(nil), e.Projects...)
	for i := range projects { // ordered by repository id
		for j := i + 1; j < len(projects); j++ {
			if projects[j].RepositoryID < projects[i].RepositoryID {
				projects[i], projects[j] = projects[j], projects[i]
			}
		}
	}
	return owner.State{Owner: e.Owner, Secret: e.Secret, Projects: projects, Tier: e.Tier, PolicyRevision: owner.RevisionID(generation), EnrolledAt: e.EnrolledAt}
}

type otEnv struct {
	t     *testing.T
	ctx   context.Context
	store owner.Store
	seed  SeedSuite
}

func (e *otEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *otEnv) requireErr(err, want error) {
	e.t.Helper()
	if !errors.Is(err, want) {
		e.t.Fatalf("error = %v; want %v", err, want)
	}
}

// requireState checks Current against want, or that nothing is enrolled when want is nil.
func (e *otEnv) requireState(want *owner.State) {
	e.t.Helper()
	got, found, err := e.store.Current(e.ctx)
	e.must(err)
	if want == nil {
		if found {
			e.t.Fatalf("Current found %+v; want nothing enrolled", got)
		}
		return
	}
	if !found || !reflect.DeepEqual(got, *want) {
		e.t.Fatalf("Current = %+v, found %v;\nwant %+v", got, found, *want)
	}
}

func (e *otEnv) requireProtection(want ...owner.ProtectionResult) {
	e.t.Helper()
	got, err := e.store.Protection(e.ctx)
	e.must(err)
	if got == nil || len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		e.t.Fatalf("Protection = %+v; want %+v (a non-nil list)", got, want)
	}
}

// RunConformance runs the owner.Store conformance suite against stores made
// by newStore, which returns the store and the way to give it a Suite.
func RunConformance(t *testing.T, newStore func(*testing.T) (owner.Store, SeedSuite)) {
	cases := []struct {
		name string
		run  func(*otEnv)
	}{
		{"enroll and read back", otEnrollRoundTrip},
		{"re-enroll replaces the secret and increments the generation", otReEnroll},
		{"re-enroll with another owner while a project has a Suite is rejected", otReEnrollOtherOwner},
		{"an invalid enrollment writes nothing", otInvalidEnrollment},
		{"a used step is rejected by Enroll and by ConsumeStep", otUsedStep},
		{"ConsumeStep without an owner", otConsumeWithoutOwner},
		{"a step consumed by the same source again is success", otConsumeSameSource},
		{"Enroll consumes its step with the source of its generation", otEnrollConsumesStep},
		{"concurrent ConsumeStep of one step by two sources: exactly one source wins", otConcurrentConsume},
		{"a protection result is replaced and listed", otProtectionReplaced},
		{"an invalid protection result is not recorded", otInvalidProtection},
		{"a canceled context writes nothing", otCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, seed := newStore(t)
			tc.run(&otEnv{t: t, ctx: t.Context(), store: store, seed: seed})
		})
	}
}

func otEnrollRoundTrip(e *otEnv) {
	e.requireState(nil)
	enrollment := otEnrollment(ownerA, 1, 100, otProject(20, "ci / test"), otProject(10, "build"))
	e.must(e.store.Enroll(e.ctx, enrollment))
	e.requireState(ptr(otState(enrollment, 1)))
	// The store keeps its own copy of the secret.
	enrollment.Secret[0] ^= 0xFF
	got, _, err := e.store.Current(e.ctx)
	e.must(err)
	got.Secret[1] ^= 0xFF
	e.requireState(ptr(otState(otEnrollment(ownerA, 1, 100, otProject(20, "ci / test"), otProject(10, "build")), 1)))
}

func otReEnroll(e *otEnv) {
	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build"), otProject(20, "ci"))))
	second := otEnrollment(ownerA, 2, 101, otProject(10, "test"), otProject(30, "ci"))
	second.Tier = owner.TierCoLocated
	second.Owner.Login = "renamed"
	e.must(e.store.Enroll(e.ctx, second))
	e.requireState(ptr(otState(second, 2)))
	third := otEnrollment(ownerA, 3, 102, otProject(10, "test"))
	e.must(e.store.Enroll(e.ctx, third))
	e.requireState(ptr(otState(third, 3)))
}

func otReEnrollOtherOwner(e *otEnv) {
	first := otEnrollment(ownerA, 1, 100, otProject(10, "build"), otProject(20, "ci"))
	e.must(e.store.Enroll(e.ctx, first))
	e.must(e.seed(owner.ProjectID(20), owner.RevisionID(1)))

	e.requireErr(e.store.Enroll(e.ctx, otEnrollment(ownerB, 2, 100, otProject(10, "build"), otProject(20, "ci"))), owner.ErrInvalidState)
	e.requireState(ptr(otState(first, 1)))

	// The same owner may re-enroll over a Suite.
	same := otEnrollment(ownerA, 3, 101, otProject(10, "build"), otProject(20, "ci"))
	e.must(e.store.Enroll(e.ctx, same))
	e.requireState(ptr(otState(same, 2)))

	// Another owner may enroll projects that have no Suite; the rejected attempt consumed step 100 for nobody.
	changed := otEnrollment(ownerB, 4, 100, otProject(10, "build"), otProject(30, "ci"))
	e.must(e.store.Enroll(e.ctx, changed))
	e.requireState(ptr(otState(changed, 3)))
}

func otInvalidEnrollment(e *otEnv) {
	valid := func() owner.Enrollment { return otEnrollment(ownerA, 1, 100, otProject(10, "build")) }
	mutations := map[string]func(*owner.Enrollment){
		"short secret":       func(x *owner.Enrollment) { x.Secret = x.Secret[:19] },
		"long secret":        func(x *owner.Enrollment) { x.Secret = append(x.Secret, 0) },
		"account id":         func(x *owner.Enrollment) { x.Owner.AccountID = 0 },
		"repository id":      func(x *owner.Enrollment) { x.Projects[0].RepositoryID = 0 },
		"empty test check":   func(x *owner.Enrollment) { x.Projects[0].TestCheck = "" },
		"long test check":    func(x *owner.Enrollment) { x.Projects[0].TestCheck = strings.Repeat("c", owner.MaxTestCheckBytes+1) },
		"unknown tier":       func(x *owner.Enrollment) { x.Tier = "cloud" },
		"no projects":        func(x *owner.Enrollment) { x.Projects = nil },
		"duplicate project":  func(x *owner.Enrollment) { x.Projects = append(x.Projects, x.Projects[0]) },
		"zero enrolled time": func(x *owner.Enrollment) { x.EnrolledAt = time.Time{} },
	}
	reject := func() {
		for name, mutate := range mutations {
			x := valid()
			x.Step = 777
			mutate(&x)
			e.t.Run(name, func(t *testing.T) {
				sub := *e
				sub.t = t
				sub.requireErr(sub.store.Enroll(sub.ctx, x), owner.ErrInvalidState)
			})
		}
	}
	reject()
	e.requireState(nil)
	first := valid()
	e.must(e.store.Enroll(e.ctx, first))
	reject()
	e.requireState(ptr(otState(first, 1)))
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 777, otTime(2), srcA)) // no rejected enrollment consumed its step
}

func otUsedStep(e *otEnv) {
	first := otEnrollment(ownerA, 1, 100, otProject(10, "build"))
	e.must(e.store.Enroll(e.ctx, first))
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 100, otTime(2), srcA), owner.ErrStepUsed) // Enroll consumed its step
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 200, otTime(2), srcA))
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 200, otTime(3), srcB), owner.ErrStepUsed)

	// A used step enrolls nothing: not the generation, the secret nor the projects.
	e.requireErr(e.store.Enroll(e.ctx, otEnrollment(ownerA, 2, 200, otProject(10, "test"), otProject(20, "ci"))), owner.ErrStepUsed)
	e.requireErr(e.store.Enroll(e.ctx, otEnrollment(ownerA, 2, 100, otProject(10, "test"), otProject(20, "ci"))), owner.ErrStepUsed)
	e.requireState(ptr(otState(first, 1)))
	next := otEnrollment(ownerA, 3, 300, otProject(10, "test"))
	e.must(e.store.Enroll(e.ctx, next))
	e.requireState(ptr(otState(next, 2)))

	// A step is single use per owner: another owner has its own.
	other := otEnrollment(ownerB, 4, 100, otProject(10, "test"))
	e.must(e.store.Enroll(e.ctx, other))
	e.requireState(ptr(otState(other, 3)))
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerB, 100, otTime(4), srcB), owner.ErrStepUsed)
}

func otConsumeWithoutOwner(e *otEnv) {
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 1, otTime(1), srcA), owner.ErrNotEnrolled)
	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build"))))
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerB, 1, otTime(1), srcA), owner.ErrNotEnrolled)
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 1, time.Time{}, srcA), owner.ErrInvalidState)
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 1, otTime(1), ""), owner.ErrInvalidState)
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 1, otTime(1), srcA)) // no failed attempt consumed it
}

func otConsumeSameSource(e *otEnv) {
	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build"))))
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 200, otTime(2), srcA))
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 200, otTime(3), srcA)) // a retry after a crash
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 200, otTime(3), srcB), owner.ErrStepUsed)
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 200, otTime(4), srcA)) // the failed attempt of another source changed nothing
}

func otEnrollConsumesStep(e *otEnv) {
	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build"))))
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 100, otTime(2), owner.SetupSource(1)))
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 100, otTime(2), owner.SetupSource(2)), owner.ErrStepUsed)
	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 2, 101, otProject(10, "build"))))
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 101, otTime(2), owner.SetupSource(2)))
	e.requireErr(e.store.ConsumeStep(e.ctx, ownerA, 101, otTime(2), owner.SetupSource(1)), owner.ErrStepUsed)
}

func otConcurrentConsume(e *otEnv) {
	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build"))))
	const callers = 8
	type outcome struct {
		source contract.SourceCommandID
		err    error
	}
	results := make(chan outcome, callers)
	var ready, done sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	for i := range callers {
		source := srcA
		if i%2 == 1 {
			source = srcB
		}
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			results <- outcome{source, e.store.ConsumeStep(e.ctx, ownerA, 500, otTime(5), source)}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(results)
	var winner contract.SourceCommandID
	wins, used := 0, 0
	for r := range results {
		switch {
		case r.err == nil:
			if winner != "" && winner != r.source {
				e.t.Fatalf("both %q and %q consumed the step", winner, r.source)
			}
			winner = r.source
			wins++
		case errors.Is(r.err, owner.ErrStepUsed):
			used++
		default:
			e.t.Fatalf("ConsumeStep = %v; want success or ErrStepUsed", r.err)
		}
	}
	// The winning source's callers all succeed (idempotent); the other source's all get ErrStepUsed.
	if wins != callers/2 || used != callers/2 {
		e.t.Fatalf("%d callers succeeded and %d got ErrStepUsed; want %d of each", wins, used, callers/2)
	}
}

func otProtectionReplaced(e *otEnv) {
	e.requireProtection() // before setup, and independent of it
	unknown := owner.ProtectionResult{RepositoryID: 30, Result: owner.ResultUnknown, CheckedAt: otTime(1)}
	findings := owner.ProtectionResult{RepositoryID: 10, Result: owner.ResultFindings, Findings: []string{owner.FindingAdminBypass, owner.FindingNotUpToDate},
		Caveats: []string{owner.CaveatRulesetBypass}, CheckedAt: otTime(2)}
	enforced := owner.ProtectionResult{RepositoryID: 20, Result: owner.ResultEnforced, CheckedAt: otTime(3)}
	for _, r := range []owner.ProtectionResult{unknown, findings, enforced} {
		e.must(e.store.RecordProtection(e.ctx, r))
	}
	normalize := func(r owner.ProtectionResult) owner.ProtectionResult {
		if r.Findings == nil {
			r.Findings = []string{}
		}
		if r.Caveats == nil {
			r.Caveats = []string{}
		}
		return r
	}
	e.requireProtection(normalize(findings), normalize(enforced), normalize(unknown))

	replaced := owner.ProtectionResult{RepositoryID: 20, Result: owner.ResultFindings, Findings: []string{owner.FindingMissingTestCheck}, CheckedAt: otTime(4)}
	e.must(e.store.RecordProtection(e.ctx, replaced))
	e.requireProtection(normalize(findings), normalize(replaced), normalize(unknown))
	e.must(e.store.RecordProtection(e.ctx, enforced))
	e.requireProtection(normalize(findings), normalize(enforced), normalize(unknown))

	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build")))) // enrollment leaves results alone
	e.requireProtection(normalize(findings), normalize(enforced), normalize(unknown))
}

func otInvalidProtection(e *otEnv) {
	kept := owner.ProtectionResult{RepositoryID: 10, Result: owner.ResultEnforced, CheckedAt: otTime(1)}
	e.must(e.store.RecordProtection(e.ctx, kept))
	bad := []owner.ProtectionResult{
		{RepositoryID: 10, Result: "fine", CheckedAt: otTime(2)},
		{RepositoryID: 0, Result: owner.ResultEnforced, CheckedAt: otTime(2)},
		{RepositoryID: 10, Result: owner.ResultFindings, CheckedAt: otTime(2)},
		{RepositoryID: 10, Result: owner.ResultEnforced, Findings: []string{owner.FindingAdminBypass}, CheckedAt: otTime(2)},
		{RepositoryID: 10, Result: owner.ResultFindings, Findings: []string{"other"}, CheckedAt: otTime(2)},
		{RepositoryID: 10, Result: owner.ResultFindings, Findings: []string{owner.FindingNotUpToDate, owner.FindingAdminBypass}, CheckedAt: otTime(2)},
		{RepositoryID: 10, Result: owner.ResultEnforced, Caveats: []string{"other"}, CheckedAt: otTime(2)},
		{RepositoryID: 10, Result: owner.ResultEnforced},
	}
	for _, r := range bad {
		e.requireErr(e.store.RecordProtection(e.ctx, r), owner.ErrInvalidState)
	}
	kept.Findings, kept.Caveats = []string{}, []string{}
	e.requireProtection(kept)
}

func otCanceled(e *otEnv) {
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	e.requireErr(e.store.Enroll(ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build"))), context.Canceled)
	_, _, err := e.store.Current(ctx)
	e.requireErr(err, context.Canceled)
	e.requireErr(e.store.RecordProtection(ctx, owner.ProtectionResult{RepositoryID: 10, Result: owner.ResultUnknown, CheckedAt: otTime(1)}), context.Canceled)
	_, err = e.store.Protection(ctx)
	e.requireErr(err, context.Canceled)
	e.requireState(nil)
	e.requireProtection()
	e.must(e.store.Enroll(e.ctx, otEnrollment(ownerA, 1, 100, otProject(10, "build"))))
	e.requireErr(e.store.ConsumeStep(ctx, ownerA, 200, otTime(1), srcA), context.Canceled)
	e.must(e.store.ConsumeStep(e.ctx, ownerA, 200, otTime(1), srcA))
}

func ptr[T any](v T) *T { return &v }
