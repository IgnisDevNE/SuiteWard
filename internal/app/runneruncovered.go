package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// Runner files outside the scope (docs/contracts/runtime.md, "Check reason runner-uncovered"; D-SCOPE-DISCOVERY, "Uncovered runner files block"):
// after V1 a pull request that adds or changes a runner file the scope does not cover is never success until the owner protects or ignores it.

const (
	runnerUncoveredKey = "runner-uncovered" // runner-uncovered:<repository id>:<pull>:<review digest>

	// maxPullFiles is how many files of a pull request GitHub lists: a list that long may be cut, and then no file is known to be untouched.
	maxPullFiles = 3000
)

// runnerSet is what the checks job found for the head of a pull request under one canonical: the runner files that the pull request touches and
// the head's scope does not cover, sorted, and what the comment and the review of the two choices need. It is kept in memory, so a head is read once.
type runnerSet struct {
	at       unchangedAt
	paths    []string         // none: the head is not blocked
	declared []byte           // the head's .suiteward.yml, nil when it has none
	diff     *github.PullFile // the pull request's change of .suiteward.yml, nil when it does not change it
	written  bool             // the comment was decided
}

// runnerFiles returns the runner set of the head, deciding it once for each head and canonical. wait is true when GitHub could not be read now:
// a head whose runner files are unknown is never success, so the caller publishes assessment-pending.
func (j *checksJob) runnerFiles(ctx context.Context, p owner.ProjectPolicy, pull githubsync.PullState, canonical contract.SuiteVersionID) (set *runnerSet, wait bool, err error) {
	key, at := checkPull{p.RepositoryID, pull.Number}, unchangedAt{pull.HeadSHA, canonical}
	if known := j.runners[key]; known != nil && known.at == at {
		return known, false, nil
	}
	set, err = j.findRunners(ctx, p, pull)
	switch {
	case ctx.Err() != nil:
		return nil, false, ctx.Err()
	case errors.Is(err, errGitHubRead):
		logReadFailure(ctx, j.logger, "github checks runner files read failed", p.Repo, err)
		return nil, true, nil
	case invalidSource(err): // the head was assessed, so this is a read that went wrong; the next run tries again
		j.logger.WarnContext(ctx, "github checks runner files skipped: the source cannot be read", "repo", p.Repo, "pull", pull.Number)
		return nil, true, nil
	case err != nil:
		return nil, false, &opaque{what: "find the runner files", cause: err}
	}
	set.at = at
	if j.runners == nil {
		j.runners = map[checkPull]*runnerSet{}
	}
	j.runners[key] = set
	return set, false, nil
}

// findRunners reads the head's tree and, only when it holds runner files that its own declaration does not cover, the files of the pull request.
// The head's declaration is the scope here, which is sound because only a head with nothing to approve or with an approved declaration is success.
func (j *checksJob) findRunners(ctx context.Context, p owner.ProjectPolicy, pull githubsync.PullState) (*runnerSet, error) {
	read, err := j.reader.Tree(ctx, p.Repo, pull.HeadSHA)
	if err != nil {
		return nil, githubRead(err)
	}
	head := &githubTree{tree: read, files: map[string][]byte{}}
	entries, err := head.Entries(ctx)
	if err != nil {
		return nil, err
	}
	set := &runnerSet{}
	governing := scope.NewScope(scope.NoDeclaration())
	if hasDeclaration(entries) {
		if set.declared, err = head.Read(ctx, scope.DeclarationPath); err != nil {
			return nil, err
		}
		declaration, err := scope.ParseDeclaration(set.declared)
		if err != nil {
			return nil, err
		}
		governing = scope.NewScope(declaration)
	}
	candidates, err := inventory.UncoveredRunners(entries, governing)
	if err != nil || len(candidates) == 0 {
		return set, err
	}
	changed, err := j.reader.PullFiles(ctx, p.Repo, pull.Number)
	if err != nil {
		return nil, githubRead(err)
	}
	touched := make(map[string]bool, len(changed))
	for _, f := range changed {
		touched[f.Path] = f.Status != "removed" && f.Status != "unchanged"
		if f.Path == scope.DeclarationPath {
			set.diff = &f
		}
	}
	set.paths = slices.DeleteFunc(candidates, func(path string) bool { return len(changed) < maxPullFiles && !touched[path] })
	return set, nil
}

