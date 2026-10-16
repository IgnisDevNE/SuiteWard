package app

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance/governancetest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

const (
	cjRepo1, cjID1 = "acme/widgets", int64(1001)
	cjRepo2, cjID2 = "acme/gadgets", int64(1002)
	cjOwnerID      = int64(7)
	cjOtherID      = int64(8)
	cjBotID        = int64(99)
	cjLogin        = "octo-owner"
	cjPull         = 7
	cjMarker       = "SECRET-COMMENT-TEXT"
	cjHead         = "3333333333333333333333333333333333333333" // the head of every observed pull request
	cjBranch       = "feature"                                  // and its branch
)

var (
	// cjOrigin is the enrollment time, a multiple of the 30 s TOTP period.
	cjOrigin = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	// cjSync is when every repository's last successful cycle finished: comments up to cjSync-90 s are ready.
	cjSync     = cjOrigin.Add(time.Hour)
	cjSecret   = bytes.Repeat([]byte{3}, 20)
	cjRepoName = map[int64]string{cjID1: cjRepo1, cjID2: cjRepo2}

	errCjStore = errors.New("the store failed")
	errCjCrash = errors.New("the process crashed")
)

func cjMust[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// cjOwners fails or crashes ConsumeStep on demand and remembers the sources it was asked to consume.
type cjOwners struct {
	owner.Store
	consumeErr func(contract.SourceCommandID) error // fails before the store is reached
	crashAfter bool                                 // the next successful ConsumeStep commits and then reports a crash
	consumed   []contract.SourceCommandID
}

func (o *cjOwners) ConsumeStep(ctx context.Context, account, step int64, at time.Time, source contract.SourceCommandID) error {
	o.consumed = append(o.consumed, source)
	if o.consumeErr != nil {
		if err := o.consumeErr(source); err != nil {
			return err
		}
	}
	if err := o.Store.ConsumeStep(ctx, account, step, at, source); err != nil {
		return err
	}
	if o.crashAfter {
		o.crashAfter = false
		return errCjCrash
	}
	return nil
}

type cjRecord struct {
	entry commands.Entry
	ack   *governance.OutboxMessage
}

// cjLedger remembers every Record that committed, in order, and can fail or crash one.
type cjLedger struct {
	*commandstest.Memory
	pendingErr   error
	pendingCalls int
	recordErr    error                // fails every Record before the store is reached
	crashRecord  commands.Disposition // the next Record of this disposition commits and then reports a crash
	records      []cjRecord
}

func (l *cjLedger) Pending(ctx context.Context, horizon time.Time, limit int) ([]commands.Candidate, error) {
	l.pendingCalls++
	if l.pendingErr != nil {
		return nil, l.pendingErr
	}
	return l.Memory.Pending(ctx, horizon, limit)
}

func (l *cjLedger) Record(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage) error {
	if l.recordErr != nil {
		return l.recordErr
	}
	if err := l.Memory.Record(ctx, e, ack); err != nil {
		return err
	}
	l.records = append(l.records, cjRecord{e, ack})
	if l.crashRecord != "" && l.crashRecord == e.Disposition {
		l.crashRecord = ""
		return errCjCrash
	}
	return nil
}

type cjComment struct {
	pull             int
	id, author       int64
	created, updated time.Time
	body             string
}

type cjWorld struct {
	t       *testing.T
	owners  *cjOwners
	obs     *githubsynctest.Memory
	ledger  *cjLedger
	mem     *governancetest.Memory
	suites  *probeSuites
	logs    *bytes.Buffer
	job     *commandsJob
	listed  map[int64][]cjComment
	botErr  error
	botCall int

	reader   *fakeBootReader  // GitHub, as /suiteward propose reads it
	content  *fakeBootContent // the files a proposed baseline stored
	headRepo string           // the head repository the observed pulls name; "" is not observed
	baseRef  string           // the base branch the observed pulls name; "" is main
}

func newCjWorld(t *testing.T) *cjWorld {
	t.Helper()
	return newCjWorldWithTiers(t, owner.TierIsolated, owner.TierIsolated)
}

// newCjWorldWithTiers is newCjWorld for an owner enrolled at the tier enrolled, on an installation configured for configured.
func newCjWorldWithTiers(t *testing.T, enrolledTier, configured string) *cjWorld {
	t.Helper()
	enrolled := ownertest.NewMemory()
	projects := []owner.ProjectPolicy{{RepositoryID: cjID1, Repo: cjRepo1, TestCheck: "ci"}, {RepositoryID: cjID2, Repo: cjRepo2, TestCheck: "ci"}}
	if err := enrolled.Enroll(t.Context(), owner.Enrollment{Owner: owner.Owner{AccountID: cjOwnerID, Login: cjLogin}, Secret: cjSecret, Projects: projects,
		Step: 1, Tier: enrolledTier, EnrolledAt: cjOrigin}); err != nil {
		t.Fatal(err)
	}
	w := &cjWorld{t: t, owners: &cjOwners{Store: enrolled}, obs: githubsynctest.NewMemory(), mem: governancetest.NewMemory(), logs: &bytes.Buffer{}, listed: map[int64][]cjComment{}}
	w.ledger = &cjLedger{Memory: commandstest.NewMemory(enrolled, w.obs)}
	w.suites = &probeSuites{UnitOfWork: w.mem}
	w.reader = &fakeBootReader{repos: map[string]*bootRepository{cjRepo1: newBootRepository(cjRepo1, cjID1), cjRepo2: newBootRepository(cjRepo2, cjID2)}, calls: map[string]int{}}
	w.content = &fakeBootContent{stored: map[string]bool{}}
	w.job = &commandsJob{ledger: w.ledger, owners: w.owners, suites: w.suites, syncs: w.obs, repos: []string{cjRepo1, cjRepo2}, now: func() time.Time { return cjSync.Add(time.Hour) },
		reader: w.reader, content: w.content, pulls: w.obs, tier: configured,
		bot: newBotAccount(func(context.Context) (int64, error) {
			w.botCall++
			return cjBotID, w.botErr
		}),
		logger: slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	w.seed(cjID1)
	return w
}

// seed creates the default Suite of the repository's project with proposal P1, whose revisions R1 and R2 are carried by pull request 7.
func (w *cjWorld) seed(repositoryID int64) {
	w.t.Helper()
	project := owner.ProjectID(repositoryID)
	protected := cjMust(contract.NewProtectedContract(
		cjMust(artifact.NewManifest([]artifact.Entry{{Path: "tests/a_test.go", Content: artifact.Hash([]byte("v1"))}})),
		artifact.Hash([]byte("tests/**")), map[string]string{"runner": "v1"}))
	var proposal contract.Proposal
	for _, revisionID := range []contract.ProposalRevisionID{"R1", "R2"} {
		reference := contract.ProposalReference{ProjectID: project, SuiteID: "default", ProposalID: "P1", RevisionID: revisionID}
		binding := cjMust(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, ExpectedCanonical: "",
			Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "setup-1", CoveredInputs: protected.CoveredInputs()}))
		revision := cjMust(contract.NewProposalRevision(binding, "origin", contract.OriginBranch, carrierID(repositoryID, cjPull)))
		if proposal.IsZero() {
			proposal = cjMust(contract.NewProposal(revision))
		} else {
			proposal = cjMust(proposal.Revise(revision))
		}
	}
	principal := cjMust(contract.NewPrincipal(owner.PrincipalID(cjOwnerID), contract.Human))
	policy := cjMust(contract.NewPolicy(project, "setup-1", principal))
	canonical := cjMust(contract.NewCanonicalSnapshot(cjMust(contract.NewSuite(project, "default", "", 7)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	seed := governance.Seed{Suite: governance.SuiteState{Canonical: canonical, Policy: policy, Target: "branch:main"}, Proposals: []governance.SeedProposal{{Proposal: proposal}}}
	if err := w.mem.Seed(w.t.Context(), seed); err != nil {
		w.t.Fatal(err)
	}
}

// observe records one successful cycle of the repository at the time at, listing every comment given so far.
func (w *cjWorld) observe(repositoryID int64, at time.Time, comments ...cjComment) {
	w.t.Helper()
	w.listed[repositoryID] = append(w.listed[repositoryID], comments...)
	cycle := githubsync.Cycle{Repo: cjRepoName[repositoryID], InstallationID: 1, At: at, StartedAt: at, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
	for _, c := range w.listed[repositoryID] {
		if _, listed := cycle.Comments[c.pull]; !listed {
			cycle.OpenPulls = append(cycle.OpenPulls, githubsync.ObservedPull{Number: c.pull, HeadSHA: cjHead, HeadRef: cjBranch, HeadRepo: w.headRepo, BaseRef: cmp.Or(w.baseRef, "main"), BaseSHA: "base", UpdatedAt: at})
		}
		cycle.Comments[c.pull] = append(cycle.Comments[c.pull], githubsync.ObservedComment{CommentID: c.id, AuthorID: c.author, AuthorLogin: "someone",
			CreatedAt: c.created, UpdatedAt: c.updated, Body: c.body})
	}
	if _, err := w.obs.RecordCycle(w.t.Context(), cycle); err != nil {
		w.t.Fatal(err)
	}
}

// closePull records a cycle of the repository that lists no open pull request, so the observed ones are not open any more.
func (w *cjWorld) closePull(repositoryID int64, at time.Time) {
	w.t.Helper()
	if _, err := w.obs.RecordCycle(w.t.Context(), githubsync.Cycle{Repo: cjRepoName[repositoryID], InstallationID: 1, At: at, StartedAt: at, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}); err != nil {
		w.t.Fatal(err)
	}
}

// syncAll records a cycle at cjSync for each repository; comments belong to repository 1 unless listed for repository 2.
func (w *cjWorld) syncAll(repo1 []cjComment, repo2 ...cjComment) {
	w.t.Helper()
	w.observe(cjID1, cjSync, repo1...)
	w.observe(cjID2, cjSync, repo2...)
}

// code is the TOTP code of the step that is `offset` steps from the one holding created.
func cjCode(created time.Time, offset int64) string {
	return cjMust(totp.Code(cjSecret, totp.Step(created)+offset))
}

// command is the body of an owner command: a line of text that must never be echoed, then the command line.
func cjCommand(verb, reference string, created time.Time, offset int64) string {
	return cjMarker + "\n/suiteward " + verb + " " + reference + " " + cjCode(created, offset)
}

// ownerCommand is a first-observed, unedited command comment of the owner.
func ownerCommand(id int64, created time.Time, verb, reference string, offset int64) cjComment {
	return cjComment{pull: cjPull, id: id, author: cjOwnerID, created: created, updated: created, body: cjCommand(verb, reference, created, offset)}
}

func cjAt(minutes int) time.Time { return cjOrigin.Add(time.Duration(minutes) * time.Minute) }

func (w *cjWorld) run() error { _, err := w.job.run(w.t.Context()); return err }

func (w *cjWorld) mustRun() {
	w.t.Helper()
	if err := w.run(); err != nil {
		w.t.Fatalf("run: %v\nlogs: %s", err, w.logs)
	}
}

// recorded returns the ledger entry of the comment, and its acknowledgment.
func (w *cjWorld) recorded(repositoryID, comment int64) (commands.Entry, *governance.OutboxMessage, bool) {
	for _, r := range w.ledger.records {
		if r.entry.RepositoryID == repositoryID && r.entry.CommentID == comment {
			return r.entry, r.ack, true
		}
	}
	return commands.Entry{}, nil, false
}

func (w *cjWorld) requireDisposition(repositoryID, comment int64, want commands.Disposition) (commands.Entry, *governance.OutboxMessage) {
	w.t.Helper()
	entry, ack, found := w.recorded(repositoryID, comment)
	if !found {
		w.t.Fatalf("comment %d has no ledger entry; want %q\nlogs: %s", comment, want, w.logs)
	}
	if entry.Disposition != want {
		w.t.Fatalf("comment %d is %q; want %q", comment, entry.Disposition, want)
	}
	return entry, ack
}

func (w *cjWorld) requireNoEntry(repositoryID, comment int64) {
	w.t.Helper()
	if entry, _, found := w.recorded(repositoryID, comment); found {
		w.t.Fatalf("comment %d was recorded as %q; want it left pending", comment, entry.Disposition)
	}
}

func cjTarget(repositoryID int64) commentTarget {
	return commentTarget{RepositoryID: repositoryID, Repo: cjRepoName[repositoryID], Pull: cjPull}
}

func cjOperation(comment int64) contract.OperationID {
	return contract.OperationID(fmt.Sprintf("consent:github-comment:%d", comment))
}

// requireProcessed requires a processed entry whose acknowledgment went out with ProcessConsent, and returns the receipt.
func (w *cjWorld) requireProcessed(repositoryID, comment int64) governance.ConsentReceipt {
	w.t.Helper()
	entry, ack := w.requireDisposition(repositoryID, comment, commands.Processed)
	if entry.Operation != cjOperation(comment) || ack != nil {
		w.t.Fatalf("processed entry has operation %q and a ledger acknowledgment %v; want %q and none (ProcessConsent wrote it)", entry.Operation, ack, cjOperation(comment))
	}
	want := cjMust(newProcessedAck(cjTarget(repositoryID), comment, cjLogin, owner.ProjectID(repositoryID), "default", cjOperation(comment)))
	found := false
	for _, m := range w.suites.messages {
		found = found || (m.Key == want.Key && m.Kind == want.Kind && bytes.Equal(m.Payload, want.Payload))
	}
	if !found {
		w.t.Fatalf("no processed acknowledgment %q among %d outbox messages", want.Key, len(w.suites.messages))
	}
	var receipt governance.ConsentReceipt
	err := w.mem.Do(w.t.Context(), owner.ProjectID(repositoryID), "default", func(ctx context.Context, tx governance.Tx) error {
		stored, found, err := tx.Receipt(ctx, cjOperation(comment))
		if err != nil || !found {
			return fmt.Errorf("receipt found %v: %v", found, err)
		}
		receipt = *stored.Consent
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if receipt.Result.Command().Order() != contract.CommandOrder(comment) || receipt.Result.Command().Actor().ID() != owner.PrincipalID(cjOwnerID) ||
		receipt.Result.Command().SourceCommandID() != commentSource(comment) || receipt.Result.Command().Carrier() != carrierID(repositoryID, cjPull) {
		w.t.Fatalf("the command given to the domain is %+v", receipt.Result.Command())
	}
	return receipt
}

// requireRejected requires the disposition with the acknowledgment the A6a constructor builds for it.
func (w *cjWorld) requireRejected(repositoryID, comment int64, d commands.Disposition, reason commandtext.Reason, reference string) {
	w.t.Helper()
	_, ack := w.requireDisposition(repositoryID, comment, d)
	want := cjMust(newRejectedAck(cjTarget(repositoryID), comment, cjLogin, d, reason, reference))
	if ack == nil || ack.Key != want.Key || ack.Kind != want.Kind || !bytes.Equal(ack.Payload, want.Payload) {
		w.t.Fatalf("comment %d: acknowledgment %+v; want %+v", comment, ack, want)
	}
}

func (w *cjWorld) requireSilent(repositoryID, comment int64, d commands.Disposition) {
	w.t.Helper()
	if _, ack := w.requireDisposition(repositoryID, comment, d); ack != nil {
		w.t.Fatalf("comment %d (%s) was acknowledged with %+v; want no acknowledgment", comment, d, ack)
	}
}

// logEntries decodes the log lines without their wall-clock time, which could contain a six-digit code by chance.
func (w *cjWorld) logEntries() []map[string]any {
	w.t.Helper()
	var entries []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(w.logs.Bytes()))
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			w.t.Fatalf("log is not JSON: %v", err)
		}
		delete(entry, "time")
		entries = append(entries, entry)
	}
	return entries
}

func (w *cjWorld) requireNothingLeaked(codes ...string) {
	w.t.Helper()
	var texts []string
	for _, entry := range w.logEntries() {
		line, _ := json.Marshal(entry)
		texts = append(texts, string(line))
	}
	for _, r := range w.ledger.records {
		if r.ack != nil {
			texts = append(texts, string(r.ack.Payload))
		}
	}
	for _, m := range w.suites.messages {
		texts = append(texts, string(m.Payload))
	}
	for _, text := range texts {
		for _, secret := range append([]string{cjMarker, "/suiteward"}, codes...) {
			if strings.Contains(text, secret) {
				w.t.Fatalf("%q appears in a log or an acknowledgment: %s", secret, text)
			}
		}
	}
}

func TestCommandsJobRecordsEveryDisposition(t *testing.T) {
	created := cjAt(10)
	edited := created.Add(time.Minute)
	for _, tc := range []struct {
		name    string
		comment cjComment
		check   func(w *cjWorld, id int64)
		steps   int // sources the job asked the owner store to consume
	}{
		{"a comment with no command line", cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: cjMarker},
			func(w *cjWorld, id int64) { w.requireSilent(cjID1, id, commands.NotCommand) }, 0},
		{"an edited comment with no command line is not a command, and is not acknowledged", cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: edited, body: "LGTM " + cjMarker},
			func(w *cjWorld, id int64) { w.requireSilent(cjID1, id, commands.NotCommand) }, 0},
		{"the bot's own comment", cjComment{pull: cjPull, id: 500, author: cjBotID, created: created, updated: created, body: cjCommand("approve", "P1-R2", created, 0)},
			func(w *cjWorld, id int64) { w.requireSilent(cjID1, id, commands.NotCommand) }, 0},
		{"a command created before the enrollment", ownerCommand(500, cjOrigin.Add(-time.Minute), "approve", "P1-R2", 0),
			func(w *cjWorld, id int64) { w.requireSilent(cjID1, id, commands.BeforeEnrollment) }, 0},
		{"a command of another account", cjComment{pull: cjPull, id: 500, author: cjOtherID, created: created, updated: created, body: cjCommand("approve", "P1-R2", created, 0)},
			func(w *cjWorld, id int64) { w.requireSilent(cjID1, id, commands.IgnoredAuthor) }, 0},
		{"a malformed command of another account", cjComment{pull: cjPull, id: 500, author: cjOtherID, created: created, updated: created, body: "/suiteward approve"},
			func(w *cjWorld, id int64) { w.requireSilent(cjID1, id, commands.IgnoredAuthor) }, 0},
		{"an owner command edited before it was first observed", cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: edited, body: cjCommand("approve", "P1-R2", created, 0)},
			func(w *cjWorld, id int64) { w.requireRejected(cjID1, id, commands.EditedFirst, 0, "") }, 0},
		{"an edited malformed command is edited-first, not malformed", cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: edited, body: "/suiteward approve"},
			func(w *cjWorld, id int64) { w.requireRejected(cjID1, id, commands.EditedFirst, 0, "") }, 0},
		{"a malformed command", cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: cjMarker + "\n/suiteward approve P1-R2"},
			func(w *cjWorld, id int64) {
				w.requireRejected(cjID1, id, commands.Malformed, commandtext.MissingCode, "")
			}, 0},
		{"a command with an unknown verb", cjComment{pull: cjPull, id: 500, author: cjOwnerID, created: created, updated: created, body: "/suiteward frobnicate"},
			func(w *cjWorld, id int64) {
				w.requireRejected(cjID1, id, commands.Malformed, commandtext.UnknownVerb, "")
			}, 0},
		{"a code that does not verify", ownerCommand(500, created, "approve", "P1-R2", 5),
			func(w *cjWorld, id int64) { w.requireRejected(cjID1, id, commands.InvalidCode, 0, "P1-R2") }, 0},
		{"an approval", ownerCommand(500, created, "approve", "P1-R2", 0),
			func(w *cjWorld, id int64) {
				if receipt := w.requireProcessed(cjID1, id); receipt.Result.Outcome() != contract.ConsentApproved {
					t.Fatalf("outcome = %v; want approved", receipt.Result.Outcome())
				}
			}, 1},
		{"a revocation with no approval", ownerCommand(500, created, "revoke", "P1-R2", 0),
			func(w *cjWorld, id int64) {
				if receipt := w.requireProcessed(cjID1, id); receipt.Result.Outcome() != contract.ConsentNoActiveApproval {
					t.Fatalf("outcome = %v; want no active approval", receipt.Result.Outcome())
				}
			}, 1},
		{"an unknown revision is processed and rejected by the domain", ownerCommand(500, created, "approve", "P1-R9", 0),
			func(w *cjWorld, id int64) {
				receipt := w.requireProcessed(cjID1, id)
				if receipt.Result.Outcome() != contract.ConsentRejected || receipt.Result.Reason() != contract.ConsentReasonUnknownRevision {
					t.Fatalf("outcome %v, reason %v; want rejected, unknown revision", receipt.Result.Outcome(), receipt.Result.Reason())
				}
			}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.syncAll([]cjComment{tc.comment})
			w.mustRun()
			tc.check(w, tc.comment.id)
			if len(w.owners.consumed) != tc.steps {
				t.Fatalf("the job consumed %d steps; want %d", len(w.owners.consumed), tc.steps)
			}
			w.requireNothingLeaked(cjCode(created, 0), cjCode(created, 5))
			w.requireNoEntryAfterSecondRun(tc.comment.id)
		})
	}
}

