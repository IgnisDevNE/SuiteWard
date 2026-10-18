package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The github_comment publisher (docs/contracts/runtime.md, "Outbox kind github_comment"). The payloads of
// bootstrap-summary, bootstrap-promoted, bootstrap-notice and bootstrap-cancelled are in bootstrap.go; the acknowledgments of the command job are here.

const (
	ackKey      = "ack"
	mergeKey    = "merge"
	revisionKey = "revision"
)

var (
	repoPattern      = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	referencePattern = regexp.MustCompile(`^P([1-9][0-9]{0,8})-R([1-9][0-9]{0,8})$`)
	// loginPattern is commandtext's: the renderer panics on any other owner.
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)
)

// validate reports why the target cannot name a pull request.
func (t commentTarget) validate() error {
	switch {
	case t.RepositoryID <= 0:
		return errors.New("the repository id must be positive")
	case !repoPattern.MatchString(t.Repo):
		return errors.New("the repository is not owner/name")
	case t.Pull < 1 || t.Pull > math.MaxInt32:
		return errors.New("the pull request number is out of range")
	}
	return nil
}

// ackPayload is the payload of ack:<repository id>:<comment id>, the acknowledgment of one command comment. It
// holds identifiers and codes, never the comment text or the code the owner typed: the publisher renders the body
// from what the facts say. Two shapes share it, told apart by Disposition.
//
// A processed command (the command job called ProcessConsent) has Project, Suite and Operation, and no
// Disposition: the publisher reads the receipt of Operation. Any other command (the job recorded a disposition
// without reaching ProcessConsent) has Disposition and none of the three: the payload holds the decision.
type ackPayload struct {
	commentTarget
	Project     contract.ProjectID   `json:"project,omitempty"`     // processed: the project of the Suite
	Suite       contract.SuiteID     `json:"suite,omitempty"`       // processed: the Suite whose receipt is read
	Operation   contract.OperationID `json:"operation,omitempty"`   // processed: the operation of the consent receipt
	Disposition string               `json:"disposition,omitempty"` // otherwise: malformed, invalid-code, replayed-code, no-suite, unknown-proposal, edited-first, proposed, propose-refused, skipped, skip-refused or failed
	Comment     int64                `json:"comment"`               // the id of the command comment
	Owner       string               `json:"owner"`                 // the owner's GitHub login at processing time
	Reason      string               `json:"reason,omitempty"`      // malformed: the reason code; propose-refused and skip-refused: the refusal
	Reference   string               `json:"reference,omitempty"`   // invalid-code, replayed-code and no-suite of an approve or revoke, unknown-proposal and proposed: P<n>-R<m>
	Branch      string               `json:"branch,omitempty"`      // propose-refused for declaration-missing: the pull request's head ref
	Suggestion  []string             `json:"suggestion,omitempty"`  // and the include patterns inventory.Suggest found
	Runner      []string             `json:"runner,omitempty"`      // and the runner patterns,
	Mixed       []string             `json:"mixed,omitempty"`       // the mixed files, shown and never suggested,
	Cut         bool                 `json:"cut,omitempty"`         // and whether the limit of 20 patterns cut the lists
}

// The codes of the malformed reasons in a payload.
var malformedCodes = []struct {
	reason commandtext.Reason
	code   string
}{
	{commandtext.UnknownVerb, "unknown-verb"}, {commandtext.MissingReference, "missing-reference"}, {commandtext.BadReference, "bad-reference"},
	{commandtext.MissingCode, "missing-code"}, {commandtext.BadCode, "bad-code"}, {commandtext.ExtraText, "extra-text"},
}

func reasonCode(r commandtext.Reason) string {
	for _, c := range malformedCodes {
		if c.reason == r {
			return c.code
		}
	}
	return ""
}

func codeReason(code string) (commandtext.Reason, bool) {
	for _, c := range malformedCodes {
		if c.code == code {
			return c.reason, true
		}
	}
	return 0, false
}

// The reasons of propose-refused and skip-refused in a payload (docs/contracts/runtime.md, "Comment keys").
const (
	reasonCanonicalPresent   = "canonical-present"
	reasonFork               = "fork"
	reasonNotOpen            = "not-open"
	reasonBaseNotTarget      = "base-not-target"
	reasonEmptyInventory     = "empty-inventory"
	refusalInvalidSource     = "invalid-source" // named like the bootstrap job's status code
	reasonCarrier            = "carrier"
	reasonDeclarationMissing = "declaration-missing" // rendered by commandtext.SuggestDeclaration, which is not an Ack
)

