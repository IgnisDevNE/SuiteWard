package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// Scope discovery after V1 (docs/contracts/runtime.md, "Scope discovery (M1.9)"; D-SCOPE-DISCOVERY): the checks job tells the
// owner which test-like paths a pull request adds outside the protected scope. It is evidence: it never changes a check and
// protects nothing.

const (
	scopeNoticeKey = "scope-notice" // scope-notice:<repository id>:<pull>:<set digest>
	scopeReviewKey = "scope-review" // scope-review:<repository id>:<pull>:<review digest>: the set and the head's file in one digest
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// scopeNoticePayload is the payload of scope-notice:<repository id>:<pull>:<set digest>. The key's digest is that of the
// whole set of added paths, which the bounded list below cannot reproduce, so the publisher checks only its shape.
type scopeNoticePayload struct {
	commentTarget
	Head        string   `json:"head"`
	Branch      string   `json:"branch"`
	Paths       []string `json:"paths"` // at most 20, each cut to 150 bytes
	PathsTotal  int      `json:"pathsTotal"`
	Declaration string   `json:"declaration"` // the canonical declaration with the new patterns added, at most 8192 bytes
	Mixed       []string `json:"mixed,omitempty"`
	Submodules  []string `json:"submodules,omitempty"` // the submodules a suggested pattern reaches, whose contents are not protected
	Cut         bool     `json:"cut,omitempty"`        // the limit of 20 patterns cut the declaration's new patterns
}

func (p scopeNoticePayload) notice() commandtext.ScopeNotice {
	return commandtext.ScopeNotice{Repo: p.Repo, Branch: p.Branch, Paths: p.Paths, PathsTotal: p.PathsTotal, Declaration: []byte(p.Declaration), Mixed: p.Mixed, Submodules: p.Submodules, Cut: p.Cut}
}

// validate reports why the payload cannot be rendered, without quoting it.
func (p scopeNoticePayload) validate() error {
	if err := p.commentTarget.validate(); err != nil {
		return err
	}
	switch {
	case !headPattern.MatchString(p.Head):
		return errors.New("the head is not a commit SHA")
	case p.PathsTotal < len(p.Paths):
		return errors.New("the total is below the listed paths")
	}
	for _, text := range slices.Concat(p.Paths, p.Mixed, p.Submodules) {
		if len(text) > maxSummaryText {
			return errors.New("a path is too long")
		}
	}
	if commandtext.ValidateScopeNotice(p.notice()) != nil { // its text quotes the repository and the branch, so only a fixed reason is kept
		return errors.New("the repository, the branch, the paths or the declaration do not fit the notice")
	}
	return nil
}

// scopeTail is whether tail is <pull>:<set digest> of the pull request.
func scopeTail(tail string, pull int) bool {
	number, digest, ok := strings.Cut(tail, ":")
	return ok && number == strconv.Itoa(pull) && digestPattern.MatchString(digest)
}

// setDigest is the SHA-256 hex digest of the sorted paths joined by newlines.
func setDigest(paths []string) string {
	sum := sha256.Sum256([]byte(strings.Join(paths, "\n")))
	return hex.EncodeToString(sum[:])
}

// newScopeNoticeMessage builds the notice of the added uncovered paths, bounding the repository text it carries. A payload
// the publisher could not render is an error, so nobody writes a message that nobody can post.
func newScopeNoticeMessage(target commentTarget, head, branch string, paths []string, declaration []byte, mixed, submodules []string, cut bool) (governance.OutboxMessage, error) {
	p := scopeNoticePayload{commentTarget: target, Head: head, Branch: branch, Paths: boundedText(paths), PathsTotal: len(paths), Declaration: string(declaration), Mixed: boundedText(mixed), Submodules: boundedText(submodules), Cut: cut}
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("scope notice: %w", err)
	}
	payload, err := marshalPayload(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the scope notice: %w", err)
	}
	key := fmt.Sprintf("%s:%d:%d:%s", scopeNoticeKey, target.RepositoryID, target.Pull, setDigest(paths))
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: payload}, nil
}

