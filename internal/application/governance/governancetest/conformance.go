package governancetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Store is what the conformance suite needs from an implementation: the unit
// of work under test and the trusted seeder that prepares its state.
type Store interface {
	governance.UnitOfWork
	governance.Seeder
}

// FailNexter is implemented by stores that can inject a write failure. The
// next write made by the next unit of work fails with err; the failure is
// consumed by that unit of work whether or not it writes. Subtests that need
// it are skipped for stores that do not implement it.
type FailNexter interface {
	FailNext(err error)
}

// ConformanceBlobs returns the artifact bytes behind every manifest entry the
// suite writes, so a store that verifies artifacts when a version is written
// can be given them before RunConformance runs.
func ConformanceBlobs() [][]byte {
	blobs := make([][]byte, 0, len(cfContents))
	for _, content := range cfContents {
		blobs = append(blobs, cfBlob(content))
	}
	return blobs
}

// RunConformance runs the shared persistence semantics of the governance
// persistence contract against stores created by newStore. Every subtest gets
// its own empty store.
func RunConformance(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	cases := []struct {
		name string
		run  func(*cfEnv)
	}{
		{"SeedReadBackWithoutCurrentVersion", cfSeedWithoutCurrentVersion},
		{"SeedReadBackWithCurrentVersion", cfSeedWithCurrentVersion},
		{"SeedReadBackProposalsWithAllRevisions", cfSeedProposals},
		{"SeedReadBackAssessments", cfSeedAssessments},
		{"SeedReadBackHistoryVersions", cfSeedHistory},
		{"ReceiptReplayByOperationAndSourceCommand", cfReceiptReplay},
		{"BootstrapReceiptKeepsItsKind", cfBootstrapReceipt},
		{"OperationIDIsUniqueAcrossSuites", cfOperationAcrossSuites},
		{"OperationIDIsUniqueAcrossKinds", cfOperationAcrossKinds},
		{"SourceCommandReplayedByAnotherActorConflicts", cfSourceReplayConflicts},
		{"AliasRecordsOperationAndKeepsOriginalReceipt", cfAlias},
		{"EveryWriteAdvancesTheRevisionByOne", cfRevisionAdvances},
		{"RejectedResultsAreStoredAndConsumeNoOrder", cfRejectedResults},
		{"RollbackWhenTheUnitOfWorkFails", cfRollbackOnError},
		{"RollbackWhenAWriteFails", cfRollbackOnWriteFailure},
		{"CanceledContextFailsWithoutEffects", cfCanceledContext},
		{"CancellationDuringTheUnitOfWorkRollsBack", cfCancellationDuringWork},
		{"VersionConflictLeavesNoPartialEffects", cfVersionConflict},
		{"PromotionRequiresAKnownProposalRevision", cfUnknownPromotionReference},
		{"PromotionRoundTrip", cfPromotionRoundTrip},
		{"UnknownAggregatesAreNotFound", cfNotFound},
		{"TransactionIsUnusableAfterTheUnitOfWork", cfTransactionClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer cfGuard(t)
			owner := cfMust(contract.NewPrincipal("owner", contract.Human))
			tc.run(&cfEnv{t: t, store: newStore(t), owner: owner})
		})
	}
}

// cfGuard fails the running subtest on a panic, from a fixture that cannot be
// built or from the store, instead of aborting the others. Every subtest,
// including nested ones, must run through RunConformance or cfEnv.run.
func cfGuard(t *testing.T) {
	if r := recover(); r != nil {
		t.Fatalf("panic: %v\n%s", r, debug.Stack())
	}
}

const (
	cfProject        = contract.ProjectID("project")
	cfSuite          = contract.SuiteID("suite")
	cfPolicy         = contract.PolicyRevisionID("policy-1")
	cfTarget         = contract.IntegrationTargetID("default")
	cfSeededRevision = contract.StateRevision(7)
)

var (
	cfContents   = []string{"v1", "v2", "v3"}
	cfRecordedAt = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
)

func cfBlob(content string) []byte { return []byte("conformance:" + content) }

// cfMust unwraps a fixture value; the runner turns the panic of a fixture that
// cannot be built into a failed subtest.
func cfMust[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func cfProtectedOf(content string) contract.ProtectedContract {
	manifest := cfMust(artifact.NewManifest([]artifact.Entry{{Path: "tests/contract.txt", Content: artifact.Hash(cfBlob(content))}}))
	return cfMust(contract.NewProtectedContract(manifest, artifact.Hash([]byte("tests/**")), map[string]string{"runner": "v1"}))
}

// cfCandidate is a proposal against a baseline canonical with passing
// integrity assessments for its candidate source and its merged source.
type cfCandidate struct {
	project     contract.ProjectID
	suite       contract.SuiteID
	id          string
	protected   contract.ProtectedContract
	proposal    contract.Proposal
	bindings    map[contract.ProposalRevisionID]contract.ApprovalBinding
	carrier     contract.ApprovalCarrierID
	origin      contract.SourceRevision
	merged      contract.SourceRevision
	assessments []contract.IntegrityAssessment
}

func cfNewCandidate(project contract.ProjectID, suite contract.SuiteID, id string, baseline contract.SuiteVersionID, content string, revisions ...contract.ProposalRevisionID) cfCandidate {
	if len(revisions) == 0 {
		revisions = []contract.ProposalRevisionID{"revision-1"}
	}
	protected := cfProtectedOf(content)
	c := cfCandidate{project: project, suite: suite, id: id, protected: protected, bindings: map[contract.ProposalRevisionID]contract.ApprovalBinding{},
		carrier: contract.ApprovalCarrierID("carrier-" + id), origin: contract.SourceRevision("candidate-" + id), merged: contract.SourceRevision("merged-" + id)}
	for _, revisionID := range revisions {
		binding := cfMust(contract.NewApprovalBinding(contract.BindingInput{Reference: c.reference(revisionID), ExpectedCanonical: baseline,
			Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: cfPolicy, CoveredInputs: protected.CoveredInputs()}))
		c.bindings[revisionID] = binding
		revision := cfMust(contract.NewProposalRevision(binding, c.origin, c.carrier))
		if c.proposal.IsZero() {
			c.proposal = cfMust(contract.NewProposal(revision))
		} else {
			c.proposal = cfMust(c.proposal.Revise(revision))
		}
		for _, source := range []contract.SourceRevision{c.origin, c.merged} {
			evidence := cfMust(contract.NewIntegrityEvidence("verifier", source, binding, contract.IntegrityPassed))
			c.assessments = append(c.assessments, cfMust(contract.AssessIntegrity(source, binding, &evidence)))
		}
	}
	return c
}

func (c cfCandidate) reference(revision contract.ProposalRevisionID) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: c.project, SuiteID: c.suite, ProposalID: contract.ProposalID(c.id), RevisionID: revision}
}

func (c cfCandidate) current() contract.ProposalReference {
	return c.proposal.Current().Binding().Reference()
}

func (c cfCandidate) proposalID() contract.ProposalID { return contract.ProposalID(c.id) }

func (c cfCandidate) mergedRequest(operation string, version contract.SuiteVersionID) governance.PromoteRequest {
	integration := cfMust(contract.NewIntegration(c.project, cfTarget, c.merged, c.carrier, contract.IntegrationMergedChange))
	return governance.PromoteRequest{OperationID: contract.OperationID(operation), Reference: c.current(), Carrier: c.carrier, Proposed: c.protected,
		AssessmentSource: c.merged, Integration: integration, NewVersionID: version, RecordedAt: cfRecordedAt}
}

func (c cfCandidate) baselineRequest(operation string, version contract.SuiteVersionID) governance.PromoteRequest {
	integration := cfMust(contract.NewIntegration(c.project, cfTarget, c.origin, "", contract.IntegrationExistingBaseline))
	return governance.PromoteRequest{OperationID: contract.OperationID(operation), Reference: c.current(), Carrier: c.carrier, Proposed: c.protected,
		AssessmentSource: c.origin, Integration: integration, NewVersionID: version, RecordedAt: cfRecordedAt}
}