// explainRunners writes the comment of a blocked head, with the review of the two choices when the pull request's diff gives lines for them, once
// for each set of paths and head declaration: the outbox key is the idempotency key. A comment that cannot be written is logged and skipped:
// the check says the same without it.
func (j *checksJob) explainRunners(ctx context.Context, p owner.ProjectPolicy, pull githubsync.PullState, set *runnerSet) error {
	base, _, err := canonicalDeclaration(ctx, j.suites, j.contents, owner.ProjectID(p.RepositoryID))
	switch {
	case errors.Is(err, errNoCanonicalDeclaration):
		j.logger.InfoContext(ctx, "github checks runner-uncovered comment skipped: the canonical has no declaration to start from", "repo", p.Repo, "pull", pull.Number)
		return nil
	case err != nil:
		return &opaque{what: "read the canonical declaration", cause: err}
	}
	protect, err := scope.AddPatterns(base, nil, inventory.RunnerPatterns(set.paths))
	if err != nil {
		return &opaque{what: "add the runner patterns", cause: err}
	}
	var ignore []byte
	if excludable(set.paths) {
		if ignore, err = scope.AddExcludes(base, excludePatterns(set.paths)); err != nil {
			return &opaque{what: "add the excludes", cause: err}
		}
	}
	if len(ignore) > commandtext.MaxDeclaration { // a choice that cannot be shown is left out; protecting stays
		ignore = nil
	}
	target := commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull.Number}
	comment, err := newRunnerUncoveredMessage(target, pull.HeadSHA, pull.HeadRef, set.paths, protect, ignore, reviewDigest(setDigest(set.paths), set.declared))
	if err != nil {
		j.logger.WarnContext(ctx, "github checks runner-uncovered comment skipped: it cannot be rendered", "repo", p.Repo, "pull", pull.Number, "reason", err.Error())
		return nil
	}
	messages := []governance.OutboxMessage{comment}
	messages = append(messages, j.runnerReview(ctx, target, pull.HeadSHA, set, ignore != nil)...)
	return outboxOnce(ctx, j.suites, owner.ProjectID(p.RepositoryID), messages)
}

// excludePatterns returns the exclude patterns that name exactly the files at paths: a name without a slash would match at every depth, so it gets a
// leading slash; a path with a slash is anchored already.
func excludePatterns(paths []string) []string {
	patterns := make([]string, len(paths))
	for i, path := range paths {
		patterns[i] = path
		if !strings.Contains(path, "/") {
			patterns[i] = "/" + path
		}
	}
	return patterns
}

// excludable reports whether every path can be excluded by its own pattern: a path that contains a pattern character does not match
// the pattern it spells, so excluding it would never resolve the block.
func excludable(paths []string) bool {
	return !slices.ContainsFunc(paths, func(path string) bool {
		pattern, err := scope.ParsePattern(excludePatterns([]string{path})[0])
		if err != nil {
			return true
		}
		matched, err := pattern.Match(path)
		return err != nil || !matched
	})
}

// runnerReview builds the review of the choices: one message, or none (an empty list). Its key digests a salt with the set and the head's file, so it never
// collides with the review of the scope notice for the same set, whose comment would be lost with it.
func (j *checksJob) runnerReview(ctx context.Context, target commentTarget, head string, set *runnerSet, ignorable bool) []governance.OutboxMessage {
	var ignore []string
	if ignorable {
		ignore = excludePatterns(set.paths)
	}
	suggestions, err := j.runnerSuggestions(ctx, target, set, inventory.RunnerPatterns(set.paths), ignore)
	if err != nil {
		j.logger.WarnContext(ctx, "github checks runner-uncovered review skipped: the diff of the declaration cannot be read", "repo", target.Repo, "pull", target.Pull)
		return []governance.OutboxMessage{}
	}
	if len(suggestions) == 0 {
		return []governance.OutboxMessage{}
	}
	message, err := newScopeReviewMessage(target, head, reviewDigest("runner-uncovered:"+setDigest(set.paths), set.declared), suggestions)
	if err != nil {
		j.logger.WarnContext(ctx, "github checks runner-uncovered review skipped: it cannot be rendered", "repo", target.Repo, "pull", target.Pull, "reason", err.Error())
		return []governance.OutboxMessage{}
	}
	return []governance.OutboxMessage{message}
}

// runnerSuggestions builds the suggestion of each choice that has a place in the head's file: the patterns go after the last item of the
// choice's list when that item is one line inside a hunk of the pull request's diff, which is the only place that starts from the head's file.
// A choice whose list is missing creates it after the last item of a block-style list whose last item is such a line (a suggestion replaces lines
// of the diff only, and a new key after a list's last item is valid YAML). A choice with no such line, or one that does not render, has none.
func (j *checksJob) runnerSuggestions(ctx context.Context, target commentTarget, set *runnerSet, protect, ignore []string) ([]scopeSuggestion, error) {
	suggestions := []scopeSuggestion{}
	if set.diff == nil || len(set.declared) == 0 {
		return suggestions, nil
	}
	hunks, err := set.diff.NewLineRanges()
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(set.declared, &root); err != nil {
		return nil, fmt.Errorf("read the head's %s as YAML: %w", scope.DeclarationPath, err)
	}
	top := root.Content[0] // the declaration parsed: one document holding a mapping
	lines := strings.Split(string(set.declared), "\n")
	for _, choice := range []struct {
		name, key string
		patterns  []string
	}{{"protect", "runner", protect}, {"ignore", "exclude", ignore}} {
		if len(choice.patterns) == 0 {
			continue
		}
		s, ok := listSuggestion(top, lines, choice.key, choice.patterns, hunks)
		if !ok && !hasKey(top, choice.key) {
			s, ok = newListSuggestion(top, lines, choice.key, choice.patterns, hunks)
		}
		s.List = choice.name
		if !ok {
			continue
		}
		if err := s.validate(); err != nil { // its text is fixed: it quotes neither the anchor line nor a path
			j.logger.WarnContext(ctx, "github checks runner-uncovered choice skipped: it does not fit a suggestion", "repo", target.Repo, "pull", target.Pull, "choice", choice.name, "reason", err.Error())
			continue
		}
		suggestions = append(suggestions, s)
	}
	return suggestions, nil
}

