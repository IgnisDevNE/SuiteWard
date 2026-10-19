package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks/checkstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity/integritytest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	cjHead2     = bootMovedHead // a head whose protected files differ from the canonical
	cjHead3     = "3333333333333333333333333333333333333333"
	cjHead4     = "4444444444444444444444444444444444444444"
	cjHead5     = "5555555555555555555555555555555555555555"
	cjOtherUser = int64(99)
)

// cjCountingReader answers the job's tree reads from the boot repositories and counts them per commit.
type cjCountingReader struct {
	*fakeBootReader
	reads     map[string]int
	files     map[int][]github.PullFile // the files each pull request changes
	filesErr  error                     // every PullFiles fails with it
	fileReads int
}

func (r *cjCountingReader) PullFiles(_ context.Context, _ string, number int) ([]github.PullFile, error) {
	r.fileReads++
	return r.files[number], r.filesErr
}

func (r *cjCountingReader) Tree(ctx context.Context, repo, sha string) (inventory.Tree, error) {
	r.reads[sha]++
	return r.fakeBootReader.Tree(ctx, repo, sha)
}

// cjRecordingLedger keeps what the job publishes, as the single writer of the ledger.
type cjRecordingLedger struct {
	checks.Store
	messages   []governance.OutboxMessage
	publishErr error // every Publish fails with it, before it writes anything
}

func (l *cjRecordingLedger) Publish(ctx context.Context, r checks.Result, at time.Time, message func(int64) (governance.OutboxMessage, error)) (checks.Record, bool, error) {
	if l.publishErr != nil {
		return checks.Record{}, false, l.publishErr
	}
	return l.Store.Publish(ctx, r, at, func(generation int64) (governance.OutboxMessage, error) {
		m, err := message(generation)
		if err == nil {
			l.messages = append(l.messages, m)
		}
		return m, err
	})
}

// erroringSuites fails the units of work of one project with err.
type erroringSuites struct {
	governance.UnitOfWork
	project contract.ProjectID
	err     error
}

func (s *erroringSuites) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	if project == s.project {
		return s.err
	}
	return s.UnitOfWork.Do(ctx, project, suite, fn)
}

// cjJobWorld is a bootstrapped repository (canonical V1 from the tree of bootHead, pull 4) with the job's fakes.
type cjJobWorld struct {
	*bootWorld
	reader   *cjCountingReader
	ledger   *cjRecordingLedger
	memory   *checkstest.Memory
	verdicts *integritytest.Memory
	headRepo map[int]string // the observed head repository of a pull request; absent means not observed
	bases    map[int]string // the observed base commit of a pull request; absent means bootHead
	refs     map[int]string // the observed head ref of a pull request; absent means "feature"
	cmds     *commandstest.Memory
	check    *checksJob
	problems *bootstrapProblems
	clock    time.Time
	heads    map[int]string // the open pull requests of bootRepo and their heads
}

func newCJWorld(t *testing.T, canonical bool, options ...func(*bootOptions)) *cjJobWorld {
	t.Helper()
	var b *bootWorld
	switch {
	case canonical:
		b = approvedWorld(t, options...)
		b.mustRun() // promotes V1
		if _, found := b.canonical(); !found {
			t.Fatalf("the setup did not bootstrap the canonical\nlogs: %s", b.logs)
		}
	default:
		b = newBootWorld(t, options...)
	}
	return wrapCJWorld(t, b)
}

// newCJWorldOf is a canonical world whose V1 is the tree of files: the default branch holds them and the owner approved them.
func newCJWorldOf(t *testing.T, files map[string]string) *cjJobWorld {
	t.Helper()
	b := newBootWorld(t)
	b.reader.repo(bootRepo).trees[bootHead] = &bootTree{files: files}
	b.openPulls(bootRepo, 4)
	b.seedSuite(bootRepoID, "branch:main")
	proposeBaseline(t, b.suites, b.content, bootRepoID, bootRepo, contract.OriginBranch, 4, bootHead, files)
	b.approve(1, 4)
	b.mustRun() // promotes V1
	if _, found := b.canonical(); !found {
		t.Fatalf("the setup did not bootstrap the canonical\nlogs: %s", b.logs)
	}
	w := wrapCJWorld(t, b)
	w.check.contents = declarationContents(files[".suiteward.yml"])
	return w
}

// declarationContents answers every read with the same declaration, the canonical's own.
type declarationContents string