// cfEnv is one subtest's store and the helpers that drive it.
type cfEnv struct {
	t     *testing.T
	store Store
	owner contract.Principal
}

// run runs fn as a nested subtest with its own env and panic isolation.
func (e *cfEnv) run(name string, fn func(*cfEnv)) {
	e.t.Run(name, func(t *testing.T) {
		defer cfGuard(t)
		fn(&cfEnv{t: t, store: e.store, owner: e.owner})
	})
}

func (e *cfEnv) candidate(id string, baseline contract.SuiteVersionID, content string, revisions ...contract.ProposalRevisionID) cfCandidate {
	return cfNewCandidate(cfProject, cfSuite, id, baseline, content, revisions...)
}

func (e *cfEnv) policy(project contract.ProjectID) contract.Policy {
	return cfMust(contract.NewPolicy(project, cfPolicy, e.owner))
}

// seedSimple seeds a Suite without a canonical version in which every
// candidate is admitted to the schedule in order.
func (e *cfEnv) seedSimple(project contract.ProjectID, suite contract.SuiteID, candidates ...cfCandidate) governance.Seed {
	e.t.Helper()
	snapshot := cfMust(contract.NewCanonicalSnapshot(cfMust(contract.NewSuite(project, suite, "", cfSeededRevision)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	schedule := cfMust(contract.NewSchedule(project, suite))
	seed := governance.Seed{}
	for _, c := range candidates {
		schedule = cfMust(schedule.Admit(c.proposal, true))
		seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: c.proposal, Assessments: c.assessments})
	}
	seed.Suite = governance.SuiteState{Canonical: snapshot, Policy: e.policy(project), Target: cfTarget, Schedule: schedule}
	e.seed(seed)
	return seed
}

func (e *cfEnv) seed(seed governance.Seed) {
	e.t.Helper()
	if err := e.store.Seed(context.Background(), seed); err != nil {
		e.t.Fatalf("seed: %v", err)
	}
}

// cfRich is a Suite with two promoted versions, the second of them current,
// and an open candidate whose second revision carries one assessment of every
// kind.
type cfRich struct {
	base, second, open cfCandidate
	seed               governance.Seed
	versions           [2]contract.HistoricalCanonical
	assessments        map[string]contract.IntegrityAssessment
}

func (e *cfEnv) seedRich() cfRich {
	e.t.Helper()
	r := cfRich{
		base:        e.candidate("p0", "", "v1", "revision-1", "revision-2"),
		second:      e.candidate("p1", "version-1", "v2"),
		open:        e.candidate("p2", "version-2", "v3", "revision-1", "revision-2"),
		assessments: map[string]contract.IntegrityAssessment{},
	}
	older, newer := r.open.bindings["revision-1"], r.open.bindings["revision-2"]
	failed := cfMust(contract.NewIntegrityEvidence("verifier", "failing-src", newer, contract.IntegrityFailed))
	strayed := cfMust(contract.NewIntegrityEvidence("verifier", "mismatch-src", older, contract.IntegrityPassed))
	r.assessments["failing"] = cfMust(contract.AssessIntegrity("failing-src", newer, &failed))
	r.assessments["missing"] = cfMust(contract.AssessIntegrity("missing-src", newer, nil))
	r.assessments["mismatch"] = cfMust(contract.AssessIntegrity("mismatch-src", newer, &strayed))
	for _, name := range []string{"failing", "missing", "mismatch"} {
		r.open.assessments = append(r.open.assessments, r.assessments[name])
	}
	for i, step := range []struct {
		c         cfCandidate
		operation contract.OperationID
		version   contract.SuiteVersionID
	}{{r.base, "seed-op-1", "version-1"}, {r.second, "seed-op-2", "version-2"}} {
		version := cfMust(contract.NewSuiteVersion(cfProject, cfSuite, step.version, step.c.protected.Manifest()))
		record := cfMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: step.operation, VersionID: step.version, Binding: step.c.proposal.Current().Binding(),
			Carrier: step.c.carrier, Source: step.c.merged, Target: cfTarget, RecordedAt: cfRecordedAt}))
		r.versions[i] = cfMust(contract.NewHistoricalCanonical(version, record))
	}
	schedule := cfMust(contract.NewSchedule(cfProject, cfSuite))
	for _, c := range []cfCandidate{r.base, r.second, r.open} {
		schedule = cfMust(schedule.Admit(c.proposal, true))
	}
	schedule = cfMust(schedule.Observe(r.base.proposal, contract.ObservePromoted))
	schedule = cfMust(schedule.Observe(r.second.proposal, contract.ObservePromoted))
	current := r.versions[1]
	suite := cfMust(contract.NewSuite(cfProject, cfSuite, "version-2", cfSeededRevision))
	canonical := cfMust(contract.NewCanonicalSnapshot(suite, current.Version(), r.second.protected, current.Record()))
	r.seed = governance.Seed{
		Suite:   governance.SuiteState{Canonical: canonical, Policy: e.policy(cfProject), Target: cfTarget, Schedule: schedule},
		History: r.versions[:],
		Proposals: []governance.SeedProposal{
			{Proposal: r.base.proposal, Assessments: r.base.assessments},
			{Proposal: r.second.proposal, Assessments: r.second.assessments},
			{Proposal: r.open.proposal, Assessments: r.open.assessments},
		},
	}
	e.seed(r.seed)
	return r
}

func (e *cfEnv) command(c cfCandidate, revision contract.ProposalRevisionID, operation, source string, action contract.ConsentAction, order contract.CommandOrder) contract.Command {
	return e.commandBy(e.owner, c, revision, operation, source, action, order)
}

func (e *cfEnv) commandBy(actor contract.Principal, c cfCandidate, revision contract.ProposalRevisionID, operation, source string, action contract.ConsentAction, order contract.CommandOrder) contract.Command {
	return cfMust(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID(operation), SourceCommandID: contract.SourceCommandID(source),
		Actor: actor, Reference: c.reference(revision), Carrier: c.carrier, Action: action, Order: order}))
}

func (e *cfEnv) consent(command contract.Command) governance.ConsentResponse {
	e.t.Helper()
	response, err := governance.ProcessConsent(context.Background(), e.store, governance.ConsentRequest{Command: command})
	if err != nil {
		e.t.Fatalf("process consent %q: %v", command.OperationID(), err)
	}
	return response
}

// approve records the owner's approval of the candidate's current revision.
func (e *cfEnv) approve(c cfCandidate) governance.ConsentResponse {
	e.t.Helper()
	response := e.consent(e.command(c, c.current().RevisionID, "approve-"+c.id, "comment-"+c.id, contract.ApproveConsent, 1))
	if !response.Receipt.CurrentApprovalEligible {
		e.t.Fatalf("approval of %s is not eligible: %v", c.id, response.Receipt.Result.Reason())
	}
	return response
}

func (e *cfEnv) promote(request governance.PromoteRequest) governance.PromoteResult {
	e.t.Helper()
	result, err := governance.Promote(context.Background(), e.store, request)
	if err != nil {
		e.t.Fatalf("promote %q: %v", request.OperationID, err)
	}
	return result
}

// doIn runs fn in a unit of work of the given Suite.
func (e *cfEnv) doIn(project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return e.store.Do(context.Background(), project, suite, fn)
}

// do runs fn in a unit of work of the default Suite.
func (e *cfEnv) do(fn func(context.Context, governance.Tx) error) error {
	return e.doIn(cfProject, cfSuite, fn)
}