// requireNoEntryAfterSecondRun runs again: a decided comment is never decided twice.
func (w *cjWorld) requireNoEntryAfterSecondRun(comment int64) {
	w.t.Helper()
	before, consumed := len(w.ledger.records), len(w.owners.consumed)
	w.mustRun()
	if len(w.ledger.records) != before || len(w.owners.consumed) != consumed {
		w.t.Fatalf("a second run recorded %d more entries and consumed %d more steps for comment %d; want none", len(w.ledger.records)-before, len(w.owners.consumed)-consumed, comment)
	}
}

func TestCommandsJobRejectsACommandForAProjectWithNoSuite(t *testing.T) {
	w := newCjWorld(t) // repository 2 has no Suite
	w.syncAll(nil, ownerCommand(700, cjAt(10), "approve", "P3-R2", 0))
	w.mustRun()
	w.requireRejected(cjID2, 700, commands.NoSuite, 0, "P3-R2")
	if len(w.owners.consumed) != 1 {
		t.Fatalf("consumed %d steps; the step is used before the Suite is looked for", len(w.owners.consumed))
	}
	w.requireNothingLeaked(cjCode(cjAt(10), 0))
}

func TestCommandsJobAcceptsTheCodesOfTheToleranceAndNoOther(t *testing.T) {
	w := newCjWorld(t)
	var comments []cjComment
	for i, offset := range []int64{-2, -1, 0, 1, 2} {
		comments = append(comments, ownerCommand(int64(500+i), cjAt(10+5*i), "approve", "P1-R2", offset))
	}
	w.syncAll(comments)
	w.mustRun()
	for i, want := range []commands.Disposition{commands.InvalidCode, commands.Processed, commands.Processed, commands.Processed, commands.InvalidCode} {
		w.requireDisposition(cjID1, int64(500+i), want)
	}
}