func (c declarationContents) Read(context.Context, artifact.Digest) ([]byte, error) {
	return []byte(c), nil
}

func wrapCJWorld(t *testing.T, b *bootWorld) *cjJobWorld {
	t.Helper()
	w := &cjJobWorld{bootWorld: b, memory: checkstest.NewMemory(), verdicts: integritytest.NewMemory(), cmds: commandstest.NewMemory(b.owners, b.pulls), problems: &bootstrapProblems{},
		clock: bootTime.Add(2 * time.Hour), heads: map[int]string{4: bootHead}}
	w.reader = &cjCountingReader{fakeBootReader: b.reader, reads: map[string]int{}}
	w.ledger = &cjRecordingLedger{Store: w.memory}
	w.check = &checksJob{repos: b.job.repos, reader: w.reader, owners: b.owners, suites: b.suites, contents: nopContents{}, content: b.content, ledger: w.ledger, integrity: w.verdicts, commands: w.cmds,
		pulls: b.pulls, tier: b.tier, problems: w.problems, logger: slog.New(slog.NewJSONHandler(b.logs, nil)), now: func() time.Time { return w.clock }}
	return w
}

// nopContents is a ContentReader for a canonical whose scope declaration the job never needs from the store: the
// bootstrap stored it in the content writer fake, which keeps digests only.
type nopContents struct{}

func (nopContents) Read(context.Context, artifact.Digest) ([]byte, error) {
	return []byte("version: 1\ninclude: [/tests/**]\n"), nil
}

// PullFiles lists no changed file: the integration tests that share this reader never ask for a diff that matters.
func (f *fakeBootReader) PullFiles(context.Context, string, int) ([]github.PullFile, error) {
	return nil, nil
}

func (w *cjJobWorld) tree(sha string, files map[string]string) {
	w.reader.repos[bootRepo].trees[sha] = &bootTree{files: files}
}