// read runs fn in a unit of work that writes nothing and fails the subtest if it fails.
func (e *cfEnv) read(fn func(context.Context, governance.Tx) error) {
	e.t.Helper()
	if err := e.do(fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *cfEnv) state() governance.SuiteState {
	e.t.Helper()
	var state governance.SuiteState
	e.read(func(ctx context.Context, tx governance.Tx) (err error) {
		state, err = tx.Suite(ctx)
		return err
	})
	return state
}

func (e *cfEnv) revision() contract.StateRevision { return e.state().Canonical.Suite().Revision() }

func (e *cfEnv) requireWrites(before contract.StateRevision, writes int) {
	e.t.Helper()
	if got, want := e.revision(), before+contract.StateRevision(writes); got != want {
		e.t.Fatalf("revision = %d, want %d", got, want)
	}
}

// results returns the stored consent results of a proposal in processing order.
func (e *cfEnv) results(proposal contract.ProposalID) []contract.CommandResult {
	e.t.Helper()
	var results []contract.CommandResult
	e.read(func(ctx context.Context, tx governance.Tx) error {
		_, consent, err := tx.Proposal(ctx, proposal)
		results = consent.Results()
		return err
	})
	return results
}

// cfProbe names the facts whose visibility a test compares around an attempt
// that must leave no trace.
type cfProbe struct {
	proposal   contract.ProposalID
	operations []contract.OperationID
	sources    []contract.SourceCommandID
	versions   []contract.SuiteVersionID
	references []contract.ProposalReference
}

type cfView struct {
	Revision   contract.StateRevision
	Current    contract.SuiteVersionID
	Generation contract.ScheduleGeneration
	Entries    []contract.ScheduleEntry
	Results    int
	Operations []bool
	Sources    []bool
	Versions   []bool
	References []bool
}

func (e *cfEnv) view(p cfProbe) cfView {
	e.t.Helper()
	var v cfView
	e.read(func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return err
		}
		v.Revision = state.Canonical.Suite().Revision()
		v.Current, _ = state.Canonical.Suite().CurrentVersionID()
		v.Generation, v.Entries = state.Schedule.Generation(), state.Schedule.Entries()
		if p.proposal != "" {
			_, consent, err := tx.Proposal(ctx, p.proposal)
			if err != nil {
				return err
			}
			v.Results = len(consent.Results())
		}
		for _, id := range p.operations {
			_, found, err := tx.Receipt(ctx, id)
			if err != nil {
				return err
			}
			v.Operations = append(v.Operations, found)
		}
		for _, id := range p.sources {
			_, found, err := tx.ReceiptBySource(ctx, id)
			if err != nil {
				return err
			}
			v.Sources = append(v.Sources, found)
		}
		for _, id := range p.versions {
			_, found, err := tx.Version(ctx, id)
			if err != nil {
				return err
			}
			v.Versions = append(v.Versions, found)
		}
		for _, reference := range p.references {
			_, found, err := tx.PromotionFor(ctx, reference)
			if err != nil {
				return err
			}
			v.References = append(v.References, found)
		}
		return nil
	})
	return v
}

func (e *cfEnv) requireSameView(before, after cfView) {
	e.t.Helper()
	if !reflect.DeepEqual(before, after) {
		e.t.Fatalf("visible state changed:\n before %+v\n after  %+v", before, after)
	}
}

// consentWrite decides a command inside tx exactly as ProcessConsent does for
// a new command, or builds the alias write for a replayed source command.
func (e *cfEnv) consentWrite(ctx context.Context, tx governance.Tx, command contract.Command) (governance.ConsentWrite, error) {
	state, err := tx.Suite(ctx)
	if err != nil {
		return governance.ConsentWrite{}, fmt.Errorf("load suite: %w", err)
	}
	proposal, consent, err := tx.Proposal(ctx, command.Reference().ProposalID)
	if err != nil {
		return governance.ConsentWrite{}, fmt.Errorf("load proposal: %w", err)
	}
	next, result, err := consent.Apply(proposal, state.Policy, command)
	if err != nil {
		return governance.ConsentWrite{}, fmt.Errorf("apply consent: %w", err)
	}
	if result.Duplicate() {
		original, found, err := tx.ReceiptBySource(ctx, command.SourceCommandID())
		if err != nil || !found || original.Consent == nil {
			return governance.ConsentWrite{}, fmt.Errorf("load original receipt: found %v: %w", found, err)
		}
		return governance.ConsentWrite{OperationID: command.OperationID(), Result: result, Receipt: *original.Consent, Alias: true}, nil
	}
	receipt := governance.ConsentReceipt{Result: result, EvaluatedReference: proposal.Current().Binding().Reference(), PolicyRevisionID: state.Policy.RevisionID(),
		CurrentApprovalEligible: next.HasApproval(proposal, state.Policy)}
	return governance.ConsentWrite{OperationID: command.OperationID(), Result: result, Receipt: receipt}, nil
}

// promotionWrite builds the write of a promotion of the candidate's current
// revision, observing it in the schedule as the use cases do.
func (e *cfEnv) promotionWrite(ctx context.Context, tx governance.Tx, c cfCandidate, operation string, version, corrects contract.SuiteVersionID) (governance.PromotionWrite, error) {
	state, err := tx.Suite(ctx)
	if err != nil {
		return governance.PromotionWrite{}, fmt.Errorf("load suite: %w", err)
	}
	schedule, err := state.Schedule.Observe(c.proposal, contract.ObservePromoted)
	if err != nil {
		return governance.PromotionWrite{}, fmt.Errorf("observe promotion: %w", err)
	}
	return e.promotionWriteWith(c, operation, version, corrects, schedule), nil
}

func (e *cfEnv) promotionWriteWith(c cfCandidate, operation string, version, corrects contract.SuiteVersionID, schedule contract.Schedule) governance.PromotionWrite {
	request := c.mergedRequest(operation, version)
	suiteVersion := cfMust(contract.NewSuiteVersion(c.project, c.suite, version, c.protected.Manifest()))
	record := cfMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: request.OperationID, VersionID: version, Binding: c.proposal.Current().Binding(),
		Carrier: c.carrier, Source: c.merged, Target: cfTarget, RecordedAt: cfRecordedAt, CorrectsVersionID: corrects}))
	kind := governance.OperationPromote
	if corrects != "" {
		kind = governance.OperationCorrect
	}
	identity := governance.PromotionIdentity{Kind: kind, Request: request, CorrectsVersionID: corrects, Binding: c.proposal.Current().Binding()}
	return governance.PromotionWrite{Receipt: governance.PromotionReceipt{Identity: identity, Record: record}, Version: suiteVersion, Schedule: schedule}
}

// Equality of domain values that are not comparable with ==.

func cfSameRecord(a, b contract.PromotionRecord) bool {
	return a.VersionID() == b.VersionID() && a.Binding().Equal(b.Binding()) && a.OperationID() == b.OperationID() && a.Carrier() == b.Carrier() &&
		a.Source() == b.Source() && a.Target() == b.Target() && a.RecordedAt().Equal(b.RecordedAt()) && a.CorrectsVersionID() == b.CorrectsVersionID()
}

func cfSameHistory(a, b contract.HistoricalCanonical) bool {
	return a.Version().ID() == b.Version().ID() && a.Version().ProjectID() == b.Version().ProjectID() && a.Version().SuiteID() == b.Version().SuiteID() &&
		a.Version().Manifest().Digest() == b.Version().Manifest().Digest() && cfSameRecord(a.Record(), b.Record())
}

func cfSameSchedule(a, b contract.Schedule) bool {
	return a.ProjectID() == b.ProjectID() && a.SuiteID() == b.SuiteID() && a.Generation() == b.Generation() && slices.Equal(a.Entries(), b.Entries())
}

func cfSameRevision(a, b contract.ProposalRevision) bool {
	return a.Binding().Equal(b.Binding()) && a.Origin() == b.Origin() && a.Carrier() == b.Carrier()
}

func cfSameAssessment(a, b contract.IntegrityAssessment) bool {
	if a.Source() != b.Source() || !a.Binding().Equal(b.Binding()) || a.Reason() != b.Reason() || a.Assurance() != b.Assurance() || a.Passed() != b.Passed() {
		return false
	}
	left, hasLeft := a.Evidence()
	right, hasRight := b.Evidence()
	if hasLeft != hasRight {
		return false
	}
	return !hasLeft || (left.EmitterID() == right.EmitterID() && left.Source() == right.Source() && left.Outcome() == right.Outcome() && left.Binding().Equal(right.Binding()))
}

