package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance/governancetest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

const (
	cmtRepo       = "acme/widgets"
	cmtRepoID     = int64(1001)
	cmtPull       = 7
	cmtComment    = int64(555)
	cmtOwner      = "octo-owner"
	cmtOwnerID    = int64(7)
	cmtBotID      = int64(99)
	cmtInstance   = "0123456789abcdef0123456789abcdef"
	cmtOtherInst  = "fedcba9876543210fedcba9876543210"
	cmtSecretText = "SECRET-TEXT-GITHUB-SENT"
)

func cmtMust[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func cmtTarget() commentTarget {
	return commentTarget{RepositoryID: cmtRepoID, Repo: cmtRepo, Pull: cmtPull}
}

type cmtPosted struct {
	repo   string
	number int
	body   string
}

// cmtReviewPosted is one review comment posted on the diff.
type cmtReviewPosted struct {
	repo, commit, path  string
	number, start, line int
	body                string
}

// fakeCommentWriter keeps the comments of one pull request. A posted comment becomes one of the bot's, so a
// second delivery finds it the way GitHub would show it.
type fakeCommentWriter struct {
	mu       sync.Mutex
	comments []github.Comment
	posted   []cmtPosted
	listed   []int64 // the author id of every readback
	listErr  error
	postErr  error

	reviews        []github.Comment  // the review comments of the pull request, the bot's included once posted
	reviewPosted   []cmtReviewPosted // every review comment posted
	reviewListed   []int64           // the author id of every review readback
	reviewListErr  error
	reviewPostErrs []error // consumed one per post: nil posts the comment, anything else is its answer
}

func (f *fakeCommentWriter) PostComment(_ context.Context, repo string, number int, body string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return 0, f.postErr
	}
	f.posted = append(f.posted, cmtPosted{repo, number, body})
	id := int64(1000 + len(f.comments))
	f.comments = append(f.comments, github.Comment{ID: id, AuthorID: cmtBotID, Body: body})
	return id, nil
}

func (f *fakeCommentWriter) CommentsByAuthor(_ context.Context, _ string, _ int, authorID int64) ([]github.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed = append(f.listed, authorID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var mine []github.Comment
	for _, c := range f.comments {
		if c.AuthorID == authorID {
			mine = append(mine, c)
		}
	}
	return mine, nil
}

func (f *fakeCommentWriter) PostReviewComment(_ context.Context, repo string, number int, commitSHA, path string, startLine, line int, body string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reviewPostErrs) > 0 {
		err := f.reviewPostErrs[0]
		f.reviewPostErrs = f.reviewPostErrs[1:]
		if err != nil {
			return 0, err
		}
	}
	f.reviewPosted = append(f.reviewPosted, cmtReviewPosted{repo: repo, commit: commitSHA, path: path, number: number, start: startLine, line: line, body: body})
	id := int64(2000 + len(f.reviews))
	f.reviews = append(f.reviews, github.Comment{ID: id, AuthorID: cmtBotID, Body: body})
	return id, nil
}

func (f *fakeCommentWriter) ReviewCommentsByAuthor(_ context.Context, _ string, _ int, authorID int64) ([]github.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewListed = append(f.reviewListed, authorID)
	if f.reviewListErr != nil {
		return nil, f.reviewListErr
	}
	var mine []github.Comment
	for _, c := range f.reviews {
		if c.AuthorID == authorID {
			mine = append(mine, c)
		}
	}
	return mine, nil
}

func (f *fakeCommentWriter) existing(authorID int64, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, github.Comment{ID: int64(10 + len(f.comments)), AuthorID: authorID, Body: body})
}

func (f *fakeCommentWriter) posts() []cmtPosted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cmtPosted(nil), f.posted...)
}

type cmtWorld struct {
	t         *testing.T
	mem       *governancetest.Memory
	writer    *fakeCommentWriter
	owners    *ownertest.Memory
	pub       *commentPublisher
	project   contract.ProjectID
	principal contract.Principal
	order     contract.CommandOrder
	comments  map[contract.OperationID]int64 // the comment that carried each processed command
	botCalls  atomic.Int32
	instCalls atomic.Int32
	logs      bytes.Buffer
}

func newCmtWorld(t *testing.T) *cmtWorld {
	t.Helper()
	w := &cmtWorld{t: t, mem: governancetest.NewMemory(), writer: &fakeCommentWriter{}, owners: ownertest.NewMemory(), project: owner.ProjectID(cmtRepoID),
		principal: cmtMust(contract.NewPrincipal(owner.PrincipalID(cmtOwnerID), contract.Human))}
	w.seedSuite()
	err := w.owners.Enroll(t.Context(), owner.Enrollment{Owner: owner.Owner{AccountID: cmtOwnerID, Login: cmtOwner}, Secret: make([]byte, 20),
		Projects: []owner.ProjectPolicy{{RepositoryID: cmtRepoID, Repo: cmtRepo, TestCheck: "ci"}}, Step: 1, Tier: owner.TierIsolated, EnrolledAt: bootTime})
	if err != nil {
		t.Fatal(err)
	}
	w.pub = &commentPublisher{
		writer: w.writer,
		bot: newBotAccount(func(context.Context) (int64, error) {
			w.botCalls.Add(1)
			return cmtBotID, nil
		}),
		instance: newLazy(func(context.Context) (string, error) {
			w.instCalls.Add(1)
			return cmtInstance, nil
		}),
		owners: w.owners,
		suites: w.mem,
		logger: slog.New(slog.NewJSONHandler(&w.logs, nil)),
	}
	return w
}