// sync records a cycle in which exactly the pull requests of heads are open.
func (w *cjJobWorld) sync(heads map[int]string) {
	w.t.Helper()
	w.heads = heads
	w.cycles++
	cycle := githubsync.Cycle{Repo: bootRepo, InstallationID: 1, At: bootTime.Add(time.Duration(w.cycles) * time.Minute), StartedAt: bootTime.Add(time.Duration(w.cycles) * time.Minute), Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
	for number, head := range heads {
		cycle.OpenPulls = append(cycle.OpenPulls, githubsync.ObservedPull{Number: number, HeadSHA: head, HeadRef: cmp.Or(w.refs[number], "feature"), BaseRef: "main", BaseSHA: cmp.Or(w.bases[number], bootHead), UpdatedAt: bootTime, HeadRepo: w.headRepo[number]})
		cycle.Comments[number] = nil
	}
	if _, err := w.pulls.RecordCycle(w.t.Context(), cycle); err != nil {
		w.t.Fatal(err)
	}
}

func (w *cjJobWorld) runChecks() error {
	w.clock = w.clock.Add(time.Minute)
	return w.check.run(w.t.Context())
}

func (w *cjJobWorld) mustCheck() {
	w.t.Helper()
	if err := w.runChecks(); err != nil {
		w.t.Fatalf("checks run: %v\nlogs: %s", err, w.logs)
	}
}

func (w *cjJobWorld) record(pull int) (checks.Record, bool) {
	w.t.Helper()
	record, found, err := w.memory.Current(w.t.Context(), bootRepoID, pull)
	if err != nil {
		w.t.Fatal(err)
	}
	return record, found
}

// want requires the pull's current check to be state/reason of the given generation (reference when not empty).
func (w *cjJobWorld) want(pull int, state checks.State, reason string, generation int64) checks.Record {
	w.t.Helper()
	record, found := w.record(pull)
	if !found {
		w.t.Fatalf("pull %d has no check record\nlogs: %s", pull, w.logs)
	}
	if record.State != state || record.Reason != reason || record.Generation != generation {
		w.t.Fatalf("pull %d: %s/%s generation %d, want %s/%s generation %d\nlogs: %s", pull, record.State, record.Reason, record.Generation, state, reason, generation, w.logs)
	}
	return record
}

func (w *cjJobWorld) wantNone(pull int) {
	w.t.Helper()
	if record, found := w.record(pull); found {
		w.t.Fatalf("pull %d has the check record %+v, want none", pull, record)
	}
}

// consent applies the owner's approve or revoke of P<proposal>-R<revision> on carrier pull, with the ledger entry its comment would leave.
func (w *cjJobWorld) consent(action contract.ConsentAction, comment int64, proposal, revision, pull int) contract.ConsentOutcome {
	w.t.Helper()
	operation := contract.OperationID(fmt.Sprintf("consent:github-comment:%d", comment))
	command := bootMust(contract.NewCommand(contract.CommandInput{OperationID: operation, SourceCommandID: contract.SourceCommandID(fmt.Sprintf("github-comment:%d", comment)),
		Actor: bootOwnerPrincipal(), Action: action, Order: contract.CommandOrder(comment), Carrier: carrierID(bootRepoID, pull),
		Reference: contract.ProposalReference{ProjectID: w.project(), SuiteID: "default", ProposalID: proposalID(proposal), RevisionID: contract.ProposalRevisionID(fmt.Sprintf("R%d", revision))}}))
	response, err := governance.ProcessConsent(w.t.Context(), w.mem, governance.ConsentRequest{Command: command})
	if err != nil {
		w.t.Fatal(err)
	}
	w.entry(pull, comment, bootOwnerID, commands.Processed, operation)
	return response.Receipt.Result.Outcome()
}

// entry records the ledger entry of a comment on pull, created now.
func (w *cjJobWorld) entry(pull int, comment, author int64, disposition commands.Disposition, operation contract.OperationID) {
	w.t.Helper()
	e := commands.Entry{RepositoryID: bootRepoID, CommentID: comment, PullNumber: pull, AuthorID: author, CreatedAt: w.clock.Add(30 * time.Second),
		BodyDigest: artifact.Hash([]byte("body")), Disposition: disposition, Operation: operation, DecidedAt: w.clock.Add(31 * time.Second)}
	if err := w.cmds.Record(w.t.Context(), e, nil); err != nil {
		w.t.Fatal(err)
	}
}

// protectedPull opens pull 5 at head 2, whose protected tests differ from the canonical, and checks it once.
func (w *cjJobWorld) protectedPull() {
	w.t.Helper()
	w.tree(cjHead2, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 1)
}

func TestChecksPublishNothingWithoutAnEnrolledOwner(t *testing.T) {
	w := newCJWorld(t, false, notEnrolled)
	w.sync(map[int]string{4: bootHead})

	w.mustCheck()

	w.wantNone(4)
}

func TestChecksIgnoreRepositoriesThatAreNotConfigured(t *testing.T) {
	w := newCJWorld(t, false, withRepos(bootOtherRepo))
	w.sync(map[int]string{4: bootHead})

	w.mustCheck()

	w.wantNone(4)
}

func TestChecksPublishSuccessForAnUnchangedHeadAndDoNotReadItsTreeAgain(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})

	w.mustCheck()

	record := w.want(4, checks.Success, "unchanged", 1)
	if record.Change != "unchanged" || record.Reference != "" {
		t.Fatalf("record %+v, want the unchanged classification and no reference", record)
	}
	reads := w.reader.reads[bootHead]
	w.mustCheck()
	w.mustCheck()
	if w.reader.reads[bootHead] != reads {
		t.Fatalf("the tree of an unchanged head was read again: %d reads after %d", w.reader.reads[bootHead], reads)
	}
	w.want(4, checks.Success, "unchanged", 1)
	if keys, _ := w.memory.OutboxKeys(t.Context()); !slices.Equal(keys, []string{"check:1001:4:1"}) {
		t.Fatalf("outbox %v, want only generation 1", keys)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.ledger.messages[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["version"] != "V1" || payload["tier"] != "isolated" || payload["headSha"] != bootHead || payload["state"] != "success" || payload["reason"] != "unchanged" {
		t.Fatalf("payload %v", payload)
	}
}

func TestChecksKeepAnUnchangedResultWithoutReadingTheTreeWhenOnlyTheSuiteMoved(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	reads := w.reader.reads[bootHead]

	w.protectedPull() // pull 5 writes a proposal and an assessment: the Suite revision moves
	w.mustCheck()

	if w.reader.reads[bootHead] != reads {
		t.Fatalf("the tree of the unchanged head was read again after an unrelated Suite write: %d reads after %d", w.reader.reads[bootHead], reads)
	}
	w.want(4, checks.Success, "unchanged", 1)
}

func TestChecksFollowAProtectedChangeThroughApprovalAndRevocation(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	record, _ := w.record(5)
	if record.Reference != "P2-R1" || record.Change == "" || record.Change == "unchanged" {
		t.Fatalf("record %+v, want the reference P2-R1 and a protected change", record)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.ledger.messages[len(w.ledger.messages)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reference"] != "P2-R1" || payload["reason"] != "approval-missing" {
		t.Fatalf("payload %v", payload)
	}

	if outcome := w.consent(contract.ApproveConsent, 201, 2, 1, 5); outcome != contract.ConsentApproved {
		t.Fatalf("approval outcome %v", outcome)
	}
	w.mustCheck()
	w.want(5, checks.Success, "ready", 2)

	w.mustCheck() // nothing moved
	w.want(5, checks.Success, "ready", 2)

	if outcome := w.consent(contract.RevokeConsent, 202, 2, 1, 5); outcome != contract.ConsentRevoked {
		t.Fatalf("revocation outcome %v", outcome)
	}
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 3)
}

func TestChecksKeepConsentAcrossAnImplementationOnlyPush(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()
	w.want(5, checks.Success, "ready", 2)

	files := bootFiles("v2")
	files["src/main.go"] = "package main // changed\n"
	w.tree(cjHead3, files)
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()

	record := w.want(5, checks.Success, "ready", 3) // a new generation: the head changed
	if record.HeadSHA != cjHead3 || record.Reference != "P2-R1" {
		t.Fatalf("record %+v, want head 3 under the same revision", record)
	}
}

func TestChecksAskForANewApprovalAfterAProtectedPush(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.mustCheck()
	w.want(5, checks.Success, "ready", 2)

	w.tree(cjHead4, bootFiles("v3"))
	w.sync(map[int]string{4: bootHead, 5: cjHead4})
	w.mustCheck()

	record := w.want(5, checks.InProgress, "approval-missing", 3)
	if record.HeadSHA != cjHead4 || record.Reference != "P2-R2" {
		t.Fatalf("record %+v, want head 4 under the new revision P2-R2", record)
	}
}

func TestChecksChangeTheHeadOfAnUnchangedPullWithANewGeneration(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	files := bootFiles("v1")
	files["src/main.go"] = "package main // changed\n"
	w.tree(cjHead3, files)

	w.sync(map[int]string{4: cjHead3})
	w.mustCheck()

	if record := w.want(4, checks.Success, "unchanged", 2); record.HeadSHA != cjHead3 {
		t.Fatalf("head %s, want head 3", record.HeadSHA)
	}
}

// Only a wrong confirmation code fails the check (D-COMMANDS, "A wrong command has no effect").
func TestChecksFailAfterAWrongCodeUntilTheNextStateChange(t *testing.T) {
	t.Run("wrong codes", func(t *testing.T) {
		for _, disposition := range []commands.Disposition{commands.InvalidCode, commands.ReplayedCode} {
			t.Run(string(disposition), func(t *testing.T) {
				w := newCJWorld(t, true)
				w.protectedPull()
				w.entry(5, 301, bootOwnerID, disposition, "")

				w.mustCheck()

				record := w.want(5, checks.Failure, "owner-command-rejected", 2)
				if record.CommandID != 301 || record.Reference != "P2-R1" {
					t.Fatalf("record %+v, want command 301 and the reference kept", record)
				}
			})
		}
	})
	t.Run("cleared by the next processed valid command", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.protectedPull()
		w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")
		w.mustCheck()
		w.want(5, checks.Failure, "owner-command-rejected", 2)
		w.mustCheck() // still failing, one generation
		w.want(5, checks.Failure, "owner-command-rejected", 2)

		w.consent(contract.ApproveConsent, 302, 2, 1, 5)
		w.mustCheck()

		w.want(5, checks.Success, "ready", 3)
	})
	t.Run("cleared by a new head", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.protectedPull()
		w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")
		w.mustCheck()
		w.want(5, checks.Failure, "owner-command-rejected", 2)

		w.tree(cjHead4, bootFiles("v3"))
		w.sync(map[int]string{4: bootHead, 5: cjHead4})
		w.mustCheck()

		w.want(5, checks.InProgress, "approval-missing", 3)
	})
	t.Run("a command from before the head does not count", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.tree(cjHead2, bootFiles("v2"))
		w.sync(map[int]string{4: bootHead, 5: cjHead2})
		w.mustCheck() // the head is first recorded now
		old := commands.Entry{RepositoryID: bootRepoID, CommentID: 300, PullNumber: 5, AuthorID: bootOwnerID, CreatedAt: bootTime,
			BodyDigest: artifact.Hash([]byte("old")), Disposition: commands.InvalidCode, DecidedAt: bootTime}
		if err := w.cmds.Record(t.Context(), old, nil); err != nil {
			t.Fatal(err)
		}

		w.mustCheck()

		w.want(5, checks.InProgress, "approval-missing", 1)
	})
}

