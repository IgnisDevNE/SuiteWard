package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The checks job before V1 (docs/contracts/runtime.md, "Explicit bootstrap: propose and skip"): the carrier is the pull request
// that carries the Suite's latest bootstrap proposal, the owner starts it with `propose`, and every other pull request waits.

const (
	cjHead6 = "6666666666666666666666666666666666666666"
	cjHead7 = "7777777777777777777777777777777777777777"
	cjHead8 = "8888888888888888888888888888888888888888"
)

// newCJBootWorld is a repository whose Suite exists and has no canonical and no proposal. The checks job never reads the
// default branch, so the world has no tree for it that matters.
func newCJBootWorld(t *testing.T) *cjJobWorld {
	t.Helper()
	w := newCJWorld(t, false)
	w.seedSuite(bootRepoID, "branch:main")
	return w
}

// carrierPull opens pull 4 (no protected files) and pull 5 at head 2 (protected files, v1), proposes the bootstrap on pull 5 as the
// commands job would, and checks them.
func (w *cjJobWorld) carrierPull() {
	w.t.Helper()
	w.tree(cjHead2, bootFiles("v1"))
	w.tree(cjHead3, map[string]string{"src/main.go": "package main // 4\n"})
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})
	w.pullProposal(5, cjHead2, bootFiles("v1"))
	w.mustCheck()
}

// commentKeys are the keys of the Suite's outbox that start with prefix.
func (w *cjJobWorld) commentKeys(prefix string) []string {
	var keys []string
	for _, key := range w.outboxKeys() {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys
}

func (w *cjJobWorld) wantKeys(prefix string, want ...string) {
	w.t.Helper()
	if got := w.commentKeys(prefix); !slices.Equal(got, want) {
		w.t.Fatalf("outbox keys %q = %v, want %v", prefix, got, want)
	}
}

func TestChecksReadNothingAndSayBootstrapAwaitingWhileTheSuiteDoesNotExist(t *testing.T) {
	w := newCJWorld(t, false)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	w.mustCheck()
	w.mustCheck()

	for _, pull := range []int{4, 5} {
		if record := w.want(pull, checks.InProgress, "bootstrap-awaiting", 1); record.Reference != "" {
			t.Fatalf("record %+v, want no reference", record)
		}
	}
	if w.reader.calls["head"] != 0 || len(w.reader.reads) != 0 {
		t.Fatalf("GitHub reads %v and tree reads %v, want none before the Suite exists", w.reader.calls, w.reader.reads)
	}
	w.requireOutbox() // there is no notice comment without a Suite
}

func TestChecksSayAwaitingWithOneNoticePerPullWhenNoProposalExists(t *testing.T) {
	w := newCJBootWorld(t)
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})

	w.mustCheck()
	w.mustCheck()

	w.want(4, checks.InProgress, "bootstrap-awaiting", 1)
	w.want(5, checks.InProgress, "bootstrap-awaiting", 1)
	w.wantKeys("bootstrap-notice:", "bootstrap-notice:1001:4", "bootstrap-notice:1001:5")
	payload := w.payload("bootstrap-notice:1001:4")
	if len(payload) != 3 || payload["repositoryId"] != float64(bootRepoID) || payload["repo"] != bootRepo || payload["pull"] != float64(4) {
		t.Fatalf("notice payload = %v, want the repository and the pull request only", payload)
	}
	if w.reader.calls["head"] != 0 || len(w.reader.reads) != 0 {
		t.Fatalf("GitHub reads %v and tree reads %v, want none: the checks job reads neither the default branch nor a head with no proposal", w.reader.calls, w.reader.reads)
	}

	w.sync(map[int]string{4: cjHead4, 5: cjHead2}) // a new head on pull 4: a new check, but not a second notice
	w.mustCheck()
	w.want(4, checks.InProgress, "bootstrap-awaiting", 2)
	w.wantKeys("bootstrap-notice:", "bootstrap-notice:1001:4", "bootstrap-notice:1001:5")
}

