package governancetest

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The cases of the M1.7b bootstrap from a PR: the baseline proposal of a carrier, baselines from a PR head, the
// assessment of a bootstrap PR's head, and the merge that decides V1.

func bootstrapCases() []struct {
	name string
	run  func(*cfEnv)
} {
	return []struct {
		name string
		run  func(*cfEnv)
	}{
		{"BaselineProposalForFindsThePullBootstrapOfACarrier", cfBaselineProposalForFindsThePullBootstrap},
		{"BaselineProposalForSkipsAPromotedProposal", cfBaselineProposalForSkipsAPromotedProposal},
		{"BaselineProposalForRejectsTwoBootstrapProposalsOfOneCarrier", cfBaselineProposalForRejectsTwo},
		{"ProposeBaselineFromAPullHeadTakesLaterRevisions", cfProposeBaselineFromAPullHead},
		{"ProposeBaselineNeedsAnOriginKind", cfProposeBaselineNeedsAnOriginKind},
	}
}

func (e *cfEnv) requireBaselineProposal(carrier contract.ApprovalCarrierID, want contract.ProposalID) {
	e.t.Helper()
	var id contract.ProposalID
	var found bool
	err := e.do(func(ctx context.Context, tx governance.Tx) (err error) {
		id, found, err = tx.BaselineProposalFor(ctx, carrier)
		return err
	})
	if err != nil || id != want || found != (want != "") {
		e.t.Fatalf("BaselineProposalFor(%q) = %q, %v, %v; want %q", carrier, id, found, err, want)
	}
}

// proposeBaseline runs ProposeBaseline for the tree of content; the stores were given its blobs.
func (e *cfEnv) proposeBaseline(reference contract.ProposalReference, carrier contract.ApprovalCarrierID, origin contract.SourceRevision, kind contract.OriginKind, content string) (governance.BaselineResult, error) {
	return governance.ProposeBaseline(context.Background(), e.store, mcDiscard{}, governance.BaselineRequest{Reference: reference, Carrier: carrier, Origin: origin,
		OriginKind: kind, Tree: mcTreeOf(content)})
}

func cfBaselineProposalForFindsThePullBootstrap(e *cfEnv) {
	branch := e.candidate("p0", "", "v1") // a baseline from a pinned branch commit
	pull := e.candidate("p1", "", "v2").asPull()
	change := e.candidate("p2", "version-1", "v3")
	e.seedSimple(cfProject, cfSuite, branch, pull, change)

	e.requireBaselineProposal(pull.carrier, "p1")
	e.requireBaselineProposal(branch.carrier, "")
	e.requireBaselineProposal(change.carrier, "")
	e.requireBaselineProposal("carrier-unknown", "")
}

func cfBaselineProposalForSkipsAPromotedProposal(e *cfEnv) {
	open := mcCandidate("boot", "", "v2").asPull()
	e.mcSeedWith([]contract.SuiteVersionID{"V1"}, true, open) // p0 became V1 by its merge

	e.requireBaselineProposal("carrier-p0", "")
	e.requireBaselineProposal(open.carrier, "boot")
}

func cfBaselineProposalForRejectsTwo(e *cfEnv) {
	first, second := e.candidate("p1", "", "v1").asPull(), e.candidate("p2", "", "v2").asPull()
	e.seedSimple(cfProject, cfSuite, first)
	sameCarrier := cfMust(contract.NewProposalRevision(second.proposal.Current().Binding(), second.origin, contract.OriginPull, first.carrier))
	if err := e.appendRevision(sameCarrier); err != nil {
		e.t.Fatal(err)
	}

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		_, _, err := tx.BaselineProposalFor(ctx, first.carrier)
		return err
	})

	if !errors.Is(err, governance.ErrInvalidState) {
		e.t.Fatalf("BaselineProposalFor with two qualifying proposals = %v, want ErrInvalidState", err)
	}
}

func cfProposeBaselineFromAPullHead(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	const carrier = contract.ApprovalCarrierID("carrier-boot")
	reference := func(revision contract.ProposalRevisionID) contract.ProposalReference {
		return contract.ProposalReference{ProjectID: cfProject, SuiteID: cfSuite, ProposalID: "P1", RevisionID: revision}
	}

	first, err := e.proposeBaseline(reference("R1"), carrier, "head-1", contract.OriginPull, "v1")
	if err != nil || !first.Revise.Committed {
		e.t.Fatalf("R1 = %+v, %v; want a committed first revision", first.Revise, err)
	}
	second, err := e.proposeBaseline(reference("R2"), carrier, "head-2", contract.OriginPull, "v2")
	if err != nil || !second.Revise.Committed || second.Revise.Current != reference("R2") {
		e.t.Fatalf("R2 = %+v, %v; want a committed second revision of the same proposal", second.Revise, err)
	}

	e.read(func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, "P1")
		if err != nil {
			return err
		}
		revisions := proposal.Revisions()
		if len(revisions) != 2 || revisions[0].OriginKind() != contract.OriginPull || revisions[1].OriginKind() != contract.OriginPull ||
			revisions[1].Origin() != "head-2" || revisions[1].Binding().ExpectedCanonical() != "" {
			e.t.Fatalf("revisions = %d, kinds %v and %v, origin %q; want two pull revisions that expect no canonical, the second from head-2",
				len(revisions), revisions[0].OriginKind(), revisions[len(revisions)-1].OriginKind(), revisions[len(revisions)-1].Origin())
		}
		return nil
	})
	e.requireBaselineProposal(carrier, "P1")
}

func cfProposeBaselineNeedsAnOriginKind(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	before := e.revision()

	_, err := e.proposeBaseline(contract.ProposalReference{ProjectID: cfProject, SuiteID: cfSuite, ProposalID: "P1", RevisionID: "R1"}, "carrier-boot", "head-1", "", "v1")

	if !errors.Is(err, governance.ErrInvalidRequest) {
		e.t.Fatalf("error = %v, want ErrInvalidRequest for a baseline without an origin kind", err)
	}
	e.requireWrites(before, 0)
}