// A wrong command that is no wrong code changes nothing: the check of a green or an in-progress pull stays as it was, and a failure
// that a wrong code set neither moves nor clears. The dispositions are those the commands job records for a malformed `suggest please`,
// an `approve` without a code, a command edited before it was first seen, a proposal that does not exist and a Suite that does not exist.
func TestChecksLeaveTheCheckAloneAfterAWrongCommandThatIsNoWrongCode(t *testing.T) {
	unknownRevision := func(w *cjJobWorld, pull int, comment int64) {
		if outcome := w.consent(contract.ApproveConsent, comment, 2, 9, pull); outcome != contract.ConsentRejected { // P2-R9 does not exist
			t.Fatalf("outcome %v, want rejected", outcome)
		}
	}
	wrong := map[string]func(w *cjJobWorld, pull int, comment int64){
		"malformed": func(w *cjJobWorld, pull int, comment int64) {
			w.entry(pull, comment, bootOwnerID, commands.Malformed, "")
		},
		"edited first": func(w *cjJobWorld, pull int, comment int64) {
			w.entry(pull, comment, bootOwnerID, commands.EditedFirst, "")
		},
		"unknown proposal": func(w *cjJobWorld, pull int, comment int64) {
			w.entry(pull, comment, bootOwnerID, commands.UnknownProposal, "")
		},
		"no suite": func(w *cjJobWorld, pull int, comment int64) {
			w.entry(pull, comment, bootOwnerID, commands.NoSuite, "")
		},
		"unknown revision": unknownRevision,
	}
	for name, command := range wrong {
		t.Run(name+" leaves a green and an in-progress check alone", func(t *testing.T) {
			w := newCJWorld(t, true)
			w.protectedPull()
			w.want(4, checks.Success, "unchanged", 1)

			command(w, 4, 301)
			command(w, 5, 302)
			w.mustCheck()

			w.want(4, checks.Success, "unchanged", 1)
			w.want(5, checks.InProgress, "approval-missing", 1)
		})
		t.Run(name+" after a wrong code keeps the failure until a valid approve", func(t *testing.T) {
			w := newCJWorld(t, true)
			w.protectedPull()
			w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")
			w.mustCheck()
			w.want(5, checks.Failure, "owner-command-rejected", 2)

			command(w, 5, 302)
			w.mustCheck()
			w.want(5, checks.Failure, "owner-command-rejected", 2)

			w.consent(contract.ApproveConsent, 303, 2, 1, 5)
			w.mustCheck()
			w.want(5, checks.Success, "ready", 3)
		})
	}
}