func TestChecksSayForkUnsupportedFirstAndWriteNoNoticeForAFork(t *testing.T) {
	w := newCJBootWorld(t)
	w.headRepo = map[int]string{4: "evil/widgets", 5: "evil/widgets"}
	w.tree(cjHead2, bootFiles("v1"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})
	w.pullProposal(5, cjHead2, bootFiles("v1")) // a fork can carry no proposal, but if one did, the fork rule comes first

	w.mustCheck()

	w.want(4, checks.InProgress, "fork-unsupported", 1)
	w.want(5, checks.InProgress, "fork-unsupported", 1)
	w.wantKeys("bootstrap-notice:")
	if w.reader.reads[cjHead2] != 0 {
		t.Fatalf("the fork carrier's head was read %d times", w.reader.reads[cjHead2])
	}
}

func TestChecksAssessTheCarrierOfAPullProposalAndMakeItReadyAfterApproval(t *testing.T) {
	w := newCJBootWorld(t)

	w.carrierPull()

	record := w.want(5, checks.InProgress, "approval-missing", 1)
	if record.Reference != "P1-R1" || record.Change != "protected-change" {
		t.Fatalf("record %+v, want the reference P1-R1", record)
	}
	w.want(4, checks.InProgress, "bootstrap-awaiting", 1)
	w.wantKeys("bootstrap-notice:", "bootstrap-notice:1001:4")
	w.wantKeys("bootstrap-cancelled:")
	if outcome := w.consent(contract.ApproveConsent, 201, 1, 1, 5); outcome != contract.ConsentApproved {
		t.Fatalf("approval outcome %v", outcome)
	}
	w.mustCheck()

	ready := w.want(5, checks.Success, "bootstrap-ready", 2)
	if ready.Reference != "P1-R1" {
		t.Fatalf("record %+v, want P1-R1", ready)
	}
	w.want(4, checks.InProgress, "bootstrap-awaiting", 1)
	if _, found := w.canonical(); found {
		t.Fatal("a canonical exists before the merge")
	}
	if keys := w.revisionKeys(); len(keys) != 0 {
		t.Fatalf("revision comments %v, want none: a bootstrap proposal is announced by its summary", keys)
	}
}

func TestChecksSayApprovalMissingForTheCarrierOfABranchProposalUntilV1ExistsAndReadNoHead(t *testing.T) {
	w := newCJBootWorld(t)
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.branchProposal(4)

	w.mustCheck()

	if record := w.want(4, checks.InProgress, "approval-missing", 1); record.Reference != "P1-R1" {
		t.Fatalf("record %+v, want the reference P1-R1", record)
	}
	w.want(5, checks.InProgress, "bootstrap-awaiting", 1)
	w.approve(1, 4)
	w.mustCheck()
	w.want(4, checks.InProgress, "approval-missing", 1) // until the bootstrap job promotes V1
	if w.reader.reads[bootHead] != 0 || w.reader.reads[cjHead3] != 0 {
		t.Fatalf("tree reads %v, want none: a branch carrier needs no head assessment", w.reader.reads)
	}

	w.mustRun() // the bootstrap job promotes V1 from the approved branch proposal
	w.mustCheck()

	w.want(4, checks.Success, "unchanged", 2) // the pull request is assessed as a change against V1
}

func TestChecksSayBootstrapSkippedForASkippedPullAndReadItEveryRun(t *testing.T) {
	w := newCJBootWorld(t)
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})
	w.mustCheck()
	w.want(4, checks.InProgress, "bootstrap-awaiting", 1)

	w.entry(4, 301, bootOwnerID, commands.Skipped, "") // the owner skips pull 4 at an unchanged head
	w.mustCheck()

	if record := w.want(4, checks.Success, "bootstrap-skipped", 2); record.Reference != "" || record.Change != "" {
		t.Fatalf("record %+v, want a success that assessed nothing", record)
	}
	w.want(5, checks.InProgress, "bootstrap-awaiting", 1)
	if len(w.reader.reads) != 0 {
		t.Fatalf("tree reads %v, want none for a skipped pull request", w.reader.reads)
	}
	w.mustCheck()
	w.want(4, checks.Success, "bootstrap-skipped", 2)
}