func (p *commentPublisher) scopeNotice(key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	n, err := decodePayload[scopeNoticePayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if n.validate() != nil || !scopeTail(tail, n.Pull) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := render(githubCommentKind, key, func() string { return commandtext.ScopeNoticeComment(n.notice()) })
	return n.commentTarget, text, err
}

// scopeSuggestion is one review comment of a scope review: the new patterns of one list of the head's .suiteward.yml, with the
// list item (line and text) they are suggested after. Its marker key is the message's key and ":" and List.
type scopeSuggestion struct {
	List     string   `json:"list"`     // include or runner, for a scope notice; protect or ignore, for a runner-uncovered check
	Line     int      `json:"line"`     // the line of the list item in the head's file
	Anchor   string   `json:"anchor"`   // that line without its line break
	Patterns []string `json:"patterns"` // the new patterns, none of which the head's list holds
	// NewList, for a choice only, names the list the suggestion creates after the anchor, the last item of another list: runner for protect, exclude for ignore.
	// KeyIndent is then the spaces before its key.
	NewList   string `json:"newList,omitempty"`
	KeyIndent string `json:"keyIndent,omitempty"`
}

// choices are the lists of a runner-uncovered check's review suggestions, each the commandtext choice it renders.
var choices = map[string]commandtext.RunnerChoice{"protect": commandtext.ProtectChoice, "ignore": commandtext.IgnoreChoice}

// indent is the spaces before the item's dash, or before the key of the list a suggestion creates.
func (s scopeSuggestion) indent() string {
	if s.NewList != "" {
		return s.KeyIndent
	}
	return s.Anchor[:len(s.Anchor)-len(strings.TrimLeft(s.Anchor, " "))]
}

// validate reports why the suggestion cannot be rendered. The error quotes nothing: its text may be kept.
func (s scopeSuggestion) validate() error {
	choice, isChoice := choices[s.List]
	switch {
	case s.List != "include" && s.List != "runner" && !isChoice:
		return errors.New("the list is unknown")
	case s.Line <= 0:
		return errors.New("the line is not positive")
	case !isChoice && (s.NewList != "" || s.KeyIndent != ""):
		return errors.New("only a choice creates a list")
	case s.NewList == "" && s.KeyIndent != "":
		return errors.New("an indent without a created list")
	}
	var err error
	if isChoice {
		err = commandtext.ValidateRunnerChoice(choice, s.Anchor, s.indent(), s.Patterns, s.NewList)
	} else {
		err = commandtext.ValidateScopeSuggestion(s.Anchor, s.indent(), s.Patterns)
	}
	if err != nil { // its text quotes the anchor line and the patterns
		return errors.New("the anchor line, its indent or the patterns do not fit the suggestion")
	}
	return nil
}

// body renders the review comment of the suggestion; it panics on one that does not validate.
func (s scopeSuggestion) body() string {
	if choice, isChoice := choices[s.List]; isChoice {
		return commandtext.RunnerChoiceBody(choice, s.Anchor, s.indent(), s.Patterns, s.NewList)
	}
	return commandtext.ScopeSuggestionBody(s.Anchor, s.indent(), s.Patterns)
}

// scopeReviewPayload is the payload of scope-review:<repository id>:<pull>:<review digest>: one or two suggestions, the include
// list's and the runner list's, for one version of the head's file.
type scopeReviewPayload struct {
	commentTarget
	Head        string            `json:"head"`
	Suggestions []scopeSuggestion `json:"suggestions"`
}

// validate reports why the payload cannot be rendered, without quoting it.
func (p scopeReviewPayload) validate() error {
	if err := p.commentTarget.validate(); err != nil {
		return err
	}
	if !headPattern.MatchString(p.Head) || len(p.Suggestions) < 1 || len(p.Suggestions) > 2 {
		return errors.New("the head is not a commit SHA, or the number of suggestions is not one or two")
	}
	seen := map[string]bool{}
	for _, s := range p.Suggestions {
		if seen[s.List] {
			return errors.New("a suggestion names a repeated list")
		}
		seen[s.List] = true
		if err := s.validate(); err != nil {
			return err
		}
	}
	return nil
}

// reviewDigest is the SHA-256 hex digest of the set digest, a newline and the head's .suiteward.yml. A review is anchored to one
// version of that file: a later suggest after the file changed is a fresh review, a repeat on the same file is not.
func reviewDigest(set string, file []byte) string {
	sum := sha256.Sum256(append([]byte(set+"\n"), file...))
	return hex.EncodeToString(sum[:])
}

// newScopeReviewMessage builds the review of the suggestions of the review digest. A payload the publisher could not render is
// an error.
func newScopeReviewMessage(target commentTarget, head, digest string, suggestions []scopeSuggestion) (governance.OutboxMessage, error) {
	p := scopeReviewPayload{commentTarget: target, Head: head, Suggestions: suggestions}
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("scope review: %w", err)
	}
	payload, err := marshalPayload(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the scope review: %w", err)
	}
	key := fmt.Sprintf("%s:%d:%d:%s", scopeReviewKey, target.RepositoryID, target.Pull, digest)
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: payload}, nil
}