// The walk back from a processed command whose consent result was rejected stops at the head's window: a wrong code posted for an earlier head
// does not fail the new one.
func TestChecksWalkBackFromARejectedConsentOnlyWithinTheHeadsWindow(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.entry(5, 301, bootOwnerID, commands.InvalidCode, "")
	w.mustCheck()
	w.want(5, checks.Failure, "owner-command-rejected", 2)
	w.tree(cjHead4, bootFiles("v3"))
	w.sync(map[int]string{4: bootHead, 5: cjHead4})
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 3)

	if outcome := w.consent(contract.ApproveConsent, 302, 2, 9, 5); outcome != contract.ConsentRejected { // P2-R9 does not exist
		t.Fatalf("outcome %v, want rejected", outcome)
	}
	w.mustCheck()

	w.want(5, checks.InProgress, "approval-missing", 3)
}

func TestChecksIgnoreCommandsOfOtherAccountsAndOrdinaryOwnerComments(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.entry(5, 301, cjOtherUser, commands.InvalidCode, "")
	w.entry(5, 302, cjOtherUser, commands.IgnoredAuthor, "")
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 1)

	w.entry(5, 303, bootOwnerID, commands.InvalidCode, "")
	w.mustCheck()
	w.want(5, checks.Failure, "owner-command-rejected", 2)

	w.entry(5, 304, bootOwnerID, commands.NotCommand, "") // an ordinary comment does not clear the failure
	w.mustCheck()
	w.want(5, checks.Failure, "owner-command-rejected", 2)
}