func cfSamePromotionReceipt(a, b governance.PromotionReceipt) bool {
	x, y := a.Identity, b.Identity
	p, q := x.Request, y.Request
	return x.Kind == y.Kind && x.CorrectsVersionID == y.CorrectsVersionID && x.Binding.Equal(y.Binding) &&
		p.OperationID == q.OperationID && p.Reference == q.Reference && p.Carrier == q.Carrier && p.Proposed.Equal(q.Proposed) && p.AssessmentSource == q.AssessmentSource &&
		p.Integration == q.Integration && p.NewVersionID == q.NewVersionID && p.RecordedAt.Equal(q.RecordedAt) && cfSameRecord(a.Record, b.Record)
}

func cfSameReceipt(a, b governance.OperationReceipt) bool {
	if a.Kind != b.Kind || a.ProjectID != b.ProjectID || a.SuiteID != b.SuiteID || (a.Consent == nil) != (b.Consent == nil) || (a.Promotion == nil) != (b.Promotion == nil) {
		return false
	}
	if a.Consent != nil && *a.Consent != *b.Consent {
		return false
	}
	return a.Promotion == nil || cfSamePromotionReceipt(*a.Promotion, *b.Promotion)
}

// Seed and read back.

func cfSeedWithoutCurrentVersion(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	seed := e.seedSimple(cfProject, cfSuite, c)

	state := e.state()
	suite := state.Canonical.Suite()
	if _, present := suite.CurrentVersionID(); present || suite.ProjectID() != cfProject || suite.ID() != cfSuite || suite.Revision() != cfSeededRevision {
		e.t.Errorf("suite = %+v, want %s/%s at revision %d without a current version", suite, cfProject, cfSuite, cfSeededRevision)
	}
	if !state.Canonical.Version().Manifest().IsZero() || !state.Canonical.Contract().IsZero() || !state.Canonical.Record().IsZero() {
		e.t.Error("canonical carries a version, contract or record before any promotion")
	}
	e.requireSuiteAuthority(state, seed.Suite)
}

func cfSeedWithCurrentVersion(e *cfEnv) {
	r := e.seedRich()

	state := e.state()
	suite := state.Canonical.Suite()
	if current, present := suite.CurrentVersionID(); !present || current != "version-2" || suite.Revision() != cfSeededRevision {
		e.t.Errorf("suite = %+v, want current version-2 at revision %d", suite, cfSeededRevision)
	}
	if state.Canonical.Version().ID() != "version-2" || state.Canonical.Version().Manifest().Digest() != r.second.protected.Manifest().Digest() {
		e.t.Errorf("canonical version = %q, want version-2 with the second manifest", state.Canonical.Version().ID())
	}
	if !state.Canonical.Contract().Equal(r.second.protected) {
		e.t.Error("canonical contract is not the second protected contract")
	}
	if !cfSameRecord(state.Canonical.Record(), r.versions[1].Record()) {
		e.t.Error("canonical promotion record is not the stored record of version-2")
	}
	e.requireSuiteAuthority(state, r.seed.Suite)
	if active, ok := state.Schedule.Active(); !ok || active.ProposalID() != r.open.proposalID() || active.Carrier() != r.open.carrier {
		e.t.Errorf("active schedule entry = %+v, %v, want the open candidate", active, ok)
	}
}

func (e *cfEnv) requireSuiteAuthority(got, want governance.SuiteState) {
	e.t.Helper()
	if got.Policy.ProjectID() != want.Policy.ProjectID() || got.Policy.RevisionID() != want.Policy.RevisionID() || got.Policy.OwnerID() != want.Policy.OwnerID() || !got.Policy.CanApprove(e.owner) {
		e.t.Errorf("policy = %+v, want %+v", got.Policy, want.Policy)
	}
	if got.Target != want.Target {
		e.t.Errorf("target = %q, want %q", got.Target, want.Target)
	}
	if !cfSameSchedule(got.Schedule, want.Schedule) {
		e.t.Errorf("schedule = %d %v, want %d %v", got.Schedule.Generation(), got.Schedule.Entries(), want.Schedule.Generation(), want.Schedule.Entries())
	}
}

func cfSeedProposals(e *cfEnv) {
	r := e.seedRich()
	for _, c := range []cfCandidate{r.base, r.second, r.open} {
		e.read(func(ctx context.Context, tx governance.Tx) error {
			proposal, consent, err := tx.Proposal(ctx, c.proposalID())
			if err != nil {
				return fmt.Errorf("proposal %s: %w", c.id, err)
			}
			if !cfSameRevision(proposal.Current(), c.proposal.Current()) {
				e.t.Errorf("%s: current revision = %v, want %v", c.id, proposal.Current().Binding().Reference(), c.current())
			}
			for revisionID, binding := range c.bindings {
				revision, err := proposal.Lookup(c.reference(revisionID), c.carrier)
				if err != nil || !revision.Binding().Equal(binding) || revision.Origin() != c.origin {
					e.t.Errorf("%s: revision %s = %v, %v, want the seeded revision", c.id, revisionID, revision.Binding().Reference(), err)
				}
				_, resolved := proposal.Resolve(c.reference(revisionID), c.carrier)
				if isCurrent := revisionID == c.current().RevisionID; isCurrent != (resolved == nil) || (!isCurrent && !errors.Is(resolved, contract.ErrSupersededRevision)) {
					e.t.Errorf("%s: resolving %s = %v, want current only", c.id, revisionID, resolved)
				}
			}
			if len(consent.Results()) != 0 {
				e.t.Errorf("%s: seeded consent has %d results", c.id, len(consent.Results()))
			}
			return nil
		})
	}
}

func cfSeedAssessments(e *cfEnv) {
	r := e.seedRich()
	reference := r.open.current()
	type want struct {
		name   string
		source contract.SourceRevision
		reason contract.IntegrityReason
		stored contract.IntegrityAssessment
	}
	var cases []want
	for _, a := range r.open.assessments {
		if a.Reason() == contract.IntegrityReasonSatisfied && a.Binding().Reference() == reference {
			cases = append(cases, want{"passing " + string(a.Source()), a.Source(), contract.IntegrityReasonSatisfied, a})
		}
	}
	cases = append(cases,
		want{"failing", "failing-src", contract.IntegrityReasonFailed, r.assessments["failing"]},
		want{"missing evidence", "missing-src", contract.IntegrityReasonMissing, r.assessments["missing"]},
		want{"evidence bound to another revision", "mismatch-src", contract.IntegrityReasonMismatch, r.assessments["mismatch"]},
	)
	if len(cases) != 5 {
		e.t.Fatalf("fixture has %d assessment cases, want 5", len(cases))
	}
	for _, tc := range cases {
		e.read(func(ctx context.Context, tx governance.Tx) error {
			got, found, err := tx.Assessment(ctx, reference, tc.source)
			if err != nil {
				return err
			}
			if !found || got.Reason() != tc.reason || !cfSameAssessment(got, tc.stored) {
				e.t.Errorf("%s: assessment found=%v reason=%v, want %v as seeded", tc.name, found, got.Reason(), tc.reason)
			}
			return nil
		})
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		if _, found, err := tx.Assessment(ctx, reference, "never-assessed"); err != nil || found {
			e.t.Errorf("assessment of an unassessed source = found %v, %v, want not found", found, err)
		}
		older := r.open.reference("revision-1")
		if got, found, err := tx.Assessment(ctx, older, r.open.merged); err != nil || !found || got.Binding().Reference() != older || !got.Passed() {
			e.t.Errorf("assessment of the older revision = %v found %v, %v, want its own passing assessment", got.Binding().Reference(), found, err)
		}
		return nil
	})
}