// publishReview posts each suggestion of the message as a review comment on its line of the head's .suiteward.yml, unless the
// bot already posted it (the marker of the list is the idempotency key). GitHub answers 422 when the line is no longer in the
// diff: that suggestion is dropped, logged by kind and status, and the message still completes.
func (p *commentPublisher) publishReview(ctx context.Context, key string, repositoryID int64, tail string, payload json.RawMessage) error {
	r, err := decodePayload[scopeReviewPayload](githubCommentKind, key, payload)
	if err != nil {
		return err
	}
	if r.validate() != nil || r.RepositoryID != repositoryID || !scopeTail(tail, r.Pull) {
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
	existing, err := p.writer.ReviewCommentsByAuthor(ctx, r.Repo, r.Pull, botID)
	if err != nil {
		return githubFailure(githubCommentKind, key, "list the review comments", err)
	}
	for _, s := range r.Suggestions {
		marked := key + ":" + s.List
		if slices.ContainsFunc(existing, func(c github.Comment) bool { return commandtext.HasMarker(c.Body, instance, marked) }) {
			continue
		}
		text, err := render(githubCommentKind, key, s.body)
		if err != nil {
			return err
		}
		body := strings.TrimRight(text, "\n") + "\n\n" + commandtext.Marker(instance, marked)
		_, err = p.writer.PostReviewComment(ctx, r.Repo, r.Pull, r.Head, scope.DeclarationPath, s.Line, s.Line, body)
		switch {
		case errors.Is(err, rest.ErrUnprocessable):
			failure := readFailure(err)
			p.logger.WarnContext(ctx, "github_comment: a review suggestion is no longer on the diff", "key", key, "list", s.List, "kind", string(failure.Kind), "status", failure.Status)
		case err != nil:
			return githubFailure(githubCommentKind, key, "post the review comment", err)
		}
	}
	return nil
}

// errNoCanonicalDeclaration says that the canonical holds no .suiteward.yml, so there is no declaration to widen.
var errNoCanonicalDeclaration = governance.ErrNoCanonicalDeclaration

// canonicalDeclaration returns the bytes of the canonical's own .suiteward.yml and the scope they declare. It reads the
// canonical as AssessHead does and verifies the bytes against the scope digest the canonical was approved with.
func canonicalDeclaration(ctx context.Context, suites governance.UnitOfWork, contents governance.ContentReader, project contract.ProjectID) ([]byte, scope.Scope, error) {
	var canonical contract.CanonicalSnapshot
	err := suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		canonical = state.Canonical
		return nil
	})
	if err != nil {
		return nil, scope.Scope{}, err
	}
	raw, declaration, err := governance.CanonicalDeclaration(ctx, contents, canonical)
	if err != nil {
		return nil, scope.Scope{}, err
	}
	return raw, scope.NewScope(declaration), nil
}