func TestChecksMapTheTreeProblemsOfAHead(t *testing.T) {
	t.Run("a truncated tree", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%w: %w", inventory.ErrInvalidTree, github.ErrTreeTruncated)
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.want(5, checks.InProgress, "source-unavailable", 1)
	})
	t.Run("an invalid declaration", func(t *testing.T) {
		w := newCJWorld(t, true)
		files := bootFiles("v2")
		files[".suiteward.yml"] = "version: 1\nunknown: [\n"
		w.tree(cjHead2, files)
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.want(5, checks.Failure, "invalid-source", 1)
		w.mustCheck()
		w.want(5, checks.Failure, "invalid-source", 1)
	})
	t.Run("an unsafe entry", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%w: a symlink", inventory.ErrUnsafeEntry)
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.want(5, checks.Failure, "invalid-source", 1)
	})
}

func TestChecksKeepTheRecordOnATransientReadAndNeverLeaveANewHeadWithoutACheck(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.consent(contract.RevokeConsent, 201, 2, 1, 5) // moves the Suite so the next run reads the tree again
	w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)

	if err := w.runChecks(); err != nil {
		t.Fatalf("a failed GitHub read must not fail the run: %v", err)
	}

	w.want(5, checks.InProgress, "approval-missing", 1) // the record is kept
	if got := w.problems.lookup(bootRepo); got != "read-failed" {
		t.Fatalf("problem %q, want read-failed", got)
	}

	w.reader.repos[bootRepo].treeErr[cjHead5] = errors.New(bootSecret)
	w.sync(map[int]string{4: bootHead, 5: cjHead5})
	w.mustCheck()

	record := w.want(5, checks.InProgress, "assessment-pending", 2) // the new head has a check at once
	if record.HeadSHA != cjHead5 {
		t.Fatalf("head %s, want head 5", record.HeadSHA)
	}
	if strings.Contains(w.logs.String(), bootSecret) {
		t.Fatalf("a log line carries what GitHub sent: %s", w.logs)
	}

	delete(w.reader.repos[bootRepo].treeErr, cjHead5) // GitHub answers again
	w.tree(cjHead5, bootFiles("v2"))
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 3)
	if got := w.problems.lookup(bootRepo); got != "" {
		t.Fatalf("problem %q after a clean run, want none", got)
	}
}

// An upgrade can leave a stored check of a reason the renderer no longer knows (bootstrap-pending): a failed read must not carry it
// over to a message that cannot be rendered.
func TestChecksNeverKeepAStoredReasonThatNoLongerExistsOnATransientRead(t *testing.T) {
	w := newCJWorld(t, true)
	w.tree(cjHead2, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	stored := checks.Result{RepositoryID: bootRepoID, PullNumber: 5, HeadSHA: cjHead2, State: checks.InProgress, Reason: "bootstrap-pending"}
	_, _, err := w.memory.Publish(t.Context(), stored, w.clock, func(generation int64) (governance.OutboxMessage, error) {
		stored.Reason = reasonAssessmentPending // the message of the old record only needs to be a valid one
		return newCheckMessage(bootRepo, stored, owner.TierIsolated, "", generation)
	})
	if err != nil {
		t.Fatal(err)
	}
	w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)

	if err := w.runChecks(); err != nil {
		t.Fatalf("a stored reason that no longer exists must not fail the run: %v", err)
	}

	w.want(5, checks.InProgress, "assessment-pending", 2)
}

func TestChecksGiveANewHeadWhoseFirstReadFailedAnAssessmentPendingCheck(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.repos[bootRepo].treeErr[cjHead2] = errors.New(bootSecret)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})

	if err := w.runChecks(); err != nil {
		t.Fatal(err)
	}

	w.want(5, checks.InProgress, "assessment-pending", 1)
	w.want(4, checks.Success, "unchanged", 1)
}

func TestChecksKeepTheRecordWhenTheCanonicalMovedDuringTheAssessment(t *testing.T) {
	w := newCJWorld(t, true)
	w.protectedPull()
	w.consent(contract.ApproveConsent, 201, 2, 1, 5)
	w.check.suites = &movedSuites{UnitOfWork: w.suites, err: governance.ErrCanonicalMoved}

	if err := w.runChecks(); err != nil {
		t.Fatalf("a moved canonical is retried, not a failure: %v", err)
	}

	w.want(5, checks.InProgress, "approval-missing", 1)
	w.check.suites = w.suites
	w.mustCheck()
	w.want(5, checks.Success, "ready", 2)
}

