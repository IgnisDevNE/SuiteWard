package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// faultUnitOfWork runs the unit of work of inner with a Tx whose read method fails on its nth call (1-based) within one
// Do, or, with vanish, answers that the record does not exist. Everything the unit of work wrote before the failure is
// rolled back by inner, as for any failing unit of work.
type faultUnitOfWork struct {
	inner  governance.UnitOfWork
	method string
	nth    int
	vanish bool
	err    error
	calls  int
}

func faulting(inner governance.UnitOfWork, method string, nth int) *faultUnitOfWork {
	return &faultUnitOfWork{inner: inner, method: method, nth: nth, err: errors.New("injected " + method + " failure")}
}

func (f *faultUnitOfWork) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	f.calls = 0
	return f.inner.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error { return fn(ctx, &faultTx{Tx: tx, fault: f}) })
}

func (f *faultUnitOfWork) CreateSuite(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, policy contract.PolicyRevisionID, target contract.IntegrationTargetID) error {
	return f.inner.CreateSuite(ctx, project, suite, policy, target)
}

type faultTx struct {
	governance.Tx
	fault *faultUnitOfWork
}

// hits reports whether this call of method is the one that must fail.
func (t *faultTx) hits(method string) bool {
	if t.fault.method != method {
		return false
	}
	t.fault.calls++
	return t.fault.calls == t.fault.nth
}

// failure is the error of the failing call; nil when the call must answer "not found" instead.
func (t *faultTx) failure() error {
	if t.fault.vanish {
		return nil
	}
	return t.fault.err
}

func (t *faultTx) Suite(ctx context.Context) (governance.SuiteState, error) {
	if t.hits("Suite") {
		return governance.SuiteState{}, t.failure()
	}
	return t.Tx.Suite(ctx)
}

func (t *faultTx) Proposal(ctx context.Context, id contract.ProposalID) (contract.Proposal, contract.Consent, error) {
	if t.hits("Proposal") {
		return contract.Proposal{}, contract.Consent{}, t.failure()
	}
	return t.Tx.Proposal(ctx, id)
}

func (t *faultTx) Assessment(ctx context.Context, reference contract.ProposalReference, source contract.SourceRevision) (contract.IntegrityAssessment, bool, error) {
	if t.hits("Assessment") {
		return contract.IntegrityAssessment{}, false, t.failure()
	}
	return t.Tx.Assessment(ctx, reference, source)
}

func (t *faultTx) Version(ctx context.Context, id contract.SuiteVersionID) (contract.HistoricalCanonical, bool, error) {
	if t.hits("Version") {
		return contract.HistoricalCanonical{}, false, t.failure()
	}
	return t.Tx.Version(ctx, id)
}

func (t *faultTx) RecordAssessment(ctx context.Context, write governance.AssessmentWrite) error {
	if t.hits("RecordAssessment") {
		return t.failure()
	}
	return t.Tx.RecordAssessment(ctx, write)
}

// RecordPromotion writes through and then fails, so the failure comes with a promotion the unit of work has to undo.
func (t *faultTx) RecordPromotion(ctx context.Context, write governance.PromotionWrite) error {
	if err := t.Tx.RecordPromotion(ctx, write); err != nil {
		return err
	}
	if t.hits("RecordPromotion") {
		return t.fault.err
	}
	return nil
}

func (t *faultTx) ChangeProposalFor(ctx context.Context, carrier contract.ApprovalCarrierID) (contract.ProposalID, bool, error) {
	if t.hits("ChangeProposalFor") {
		return "", false, t.failure()
	}
	return t.Tx.ChangeProposalFor(ctx, carrier)
}

func (t *faultTx) LatestBootstrapProposal(ctx context.Context) (contract.ProposalID, bool, error) {
	if t.hits("LatestBootstrapProposal") {
		return "", false, t.failure()
	}
	return t.Tx.LatestBootstrapProposal(ctx)
}

func (t *faultTx) NextProposalID(ctx context.Context) (contract.ProposalID, error) {
	if t.hits("NextProposalID") {
		return "", t.failure()
	}
	return t.Tx.NextProposalID(ctx)
}

// fault is one read that fails: the method and which of its calls within the unit of work.
type fault struct {
	method string
	nth    int
}

func bootstrapHeadRequest(head string) governance.BootstrapHeadRequest {
	return governance.BootstrapHeadRequest{Project: projectID, Suite: suiteID, Carrier: prA, Head: contract.SourceRevision(head), Tree: headTree("v1", "package main\n")}
}

