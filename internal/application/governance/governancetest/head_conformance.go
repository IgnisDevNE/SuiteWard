package governancetest

import (
	"context"
	"errors"
	"fmt"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The cases of the M1.6b proposal lookups: ChangeProposalFor and NextProposalID.

func (e *cfEnv) changeProposalFor(carrier contract.ApprovalCarrierID) (id contract.ProposalID, found bool, err error) {
	err = e.do(func(ctx context.Context, tx governance.Tx) (err error) {
		id, found, err = tx.ChangeProposalFor(ctx, carrier)
		return err
	})
	return id, found, err
}

func (e *cfEnv) requireChangeProposal(carrier contract.ApprovalCarrierID, want contract.ProposalID) {
	e.t.Helper()
	id, found, err := e.changeProposalFor(carrier)
	if err != nil || id != want || found != (want != "") {
		e.t.Fatalf("ChangeProposalFor(%q) = %q, %v, %v; want %q", carrier, id, found, err, want)
	}
}

func cfChangeProposalForFindsTheOpenChangeOfACarrier(e *cfEnv) {
	r := e.seedRich()

	e.requireChangeProposal(r.open.carrier, "p2")
	e.requireChangeProposal(r.base.carrier, "")   // a promoted baseline
	e.requireChangeProposal(r.second.carrier, "") // a promoted change
	e.requireChangeProposal("carrier-unknown", "")
}

func cfChangeProposalForSkipsABaselineProposal(e *cfEnv) {
	baseline := e.candidate("p0", "", "v1")
	change := e.candidate("p1", "version-1", "v2")
	e.seedSimple(cfProject, cfSuite, baseline, change)

	e.requireChangeProposal(baseline.carrier, "")
	e.requireChangeProposal(change.carrier, "p1")
}

func cfChangeProposalForIgnoresAnotherSuite(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	otherChange := cfNewCandidate("project-b", "suite-b", "pc", "version-1", "v2")
	e.seedSimple("project-b", "suite-b", otherChange)

	e.requireChangeProposal(otherChange.carrier, "")
}

func cfChangeProposalForRejectsTwoChangeProposalsOfOneCarrier(e *cfEnv) {
	first := e.candidate("p1", "version-1", "v2")
	second := e.candidate("p2", "version-1", "v3")
	e.seedSimple(cfProject, cfSuite, first)
	sameCarrier := cfMust(contract.NewProposalRevision(second.proposal.Current().Binding(), second.origin, first.carrier))
	if err := e.appendRevision(sameCarrier); err != nil {
		e.t.Fatal(err)
	}

	_, _, err := e.changeProposalFor(first.carrier)

	if !errors.Is(err, governance.ErrInvalidState) {
		e.t.Fatalf("ChangeProposalFor with two qualifying proposals = %v, want ErrInvalidState", err)
	}
}

func (e *cfEnv) nextProposalID() (id contract.ProposalID, err error) {
	err = e.do(func(ctx context.Context, tx governance.Tx) (err error) {
		id, err = tx.NextProposalID(ctx)
		return err
	})
	return id, err
}

func cfNextProposalIDIsOnePastTheHighestNumber(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	if id, err := e.nextProposalID(); err != nil || id != "P1" {
		e.t.Fatalf("NextProposalID of an empty Suite = %q, %v; want P1", id, err)
	}
	// P9 against P10 pins that the number decides, not the string; other names are not P<number>.
	for _, name := range []string{"P2", "P9", "p20", "Pfoo", "P"} {
		if err := e.appendRevision(e.candidate(name, "version-1", "v2").proposal.Current()); err != nil {
			e.t.Fatal(err)
		}
	}
	id, err := e.nextProposalID()
	if err != nil || id != "P10" {
		e.t.Fatalf("NextProposalID after P2 and P9 = %q, %v; want P10", id, err)
	}

	if err := e.appendRevision(e.candidate(string(id), "version-1", "v2").proposal.Current()); err != nil {
		e.t.Fatal(err)
	}
	if id, err := e.nextProposalID(); err != nil || id != "P11" {
		e.t.Fatalf("NextProposalID after P10 = %q, %v; want P11 (10 is above 9)", id, err)
	}
}

func cfNextProposalIDSeesItsOwnTransactionAndOnlyItsSuite(e *cfEnv) {
	e.seedSimple(cfProject, cfSuite)
	otherSuite := cfNewCandidate("project-b", "suite-b", "P99", "", "v1")
	e.seedSimple("project-b", "suite-b", otherSuite)

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		first, err := tx.NextProposalID(ctx)
		if err != nil {
			return err
		}
		if err := tx.AppendProposalRevision(ctx, governance.ProposalWrite{Revision: e.candidate(string(first), "version-1", "v2").proposal.Current()}); err != nil {
			return err
		}
		second, err := tx.NextProposalID(ctx)
		if first != "P1" || second != "P2" {
			return fmt.Errorf("NextProposalID before and after a write = %q, %q; want P1 then P2 (P99 belongs to another Suite)", first, second)
		}
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}
