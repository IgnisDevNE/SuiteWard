package governancetest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The cases of the M1.7 merge outcome: PromoteMerge end to end on real inventories, and the two Tx methods it uses.

const mcDeclaration = "version: 1\ninclude: [/tests/**]\n"

var mcContents = []string{"v1", "v2", "v3", "v4"}

func mcBlob(content string) []byte { return []byte("merge:" + content) }

// mcTree is a pinned source tree: regular files, plus the entries and the read failure a case adds.
type mcTree struct {
	files   map[string]string
	extra   []inventory.Entry
	readErr error // fails the read of the protected test file
}

func mcTreeOf(content string) *mcTree {
	return &mcTree{files: map[string]string{scope.DeclarationPath: mcDeclaration, "tests/a.txt": string(mcBlob(content)), "src/main.go": "package main\n"}}
}

func (t *mcTree) Entries(context.Context) ([]inventory.Entry, error) {
	entries := slices.Clone(t.extra)
	for path := range t.files {
		entries = append(entries, inventory.Entry{Path: path, Kind: inventory.File})
	}
	return entries, nil
}

func (t *mcTree) Read(_ context.Context, path string) ([]byte, error) {
	if path == "tests/a.txt" && t.readErr != nil {
		return nil, t.readErr
	}
	content, found := t.files[path]
	if !found {
		return nil, fmt.Errorf("no file %q", path)
	}
	return []byte(content), nil
}

// mcDiscard is the content writer of the cases: a store that verifies content was given every blob by ConformanceBlobs.
type mcDiscard struct{}

func (mcDiscard) Put(context.Context, artifact.Digest, io.Reader) error { return nil }

func mcContract(content string) contract.ProtectedContract {
	return cfMust(cfMust(inventory.Build(context.Background(), mcTreeOf(content))).Contract())
}

// mcCandidate is an open change proposal of the merge cases: its contract is the real inventory of tree content.
func mcCandidate(id string, baseline contract.SuiteVersionID, content string) cfCandidate {
	return cfNewCandidateOf(cfProject, cfSuite, id, baseline, mcContract(content))
}

func mcSource(c cfCandidate) contract.SourceRevision {
	return contract.SourceRevision("merge-sha-" + c.id)
}

// mcSeedAs seeds a Suite whose history is the given versions in order (the first holds tree v1, the second tree v2,
// and so on), the last of them current, next to the open candidates.
func (e *cfEnv) mcSeedAs(ids []contract.SuiteVersionID, open ...cfCandidate) {
	e.t.Helper()
	e.mcSeedWith(ids, false, open...)
}

// mcSeedWith is mcSeedAs where the proposal of the first version is a PR head (a bootstrap by merge) when firstPull.
func (e *cfEnv) mcSeedWith(ids []contract.SuiteVersionID, firstPull bool, open ...cfCandidate) {
	e.t.Helper()
	seed := governance.Seed{}
	var history []contract.HistoricalCanonical
	var last cfCandidate
	baseline := contract.SuiteVersionID("")
	for i, id := range ids {
		c := mcCandidate(fmt.Sprintf("p%d", i), baseline, fmt.Sprintf("v%d", i+1))
		if i == 0 && firstPull {
			c = c.asPull()
		}
		version := cfMust(contract.NewSuiteVersion(cfProject, cfSuite, id, c.protected.Manifest()))
		record := cfMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: contract.OperationID("seed-op-" + c.id), VersionID: id,
			Binding: c.proposal.Current().Binding(), Carrier: c.carrier, Source: c.merged, Target: cfTarget, RecordedAt: cfRecordedAt}))
		history = append(history, cfMust(contract.NewHistoricalCanonical(version, record)))
		seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: c.proposal, Assessments: c.assessments})
		baseline, last = id, c
	}
	current := history[len(history)-1]
	suite := cfMust(contract.NewSuite(cfProject, cfSuite, baseline, cfSeededRevision))
	canonical := cfMust(contract.NewCanonicalSnapshot(suite, current.Version(), last.protected, current.Record()))
	for _, c := range open {
		seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: c.proposal, Assessments: c.assessments})
	}
	seed.Suite, seed.History = governance.SuiteState{Canonical: canonical, Policy: e.policy(cfProject), Target: cfTarget}, history
	e.seed(seed)
}

