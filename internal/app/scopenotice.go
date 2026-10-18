package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
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
	// maxScopeDeclaration is commandtext's bound on the declaration a notice shows.
	maxScopeDeclaration = 8192
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
}

func (p scopeNoticePayload) notice() commandtext.ScopeNotice {
	return commandtext.ScopeNotice{Repo: p.Repo, Branch: p.Branch, Paths: p.Paths, PathsTotal: p.PathsTotal, Declaration: []byte(p.Declaration), Mixed: p.Mixed}
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
	for _, text := range slices.Concat(p.Paths, p.Mixed) {
		if len(text) > maxSummaryText {
			return errors.New("a path is too long")
		}
	}
	return commandtext.ValidateScopeNotice(p.notice())
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
func newScopeNoticeMessage(target commentTarget, head, branch string, paths []string, declaration []byte, mixed []string) (governance.OutboxMessage, error) {
	p := scopeNoticePayload{commentTarget: target, Head: head, Branch: branch, Paths: boundedText(paths), PathsTotal: len(paths), Declaration: string(declaration), Mixed: boundedText(mixed)}
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("scope notice: %w", err)
	}
	payload, err := json.Marshal(p)
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

// errNoCanonicalDeclaration says that the canonical holds no .suiteward.yml, so there is no declaration to widen.
var errNoCanonicalDeclaration = errors.New("the canonical has no declaration")

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
	protected := canonical.Contract()
	for _, entry := range protected.Manifest().Entries() {
		if entry.Path != scope.DeclarationPath {
			continue
		}
		raw, err := contents.Read(ctx, entry.Content)
		if err != nil {
			return nil, scope.Scope{}, fmt.Errorf("read the canonical %s: %w", scope.DeclarationPath, err)
		}
		declaration, err := scope.ParseDeclaration(raw)
		if err != nil { // stored state, not the owner's source under review
			return nil, scope.Scope{}, fmt.Errorf("%w: the canonical %s does not parse: %v", governance.ErrInvalidState, scope.DeclarationPath, err)
		}
		governing := scope.NewScope(declaration)
		if governing.Digest() != protected.ScopeDigest() {
			return nil, scope.Scope{}, fmt.Errorf("%w: the canonical's declaration does not hash to its approved scope", governance.ErrInvalidState)
		}
		return raw, governing, nil
	}
	return nil, scope.Scope{}, errNoCanonicalDeclaration
}

// headScope is what scope discovery finds in the head of a pull request under the canonical contract. The checks job uses
// it for the scope notice, and the suggest command of the commands job can use it for the same answer.
type headScope struct {
	entries []inventory.Entry
	// Paths are the test-like paths that neither the governing scope nor the head's own valid declaration covers, sorted.
	Paths []string
	// Mixed are the mixed files of the head (at most 20), which may hold test settings and are never suggested.
	Mixed     []string
	canonical []byte // the canonical's declaration, which a suggested declaration starts from
}

// discoverHead finds the uncovered test-like paths of tree, the head of a pull request, under the canonical of project.
// The head's own declaration, when it is valid, only silences: a path it covers is not reported, though covering it still
// needs the owner's approval. The head's file is read only when something is uncovered.
func discoverHead(ctx context.Context, suites governance.UnitOfWork, contents governance.ContentReader, project contract.ProjectID, tree inventory.Tree) (headScope, error) {
	raw, governing, err := canonicalDeclaration(ctx, suites, contents, project)
	if err != nil {
		return headScope{}, err
	}
	entries, err := tree.Entries(ctx)
	if err != nil {
		return headScope{}, err
	}
	paths, err := inventory.Uncovered(entries, governing)
	if err != nil {
		return headScope{}, fmt.Errorf("find the uncovered paths: %w", err)
	}
	if len(paths) > 0 && hasDeclaration(entries) {
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
			if paths, err = withoutCovered(paths, scope.NewScope(declaration)); err != nil {
				return headScope{}, err
			}
		}
	}
	return headScope{entries: entries, Paths: paths, Mixed: inventory.Suggest(entries).Mixed, canonical: raw}, nil
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

// declaration returns the canonical declaration with the new patterns for paths added, and the suggestion those patterns
// come from. It never starts from the head's file.
func (h headScope) declaration(paths []string) (inventory.Suggestion, []byte, error) {
	entries := make([]inventory.Entry, len(paths))
	for i, path := range paths {
		entries[i] = inventory.Entry{Path: path, Kind: inventory.File}
	}
	suggestion := inventory.Suggest(entries)
	declaration, err := scope.AddPatterns(h.canonical, suggestion.Include, suggestion.Runner)
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
	case errors.Is(err, errGitHubRead):
		logReadFailure(ctx, j.logger, "github checks scope notice read failed", p.Repo, err)
		return nil
	case invalidSource(err) || errors.Is(err, github.ErrInvalidArgument) || errors.Is(err, errNoCanonicalDeclaration):
		j.logger.InfoContext(ctx, "github checks scope notice skipped: the source cannot be read", "repo", p.Repo, "pull", pull.Number)
	case err != nil:
		return &opaque{what: "decide the scope notice", cause: err}
	}
	if j.scoped == nil {
		j.scoped = map[checkPull]unchangedAt{}
	}
	j.scoped[key] = at
	return nil
}

// decideScope reads the head tree, then the base tree only when the head has uncovered paths, and writes the notice.
func (j *checksJob) decideScope(ctx context.Context, p owner.ProjectPolicy, pull githubsync.PullState) error {
	project := owner.ProjectID(p.RepositoryID)
	read, err := j.reader.Tree(ctx, p.Repo, pull.HeadSHA)
	if err != nil {
		return githubRead(err)
	}
	head := &githubTree{tree: read, files: map[string][]byte{}}
	found, err := discoverHead(ctx, j.suites, j.contents, project, head)
	if err != nil || len(found.Paths) == 0 {
		return err
	}
	added, err := j.absentFromBase(ctx, p.Repo, pull.BaseSHA, found.Paths)
	if err != nil || len(added) == 0 {
		return err
	}
	_, declaration, err := found.declaration(added)
	if err != nil {
		return err
	}
	if len(declaration) > maxScopeDeclaration {
		j.logger.InfoContext(ctx, "github checks scope notice skipped: the declaration is over the limit", "repo", p.Repo, "pull", pull.Number)
		return nil
	}
	target := commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull.Number}
	notice, err := newScopeNoticeMessage(target, pull.HeadSHA, pull.HeadRef, added, declaration, found.Mixed)
	if err != nil {
		j.logger.WarnContext(ctx, "github checks scope notice skipped: it cannot be rendered", "repo", p.Repo, "pull", pull.Number)
		return nil
	}
	_, err = j.outboxOnce(ctx, project, notice)
	return err
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

// outboxOnce writes message unless its key exists, and reports whether it wrote it.
func (j *checksJob) outboxOnce(ctx context.Context, project contract.ProjectID, message governance.OutboxMessage) (bool, error) {
	err := j.suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error { return tx.Outbox(ctx, message) })
	if errors.Is(err, governance.ErrOperationConflict) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("write the comment %s: %w", message.Key, err)
	}
	return true, nil
}
