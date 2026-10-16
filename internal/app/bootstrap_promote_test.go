package app

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The bootstrap job only promotes (docs/contracts/runtime.md, "github_bootstrap (promote only)"): every proposal is written by
// the commands job, and the newest bootstrap proposal decides what it may do.

func TestBootstrapProposesNothingForASuiteWithNoProposal(t *testing.T) {
	w := newBootWorld(t) // the default branch protects tests/**, and pull requests are open
	w.openPulls(bootRepo, 4, 5)

	w.mustRun()
	w.mustRun()

	if !w.suiteExists() {
		t.Fatal("the Suite was not created")
	}
	if _, found := w.proposal(1); found {
		t.Fatal("the bootstrap job proposed a baseline: only the commands job proposes")
	}
	w.requireOutbox()
	if w.reader.calls["head"] != 0 || w.reader.calls["tree"] != 0 {
		t.Fatalf("the job read the default branch %d times and a tree %d times, want none", w.reader.calls["head"], w.reader.calls["tree"])
	}
	w.requireProblem("")
}

func TestBootstrapDoesNothingForALatestPullProposal(t *testing.T) {
	for name, approved := range map[string]bool{"unapproved": false, "approved": true} {
		t.Run(name, func(t *testing.T) {
			w := newBootWorld(t) // the default branch protects tests/**: the old job would have superseded the pull proposal
			w.openPulls(bootRepo, 5, 6)
			w.pullProposal(5, bootMovedHead, bootFiles("v2"))
			if approved {
				w.approveIn(bootRepoID, 101, 1, 5)
			}

			w.mustRun()
			w.mustRun()

			if _, found := w.proposal(2); found {
				t.Fatal("a second proposal was written: the bootstrap job never proposes")
			}
			if version, found := w.canonical(); found {
				t.Fatalf("canonical = %q, want none: a pull proposal becomes canonical only by its merge", version)
			}
			if _, found := w.assessmentOutcome(1, bootMovedHead); found {
				t.Fatal("the bootstrap job assessed a pull proposal: the checks job does")
			}
			w.requireOutbox("bootstrap-summary:1001:P1-R1")
			if w.reader.calls["tree"] != 0 {
				t.Fatalf("%d tree reads, want none for a pull proposal", w.reader.calls["tree"])
			}
			w.requireProblem("")
		})
	}
}

func TestBootstrapNeverPromotesASupersededBranchProposal(t *testing.T) {
	w := newBootWorld(t)
	w.openPulls(bootRepo, 5, 6)
	w.branchProposal(5) // P1, approved by the owner
	w.approveIn(bootRepoID, 101, 1, 5)
	w.pullProposal(6, bootMovedHead, bootFiles("v2")) // P2: the newest proposal wins

	w.mustRun()
	w.mustRun()

	if version, found := w.canonical(); found {
		t.Fatalf("canonical = %q, want none: the approved branch proposal is superseded by the newer pull proposal", version)
	}
	if _, found := w.assessmentOutcome(1, bootHead); found {
		t.Fatal("the superseded branch proposal was assessed")
	}
	w.requireOutbox("bootstrap-summary:1001:P1-R1", "bootstrap-summary:1001:P2-R1")
	w.requireProblem("")
}

func TestBootstrapPromotesTheBranchProposalThatIsNewerThanAPullProposal(t *testing.T) {
	w := newBootWorld(t)
	w.openPulls(bootRepo, 5, 6)
	w.pullProposal(5, bootMovedHead, bootFiles("v2")) // P1
	w.branchProposal(6)                               // P2: the newest
	w.approveIn(bootRepoID, 102, 2, 6)

	w.mustRun()

	if version, found := w.canonical(); !found || version != "V1" {
		t.Fatalf("canonical = %q (found %v), want V1 from the newest, approved branch proposal\nlogs: %s", version, found, w.logs)
	}
	if proposal, _ := w.proposal(2); proposal.Current().OriginKind() != contract.OriginBranch {
		t.Fatalf("P2 has the origin kind %q", proposal.Current().OriginKind())
	}
}

func TestBootstrapDoesNotPromoteAnUnapprovedLatestBranchProposal(t *testing.T) {
	w := proposedWorld(t)
	w.openPulls(bootRepo) // the carrier closed: the job does not move the proposal to another pull request

	w.mustRun()

	if _, found := w.proposal(2); found {
		t.Fatal("a second proposal was written")
	}
	if _, found := w.canonical(); found {
		t.Fatal("a canonical exists without the owner's approval")
	}
	w.requireProblem("")
}