// The offsets of the owner's codes are logged to measure the window before M1.8: a verified code with its distance in
// steps from the comment's step, a rejected one with whether a window of 2 steps would have accepted it. Never the code.
func TestCommandsJobLogsTheOffsetOfEveryCodeAndNeverTheCode(t *testing.T) {
	w := newCjWorld(t)
	offsets := []int64{-3, -2, -1, 0, 1, 2, 3}
	var comments []cjComment
	var codes []string
	for i, offset := range offsets {
		created := cjAt(10 + 5*i)
		comments = append(comments, ownerCommand(int64(500+i), created, "approve", "P1-R2", offset))
		codes = append(codes, cjCode(created, offset))
	}
	w.syncAll(comments)
	w.mustRun()
	logged := map[int64]map[string]any{}
	for _, entry := range w.logEntries() {
		if message, _ := entry["msg"].(string); strings.HasPrefix(message, "github command code") {
			logged[int64(entry["comment"].(float64))] = entry
		}
	}
	for i, offset := range offsets {
		entry := logged[int64(500+i)]
		switch {
		case entry == nil:
			t.Errorf("offset %d: no log line for the code; logs: %s", offset, w.logs)
		case offset >= -1 && offset <= 1: // accepted
			if entry["msg"] != "github command code verified" || entry["offset"] != float64(offset) {
				t.Errorf("offset %d: log %v, want a verified code at that offset", offset, entry)
			}
		case offset >= -2 && offset <= 2: // rejected, but inside the window of 2
			if entry["msg"] != "github command code rejected" || entry["would_match"] != true || entry["offset"] != float64(offset) {
				t.Errorf("offset %d: log %v, want a rejected code that a window of 2 would accept at that offset", offset, entry)
			}
		default:
			if entry["msg"] != "github command code rejected" || entry["would_match"] != false || entry["offset"] != nil {
				t.Errorf("offset %d: log %v, want a rejected code that a window of 2 would not accept", offset, entry)
			}
		}
	}
	w.requireNothingLeaked(append(codes, string(cjSecret))...)
}

