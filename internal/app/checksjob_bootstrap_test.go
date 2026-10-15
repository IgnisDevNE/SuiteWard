package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// newCJBootWorld is a repository whose Suite exists and has no canonical, and whose default branch has no protected
// files: the first canonical can only come from a pull request that carries the bootstrap.
func newCJBootWorld(t *testing.T) *cjJobWorld {
	t.Helper()
	w := newCJWorld(t, false)
	w.reader.repos[bootRepo].trees[bootHead] = &bootTree{files: map[string]string{"src/main.go": "package main\n"}}
	w.seedSuite(bootRepoID, "branch:main")
	return w
}

// summaryOf returns the inputs of the bootstrap-summary message written under key.
func (w *cjJobWorld) summaryOf(key string) map[string]any {
	w.t.Helper()
	return w.payload(key)["summary"].(map[string]any)
}

// carrierPull opens pull 5 at head 2 (protected files, v1) and pull 4 at head 3 (none), and checks them.
func (w *cjJobWorld) carrierPull() {
	w.t.Helper()
	w.tree(cjHead2, bootFiles("v1"))
	w.tree(cjHead3, map[string]string{"src/main.go": "package main // 4\n"})
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})
	w.mustCheck()
}

func TestChecksReadNothingAndSayBootstrapPendingWhileTheSuiteDoesNotExist(t *testing.T) {
	w := newCJWorld(t, false)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()

	w.want(4, checks.InProgress, "bootstrap-pending", 1)
	w.want(5, checks.InProgress, "bootstrap-pending", 1)
	if w.reader.calls["head"] != 0 || len(w.reader.reads) != 0 {
		t.Fatalf("GitHub reads %v and tree reads %v, want none before the Suite exists", w.reader.calls, w.reader.reads)
	}
}

func TestChecksSayBootstrapPendingOnEveryPullWhileTheDefaultBranchHasProtectedFilesAndReadItOncePerHead(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(bootHead, bootFiles("v1")) // the default branch protects tests/**
	w.tree(cjHead2, bootFiles("v1"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})

	w.mustCheck()
	w.mustCheck()

	w.want(4, checks.InProgress, "bootstrap-pending", 1)
	w.want(5, checks.InProgress, "bootstrap-pending", 1)
	if _, found := w.proposal(1); found {
		t.Fatal("a pull proposal was written although the default branch has protected files")
	}
	if w.reader.reads[bootHead] != 1 || w.reader.calls["head"] != 2 {
		t.Fatalf("%d tree reads of the default branch and %d head reads over two runs, want its tree read once and its head each run", w.reader.reads[bootHead], w.reader.calls["head"])
	}
	if w.reader.reads[cjHead2] != 0 {
		t.Fatalf("the head of a pull request was read %d times while the branch path is pending", w.reader.reads[cjHead2])
	}

	w.reader.repos[bootRepo].heads["main"] = bootMovedHead // the default branch moves: its tree is read again
	w.tree(bootMovedHead, bootFiles("v2"))
	w.mustCheck()
	if w.reader.reads[bootMovedHead] != 1 {
		t.Fatalf("the moved default branch was read %d times, want once", w.reader.reads[bootMovedHead])
	}
}

func TestChecksChooseNoCarrierWhenNoPullHasProtectedFilesAndReadEachHeadOnce(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead3, map[string]string{"src/main.go": "package main // 4\n"})
	w.tree(cjHead4, map[string]string{"src/main.go": "package main // 5\n"})
	w.sync(map[int]string{4: cjHead3, 5: cjHead4})

	w.mustCheck()
	w.mustCheck()

	w.want(4, checks.InProgress, "bootstrap-pending", 1)
	w.want(5, checks.InProgress, "bootstrap-pending", 1)
	if w.reader.reads[cjHead3] != 1 || w.reader.reads[cjHead4] != 1 {
		t.Fatalf("head reads %v, want each head read once: its verdict is cached by SHA", w.reader.reads)
	}
	if _, found := w.proposal(1); found {
		t.Fatal("a proposal was written with no carrier")
	}
}