// mcSeed seeds V1 (tree v1), and V2 (tree v2) when second, as the current version.
func (e *cfEnv) mcSeed(second bool, open ...cfCandidate) {
	e.t.Helper()
	ids := []contract.SuiteVersionID{"V1"}
	if second {
		ids = append(ids, "V2")
	}
	e.mcSeedAs(ids, open...)
}

// mcMessages records the messages a request asked for.
type mcMessages struct {
	calls int
	last  governance.MergeOutcome
	key   string
	mute  bool // answer false, as for an outcome that has nothing to tell
	fail  error
}

func (m *mcMessages) message(outcome governance.MergeOutcome) (governance.OutboxMessage, bool, error) {
	m.calls++
	m.last = outcome
	if m.fail != nil {
		return governance.OutboxMessage{}, false, m.fail
	}
	if m.mute {
		return governance.OutboxMessage{}, false, nil
	}
	return governance.OutboxMessage{Key: m.key, Kind: "merge.outcome", Payload: cfObject}, true, nil
}

// mcRequest is the merge of candidate c, whose merge commit has the tree of content, observed after the canonical.
func mcRequest(c cfCandidate, content string, messages *mcMessages) governance.MergeRequest {
	if messages.key == "" {
		messages.key = "comment/" + string(c.carrier)
	}
	return governance.MergeRequest{Project: cfProject, Suite: cfSuite, Carrier: c.carrier, Source: mcSource(c), Target: cfTarget,
		ObservedAt: cfRecordedAt.Add(time.Hour), Tree: mcTreeOf(content), RecordedAt: cfRecordedAt.Add(2 * time.Hour), Message: messages.message}
}

func (e *cfEnv) merge(request governance.MergeRequest) governance.MergeResult {
	e.t.Helper()
	result, err := governance.PromoteMerge(context.Background(), e.store, mcDiscard{}, request)
	if err != nil {
		e.t.Fatalf("promote merge of %q: %v", request.Carrier, err)
	}
	return result
}

func (e *cfEnv) mergeErr(request governance.MergeRequest) error {
	e.t.Helper()
	if _, err := governance.PromoteMerge(context.Background(), e.store, mcDiscard{}, request); err == nil {
		e.t.Fatalf("promote merge of %q succeeded, want an error", request.Carrier)
	} else {
		return err
	}
	return nil
}

func (e *cfEnv) outcomeOf(carrier contract.ApprovalCarrierID) (governance.MergeOutcome, bool) {
	e.t.Helper()
	outcome, found, err := governance.MergeOutcomeOf(context.Background(), e.store, cfProject, cfSuite, carrier)
	if err != nil {
		e.t.Fatalf("outcome of %q: %v", carrier, err)
	}
	return outcome, found
}

// requireOutcome fails unless got is want, the reference compared by value, and unless MergeOutcomeOf answers the same.
func (e *cfEnv) requireOutcome(carrier contract.ApprovalCarrierID, got, want governance.MergeOutcome) {
	e.t.Helper()
	same := func(a, b governance.MergeOutcome) bool {
		return a.Kind == b.Kind && a.Reason == b.Reason && a.Source == b.Source && a.Version == b.Version && a.Corrects == b.Corrects &&
			(a.Reference == nil) == (b.Reference == nil) && (a.Reference == nil || *a.Reference == *b.Reference)
	}
	show := func(o governance.MergeOutcome) string {
		reference := "none"
		if o.Reference != nil {
			reference = fmt.Sprintf("%+v", *o.Reference)
		}
		return fmt.Sprintf("{%s %q source %q version %q corrects %q reference %s}", o.Kind, o.Reason, o.Source, o.Version, o.Corrects, reference)
	}
	if !same(got, want) {
		e.t.Fatalf("outcome = %s, want %s", show(got), show(want))
	}
	if stored, found := e.outcomeOf(carrier); !found || !same(stored, want) {
		e.t.Fatalf("stored outcome = %s (found %v), want %s", show(stored), found, show(want))
	}
}

func (e *cfEnv) version(id contract.SuiteVersionID) (contract.HistoricalCanonical, bool) {
	e.t.Helper()
	var historical contract.HistoricalCanonical
	var found bool
	e.read(func(ctx context.Context, tx governance.Tx) (err error) {
		historical, found, err = tx.Version(ctx, id)
		return err
	})
	return historical, found
}

func (e *cfEnv) currentVersion() contract.SuiteVersionID {
	id, _ := e.state().Canonical.Suite().CurrentVersionID()
	return id
}