// seedSuite seeds the default Suite with proposal P1, whose revisions R1 and R2 are the first and the current one.
func (w *cmtWorld) seedSuite() {
	w.t.Helper()
	protected := cmtMust(contract.NewProtectedContract(
		cmtMust(artifact.NewManifest([]artifact.Entry{{Path: "tests/a_test.go", Content: artifact.Hash([]byte("v1"))}})),
		artifact.Hash([]byte("tests/**")), map[string]string{"runner": "v1"}))
	carrier := contract.ApprovalCarrierID("github-pr:1001/7")
	var proposal contract.Proposal
	for _, revisionID := range []contract.ProposalRevisionID{"R1", "R2"} {
		binding := cmtMust(contract.NewApprovalBinding(contract.BindingInput{Reference: w.reference(revisionID), ExpectedCanonical: "",
			Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "setup-1", CoveredInputs: protected.CoveredInputs()}))
		revision := cmtMust(contract.NewProposalRevision(binding, "origin", contract.OriginBranch, carrier))
		if proposal.IsZero() {
			proposal = cmtMust(contract.NewProposal(revision))
		} else {
			proposal = cmtMust(proposal.Revise(revision))
		}
	}
	policy := cmtMust(contract.NewPolicy(w.project, "setup-1", w.principal))
	canonical := cmtMust(contract.NewCanonicalSnapshot(cmtMust(contract.NewSuite(w.project, "default", "", 7)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	seed := governance.Seed{Suite: governance.SuiteState{Canonical: canonical, Policy: policy, Target: "branch:main"}, Proposals: []governance.SeedProposal{{Proposal: proposal}}}
	if err := w.mem.Seed(w.t.Context(), seed); err != nil {
		w.t.Fatal(err)
	}
}

func (w *cmtWorld) reference(revision contract.ProposalRevisionID) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: w.project, SuiteID: "default", ProposalID: "P1", RevisionID: revision}
}

// consent processes one owner command through governance and returns its operation and receipt.
func (w *cmtWorld) consent(action contract.ConsentAction, revision contract.ProposalRevisionID) (contract.OperationID, governance.ConsentReceipt) {
	w.t.Helper()
	w.order++
	operation := contract.OperationID(fmt.Sprintf("op-%d", w.order))
	command := cmtMust(contract.NewCommand(contract.CommandInput{OperationID: operation, SourceCommandID: contract.SourceCommandID(fmt.Sprintf("github-comment:%d", w.comment(operation, cmtComment+int64(w.order)-1))),
		Actor: w.principal, Reference: w.reference(revision), Carrier: "github-pr:1001/7", Action: action, Order: w.order}))
	response, err := governance.ProcessConsent(w.t.Context(), w.mem, governance.ConsentRequest{Command: command})
	if err != nil {
		w.t.Fatalf("process consent: %v", err)
	}
	return operation, response.Receipt
}

// comment records and returns the id of the comment that carries the command of operation.
func (w *cmtWorld) comment(operation contract.OperationID, id int64) int64 {
	if w.comments == nil {
		w.comments = map[contract.OperationID]int64{}
	}
	w.comments[operation] = id
	return id
}

// processedAck builds the outbox message of the owner's command comment for operation; the first command of a world
// is the comment cmtComment.
func (w *cmtWorld) processedAck(operation contract.OperationID) governance.OutboxMessage {
	w.t.Helper()
	comment, known := w.comments[operation]
	if !known {
		comment = cmtComment // an operation that was never processed
	}
	return cmtMust(newProcessedAck(cmtTarget(), comment, cmtOwner, w.project, "default", operation))
}

func (w *cmtWorld) deliver(m governance.OutboxMessage) error {
	return w.pub.publish(w.t.Context(), m.Key, m.Payload)
}

func (w *cmtWorld) mustDeliver(m governance.OutboxMessage) {
	w.t.Helper()
	if err := w.deliver(m); err != nil {
		w.t.Fatalf("publish %s: %v", m.Key, err)
	}
}

// wantPost requires exactly one comment, whose body is text, a blank line and the marker of key.
func (w *cmtWorld) wantPost(key, text string) {
	w.t.Helper()
	posts := w.writer.posts()
	if len(posts) != 1 {
		w.t.Fatalf("posted %d comments, want 1", len(posts))
	}
	want := strings.TrimRight(text, "\n") + "\n\n" + commandtext.Marker(cmtInstance, key)
	if posts[0].repo != cmtRepo || posts[0].number != cmtPull {
		w.t.Fatalf("posted on %s#%d, want %s#%d", posts[0].repo, posts[0].number, cmtRepo, cmtPull)
	}
	if posts[0].body != want {
		w.t.Fatalf("body =\n%s\nwant\n%s", posts[0].body, want)
	}
	if !commandtext.HasMarker(posts[0].body, cmtInstance, key) {
		w.t.Fatal("the body does not end with its marker")
	}
}

func cmtSummaryInputs() bootstrapSummaryInputs {
	return bootstrapSummaryInputs{Proposal: 1, Revision: 1, Origin: bootHead, Branch: "main", Protected: 2, Paths: []string{"tests/a_test.go", "tests/b_test.go"},
		Include: []string{"/tests/**"}, Exclude: []string{"/tests/gen/**"}, IncludeTotal: 3, ExcludeTotal: 1, Tier: owner.TierCoLocated}
}

func TestCommentPublisherRendersEveryKeyKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(w *cmtWorld) governance.OutboxMessage
		text func(w *cmtWorld) string
	}{
		{"processed acknowledgment", func(w *cmtWorld) governance.OutboxMessage {
			op, _ := w.consent(contract.ApproveConsent, "R2")
			return w.processedAck(op)
		}, func(*cmtWorld) string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Approved, Proposal: 1, Revision: 2, Owner: cmtOwner, Source: cmtComment, Eligible: true})
		}},
		{"rejected acknowledgment", func(*cmtWorld) governance.OutboxMessage {
			return cmtMust(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.InvalidCode, 0, "P1-R2"))
		}, func(*cmtWorld) string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 1, Revision: 2, Owner: cmtOwner, Source: cmtComment})
		}},
		{"bootstrap summary", func(*cmtWorld) governance.OutboxMessage {
			return cmtMust(newSummaryMessage(cmtTarget(), cmtSummaryInputs()))
		}, func(*cmtWorld) string {
			inputs := cmtSummaryInputs()
			inputs.IncludeTotal, inputs.ExcludeTotal, inputs.Runner = len(inputs.Include), len(inputs.Exclude), []string{} // the constructor counts the rules it was given and states that there are no runner rules
			return commandtext.BootstrapSummary(cmtMust(inputs.summary()))
		}},
		{"bootstrap promotion", func(*cmtWorld) governance.OutboxMessage {
			return cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))
		}, func(*cmtWorld) string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: 1, Revision: 1, Version: 1, Owner: cmtOwner})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			message := tc.make(w)
			w.mustDeliver(message)
			w.wantPost(message.Key, tc.text(w))
			if len(w.writer.listed) != 1 || w.writer.listed[0] != cmtBotID {
				t.Fatalf("the readback asked for the authors %v, want the bot %d", w.writer.listed, cmtBotID)
			}
		})
	}
}