// movedSuites fails the writes of AssessHead with err (ErrCanonicalMoved when the
// canonical moved after it read the tree, or another ErrConflict); reads pass.
type movedSuites struct {
	governance.UnitOfWork
	err error
}

func (s *movedSuites) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	return s.UnitOfWork.Do(ctx, project, suite, func(ctx context.Context, tx governance.Tx) error {
		return fn(ctx, &movedTx{Tx: tx, err: s.err})
	})
}

type movedTx struct {
	governance.Tx
	err error
}

func (t *movedTx) AppendProposalRevision(context.Context, governance.ProposalWrite) error {
	return t.err
}

func (t *movedTx) RecordAssessment(context.Context, governance.AssessmentWrite) error {
	return t.err
}

func TestChecksIgnoreClosedPulls(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead, 6: cjHead2})
	w.sync(map[int]string{4: bootHead}) // pull 6 closed

	w.mustCheck()

	w.wantNone(6)
	w.want(4, checks.Success, "unchanged", 1)
}

func TestChecksRepublishALostMessageWithTheNextGeneration(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	if err := w.memory.FailMessage(t.Context(), "check:1001:4:1"); err != nil {
		t.Fatal(err)
	}

	w.mustCheck()

	w.want(4, checks.Success, "unchanged", 2)
	w.mustCheck()
	w.want(4, checks.Success, "unchanged", 2)
}

func TestChecksFailTheRunOnAStoreErrorAfterEveryRepositoryAndShowItInStatus(t *testing.T) {
	w := newCJWorld(t, false, withRepos(bootRepo, bootOtherRepo))
	w.check.suites = &erroringSuites{UnitOfWork: w.suites, project: w.project(), err: errBootStore}
	w.sync(map[int]string{4: bootHead})
	other := githubsync.Cycle{Repo: bootOtherRepo, InstallationID: 1, At: bootTime.Add(time.Hour), StartedAt: bootTime.Add(time.Hour), Requests: 1, Comments: map[int][]githubsync.ObservedComment{3: nil},
		OpenPulls: []githubsync.ObservedPull{{Number: 3, HeadSHA: bootHead, HeadRef: "feature", BaseRef: "main", BaseSHA: bootHead, UpdatedAt: bootTime}}}
	if _, err := w.pulls.RecordCycle(t.Context(), other); err != nil {
		t.Fatal(err)
	}

	err := w.runChecks()

	if !errors.Is(err, errBootStore) {
		t.Fatalf("run error %v, want the store error", err)
	}
	if record, found, _ := w.memory.Current(t.Context(), bootOtherRepoID, 3); !found || record.Reason != "bootstrap-awaiting" {
		t.Fatalf("the other repository was not tried: %+v, found %v", record, found)
	}
	if got := w.problems.lookup(bootRepo); got != "failed" {
		t.Fatalf("problem %q, want failed", got)
	}
	status := &githubStatus{repos: []string{bootRepo, bootOtherRepo}, store: w.pulls, ids: &repositoryIDs{}, problems: &bootstrapProblems{}, checks: w.problems, merges: &bootstrapProblems{}, integrity: noVerdicts()}
	entries, err := status.report(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Checks != "failed" || entries[1].Checks != "" {
		t.Fatalf("status checks %q and %q, want failed and none", entries[0].Checks, entries[1].Checks)
	}
	encoded, _ := json.Marshal(entries[0])
	if !strings.Contains(string(encoded), `"checks":"failed"`) {
		t.Fatalf("status entry %s", encoded)
	}

	w.check.suites = w.suites
	w.mustCheck()
	if got := w.problems.lookup(bootRepo); got != "" {
		t.Fatalf("problem %q after a clean run, want none", got)
	}
}

func TestChecksNeverLogOrReturnWhatGitHubSent(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.repos[bootRepo].treeErr[cjHead2] = fmt.Errorf("%s: %w", bootSecret, inventory.ErrUnsafeEntry)
	w.reader.repos[bootRepo].treeErr[cjHead3] = errors.New(bootSecret)
	w.sync(map[int]string{4: bootHead, 5: cjHead2, 6: cjHead3})

	err := w.runChecks()

	if err != nil && strings.Contains(err.Error(), bootSecret) {
		t.Fatalf("the run error carries what GitHub sent: %v", err)
	}
	if strings.Contains(w.logs.String(), bootSecret) {
		t.Fatalf("a log line carries what GitHub sent: %s", w.logs)
	}
	for _, m := range w.ledger.messages {
		if strings.Contains(string(m.Payload), bootSecret) {
			t.Fatalf("a message carries what GitHub sent: %s", m.Payload)
		}
	}
}

// TestTheChecksJobIsTheOnlyWriterOfCheckStates scans the production code: nothing but the job calls Publish of
// the check ledger (D-CHECKS: a single writer).
func TestTheChecksJobIsTheOnlyWriterOfCheckStates(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	var callers []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(filepath.ToSlash(path), "/checkstest/") { // the in-memory ledger and its conformance suite are test support
			return err
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Publish" {
					callers = append(callers, filepath.ToSlash(path))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) == 0 {
		t.Fatal("no call of Publish found: the job is missing or the scan is wrong")
	}
	for _, caller := range callers {
		if !strings.HasSuffix(caller, "internal/app/checksjob.go") {
			t.Fatalf("%s calls Publish; only the checks job may write check states", caller)
		}
	}
}

func TestChecksDoNotFailOnACommandThatIsNoRejection(t *testing.T) {
	t.Run("no-suite", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.protectedPull()
		w.entry(5, 301, bootOwnerID, commands.NoSuite, "")

		w.mustCheck()

		w.want(5, checks.InProgress, "approval-missing", 1)
	})
	t.Run("a processed revoke with no active approval", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.protectedPull()
		if outcome := w.consent(contract.RevokeConsent, 301, 2, 1, 5); outcome != contract.ConsentNoActiveApproval {
			t.Fatalf("outcome %v, want no active approval", outcome)
		}

		w.mustCheck()

		w.want(5, checks.InProgress, "approval-missing", 1)
	})
}