func TestCommandsJobBindsACodeToTheFirstCommandThatUsesIt(t *testing.T) {
	w := newCjWorld(t)
	first, second := ownerCommand(500, cjAt(10), "approve", "P1-R2", 0), ownerCommand(501, cjAt(10).Add(5*time.Second), "approve", "P1-R2", 0)
	w.syncAll([]cjComment{second, first})
	w.mustRun()
	w.requireProcessed(cjID1, 500)
	w.requireRejected(cjID1, 501, commands.ReplayedCode, 0, "P1-R2")
}

func TestCommandsJobUsesNoStepForAnEditedCommand(t *testing.T) {
	w := newCjWorld(t)
	edited := ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)
	edited.updated = edited.created.Add(time.Minute)
	next := ownerCommand(501, cjAt(10).Add(5*time.Second), "approve", "P1-R2", 0) // the same step: the edited comment did not use it
	w.syncAll([]cjComment{edited, next})
	w.mustRun()
	w.requireRejected(cjID1, 500, commands.EditedFirst, 0, "")
	w.requireProcessed(cjID1, 501)
}

func TestCommandsJobProcessesInTheOrderOfCreationThenCommentIDAcrossRepositories(t *testing.T) {
	w := newCjWorld(t)
	w.seed(cjID2)
	w.syncAll(
		[]cjComment{ownerCommand(100, cjAt(11), "approve", "P1-R2", 0), ownerCommand(300, cjAt(12), "approve", "P1-R2", 0)},
		ownerCommand(900, cjAt(10), "approve", "P1-R2", 0), ownerCommand(200, cjAt(12), "approve", "P1-R2", 1))
	w.mustRun()
	var order []int64
	for _, r := range w.ledger.records {
		order = append(order, r.entry.CommentID)
	}
	if fmt.Sprint(order) != "[900 100 200 300]" {
		t.Fatalf("recorded in the order %v; want [900 100 200 300]", order)
	}
	w.requireProcessed(cjID2, 900)
	w.requireProcessed(cjID1, 300)
}