// proposeBootstrap proposes the contract of headTree("v1") as P1-R1, sourced from a head of the pull request prA.
func (w *world) proposeBootstrap() {
	w.t.Helper()
	w.propose(newFakeWriter(), governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: projectID, SuiteID: suiteID, ProposalID: "P1", RevisionID: "R1"},
		Carrier:   prA, Origin: "head-0", OriginKind: contract.OriginPull, Tree: headTree("v1", "package main\n")})
}

func TestAssessBootstrapHeadSurfacesStoreFailuresAndWritesNothing(t *testing.T) {
	for name, failure := range map[string]fault{
		"suite":                     {"Suite", 1},
		"latest bootstrap proposal": {"LatestBootstrapProposal", 1},
		"proposal":                  {"Proposal", 1},
		"assessment write":          {"RecordAssessment", 1},
		"assessment read":           {"Assessment", 1},
		"suite after the writes":    {"Suite", 2},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.proposeBootstrap()
			before := w.revision()
			uow := faulting(w.mem, failure.method, failure.nth)

			_, err := governance.AssessBootstrapHead(context.Background(), uow, bootstrapHeadRequest("head-a"))

			if !errors.Is(err, uow.err) {
				t.Fatalf("error = %v, want the injected failure", err)
			}
			w.requireWrites(before, 0)
		})
	}
}

// A head that is cancelled still reads the Suite and the latest proposal, and a failure there is an error, not a cancellation.
func TestAssessBootstrapHeadSurfacesStoreFailuresOfACancellation(t *testing.T) {
	for name, failure := range map[string]fault{
		"suite":                     {"Suite", 1},
		"latest bootstrap proposal": {"LatestBootstrapProposal", 1},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t) // no proposal, so the head is cancelled when the reads succeed
			before := w.revision()
			uow := faulting(w.mem, failure.method, failure.nth)

			_, err := governance.AssessBootstrapHead(context.Background(), uow, bootstrapHeadRequest("head-a"))

			if !errors.Is(err, uow.err) {
				t.Fatalf("error = %v, want the injected failure", err)
			}
			w.requireWrites(before, 0)
		})
	}
}

func TestAssessBootstrapHeadNeedsTheAssessmentItJustRecorded(t *testing.T) {
	w := newWorld(t)
	w.proposeBootstrap()
	before := w.revision()
	uow := faulting(w.mem, "Assessment", 1)
	uow.vanish = true

	_, err := governance.AssessBootstrapHead(context.Background(), uow, bootstrapHeadRequest("head-a"))

	if !errors.Is(err, governance.ErrInvalidState) {
		t.Fatalf("error = %v, want invalid state for an assessment that vanished", err)
	}
	w.requireWrites(before, 0)
}

func TestAssessHeadSurfacesStoreFailuresAndWritesNothing(t *testing.T) {
	for name, failure := range map[string]fault{
		"change proposal of the carrier": {"ChangeProposalFor", 1},
		"proposal id":                    {"NextProposalID", 1},
	} {
		t.Run(name, func(t *testing.T) {
			e := newHeadEnv(t)
			before := e.revision()
			uow := faulting(e.mem, failure.method, failure.nth)

			_, err := governance.AssessHead(context.Background(), uow, e.reader, e.writer, e.request(prA, "head-a", headTree("v2", "package main\n")))

			if !errors.Is(err, uow.err) {
				t.Fatalf("error = %v, want the injected failure", err)
			}
			e.requireWrites(before, 0)
			if e.hasProposal("P2") {
				t.Fatal("a proposal outlived the failed assessment")
			}
		})
	}
}

func TestAssessHeadSurfacesTheFailureToLoadTheCarriersProposal(t *testing.T) {
	e := newHeadEnv(t)
	e.assess(prA, "head-a", headTree("v2", "package main\n")) // the carrier now has P2
	before := e.revision()
	uow := faulting(e.mem, "Proposal", 1)

	_, err := governance.AssessHead(context.Background(), uow, e.reader, e.writer, e.request(prA, "head-b", headTree("v3", "package main\n")))

	if !errors.Is(err, uow.err) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	e.requireWrites(before, 0)
	if e.revisions("P2") != 1 {
		t.Fatalf("P2 has %d revisions, want the failed assessment to add none", e.revisions("P2"))
	}
}