func cfSeedHistory(e *cfEnv) {
	r := e.seedRich()
	e.read(func(ctx context.Context, tx governance.Tx) error {
		for i, stored := range r.versions {
			id := stored.Version().ID()
			got, found, err := tx.Version(ctx, id)
			if err != nil || !found || !cfSameHistory(got, stored) {
				e.t.Errorf("version %s = found %v, %v, want the seeded history entry %d", id, found, err, i)
			}
			record, found, err := tx.PromotionFor(ctx, stored.Record().Binding().Reference())
			if err != nil || !found || !cfSameRecord(record, stored.Record()) {
				e.t.Errorf("promotion for %v = found %v, %v, want the seeded record", stored.Record().Binding().Reference(), found, err)
			}
		}
		if _, found, err := tx.Version(ctx, "version-0"); err != nil || found {
			e.t.Errorf("unknown version = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.PromotionFor(ctx, r.open.current()); err != nil || found {
			e.t.Errorf("promotion for an unpromoted reference = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.PromotionFor(ctx, r.base.reference("revision-1")); err != nil || found {
			e.t.Errorf("promotion for a revision that was not promoted = found %v, %v, want not found", found, err)
		}
		return nil
	})
}

// Receipts and idempotency.

func cfReceiptReplay(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	consent := e.approve(c)
	request := c.mergedRequest("promote-1", "version-1")
	result := e.promote(request)
	if !result.Committed || result.Outcome != contract.PromotionProposed {
		e.t.Fatalf("promotion = %+v, want a committed proposal", result)
	}
	wantConsent := governance.OperationReceipt{Kind: governance.OperationConsent, ProjectID: cfProject, SuiteID: cfSuite, Consent: &consent.Receipt}
	wantPromotion := governance.OperationReceipt{Kind: governance.OperationPromote, ProjectID: cfProject, SuiteID: cfSuite, Promotion: &governance.PromotionReceipt{
		Identity: governance.PromotionIdentity{Kind: governance.OperationPromote, Request: request, Binding: c.proposal.Current().Binding()}, Record: result.Record}}

	e.read(func(ctx context.Context, tx governance.Tx) error {
		byOperation, found, err := tx.Receipt(ctx, "approve-p1")
		if err != nil || !found || !cfSameReceipt(byOperation, wantConsent) {
			e.t.Errorf("receipt of the consent operation = %+v found %v, %v, want the stored consent receipt", byOperation, found, err)
		}
		bySource, found, err := tx.ReceiptBySource(ctx, "comment-p1")
		if err != nil || !found || !cfSameReceipt(bySource, wantConsent) {
			e.t.Errorf("receipt of the source command = %+v found %v, %v, want the stored consent receipt", bySource, found, err)
		}
		promotion, found, err := tx.Receipt(ctx, "promote-1")
		if err != nil || !found || !cfSameReceipt(promotion, wantPromotion) {
			e.t.Errorf("receipt of the promotion = %+v found %v, %v, want the stored promotion receipt", promotion, found, err)
		}
		if _, found, err := tx.Receipt(ctx, "comment-p1"); err != nil || found {
			e.t.Errorf("receipt by a source command id as an operation id = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.ReceiptBySource(ctx, "approve-p1"); err != nil || found {
			e.t.Errorf("receipt by an operation id as a source command id = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.ReceiptBySource(ctx, "promote-1"); err != nil || found {
			e.t.Errorf("a promotion operation has a source command receipt: found %v, %v", found, err)
		}
		return nil
	})

	replay, err := governance.Promote(context.Background(), e.store, request)
	if err != nil || !replay.Duplicate || !replay.Committed || !cfSameRecord(replay.Record, result.Record) {
		e.t.Errorf("promotion replay = %+v, %v, want the original record as a duplicate", replay, err)
	}
}

func cfBootstrapReceipt(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	e.approve(c)
	request := c.baselineRequest("bootstrap-1", "version-1")
	result, err := governance.Bootstrap(context.Background(), e.store, request)
	if err != nil || !result.Committed {
		e.t.Fatalf("bootstrap = %+v, %v, want a committed promotion", result, err)
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		receipt, found, err := tx.Receipt(ctx, "bootstrap-1")
		if err != nil || !found || receipt.Kind != governance.OperationBootstrap || receipt.Promotion == nil || receipt.Promotion.Identity.Kind != governance.OperationBootstrap {
			e.t.Errorf("receipt = %+v found %v, %v, want a bootstrap receipt", receipt, found, err)
		}
		return nil
	})
}

// cfOtherSuite seeds a Suite of another project with one candidate.
func (e *cfEnv) seedOtherSuite() cfCandidate {
	other := cfNewCandidate("project-b", "suite-b", "pb", "", "v1")
	e.seedSimple("project-b", "suite-b", other)
	return other
}

func cfOperationAcrossSuites(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	other := e.seedOtherSuite()
	original := e.approve(c)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"approve-p1"}, sources: []contract.SourceCommandID{"comment-p1", "comment-other"}}
	before := e.view(probe)
	otherProbe := cfProbe{proposal: "pb", operations: []contract.OperationID{"approve-p1"}, sources: []contract.SourceCommandID{"comment-p1", "comment-other"}}
	otherBefore := e.viewIn("project-b", "suite-b", otherProbe)

	command := e.command(other, "revision-1", "approve-p1", "comment-other", contract.ApproveConsent, 1)
	_, err := governance.ProcessConsent(context.Background(), e.store, governance.ConsentRequest{Command: command})
	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("operation id of another Suite = %v, want an operation conflict", err)
	}
	e.requireSameView(before, e.view(probe))
	e.requireSameView(otherBefore, e.viewIn("project-b", "suite-b", otherProbe))

	// Receipt lookups are global: the other Suite sees the receipt and its owner.
	err = e.doIn("project-b", "suite-b", func(ctx context.Context, tx governance.Tx) error {
		receipt, found, err := tx.Receipt(ctx, "approve-p1")
		if err != nil || !found || receipt.ProjectID != cfProject || receipt.SuiteID != cfSuite || receipt.Consent == nil || *receipt.Consent != original.Receipt {
			e.t.Errorf("global receipt lookup = %+v found %v, %v, want the receipt of %s/%s", receipt, found, err, cfProject, cfSuite)
		}
		// A store rejects the write itself, not only the use case that checks first.
		write, err := e.consentWrite(ctx, tx, command)
		if err != nil {
			return err
		}
		return tx.AppendConsent(ctx, write)
	})
	if !errors.Is(err, governance.ErrOperationConflict) {
		e.t.Fatalf("writing an operation id of another Suite = %v, want an operation conflict", err)
	}
	e.requireSameView(otherBefore, e.viewIn("project-b", "suite-b", otherProbe))
}

func (e *cfEnv) viewIn(project contract.ProjectID, suite contract.SuiteID, p cfProbe) cfView {
	e.t.Helper()
	inner := &cfEnv{t: e.t, store: &cfSuiteStore{Store: e.store, project: project, suite: suite}, owner: e.owner}
	return inner.view(p)
}

// cfSuiteStore redirects the default Suite of an env to another Suite.
type cfSuiteStore struct {
	Store
	project contract.ProjectID
	suite   contract.SuiteID
}

func (s *cfSuiteStore) Do(ctx context.Context, _ contract.ProjectID, _ contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return s.Store.Do(ctx, s.project, s.suite, fn)
}

func cfOperationAcrossKinds(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	fresh := e.candidate("p2", "", "v2") // never promoted: only its operation id can conflict
	e.seedSimple(cfProject, cfSuite, c, fresh)
	e.approve(c)
	e.promote(c.mergedRequest("promote-1", "version-1"))
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"approve-p1", "promote-1"}, sources: []contract.SourceCommandID{"comment-other"},
		versions: []contract.SuiteVersionID{"version-2"}, references: []contract.ProposalReference{c.current(), fresh.current()}}
	before := e.view(probe)

	e.run("a consent operation id used by a promotion", func(e *cfEnv) {
		_, err := governance.Promote(context.Background(), e.store, c.mergedRequest("approve-p1", "version-2"))
		if !errors.Is(err, governance.ErrOperationConflict) {
			e.t.Fatalf("error = %v, want an operation conflict", err)
		}
	})
	e.run("a promotion operation id used by a consent command", func(e *cfEnv) {
		command := e.command(c, "revision-1", "promote-1", "comment-other", contract.ApproveConsent, 2)
		_, err := governance.ProcessConsent(context.Background(), e.store, governance.ConsentRequest{Command: command})
		if !errors.Is(err, governance.ErrOperationConflict) {
			e.t.Fatalf("error = %v, want an operation conflict", err)
		}
	})
	e.run("a promotion operation id used by another promotion kind", func(e *cfEnv) {
		_, err := governance.Bootstrap(context.Background(), e.store, c.baselineRequest("promote-1", "version-1"))
		if !errors.Is(err, governance.ErrOperationConflict) {
			e.t.Fatalf("error = %v, want an operation conflict", err)
		}
	})
	e.run("a promotion operation id used by a changed request", func(e *cfEnv) {
		_, err := governance.Promote(context.Background(), e.store, c.mergedRequest("promote-1", "version-2"))
		if !errors.Is(err, governance.ErrOperationConflict) {
			e.t.Fatalf("error = %v, want an operation conflict", err)
		}
	})
	e.run("a store rejects an operation id of another kind on write", func(e *cfEnv) {
		err := e.do(func(ctx context.Context, tx governance.Tx) error {
			write := e.promotionWriteWith(fresh, "approve-p1", "version-2", "", cfMust(cfSchedule(ctx, tx)))
			return tx.RecordPromotion(ctx, write)
		})
		if !errors.Is(err, governance.ErrOperationConflict) {
			e.t.Fatalf("error = %v, want an operation conflict", err)
		}
	})
	e.requireSameView(before, e.view(probe))
}

