package governancetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The merge cases about atomicity, concurrency and the two Tx methods themselves.

func mcRefusalPrecedence(e *cfEnv) {
	e.mcSeed(false)
	for _, test := range []struct {
		pr   cfCandidate
		edit func(*governance.MergeRequest)
		want governance.MergeKind
	}{
		{mcCandidate("not-target", "V1", "v2"), func(r *governance.MergeRequest) { r.Target = "branch:other" }, governance.MergeNotTarget},
		{mcCandidate("early", "V1", "v2"), func(r *governance.MergeRequest) { r.ObservedAt = cfRecordedAt.Add(-time.Hour) }, governance.MergeBeforeCanonical},
	} {
		request := governance.MergeRequest{Project: cfProject, Suite: cfSuite, Carrier: test.pr.carrier, Source: mcSource(test.pr), Target: cfTarget,
			ObservedAt: cfRecordedAt.Add(time.Hour), Refusal: governance.MergeReasonForkUnsupported, RecordedAt: cfRecordedAt.Add(2 * time.Hour),
			Message: (&mcMessages{mute: true}).message}
		test.edit(&request)

		if result := e.merge(request); result.Kind != test.want {
			e.t.Fatalf("%s with a refusal = %s, want %s", test.pr.id, result.Kind, test.want)
		}
	}
}

func mcUnnumberedCurrent(e *cfEnv) {
	pr := mcCandidate("pr", "latest", "v3")
	e.mcSeedAs([]contract.SuiteVersionID{"V1", "latest"}, pr)
	e.approve(pr)
	before := e.revision()

	err := e.mergeErr(mcRequest(pr, "v3", &mcMessages{}))

	if !errors.Is(err, governance.ErrInvalidState) {
		e.t.Fatalf("error = %v, want ErrInvalidState", err)
	}
	if _, found := e.outcomeOf(pr.carrier); found {
		e.t.Fatal("an outcome was recorded")
	}
	e.requireWrites(before, 0)
}

func mcMissingFirstVersion(e *cfEnv) {
	e.mcSeedAs([]contract.SuiteVersionID{"version-1"})
	before := e.revision()
	pr := mcCandidate("pr", "version-1", "v3")

	err := e.mergeErr(mcRequest(pr, "v3", &mcMessages{}))

	if !errors.Is(err, governance.ErrInvalidState) {
		e.t.Fatalf("error = %v, want ErrInvalidState", err)
	}
	e.requireWrites(before, 0)
}

func mcMessageFailure(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	before := e.revision()
	boom := errors.New("message cannot be built")

	err := e.mergeErr(mcRequest(pr, "v2", &mcMessages{fail: boom}))

	if !errors.Is(err, boom) {
		e.t.Fatalf("error = %v, want the message error", err)
	}
	if _, found := e.outcomeOf(pr.carrier); found || e.currentVersion() != "V1" {
		e.t.Fatalf("outcome recorded %v, current version %q; want neither", found, e.currentVersion())
	}
	if _, found := e.assessmentAt(pr.current(), mcSource(pr)); found {
		e.t.Fatal("the assessment of the merge commit survived the rollback")
	}
	e.requireWrites(before, 0)
	e.requireQueued(nil, nil)
}

func mcOutboxConflict(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	other := mcCandidate("other", "V1", "v1")
	e.merge(mcRequest(other, "v1", &mcMessages{key: "comment/shared"})) // takes the key
	before := e.revision()

	err := e.mergeErr(mcRequest(pr, "v2", &mcMessages{key: "comment/shared"}))

	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("error = %v, want ErrOperationConflict", err)
	}
	if _, found := e.outcomeOf(pr.carrier); found || e.currentVersion() != "V1" {
		e.t.Fatalf("outcome recorded %v, current version %q; want the promotion and the outcome rolled back", found, e.currentVersion())
	}
	e.requireWrites(before, 0)
}