func TestCommentPublisherPostsOncePerMarker(t *testing.T) {
	w := newCmtWorld(t)
	op, _ := w.consent(contract.ApproveConsent, "R2")
	message := w.processedAck(op)

	w.mustDeliver(message)
	w.mustDeliver(message) // a redelivery: the first post is already on the pull request

	if got := len(w.writer.posts()); got != 1 {
		t.Fatalf("posted %d comments for two deliveries, want 1", got)
	}
}

func TestCommentPublisherSkipsAnExistingMarker(t *testing.T) {
	w := newCmtWorld(t)
	message := cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))
	// A comment that an earlier delivery posted before it crashed.
	w.writer.existing(cmtBotID, "Bootstrap complete.\n\n"+commandtext.Marker(cmtInstance, message.Key)+"\n")

	w.mustDeliver(message)

	if got := len(w.writer.posts()); got != 0 {
		t.Fatalf("posted %d comments though the marker was already there, want 0", got)
	}
}

func TestCommentPublisherIgnoresOtherMarkers(t *testing.T) {
	message := cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))
	other := cmtMust(newPromotedMessage(cmtTarget(), "P2-R1"))
	for _, tc := range []struct {
		name   string
		author int64
		body   string
	}{
		{"another instance", cmtBotID, "text\n\n" + commandtext.Marker(cmtOtherInst, message.Key)},
		{"another key", cmtBotID, "text\n\n" + commandtext.Marker(cmtInstance, other.Key)},
		{"the marker is not at the end", cmtBotID, commandtext.Marker(cmtInstance, message.Key) + "\nmore text"},
		{"another author", cmtBotID + 1, "text\n\n" + commandtext.Marker(cmtInstance, message.Key)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			w.writer.existing(tc.author, tc.body)
			w.mustDeliver(message)
			if got := len(w.writer.posts()); got != 1 {
				t.Fatalf("posted %d comments, want 1", got)
			}
		})
	}
}