func cfSchedule(ctx context.Context, tx governance.Tx) (contract.Schedule, error) {
	state, err := tx.Suite(ctx)
	return state.Schedule, err
}

func cfSourceReplayConflicts(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	d := e.candidate("p2", "", "v2")
	e.seedSimple(cfProject, cfSuite, c, d)
	other := e.seedOtherSuite()
	e.approve(c)
	agent := cfMust(contract.NewPrincipal("agent-1", contract.Agent))
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"replay-agent", "replay-other-proposal"}, sources: []contract.SourceCommandID{"comment-p1"}}
	before := e.view(probe)

	for _, tc := range []struct {
		name    string
		command contract.Command
	}{
		{"another actor", e.commandBy(agent, c, "revision-1", "replay-agent", "comment-p1", contract.ApproveConsent, 2)},
		{"another proposal", e.command(d, "revision-1", "replay-other-proposal", "comment-p1", contract.ApproveConsent, 2)},
		{"another suite", e.command(other, "revision-1", "replay-other-suite", "comment-p1", contract.ApproveConsent, 2)},
	} {
		e.run(tc.name, func(e *cfEnv) {
			_, err := governance.ProcessConsent(context.Background(), e.store, governance.ConsentRequest{Command: tc.command})
			if !errors.Is(err, governance.ErrOperationConflict) {
				e.t.Fatalf("error = %v, want an operation conflict", err)
			}
		})
	}
	e.requireSameView(before, e.view(probe))
}

func cfAlias(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	original := e.approve(c)
	before := e.revision()
	retry := e.command(c, "revision-1", "approve-retry", "comment-p1", contract.ApproveConsent, 1)

	alias := e.consent(retry)
	if !alias.Duplicate || !alias.Committed || alias.Receipt != original.Receipt {
		e.t.Fatalf("alias = %+v, want the original receipt as a committed duplicate", alias)
	}
	e.requireWrites(before, 1)
	if got := e.results("p1"); len(got) != 1 {
		e.t.Errorf("results after the alias = %d, want the original only", len(got))
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		byAlias, found, err := tx.Receipt(ctx, "approve-retry")
		if err != nil || !found || byAlias.Kind != governance.OperationConsent || byAlias.Consent == nil || *byAlias.Consent != original.Receipt {
			e.t.Errorf("receipt of the alias operation = %+v found %v, %v, want the original receipt", byAlias, found, err)
		}
		bySource, found, err := tx.ReceiptBySource(ctx, "comment-p1")
		if err != nil || !found || bySource.Consent == nil || *bySource.Consent != original.Receipt || bySource.Consent.Result.Command().OperationID() != "approve-p1" {
			e.t.Errorf("receipt of the source command = %+v found %v, %v, want the original, still pointing at the original operation", bySource, found, err)
		}
		return nil
	})

	again := e.consent(retry)
	if !again.Duplicate || again.Receipt != original.Receipt {
		e.t.Errorf("alias replay = %+v, want the original receipt", again)
	}
	e.requireWrites(before, 1)
	// The original operation still replays, and a second alias is another write.
	if replay := e.consent(e.command(c, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1)); !replay.Duplicate || replay.Receipt != original.Receipt {
		e.t.Errorf("original replay = %+v, want the original receipt", replay)
	}
	e.requireWrites(before, 1)
	if second := e.consent(e.command(c, "revision-1", "approve-retry-2", "comment-p1", contract.ApproveConsent, 1)); !second.Duplicate || second.Receipt != original.Receipt {
		e.t.Errorf("second alias = %+v, want the original receipt", second)
	}
	e.requireWrites(before, 2)
}

// Writes and reads inside one unit of work.