func mcConcurrent(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	before := e.revision()
	const callers = 4
	results, errs := make([]governance.MergeResult, callers), make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("panic: %v", r)
				}
			}()
			results[i], errs[i] = governance.PromoteMerge(context.Background(), e.store, mcDiscard{}, mcRequest(pr, "v2", &mcMessages{}))
		}()
	}
	wg.Wait()

	fresh := 0
	for i := range callers {
		if errs[i] != nil {
			e.t.Fatalf("caller %d: %v", i, errs[i])
		}
		if !results[i].Duplicate {
			fresh++
		}
		if results[i].Kind != governance.MergePromoted || results[i].Version != "V2" {
			e.t.Fatalf("caller %d outcome = %+v, want the one promotion to V2", i, results[i].MergeOutcome)
		}
	}
	if fresh != 1 {
		e.t.Fatalf("%d callers wrote the outcome, want exactly 1", fresh)
	}
	e.requireWrites(before, 2)
	e.requireQueued(nil, []string{"comment/" + string(pr.carrier)})
}

func mcOutcomeOfUnknown(e *cfEnv) {
	e.mcSeed(false)
	if outcome, found := e.outcomeOf("carrier-unknown"); found {
		e.t.Fatalf("outcome of an unknown carrier = %+v", outcome)
	}
	_, _, err := governance.MergeOutcomeOf(context.Background(), e.store, cfProject, "no-such-suite", "carrier-unknown")
	if !errors.Is(err, governance.ErrNotFound) {
		e.t.Fatalf("outcome in an unknown suite: error = %v, want ErrNotFound", err)
	}
}

func (e *cfEnv) recordMerge(carrier contract.ApprovalCarrierID, outcome governance.MergeOutcome) error {
	return e.do(func(ctx context.Context, tx governance.Tx) error {
		return tx.RecordMergeOutcome(ctx, governance.MergeWrite{Carrier: carrier, Outcome: outcome, RecordedAt: cfRecordedAt.Add(time.Hour)})
	})
}

func mcRecordRoundTrip(e *cfEnv) {
	e.mcSeed(true)
	before := e.revision()
	reference := mcCandidate("p1", "V1", "v2").current() // the seeded proposal of V2
	outcomes := []governance.MergeOutcome{
		{Kind: governance.MergePromoted, Source: "sha-0", Version: "V2", Reference: &reference},
		{Kind: governance.MergeCorrected, Source: "sha-1", Version: "V2", Corrects: "V1", Reference: &reference},
		{Kind: governance.MergeNoChange, Source: "sha-2", Version: "V2"},
		{Kind: governance.MergeNotTarget, Source: "sha-3", Version: "V1"},
		{Kind: governance.MergeBeforeCanonical, Source: "sha-4", Version: "V1"},
		{Kind: governance.MergeBlocked, Reason: governance.MergeReasonIntegrationUnreadable, Source: "", Version: "V2"},
		// Without a canonical there is no version to name: only a blocked or before-canonical outcome may have none.
		{Kind: governance.MergeBlocked, Reason: governance.MergeReasonDiffersFromApproved, Source: "sha-5", Reference: &reference},
		{Kind: governance.MergeBlocked, Reason: governance.MergeReasonUnapprovedChange, Source: "sha-6"},
		{Kind: governance.MergeBeforeCanonical, Source: "sha-7"},
	}
	for _, reason := range mcAllReasons { // every code of the schema's CHECK, spelled as the Go constant
		outcomes = append(outcomes, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: reason, Source: "sha-r", Version: "V2", Reference: &reference})
	}
	for i, outcome := range outcomes {
		carrier := contract.ApprovalCarrierID(fmt.Sprintf("carrier-%d", i))
		if err := e.recordMerge(carrier, outcome); err != nil {
			e.t.Fatalf("record %v %q: %v", outcome.Kind, outcome.Reason, err)
		}
		stored, _ := e.outcomeOf(carrier)
		e.requireOutcome(carrier, stored, outcome)
	}
	e.requireWrites(before, 0) // the outcome advances no revision
}