func (e *cfEnv) assessmentAt(reference contract.ProposalReference, source contract.SourceRevision) (contract.IntegrityAssessment, bool) {
	e.t.Helper()
	var assessment contract.IntegrityAssessment
	var found bool
	e.read(func(ctx context.Context, tx governance.Tx) (err error) {
		assessment, found, err = tx.Assessment(ctx, reference, source)
		return err
	})
	return assessment, found
}

func mcRef(c cfCandidate) *contract.ProposalReference {
	reference := c.current()
	return &reference
}

func mergeCases() []struct {
	name string
	run  func(*cfEnv)
} {
	return []struct {
		name string
		run  func(*cfEnv)
	}{
		{"MergePromotesAnApprovedChange", mcPromotes},
		{"MergeReplayIsADuplicateThatWritesAndSaysNothing", mcReplay},
		{"MergeCorrectsAnEarlierVersion", mcCorrects},
		{"VersionsHoldingListsEveryVersionOfTheLockedSuite", mvListsEveryVersionOfTheLockedSuite},
		{"VersionsHoldingIsEmptyWithoutVersionsAndFailsAfterTheUnitOfWork", mvIsEmptyWithoutVersionsAndFailsAfterTheUnitOfWork},
		{"MergeOfASecondPRApprovedAgainstTheSameCanonicalIsBlocked", mcSecondPRBlocked},
		{"MergeOfARebasedPRWithoutApprovalIsBlocked", mcRebasedPRBlocked},
		{"MergeOfAPRBoundToAnotherPolicyIsBlocked", mcPolicyChanged},
		{"MergeRestoringTheContentOfAPriorPRWithTheSameCarrierIsBlocked", mcCorrectionContextReused},
		{"MergeOfATreeThatDiffersFromTheApprovedRevisionIsBlockedAndAssessedAsFailed", mcDiffersFromApproved},
		{"MergeWithoutApprovalOrWithARevokedApprovalIsBlocked", mcApprovalMissing},
		{"MergeWithAnAssessmentConflictIsBlockedAndKeepsTheCanonical", mcAssessmentConflict},
		{"MergeOfAnInvalidSourceIsBlocked", mcInvalidSource},
		{"MergeWithoutAChangeProposalIsBlockedAsUnapproved", mcUnapproved},
		{"MergeOfAnUnchangedContractIsNoChange", mcNoChange},
		{"MergeIntoAnotherTargetIsNotTheTarget", mcNotTarget},
		{"MergeObservedBeforeTheCanonicalIsBeforeTheCanonical", mcBeforeCanonical},
		{"MergeOfAPRWithoutACanonicalIsBeforeTheCanonical", mcNoCanonical},
		{"MergeRecordsACallerRefusalWithoutATree", mcRefusals},
		{"MergeDecidesTargetAndTimeBeforeARefusal", mcRefusalPrecedence},
		{"MergeReturnsATreeReadErrorAndWritesNothing", mcTreeReadError},
		{"MergeCannotSucceedACurrentVersionThatIsNotNumbered", mcUnnumberedCurrent},
		{"MergeNeedsVersionV1WhileACanonicalExists", mcMissingFirstVersion},
		{"MergeMessageFailureRollsBackThePromotionAndTheOutcome", mcMessageFailure},
		{"MergeMessageFailureRollsBackACorrection", mcCorrectionMessageFailure},
		{"MergeOperationIDBelongsToItsKind", mcOperationAcrossKinds},
		{"MergeRollsBackWhenAWriteFails", mcWriteFailure},
		{"MergeOutboxConflictRollsBackTheOutcome", mcOutboxConflict},
		{"ConcurrentMergesOfOneCarrierWriteOneOutcome", mcConcurrent},
		{"MergeOutcomeOfAnUnknownCarrierOrSuite", mcOutcomeOfUnknown},
		{"RecordMergeOutcomeRoundTripsWithoutAdvancingTheRevision", mcRecordRoundTrip},
		{"RecordMergeOutcomeTwiceConflicts", mcRecordTwiceConflicts},
		{"RecordMergeOutcomeRejectsInconsistentOrDanglingWrites", mcRecordRejects},
	}
}