func cfRevisionAdvances(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	before := e.revision()
	approve := e.command(c, "revision-1", "op-approve", "src-approve", contract.ApproveConsent, 1)
	revoke := e.command(c, "revision-1", "op-revoke", "src-revoke", contract.RevokeConsent, 2)
	replay := e.command(c, "revision-1", "op-alias", "src-approve", contract.ApproveConsent, 1)

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		at := func(want int, step string) {
			state, err := tx.Suite(ctx)
			if err != nil || state.Canonical.Suite().Revision() != before+contract.StateRevision(want) {
				e.t.Errorf("revision after %s = %d, %v, want %d", step, state.Canonical.Suite().Revision(), err, before+contract.StateRevision(want))
			}
		}
		write, err := e.consentWrite(ctx, tx, approve)
		if err != nil {
			return err
		}
		if err := tx.AppendConsent(ctx, write); err != nil {
			return fmt.Errorf("append approval: %w", err)
		}
		at(1, "the approval")
		if receipt, found, err := tx.Receipt(ctx, "op-approve"); err != nil || !found || receipt.Consent == nil || *receipt.Consent != write.Receipt {
			e.t.Errorf("own write not visible by operation: found %v, %v", found, err)
		}
		if _, found, err := tx.ReceiptBySource(ctx, "src-approve"); err != nil || !found {
			e.t.Errorf("own write not visible by source command: found %v, %v", found, err)
		}
		proposal, consent, err := tx.Proposal(ctx, "p1")
		if err != nil {
			return err
		}
		if !consent.HasApproval(proposal, e.policy(cfProject)) || len(consent.Results()) != 1 {
			e.t.Errorf("own approval not visible: results %d", len(consent.Results()))
		}

		if write, err = e.consentWrite(ctx, tx, revoke); err != nil {
			return err
		}
		if err := tx.AppendConsent(ctx, write); err != nil {
			return fmt.Errorf("append revocation: %w", err)
		}
		at(2, "the revocation")
		if _, consent, err = tx.Proposal(ctx, "p1"); err != nil {
			return err
		}
		if consent.HasApproval(proposal, e.policy(cfProject)) || len(consent.Results()) != 2 {
			e.t.Errorf("own revocation not visible: results %d", len(consent.Results()))
		}

		if write, err = e.consentWrite(ctx, tx, replay); err != nil {
			return err
		}
		if !write.Alias {
			e.t.Error("a replayed source command did not produce an alias write")
		}
		if err := tx.AppendConsent(ctx, write); err != nil {
			return fmt.Errorf("append alias: %w", err)
		}
		at(3, "the alias")
		if _, found, err := tx.Receipt(ctx, "op-alias"); err != nil || !found {
			e.t.Errorf("own alias not visible: found %v, %v", found, err)
		}

		promotion, err := e.promotionWrite(ctx, tx, c, "op-promote", "version-1", "")
		if err != nil {
			return err
		}
		if err := tx.RecordPromotion(ctx, promotion); err != nil {
			return fmt.Errorf("record promotion: %w", err)
		}
		at(4, "the promotion")
		state, err := tx.Suite(ctx)
		if err != nil {
			return err
		}
		if current, _ := state.Canonical.Suite().CurrentVersionID(); current != "version-1" {
			e.t.Errorf("own promotion not visible: current version %q", current)
		}
		if _, found, err := tx.Version(ctx, "version-1"); err != nil || !found {
			e.t.Errorf("own version not visible: found %v, %v", found, err)
		}
		if _, found, err := tx.PromotionFor(ctx, c.current()); err != nil || !found {
			e.t.Errorf("own promotion record not visible: found %v, %v", found, err)
		}
		if _, found, err := tx.Receipt(ctx, "op-promote"); err != nil || !found {
			e.t.Errorf("own promotion receipt not visible: found %v, %v", found, err)
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.requireWrites(before, 4)
	if got := e.results("p1"); len(got) != 2 {
		e.t.Errorf("committed results = %d, want the approval and the revocation", len(got))
	}
}

func cfRejectedResults(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	wrongCarrier := cfMust(contract.NewCommand(contract.CommandInput{OperationID: "op-rejected", SourceCommandID: "src-rejected", Actor: e.owner,
		Reference: c.current(), Carrier: "another-carrier", Action: contract.ApproveConsent, Order: 100}))

	rejected := e.consent(wrongCarrier)
	if rejected.Receipt.Result.Outcome() != contract.ConsentRejected || rejected.Receipt.Result.Reason() != contract.ConsentReasonContextMismatch || rejected.Receipt.CurrentApprovalEligible {
		e.t.Fatalf("receipt = %+v, want a rejection for the foreign carrier", rejected.Receipt)
	}
	before := e.revision()

	// The same order as the rejected command follows the domain rule: it is rejected, too.
	tied := e.consent(e.command(c, "revision-1", "op-tied", "src-tied", contract.ApproveConsent, 100))
	if tied.Receipt.Result.Outcome() != contract.ConsentRejected || tied.Receipt.Result.Reason() != contract.ConsentReasonObsoleteCommand {
		e.t.Errorf("equal order = %v/%v, want rejected/obsolete command", tied.Receipt.Result.Outcome(), tied.Receipt.Result.Reason())
	}
	// A rejection consumes no order: a lower order is still accepted.
	approved := e.consent(e.command(c, "revision-1", "op-approved", "src-approved", contract.ApproveConsent, 20))
	if approved.Receipt.Result.Outcome() != contract.ConsentApproved || !approved.Receipt.CurrentApprovalEligible {
		e.t.Errorf("lower order = %v/%v eligible %v, want an eligible approval", approved.Receipt.Result.Outcome(), approved.Receipt.Result.Reason(), approved.Receipt.CurrentApprovalEligible)
	}
	e.requireWrites(before, 2)

	results := e.results("p1")
	wantOrders, wantOutcomes := []contract.CommandOrder{100, 100, 20}, []contract.ConsentOutcome{contract.ConsentRejected, contract.ConsentRejected, contract.ConsentApproved}
	if len(results) != 3 {
		e.t.Fatalf("stored results = %d, want 3 in processing order", len(results))
	}
	for i, result := range results {
		if result.Command().Order() != wantOrders[i] || result.Outcome() != wantOutcomes[i] || result.Duplicate() {
			e.t.Errorf("result %d = order %d %v duplicate %v, want order %d %v", i, result.Command().Order(), result.Outcome(), result.Duplicate(), wantOrders[i], wantOutcomes[i])
		}
	}

	// A rejected command replays by operation id and by source command id.
	replay := e.consent(wrongCarrier)
	if !replay.Duplicate || !replay.Committed || replay.Receipt != rejected.Receipt {
		e.t.Errorf("replay = %+v, want the original rejection as a duplicate", replay)
	}
	e.requireWrites(before, 2)
	alias := e.consent(cfMust(contract.NewCommand(contract.CommandInput{OperationID: "op-rejected-retry", SourceCommandID: "src-rejected", Actor: e.owner,
		Reference: c.current(), Carrier: "another-carrier", Action: contract.ApproveConsent, Order: 100})))
	if !alias.Duplicate || alias.Receipt != rejected.Receipt {
		e.t.Errorf("alias = %+v, want the original rejection as a duplicate", alias)
	}
	e.requireWrites(before, 3)
	if got := e.results("p1"); len(got) != 3 {
		e.t.Errorf("results after replays = %d, want 3", len(got))
	}
}

// Failure and cancellation.

var errCfAbort = errors.New("conformance: abort the unit of work")

func cfRollbackOnError(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"rb-approve", "rb-promote"}, sources: []contract.SourceCommandID{"rb-comment"},
		versions: []contract.SuiteVersionID{"version-1"}, references: []contract.ProposalReference{c.current()}}
	before := e.view(probe)
	approve := e.command(c, "revision-1", "rb-approve", "rb-comment", contract.ApproveConsent, 1)

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		write, err := e.consentWrite(ctx, tx, approve)
		if err != nil {
			return err
		}
		if err := tx.AppendConsent(ctx, write); err != nil {
			return err
		}
		promotion, err := e.promotionWrite(ctx, tx, c, "rb-promote", "version-1", "")
		if err != nil {
			return err
		}
		if err := tx.RecordPromotion(ctx, promotion); err != nil {
			return err
		}
		return errCfAbort
	})
	if !errors.Is(err, errCfAbort) {
		e.t.Fatalf("error = %v, want the error of the unit of work", err)
	}
	e.requireSameView(before, e.view(probe))

	// Nothing of the failed attempt is left in the way of the same work.
	if retry := e.consent(approve); !retry.Committed || retry.Duplicate || !retry.Receipt.CurrentApprovalEligible {
		e.t.Errorf("retry = %+v, want a new committed approval", retry)
	}
}

func cfRollbackOnWriteFailure(e *cfEnv) {
	fail, ok := e.store.(FailNexter)
	if !ok {
		e.t.Skip("the store cannot inject a write failure")
	}
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"approve-p1", "promote-1"}, sources: []contract.SourceCommandID{"comment-p1"},
		versions: []contract.SuiteVersionID{"version-1"}, references: []contract.ProposalReference{c.current()}}
	boom := errors.New("conformance: injected write failure")
	approve := e.command(c, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1)

	before := e.view(probe)
	fail.FailNext(boom)
	if _, err := governance.ProcessConsent(context.Background(), e.store, governance.ConsentRequest{Command: approve}); !errors.Is(err, boom) {
		e.t.Fatalf("consent error = %v, want the injected failure", err)
	}
	e.requireSameView(before, e.view(probe))
	if retry := e.consent(approve); !retry.Committed || retry.Duplicate {
		e.t.Fatalf("consent retry = %+v, want a new committed approval", retry)
	}

	before = e.view(probe)
	request := c.mergedRequest("promote-1", "version-1")
	fail.FailNext(boom)
	if _, err := governance.Promote(context.Background(), e.store, request); !errors.Is(err, boom) {
		e.t.Fatalf("promotion error = %v, want the injected failure", err)
	}
	e.requireSameView(before, e.view(probe))
	if retry := e.promote(request); !retry.Committed || retry.Duplicate {
		e.t.Errorf("promotion retry = %+v, want a new committed promotion", retry)
	}
}

func cfCanceledContext(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"approve-p1"}, sources: []contract.SourceCommandID{"comment-p1"}}
	before := e.view(probe)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	err := e.store.Do(ctx, cfProject, cfSuite, func(context.Context, governance.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		e.t.Errorf("Do = %v (function called: %v), want context canceled without running the function", err, called)
	}
	approve := e.command(c, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1)
	if _, err := governance.ProcessConsent(ctx, e.store, governance.ConsentRequest{Command: approve}); !errors.Is(err, context.Canceled) {
		e.t.Errorf("ProcessConsent = %v, want context canceled", err)
	}
	e.requireSameView(before, e.view(probe))
}

func cfCancellationDuringWork(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"approve-p1"}, sources: []contract.SourceCommandID{"comment-p1"}}
	before := e.view(probe)
	approve := e.command(c, "revision-1", "approve-p1", "comment-p1", contract.ApproveConsent, 1)

	e.run("canceled after a write and returning nil", func(e *cfEnv) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := e.store.Do(ctx, cfProject, cfSuite, func(ctx context.Context, tx governance.Tx) error {
			write, err := e.consentWrite(ctx, tx, approve)
			if err != nil {
				return err
			}
			if err := tx.AppendConsent(ctx, write); err != nil {
				return err
			}
			cancel()
			return nil
		})
		if err == nil {
			e.t.Fatal("Do committed a unit of work whose context was canceled before it ended")
		}
		e.requireSameView(before, e.view(probe))
	})
	e.run("a write with a canceled context fails", func(e *cfEnv) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var writeErr error
		_ = e.store.Do(ctx, cfProject, cfSuite, func(_ context.Context, tx governance.Tx) error {
			write, err := e.consentWrite(context.Background(), tx, approve)
			if err != nil {
				return err
			}
			cancel()
			writeErr = tx.AppendConsent(ctx, write)
			return writeErr
		})
		if writeErr == nil {
			e.t.Error("AppendConsent succeeded with a canceled context")
		}
		e.requireSameView(before, e.view(probe))
	})
}