var (
	proposeRefusals = map[string]commandtext.Refusal{reasonCanonicalPresent: commandtext.CanonicalPresent, reasonFork: commandtext.Fork, reasonNotOpen: commandtext.NotOpen, reasonBaseNotTarget: commandtext.BaseNotTarget, reasonEmptyInventory: commandtext.EmptyInventory, refusalInvalidSource: commandtext.InvalidSource}
	skipRefusals    = map[string]commandtext.Refusal{reasonCanonicalPresent: commandtext.CanonicalPresent, reasonFork: commandtext.Fork, reasonCarrier: commandtext.Carrier}
)

// rejectedOutcomes maps the dispositions that get an acknowledgment without a receipt.
var rejectedOutcomes = map[commands.Disposition]commandtext.Outcome{
	commands.Malformed: commandtext.MalformedCommand, commands.InvalidCode: commandtext.InvalidCode, commands.ReplayedCode: commandtext.ReplayedCode,
	commands.NoSuite: commandtext.NoSuite, commands.EditedFirst: commandtext.EditedFirst, commands.UnknownProposal: commandtext.Rejected,
	commands.Proposed: commandtext.AlreadyProposed, commands.ProposeRefused: commandtext.ProposeRefused, commands.Skipped: commandtext.Skipped,
	commands.SkipRefused: commandtext.SkipRefused, commands.Failed: commandtext.ProposeFailed, // a failed command is acknowledged only when it was a propose
}

// referenceRule says whether the acknowledgment of d names a reference: always, never, or only when the command had one
// (a skip and a propose have none, so their code outcomes and no-suite come without).
func referenceRule(d commands.Disposition) (required, allowed bool) {
	switch d {
	case commands.UnknownProposal, commands.Proposed:
		return true, true
	case commands.InvalidCode, commands.ReplayedCode, commands.NoSuite:
		return false, true
	}
	return false, false
}

// reasonFits reports whether reason is one a payload of d may carry.
func reasonFits(d commands.Disposition, reason string) bool {
	switch d {
	case commands.Malformed:
		_, known := codeReason(reason)
		return known
	case commands.ProposeRefused:
		_, known := proposeRefusals[reason]
		return known || reason == reasonDeclarationMissing
	case commands.SkipRefused:
		_, known := skipRefusals[reason]
		return known
	}
	return reason == ""
}

// declarationMissing is whether the payload is the refusal of a propose for want of a .suiteward.yml.
func (a ackPayload) declarationMissing() bool {
	return a.Disposition == string(commands.ProposeRefused) && a.Reason == reasonDeclarationMissing
}

// suggested is whether the payload holds any of the lists of a suggestion.
func (a ackPayload) suggested() bool {
	return len(a.Suggestion) != 0 || len(a.Runner) != 0 || len(a.Mixed) != 0 || a.Cut
}

// suggestion is what the renderer takes for a declaration-missing payload.
func (a ackPayload) suggestion() commandtext.DeclarationSuggestion {
	return commandtext.DeclarationSuggestion{Source: a.Comment, Repo: a.Repo, Branch: a.Branch, Patterns: a.Suggestion, Runner: a.Runner, Mixed: a.Mixed, Cut: a.Cut}
}

// validate reports why the payload cannot be rendered, without quoting it.
func (a ackPayload) validate() error {
	if err := a.commentTarget.validate(); err != nil {
		return err
	}
	if a.Comment <= 0 || !loginPattern.MatchString(a.Owner) {
		return errors.New("the comment id or the owner login is invalid")
	}
	if a.Disposition == "" {
		if a.Project == "" || a.Suite == "" || a.Operation == "" || a.Reason != "" || a.Reference != "" || a.Branch != "" || a.suggested() {
			return errors.New("a processed acknowledgment names its project, Suite and operation and nothing else")
		}
		return nil
	}
	d := commands.Disposition(a.Disposition)
	if _, known := rejectedOutcomes[d]; !known || a.Project != "" || a.Suite != "" || a.Operation != "" {
		return errors.New("a rejected acknowledgment names a known disposition and no operation")
	}
	if !reasonFits(d, a.Reason) {
		return errors.New("the reason does not fit the disposition")
	}
	required, allowed := referenceRule(d)
	if (required && a.Reference == "") || (!allowed && a.Reference != "") {
		return errors.New("the reference fits only a command that parsed")
	}
	if a.Reference != "" {
		if _, _, ok := parseReference(a.Reference); !ok {
			return errors.New("the reference is not P<n>-R<m>")
		}
	}
	if !a.declarationMissing() {
		if a.Branch != "" || a.suggested() {
			return errors.New("the branch and the suggestion fit only a missing declaration")
		}
		return nil
	}
	// SuggestDeclaration panics on what it refuses, which would loop the outbox job: refuse it here.
	if commandtext.ValidateSuggestion(a.suggestion()) != nil {
		return errors.New("the branch, the repository or the suggestion cannot be rendered")
	}
	for _, text := range slices.Concat(a.Suggestion, a.Runner, a.Mixed) {
		if len(text) > maxSummaryText {
			return errors.New("a suggested pattern or a mixed path is too long")
		}
	}
	return nil
}