var mcAllReasons = []governance.MergeReason{
	governance.MergeReasonUnapprovedChange, governance.MergeReasonInvalidSource, governance.MergeReasonAssessmentConflict, governance.MergeReasonApprovalMissing,
	governance.MergeReasonDiffersFromApproved, governance.MergeReasonNotCurrent, governance.MergeReasonCanonicalChanged, governance.MergeReasonPolicyChanged,
	governance.MergeReasonIntegrityNotPassed, governance.MergeReasonIntegrationMismatch, governance.MergeReasonCorrectionContextReused,
	governance.MergeReasonForkUnsupported, governance.MergeReasonSharedMergeCommit, governance.MergeReasonIntegrationUnreadable, governance.MergeReasonIntegrityFailed,
}

func mcRecordTwiceConflicts(e *cfEnv) {
	e.mcSeed(false)
	outcome := governance.MergeOutcome{Kind: governance.MergeNoChange, Source: "sha", Version: "V1"}
	if err := e.recordMerge("carrier", outcome); err != nil {
		e.t.Fatal(err)
	}

	err := e.recordMerge("carrier", governance.MergeOutcome{Kind: governance.MergeNotTarget, Source: "other", Version: "V1"})

	if !errors.Is(err, governance.ErrConflict) {
		e.t.Fatalf("second outcome: error = %v, want ErrConflict", err)
	}
	stored, _ := e.outcomeOf("carrier")
	e.requireOutcome("carrier", stored, outcome)
}

func mcRecordRejects(e *cfEnv) {
	e.mcSeed(true)
	before := e.revision()
	dangling := mcCandidate("nobody", "V1", "v2").current()
	for name, test := range map[string]struct {
		outcome governance.MergeOutcome
		want    error
	}{
		"blocked without a reason":       {governance.MergeOutcome{Kind: governance.MergeBlocked, Source: "s", Version: "V1"}, governance.ErrInvalidRequest},
		"a reason on a promotion":        {governance.MergeOutcome{Kind: governance.MergePromoted, Reason: governance.MergeReasonInvalidSource, Source: "s", Version: "V2"}, governance.ErrInvalidRequest},
		"an unknown reason":              {governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: "bogus", Source: "s", Version: "V1"}, governance.ErrInvalidRequest},
		"an unknown kind":                {governance.MergeOutcome{Kind: "bogus", Source: "s", Version: "V1"}, governance.ErrInvalidRequest},
		"a correction without a target":  {governance.MergeOutcome{Kind: governance.MergeCorrected, Source: "s", Version: "V2"}, governance.ErrInvalidRequest},
		"a correction on a no-change":    {governance.MergeOutcome{Kind: governance.MergeNoChange, Corrects: "V1", Source: "s", Version: "V2"}, governance.ErrInvalidRequest},
		"no version":                     {governance.MergeOutcome{Kind: governance.MergeNoChange, Source: "s"}, governance.ErrInvalidRequest},
		"a promotion without a version":  {governance.MergeOutcome{Kind: governance.MergePromoted, Source: "s"}, governance.ErrInvalidRequest},
		"a correction without a version": {governance.MergeOutcome{Kind: governance.MergeCorrected, Corrects: "V1", Source: "s"}, governance.ErrInvalidRequest},
		"not-target without a version":   {governance.MergeOutcome{Kind: governance.MergeNotTarget, Source: "s"}, governance.ErrInvalidRequest},
		"an unknown version":             {governance.MergeOutcome{Kind: governance.MergeNoChange, Source: "s", Version: "V9"}, governance.ErrNotFound},
		"an unknown corrected version":   {governance.MergeOutcome{Kind: governance.MergeCorrected, Corrects: "V9", Source: "s", Version: "V2"}, governance.ErrNotFound},
		"an unknown proposal revision":   {governance.MergeOutcome{Kind: governance.MergeNoChange, Source: "s", Version: "V2", Reference: &dangling}, governance.ErrInvalidRequest},
	} {
		carrier := contract.ApprovalCarrierID("carrier-" + name)
		if err := e.recordMerge(carrier, test.outcome); !errors.Is(err, test.want) {
			e.t.Fatalf("%s: error = %v, want %v", name, err, test.want)
		}
		if _, found := e.outcomeOf(carrier); found {
			e.t.Fatalf("%s: an outcome was stored", name)
		}
	}
	valid := governance.MergeOutcome{Kind: governance.MergeNoChange, Source: "s", Version: "V1"}
	for name, write := range map[string]governance.MergeWrite{
		"a blank carrier":          {Carrier: " ", Outcome: valid, RecordedAt: cfRecordedAt},
		"a sub-microsecond moment": {Carrier: "c", Outcome: valid, RecordedAt: cfRecordedAt.Add(time.Nanosecond)},
	} {
		err := e.do(func(ctx context.Context, tx governance.Tx) error { return tx.RecordMergeOutcome(ctx, write) })
		if !errors.Is(err, governance.ErrInvalidRequest) {
			e.t.Fatalf("%s: error = %v, want ErrInvalidRequest", name, err)
		}
	}
	e.requireWrites(before, 0)
}