// Promotion conflicts and round trip.

func cfVersionConflict(e *cfEnv) {
	r := e.seedRich()
	probe := cfProbe{proposal: "p2", operations: []contract.OperationID{"vc-op-1", "vc-op-2"}, versions: []contract.SuiteVersionID{"version-3", "version-9"},
		references: []contract.ProposalReference{r.open.current(), r.second.current()}}
	before := e.view(probe)

	for _, attempt := range []struct {
		name      string
		candidate cfCandidate
		operation string
		version   contract.SuiteVersionID
	}{
		{"an existing version id", r.open, "vc-op-1", "version-1"},
		{"an already promoted proposal revision", r.second, "vc-op-2", "version-9"},
	} {
		e.run(attempt.name, func(e *cfEnv) {
			err := e.do(func(ctx context.Context, tx governance.Tx) error {
				state, err := tx.Suite(ctx)
				if err != nil {
					return err
				}
				return tx.RecordPromotion(ctx, e.promotionWriteWith(attempt.candidate, attempt.operation, attempt.version, "", state.Schedule))
			})
			if !errors.Is(err, governance.ErrVersionConflict) {
				e.t.Fatalf("error = %v, want a version conflict", err)
			}
			e.requireSameView(before, e.view(probe))
		})
	}
}

func cfUnknownPromotionReference(e *cfEnv) {
	e.seedRich()
	probe := cfProbe{proposal: "p2", operations: []contract.OperationID{"ghost-op"}, versions: []contract.SuiteVersionID{"version-9"}}
	before := e.view(probe)

	for _, tc := range []struct {
		name  string
		ghost cfCandidate
	}{
		{"an unknown proposal", e.candidate("ghost", "version-2", "v3")},
		{"an unknown revision of a known proposal", e.candidate("p2", "version-2", "v3", "revision-9")},
	} {
		e.run(tc.name, func(e *cfEnv) {
			err := e.do(func(ctx context.Context, tx governance.Tx) error {
				state, err := tx.Suite(ctx)
				if err != nil {
					return err
				}
				return tx.RecordPromotion(ctx, e.promotionWriteWith(tc.ghost, "ghost-op", "version-9", "", state.Schedule))
			})
			if err == nil {
				e.t.Fatal("RecordPromotion accepted a promotion of a proposal revision the store does not hold")
			}
			e.requireSameView(before, e.view(probe))
		})
	}
}

func cfPromotionRoundTrip(e *cfEnv) {
	r := e.seedRich()
	before := e.revision()
	wantSchedule := cfMust(r.seed.Suite.Schedule.Observe(r.open.proposal, contract.ObservePromoted))
	var write governance.PromotionWrite
	err := e.do(func(ctx context.Context, tx governance.Tx) (err error) {
		if write, err = e.promotionWrite(ctx, tx, r.open, "round-trip", "version-3", "version-1"); err != nil {
			return err
		}
		return tx.RecordPromotion(ctx, write)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.requireWrites(before, 1)
	record := write.Receipt.Record

	state := e.state()
	if current, _ := state.Canonical.Suite().CurrentVersionID(); current != "version-3" || state.Canonical.Version().ID() != "version-3" {
		e.t.Errorf("current version = %q, want version-3", current)
	}
	if !state.Canonical.Contract().Equal(r.open.protected) || !cfSameRecord(state.Canonical.Record(), record) {
		e.t.Error("canonical does not carry the promoted contract and its record")
	}
	if !cfSameSchedule(state.Schedule, wantSchedule) {
		e.t.Errorf("schedule = %d %v, want %d %v", state.Schedule.Generation(), state.Schedule.Entries(), wantSchedule.Generation(), wantSchedule.Entries())
	}
	if _, active := state.Schedule.Active(); active {
		e.t.Error("the promoted proposal still holds the active schedule entry")
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		history, found, err := tx.Version(ctx, "version-3")
		if err != nil || !found || history.Version().Manifest().Digest() != r.open.protected.Manifest().Digest() || !cfSameRecord(history.Record(), record) {
			e.t.Errorf("version-3 = found %v, %v, want the promoted version with its record", found, err)
		}
		if history.Record().CorrectsVersionID() != "version-1" {
			e.t.Errorf("corrected version = %q, want version-1", history.Record().CorrectsVersionID())
		}
		promoted, found, err := tx.PromotionFor(ctx, r.open.current())
		if err != nil || !found || !cfSameRecord(promoted, record) {
			e.t.Errorf("promotion for the reference = found %v, %v, want the record", found, err)
		}
		for i, previous := range r.versions {
			if got, found, err := tx.Version(ctx, previous.Version().ID()); err != nil || !found || !cfSameHistory(got, previous) {
				e.t.Errorf("earlier version %d = found %v, %v, want it unchanged", i, found, err)
			}
		}
		receipt, found, err := tx.Receipt(ctx, "round-trip")
		if err != nil || !found || receipt.Kind != governance.OperationCorrect || receipt.Promotion == nil || !cfSamePromotionReceipt(*receipt.Promotion, write.Receipt) {
			e.t.Errorf("receipt = %+v found %v, %v, want the stored correction receipt", receipt, found, err)
		}
		return nil
	})
}

func cfNotFound(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	called := false
	for _, ids := range []struct {
		name    string
		project contract.ProjectID
		suite   contract.SuiteID
	}{{"suite", cfProject, "unknown-suite"}, {"project", "unknown-project", cfSuite}} {
		err := e.doIn(ids.project, ids.suite, func(context.Context, governance.Tx) error {
			called = true
			return nil
		})
		if !errors.Is(err, governance.ErrNotFound) || called {
			e.t.Errorf("Do on an unknown %s = %v (function called: %v), want not found without running the function", ids.name, err, called)
		}
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		if _, _, err := tx.Proposal(ctx, "unknown-proposal"); !errors.Is(err, governance.ErrNotFound) {
			e.t.Errorf("unknown proposal = %v, want not found", err)
		}
		if _, found, err := tx.Assessment(ctx, c.reference("revision-9"), "src"); err != nil || found {
			e.t.Errorf("assessment of an unknown revision = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.Version(ctx, "unknown-version"); err != nil || found {
			e.t.Errorf("unknown version = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.PromotionFor(ctx, c.current()); err != nil || found {
			e.t.Errorf("promotion of an unpromoted proposal = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.Receipt(ctx, "unknown-operation"); err != nil || found {
			e.t.Errorf("unknown operation = found %v, %v, want not found", found, err)
		}
		if _, found, err := tx.ReceiptBySource(ctx, "unknown-source"); err != nil || found {
			e.t.Errorf("unknown source command = found %v, %v, want not found", found, err)
		}
		return nil
	})
}

func cfTransactionClosed(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"late-approve"}, sources: []contract.SourceCommandID{"late-comment"}}
	before := e.view(probe)
	approve := e.command(c, "revision-1", "late-approve", "late-comment", contract.ApproveConsent, 1)

	var leaked governance.Tx
	var write governance.ConsentWrite
	err := e.do(func(ctx context.Context, tx governance.Tx) (err error) {
		leaked = tx
		write, err = e.consentWrite(ctx, tx, approve)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := leaked.Suite(ctx); err == nil {
		e.t.Error("a transaction can still be read after its unit of work ended")
	}
	if err := leaked.AppendConsent(ctx, write); err == nil {
		e.t.Error("a transaction can still be written after its unit of work ended")
	}
	e.requireSameView(before, e.view(probe))
}
