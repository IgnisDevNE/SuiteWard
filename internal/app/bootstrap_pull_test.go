package app

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
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
	w.reader.repos[bootRepo].trees[bootMovedHead] = &bootTree{files: bootFiles("v1")}                               // the head of the pull request
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

const bootMergeSHA = "5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e"

// resolveCarrier records what GitHub said about the pull request that left the open list: merged as bootMergeSHA, or closed.
func (w *bootWorld) resolveCarrier(pull int, merged bool) {
	w.t.Helper()
	pulls, err := w.pulls.Pulls(w.t.Context(), bootRepo)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, state := range pulls {
		if state.Number != pull {
			continue
		}
		resolution := githubsync.Resolution{Number: pull, NotOpenSince: state.NotOpenSince, HeadSHA: state.HeadSHA, HeadRef: state.HeadRef, BaseRef: state.BaseRef,
			BaseSHA: state.BaseSHA, HeadRepo: bootRepo, ResolvedAt: bootTime.Add(time.Hour)}
		if merged {
			resolution.Merged, resolution.MergedAt, resolution.MergeCommitSHA = true, bootTime.Add(time.Hour), bootMergeSHA
		}
		if err := w.pulls.RecordResolution(w.t.Context(), bootRepo, resolution); err != nil {
			w.t.Fatal(err)
		}
		return
	}
	w.t.Fatalf("pull %d was never observed", pull)
}

// decideMerge decides the merge of pull as the merges job does: from the merge commit tree, or with a refusal.
func (w *bootWorld) decideMerge(pull int, refusal governance.MergeReason) governance.MergeOutcome {
	w.t.Helper()
	request := governance.MergeRequest{Project: w.project(), Suite: defaultSuite, Carrier: carrierID(bootRepoID, pull), Source: bootMergeSHA, Target: "branch:main",
		ObservedAt: bootTime, RecordedAt: bootTime.Add(2 * time.Hour), Refusal: refusal,
		Message: func(governance.MergeOutcome) (governance.OutboxMessage, bool, error) {
			return governance.OutboxMessage{}, false, nil
		}}
	if refusal == "" {
		request.Tree = &bootTree{files: bootFiles("v1")}
	}
	result, err := governance.PromoteMerge(w.t.Context(), w.mem, w.content, request)
	if err != nil {
		w.t.Fatal(err)
	}
	return result.MergeOutcome
}

// mergedCarrierWorld is a repository whose default branch has protected files, whose pull request 5 carried the approved
// pull proposal P1 and whose pull request 6 is open.
func mergedCarrierWorld(t *testing.T) *bootWorld {
	t.Helper()
	w := newBootWorld(t)
	w.openPulls(bootRepo, 5, 6)
	w.pullProposal(5, bootMovedHead, bootFiles("v1"))
	w.approveIn(bootRepoID, 101, 1, 5)
	return w
}

func TestBootstrapWaitsForTheMergeDecisionOfAMergedPullCarrier(t *testing.T) {
	w := mergedCarrierWorld(t)
	w.openPulls(bootRepo, 6)
	w.resolveCarrier(5, true) // the merge put the protected files on the default branch

	w.mustRun()
	w.mustRun()

	if _, found := w.proposal(2); found {
		t.Fatal("P2 was proposed before the merges job decided the merge of the carrier of P1")
	}
	w.requireOutbox("bootstrap-summary:1001:P1-R1")
	w.requireProblem("awaiting-merge-decision")

	if outcome := w.decideMerge(5, ""); outcome.Kind != governance.MergePromoted || outcome.Version != "V1" {
		t.Fatalf("the merge outcome is %+v, want V1 promoted", outcome)
	}
	w.mustRun()

	if _, found := w.proposal(2); found {
		t.Fatal("P2 was proposed after the merge promoted V1")
	}
	if version, found := w.canonical(); !found || version != "V1" {
		t.Fatalf("canonical = %q (found %v), want V1", version, found)
	}
	w.requireOutbox("bootstrap-summary:1001:P1-R1")
	w.requireProblem("")
}

func TestBootstrapWaitsWhileTheCarrierResolutionIsStillUnknown(t *testing.T) {
	w := mergedCarrierWorld(t)
	w.openPulls(bootRepo, 6) // not resolved yet: it may have been merged

	w.mustRun()

	if _, found := w.proposal(2); found {
		t.Fatal("P2 was proposed while it is unknown whether the carrier merged")
	}
	w.requireProblem("awaiting-merge-decision")
}

func TestBootstrapSupersedesAPullProposalWhoseCarrierClosedUnmerged(t *testing.T) {
	w := mergedCarrierWorld(t)
	w.openPulls(bootRepo, 6)
	w.resolveCarrier(5, false) // the protected files on the default branch came from elsewhere

	w.mustRun()
	w.mustRun()

	proposal, found := w.proposal(2)
	if !found || proposal.Current().Carrier() != carrierID(bootRepoID, 6) || proposal.Current().OriginKind() != contract.OriginBranch {
		t.Fatalf("P2 = %+v (found %v), want a branch baseline on pull 6\nlogs: %s", proposal, found, w.logs)
	}
	w.requireOutbox("bootstrap-summary:1001:P1-R1", "bootstrap-summary:1001:P2-R1")
	w.requireProblem("")
}

func TestBootstrapSupersedesAPullProposalWhoseMergeWasNotPromoted(t *testing.T) {
	w := mergedCarrierWorld(t)
	w.openPulls(bootRepo, 6)
	w.resolveCarrier(5, true)
	if outcome := w.decideMerge(5, governance.MergeReasonIntegrationUnreadable); outcome.Kind != governance.MergeBlocked {
		t.Fatalf("the merge outcome is %+v, want blocked", outcome)
	}

	w.mustRun()

	if proposal, found := w.proposal(2); !found || proposal.Current().Carrier() != carrierID(bootRepoID, 6) || proposal.Current().OriginKind() != contract.OriginBranch {
		t.Fatalf("P2 = %+v (found %v), want a branch baseline on pull 6 after a merge that did not promote\nlogs: %s", proposal, found, w.logs)
	}
	w.requireProblem("")
}