func TestChecksProposeTheBootstrapOnTheCarrierAndMakeItReadyAfterApproval(t *testing.T) {
	w := newCJBootWorld(t)

	w.carrierPull()

	record := w.want(5, checks.InProgress, "approval-missing", 1)
	if record.Reference != "P1-R1" || record.Change != "protected-change" {
		t.Fatalf("record %+v, want the reference P1-R1", record)
	}
	w.want(4, checks.InProgress, "bootstrap-pending", 1)
	proposal, found := w.proposal(1)
	if !found || proposal.Current().OriginKind() != contract.OriginPull || proposal.Current().Origin() != cjHead2 || proposal.Current().Carrier() != carrierID(bootRepoID, 5) {
		t.Fatalf("P1 = %+v (found %v), want a pull revision from head 2 carried by pull 5", proposal, found)
	}
	summary := w.summaryOf("bootstrap-summary:1001:P1-R1")
	if summary["source"] != "pull" || summary["origin"] != cjHead2 || summary["branch"] != nil || summary["tier"] != "isolated" {
		t.Fatalf("summary %v, want a pull summary of head 2", summary)
	}
	if w.payload("bootstrap-summary:1001:P1-R1")["pull"] != float64(5) {
		t.Fatalf("the summary is not for pull 5: %v", w.payload("bootstrap-summary:1001:P1-R1"))
	}
	var check map[string]any
	if err := json.Unmarshal(w.ledger.messages[len(w.ledger.messages)-1].Payload, &check); err != nil || check["pull"] != float64(5) || check["reason"] != "approval-missing" ||
		check["reference"] != "P1-R1" || check["version"] != nil {
		t.Fatalf("check payload %v (%v), want approval-missing under P1-R1 for pull 5 with no canonical version", check, err)
	}

	if outcome := w.consent(contract.ApproveConsent, 201, 1, 1, 5); outcome != contract.ConsentApproved {
		t.Fatalf("approval outcome %v", outcome)
	}
	w.mustCheck()

	ready := w.want(5, checks.Success, "bootstrap-ready", 2)
	if ready.Reference != "P1-R1" {
		t.Fatalf("record %+v, want P1-R1", ready)
	}
	w.want(4, checks.InProgress, "bootstrap-pending", 1)
	if _, found := w.canonical(); found {
		t.Fatal("a canonical exists before the merge")
	}
}

func TestChecksAskForANewApprovalAfterAProtectedPushToTheCarrierAndKeepConsentAcrossAnImplementationPush(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.consent(contract.ApproveConsent, 201, 1, 1, 5)
	w.mustCheck()
	w.want(5, checks.Success, "bootstrap-ready", 2)

	files := bootFiles("v1")
	files["src/main.go"] = "package main // changed\n"
	w.tree(cjHead4, files)
	w.sync(map[int]string{4: cjHead3, 5: cjHead4}) // an implementation-only push
	w.mustCheck()
	if record := w.want(5, checks.Success, "bootstrap-ready", 3); record.Reference != "P1-R1" || record.HeadSHA != cjHead4 {
		t.Fatalf("record %+v, want head 4 ready under the same revision", record)
	}

	w.tree(cjHead5, bootFiles("v2"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead5}) // a protected push
	w.mustCheck()
	if record := w.want(5, checks.InProgress, "approval-missing", 4); record.Reference != "P1-R2" {
		t.Fatalf("record %+v, want the new revision P1-R2", record)
	}
	if summary := w.summaryOf("bootstrap-summary:1001:P1-R2"); summary["source"] != "pull" || summary["origin"] != cjHead5 {
		t.Fatalf("summary %v, want the pull summary of head 5: every bootstrap revision gets one", summary)
	}
	if keys := w.revisionKeys(); len(keys) != 0 {
		t.Fatalf("revision comments %v, want none: a bootstrap revision is announced by its summary", keys)
	}
}