// newProcessedAck builds the acknowledgment of a command that ProcessConsent processed: the publisher reads the
// receipt of operation in the Suite. comment is the id of the command comment and ownerLogin the owner's login.
func newProcessedAck(target commentTarget, comment int64, ownerLogin string, project contract.ProjectID, suite contract.SuiteID, operation contract.OperationID) (governance.OutboxMessage, error) {
	return newAck(ackPayload{commentTarget: target, Project: project, Suite: suite, Operation: operation, Comment: comment, Owner: ownerLogin})
}

// newRejectedAck builds the acknowledgment of a command the job rejected before ProcessConsent: disposition is
// Malformed (with its reason), InvalidCode, ReplayedCode, NoSuite, UnknownProposal or EditedFirst. reference (P<n>-R<m>) is the
// parsed command's: required for UnknownProposal, optional for InvalidCode, ReplayedCode and NoSuite (a skip or a
// propose has none) and empty otherwise.
func newRejectedAck(target commentTarget, comment int64, ownerLogin string, disposition commands.Disposition, reason commandtext.Reason, reference string) (governance.OutboxMessage, error) {
	return newAck(ackPayload{commentTarget: target, Disposition: string(disposition), Comment: comment, Owner: ownerLogin, Reason: reasonCode(reason), Reference: reference})
}

// newBootstrapAck builds the acknowledgment of a propose or a skip: Proposed (already proposed, with its reference),
// ProposeRefused or SkipRefused (with the refusal's code, other than declaration-missing), Skipped, or Failed.
func newBootstrapAck(target commentTarget, comment int64, ownerLogin string, disposition commands.Disposition, reason, reference string) (governance.OutboxMessage, error) {
	return newAck(ackPayload{commentTarget: target, Disposition: string(disposition), Comment: comment, Owner: ownerLogin, Reason: reason, Reference: reference})
}

// newDeclarationMissingAck builds the refusal of a propose when no tree holds a .suiteward.yml: branch is the pull
// request's head ref and suggested the patterns and the mixed files found. Each is cut to the payload bound, which only
// keeps the payload small: the renderer leaves out any pattern over its own, lower limit (a cut one included) and counts
// those it left out, so a cut pattern is never suggested.
func newDeclarationMissingAck(target commentTarget, comment int64, ownerLogin, branch string, suggested inventory.Suggestion) (governance.OutboxMessage, error) {
	cut := func(list []string) []string {
		var bounded []string // nil without entries: the payload then omits the list
		for _, entry := range list {
			bounded = append(bounded, cutBytes(entry, maxSummaryText))
		}
		return bounded
	}
	return newAck(ackPayload{commentTarget: target, Disposition: string(commands.ProposeRefused), Comment: comment, Owner: ownerLogin, Reason: reasonDeclarationMissing, Branch: branch,
		Suggestion: cut(suggested.Include), Runner: cut(suggested.Runner), Mixed: cut(suggested.Mixed), Cut: suggested.Cut})
}

func newAck(p ackPayload) (governance.OutboxMessage, error) {
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("acknowledgment: %w", err)
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the acknowledgment: %w", err)
	}
	key := fmt.Sprintf("%s:%d:%d", ackKey, p.RepositoryID, p.Comment)
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: payload}, nil
}

func parseReference(s string) (proposal, revision int, ok bool) {
	m := referencePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	proposal, _ = strconv.Atoi(m[1]) // the pattern admits one to nine digits
	revision, _ = strconv.Atoi(m[2])
	return proposal, revision, true
}

// lazy resolves a value on first use and keeps it. A failure is returned and the next call tries again, so
// startup never depends on the source being reachable.
type lazy[T any] struct {
	resolve func(context.Context) (T, error)
	mu      sync.Mutex
	value   T
	done    bool
}

func newLazy[T any](resolve func(context.Context) (T, error)) *lazy[T] {
	return &lazy[T]{resolve: resolve}
}

func (l *lazy[T]) get(ctx context.Context) (T, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		value, err := l.resolve(ctx)
		if err != nil {
			var zero T
			return zero, err
		}
		l.value, l.done = value, true
	}
	return l.value, nil
}

// botAccount is the numeric id of SuiteWard's bot account, resolved on first use and cached for the process. The
// comment publisher and the command job (which skips the bot's own comments) share one.
type botAccount struct{ *lazy[int64] }

