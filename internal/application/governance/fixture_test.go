package governance_test

import (
	"context"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance/governancetest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	projectID = contract.ProjectID("project")
	suiteID   = contract.SuiteID("suite")
	policyID  = contract.PolicyRevisionID("policy-1")
	target    = contract.IntegrationTargetID("default")
)

var recordedAt = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// must unwraps a fixture value; a failed fixture panics and fails the test.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func protectedOf(t *testing.T, content string) contract.ProtectedContract {
	t.Helper()
	manifest := must(artifact.NewManifest([]artifact.Entry{{Path: "tests/contract.txt", Content: artifact.Hash([]byte(content))}}))
	return must(contract.NewProtectedContract(manifest, artifact.Hash([]byte("tests/**")), map[string]string{"runner": "v1"}))
}

// candidate is a proposal against a baseline canonical, with passing
// integrity assessments for its candidate source and its merged source.
type candidate struct {
	id          string
	protected   contract.ProtectedContract
	proposal    contract.Proposal
	carrier     contract.ApprovalCarrierID
	origin      contract.SourceRevision
	merged      contract.SourceRevision
	assessments []contract.IntegrityAssessment
}

// newCandidate builds one proposal; the last revision is the current one.
func newCandidate(t *testing.T, id string, baseline contract.SuiteVersionID, protected contract.ProtectedContract, revisions ...contract.ProposalRevisionID) candidate {
	t.Helper()
	if len(revisions) == 0 {
		revisions = []contract.ProposalRevisionID{"revision-1"}
	}
	c := candidate{id: id, protected: protected, carrier: contract.ApprovalCarrierID("carrier-" + id),
		origin: contract.SourceRevision("candidate-" + id), merged: contract.SourceRevision("merged-" + id)}
	for _, revisionID := range revisions {
		binding := must(contract.NewApprovalBinding(contract.BindingInput{Reference: c.reference(revisionID), ExpectedCanonical: baseline,
			Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: policyID, CoveredInputs: protected.CoveredInputs()}))
		revision := must(contract.NewProposalRevision(binding, c.origin, c.carrier))
		if c.proposal.IsZero() {
			c.proposal = must(contract.NewProposal(revision))
		} else {
			c.proposal = must(c.proposal.Revise(revision))
		}
		for _, source := range []contract.SourceRevision{c.origin, c.merged} {
			evidence := must(contract.NewIntegrityEvidence("verifier", source, binding, contract.IntegrityPassed))
			c.assessments = append(c.assessments, must(contract.AssessIntegrity(source, binding, &evidence)))
		}
	}
	return c
}

func (c candidate) reference(revision contract.ProposalRevisionID) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: projectID, SuiteID: suiteID, ProposalID: contract.ProposalID(c.id), RevisionID: revision}
}

func (c candidate) current() contract.ProposalReference {
	return c.proposal.Current().Binding().Reference()
}

func (c candidate) mergedRequest(t *testing.T, operation string, version contract.SuiteVersionID) governance.PromoteRequest {
	t.Helper()
	integration := must(contract.NewIntegration(projectID, target, c.merged, c.carrier, contract.IntegrationMergedChange))
	return governance.PromoteRequest{OperationID: contract.OperationID(operation), Reference: c.current(), Carrier: c.carrier, Proposed: c.protected,
		AssessmentSource: c.merged, Integration: integration, NewVersionID: version, RecordedAt: recordedAt}
}

func (c candidate) baselineRequest(t *testing.T, operation string, version contract.SuiteVersionID) governance.PromoteRequest {
	t.Helper()
	integration := must(contract.NewIntegration(projectID, target, c.origin, "", contract.IntegrationExistingBaseline))
	return governance.PromoteRequest{OperationID: contract.OperationID(operation), Reference: c.current(), Carrier: c.carrier, Proposed: c.protected,
		AssessmentSource: c.origin, Integration: integration, NewVersionID: version, RecordedAt: recordedAt}
}

type world struct {
	t     *testing.T
	mem   *governancetest.Memory
	owner contract.Principal
}