func TestChecksReadNoTreeForAnUnchangedCarrier(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.consent(contract.ApproveConsent, 201, 1, 1, 5)
	w.mustCheck()
	w.want(5, checks.Success, "bootstrap-ready", 2)
	reads := w.reader.reads[cjHead2]

	w.mustCheck()
	w.mustCheck()

	if w.reader.reads[cjHead2] != reads {
		t.Fatalf("the carrier's tree was read %d more times with nothing changed", w.reader.reads[cjHead2]-reads)
	}
	w.want(5, checks.Success, "bootstrap-ready", 2)
}

func TestChecksLetOnlyTheLowestPullWithProtectedFilesCarryAndReadNoHeadBeyondIt(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead2, bootFiles("v1"))
	w.tree(cjHead4, bootFiles("v2"))
	w.sync(map[int]string{5: cjHead2, 6: cjHead4})

	w.mustCheck()

	w.want(5, checks.InProgress, "approval-missing", 1)
	w.want(6, checks.InProgress, "bootstrap-pending", 1)
	if _, found := w.proposal(2); found {
		t.Fatal("the second pull request got a proposal")
	}
	if w.reader.reads[cjHead4] != 0 {
		t.Fatalf("the head of pull 6 was read %d times, want none: the search stops at the carrier", w.reader.reads[cjHead4])
	}
}

func TestChecksKeepTheCarrierOfAnExistingProposalWhenALowerPullAppears(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead2, bootFiles("v1"))
	w.tree(cjHead4, bootFiles("v2"))
	w.sync(map[int]string{6: cjHead4})
	w.mustCheck()
	w.want(6, checks.InProgress, "approval-missing", 1)

	w.sync(map[int]string{5: cjHead2, 6: cjHead4}) // a lower pull appears later
	w.mustCheck()

	w.want(6, checks.InProgress, "approval-missing", 1)
	w.want(5, checks.InProgress, "bootstrap-pending", 1)
}

func TestChecksMoveTheBootstrapToTheNextPullWhenTheCarrierCloses(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead2, bootFiles("v1"))
	w.tree(cjHead4, bootFiles("v2"))
	w.sync(map[int]string{5: cjHead2, 6: cjHead4})
	w.mustCheck()
	w.want(6, checks.InProgress, "bootstrap-pending", 1)

	w.sync(map[int]string{6: cjHead4}) // pull 5 closes; nothing writes to the Suite
	w.mustCheck()
	w.mustCheck()

	record := w.want(6, checks.InProgress, "approval-missing", 2)
	if record.Reference != "P2-R1" {
		t.Fatalf("record %+v, want a new proposal P2-R1: P1 stays with the closed pull request", record)
	}
	if proposal, found := w.proposal(2); !found || proposal.Current().Carrier() != carrierID(bootRepoID, 6) {
		t.Fatalf("P2 = %+v (found %v), want it carried by pull 6", proposal, found)
	}
}

func TestChecksNeverChooseAForkAsTheCarrier(t *testing.T) {
	w := newCJBootWorld(t)
	w.headRepo = map[int]string{4: "evil/widgets"}
	w.tree(cjHead3, bootFiles("v1"))
	w.tree(cjHead2, bootFiles("v1"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})

	w.mustCheck()

	w.want(4, checks.InProgress, "fork-unsupported", 1)
	w.want(5, checks.InProgress, "approval-missing", 1)
	if w.reader.reads[cjHead3] != 0 {
		t.Fatalf("the fork's head was read %d times", w.reader.reads[cjHead3])
	}
}

func TestChecksSayBootstrapPendingForACarrierWhoseHeadLostItsProtectedFiles(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()

	w.tree(cjHead4, map[string]string{"src/main.go": "package main // no tests\n"})
	w.sync(map[int]string{4: cjHead3, 5: cjHead4})
	w.mustCheck()

	w.want(5, checks.InProgress, "bootstrap-pending", 2)
}