func mcPromotes(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	before := e.revision()
	messages := &mcMessages{}

	result := e.merge(mcRequest(pr, "v2", messages))

	want := governance.MergeOutcome{Kind: governance.MergePromoted, Source: mcSource(pr), Version: "V2", Reference: mcRef(pr)}
	e.requireOutcome(pr.carrier, result.MergeOutcome, want)
	if result.Duplicate || messages.calls != 1 || messages.last.Kind != governance.MergePromoted {
		e.t.Fatalf("duplicate %v, message calls %d (last %v); want a new outcome and one message about it", result.Duplicate, messages.calls, messages.last.Kind)
	}
	e.requireWrites(before, 2) // the assessment of the merge commit and the promotion; the outcome advances nothing
	if got := e.currentVersion(); got != "V2" {
		e.t.Fatalf("current version = %q, want V2", got)
	}
	historical, found := e.version("V2")
	record := historical.Record()
	if !found || record.OperationID() != "merge:"+contract.OperationID(pr.carrier) || record.Source() != mcSource(pr) || record.Carrier() != pr.carrier ||
		record.Target() != cfTarget || record.CorrectsVersionID() != "" || !record.RecordedAt().Equal(cfRecordedAt.Add(2*time.Hour)) {
		e.t.Fatalf("promotion record = %+v (found %v); want operation merge:%s at %s, source %s, no correction", record, found, pr.carrier, mcSource(pr), cfRecordedAt.Add(2*time.Hour))
	}
	if !e.state().Canonical.Contract().Equal(pr.protected) {
		e.t.Fatal("the canonical contract is not the merged contract")
	}
	integration := cfMust(contract.NewIntegration(cfProject, cfTarget, mcSource(pr), pr.carrier, contract.IntegrationMergedChange))
	wantReceipt := governance.OperationReceipt{Kind: governance.OperationPromote, ProjectID: cfProject, SuiteID: cfSuite, Promotion: &governance.PromotionReceipt{
		Identity: governance.PromotionIdentity{Kind: governance.OperationPromote, Binding: pr.proposal.Current().Binding(), Request: governance.PromoteRequest{
			OperationID: record.OperationID(), Reference: pr.current(), Carrier: pr.carrier, Proposed: pr.protected, AssessmentSource: mcSource(pr), Integration: integration,
			NewVersionID: "V2", RecordedAt: cfRecordedAt.Add(2 * time.Hour)}}, Record: record}}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		receipt, found, err := tx.Receipt(ctx, record.OperationID())
		if err != nil || !found || !cfSameReceipt(receipt, wantReceipt) {
			return fmt.Errorf("receipt = %+v, %v, %v; want the promotion of the merged change at %s", receipt, found, err, mcSource(pr))
		}
		if _, found, err := tx.ReceiptBySource(ctx, contract.SourceCommandID(record.OperationID())); err != nil || found {
			return fmt.Errorf("a merge promotion has a source command receipt: found %v, %v", found, err)
		}
		return nil
	})
	if assessment, found := e.assessmentAt(pr.current(), mcSource(pr)); !found || !assessment.Passed() {
		e.t.Fatalf("assessment of the merge commit = %+v (found %v), want a passed one", assessment, found)
	}
	e.requireOutbox([]string{"comment/" + string(pr.carrier)})
}

func mcReplay(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	first := e.merge(mcRequest(pr, "v2", &mcMessages{}))
	after := e.revision()
	again := &mcMessages{}

	// The request is not compared with the first one: a PR merges once.
	other := mcRequest(pr, "v3", again)
	other.Source = "another-commit"
	replay := e.merge(other)

	e.requireOutcome(pr.carrier, replay.MergeOutcome, first.MergeOutcome)
	if !replay.Duplicate || again.calls != 0 {
		e.t.Fatalf("replay duplicate %v, message calls %d; want a duplicate that calls no message", replay.Duplicate, again.calls)
	}
	e.requireWrites(after, 0)
	e.requireOutbox([]string{"comment/" + string(pr.carrier)})
}