func TestCommandsJobWaitsForTheSyncWatermark(t *testing.T) {
	w := newCjWorld(t)
	pending := ownerCommand(500, cjSync.Add(-11*time.Second), "approve", "P1-R2", 0) // 11 s before the earliest start: before the horizon (-10 s)
	notYet := ownerCommand(501, cjSync.Add(-9*time.Second), "approve", "P1-R2", 3)   // 9 s before: not yet past the horizon
	w.syncAll([]cjComment{notYet, pending})
	w.mustRun()
	w.requireProcessed(cjID1, 500)
	w.requireNoEntry(cjID1, 501) // a synchronization that began before this comment may not have seen the owner's own earlier commands
}

func TestCommandsJobUsesTheEarliestSuccessfulCycle(t *testing.T) {
	w := newCjWorld(t)
	late := cjSync.Add(-commandListingMargin).Add(-time.Minute) // before repository 1's horizon, after repository 2's
	w.observe(cjID1, cjSync, ownerCommand(500, late, "approve", "P1-R2", 0))
	w.observe(cjID2, cjSync.Add(-30*time.Minute))
	w.mustRun()
	w.requireNoEntry(cjID1, 500)
}

func TestCommandsJobHorizonIsTheEarliestCycleStartMinusTheListingMargin(t *testing.T) {
	w := newCjWorld(t)
	w.observe(cjID1, cjSync)
	w.observe(cjID2, cjSync.Add(-5*time.Minute))
	enrollment, enrolled, err := w.owners.Current(w.t.Context())
	if err != nil || !enrolled {
		t.Fatalf("enrollment = %v, %v, %v", enrollment, enrolled, err)
	}
	horizon, ready, err := w.job.horizon(w.t.Context(), enrollment)
	if err != nil || !ready {
		t.Fatalf("horizon = %v, ready %v, err %v; want ready", horizon, ready, err)
	}
	if want := cjSync.Add(-5 * time.Minute).Add(-commandListingMargin); !horizon.Equal(want) {
		t.Fatalf("horizon = %v; want the earliest start (%v) minus %v = %v", horizon, cjSync.Add(-5*time.Minute), commandListingMargin, want)
	}
}