func hasKey(top *yaml.Node, key string) bool {
	for i := 0; i < len(top.Content); i += 2 {
		if top.Content[i].Value == key {
			return true
		}
	}
	return false
}

// newListSuggestion is the suggestion that creates the list key with patterns after the last item of the last block-style list of the declaration
// top whose last item is a single line inside a hunk. The key goes where the list's own key is, so the file stays a mapping of lists.
func newListSuggestion(top *yaml.Node, lines []string, key string, patterns []string, hunks []github.LineRange) (scopeSuggestion, bool) {
	for i := len(top.Content) - 2; i >= 0; i -= 2 {
		name, list := top.Content[i], top.Content[i+1]
		if list.Kind != yaml.SequenceNode || list.Style&yaml.FlowStyle != 0 || len(list.Content) == 0 {
			continue
		}
		item := list.Content[len(list.Content)-1]
		if !slices.ContainsFunc(hunks, func(h github.LineRange) bool { return item.Line >= h.Start && item.Line <= h.End }) {
			continue
		}
		anchor := strings.TrimSuffix(lines[item.Line-1], "\r")
		if singleLineItem(anchor, item) {
			return scopeSuggestion{Line: item.Line, Anchor: anchor, Patterns: patterns, NewList: key, KeyIndent: strings.Repeat(" ", name.Column-1)}, true
		}
	}
	return scopeSuggestion{}, false
}

// runnerUncoveredPayload is the payload of runner-uncovered:<repository id>:<pull>:<review digest>. The key's digest is that of the whole
// set of paths and the head's file, which the bounded list below cannot reproduce, so the publisher checks only its shape.
type runnerUncoveredPayload struct {
	commentTarget
	Head       string   `json:"head"`
	Branch     string   `json:"branch"`
	Paths      []string `json:"paths"` // at most 20, each cut to 150 bytes
	PathsTotal int      `json:"pathsTotal"`
	Protect    string   `json:"protect"`          // the canonical declaration with the runner patterns added, at most 8192 bytes
	Ignore     string   `json:"ignore,omitempty"` // the canonical declaration with the paths excluded, at most 8192 bytes; none when they cannot be named there
}

func (p runnerUncoveredPayload) notice() commandtext.RunnerUncovered {
	return commandtext.RunnerUncovered{Repo: p.Repo, Branch: p.Branch, Paths: p.Paths, PathsTotal: p.PathsTotal, Protect: []byte(p.Protect), Ignore: []byte(p.Ignore)}
}

// validate reports why the payload cannot be rendered, without quoting it.
func (p runnerUncoveredPayload) validate() error {
	if err := p.commentTarget.validate(); err != nil {
		return err
	}
	if !headPattern.MatchString(p.Head) {
		return errors.New("the head is not a commit SHA")
	}
	for _, path := range p.Paths {
		if len(path) > maxSummaryText {
			return errors.New("a path is too long")
		}
	}
	if commandtext.ValidateRunnerUncovered(p.notice()) != nil { // its text quotes the repository and the branch, so only a fixed reason is kept
		return errors.New("the repository, the branch, the paths or the declarations do not fit the comment")
	}
	return nil
}

// newRunnerUncoveredMessage builds the comment of a blocked head, bounding the repository text it carries. A payload the publisher could not
// render is an error, so nobody writes a message that nobody can post.
func newRunnerUncoveredMessage(target commentTarget, head, branch string, paths []string, protect, ignore []byte, digest string) (governance.OutboxMessage, error) {
	if !digestPattern.MatchString(digest) {
		return governance.OutboxMessage{}, errors.New("runner-uncovered comment: the digest is not SHA-256 hex")
	}
	p := runnerUncoveredPayload{commentTarget: target, Head: head, Branch: branch, Paths: boundedText(paths), PathsTotal: len(paths), Protect: string(protect), Ignore: string(ignore)}
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("runner-uncovered comment: %w", err)
	}
	payload, err := marshalPayload(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the runner-uncovered comment: %w", err)
	}
	key := fmt.Sprintf("%s:%d:%d:%s", runnerUncoveredKey, target.RepositoryID, target.Pull, digest)
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: payload}, nil
}

func (p *commentPublisher) runnerUncovered(key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	n, err := decodePayload[runnerUncoveredPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if n.validate() != nil || !scopeTail(tail, n.Pull) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := render(githubCommentKind, key, func() string { return commandtext.RunnerUncoveredComment(n.notice()) })
	return n.commentTarget, text, err
}
