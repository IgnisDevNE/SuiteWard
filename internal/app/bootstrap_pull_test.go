package app

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// pullProposal proposes the bootstrap of pull request pull from its head, as the checks job does: P<n>-R1 of origin kind pull.
func (w *bootWorld) pullProposal(pull int, head string, files map[string]string) governance.BootstrapHeadResult {
	w.t.Helper()
	if !w.suiteExists() {
		w.seedSuite(bootRepoID, "branch:main")
	}
	result, err := governance.AssessBootstrapHead(w.t.Context(), w.suites, w.content, governance.BootstrapHeadRequest{Project: w.project(), Suite: defaultSuite,
		Carrier: carrierID(bootRepoID, pull), Head: contract.SourceRevision(head), Tree: &bootTree{files: files},
		Summary: func(r contract.ProposalReference, inv inventory.Inventory) (governance.OutboxMessage, error) {
			proposal, revision, _ := parseReference(referenceText(r))
			inputs := inventoryInputs(bootstrapSummaryInputs{Proposal: proposal, Revision: revision, Source: sourcePull, Origin: head, Tier: "isolated"}, inv)
			return newSummaryMessage(commentTarget{RepositoryID: bootRepoID, Repo: bootRepo, Pull: pull}, inputs)
		}})
	if err != nil || result.Reference == nil {
		w.t.Fatalf("propose the bootstrap of pull %d: %+v, %v", pull, result, err)
	}
	return result
}

func TestBootstrapNeverPromotesAnApprovedPullRevision(t *testing.T) {
	w := newBootWorld(t)
	w.reader.repos[bootRepo].trees[bootHead] = &bootTree{files: map[string]string{"src/main.go": "package main\n"}} // the default branch has no protected files
	w.reader.repos[bootRepo].trees[bootMovedHead] = &bootTree{files: bootFiles("v1")} // the head of the pull request
	w.openPulls(bootRepo, 5)
	w.pullProposal(5, bootMovedHead, bootFiles("v1"))
	w.approveIn(bootRepoID, 101, 1, 5)

	w.mustRun()
	w.mustRun()

	if version, found := w.canonical(); found {
		t.Fatalf("canonical = %q, want none: a pull head becomes canonical only by its merge", version)
	}
	if _, found := w.proposal(2); found {
		t.Fatal("a second proposal was written although the default branch has no protected files")
	}
	w.requireProblem("awaiting-initial-contract")
}

func TestBootstrapSupersedesAPullProposalOnceTheDefaultBranchHasProtectedFiles(t *testing.T) {
	for name, approved := range map[string]bool{"unapproved": false, "approved": true} {
		t.Run(name, func(t *testing.T) {
			w := newBootWorld(t)
			w.openPulls(bootRepo, 5, 6) // the default branch (bootHead) protects tests/**
			w.pullProposal(5, bootMovedHead, bootFiles("v2"))
			if approved {
				w.approveIn(bootRepoID, 101, 1, 5)
			}

			w.mustRun()
			w.mustRun()

			proposal, found := w.proposal(2)
			if !found || proposal.Current().Carrier() != carrierID(bootRepoID, 5) || proposal.Current().OriginKind() != contract.OriginBranch ||
				proposal.Current().Origin() != bootHead || proposal.Current().Binding().Reference().RevisionID != "R1" {
				t.Fatalf("P2 = %+v (found %v), want a branch R1 from the default branch on the lowest open pull", proposal, found)
			}
			if first, _ := w.proposal(1); first.Current().OriginKind() != contract.OriginPull {
				t.Fatalf("P1 has the kind %q, want the pull proposal kept", first.Current().OriginKind())
			}
			if _, found := w.proposal(3); found {
				t.Fatal("P3 was proposed: the branch proposal is current")
			}
			if version, found := w.canonical(); found {
				t.Fatalf("canonical = %q, want none: the branch proposal is unapproved and the pull one is never promoted", version)
			}
			w.requireOutbox("bootstrap-summary:1001:P1-R1", "bootstrap-summary:1001:P2-R1")
			w.requireProblem("")
		})
	}
}

func TestBootstrapPromotesTheBranchBaselineThatSupersededAPullProposal(t *testing.T) {
	w := newBootWorld(t)
	w.openPulls(bootRepo, 5)
	w.pullProposal(5, bootMovedHead, bootFiles("v2"))
	w.mustRun() // proposes P2 from the default branch
	w.approveIn(bootRepoID, 102, 2, 5)

	w.mustRun()

	if version, found := w.canonical(); !found || version != "V1" {
		t.Fatalf("canonical = %q (found %v), want V1 from the approved branch baseline\nlogs: %s", version, found, w.logs)
	}
}