func TestCommandsJobHorizonFollowsTheCycleStartNotTheTimeTheCycleEnded(t *testing.T) {
	w := newCjWorld(t)
	started := cjSync
	for _, id := range []int64{cjID1, cjID2} { // At, which is LastSuccess, is a minute after the cycle started on GitHub's clock
		cycle := githubsync.Cycle{Repo: cjRepoName[id], InstallationID: 1, At: started.Add(time.Minute), StartedAt: started, Requests: 1, Comments: map[int][]githubsync.ObservedComment{}}
		if _, err := w.obs.RecordCycle(w.t.Context(), cycle); err != nil {
			t.Fatal(err)
		}
	}
	enrollment, enrolled, err := w.owners.Current(w.t.Context())
	if err != nil || !enrolled {
		t.Fatalf("enrollment = %v, %v, %v", enrollment, enrolled, err)
	}
	horizon, ready, err := w.job.horizon(w.t.Context(), enrollment)
	if err != nil || !ready {
		t.Fatalf("horizon = %v, ready %v, err %v; want ready", horizon, ready, err)
	}
	if want := started.Add(-commandListingMargin); !horizon.Equal(want) {
		t.Fatalf("horizon = %v; want StartedAt (%v) minus %v = %v, not derived from LastSuccess", horizon, started, commandListingMargin, want)
	}
}

func TestCommandsJobWakesChecksAndBootstrapAfterRecordingAnEntry(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)})

	wake, err := w.job.run(t.Context())
	if err != nil {
		t.Fatalf("run: %v\nlogs: %s", err, w.logs)
	}
	if want := (river.Wake{river.ChecksKind, river.BootstrapKind}); !slices.Equal(wake, want) {
		t.Fatalf("a run that recorded an entry woke %v; want %v", wake, want)
	}
}

func TestCommandsJobWakesNothingWhenNothingWasPending(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll(nil)

	if wake, err := w.job.run(t.Context()); err != nil || len(wake) != 0 {
		t.Fatalf("a run with nothing pending = %v, %v; want no wake-ups", wake, err)
	}
}

func TestCommandsJobWakesNothingWhileAnEnrolledRepositoryHasNotSynchronized(t *testing.T) {
	w := newCjWorld(t)
	w.observe(cjID1, cjSync, ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)) // repository 2 has no cycle

	if wake, err := w.job.run(t.Context()); err != nil || len(wake) != 0 {
		t.Fatalf("a run held back by the horizon = %v, %v; want no wake-ups", wake, err)
	}
}