// headScope is what scope discovery finds in the head of a pull request under a base declaration: the canonical's, for the
// checks job's scope notice, and before the first contract the suggest command's own pick (the head's file or the default
// branch's).
type headScope struct {
	entries []inventory.Entry
	// Paths are the test-like paths that neither the governing scope nor the head's own valid declaration covers, sorted.
	Paths []string
	// Mixed are the mixed files of the head (at most 20), which may hold test settings and are never suggested.
	Mixed []string
	// Submodules are the submodules of the head (at most 20) that the patterns Suggest builds from the head reach: their contents are outside the scope.
	Submodules []string
	base       []byte      // the base declaration, which a suggested declaration starts from: never the head's file after the first contract
	governing  scope.Scope // the scope that base declares
}

// discoverHead finds the uncovered test-like paths of tree, the head of a pull request, under the canonical of project.
// The head's own declaration, when it is valid, only silences: a path it covers is not reported, though covering it still
// needs the owner's approval. The head's file is read only when something is uncovered. Only the checks job silences: the
// suggest command compares strictly with the canonical, through discoverUnder.
func discoverHead(ctx context.Context, suites governance.UnitOfWork, contents governance.ContentReader, project contract.ProjectID, tree inventory.Tree) (headScope, error) {
	raw, governing, err := canonicalDeclaration(ctx, suites, contents, project)
	if err != nil {
		return headScope{}, err
	}
	found, err := discoverUnder(ctx, tree, raw, governing)
	if err != nil || len(found.Paths) == 0 || !hasDeclaration(found.entries) {
		return found, err
	}
	own, err := tree.Read(ctx, scope.DeclarationPath)
	if err != nil {
		return headScope{}, err
	}
	declaration, err := scope.ParseDeclaration(own)
	switch {
	case errors.Is(err, scope.ErrInvalidDeclaration): // the governing scope stays in force
	case err != nil:
		return headScope{}, err
	default:
		if found.Paths, err = withoutCovered(found.Paths, scope.NewScope(declaration)); err != nil {
			return headScope{}, err
		}
	}
	return found, nil
}

// discoverUnder finds the test-like paths of tree, the head of a pull request, that the declaration raw, which declares governing,
// does not cover, with the mixed files and the submodules of the head. It consults no other declaration.
func discoverUnder(ctx context.Context, tree inventory.Tree, raw []byte, governing scope.Scope) (headScope, error) {
	entries, err := tree.Entries(ctx)
	if err != nil {
		return headScope{}, err
	}
	paths, err := inventory.Uncovered(entries, governing)
	if err != nil {
		return headScope{}, fmt.Errorf("find the uncovered paths: %w", err)
	}
	suggested := inventory.Suggest(entries)
	return headScope{entries: entries, Paths: paths, Mixed: suggested.Mixed, Submodules: suggested.Submodules, base: raw, governing: governing}, nil
}

// withoutCovered returns the paths that s does not cover, in order.
func withoutCovered(paths []string, s scope.Scope) ([]string, error) {
	kept := []string{}
	for _, path := range paths {
		covered, err := s.Covers(path)
		if err != nil {
			return nil, fmt.Errorf("find the uncovered paths: %w", err)
		}
		if !covered {
			kept = append(kept, path)
		}
	}
	return kept, nil
}

// declaration returns the base declaration with the new patterns for paths added, and the suggestion those patterns
// come from. After the first contract the base is the canonical's, never the head's file.
func (h headScope) declaration(paths []string) (inventory.Suggestion, []byte, error) {
	suggestion, err := h.pathsSuggestion(paths)
	if err != nil {
		return inventory.Suggestion{}, nil, err
	}
	return h.declarationWith(suggestion, nil)
}