// newWorld seeds a Suite without a canonical version, owned by one human,
// with every candidate admitted to the schedule in order.
func newWorld(t *testing.T, candidates ...candidate) *world {
	t.Helper()
	owner := must(contract.NewPrincipal("owner", contract.Human))
	policy := must(contract.NewPolicy(projectID, policyID, owner))
	suite := must(contract.NewSuite(projectID, suiteID, "", 7))
	canonical := must(contract.NewCanonicalSnapshot(suite, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	schedule := must(contract.NewSchedule(projectID, suiteID))
	seed := governance.Seed{}
	for _, c := range candidates {
		schedule = must(schedule.Admit(c.proposal, true))
		seed.Proposals = append(seed.Proposals, governance.SeedProposal{Proposal: c.proposal, Assessments: c.assessments})
	}
	seed.Suite = governance.SuiteState{Canonical: canonical, Policy: policy, Target: target, Schedule: schedule}
	mem := governancetest.NewMemory()
	if err := mem.Seed(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	return &world{t: t, mem: mem, owner: owner}
}

func (w *world) command(c candidate, revision contract.ProposalRevisionID, operation, source string, action contract.ConsentAction, order contract.CommandOrder) contract.Command {
	w.t.Helper()
	return w.commandBy(w.owner, c, revision, operation, source, action, order)
}

func (w *world) commandBy(actor contract.Principal, c candidate, revision contract.ProposalRevisionID, operation, source string, action contract.ConsentAction, order contract.CommandOrder) contract.Command {
	w.t.Helper()
	return must(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID(operation), SourceCommandID: contract.SourceCommandID(source),
		Actor: actor, Reference: c.reference(revision), Carrier: c.carrier, Action: action, Order: order}))
}

func (w *world) consent(command contract.Command) governance.ConsentResponse {
	w.t.Helper()
	response, err := governance.ProcessConsent(context.Background(), w.mem, governance.ConsentRequest{Command: command})
	if err != nil {
		w.t.Fatalf("process consent: %v", err)
	}
	return response
}

// approve records the owner's approval of the candidate's current revision.
func (w *world) approve(c candidate) {
	w.t.Helper()
	response := w.consent(w.command(c, c.current().RevisionID, "approve-"+c.id, "comment-"+c.id, contract.ApproveConsent, 1))
	if !response.Receipt.CurrentApprovalEligible {
		w.t.Fatalf("approval of %s is not eligible: %v", c.id, response.Receipt.Result.Reason())
	}
}

func (w *world) promote(request governance.PromoteRequest) governance.PromoteResult {
	w.t.Helper()
	result, err := governance.Promote(context.Background(), w.mem, request)
	if err != nil {
		w.t.Fatalf("promote: %v", err)
	}
	return result
}

func (w *world) bootstrap(request governance.PromoteRequest) governance.PromoteResult {
	w.t.Helper()
	result, err := governance.Bootstrap(context.Background(), w.mem, request)
	if err != nil {
		w.t.Fatalf("bootstrap: %v", err)
	}
	return result
}

func (w *world) correct(request governance.CorrectionRequest) governance.PromoteResult {
	w.t.Helper()
	result, err := governance.Correct(context.Background(), w.mem, request)
	if err != nil {
		w.t.Fatalf("correct: %v", err)
	}
	return result
}

// read runs fn in a unit of work that writes nothing.
func (w *world) read(fn func(context.Context, governance.Tx) error) {
	w.t.Helper()
	if err := w.mem.Do(context.Background(), projectID, suiteID, fn); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) state() governance.SuiteState {
	w.t.Helper()
	var state governance.SuiteState
	w.read(func(ctx context.Context, tx governance.Tx) (err error) {
		state, err = tx.Suite(ctx)
		return err
	})
	return state
}

func (w *world) revision() contract.StateRevision { return w.state().Canonical.Suite().Revision() }

func (w *world) currentVersion() contract.SuiteVersionID {
	id, _ := w.state().Canonical.Suite().CurrentVersionID()
	return id
}

func (w *world) receipt(operation string) (governance.OperationReceipt, bool) {
	w.t.Helper()
	var receipt governance.OperationReceipt
	var found bool
	w.read(func(ctx context.Context, tx governance.Tx) (err error) {
		receipt, found, err = tx.Receipt(ctx, contract.OperationID(operation))
		return err
	})
	return receipt, found
}

func (w *world) version(id contract.SuiteVersionID) (contract.HistoricalCanonical, bool) {
	w.t.Helper()
	var history contract.HistoricalCanonical
	var found bool
	w.read(func(ctx context.Context, tx governance.Tx) (err error) {
		history, found, err = tx.Version(ctx, id)
		return err
	})
	return history, found
}

// requireWrites fails unless the given number of writes advanced the revision.
func (w *world) requireWrites(before contract.StateRevision, writes int) {
	w.t.Helper()
	if got := w.revision(); got != before+contract.StateRevision(writes) {
		w.t.Fatalf("revision = %d, want %d", got, before+contract.StateRevision(writes))
	}
}