func TestCommandsJobThatFailsStillWakesWhatItRecorded(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{
		{pull: cjPull, id: 500, author: cjOwnerID, created: cjAt(10), updated: cjAt(10), body: "no command"}, // recorded
		{pull: cjPull, id: 501, author: cjOtherID, created: cjAt(11), updated: cjAt(11), body: cjCommand("approve", "P1-R2", cjAt(11), 0)},
	})
	w.ledger.crashRecord = commands.IgnoredAuthor // the second entry commits and then the ledger reports a crash

	wake, err := w.job.run(t.Context())
	if !errors.Is(err, errLedger) {
		t.Fatalf("run = %v; want the ledger failure", err)
	}
	w.requireDisposition(cjID1, 500, commands.NotCommand)
	if want := (river.Wake{river.ChecksKind, river.BootstrapKind}); !slices.Equal(wake, want) {
		t.Fatalf("a failed run woke %v after recording entries; want %v", wake, want)
	}
}

func TestCommandsJobHorizonIsNotReadyWhileAnEnrolledRepositoryHasNoCycleStart(t *testing.T) {
	w := newCjWorld(t)
	w.observe(cjID1, cjSync) // repository 2 has never synchronized: its CycleStartedAt is zero
	enrollment, enrolled, err := w.owners.Current(w.t.Context())
	if err != nil || !enrolled {
		t.Fatalf("enrollment = %v, %v, %v", enrollment, enrolled, err)
	}
	horizon, ready, err := w.job.horizon(w.t.Context(), enrollment)
	if err != nil || ready || !horizon.IsZero() {
		t.Fatalf("horizon = %v, ready %v, err %v; want a zero horizon, not ready", horizon, ready, err)
	}
	if !strings.Contains(w.logs.String(), `"level":"WARN"`) || !strings.Contains(w.logs.String(), "has not synchronized yet") {
		t.Fatalf("no Warn line says an enrolled repository has not synchronized yet; logs: %s", w.logs)
	}
}

func TestCommandsJobDoesNothingWhileARepositoryHasNeverSynchronized(t *testing.T) {
	w := newCjWorld(t)
	w.observe(cjID1, cjSync, ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)) // repository 2 has no successful cycle
	w.mustRun()
	w.requireNoEntry(cjID1, 500)
	if w.ledger.pendingCalls != 0 || w.botCall != 0 {
		t.Fatalf("the run read %d pending lists and the bot %d times without a horizon", w.ledger.pendingCalls, w.botCall)
	}
	if !strings.Contains(w.logs.String(), `"level":"WARN"`) || !strings.Contains(w.logs.String(), cjRepo2) {
		t.Fatalf("no Warn line says which repository blocks the commands; logs: %s", w.logs)
	}
}

func TestCommandsJobStillWaitsForAnEnrolledRepositoryThatIsNotConfiguredAndWarnsOnce(t *testing.T) {
	w := newCjWorld(t)
	w.job.repos = []string{cjRepo1} // repository 2 was removed from GITHUB_REPOSITORIES without re-running setup
	w.observe(cjID1, cjSync, ownerCommand(500, cjAt(40), "approve", "P1-R2", 0))
	w.observe(cjID2, cjSync.Add(-30*time.Minute))
	w.mustRun()
	w.mustRun()
	w.requireNoEntry(cjID1, 500) // fail closed: the stale cycle of the removed repository still holds the watermark back
	warnings := 0
	for _, line := range strings.Split(w.logs.String(), "\n") {
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "not configured") && strings.Contains(line, cjRepo2) {
			warnings++
		}
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "not configured") && strings.Contains(line, cjRepo1) {
			t.Fatalf("the configured repository %s is reported as not configured: %s", cjRepo1, line)
		}
	}
	if warnings != 1 {
		t.Fatalf("%d warnings name the unconfigured repository %s over two runs; want 1; logs: %s", warnings, cjRepo2, w.logs)
	}
}

func TestCommandsJobDoesNothingWithoutAnEnrolledOwner(t *testing.T) {
	w := newCjWorld(t)
	w.job.owners = ownertest.NewMemory()
	w.mustRun()
	if w.ledger.pendingCalls != 0 {
		t.Fatal("the run read the pending comments without an owner")
	}
}

func TestCommandsJobResolvesTheBotOnlyForCandidates(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll(nil)
	w.mustRun()
	if w.botCall != 0 {
		t.Fatalf("resolved the bot %d times with no candidate", w.botCall)
	}
}

func TestCommandsJobStopsWhenTheBotAccountIsUnknown(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)})
	w.botErr = errCjStore
	if err := w.run(); err != nil {
		t.Fatalf("run: %v; an unknown bot account is retried on the next tick, it does not fail the job", err)
	}
	if len(w.ledger.records) != 0 || len(w.owners.consumed) != 0 {
		t.Fatal("a command was decided without knowing which comments are the bot's own")
	}
	w.botErr = nil
	w.mustRun()
	w.requireProcessed(cjID1, 500)
}

func TestCommandsJobStopsAtAFailingCandidateAndRecordsItAsFailedAfterFiveAttempts(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 0), ownerCommand(501, cjAt(11), "approve", "P1-R2", 0)})
	w.owners.consumeErr = func(source contract.SourceCommandID) error {
		if source == commentSource(500) {
			return errCjStore
		}
		return nil
	}
	for attempt := 1; attempt <= 4; attempt++ {
		if err := w.run(); err != nil {
			t.Fatalf("attempt %d: %v; a candidate's store error stops the run, it does not fail the job", attempt, err)
		}
		if len(w.ledger.records) != 0 {
			t.Fatalf("attempt %d recorded %d entries; the later candidate must not go ahead of the failing one", attempt, len(w.ledger.records))
		}
	}
	if len(w.owners.consumed) != 4 {
		t.Fatalf("consumed %v; the later candidate was reached", w.owners.consumed)
	}
	w.mustRun() // the fifth consecutive failure
	w.requireSilent(cjID1, 500, commands.Failed)
	w.requireProcessed(cjID1, 501) // the next candidate goes ahead in the same run
	for _, want := range []string{`"repo":"acme/widgets"`, `"comment":500`} {
		if !strings.Contains(w.logs.String(), want) {
			t.Fatalf("the failure log lacks %s; logs: %s", want, w.logs)
		}
	}
	w.requireNothingLeaked(cjCode(cjAt(10), 0))
}

