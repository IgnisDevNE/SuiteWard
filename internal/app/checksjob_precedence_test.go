package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The order in which checksJob.pull decides a check (docs/contracts/runtime.md, "Check job and publisher (M1.6b)"): the state of the Suite and the head, then
// the wait that keeps the stored check, a wrong code, the runner block, the comments that come before the check, the check, and the comments that come after it.

// outboxFails fails the write of every outbox message whose key starts with prefix; a message of another key is written.
type outboxFails struct {
	governance.UnitOfWork
	prefix string
	err    error
}

func (s *outboxFails) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return s.UnitOfWork.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error {
		return fn(ctx, outboxFailsTx{Tx: tx, prefix: s.prefix, err: s.err})
	})
}

type outboxFailsTx struct {
	governance.Tx
	prefix string
	err    error
}

func (t outboxFailsTx) Outbox(ctx context.Context, message governance.OutboxMessage) error {
	if strings.HasPrefix(message.Key, t.prefix) {
		return t.err
	}
	return t.Tx.Outbox(ctx, message)
}

// storeCheck stores the check of pull 5 at head 2 as the job would have, with a reason the stored message does not need to match.
func (w *cjJobWorld) storeCheck(state checks.State, reason, change string) {
	w.t.Helper()
	w.tree(cjHead2, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	stored := checks.Result{RepositoryID: bootRepoID, PullNumber: 5, HeadSHA: cjHead2, State: state, Reason: reason, Change: change}
	_, _, err := w.memory.Publish(w.t.Context(), stored, w.clock, func(generation int64) (governance.OutboxMessage, error) {
		stored.Reason = reasonAssessmentPending // the message of the stored check only needs to be a valid one
		if state == checks.Failure {
			stored.Reason = reasonIntegrityFailed
		}
		return newCheckMessage(bootRepo, stored, owner.TierIsolated, "", generation)
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// U1: a wrong code fails the check although the assessment had to wait and kept the stored check.
func TestChecksFailAWrongCodeWhileTheAssessmentWaits(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")
	w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)

	if err := w.runChecks(); err != nil {
		t.Fatalf("a failed GitHub read must not fail the run: %v", err)
	}

	w.want(5, checks.Failure, "owner-command-rejected", 2)
	if got := w.problems.lookup(bootRepo); got != "read-failed" {
		t.Fatalf("problem %q, want read-failed", got)
	}
}

// U2: a wrong code fails a pull request of a repository whose Suite does not exist yet, which is the first rule of the job.
func TestChecksFailAWrongCodeBeforeTheSuiteExists(t *testing.T) {
	w := newCJWorld(t, false)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	w.want(4, checks.InProgress, "bootstrap-awaiting", 1)
	w.entry(4, 301, bootOwnerID, commands.InvalidCode, "")

	w.mustCheck()

	w.want(4, checks.Failure, "owner-command-rejected", 2)
}

// The wrong code on a fork before a canonical exists: a fork is final, so the check says fork-unsupported, whatever the owner commanded.
func TestChecksSayForkUnsupportedOverAWrongCodeBeforeTheFirstContract(t *testing.T) {
	w := newCJBootWorld(t)
	w.headRepo = map[int]string{5: "evil/widgets"}
	w.tree(cjHead2, bootFiles("v1"))
	w.sync(map[int]string{4: cjHead3, 5: cjHead2})
	w.mustCheck()
	w.want(5, checks.InProgress, "fork-unsupported", 1)
	w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")

	w.mustCheck()

	w.want(5, checks.InProgress, "fork-unsupported", 1)
}

// U3: a failed read keeps the stored check of this head for every reason that may still hold, and no other. A kept success is never kept
// when the runner files of the head cannot be read either (U4), so it is not in this table.
func TestChecksKeepTheStoredCheckForEachKeptReasonOnAFailedRead(t *testing.T) {
	for _, tc := range []struct {
		state  checks.State
		reason string
	}{
		{checks.InProgress, "approval-missing"},
		{checks.InProgress, "not-current"},
		{checks.InProgress, "assessment-pending"},
		{checks.InProgress, "source-unavailable"},
		{checks.Failure, "invalid-source"},
		{checks.Failure, "owner-command-rejected"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			w := newCJWorld(t, true)
			w.storeCheck(tc.state, tc.reason, "")
			w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)

			if err := w.runChecks(); err != nil {
				t.Fatalf("a failed GitHub read must not fail the run: %v", err)
			}

			w.want(5, tc.state, tc.reason, 1)
			if w.reader.fileReads != 0 {
				t.Fatalf("the files of the pull request were read %d times: the runner block is for a success or a runner-uncovered check only", w.reader.fileReads)
			}
		})
	}
	for _, reason := range []string{"integrity-failed", "fork-unsupported", "bootstrap-awaiting", "bootstrap-skipped", "bootstrap-pending"} {
		t.Run("not "+reason, func(t *testing.T) {
			w := newCJWorld(t, true)
			state := checks.InProgress
			if reason == "integrity-failed" {
				state = checks.Failure
			}
			w.storeCheck(state, reason, "")
			w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)

			if err := w.runChecks(); err != nil {
				t.Fatalf("a failed GitHub read must not fail the run: %v", err)
			}

			w.want(5, checks.InProgress, "assessment-pending", 2)
		})
	}
}