// pathsSuggestion is the suggestion for the files at paths, without the runner patterns the base lists or covers: a Python test file brings the
// pytest family, which the base may hold already.
func (h headScope) pathsSuggestion(paths []string) (inventory.Suggestion, error) {
	entries := make([]inventory.Entry, len(paths))
	for i, path := range paths {
		entries[i] = inventory.Entry{Path: path, Kind: inventory.File}
	}
	suggestion, err := inventory.SuggestAgainst(entries, h.governing)
	if err != nil {
		return inventory.Suggestion{}, fmt.Errorf("suggest the patterns for the paths: %w", err)
	}
	return suggestion, nil
}

// declarationWith returns the base declaration with the patterns of suggestion and the runner patterns extra added, and the suggestion
// with extra in it.
func (h headScope) declarationWith(suggestion inventory.Suggestion, extra []string) (inventory.Suggestion, []byte, error) {
	suggestion = suggestion.WithRunner(extra)
	declaration, err := scope.AddPatterns(h.base, suggestion.Include, suggestion.Runner)
	return suggestion, declaration, err
}

// scopeNotice decides, once for each head and canonical of a pull request, whether the head adds test-like paths outside the
// governing scope, and writes the notice for a set that has none yet. A failed GitHub read leaves the decision for the next
// run; every other end of the decision is remembered, so the base tree is read at most once for a head.
func (j *checksJob) scopeNotice(ctx context.Context, p owner.ProjectPolicy, pull githubsync.PullState, canonical contract.SuiteVersionID) error {
	key, at := checkPull{p.RepositoryID, pull.Number}, unchangedAt{pull.HeadSHA, canonical}
	if j.scoped[key] == at {
		return nil
	}
	err := j.decideScope(ctx, p, pull)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	// Before the read failure, which wraps an invalid argument such as a base commit that is not a SHA: that will never succeed.
	case invalidSource(err) || errors.Is(err, github.ErrInvalidArgument) || errors.Is(err, errNoCanonicalDeclaration):
		j.logger.InfoContext(ctx, "github checks scope notice skipped: the source cannot be read", "repo", p.Repo, "pull", pull.Number)
	case errors.Is(err, errGitHubRead):
		logReadFailure(ctx, j.logger, "github checks scope notice read failed", p.Repo, err)
		return nil
	case err != nil:
		return &opaque{what: "decide the scope notice", cause: err}
	}
	if j.scoped == nil {
		j.scoped = map[checkPull]unchangedAt{}
	}
	j.scoped[key] = at
	return nil
}

// decideScope reads the head tree, then the base tree only when the head has uncovered paths that no earlier head of the pull
// request had, and writes the notice. The set of uncovered paths of the head, which is the added set when the pull request
// adds them all, is remembered once decided: a push that leaves it as it was reads nothing more.
func (j *checksJob) decideScope(ctx context.Context, p owner.ProjectPolicy, pull githubsync.PullState) error {
	read, err := j.reader.Tree(ctx, p.Repo, pull.HeadSHA)
	if err != nil {
		return githubRead(err)
	}
	head := &githubTree{tree: read, files: map[string][]byte{}}
	found, err := discoverHead(ctx, j.suites, j.contents, owner.ProjectID(p.RepositoryID), head)
	if err != nil || len(found.Paths) == 0 {
		return err
	}
	decided := fmt.Sprintf("scope-set:%d:%d:%s", p.RepositoryID, pull.Number, setDigest(found.Paths))
	if j.announced[decided] {
		return nil
	}
	if err := j.reportAdded(ctx, p, pull, head, found); err != nil {
		return err
	}
	if j.announced == nil {
		j.announced = map[string]bool{}
	}
	j.announced[decided] = true
	return nil
}