func TestCommandsJobCountsOnlyConsecutiveFailuresOfOneCandidate(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)})
	failing := true
	w.owners.consumeErr = func(contract.SourceCommandID) error {
		if failing {
			return errCjStore
		}
		return nil
	}
	for range 4 {
		w.mustRun()
	}
	failing = false
	w.mustRun()
	w.requireProcessed(cjID1, 500) // four failures are not five
}

func TestCommandsJobFailsOnlyWhenTheLedgerCannotBeUsed(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		w := newCjWorld(t)
		w.syncAll(nil)
		w.ledger.pendingErr = errCjStore
		if err := w.run(); !errors.Is(err, errCjStore) {
			t.Fatalf("run = %v; want the ledger read error", err)
		}
	})
	t.Run("record", func(t *testing.T) {
		w := newCjWorld(t)
		w.syncAll([]cjComment{{pull: cjPull, id: 500, author: cjOwnerID, created: cjAt(10), updated: cjAt(10), body: "no command"}})
		w.ledger.recordErr = errCjStore
		if err := w.run(); err == nil {
			t.Fatal("run succeeded although the ledger could not be written")
		} else if strings.Contains(err.Error(), "no command") {
			t.Fatalf("the error quotes the comment: %v", err)
		}
	})
}

func TestCommandsJobRepeatsEarlierStepsHarmlesslyAfterACrash(t *testing.T) {
	command := ownerCommand(500, cjAt(10), "approve", "P1-R2", 0)
	for _, tc := range []struct {
		name  string
		crash func(w *cjWorld)
	}{
		{"after the step was consumed", func(w *cjWorld) { w.owners.crashAfter = true }},
		{"after ProcessConsent committed", func(w *cjWorld) { w.suites.crashAt = 1 }},
		{"after the ledger entry was recorded", func(w *cjWorld) { w.ledger.crashRecord = commands.Processed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.syncAll([]cjComment{command})
			tc.crash(w)
			_ = w.run() // the crash may or may not surface as a job error; the next run must repair it
			w.mustRun()
			receipt := w.requireProcessed(cjID1, 500)
			if receipt.Result.Outcome() != contract.ConsentApproved || len(w.suites.messages) != 1 {
				t.Fatalf("outcome %v with %d acknowledgments; want one approval and one acknowledgment", receipt.Result.Outcome(), len(w.suites.messages))
			}
			count := 0
			for _, r := range w.ledger.records {
				if r.entry.CommentID == 500 {
					count++
				}
			}
			if count > 1 {
				t.Fatalf("comment 500 recorded %d times", count)
			}
		})
	}
}

func TestCommandsJobRepeatsARejectedRecordAfterACrash(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P1-R2", 5)})
	w.ledger.crashRecord = commands.InvalidCode
	_ = w.run()
	w.mustRun()
	w.requireRejected(cjID1, 500, commands.InvalidCode, 0, "P1-R2")
	if keys, err := w.ledger.OutboxKeys(t.Context()); err != nil || len(keys) != 1 {
		t.Fatalf("outbox keys %v, %v; want one acknowledgment", keys, err)
	}
}

func TestCommandsJobTreatsAFailedSuiteProbeAsAStoreError(t *testing.T) {
	// An unknown proposal makes ProcessConsent report ErrNotFound; the probe that tells it from a missing Suite then fails
	// (call 1 is ProcessConsent, call 2 the probe). That is a store error to retry, never a no-suite acknowledgment.
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P9-R1", 0)})
	w.suites.failAt = 2
	w.mustRun()
	w.requireNoEntry(cjID1, 500)
}

func TestCommandsJobRejectsAnUnknownProposalOfAnExistingSuite(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P9-R1", 0)})
	w.mustRun()
	w.requireRejected(cjID1, 500, commands.UnknownProposal, 0, "P9-R1")
	if len(w.owners.consumed) != 1 {
		t.Fatalf("consumed %d steps; the code was valid, so its step is used", len(w.owners.consumed))
	}
	if keys, err := w.ledger.OutboxKeys(t.Context()); err != nil || len(keys) != 1 {
		t.Fatalf("outbox keys %v, %v; want one acknowledgment", keys, err)
	}
	w.requireNothingLeaked(cjCode(cjAt(10), 0))
}

func TestCommandsJobRepeatsAnUnknownProposalAfterACrashOnce(t *testing.T) {
	w := newCjWorld(t)
	w.syncAll([]cjComment{ownerCommand(500, cjAt(10), "approve", "P9-R1", 0)})
	w.owners.crashAfter = true
	w.mustRun() // the step is consumed, then the process "crashes"
	w.requireNoEntry(cjID1, 500)
	w.mustRun()
	w.requireRejected(cjID1, 500, commands.UnknownProposal, 0, "P9-R1")
	if keys, err := w.ledger.OutboxKeys(t.Context()); err != nil || len(keys) != 1 {
		t.Fatalf("outbox keys %v, %v; want one acknowledgment", keys, err)
	}
}