func TestAckOfMapsEveryConsentOutcome(t *testing.T) {
	base := func(o commandtext.Outcome, revision int) commandtext.Ack {
		return commandtext.Ack{Outcome: o, Proposal: 1, Revision: revision, Owner: cmtOwner, Source: cmtComment}
	}
	for _, tc := range []struct {
		name     string
		prepare  func(w *cmtWorld)
		action   contract.ConsentAction
		revision contract.ProposalRevisionID
		promoted contract.SuiteVersionID
		want     commandtext.Ack
	}{
		{"approved", nil, contract.ApproveConsent, "R2", "", func() commandtext.Ack { a := base(commandtext.Approved, 2); a.Eligible = true; return a }()},
		{"revoked", func(w *cmtWorld) { w.consent(contract.ApproveConsent, "R2") }, contract.RevokeConsent, "R2", "", base(commandtext.Revoked, 2)},
		{"no active approval", nil, contract.RevokeConsent, "R2", "", base(commandtext.NoActiveApproval, 2)},
		{"rejected as superseded", nil, contract.ApproveConsent, "R1", "", func() commandtext.Ack {
			a := base(commandtext.Rejected, 1)
			a.Rejection = commandtext.SupersededRevision
			return a
		}()},
		{"rejected as unknown", nil, contract.ApproveConsent, "R9", "", func() commandtext.Ack {
			a := base(commandtext.Rejected, 9)
			a.Rejection = commandtext.UnknownRevision
			return a
		}()},
		{"approved after promotion", nil, contract.ApproveConsent, "R2", "V1", base(commandtext.ApprovedAfterPromotion, 2)},
		{"revoked after promotion", func(w *cmtWorld) { w.consent(contract.ApproveConsent, "R2") }, contract.RevokeConsent, "R2", "V1", base(commandtext.RevokedAfterPromotion, 2)},
		{"revoked after promotion without an active approval", nil, contract.RevokeConsent, "R2", "V1", base(commandtext.RevokedAfterPromotion, 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			if tc.prepare != nil {
				tc.prepare(w)
			}
			_, receipt := w.consent(tc.action, tc.revision)
			receipt.PromotedVersionID = tc.promoted // the Memory has no promotion; the receipt of a promoted proposal carries its version
			got, err := ackOf(receipt, cmtOwner, cmtComment)
			if err != nil {
				t.Fatalf("ackOf: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ack = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAckOfCarriesEligibilityOnlyWhereItIsRendered(t *testing.T) {
	w := newCmtWorld(t)
	_, receipt := w.consent(contract.RevokeConsent, "R2")
	receipt.CurrentApprovalEligible = true // eligible through other valid approval

	got, err := ackOf(receipt, cmtOwner, cmtComment)
	if err != nil || got.Outcome != commandtext.NoActiveApproval || !got.Eligible {
		t.Fatalf("ack = %+v, %v; want an eligible no-active-approval", got, err)
	}
	receipt.PromotedVersionID = "V1"
	got, err = ackOf(receipt, cmtOwner, cmtComment)
	if err != nil || got.Outcome != commandtext.RevokedAfterPromotion || got.Eligible {
		t.Fatalf("ack after promotion = %+v, %v; want no eligibility (the renderer never evaluates it)", got, err)
	}
}

func TestCommentPublisherRendersProcessedOutcomes(t *testing.T) {
	w := newCmtWorld(t)
	op, _ := w.consent(contract.ApproveConsent, "R1") // superseded by R2
	message := w.processedAck(op)

	w.mustDeliver(message)

	w.wantPost(message.Key, commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.SupersededRevision, Proposal: 1, Revision: 1,
		Owner: cmtOwner, Source: cmtComment}))
}

func TestCommentPublisherRendersEveryRejectedDisposition(t *testing.T) {
	for _, tc := range []struct {
		name        string
		disposition commands.Disposition
		reason      commandtext.Reason
		reference   string
		want        commandtext.Ack
	}{
		{"malformed", commands.Malformed, commandtext.MissingCode, "", commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.MissingCode}},
		{"malformed with each reason", commands.Malformed, commandtext.ExtraText, "", commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: commandtext.ExtraText}},
		{"invalid code", commands.InvalidCode, 0, "P3-R2", commandtext.Ack{Outcome: commandtext.InvalidCode, Proposal: 3, Revision: 2}},
		{"replayed code", commands.ReplayedCode, 0, "P3-R2", commandtext.Ack{Outcome: commandtext.ReplayedCode, Proposal: 3, Revision: 2}},
		{"no suite", commands.NoSuite, 0, "P1-R1", commandtext.Ack{Outcome: commandtext.NoSuite, Proposal: 1, Revision: 1}},
		{"unknown proposal", commands.UnknownProposal, 0, "P9-R1", commandtext.Ack{Outcome: commandtext.Rejected, Rejection: commandtext.UnknownRevision, Proposal: 9, Revision: 1}},
		{"edited first", commands.EditedFirst, 0, "", commandtext.Ack{Outcome: commandtext.EditedFirst}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			message := cmtMust(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, tc.disposition, tc.reason, tc.reference))
			tc.want.Owner, tc.want.Source = cmtOwner, cmtComment

			w.mustDeliver(message)

			w.wantPost(message.Key, commandtext.Acknowledge(tc.want))
		})
	}
}