func TestPromoteMergeOfTheBootstrapCarrierSurfacesStoreFailuresAndWritesNothing(t *testing.T) {
	for name, failure := range map[string]fault{
		"latest bootstrap proposal":      {"LatestBootstrapProposal", 1},
		"proposal of the carrier":        {"Proposal", 1},
		"assessment of the merge commit": {"RecordAssessment", 1},
		"promotion":                      {"Suite", 2},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.proposeBootstrap()
			if _, err := governance.AssessBootstrapHead(context.Background(), w.mem, bootstrapHeadRequest("head-a")); err != nil {
				t.Fatal(err)
			}
			before := w.revision()
			uow := faulting(w.mem, failure.method, failure.nth)

			_, err := governance.PromoteMerge(context.Background(), uow, newFakeWriter(), validMergeRequest())

			if !errors.Is(err, uow.err) {
				t.Fatalf("error = %v, want the injected failure", err)
			}
			w.requireWrites(before, 0)
			if _, found, outcomeErr := governance.MergeOutcomeOf(context.Background(), w.mem, projectID, suiteID, prA); outcomeErr != nil || found {
				t.Fatalf("a merge outcome outlived the failed merge: found %v, error %v", found, outcomeErr)
			}
		})
	}
}

func TestPromoteMergeSurfacesTheFailureToLoadTheProposalOfTheFirstVersion(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	w.approve(c)
	w.bootstrap(c.baselineRequest(t, "bootstrap-1", "V1"))
	before := w.revision()
	uow := faulting(w.mem, "Proposal", 1) // the first load of a merge into a Suite with a canonical is the proposal of V1

	_, err := governance.PromoteMerge(context.Background(), uow, newFakeWriter(), validMergeRequest())

	if !errors.Is(err, uow.err) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	w.requireWrites(before, 0)
}

// The promotion write fails after it is made (the merge outcome would follow it): the assessment of the merge commit and the promotion itself are rolled back
// with it, and the merge can be decided again.
func TestPromoteMergeRollsBackWhenThePromotionWriteFails(t *testing.T) {
	e := newHeadEnv(t)
	e.approve(e.other)
	tree := headTree("other", "package main\n")
	before := e.revision()
	uow := faulting(e.mem, "RecordPromotion", 1)

	_, err := governance.PromoteMerge(context.Background(), uow, e.writer, e.mergeRequest(e.other, tree))

	if !errors.Is(err, uow.err) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	e.requireWrites(before, 0)
	if _, found, outcomeErr := governance.MergeOutcomeOf(context.Background(), e.mem, projectID, suiteID, e.other.carrier); outcomeErr != nil || found {
		t.Fatalf("a merge outcome outlived the failed merge: found %v, error %v", found, outcomeErr)
	}
	if _, found := e.receipt("merge:" + string(e.other.carrier)); found {
		t.Fatal("a failed promotion left a receipt")
	}
	if _, found := e.assessment(e.other.current(), "merge-sha-px"); found {
		t.Fatal("the assessment of the merge commit outlived the failed promotion")
	}
	if e.currentVersion() != e.first {
		t.Fatalf("current version = %q, want %q kept", e.currentVersion(), e.first)
	}
	if retry := e.merge(e.other, tree, governance.MergePromoted); retry.Duplicate {
		t.Fatalf("retry = %+v, want a fresh promotion", retry)
	}
}

// withoutVersion is a unit of work whose history lacks the version id, though the Suite's canonical names it.
type withoutVersion struct {
	governance.UnitOfWork
	id contract.SuiteVersionID
}

func (u withoutVersion) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return u.UnitOfWork.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error { return fn(ctx, missingVersionTx{Tx: tx, id: u.id}) })
}

type missingVersionTx struct {
	governance.Tx
	id contract.SuiteVersionID
}

func (t missingVersionTx) Version(ctx context.Context, id contract.SuiteVersionID) (contract.HistoricalCanonical, bool, error) {
	if id == t.id {
		return contract.HistoricalCanonical{}, false, nil
	}
	return t.Tx.Version(ctx, id)
}

// A correction names the current version, which the history must hold: a missing one is a broken state, not a zero version
// for the decision to refuse.
func TestPromoteMergeOfACorrectionRefusesAMissingCorrectedVersion(t *testing.T) {
	second := newCandidate(t, "pr2", "V1", treeContract(t, headTree("v2", "package main\n")))
	fix := newCandidate(t, "fix", "V2", treeContract(t, headTree("v1", "package main\n")))
	e := newHeadEnv(t, second, fix)
	e.approve(second)
	e.approve(fix)
	e.merge(second, headTree("v2", "package main\n"), governance.MergePromoted)
	before := e.revision()

	_, err := governance.PromoteMerge(context.Background(), withoutVersion{e.mem, "V2"}, e.writer, e.mergeRequest(fix, headTree("v1", "package main\n")))

	if !errors.Is(err, governance.ErrInvalidState) {
		t.Fatalf("error = %v, want ErrInvalidState for a corrected version the history lacks", err)
	}
	e.requireWrites(before, 0)
}