func mcCorrects(e *cfEnv) {
	fix := mcCandidate("fix", "V2", "v1") // restores V1's content on top of V2
	e.mcSeed(true, fix)
	e.approve(fix)
	before := e.revision()
	v1, _ := e.version("V1")
	v2, _ := e.version("V2")
	messages := &mcMessages{}

	result := e.merge(mcRequest(fix, "v1", messages))

	e.requireOutcome(fix.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeCorrected, Source: mcSource(fix), Version: "V3", Corrects: "V2", Reference: mcRef(fix)})
	if result.Duplicate || messages.calls != 1 || messages.last.Kind != governance.MergeCorrected {
		e.t.Fatalf("duplicate %v, message calls %d (last %v); want a new outcome and one message about the correction", result.Duplicate, messages.calls, messages.last.Kind)
	}
	e.requireWrites(before, 2)
	historical, found := e.version("V3")
	if !found || historical.Record().CorrectsVersionID() != "V2" || e.currentVersion() != "V3" {
		e.t.Fatalf("version V3 = %+v (found %v), current %q; want a correction of V2 that is current", historical.Record(), found, e.currentVersion())
	}
	for _, earlier := range []contract.HistoricalCanonical{v1, v2} { // a correction adds a version and rewrites none
		if now, found := e.version(earlier.Version().ID()); !found || !cfSameHistory(earlier, now) {
			e.t.Fatalf("%s changed or went missing after the correction", earlier.Version().ID())
		}
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		receipt, found, err := tx.Receipt(ctx, "merge:"+contract.OperationID(fix.carrier))
		if err != nil || !found || receipt.Kind != governance.OperationCorrect || receipt.Promotion == nil || receipt.Promotion.Identity.CorrectsVersionID != "V2" {
			return fmt.Errorf("receipt = %+v, %v, %v; want a correction of V2", receipt, found, err)
		}
		return nil
	})
	e.requireOutbox([]string{"comment/" + string(fix.carrier)})

	again := &mcMessages{} // the replay asks for nothing and writes nothing
	replay := e.merge(mcRequest(fix, "v1", again))
	e.requireOutcome(fix.carrier, replay.MergeOutcome, result.MergeOutcome)
	if !replay.Duplicate || again.calls != 0 {
		e.t.Fatalf("replay duplicate %v, message calls %d; want a duplicate that calls no message", replay.Duplicate, again.calls)
	}
	e.requireWrites(before, 2)
	e.requireOutbox([]string{"comment/" + string(fix.carrier)})
}

// mcSecondPR merges a, which promotes V2, and then b, which was approved against V1 as well.
func mcSecondPR(e *cfEnv, rebind bool) (a, b cfCandidate, result governance.MergeResult) {
	a, b = mcCandidate("a", "V1", "v2"), mcCandidate("b", "V1", "v3")
	e.mcSeed(false, a, b)
	e.approve(a)
	e.approve(b)
	if first := e.merge(mcRequest(a, "v2", &mcMessages{})); first.Kind != governance.MergePromoted {
		e.t.Fatalf("first merge = %+v, want promoted", first)
	}
	if rebind { // the push after the first merge rebased b onto V2: a new revision nobody approved
		binding := cfMust(contract.NewApprovalBinding(contract.BindingInput{Reference: b.reference("revision-2"), ExpectedCanonical: "V2",
			Manifest: b.protected.Manifest().Digest(), Scope: b.protected.ScopeDigest(), PolicyRevision: cfPolicy, CoveredInputs: b.protected.CoveredInputs()}))
		revision := cfMust(contract.NewProposalRevision(binding, b.origin, cfOriginKind(binding), b.carrier))
		if err := e.do(func(ctx context.Context, tx governance.Tx) error {
			return tx.AppendProposalRevision(ctx, governance.ProposalWrite{Revision: revision})
		}); err != nil {
			e.t.Fatal(err)
		}
		b.proposal = cfMust(b.proposal.Revise(revision))
	}
	return a, b, e.merge(mcRequest(b, "v3", &mcMessages{}))
}

func mcSecondPRBlocked(e *cfEnv) {
	_, b, result := mcSecondPR(e, false)

	e.requireOutcome(b.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonCanonicalChanged,
		Source: mcSource(b), Version: "V2", Reference: mcRef(b)})
	if got := e.currentVersion(); got != "V2" {
		e.t.Fatalf("current version = %q, want V2 kept", got)
	}
	e.read(func(ctx context.Context, tx governance.Tx) error {
		if _, found, err := tx.Receipt(ctx, "merge:"+contract.OperationID(b.carrier)); err != nil || found {
			return fmt.Errorf("the blocked merge left a promotion receipt: found %v, %v", found, err)
		}
		if _, found, err := tx.PromotionFor(ctx, b.current()); err != nil || found {
			return fmt.Errorf("the blocked merge's revision was promoted: found %v, %v", found, err)
		}
		return nil
	})
}