func TestCommentPublisherRefusesWhatDoesNotMatchItsKey(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "disposition": "edited-first", "comment": cmtComment, "owner": cmtOwner}
	}
	with := func(change func(map[string]any)) json.RawMessage {
		m := good()
		change(m)
		return cmtMust(json.Marshal(m))
	}
	summary := cmtMust(newSummaryMessage(cmtTarget(), cmtSummaryInputs()))
	promoted := cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"another comment", "ack:1001:556", with(func(map[string]any) {})},
		{"another repository", "ack:1002:555", with(func(map[string]any) {})},
		{"an unknown field", "ack:1001:555", with(func(m map[string]any) { m["body"] = cmtSecretText })},
		{"a code field", "ack:1001:555", with(func(m map[string]any) { m["code"] = "123456" })},
		{"a payload that is not JSON", "ack:1001:555", json.RawMessage(`{"repositoryId": ` + cmtSecretText)},
		{"a payload that is not an object", "ack:1001:555", json.RawMessage(`"` + cmtSecretText + `"`)},
		{"trailing data", "ack:1001:555", json.RawMessage(string(with(func(map[string]any) {})) + cmtSecretText)},
		{"an owner that is not a login", "ack:1001:555", with(func(m map[string]any) { m["owner"] = "SECRET text with spaces" })},
		{"a repository that is not owner/name", "ack:1001:555", with(func(m map[string]any) { m["repo"] = "SECRET" })},
		{"a pull number of zero", "ack:1001:555", with(func(m map[string]any) { m["pull"] = 0 })},
		{"an unknown disposition", "ack:1001:555", with(func(m map[string]any) { m["disposition"] = cmtSecretText })},
		{"a malformed disposition without a reason", "ack:1001:555", with(func(m map[string]any) { m["disposition"] = "malformed" })},
		{"a reason that is not a code", "ack:1001:555", with(func(m map[string]any) { m["disposition"], m["reason"] = "malformed", cmtSecretText })},
		{"an unknown-proposal disposition without a reference", "ack:1001:555", with(func(m map[string]any) { m["disposition"] = "unknown-proposal" })},
		{"a reference that is not P<n>-R<m>", "ack:1001:555", with(func(m map[string]any) { m["disposition"], m["reference"] = "no-suite", cmtSecretText })},
		{"a reference on an edited-first disposition", "ack:1001:555", with(func(m map[string]any) { m["reference"] = "P1-R1" })},
		{"neither a disposition nor an operation", "ack:1001:555", with(func(m map[string]any) { delete(m, "disposition") })},
		{"an acknowledgment payload under a summary key", "bootstrap-summary:1001:P1-R1", with(func(map[string]any) {})},
		{"a summary payload under another reference", "bootstrap-summary:1001:P2-R1", summary.Payload},
		{"a summary payload under an acknowledgment key", "ack:1001:555", summary.Payload},
		{"a promotion payload under another repository", "bootstrap-promoted:1002:P1-R1", promoted.Payload},
		{"a promotion payload under another reference", "bootstrap-promoted:1001:P1-R2", promoted.Payload},
		{"an unknown key kind", "probe:1001:555", promoted.Payload},
		{"a key with two parts", "ack:1001", with(func(map[string]any) {})},
		{"a key with four parts", "ack:1001:555:1", with(func(map[string]any) {})},
		{"a repository id that is not a number", "ack:x:555", with(func(map[string]any) {})},
		{"a comment id that is not a number", "ack:1001:x", with(func(map[string]any) {})},
		{"an empty key", "", with(func(map[string]any) {})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), tc.key, tc.payload)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			for _, leak := range []string{cmtSecretText, "SECRET", cmtOwner, cmtRepo, "123456"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the error %q holds payload text %q", err, leak)
				}
			}
			if tc.key != "" && !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("the error %q does not name the key %q", err, tc.key)
			}
			if len(w.writer.posts()) != 0 || len(w.writer.listed) != 0 {
				t.Fatal("GitHub was called for a payload that was refused")
			}
		})
	}
}

func TestCommentPublisherErrorsWithoutAReceiptOrASuite(t *testing.T) {
	w := newCmtWorld(t)
	missing := w.processedAck("op-never-processed")
	if err := w.deliver(missing); err == nil || !strings.Contains(err.Error(), missing.Key) {
		t.Fatalf("publish without a receipt = %v, want an error naming the key", err)
	}
	otherSuite := cmtMust(newProcessedAck(cmtTarget(), cmtComment, cmtOwner, w.project, "other", "op-1"))
	if err := w.deliver(otherSuite); err == nil {
		t.Fatal("publish for a Suite that does not exist succeeded")
	}
	if len(w.writer.posts()) != 0 {
		t.Fatal("a comment was posted without the facts it states")
	}
}

func TestCommentPublisherErrorsWithoutAnEnrolledOwnerForThePromotion(t *testing.T) {
	w := newCmtWorld(t)
	w.pub.owners = ownertest.NewMemory() // nobody enrolled
	if err := w.deliver(cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))); err == nil {
		t.Fatal("publish succeeded without an owner to name")
	}
}

func TestCommentPublisherReturnsGitHubErrorsAsStatusOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(f *fakeCommentWriter, err error)
		err    error
		status string
	}{
		{"post answered with a status", func(f *fakeCommentWriter, err error) { f.postErr = err }, fmt.Errorf("%w: %s", &rest.StatusError{Status: 403}, cmtSecretText), "403"},
		{"readback answered with a status", func(f *fakeCommentWriter, err error) { f.listErr = err }, fmt.Errorf("%w: %s", &rest.StatusError{Status: 502}, cmtSecretText), "502"},
		{"rate limited", func(f *fakeCommentWriter, err error) { f.postErr = err }, fmt.Errorf("%w: %s", &rest.RateLimitedError{Status: 429}, cmtSecretText), "rate_limited"},
		{"transport failure", func(f *fakeCommentWriter, err error) { f.postErr = err }, fmt.Errorf("Post https://api.example/%s: connection refused", cmtSecretText), "transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			tc.set(w.writer, tc.err)
			err := w.deliver(cmtMust(newPromotedMessage(cmtTarget(), "P1-R1")))
			if err == nil {
				t.Fatal("publish succeeded")
			}
			if strings.Contains(err.Error(), cmtSecretText) {
				t.Fatalf("the error %q holds text GitHub sent", err)
			}
			if !strings.Contains(err.Error(), tc.status) {
				t.Fatalf("the error %q does not say %q", err, tc.status)
			}
		})
	}
}