// reportAdded keeps the uncovered paths that the base commit lacks and writes the notice for them, with its review.
func (j *checksJob) reportAdded(ctx context.Context, p owner.ProjectPolicy, pull githubsync.PullState, head *githubTree, found headScope) error {
	project := owner.ProjectID(p.RepositoryID)
	added, err := j.absentFromBase(ctx, p.Repo, pull.BaseSHA, found.Paths)
	if err != nil || len(added) == 0 {
		return err
	}
	suggestion, declaration, err := found.declaration(added)
	if err != nil {
		return err
	}
	if len(declaration) > commandtext.MaxDeclaration {
		j.logger.InfoContext(ctx, "github checks scope notice skipped: the declaration is over the limit", "repo", p.Repo, "pull", pull.Number)
		return nil
	}
	target := commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull.Number}
	notice, err := newScopeNoticeMessage(target, pull.HeadSHA, pull.HeadRef, added, declaration, found.Mixed, found.Submodules, suggestion.Cut)
	if err != nil {
		j.logger.WarnContext(ctx, "github checks scope notice skipped: it cannot be rendered", "repo", p.Repo, "pull", pull.Number, "reason", err.Error())
		return nil
	}
	reviews, err := scopeReview(ctx, j.reader, j.logger, "github checks scope review", target, pull.HeadSHA, head, found, suggestion, setDigest(added))
	if err != nil {
		return err
	}
	return outboxOnce(ctx, j.suites, project, append([]governance.OutboxMessage{notice}, reviews...))
}

// pullFileReader is the part of the GitHub reader that lists the files a pull request changes.
type pullFileReader interface {
	PullFiles(ctx context.Context, repo string, number int) ([]github.PullFile, error)
}