func TestChecksMapTheTreeProblemsOfTheCarrier(t *testing.T) {
	t.Run("a truncated tree", func(t *testing.T) {
		w := newCJBootWorld(t)
		w.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)
		w.sync(map[int]string{5: cjHead2})

		w.mustCheck()

		w.want(5, checks.InProgress, "source-unavailable", 1)
	})
	t.Run("an invalid declaration", func(t *testing.T) {
		w := newCJBootWorld(t)
		files := bootFiles("v1")
		files[".suiteward.yml"] = "version: 1\nunknown: [\n"
		w.tree(cjHead2, files)
		w.sync(map[int]string{5: cjHead2})

		w.mustCheck()

		w.want(5, checks.Failure, "invalid-source", 1)
	})
	t.Run("a failed read stops the search and waits", func(t *testing.T) {
		w := newCJBootWorld(t)
		w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)
		w.tree(cjHead4, bootFiles("v2"))
		w.sync(map[int]string{5: cjHead2, 6: cjHead4})

		if err := w.runChecks(); err != nil {
			t.Fatalf("a failed GitHub read must not fail the run: %v", err)
		}

		w.want(5, checks.InProgress, "assessment-pending", 1)
		w.want(6, checks.InProgress, "assessment-pending", 1)
		if _, found := w.proposal(1); found || w.reader.reads[cjHead4] != 0 {
			t.Fatalf("the search went past the pull request it could not read: proposal %v, reads of head 4 %d", found, w.reader.reads[cjHead4])
		}
		if got := w.problems.lookup(bootRepo); got != "read-failed" || strings.Contains(w.logs.String(), bootSecret) {
			t.Fatalf("problem %q, logs %s, want read-failed and no GitHub text", got, w.logs)
		}
	})
}

func TestChecksKeepTheRecordsWhenTheDefaultBranchCannotBeRead(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.reader.repos[bootRepo].headErr = errors.New(bootSecret)

	if err := w.runChecks(); err != nil {
		t.Fatalf("a failed GitHub read must not fail the run: %v", err)
	}

	w.want(5, checks.InProgress, "approval-missing", 1)
	w.want(4, checks.InProgress, "bootstrap-pending", 1)
	if got := w.problems.lookup(bootRepo); got != "read-failed" {
		t.Fatalf("problem %q, want read-failed", got)
	}
}

func TestChecksFailTheCarrierAfterARejectedOwnerCommandButNotAPendingPull(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")
	w.entry(4, 302, bootOwnerID, commands.InvalidCode, "")

	w.mustCheck()

	record := w.want(5, checks.Failure, "owner-command-rejected", 2)
	if record.Reference != "P1-R1" {
		t.Fatalf("record %+v, want the reference kept", record)
	}
	w.want(4, checks.InProgress, "bootstrap-pending", 1)
}

func TestChecksSayBootstrapPendingWhenTheDefaultBranchDeclarationIsInvalid(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(bootHead, map[string]string{".suiteward.yml": "version: 1\nunknown: [\n", "src/main.go": "package main\n"})
	w.tree(cjHead2, bootFiles("v1"))
	w.sync(map[int]string{5: cjHead2})

	w.mustCheck()

	w.want(5, checks.InProgress, "bootstrap-pending", 1)
	if got := w.problems.lookup(bootRepo); got != "invalid-source" {
		t.Fatalf("problem %q, want invalid-source", got)
	}
	if _, found := w.proposal(1); found {
		t.Fatal("a proposal was written while the default branch cannot be read as a contract")
	}
}

func TestChecksNeverMakeAPullHeadCanonicalOrAnnounceItAsAChange(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.consent(contract.ApproveConsent, 201, 1, 1, 5)

	for range 3 {
		w.mustCheck()
	}

	if keys := w.revisionKeys(); !slices.Equal(keys, nil) {
		t.Fatalf("revision comments %v, want none: a bootstrap proposal is announced by its summary", keys)
	}
	if _, found := w.canonical(); found {
		t.Fatal("the checks job made a pull head canonical")
	}
}