func TestChecksIgnoreASkipFromBeforeTheCurrentEnrollment(t *testing.T) {
	w := newCJBootWorld(t)
	w.sync(map[int]string{4: cjHead3})
	skip := commands.Entry{RepositoryID: bootRepoID, CommentID: 301, PullNumber: 4, AuthorID: bootOwnerID, CreatedAt: bootTime.Add(-time.Hour),
		BodyDigest: artifact.Hash([]byte("body")), Disposition: commands.Skipped, DecidedAt: bootTime.Add(-time.Hour + time.Second)}
	if err := w.cmds.Record(t.Context(), skip, nil); err != nil {
		t.Fatal(err)
	}

	w.mustCheck()

	w.want(4, checks.InProgress, "bootstrap-awaiting", 1)
}

func TestChecksLetTheCarrierOutrankASkip(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead2, bootFiles("v1"))
	w.sync(map[int]string{5: cjHead2})
	w.entry(5, 301, bootOwnerID, commands.Skipped, "")
	w.pullProposal(5, cjHead2, bootFiles("v1")) // the owner proposed on a pull request they had skipped

	w.mustCheck()

	w.want(5, checks.InProgress, "approval-missing", 1)
}

func TestChecksNeverKeepASkippedSuccessWhenTheSkippedPullCannotBeAssessed(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead3, bootFiles("v1"))
	w.sync(map[int]string{4: cjHead3})
	w.entry(4, 301, bootOwnerID, commands.Skipped, "")
	w.mustCheck()
	w.want(4, checks.Success, "bootstrap-skipped", 1)

	w.pullProposal(4, cjHead3, bootFiles("v1")) // the same head becomes the carrier, and GitHub fails
	w.reader.repos[bootRepo].treeErr[cjHead3] = errors.New(bootSecret)
	if err := w.runChecks(); err != nil {
		t.Fatalf("a failed GitHub read must not fail the run: %v", err)
	}

	w.want(4, checks.InProgress, "assessment-pending", 2)
}

func TestChecksCancelAProtectedPushToTheCarrierAndCommentOncePerRevision(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.consent(contract.ApproveConsent, 201, 1, 1, 5)
	w.mustCheck()
	w.want(5, checks.Success, "bootstrap-ready", 2)

	files := bootFiles("v1")
	files["src/main.go"] = "package main // changed\n"
	w.tree(cjHead4, files)
	w.sync(map[int]string{4: cjHead3, 5: cjHead4}) // an implementation-only push keeps the consent
	w.mustCheck()
	if record := w.want(5, checks.Success, "bootstrap-ready", 3); record.Reference != "P1-R1" || record.HeadSHA != cjHead4 {
		t.Fatalf("record %+v, want head 4 ready under the same revision", record)
	}

	w.tree(cjHead5, bootFiles("v2"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead5}) // a protected push
	w.mustCheck()
	if record := w.want(5, checks.InProgress, "bootstrap-awaiting", 4); record.Reference != "P1-R1" {
		t.Fatalf("record %+v, want a cancelled head that names P1-R1", record)
	}
	w.wantKeys("bootstrap-cancelled:", "bootstrap-cancelled:1001:P1-R1")
	payload := w.payload("bootstrap-cancelled:1001:P1-R1")
	if len(payload) != 4 || payload["pull"] != float64(5) || payload["reference"] != "P1-R1" || payload["repositoryId"] != float64(bootRepoID) || payload["repo"] != bootRepo {
		t.Fatalf("cancelled payload = %v, want the target and the reference", payload)
	}
	if proposal, _ := w.proposal(1); len(proposal.Revisions()) != 1 {
		t.Fatalf("P1 has %d revisions, want the checks job to revise nothing", len(proposal.Revisions()))
	}
	reads := w.reader.reads[cjHead5]
	w.mustCheck()
	w.mustCheck()
	w.want(5, checks.InProgress, "bootstrap-awaiting", 4)
	if w.reader.reads[cjHead5] != reads {
		t.Fatalf("a cancelled carrier's tree was read %d more times at an unchanged head", w.reader.reads[cjHead5]-reads)
	}
	w.wantKeys("bootstrap-cancelled:", "bootstrap-cancelled:1001:P1-R1")

	w.tree(cjHead6, bootFiles("v1"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead6}) // a push that restores the exact coverage makes the revision current again
	w.mustCheck()
	if record := w.want(5, checks.Success, "bootstrap-ready", 5); record.Reference != "P1-R1" {
		t.Fatalf("record %+v, want the approved revision current again", record)
	}

	w.tree(cjHead7, bootFiles("v3"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead7})
	w.pullProposal(5, cjHead7, bootFiles("v3")) // the owner proposes again: P2-R1
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 6)
	w.tree(cjHead8, bootFiles("v4"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead8})
	w.mustCheck()
	if record := w.want(5, checks.InProgress, "bootstrap-awaiting", 7); record.Reference != "P2-R1" {
		t.Fatalf("record %+v, want the new revision named", record)
	}
	w.wantKeys("bootstrap-cancelled:", "bootstrap-cancelled:1001:P1-R1", "bootstrap-cancelled:1001:P2-R1")
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