func mcRebasedPRBlocked(e *cfEnv) {
	_, b, result := mcSecondPR(e, true)

	e.requireOutcome(b.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonApprovalMissing,
		Source: mcSource(b), Version: "V2", Reference: mcRef(b)})
	if b.current().RevisionID != "revision-2" {
		e.t.Fatalf("the reference is %v, want the rebound revision", b.current())
	}
}

func mcDiffersFromApproved(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	before := e.revision()

	result := e.merge(mcRequest(pr, "v3", &mcMessages{})) // approved v2, merged v3

	e.requireOutcome(pr.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonDiffersFromApproved,
		Source: mcSource(pr), Version: "V1", Reference: mcRef(pr)})
	e.requireWrites(before, 1) // the failed assessment stays; nothing is promoted
	if assessment, found := e.assessmentAt(pr.current(), mcSource(pr)); !found || assessment.Passed() {
		e.t.Fatalf("assessment of the merge commit = %+v (found %v), want a recorded failed one", assessment, found)
	}
	if got := e.currentVersion(); got != "V1" {
		e.t.Fatalf("current version = %q, want V1 kept", got)
	}
}

func mcApprovalMissing(e *cfEnv) {
	unapproved, revoked := mcCandidate("none", "V1", "v2"), mcCandidate("revoked", "V1", "v2")
	e.mcSeed(false, unapproved, revoked)
	e.approve(revoked)
	e.consent(e.command(revoked, "revision-1", "op-revoke", "src-revoke", contract.RevokeConsent, 2))
	before := e.revision()

	for _, c := range []cfCandidate{unapproved, revoked} {
		result := e.merge(mcRequest(c, "v2", &mcMessages{}))

		e.requireOutcome(c.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonApprovalMissing,
			Source: mcSource(c), Version: "V1", Reference: mcRef(c)})
	}
	e.requireWrites(before, 2) // one passed assessment each
}

func mcAssessmentConflict(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	before := e.revision()
	request := mcRequest(pr, "v2", &mcMessages{})
	request.Source = pr.merged // another emitter already holds an assessment at this commit

	result := e.merge(request)

	e.requireOutcome(pr.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonAssessmentConflict,
		Source: pr.merged, Version: "V1", Reference: mcRef(pr)})
	e.requireWrites(before, 0)
	if got := e.currentVersion(); got != "V1" {
		e.t.Fatalf("current version = %q, want V1 kept", got)
	}
}

func mcInvalidSource(e *cfEnv) {
	e.mcSeed(false)
	declaration := mcTreeOf("v2")
	declaration.files[scope.DeclarationPath] = "version: 2\n"
	link := mcTreeOf("v2")
	link.extra = []inventory.Entry{{Path: "tests/link", Kind: inventory.Symlink}}
	path := mcTreeOf("v2")
	path.extra = []inventory.Entry{{Path: "../escape", Kind: inventory.File}}
	before := e.revision()
	for name, tree := range map[string]*mcTree{"declaration": declaration, "symlink in scope": link, "invalid path": path} {
		carrier := contract.ApprovalCarrierID("carrier-invalid-" + name)
		messages := &mcMessages{key: "comment/" + string(carrier)}

		result := e.merge(governance.MergeRequest{Project: cfProject, Suite: cfSuite, Carrier: carrier, Source: "invalid-sha", Target: cfTarget,
			ObservedAt: cfRecordedAt.Add(time.Hour), Tree: tree, RecordedAt: cfRecordedAt.Add(2 * time.Hour), Message: messages.message})

		want := governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonInvalidSource, Source: "invalid-sha", Version: "V1"}
		e.requireOutcome(carrier, result.MergeOutcome, want)
		if messages.calls != 1 || messages.last.Reason != governance.MergeReasonInvalidSource {
			e.t.Fatalf("%s: message calls %d (last %+v), want one about the invalid source", name, messages.calls, messages.last)
		}
	}
	e.requireWrites(before, 0)
}

func mcUnapproved(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()
	stranger := mcCandidate("stranger", "V1", "v2")

	result := e.merge(mcRequest(stranger, "v2", &mcMessages{}))

	e.requireOutcome(stranger.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: governance.MergeReasonUnapprovedChange,
		Source: mcSource(stranger), Version: "V1"})
	e.requireWrites(before, 0)
}