// scopeReview builds the review suggestions that go with a notice, as at most one message, keyed by the set digest and the head's file: for each list of the head's
// .suiteward.yml, the new patterns that the head's list lacks, after a list item inside a hunk of the pull request's diff
// (GitHub accepts a review comment only on such a line). This is the only place that starts from the head's file. A list in flow
// style, a head whose file is invalid or missing from the diff, and a list with no new pattern or no item in a hunk give none.
// A failed read of the pull request's files costs only the review, unless it says to wait.
// The checks job and the suggest command share it, each with its own prefix for the log lines.
func scopeReview(ctx context.Context, files pullFileReader, logger *slog.Logger, logPrefix string, target commentTarget, headSHA string, head *githubTree, found headScope, s inventory.Suggestion, digest string) ([]governance.OutboxMessage, error) {
	if !hasDeclaration(found.entries) {
		return nil, nil
	}
	own, err := head.Read(ctx, scope.DeclarationPath)
	if err != nil {
		return nil, err
	}
	switch _, err := scope.ParseDeclaration(own); {
	case errors.Is(err, scope.ErrInvalidDeclaration): // the head's file may be invalid: the notice still stands
		return nil, nil
	case err != nil:
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(own, &root); err != nil {
		return nil, fmt.Errorf("read the head's %s as YAML: %w", scope.DeclarationPath, err)
	}
	top := root.Content[0] // ParseDeclaration accepted own: one document holding a mapping
	changed, err := files.PullFiles(ctx, target.Repo, target.Pull)
	if err != nil {
		if kind := readFailure(err).Kind; kind == githubsync.FailureRateLimited || kind == githubsync.FailureBudgetExhausted {
			return nil, githubRead(err)
		}
		logReadFailure(ctx, logger, logPrefix+" skipped: the files of the pull request cannot be read", target.Repo, err)
		return nil, nil
	}
	i := slices.IndexFunc(changed, func(f github.PullFile) bool { return f.Path == scope.DeclarationPath })
	if i < 0 {
		return nil, nil
	}
	hunks, err := changed[i].NewLineRanges()
	if err != nil {
		logger.WarnContext(ctx, logPrefix+" skipped: the diff of the declaration cannot be read", "repo", target.Repo, "pull", target.Pull)
		return nil, nil
	}
	var suggestions []scopeSuggestion
	for _, list := range []struct {
		name     string
		patterns []string
	}{{"include", s.Include}, {"runner", s.Runner}} {
		if suggested, ok := listSuggestion(top, strings.Split(string(own), "\n"), list.name, list.patterns, hunks); ok {
			suggestions = append(suggestions, suggested)
		}
	}
	if len(suggestions) == 0 {
		return nil, nil
	}
	message, err := newScopeReviewMessage(target, headSHA, reviewDigest(digest, own), suggestions)
	if err != nil {
		logger.WarnContext(ctx, logPrefix+" skipped: it cannot be rendered", "repo", target.Repo, "pull", target.Pull, "reason", err.Error())
		return nil, nil
	}
	return []governance.OutboxMessage{message}, nil
}

// listSuggestion is the suggestion of patterns for the list called name in the declaration top (a mapping node, whose text is
// lines): the last item of the block-style list that lies in a hunk is the anchor, and patterns the list already holds are
// left out. False when there is nothing to suggest.
func listSuggestion(top *yaml.Node, lines []string, name string, patterns []string, hunks []github.LineRange) (scopeSuggestion, bool) {
	for i := 0; i+1 < len(top.Content); i += 2 {
		list := top.Content[i+1]
		if top.Content[i].Value != name || list.Kind != yaml.SequenceNode || list.Style&yaml.FlowStyle != 0 {
			continue
		}
		present := map[string]bool{}
		for _, item := range list.Content {
			present[item.Value] = true
		}
		fresh := slices.DeleteFunc(slices.Clone(patterns), func(pattern string) bool { return present[pattern] })
		if len(fresh) == 0 {
			return scopeSuggestion{}, false
		}
		for k := len(list.Content) - 1; k >= 0; k-- {
			item := list.Content[k]
			if !slices.ContainsFunc(hunks, func(h github.LineRange) bool { return item.Line >= h.Start && item.Line <= h.End }) {
				continue
			}
			anchor := strings.TrimSuffix(lines[item.Line-1], "\r")
			if singleLineItem(anchor, item) {
				return scopeSuggestion{List: name, Line: item.Line, Anchor: anchor, Patterns: fresh}, true
			}
		}
	}
	return scopeSuggestion{}, false
}

// singleLineItem reports whether the line is a whole list item that holds item's value: a scalar that spans lines, or a block
// scalar, would be cut in two by a suggestion after its first line.
func singleLineItem(line string, item *yaml.Node) bool {
	if item.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return false
	}
	rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "- ")
	var value string
	return ok && yaml.Unmarshal([]byte(rest), &value) == nil && value == item.Value
}

// absentFromBase returns the paths that the tree of the base commit does not hold: the ones the pull request adds.
func (j *checksJob) absentFromBase(ctx context.Context, repo, baseSHA string, paths []string) ([]string, error) {
	read, err := j.reader.Tree(ctx, repo, baseSHA)
	if err != nil {
		return nil, githubRead(err)
	}
	entries, err := (&githubTree{tree: read}).Entries(ctx)
	if err != nil {
		return nil, err
	}
	inBase := make(map[string]bool, len(entries))
	for _, entry := range entries {
		inBase[entry.Path] = true
	}
	return slices.DeleteFunc(slices.Clone(paths), func(path string) bool { return inBase[path] }), nil
}

// outboxOnce writes the messages in one unit of work, unless the key of the first exists: a repeated decision writes nothing.
// The first is the notice, written before the messages that depend on it, so its conflict undoes nothing else. No message is no write.
func outboxOnce(ctx context.Context, suites governance.UnitOfWork, project contract.ProjectID, messages []governance.OutboxMessage) error {
	if len(messages) == 0 {
		return nil
	}
	err := suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		for _, message := range messages {
			if err := tx.Outbox(ctx, message); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, governance.ErrOperationConflict) {
		return fmt.Errorf("write the comment %s: %w", messages[0].Key, err)
	}
	return nil
}
