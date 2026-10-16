package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
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

func (t *faultTx) RecordAssessment(ctx context.Context, write governance.AssessmentWrite) error {
	if t.hits("RecordAssessment") {
		return t.failure()
	}
	return t.Tx.RecordAssessment(ctx, write)
}

func (t *faultTx) ChangeProposalFor(ctx context.Context, carrier contract.ApprovalCarrierID) (contract.ProposalID, bool, error) {
	if t.hits("ChangeProposalFor") {
		return "", false, t.failure()
	}
	return t.Tx.ChangeProposalFor(ctx, carrier)
}

func (t *faultTx) BaselineProposalFor(ctx context.Context, carrier contract.ApprovalCarrierID) (contract.ProposalID, bool, error) {
	if t.hits("BaselineProposalFor") {
		return "", false, t.failure()
	}
	return t.Tx.BaselineProposalFor(ctx, carrier)
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

func summaryOf(contract.ProposalReference, inventory.Inventory) (governance.OutboxMessage, error) {
	return governance.OutboxMessage{Key: "summary/bootstrap", Kind: "bootstrap.summary", Payload: []byte(`{}`)}, nil
}

func bootstrapHeadRequest(head string) governance.BootstrapHeadRequest {
	return governance.BootstrapHeadRequest{Project: projectID, Suite: suiteID, Carrier: prA, Head: contract.SourceRevision(head),
		Tree: headTree("v1", "package main\n"), Summary: summaryOf}
}

func TestAssessBootstrapHeadSurfacesStoreFailuresAndWritesNothing(t *testing.T) {
	for name, failure := range map[string]fault{
		"suite":                             {"Suite", 1},
		"bootstrap proposal of the carrier": {"BaselineProposalFor", 1},
		"proposal id":                       {"NextProposalID", 1},
		"proposal of the revision":          {"Proposal", 1},
		"proposal after the revision":       {"Proposal", 2},
		"assessment write":                  {"RecordAssessment", 1},
		"assessment read":                   {"Assessment", 1},
		"suite after the writes":            {"Suite", 2},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			before := w.revision()
			uow := faulting(w.mem, failure.method, failure.nth)

			_, err := governance.AssessBootstrapHead(context.Background(), uow, newFakeWriter(), bootstrapHeadRequest("head-a"))

			if !errors.Is(err, uow.err) {
				t.Fatalf("error = %v, want the injected failure", err)
			}
			w.requireWrites(before, 0)
			if w.hasProposal("P1") {
				t.Fatal("a proposal outlived the failed assessment")
			}
		})
	}
}

func TestAssessBootstrapHeadSurfacesTheFailureToLoadTheExistingProposal(t *testing.T) {
	w := newWorld(t)
	first, err := governance.AssessBootstrapHead(context.Background(), w.mem, newFakeWriter(), bootstrapHeadRequest("head-a"))
	if err != nil || !first.Committed {
		t.Fatalf("first assessment = %+v, error %v, want a committed proposal", first, err)
	}
	before := w.revision()
	uow := faulting(w.mem, "Proposal", 1) // the carrier now has a proposal, so the first load is the existing one

	_, err = governance.AssessBootstrapHead(context.Background(), uow, newFakeWriter(), bootstrapHeadRequest("head-b"))

	if !errors.Is(err, uow.err) {
		t.Fatalf("error = %v, want the injected failure", err)
	}
	w.requireWrites(before, 0)
}

func TestAssessBootstrapHeadNeedsTheAssessmentItJustRecorded(t *testing.T) {
	w := newWorld(t)
	before := w.revision()
	uow := faulting(w.mem, "Assessment", 1)
	uow.vanish = true

	_, err := governance.AssessBootstrapHead(context.Background(), uow, newFakeWriter(), bootstrapHeadRequest("head-a"))

	if !errors.Is(err, governance.ErrInvalidState) {
		t.Fatalf("error = %v, want invalid state for an assessment that vanished", err)
	}
	w.requireWrites(before, 0)
}

func TestAssessBootstrapHeadWritesNothingWhenContentCannotBeStored(t *testing.T) {
	w := newWorld(t)
	before := w.revision()
	writer := newFakeWriter()
	writer.fail = errors.New("disk full")

	_, err := governance.AssessBootstrapHead(context.Background(), w.mem, writer, bootstrapHeadRequest("head-a"))

	if !errors.Is(err, writer.fail) {
		t.Fatalf("error = %v, want the store failure", err)
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
		"bootstrap proposal of the carrier": {"BaselineProposalFor", 1},
		"proposal of the carrier":           {"Proposal", 1},
		"assessment of the merge commit":    {"RecordAssessment", 1},
		"promotion":                         {"Suite", 2},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			if _, err := governance.AssessBootstrapHead(context.Background(), w.mem, newFakeWriter(), bootstrapHeadRequest("head-a")); err != nil {
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