// A record that only waited has no reference, so it is never an assessment of the carrier, however the Suite moved.
func TestChecksNeverTakeAWaitedRecordAsTheAssessmentOfTheCarrier(t *testing.T) {
	t.Run("a pull request that was no carrier when it waited", func(t *testing.T) {
		w := newCJBootWorld(t)
		w.tree(cjHead2, bootFiles("v1"))
		w.sync(map[int]string{5: cjHead2})
		w.mustCheck()
		w.want(5, checks.InProgress, "bootstrap-awaiting", 1)

		w.pullProposal(5, cjHead2, bootFiles("v1")) // a propose makes it the carrier at the same head
		w.mustCheck()

		if record := w.want(5, checks.InProgress, "approval-missing", 2); record.Reference != "P1-R1" {
			t.Fatalf("record %+v, want the assessment of P1-R1", record)
		}
		w.mustCheck()
		w.want(5, checks.InProgress, "approval-missing", 2)
	})
	t.Run("a carrier whose read failed", func(t *testing.T) {
		w := newCJBootWorld(t)
		w.tree(cjHead2, bootFiles("v1"))
		w.sync(map[int]string{5: cjHead2})
		w.pullProposal(5, cjHead2, bootFiles("v1"))
		w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)
		if err := w.runChecks(); err != nil {
			t.Fatalf("a failed GitHub read must not fail the run: %v", err)
		}
		w.want(5, checks.InProgress, "assessment-pending", 1)
		if got := w.problems.lookup(bootRepo); got != "read-failed" || strings.Contains(w.logs.String(), bootSecret) {
			t.Fatalf("problem %q, logs %s, want read-failed and no GitHub text", got, w.logs)
		}

		delete(w.reader.repos[bootRepo].treeErr, cjHead2) // GitHub answers again at the same head and Suite revision
		w.mustCheck()

		w.want(5, checks.InProgress, "approval-missing", 2)
		if got := w.problems.lookup(bootRepo); got != "" {
			t.Fatalf("problem %q, want it cleared", got)
		}
	})
}

func TestChecksMoveTheCarrierToTheNewestProposal(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead2, bootFiles("v1"))
	w.tree(cjHead4, bootFiles("v2"))
	w.sync(map[int]string{5: cjHead2, 6: cjHead4})
	w.pullProposal(5, cjHead2, bootFiles("v1"))
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 1)
	w.want(6, checks.InProgress, "bootstrap-awaiting", 1)

	w.pullProposal(6, cjHead4, bootFiles("v2")) // P2 is carried by pull 6: the newest proposal wins
	w.mustCheck()

	if record := w.want(6, checks.InProgress, "approval-missing", 2); record.Reference != "P2-R1" {
		t.Fatalf("record %+v, want P2-R1", record)
	}
	if record := w.want(5, checks.InProgress, "bootstrap-awaiting", 2); record.Reference != "" {
		t.Fatalf("record %+v, want no reference once pull 5 carries nothing", record)
	}
	w.wantKeys("bootstrap-notice:", "bootstrap-notice:1001:5", "bootstrap-notice:1001:6")
}