func TestBotAccountResolvesOnceAndRetriesAfterAFailure(t *testing.T) {
	var calls int
	var fail = true
	bot := newBotAccount(func(context.Context) (int64, error) {
		calls++
		if fail {
			return 0, fmt.Errorf("%w: %s", &rest.StatusError{Status: 502}, cmtSecretText)
		}
		return cmtBotID, nil
	})
	if _, err := bot.ID(t.Context()); err == nil {
		t.Fatal("ID succeeded while GitHub failed")
	}
	fail = false
	for range 3 {
		id, err := bot.ID(t.Context())
		if err != nil || id != cmtBotID {
			t.Fatalf("ID = %d, %v; want %d", id, err, cmtBotID)
		}
	}
	if calls != 2 {
		t.Fatalf("resolved %d times, want 2 (the failure, then once)", calls)
	}
}

func TestCommentPublisherReturnsABotIdentityFailureAndRetriesIt(t *testing.T) {
	w := newCmtWorld(t)
	var failing atomic.Bool
	failing.Store(true)
	var calls atomic.Int32
	w.pub.bot = newBotAccount(func(context.Context) (int64, error) {
		calls.Add(1)
		if failing.Load() {
			return 0, fmt.Errorf("%w: %s", &rest.StatusError{Status: 502}, cmtSecretText)
		}
		return cmtBotID, nil
	})
	message := cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))

	err := w.deliver(message)
	if err == nil || strings.Contains(err.Error(), cmtSecretText) || !strings.Contains(err.Error(), "502") {
		t.Fatalf("publish while the identity fails = %v, want a status-only error", err)
	}
	if len(w.writer.posts()) != 0 {
		t.Fatal("a comment was posted without the bot identity")
	}
	failing.Store(false)
	w.mustDeliver(message)
	w.mustDeliver(cmtMust(newPromotedMessage(cmtTarget(), "P2-R1")))
	if calls.Load() != 2 {
		t.Fatalf("the identity was resolved %d times, want 2", calls.Load())
	}
}

func TestCommentPublisherReadsTheInstanceOnceAndRetriesAfterAFailure(t *testing.T) {
	w := newCmtWorld(t)
	var calls int
	w.pub.instance = newLazy(func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("the store failed")
		}
		return cmtInstance, nil
	})
	message := cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))

	if err := w.deliver(message); err == nil {
		t.Fatal("publish succeeded while the instance id could not be read")
	}
	w.mustDeliver(message)
	w.mustDeliver(cmtMust(newPromotedMessage(cmtTarget(), "P2-R1")))
	if calls != 2 {
		t.Fatalf("read the instance %d times, want 2", calls)
	}
}