// U4: a stored runner-uncovered check is kept while the runner files of its head are known, and the files are read again only after a restart.
func TestChecksKeepARunnerUncoveredCheckOnAFailedReadOnlyWhileTheRunnerFilesAreKnown(t *testing.T) {
	setup := func(t *testing.T) *cjJobWorld {
		w := newCJWorld(t, true)
		w.runnerPull(withFiles(bootFiles("v1"), cjJest), added(cjJest)...)
		w.want(5, checks.InProgress, "runner-uncovered", 1)
		w.check.unchanged = nil // the stored change is unchanged: without the memory of that, the head is assessed again
		w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)
		return w
	}
	t.Run("known", func(t *testing.T) {
		w := setup(t)

		if err := w.runChecks(); err != nil {
			t.Fatal(err)
		}

		w.want(5, checks.InProgress, "runner-uncovered", 1)
	})
	t.Run("forgotten by a restart", func(t *testing.T) {
		w := setup(t)
		w.check.runners = nil

		if err := w.runChecks(); err != nil {
			t.Fatal(err)
		}

		w.want(5, checks.InProgress, "assessment-pending", 2)
	})
}

// U4: a kept success stays while the runner files of its head are known as none, and is pending after a restart.
func TestChecksKeepASuccessOnAFailedReadOnlyWhileTheRunnerFilesAreKnown(t *testing.T) {
	setup := func(t *testing.T) *cjJobWorld {
		w := newCJWorld(t, true)
		w.runnerPull(bootFiles("v1"))
		w.want(5, checks.Success, "unchanged", 1)
		w.check.unchanged = nil
		w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)
		return w
	}
	t.Run("known", func(t *testing.T) {
		w := setup(t)

		if err := w.runChecks(); err != nil {
			t.Fatal(err)
		}

		w.want(5, checks.Success, "unchanged", 1)
	})
	t.Run("forgotten by a restart", func(t *testing.T) {
		w := setup(t)
		w.check.runners = nil

		if err := w.runChecks(); err != nil {
			t.Fatal(err)
		}

		w.want(5, checks.InProgress, "assessment-pending", 2)
	})
}

// U5: the runner block applies to a success and to a runner-uncovered check only: any other state keeps its reason and reads no file of the pull request.
func TestChecksSkipTheRunnerBlockForEveryOtherState(t *testing.T) {
	t.Run("assessment-pending", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)
		w.reader.files = map[int][]github.PullFile{5: added(cjJest)}
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.want(5, checks.InProgress, "assessment-pending", 1)
		w.wantRunnerKeys()
		if w.reader.fileReads != 0 {
			t.Fatalf("the files of the pull request were read %d times", w.reader.fileReads)
		}
	})
	t.Run("source-unavailable", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)
		w.reader.files = map[int][]github.PullFile{5: added(cjJest)}
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.want(5, checks.InProgress, "source-unavailable", 1)
		w.wantRunnerKeys()
		if w.reader.fileReads != 0 {
			t.Fatalf("the files of the pull request were read %d times", w.reader.fileReads)
		}
	})
	t.Run("approval-missing", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.runnerPull(withFiles(bootFiles("v2"), cjJest), added(cjJest)...)

		w.want(5, checks.InProgress, "approval-missing", 1)
		w.wantRunnerKeys()
	})
}

