package governance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// bindingInput is a revision's binding input against a baseline, bound to the governing policy.
func (c candidate) bindingInput(revision contract.ProposalRevisionID, baseline contract.SuiteVersionID, protected contract.ProtectedContract) contract.BindingInput {
	return contract.BindingInput{Reference: c.reference(revision), ExpectedCanonical: baseline, Manifest: protected.Manifest().Digest(),
		Scope: protected.ScopeDigest(), PolicyRevision: policyID, CoveredInputs: protected.CoveredInputs()}
}

func sealRevision(input contract.BindingInput, origin contract.SourceRevision, carrier contract.ApprovalCarrierID) contract.ProposalRevision {
	return must(contract.NewProposalRevision(must(contract.NewApprovalBinding(input)), origin, carrier))
}

func (w *world) revise(revision contract.ProposalRevision) governance.ReviseResult {
	w.t.Helper()
	result, err := governance.ReviseProposal(context.Background(), w.mem, governance.ReviseRequest{Revision: revision})
	if err != nil {
		w.t.Fatalf("revise proposal: %v", err)
	}
	return result
}

func (w *world) proposal(id contract.ProposalID) contract.Proposal {
	w.t.Helper()
	var proposal contract.Proposal
	w.read(func(ctx context.Context, tx governance.Tx) (err error) {
		proposal, _, err = tx.Proposal(ctx, id)
		return err
	})
	return proposal
}

func TestReviseProposalCreatesAProposalWithItsFirstRevision(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1")) // fixture only; not seeded
	w := newWorld(t)
	before := w.revision()

	result := w.revise(c.proposal.Current())

	if !result.Committed || result.Current != c.current() {
		t.Fatalf("result = %+v, want a committed revision %v", result, c.current())
	}
	w.requireWrites(before, 1)
	if got := w.proposal("p1").Current(); got.Binding().Reference() != c.current() || got.Carrier() != c.carrier || got.Origin() != c.origin {
		t.Fatalf("stored current revision = %+v, want the written one", got)
	}
}

func TestReviseProposalAppendsARevisionWhenCoverageChanges(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	before := w.revision()
	next := sealRevision(c.bindingInput("revision-2", "", protectedOf(t, "v1 changed")), "candidate-2", c.carrier)

	result := w.revise(next)

	if !result.Committed || result.Current != c.reference("revision-2") {
		t.Fatalf("result = %+v, want a committed revision-2", result)
	}
	w.requireWrites(before, 1)
	proposal := w.proposal("p1")
	if proposal.Current().Binding().Reference() != c.reference("revision-2") {
		t.Fatalf("current revision = %v, want revision-2", proposal.Current().Binding().Reference())
	}
	if _, err := proposal.Lookup(c.reference("revision-1"), c.carrier); err != nil {
		t.Fatalf("the older revision is no longer resolvable: %v", err)
	}
}

func TestReviseProposalWithUnchangedCoverageWritesNothing(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	before := w.revision()
	// An implementation-only push: a new origin and revision id over the same coverage.
	next := sealRevision(c.bindingInput("revision-2", "", c.protected), "candidate-pushed-later", c.carrier)

	result := w.revise(next)

	if result.Committed || result.Current != c.current() {
		t.Fatalf("result = %+v, want the existing current reference %v and no write", result, c.current())
	}
	w.requireWrites(before, 0)
	if got := w.proposal("p1").Revisions(); len(got) != 1 {
		t.Fatalf("stored revisions = %d, want 1", len(got))
	}
}

func TestReviseProposalRejectsTheSameCoverageUnderAnotherCarrier(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	before := w.revision()
	next := sealRevision(c.bindingInput("revision-2", "", c.protected), c.origin, "another-carrier")

	_, err := governance.ReviseProposal(context.Background(), w.mem, governance.ReviseRequest{Revision: next})

	if !errors.Is(err, contract.ErrProposalContextMismatch) {
		t.Fatalf("error = %v, want a proposal context mismatch", err)
	}
	w.requireWrites(before, 0)
}

func TestReviseProposalRejectsAReusedRevisionIDWithOtherCoverage(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	before := w.revision()
	next := sealRevision(c.bindingInput("revision-1", "", protectedOf(t, "v1 changed")), c.origin, c.carrier)

	_, err := governance.ReviseProposal(context.Background(), w.mem, governance.ReviseRequest{Revision: next})

	if !errors.Is(err, contract.ErrRevisionExists) {
		t.Fatalf("error = %v, want the revision to exist already", err)
	}
	w.requireWrites(before, 0)
}

// A revision is bound to the state it would be approved against: the governing
// policy, the current canonical version, and the locked Suite.
func TestReviseProposalMustBeBoundToTheGoverningState(t *testing.T) {
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	w := newWorld(t, c)
	before := w.revision()
	stale := c.bindingInput("revision-2", "", protectedOf(t, "v2"))

	policy := stale
	policy.PolicyRevision = "policy-0"
	canonical := stale
	canonical.ExpectedCanonical = "version-9"
	abroad := stale
	abroad.Reference.SuiteID = "suite-2"
	other := stale
	other.Reference.ProjectID = "project-2"

	for name, input := range map[string]contract.BindingInput{"policy revision": policy, "expected canonical": canonical} {
		t.Run(name, func(t *testing.T) {
			_, err := governance.ReviseProposal(context.Background(), w.mem, governance.ReviseRequest{Revision: sealRevision(input, c.origin, c.carrier)})
			if !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("error = %v, want an invalid request", err)
			}
		})
	}
	// The unit of work locks the default Suite whatever the request names, so a
	// revision for another Suite must be refused by the use case itself.
	for name, input := range map[string]contract.BindingInput{"suite": abroad, "project": other} {
		t.Run(name, func(t *testing.T) {
			_, err := governance.ReviseProposal(context.Background(), lockedTo{w.mem}, governance.ReviseRequest{Revision: sealRevision(input, c.origin, c.carrier)})
			if !errors.Is(err, governance.ErrInvalidRequest) {
				t.Fatalf("error = %v, want an invalid request", err)
			}
		})
	}
	w.requireWrites(before, 0)
	if got := w.proposal("p1").Revisions(); len(got) != 1 {
		t.Fatalf("stored revisions = %d, want 1", len(got))
	}
}

// lockedTo runs every unit of work in the default Suite.
type lockedTo struct{ governance.UnitOfWork }

func (l lockedTo) Do(ctx context.Context, _ contract.ProjectID, _ contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return l.UnitOfWork.Do(ctx, projectID, suiteID, fn)
}

func TestReviseProposalRejectsMalformedRequests(t *testing.T) {
	w := newWorld(t)
	if _, err := governance.ReviseProposal(context.Background(), w.mem, governance.ReviseRequest{}); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Errorf("empty request: error = %v, want an invalid request", err)
	}
	c := newCandidate(t, "p1", "", protectedOf(t, "v1"))
	if _, err := governance.ReviseProposal(context.Background(), nil, governance.ReviseRequest{Revision: c.proposal.Current()}); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Errorf("no unit of work: error = %v, want an invalid request", err)
	}
}

func TestReviseProposalOfAnUnknownSuiteIsNotFound(t *testing.T) {
	c := newCandidateIn(t, projectID, "suite-9", "p1", "", protectedOf(t, "v1"))
	w := newWorld(t)
	_, err := governance.ReviseProposal(context.Background(), w.mem, governance.ReviseRequest{Revision: c.proposal.Current()})
	if !errors.Is(err, governance.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
}