func TestChecksFailTheStepOfAHeadWhoseAssessmentConflictedAndContinue(t *testing.T) {
	w := newCJWorld(t, true)
	w.tree(cjHead2, bootFiles("v2"))
	// the conflicting pull request comes before the healthy one
	w.sync(map[int]string{3: cjHead2, 4: bootHead})
	w.check.suites = &movedSuites{UnitOfWork: w.suites, err: governance.ErrConflict} // deterministic, not ErrCanonicalMoved

	err := w.runChecks()

	if !errors.Is(err, governance.ErrConflict) {
		t.Fatalf("run error %v, want the conflict: it is not retried", err)
	}
	w.wantNone(3)
	w.want(4, checks.Success, "unchanged", 1) // the later pull request was still decided
	if got := w.problems.lookup(bootRepo); got != "failed" {
		t.Fatalf("problem %q, want failed", got)
	}
}

func TestChecksRetryAHeadWhoseCanonicalMovedOnANewHead(t *testing.T) {
	w := newCJWorld(t, true)
	w.tree(cjHead2, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.check.suites = &movedSuites{UnitOfWork: w.suites, err: governance.ErrCanonicalMoved}

	if err := w.runChecks(); err != nil {
		t.Fatalf("a moved canonical is retried, not a failure: %v", err)
	}

	w.want(5, checks.InProgress, "assessment-pending", 1)
	if got := w.problems.lookup(bootRepo); got != "conflict" {
		t.Fatalf("problem %q, want conflict", got)
	}
}

// The binding of a head assessment is built from the head's own inventory, so AssessHead can neither say that the
// head failed its integrity check nor that its assessment does not match: such an answer is an impossible state.
func TestChecksTreatAnImpossibleHeadAssessmentAsAFailedStepNeverAPublishedState(t *testing.T) {
	reference := contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "default", ProposalID: "P2", RevisionID: "R1"}
	for _, reason := range []contract.PromotionReason{contract.PromotionReasonIntegrityNotPassed, contract.PromotionReasonAssessmentMismatch} {
		t.Run(fmt.Sprint(reason), func(t *testing.T) {
			j := &checksJob{}
			head := governance.HeadResult{Change: inventory.ProtectedChange, Reference: &reference, Reason: reason, Canonical: "V1", Revision: 3}

			result, err := j.assessed(head, checks.Result{RepositoryID: bootRepoID, PullNumber: 5, HeadSHA: cjHead2})

			if err == nil {
				t.Fatalf("the head assessment answering %v published %s/%s, want an error", reason, result.State, result.Reason)
			}
		})
	}
}