func TestLazyIsSafeForConcurrentUse(t *testing.T) {
	var calls atomic.Int32
	bot := newBotAccount(func(context.Context) (int64, error) {
		calls.Add(1)
		return cmtBotID, nil
	})
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			if id, err := bot.ID(context.Background()); err != nil || id != cmtBotID {
				t.Errorf("ID = %d, %v", id, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("resolved %d times for concurrent callers, want 1", calls.Load())
	}
}

func TestCommentPublisherIsSafeForConcurrentDeliveries(t *testing.T) {
	w := newCmtWorld(t)
	var wg sync.WaitGroup
	for n := 1; n <= 16; n++ {
		wg.Go(func() {
			if err := w.deliver(cmtMust(newPromotedMessage(cmtTarget(), fmt.Sprintf("P%d-R1", n)))); err != nil {
				t.Errorf("publish: %v", err)
			}
		})
	}
	wg.Wait()
	if got := len(w.writer.posts()); got != 16 {
		t.Fatalf("posted %d comments, want 16", got)
	}
	if w.botCalls.Load() != 1 || w.instCalls.Load() != 1 {
		t.Fatalf("resolved the bot %d times and the instance %d times, want once each", w.botCalls.Load(), w.instCalls.Load())
	}
}

func TestAcknowledgmentConstructors(t *testing.T) {
	t.Run("processed", func(t *testing.T) {
		message, err := newProcessedAck(cmtTarget(), cmtComment, cmtOwner, "github-repo:1001", "default", "op-1")
		if err != nil {
			t.Fatal(err)
		}
		if message.Key != "ack:1001:555" || message.Kind != githubCommentKind {
			t.Fatalf("message = %s %s, want ack:1001:555 %s", message.Key, message.Kind, githubCommentKind)
		}
		var payload map[string]any
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"repositoryId": float64(1001), "repo": cmtRepo, "pull": float64(cmtPull), "project": "github-repo:1001", "suite": "default",
			"operation": "op-1", "comment": float64(555), "owner": cmtOwner}
		if fmt.Sprint(payload) != fmt.Sprint(want) {
			t.Fatalf("payload = %v, want %v", payload, want)
		}
	})
	t.Run("rejected", func(t *testing.T) {
		message, err := newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.Malformed, commandtext.BadCode, "")
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if message.Key != "ack:1001:555" || payload["disposition"] != "malformed" || payload["reason"] == "" || payload["reason"] == nil {
			t.Fatalf("message = %s %v, want a malformed acknowledgment with its reason", message.Key, payload)
		}
		if _, set := payload["reference"]; set {
			t.Fatalf("payload %v has a reference for a malformed command", payload)
		}
	})
	t.Run("invalid input", func(t *testing.T) {
		longLogin := strings.Repeat("a", 40)
		for name, err := range map[string]error{
			"a repository id of zero":              second(newProcessedAck(commentTarget{RepositoryID: 0, Repo: cmtRepo, Pull: 1}, cmtComment, cmtOwner, "p", "s", "o")),
			"a repository that is not owner/name":  second(newProcessedAck(commentTarget{RepositoryID: 1, Repo: "widgets", Pull: 1}, cmtComment, cmtOwner, "p", "s", "o")),
			"a pull of zero":                       second(newProcessedAck(commentTarget{RepositoryID: 1, Repo: cmtRepo, Pull: 0}, cmtComment, cmtOwner, "p", "s", "o")),
			"a comment id of zero":                 second(newProcessedAck(cmtTarget(), 0, cmtOwner, "p", "s", "o")),
			"an owner with spaces":                 second(newProcessedAck(cmtTarget(), cmtComment, "not a login", "p", "s", "o")),
			"an owner that is a whole comment":     second(newProcessedAck(cmtTarget(), cmtComment, longLogin, "p", "s", "o")),
			"an empty project":                     second(newProcessedAck(cmtTarget(), cmtComment, cmtOwner, "", "s", "o")),
			"an empty suite":                       second(newProcessedAck(cmtTarget(), cmtComment, cmtOwner, "p", "", "o")),
			"an empty operation":                   second(newProcessedAck(cmtTarget(), cmtComment, cmtOwner, "p", "s", "")),
			"a disposition that is not rejected":   second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.Processed, 0, "")),
			"a malformed command without a reason": second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.Malformed, 0, "")),
			"a reason on another disposition":      second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.InvalidCode, commandtext.BadCode, "P1-R1")),
			"a malformed command with a reference": second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.Malformed, commandtext.BadCode, "P1-R1")),
			"a reference that is a comment":        second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.NoSuite, 0, "approve P1-R1 123456")),
			"a reference with a zero":              second(newRejectedAck(cmtTarget(), cmtComment, cmtOwner, commands.NoSuite, 0, "P0-R1")),
		} {
			if err == nil {
				t.Errorf("%s was accepted", name)
			}
		}
	})
}

// second keeps the error of a constructor call, so a table can list the calls that must fail.
func second(_ governance.OutboxMessage, err error) error { return err }

// composeGitHubFake is a GitHub that records the requests it gets and the permissions that each installation token asked for.
type composeGitHubFake struct {
	mu        sync.Mutex
	requests  []string
	tokenMaps []map[string]string
}

func (f *composeGitHubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", setupInstallID):
		var body struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		f.tokenMaps = append(f.tokenMaps, body.Permissions)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": body.Permissions})
	case r.URL.Path == "/app":
		_, _ = fmt.Fprint(w, `{"slug":"suiteward"}`)
	case r.URL.Path == "/users/suiteward[bot]":
		_, _ = fmt.Fprintf(w, `{"login":"suiteward[bot]","id":%d,"type":"Bot"}`, cmtBotID)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/commits/"+chkHead+"/check-runs":
		_, _ = fmt.Fprint(w, `{"check_runs":[]}`)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/7/comments":
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":1}`)
	default:
		http.NotFound(w, r)
	}
}

// requireTokenPermissions requires a token that asked for pull_requests write with exactly metadata read, and one that asked for exactly metadata read and checks write.
func requireTokenPermissions(t *testing.T, tokenMaps []map[string]string) {
	t.Helper()
	var sawWrite, sawChecks bool
	for _, m := range tokenMaps {
		if m["pull_requests"] == "write" {
			sawWrite = true
			if len(m) != 2 || m["metadata"] != "read" {
				t.Fatalf("the comment token asked for %v, want metadata read and pull_requests write", m)
			}
		}
		if _, asksChecks := m["checks"]; asksChecks {
			sawChecks = true
			if len(m) != 2 || m["metadata"] != "read" || m["checks"] != "write" {
				t.Fatalf("the check token asked for %v, want exactly metadata read and checks write", m)
			}
		}
	}
	if !sawChecks {
		t.Fatalf("no token asked for checks: %v", tokenMaps)
	}
	if !sawWrite {
		t.Fatalf("no token asked for pull_requests write: %v", tokenMaps)
	}
}