func newBotAccount(resolve func(context.Context) (int64, error)) *botAccount {
	return &botAccount{newLazy(resolve)}
}

// ID returns the bot's account id; it is safe for concurrent use.
func (b *botAccount) ID(ctx context.Context) (int64, error) { return b.get(ctx) }

type commentWriter interface {
	PostComment(ctx context.Context, repo string, number int, body string) (int64, error)
	CommentsByAuthor(ctx context.Context, repo string, number int, authorID int64) ([]github.Comment, error)
	ReviewCommentsByAuthor(ctx context.Context, repo string, number int, authorID int64) ([]github.Comment, error)
	PostReviewComment(ctx context.Context, repo string, number int, commitSHA, path string, startLine, line int, body string) (int64, error)
}

type ownerSource interface {
	Current(ctx context.Context) (owner.State, bool, error)
}

// commentPublisher is the river.Publisher of the github_comment kind.
type commentPublisher struct {
	writer   commentWriter
	bot      *botAccount
	instance *lazy[string] // commands.Store.Instance, read once
	owners   ownerSource
	suites   governance.UnitOfWork
	logger   *slog.Logger
}

func keyError(kind, key, what string) error { return fmt.Errorf("%s %s: %s", kind, key, what) }

// githubFailure is a failed GitHub call as the relay stores it: the classification and the status, never the text
// GitHub sent. The cause stays reachable to errors.As.
func githubFailure(kind, key, step string, err error) error {
	failure := readFailure(err)
	text := string(failure.Kind)
	if failure.Status != 0 {
		text += " HTTP " + strconv.Itoa(failure.Status)
	}
	return &opaque{what: fmt.Sprintf("%s %s: %s (%s)", kind, key, step, text), cause: err}
}

// keyParts is the number of ':' separated parts of the keys of each github_comment kind: three, but four for the scope notice,
// whose tail is <pull>:<set digest>.
var keyParts = map[string]int{ackKey: 3, bootstrapSummaryKey: 3, bootstrapPromotedKey: 3, bootstrapNoticeKey: 3, bootstrapCancelledKey: 3, mergeKey: 3, revisionKey: 3, scopeNoticeKey: 4, scopeReviewKey: 4}

// splitKey parses <kind>:<repository id>:<tail> of a github_comment key.
func splitKey(key string) (kind string, repositoryID int64, tail string, ok bool) {
	parts := strings.Split(key, ":")
	if want, known := keyParts[parts[0]]; !known || len(parts) != want {
		return "", 0, "", false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[1] {
		return "", 0, "", false
	}
	return parts[0], id, strings.Join(parts[2:], ":"), true
}

// decodePayload reads a payload strictly: unknown fields, trailing data and non-objects are errors whose text
// never quotes the payload.
func decodePayload[T any](kind, key string, payload json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, keyError(kind, key, "the payload does not decode")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return value, keyError(kind, key, "the payload does not decode")
	}
	return value, nil
}

// render calls the renderer, which panics on a value that cannot be rendered. A panic message quotes the value,
// so it never reaches the error.
func render(kind, key string, fn func() string) (text string, err error) {
	defer func() {
		if recover() != nil {
			text, err = "", keyError(kind, key, "the payload cannot be rendered")
		}
	}()
	return fn(), nil
}

// publish delivers one github_comment message: it renders the body from the payload and the facts it names, and
// posts it unless the bot already commented it (the marker is the idempotency key, so at-least-once delivery
// posts once).
func (p *commentPublisher) publish(ctx context.Context, key string, payload json.RawMessage) error {
	kind, repositoryID, tail, ok := splitKey(key)
	if !ok {
		return keyError(githubCommentKind, key, "not a github_comment key")
	}
	if kind == scopeReviewKey { // posts on the diff, so it has no issue comment to find or to write
		return p.publishReview(ctx, key, repositoryID, tail, payload)
	}
	var (
		target commentTarget
		text   string
		err    error
	)
	switch kind {
	case ackKey:
		target, text, err = p.acknowledgment(ctx, key, tail, payload)
	case bootstrapSummaryKey:
		target, text, err = p.summary(key, tail, payload)
	case bootstrapPromotedKey:
		target, text, err = p.promoted(ctx, key, tail, payload)
	case bootstrapNoticeKey:
		target, text, err = p.notice(key, tail, payload)
	case bootstrapCancelledKey:
		target, text, err = p.cancelled(key, tail, payload)
	case mergeKey:
		target, text, err = p.merge(key, tail, payload)
	case revisionKey:
		target, text, err = p.revision(key, tail, payload)
	case scopeNoticeKey:
		target, text, err = p.scopeNotice(key, tail, payload)
	}
	if err != nil {
		return err
	}
	if target.RepositoryID != repositoryID {
		return keyError(githubCommentKind, key, "the payload does not match its key")
	}
	instance, err := p.instance.get(ctx)
	if err != nil {
		return fmt.Errorf("github_comment %s: read the instance id: %w", key, err)
	}
	botID, err := p.bot.ID(ctx)
	if err != nil {
		return githubFailure(githubCommentKind, key, "resolve the bot account", err)
	}
	existing, err := p.writer.CommentsByAuthor(ctx, target.Repo, target.Pull, botID)
	if err != nil {
		return githubFailure(githubCommentKind, key, "list the comments", err)
	}
	for _, c := range existing {
		if commandtext.HasMarker(c.Body, instance, key) {
			return nil
		}
	}
	body := strings.TrimRight(text, "\n") + "\n\n" + commandtext.Marker(instance, key)
	if _, err := p.writer.PostComment(ctx, target.Repo, target.Pull, body); err != nil {
		return githubFailure(githubCommentKind, key, "post the comment", err)
	}
	return nil
}