func mcNoChange(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()
	pr := mcCandidate("pr", "V1", "v1")

	result := e.merge(mcRequest(pr, "v1", &mcMessages{}))

	e.requireOutcome(pr.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeNoChange, Source: mcSource(pr), Version: "V1"})
	e.requireWrites(before, 0)
}

func mcNotTarget(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()
	pr := mcCandidate("pr", "V1", "v2")
	messages := &mcMessages{mute: true}
	request := mcRequest(pr, "v2", messages)
	request.Target = "branch:other"

	result := e.merge(request)

	e.requireOutcome(pr.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeNotTarget, Source: mcSource(pr), Version: "V1"})
	if messages.calls != 1 {
		e.t.Fatalf("message calls = %d, want the message function asked once", messages.calls)
	}
	e.requireWrites(before, 0)
	e.requireOutbox(nil)
}

func mcBeforeCanonical(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()
	early, atTheEdge := mcCandidate("early", "V1", "v2"), mcCandidate("edge", "V1", "v1")
	messages := &mcMessages{mute: true}
	request := mcRequest(early, "v2", messages)
	request.ObservedAt = cfRecordedAt.Add(-time.Microsecond)

	result := e.merge(request)

	e.requireOutcome(early.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBeforeCanonical, Source: mcSource(early), Version: "V1"})
	e.requireWrites(before, 0)
	e.requireOutbox(nil)
	// Observed at the very moment V1 was recorded is not before it: the merge is judged on its content.
	request = mcRequest(atTheEdge, "v1", &mcMessages{})
	request.ObservedAt = cfRecordedAt
	if got := e.merge(request); got.Kind != governance.MergeNoChange {
		e.t.Fatalf("a merge observed at V1's time = %s, want no-change", got.Kind)
	}
}

// A merge of a Suite without a canonical is recorded, not refused: it is before the canonical when its PR is not the
// bootstrap PR (the cases of the bootstrap merge say the rest).
func mcNoCanonical(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()
	pr := mcCandidate("pr", "", "v2")
	messages := &mcMessages{mute: true}

	result := e.merge(mcRequest(pr, "v2", messages))

	e.requireOutcome(pr.carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBeforeCanonical, Source: mcSource(pr)})
	e.requireWrites(before, 0)
	e.requireNoCanonical()
}

func mcRefusals(e *cfEnv) {
	e.mcSeed(false)
	before := e.revision()
	for _, reason := range []governance.MergeReason{governance.MergeReasonForkUnsupported, governance.MergeReasonSharedMergeCommit,
		governance.MergeReasonIntegrationUnreadable, governance.MergeReasonIntegrityFailed} {
		carrier := contract.ApprovalCarrierID("carrier-" + string(reason))
		source := contract.SourceRevision("sha-" + string(reason))
		if reason == governance.MergeReasonIntegrationUnreadable {
			source = ""
		}
		messages := &mcMessages{key: "comment/" + string(carrier)}

		result := e.merge(governance.MergeRequest{Project: cfProject, Suite: cfSuite, Carrier: carrier, Source: source, Target: cfTarget, // no tree to read
			ObservedAt: cfRecordedAt.Add(time.Hour), Refusal: reason, RecordedAt: cfRecordedAt.Add(2 * time.Hour), Message: messages.message})

		e.requireOutcome(carrier, result.MergeOutcome, governance.MergeOutcome{Kind: governance.MergeBlocked, Reason: reason, Source: source, Version: "V1"})
		if messages.calls != 1 {
			e.t.Fatalf("%s: message calls = %d, want 1", reason, messages.calls)
		}
	}
	e.requireWrites(before, 0)
}

func mcTreeReadError(e *cfEnv) {
	pr := mcCandidate("pr", "V1", "v2")
	e.mcSeed(false, pr)
	e.approve(pr)
	before := e.revision()
	boom := errors.New("object store unavailable")
	request := mcRequest(pr, "v2", &mcMessages{})
	request.Tree = &mcTree{files: mcTreeOf("v2").files, readErr: boom}

	err := e.mergeErr(request)

	if !errors.Is(err, boom) || errors.Is(err, governance.ErrInvalidState) {
		e.t.Fatalf("error = %v, want the read error", err)
	}
	if _, found := e.outcomeOf(pr.carrier); found {
		e.t.Fatal("an outcome was recorded for a tree that could not be read")
	}
	e.requireWrites(before, 0)
}
