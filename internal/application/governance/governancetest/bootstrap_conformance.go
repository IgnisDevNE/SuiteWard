package governancetest

import (
	"context"
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The cases of the M1.7b bootstrap from a PR: the latest bootstrap proposal and baselines from a PR head. The assessment of a
// bootstrap PR's head and the merge that decides V1 have their own files.

func bootstrapCases() []struct {
	name string
	run  func(*cfEnv)
} {
	return []struct {
		name string
		run  func(*cfEnv)
	}{
		{"LatestBootstrapProposalIsTheNewestOfEitherKind", cfLatestBootstrapNewest},
		{"LatestBootstrapProposalIsTheNewestPullOverAnOlderBranch", cfLatestBootstrapPullNewest},
		{"LatestBootstrapProposalOrdersByProposalNumber", cfLatestBootstrapNumeric},
		{"LatestBootstrapProposalSkipsAPromotedProposal", cfLatestBootstrapSkipsPromoted},
		{"LatestBootstrapProposalIsFalseWhenEveryProposalWasPromoted", cfLatestBootstrapOnlyPromoted},
		{"LatestBootstrapProposalNeverReturnsAChangeProposal", cfLatestBootstrapNoChange},
		{"LatestBootstrapProposalIsFalseWithoutOne", cfLatestBootstrapNone},
		{"ProposeBaselineTakesANewProposalWhileAnOlderOneIsOpen", cfProposeBaselineBesideAnOlderOne},
		{"ProposeBaselineFromAPullHeadTakesLaterRevisions", cfProposeBaselineFromAPullHead},
		{"ProposeBaselineNeedsAnOriginKind", cfProposeBaselineNeedsAnOriginKind},
	}
}

func (e *cfEnv) requireLatestBootstrap(want contract.ProposalID) {
	e.t.Helper()
	var id contract.ProposalID
	var found bool
	err := e.do(func(ctx context.Context, tx governance.Tx) (err error) {
		id, found, err = tx.LatestBootstrapProposal(ctx)
		return err
	})
	if err != nil || id != want || found != (want != "") {
		e.t.Fatalf("LatestBootstrapProposal = %q, %v, %v; want %q", id, found, err, want)
	}
}

// proposeBaseline runs ProposeBaseline for the tree of content; the stores were given its blobs.
func (e *cfEnv) proposeBaseline(reference contract.ProposalReference, carrier contract.ApprovalCarrierID, origin contract.SourceRevision, kind contract.OriginKind, content string) (governance.BaselineResult, error) {
	return governance.ProposeBaseline(context.Background(), e.store, mcDiscard{}, governance.BaselineRequest{Reference: reference, Carrier: carrier, Origin: origin,
		OriginKind: kind, Tree: mcTreeOf(content)})
}

// A newer branch proposal supersedes an older pull one.
func cfLatestBootstrapNewest(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("P1", "", "v1").asPull(), e.candidate("P2", "", "v2"))

	e.requireLatestBootstrap("P2")
}

// A newer pull proposal supersedes an older branch one.
func cfLatestBootstrapPullNewest(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("P1", "", "v1"), e.candidate("P2", "", "v2").asPull())

	e.requireLatestBootstrap("P2")
}

// The order is the proposal number, not the text: P10 is newer than P9.
func cfLatestBootstrapNumeric(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite, e.candidate("P9", "", "v1").asPull(), e.candidate("P10", "", "v2").asPull(), e.candidate("P2", "", "v3"))

	e.requireLatestBootstrap("P10")
}

func cfLatestBootstrapSkipsPromoted(e *cfEnv) {
	open := mcCandidate("P5", "", "v2").asPull()
	e.mcSeedWith([]contract.SuiteVersionID{"V1"}, true, open) // p0 became V1 by its merge

	e.requireLatestBootstrap("P5")
}

func cfLatestBootstrapOnlyPromoted(e *cfEnv) {
	e.mcSeedWith([]contract.SuiteVersionID{"V1"}, true)

	e.requireLatestBootstrap("")
}

func cfLatestBootstrapNoChange(e *cfEnv) {
	boot := e.candidate("P1", "", "v1").asPull()
	change := e.candidate("P2", "version-1", "v2") // newer, but it expects a canonical
	e.seedSimple(cfProject, cfSuite, boot, change)

	e.requireLatestBootstrap("P1")
}

func cfLatestBootstrapNone(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)

	e.requireLatestBootstrap("")
}

// R5a proposes P<n+1>-R1 for a new source while an older unpromoted proposal stays open, of either origin kind.
func cfProposeBaselineBesideAnOlderOne(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	for i, kind := range []contract.OriginKind{contract.OriginPull, contract.OriginBranch} {
		proposal := contract.ProposalID(fmt.Sprintf("P%d", i+1))
		reference := contract.ProposalReference{ProjectID: cfProject, SuiteID: cfSuite, ProposalID: proposal, RevisionID: "R1"}

		result, err := e.proposeBaseline(reference, contract.ApprovalCarrierID(fmt.Sprintf("carrier-%d", i+1)), contract.SourceRevision(fmt.Sprintf("head-%d", i+1)), kind, fmt.Sprintf("v%d", i+1))

		if err != nil || !result.Revise.Committed || result.Revise.Current != reference {
			e.t.Fatalf("%s-R1 of kind %s = %+v, %v; want a committed new proposal", proposal, kind, result.Revise, err)
		}
		e.requireLatestBootstrap(proposal)
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
	e.requireLatestBootstrap("P1")
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