func (p *commentPublisher) acknowledgment(ctx context.Context, key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	a, err := decodePayload[ackPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if a.validate() != nil || tail != strconv.FormatInt(a.Comment, 10) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	var ack commandtext.Ack
	if a.Disposition == "" {
		if a.Project != owner.ProjectID(a.RepositoryID) { // the command job writes the repository's own project
			return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
		}
		receipt, err := p.receipt(ctx, key, a)
		if err != nil {
			return commentTarget{}, "", err
		}
		if ack, err = ackOf(receipt, a.Owner, a.Comment); err != nil {
			return commentTarget{}, "", keyError(githubCommentKind, key, err.Error())
		}
	} else if a.declarationMissing() {
		text, err := render(githubCommentKind, key, func() string { return commandtext.SuggestDeclaration(a.suggestion()) })
		return a.commentTarget, text, err
	} else {
		ack = rejectedAckOf(a)
	}
	text, err := render(githubCommentKind, key, func() string { return commandtext.Acknowledge(ack) })
	return a.commentTarget, text, err
}

// commentSource is the source command of the command in the comment with that id (D-COMMANDS).
func commentSource(comment int64) contract.SourceCommandID {
	return contract.SourceCommandID("github-comment:" + strconv.FormatInt(comment, 10))
}

// receipt reads the consent receipt of a processed acknowledgment.
func (p *commentPublisher) receipt(ctx context.Context, key string, a ackPayload) (governance.ConsentReceipt, error) {
	var consent governance.ConsentReceipt
	err := p.suites.Do(ctx, a.Project, a.Suite, func(ctx context.Context, tx governance.Tx) error {
		receipt, found, err := tx.Receipt(ctx, a.Operation)
		switch {
		case err != nil:
			return err
		case !found:
			return errors.New("the operation has no receipt")
		case receipt.Kind != governance.OperationConsent || receipt.Consent == nil || receipt.ProjectID != a.Project || receipt.SuiteID != a.Suite:
			return errors.New("the operation is not a consent of this Suite")
		}
		consent = *receipt.Consent
		return nil
	})
	if err != nil {
		return governance.ConsentReceipt{}, fmt.Errorf("github_comment %s: read the receipt: %w", key, err)
	}
	// Guards against a wrong operation written by a caller: the receipt must be that of the comment's command.
	if consent.Result.Command().SourceCommandID() != commentSource(a.Comment) {
		return governance.ConsentReceipt{}, keyError(githubCommentKind, key, "the receipt is not that of the comment")
	}
	return consent, nil
}

// rejectedAckOf maps a validated rejected payload to the acknowledgment it states.
func rejectedAckOf(a ackPayload) commandtext.Ack {
	d := commands.Disposition(a.Disposition)
	ack := commandtext.Ack{Outcome: rejectedOutcomes[d], Owner: a.Owner, Source: a.Comment}
	if d == commands.UnknownProposal { // the text of UnknownRevision is true for a proposal that does not exist
		ack.Rejection = commandtext.UnknownRevision
	}
	ack.Malformed, _ = codeReason(a.Reason)
	switch d {
	case commands.ProposeRefused:
		ack.Refusal = proposeRefusals[a.Reason]
	case commands.SkipRefused:
		ack.Refusal = skipRefusals[a.Reason]
	}
	if a.Reference != "" {
		ack.Proposal, ack.Revision, _ = parseReference(a.Reference)
	}
	return ack
}

var rejections = map[contract.ConsentReason]commandtext.Rejection{
	contract.ConsentReasonUnauthorized: commandtext.Unauthorized, contract.ConsentReasonUnknownRevision: commandtext.UnknownRevision,
	contract.ConsentReasonSupersededRevision: commandtext.SupersededRevision, contract.ConsentReasonContextMismatch: commandtext.ContextMismatch,
	contract.ConsentReasonPolicyMismatch: commandtext.PolicyMismatch, contract.ConsentReasonObsoleteCommand: commandtext.ObsoleteCommand,
}

// ackOf maps the receipt of a processed command to its acknowledgment (ADR 0011): the outcome as evaluated at
// processing time, and the promotion that already happened for a proposal that is canonical.
func ackOf(receipt governance.ConsentReceipt, ownerLogin string, comment int64) (commandtext.Ack, error) {
	reference := receipt.Result.Command().Reference()
	proposal, revision, ok := parseReference(string(reference.ProposalID) + "-" + string(reference.RevisionID))
	if !ok {
		return commandtext.Ack{}, errors.New("the receipt does not name a P<n>-R<m> reference")
	}
	ack := commandtext.Ack{Proposal: proposal, Revision: revision, Owner: ownerLogin, Source: comment}
	promoted := receipt.PromotedVersionID != ""
	switch receipt.Result.Outcome() {
	case contract.ConsentApproved:
		ack.Outcome = commandtext.Approved
		if promoted {
			ack.Outcome = commandtext.ApprovedAfterPromotion
		}
	case contract.ConsentRevoked:
		ack.Outcome = commandtext.Revoked
		if promoted {
			ack.Outcome = commandtext.RevokedAfterPromotion
		}
	case contract.ConsentNoActiveApproval:
		ack.Outcome = commandtext.NoActiveApproval
		if promoted { // the revocation changed nothing, with or without an active approval
			ack.Outcome = commandtext.RevokedAfterPromotion
		}
	case contract.ConsentRejected:
		ack.Outcome = commandtext.Rejected
		if ack.Rejection, ok = rejections[receipt.Result.Reason()]; !ok {
			return commandtext.Ack{}, errors.New("the receipt holds a rejection with no acknowledgment")
		}
	default:
		return commandtext.Ack{}, errors.New("the receipt holds an unknown outcome")
	}
	// The renderer states the approval requirement only where the outcome evaluated it.
	ack.Eligible = receipt.CurrentApprovalEligible && ack.Outcome != commandtext.ApprovedAfterPromotion && ack.Outcome != commandtext.RevokedAfterPromotion
	return ack, nil
}

func (p *commentPublisher) summary(key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	s, err := decodePayload[bootstrapSummaryPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	proposal, revision, ok := parseReference(tail)
	if !ok || s.validate() != nil || s.Summary.Proposal != proposal || s.Summary.Revision != revision {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	summary, err := s.Summary.summary()
	if err != nil {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload cannot be rendered")
	}
	text, err := render(githubCommentKind, key, func() string { return commandtext.BootstrapSummary(summary) })
	return s.commentTarget, text, err
}

func (p *commentPublisher) promoted(ctx context.Context, key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	b, err := decodePayload[bootstrapPromotedPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	proposal, revision, ok := parseReference(tail)
	if !ok || b.validate() != nil || b.Reference != tail || b.Version != string(firstVersion) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	state, enrolled, err := p.owners.Current(ctx)
	if err != nil {
		return commentTarget{}, "", fmt.Errorf("github_comment %s: read the owner: %w", key, err)
	}
	if !enrolled {
		return commentTarget{}, "", keyError(githubCommentKind, key, "no owner is enrolled")
	}
	text, err := render(githubCommentKind, key, func() string {
		return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.BootstrapPromoted, Proposal: proposal, Revision: revision, Version: 1, Owner: state.Owner.Login})
	})
	return b.commentTarget, text, err
}

func (p *commentPublisher) notice(key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	n, err := decodePayload[bootstrapNoticePayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if n.validate() != nil || tail != strconv.Itoa(n.Pull) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := render(githubCommentKind, key, commandtext.BootstrapNotice)
	return n.commentTarget, text, err
}

func (p *commentPublisher) cancelled(key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	c, err := decodePayload[bootstrapCancelledPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	proposal, revision, ok := parseReference(tail)
	if !ok || c.validate() != nil || c.Reference != tail {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := render(githubCommentKind, key, func() string { return commandtext.BootstrapCancelled(proposal, revision) })
	return c.commentTarget, text, err
}

func parseVersion(s string) (int, bool) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1]) // the pattern admits one to nine digits
	return n, true
}

// mergePayload is the payload of merge:<repository id>:<pull>, the outcome comment of one merged pull request. It
// holds identifiers and codes, never repository text: the publisher renders the body from it.
type mergePayload struct {
	commentTarget
	Kind      string `json:"kind"`                // promoted, corrected or blocked
	Reason    string `json:"reason,omitempty"`    // blocked only
	Source    string `json:"source"`              // the merge commit; empty only for the reason integration-unreadable
	Version   string `json:"version"`             // V<n>: the new version, or the canonical version a blocked merge leaves in force; empty for a blocked merge with no canonical
	Corrects  string `json:"corrects,omitempty"`  // corrected only: V<n> corrected
	Reference string `json:"reference,omitempty"` // P<n>-R<m>: the contract change revision, when the merge had one
	Tier      string `json:"tier"`                // the effective tier: owner.TierIsolated or owner.TierCoLocated
}

var mergeKinds = map[string]commandtext.MergeKind{
	string(governance.MergePromoted): commandtext.MergePromoted, string(governance.MergeCorrected): commandtext.MergeCorrected, string(governance.MergeBlocked): commandtext.MergeBlocked,
}

// merge maps the payload to the values the renderer takes. The renderer checks the rest and panics on what cannot be stored.
func (p mergePayload) merge() (commandtext.Merge, error) {
	kind, known := mergeKinds[p.Kind]
	version, validVersion := parseVersion(p.Version)
	if p.Version == "" && p.Kind == string(governance.MergeBlocked) && p.Corrects == "" && p.Reason != string(governance.MergeReasonIntegrityFailed) {
		validVersion = true // no canonical exists to name: the renderer says so
	}
	m := commandtext.Merge{Kind: kind, Reason: p.Reason, Source: p.Source, Version: version}
	switch {
	case p.validate() != nil || !known || !validVersion:
		return commandtext.Merge{}, errors.New("the kind, the version or the target is invalid")
	case p.Corrects != "":
		var valid bool
		if m.Corrects, valid = parseVersion(p.Corrects); !valid {
			return commandtext.Merge{}, errors.New("the corrected version is invalid")
		}
	}
	if p.Reference != "" {
		var valid bool
		if m.Proposal, m.Revision, valid = parseReference(p.Reference); !valid {
			return commandtext.Merge{}, errors.New("the reference is not P<n>-R<m>")
		}
	}
	switch p.Tier {
	case owner.TierIsolated:
		m.Tier = commandtext.Isolated
	case owner.TierCoLocated:
		m.Tier = commandtext.CoLocated
	default:
		return commandtext.Merge{}, errors.New("unknown tier")
	}
	return m, nil
}

// text renders the comment of the payload stored under key, without quoting the payload in an error.
func (p mergePayload) text(key string) (string, error) {
	m, err := p.merge()
	if err != nil {
		return "", keyError(githubCommentKind, key, "the payload cannot be rendered")
	}
	return render(githubCommentKind, key, func() string { return commandtext.MergeComment(m) })
}

// newMergeMessage builds the outcome comment of a merged pull request for governance.MergeRequest.Message. send is
// false for the outcomes that get no comment; a payload the publisher could not render is an error, so the merge
// is not recorded with a message nobody can post.
func newMergeMessage(target commentTarget, outcome governance.MergeOutcome, tier string) (message governance.OutboxMessage, send bool, err error) {
	switch outcome.Kind {
	case governance.MergeNoChange, governance.MergeNotTarget, governance.MergeBeforeCanonical:
		return governance.OutboxMessage{}, false, nil
	case governance.MergePromoted, governance.MergeCorrected, governance.MergeBlocked:
	default:
		return governance.OutboxMessage{}, false, fmt.Errorf("merge message: unknown outcome kind %q", outcome.Kind)
	}
	p := mergePayload{commentTarget: target, Kind: string(outcome.Kind), Reason: string(outcome.Reason), Source: string(outcome.Source), Version: string(outcome.Version),
		Corrects: string(outcome.Corrects), Tier: tier}
	if outcome.Reference != nil { // the contract change revision is optional: a merge without a proposal has none
		p.Reference = string(outcome.Reference.ProposalID) + "-" + string(outcome.Reference.RevisionID)
	}
	key := fmt.Sprintf("%s:%d:%d", mergeKey, target.RepositoryID, target.Pull)
	if _, err := p.text(key); err != nil {
		return governance.OutboxMessage{}, false, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return governance.OutboxMessage{}, false, fmt.Errorf("encode the merge message: %w", err)
	}
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: payload}, true, nil
}

func (p *commentPublisher) merge(key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	m, err := decodePayload[mergePayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if m.validate() != nil || tail != strconv.Itoa(m.Pull) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := m.text(key)
	return m.commentTarget, text, err
}

// revisionPayload is the payload of revision:<repository id>:P<n>-R<m>, the comment about one new change-proposal
// revision. The lists hold repository text, so they are bounded like the bootstrap summary: at most maxSummaryRun
// entries of at most maxSummaryText bytes each, with the true totals beside them.
type revisionPayload struct {
	commentTarget
	Reference     string   `json:"reference"`          // P<n>-R<m>
	Previous      string   `json:"previous,omitempty"` // the revision it supersedes
	Canonical     string   `json:"canonical"`          // V<n>, the base version
	Added         []string `json:"added"`
	Modified      []string `json:"modified"`
	Removed       []string `json:"removed"`
	AddedTotal    int      `json:"addedTotal"`
	ModifiedTotal int      `json:"modifiedTotal"`
	RemovedTotal  int      `json:"removedTotal"`
	Reduced       bool     `json:"reduced"`
	// ReducedRules, ReducedPaths and ReducedPathsTotal detail a reduction, bounded like the lists above; only with Reduced.
	ReducedRules      []string `json:"reducedRules,omitempty"`
	ReducedPaths      []string `json:"reducedPaths,omitempty"`
	ReducedPathsTotal int      `json:"reducedPathsTotal,omitempty"`
	Tier              string   `json:"tier"` // the effective tier: owner.TierIsolated or owner.TierCoLocated
}

// revision maps the payload to the values the renderer takes. The renderer checks the rest and panics on what cannot be stored.
func (p revisionPayload) revision() (commandtext.Revision, error) {
	proposal, revision, validReference := parseReference(p.Reference)
	canonical, validCanonical := parseVersion(p.Canonical)
	var tier commandtext.Tier
	switch p.Tier {
	case owner.TierIsolated:
		tier = commandtext.Isolated
	case owner.TierCoLocated:
		tier = commandtext.CoLocated
	}
	if p.validate() != nil || !validReference || !validCanonical || tier == 0 || min(p.AddedTotal, p.ModifiedTotal, p.RemovedTotal, p.ReducedPathsTotal) < 0 {
		return commandtext.Revision{}, errors.New("the reference, the base version, a total or the tier is invalid")
	}
	if !p.Reduced && (len(p.ReducedRules) > 0 || len(p.ReducedPaths) > 0 || p.ReducedPathsTotal != 0) {
		return commandtext.Revision{}, errors.New("reduction detail on a revision that does not reduce the scope")
	}
	return commandtext.Revision{Proposal: proposal, Revision: revision, Previous: p.Previous, Canonical: canonical, Added: p.Added, Modified: p.Modified, Removed: p.Removed,
		AddedTotal: p.AddedTotal, ModifiedTotal: p.ModifiedTotal, RemovedTotal: p.RemovedTotal, Reduced: p.Reduced, Tier: tier,
		ReducedRules: p.ReducedRules, ReducedPaths: p.ReducedPaths, ReducedPathsTotal: p.ReducedPathsTotal}, nil
}

// text renders the comment of the payload stored under key, without quoting the payload in an error.
func (p revisionPayload) text(key string) (string, error) {
	r, err := p.revision()
	if err != nil {
		return "", keyError(githubCommentKind, key, "the payload cannot be rendered")
	}
	return render(githubCommentKind, key, func() string { return commandtext.RevisionComment(r) })
}

func referenceText(r contract.ProposalReference) string {
	return string(r.ProposalID) + "-" + string(r.RevisionID)
}

// newRevisionMessage builds the revision comment of a revision AssessHead committed, for HeadRequest.Revision. A
// payload the publisher could not render is an error, so the revision is not committed with a message nobody can post.
func newRevisionMessage(target commentTarget, notice governance.RevisionNotice, tier string) (governance.OutboxMessage, error) {
	p := revisionPayload{commentTarget: target, Reference: referenceText(notice.Reference), Canonical: string(notice.Canonical),
		Added: boundedText(notice.Added), Modified: boundedText(notice.Modified), Removed: boundedText(notice.Removed),
		AddedTotal: len(notice.Added), ModifiedTotal: len(notice.Modified), RemovedTotal: len(notice.Removed), Reduced: notice.Reduced, Tier: tier}
	p.ReducedRules, p.ReducedPaths, p.ReducedPathsTotal = boundedText(notice.ReducedRules), boundedText(notice.ReducedPaths), len(notice.ReducedPaths)
	if notice.Previous != nil {
		p.Previous = referenceText(*notice.Previous)
	}
	if _, err := p.text(revisionKey); err != nil { // before the key, which would quote a reference that is not one
		return governance.OutboxMessage{}, err
	}
	key := fmt.Sprintf("%s:%d:%s", revisionKey, target.RepositoryID, p.Reference)
	payload, err := json.Marshal(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the revision message: %w", err)
	}
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: payload}, nil
}

func (p *commentPublisher) revision(key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	r, err := decodePayload[revisionPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if r.validate() != nil || r.Reference != tail {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := r.text(key)
	return r.commentTarget, text, err
}