// mcPolicyChanged merges a PR whose revision was approved under another policy revision than the Suite's.
func mcPolicyChanged(e *cfEnv) {
	older := cfMust(contract.NewPolicy(cfProject, "policy-0", e.owner))
	snapshot := cfMust(contract.NewCanonicalSnapshot(cfMust(contract.NewSuite(cfProject, "suite-b", "", cfSeededRevision)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	e.seed(governance.Seed{Suite: governance.SuiteState{Canonical: snapshot, Policy: older, Target: cfTarget}}) // makes the policy revision exist
	pr := mcCandidate("pr", "V1", "v2")
	binding := cfMust(contract.NewApprovalBinding(contract.BindingInput{Reference: pr.reference("revision-1"), ExpectedCanonical: "V1", Manifest: pr.protected.Manifest().Digest(),
		Scope: pr.protected.ScopeDigest(), PolicyRevision: "policy-0", CoveredInputs: pr.protected.CoveredInputs()}))
	pr.proposal, pr.assessments = cfMust(contract.NewProposal(cfMust(contract.NewProposalRevision(binding, pr.origin, cfOriginKind(binding), pr.carrier)))), nil
	e.mcSeed(false, pr)
	before := e.revision()

	result := e.merge(mcRequest(pr, "v2", &mcMessages{}))

	e.requireOutcome(pr.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonPolicyChanged,
		Source: mcSource(pr), Version: "V1", Reference: mcRef(pr)})
	e.requireWrites(before, 1)
}

// mcCorrectionContextReused merges a fix that restores V1's content but is carried by the PR that promoted V2.
func mcCorrectionContextReused(e *cfEnv) {
	fix := mcCandidate("fix", "V2", "v1")
	fix.carrier = "carrier-p1" // the carrier of V2's promotion
	fix.proposal = cfMust(contract.NewProposal(cfMust(contract.NewProposalRevision(fix.proposal.Current().Binding(), fix.origin, fix.proposal.Current().OriginKind(), fix.carrier))))
	e.mcSeed(true, fix)
	e.approve(fix)
	before := e.revision()

	result := e.merge(mcRequest(fix, "v1", &mcMessages{}))

	e.requireOutcome(fix.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonCorrectionContextReused,
		Source: mcSource(fix), Version: "V2", Reference: mcRef(fix)})
	e.requireWrites(before, 1)
}

func mcCorrectionMessageFailure(e *cfEnv) {
	fix := mcCandidate("fix", "V2", "v1") // restores V1's content on top of V2
	e.mcSeed(true, fix)
	e.approve(fix)
	before := e.revision()
	boom := errors.New("message cannot be built")

	err := e.mergeErr(mcRequest(fix, "v1", &mcMessages{fail: boom}))

	if !errors.Is(err, boom) {
		e.t.Fatalf("error = %v, want the message error", err)
	}
	if _, found := e.outcomeOf(fix.carrier); found || e.currentVersion() != "V2" {
		e.t.Fatalf("outcome recorded %v, current version %q; want neither", found, e.currentVersion())
	}
	if _, found := e.version("V3"); found {
		e.t.Fatal("the corrected version survived the rollback")
	}
	if _, found := e.assessmentAt(fix.current(), mcSource(fix)); found {
		e.t.Fatal("the assessment of the merge commit survived the rollback")
	}
	e.requireWrites(before, 0)
	e.requireQueued(nil, nil)
}