// TestComposeGitHubBuildsTheCommentWriterAndALazyBot checks the composition against a GitHub that records what it
// is asked: nothing is requested at compose time, the bot's id is read once on first use with the read token,
// the comment is posted with a token that requests Pull requests: write, and the check runs are listed with a
// token that requests exactly Checks: write (the comment token never asks for it).
func TestComposeGitHubBuildsTheCommentWriterAndALazyBot(t *testing.T) {
	fake := &composeGitHubFake{}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	settings := config.GitHub{AppID: setupAppID, AppKey: config.Secret(setupKey()), InstallationID: setupInstallID, Repositories: []string{"acme/widgets"}, APIURL: server.URL}

	gh, err := composeGitHub(settings, time.Minute, githubsynctest.NewMemory(), time.Now, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	composed := len(fake.requests)
	fake.mu.Unlock()
	if composed != 0 {
		t.Fatalf("composing the GitHub side made %d requests, want none", composed)
	}
	for range 2 {
		if id, err := gh.bot.ID(t.Context()); err != nil || id != cmtBotID {
			t.Fatalf("bot id = %d, %v; want %d", id, err, cmtBotID)
		}
	}
	if _, err := gh.writer.PostComment(t.Context(), "acme/widgets", 7, "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := gh.checkRuns.Runs(t.Context(), "acme/widgets", chkHead, checkName, chkAppID); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := strings.Count(strings.Join(fake.requests, "\n"), "GET /users/suiteward[bot]"); got != 1 {
		t.Fatalf("the bot account was read %d times, want 1", got)
	}
	requireTokenPermissions(t, fake.tokenMaps)
}

func TestCommentPublisherBindsTheReceiptToItsCommentAndProject(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(w *cmtWorld, op contract.OperationID) governance.OutboxMessage
	}{
		{"another comment than the one that carried the command", func(w *cmtWorld, op contract.OperationID) governance.OutboxMessage {
			return cmtMust(newProcessedAck(cmtTarget(), cmtComment+1, cmtOwner, w.project, "default", op))
		}},
		{"a project that is not the repository's", func(w *cmtWorld, op contract.OperationID) governance.OutboxMessage {
			return cmtMust(newProcessedAck(cmtTarget(), cmtComment, cmtOwner, "github-repo:2002", "default", op))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			op, _ := w.consent(contract.ApproveConsent, "R2") // carried by the comment cmtComment, in this repository's project
			message := tc.build(w, op)

			err := w.deliver(message)

			if err == nil || !strings.Contains(err.Error(), message.Key) {
				t.Fatalf("publish = %v, want an error naming the key", err)
			}
			if err != nil && strings.Contains(err.Error(), "read the receipt") {
				t.Fatalf("the error %q says the receipt could not be read; it should say the receipt is not the comment's", err)
			}
			for _, leak := range []string{cmtOwner, cmtRepo, string(op)} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the error %q holds payload text %q", err, leak)
				}
			}
			if len(w.writer.posts()) != 0 {
				t.Fatal("a comment was posted for a receipt that is not the comment's")
			}
		})
	}
}

// ownerWith is an ownerSource that answers one enrolled state.
type ownerWith owner.State

func (o ownerWith) Current(context.Context) (owner.State, bool, error) {
	return owner.State(o), true, nil
}

// The renderer panics on a login that is not a GitHub login. The guard turns that into an error that does not
// quote it. The behavior already held when this test was written, so it has no RED.
func TestCommentPublisherDoesNotQuoteAnOwnerLoginTheRendererRefuses(t *testing.T) {
	w := newCmtWorld(t)
	w.pub.owners = ownerWith(owner.State{Owner: owner.Owner{AccountID: cmtOwnerID, Login: "bad login " + cmtSecretText}})
	message := cmtMust(newPromotedMessage(cmtTarget(), "P1-R1"))

	err := w.deliver(message)

	if err == nil || !strings.Contains(err.Error(), message.Key) {
		t.Fatalf("publish = %v, want an error naming the key", err)
	}
	if strings.Contains(err.Error(), "bad login") || strings.Contains(err.Error(), cmtSecretText) {
		t.Fatalf("the error %q holds the login", err)
	}
	if len(w.writer.posts()) != 0 {
		t.Fatal("a comment was posted for an owner that cannot be named")
	}
}

// GitHub caps an owner at 39 characters and a name at 100, so a longer repository is no repository: the payloads that carry it stay within the size
// their worst case was measured with.
func TestCommentTargetRefusesARepositoryLongerThanGitHubAllows(t *testing.T) {
	for _, repo := range []string{strings.Repeat("a", 40) + "/x", "acme/" + strings.Repeat("a", 101)} {
		if err := (commentTarget{RepositoryID: 1, Repo: repo, Pull: 1}).validate(); err == nil {
			t.Errorf("a repository of %d characters is valid", len(repo))
		}
	}
	if err := (commentTarget{RepositoryID: 1, Repo: strings.Repeat("a", 39) + "/" + strings.Repeat("a", 100), Pull: 1}).validate(); err != nil {
		t.Errorf("the longest repository is refused: %v", err)
	}
}

// Every kind that splitKey accepts, except the scope review, which posts on the diff, has a renderer, so the "no renderer" error of publish stays unreachable.
func TestEveryCommentKindButTheScopeReviewHasARenderer(t *testing.T) {
	for kind := range keyParts {
		if _, has := commentHandlers[kind]; !has && kind != scopeReviewKey {
			t.Errorf("the kind %q has no renderer in commentHandlers", kind)
		}
	}
	for kind := range commentHandlers {
		if _, known := keyParts[kind]; !known {
			t.Errorf("commentHandlers has the kind %q, which splitKey refuses", kind)
		}
	}
}