func TestChecksKeepTheCarrierWhenALowerPullAppears(t *testing.T) {
	w := newCJBootWorld(t)
	w.tree(cjHead4, bootFiles("v2"))
	w.sync(map[int]string{6: cjHead4})
	w.pullProposal(6, cjHead4, bootFiles("v2"))
	w.mustCheck()
	w.want(6, checks.InProgress, "approval-missing", 1)

	w.tree(cjHead2, bootFiles("v1"))
	w.sync(map[int]string{5: cjHead2, 6: cjHead4}) // a lower pull appears later
	w.mustCheck()

	w.want(6, checks.InProgress, "approval-missing", 1)
	w.want(5, checks.InProgress, "bootstrap-awaiting", 1)
}

func TestChecksMapTheTreeProblemsOfTheCarrierAndReadThemOnce(t *testing.T) {
	t.Run("a truncated tree", func(t *testing.T) {
		w := newCJBootWorld(t)
		w.sync(map[int]string{5: cjHead2})
		w.pullProposal(5, cjHead2, bootFiles("v1"))
		w.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)

		w.mustCheck()
		w.mustCheck()

		w.want(5, checks.InProgress, "source-unavailable", 1)
		if w.reader.reads[cjHead2] != 1 {
			t.Fatalf("the truncated head was read %d times over two runs, want once", w.reader.reads[cjHead2])
		}
	})
	t.Run("an invalid declaration", func(t *testing.T) {
		w := newCJBootWorld(t)
		files := bootFiles("v1")
		files[".suiteward.yml"] = "version: 1\nunknown: [\n"
		w.tree(cjHead2, files)
		w.sync(map[int]string{5: cjHead2})
		w.pullProposal(5, cjHead2, bootFiles("v1"))

		w.mustCheck()

		w.want(5, checks.Failure, "invalid-source", 1)
	})
}

func TestChecksFailTheCarrierAfterARejectedOwnerCommandButNotAnAwaitingOrSkippedPull(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.sync(map[int]string{4: cjHead3, 5: cjHead2, 6: cjHead4})
	w.mustCheck()
	w.entry(4, 302, bootOwnerID, commands.Skipped, "")
	w.mustCheck()
	w.want(4, checks.Success, "bootstrap-skipped", 2)
	w.want(6, checks.InProgress, "bootstrap-awaiting", 1)

	w.entry(5, 301, bootOwnerID, commands.InvalidCode, "") // a stale approve on the carrier, on a skipped pull and on an awaiting one
	w.entry(4, 303, bootOwnerID, commands.InvalidCode, "")
	w.entry(6, 304, bootOwnerID, commands.ReplayedCode, "")
	w.mustCheck()

	record := w.want(5, checks.Failure, "owner-command-rejected", 2)
	if record.Reference != "P1-R1" {
		t.Fatalf("record %+v, want the reference kept", record)
	}
	w.want(4, checks.Success, "bootstrap-skipped", 2)
	w.want(6, checks.InProgress, "bootstrap-awaiting", 1)
}

func TestChecksKeepACancelledCarrierAwaitingAfterARejectedOwnerCommand(t *testing.T) {
	w := newCJBootWorld(t)
	w.carrierPull()
	w.tree(cjHead5, bootFiles("v2"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead5})
	w.mustCheck()
	w.want(5, checks.InProgress, "bootstrap-awaiting", 2)

	w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")
	w.mustCheck()

	w.want(5, checks.InProgress, "bootstrap-awaiting", 2)
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

// cancelOnTree cancels the run while the job reads the carrier's head, as a shutdown would.
type cancelOnTree struct {
	*cjCountingReader
	cancel context.CancelFunc
}

func (r cancelOnTree) Tree(context.Context, string, string) (inventory.Tree, error) {
	r.cancel()
	return nil, errors.New(bootSecret)
}

func TestChecksReportACancelledRunInsteadOfAWaitingCheck(t *testing.T) {
	w := newCJBootWorld(t)
	w.sync(map[int]string{5: cjHead2})
	w.pullProposal(5, cjHead2, bootFiles("v1"))
	ctx, cancel := context.WithCancel(t.Context())
	w.check.reader = cancelOnTree{cjCountingReader: w.reader, cancel: cancel}

	err := w.check.run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation, not a read failure that waits", err)
	}
	if _, found := w.record(5); found {
		t.Fatal("a cancelled run published a check")
	}
	if strings.Contains(w.logs.String(), bootSecret) {
		t.Fatalf("logs %s carry GitHub text", w.logs)
	}
}