// U6: the comment that comes before the check is written although the check is not: a run whose publish fails writes it once, and the comments
// that come after the check, which are evidence and decide nothing, are not written before it.
func TestChecksWriteTheCommentBeforeTheCheckAndNeverTheOnesAfterIt(t *testing.T) {
	failure := errors.New("the ledger is down")
	t.Run("the bootstrap notice", func(t *testing.T) {
		w := newCJBootWorld(t)
		w.sync(map[int]string{4: cjHead3})
		w.ledger.publishErr = failure

		err := w.runChecks()

		if !errors.Is(err, failure) || !strings.Contains(err.Error(), "publish the check") {
			t.Fatalf("run error %v, want the failed publish", err)
		}
		w.wantNone(4)
		w.wantKeys("bootstrap-notice:", "bootstrap-notice:1001:4")
		w.ledger.publishErr = nil
		w.mustCheck()
		w.want(4, checks.InProgress, "bootstrap-awaiting", 1)
		w.wantKeys("bootstrap-notice:", "bootstrap-notice:1001:4")
	})
	t.Run("the invalid-source comment", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.linkHead(cjHead2, "spec_link")
		w.sync(map[int]string{4: bootHead, 5: cjHead2})
		w.ledger.publishErr = failure

		err := w.runChecks()

		if !errors.Is(err, failure) {
			t.Fatalf("run error %v, want the failed publish", err)
		}
		w.wantNone(5)
		w.wantInvalidSource(linkKeyOf(t, "spec_link"))
		w.ledger.publishErr = nil
		w.mustCheck()
		w.want(5, checks.Failure, "invalid-source", 1)
		w.wantInvalidSource(linkKeyOf(t, "spec_link"))
	})
	t.Run("the runner and the scope comments", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.ledger.publishErr = failure
		w.reader.files = map[int][]github.PullFile{5: added(cjJest, cjUtilTst)}
		w.bases = map[int]string{5: cjBase}
		w.tree(cjHead2, withFiles(bootFiles("v1"), cjJest, cjUtilTst))
		w.tree(cjBase, bootFiles("v1"))
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		err := w.runChecks()

		if !errors.Is(err, failure) {
			t.Fatalf("run error %v, want the failed publish", err)
		}
		w.wantNone(5)
		w.wantRunnerKeys()
		w.wantNoScopeMessages()
		w.ledger.publishErr = nil
		w.mustCheck()
		w.want(5, checks.InProgress, "runner-uncovered", 1)
		if len(w.scopeKeys("runner-uncovered:")) != 1 || len(w.scopeKeys("scope-notice:")) != 1 {
			t.Fatalf("outbox %v, want one runner-uncovered comment and one scope notice", w.outboxKeys())
		}
	})
}

// U7: a head found unchanged is kept without a read only against the canonical it was found unchanged for.
func TestChecksReadAnUnchangedHeadAgainWhenTheCanonicalMoved(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	reads := w.reader.reads[bootHead]
	w.check.unchanged[checkPull{bootRepoID, 4}] = unchangedAt{bootHead, "V0"} // what the job remembers is from another canonical
	w.protectedPull()                                                         // the Suite moves

	w.mustCheck()

	if w.reader.reads[bootHead] <= reads {
		t.Fatalf("the unchanged head was not read again: %d reads after %d", w.reader.reads[bootHead], reads)
	}
	w.want(4, checks.Success, "unchanged", 1)
}

// U8: a comment that comes after the check and fails fails the run, and the check was published; the next run writes the comment.
func TestChecksFailTheRunWhenACommentAfterTheCheckFailsButKeepTheCheck(t *testing.T) {
	failure := errors.New("the outbox is down")
	for _, tc := range []struct {
		name, prefix string
		diff         []github.PullFile
		files        []string
		state        checks.State
		reason       string
	}{
		{"the runner comment", "runner-uncovered:", added(cjJest), []string{cjJest}, checks.InProgress, "runner-uncovered"},
		{"the scope notice", "scope-notice:", added(cjUtilTst), []string{cjUtilTst}, checks.Success, "unchanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCJWorld(t, true)
			w.check.suites = &outboxFails{UnitOfWork: w.suites, prefix: tc.prefix, err: failure}
			w.reader.files = map[int][]github.PullFile{5: tc.diff}
			w.bases = map[int]string{5: cjBase}
			w.tree(cjHead2, withFiles(bootFiles("v1"), tc.files...))
			w.tree(cjBase, bootFiles("v1"))
			w.sync(map[int]string{4: bootHead, 5: cjHead2})

			err := w.runChecks()

			if !errors.Is(err, failure) {
				t.Fatalf("run error %v, want the failed comment", err)
			}
			w.want(5, tc.state, tc.reason, 1)
			w.check.suites = w.suites
			w.mustCheck()
			w.want(5, tc.state, tc.reason, 1)
			if got := w.scopeKeys(tc.prefix); len(got) != 1 {
				t.Fatalf("messages %v after the retry, want one", got)
			}
		})
	}
}
