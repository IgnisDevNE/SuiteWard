package app

import (
	"context"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The key of a notice or a runner comment digests what the comment shows (the declaration with the new patterns, or the patterns alone), so a
// canonical that moves the shown text is a new comment and one that leaves it as it is writes nothing. The Suite's version is no part of the key:
// a promotion would otherwise post a new comment on every open pull request. A world whose canonical declares another text stands for a new canonical.

const (
	declaredInBlockStyle = "version: 1\ninclude:\n  - /tests/**\n" // the same scope as cjDecl, shown otherwise
)

var (
	canonicalPolicy = owner.ProjectPolicy{RepositoryID: bootRepoID, Repo: bootRepo}
	canonicalPull   = githubsync.PullState{Number: 5, HeadSHA: cjHead2, HeadRef: "feature", BaseRef: "main", BaseSHA: cjBase}
)

// noticeKey is the key of the notice of paths whose comment shows the canonical's declaration, cjDecl, with these patterns added.
func noticeKey(t *testing.T, include, runner []string, paths ...string) string {
	t.Helper()
	return "scope-notice:1001:5:" + contentDigest([]string{scopeDigest(paths...), addPatterns(t, cjDecl, include, runner), "0"})
}

// noticeKeyUnder is the key of the scope notice for a head that adds one test path, under a canonical that declares declaration.
func noticeKeyUnder(t *testing.T, declaration string) string {
	t.Helper()
	files := withDeclaration(bootFiles("v1"), declaration)
	w := newCJWorldOf(t, files)
	w.scopePull(withFiles(files, cjUtilTst), bootFiles("v1"))
	keys := w.scopeKeys("scope-notice:")
	if len(keys) != 1 {
		t.Fatalf("scope notices %v; want one\nlogs: %s", keys, w.logs)
	}
	return keys[0]
}

// runnerKeyUnder is the key of the runner-uncovered comment for one runner file and a head file that is the same under every canonical.
func runnerKeyUnder(t *testing.T, declaration string) string {
	t.Helper()
	w := newCJWorldOf(t, withDeclaration(bootFiles("v1"), declaration))
	set := &runnerSet{paths: []string{cjJest}, declared: []byte(cjDecl)}
	if err := w.check.explainRunners(t.Context(), canonicalPolicy, canonicalPull, set); err != nil {
		t.Fatal(err)
	}
	keys := w.scopeKeys("runner-uncovered:")
	if len(keys) != 1 {
		t.Fatalf("runner-uncovered comments %v; want one\nlogs: %s", keys, w.logs)
	}
	return keys[0]
}

func TestAScopeNoticeIsNewOnlyWhenTheCanonicalChangesTheShownDeclaration(t *testing.T) {
	if a, b := noticeKeyUnder(t, cjDecl), noticeKeyUnder(t, declaredInBlockStyle); a == b {
		t.Fatalf("a canonical that shows another declaration has the notice key %s again", a)
	}
	if a, b := noticeKeyUnder(t, cjDecl), noticeKeyUnder(t, cjDecl); a != b {
		t.Fatalf("a canonical that shows the same declaration has the notice keys %s and %s", a, b)
	}
}

func TestARunnerCommentIsNewOnlyWhenTheCanonicalChangesTheShownDeclarations(t *testing.T) {
	if a, b := runnerKeyUnder(t, cjDecl), runnerKeyUnder(t, declaredInBlockStyle); a == b {
		t.Fatalf("a canonical that shows other declarations has the runner comment key %s again", a)
	}
	if a, b := runnerKeyUnder(t, cjDecl), runnerKeyUnder(t, cjDecl); a != b {
		t.Fatalf("a canonical that shows the same declarations has the runner comment keys %s and %s", a, b)
	}
}

// A new canonical may change the text, so the notice is decided again for it, from the base tree. What it writes is the key's matter.
func TestChecksDecideAScopeNoticeAgainForANewCanonical(t *testing.T) {
	w := newCJWorld(t, true)
	w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))

	if err := w.check.scopeNotice(t.Context(), canonicalPolicy, canonicalPull, "V2"); err != nil {
		t.Fatal(err)
	}

	if reads := w.reader.reads[cjBase]; reads != 2 {
		t.Fatalf("the base tree was read %d times, want 2: once for each canonical", reads)
	}
	if keys := w.scopeKeys("scope-notice:"); len(keys) != 1 {
		t.Fatalf("scope notices %v; a canonical that shows the same text writes no second one", keys)
	}
}

// A declaration that cannot be shown (a character that is not graphic) is left out, and the notice lists the patterns: they are what its key digests.
func TestAScopeNoticeThatListsThePatternsAloneIsKeyedByThem(t *testing.T) {
	key := func(declaration string, include ...string) string {
		t.Helper()
		message, err := newScopeNoticeMessage(cmtTarget(), cmtScopeHead, "feature", []string{"a_test.go"}, []byte(declaration), nil, nil, inventory.Suggestion{Include: include})
		if err != nil {
			t.Fatal(err)
		}
		return message.Key
	}
	if a, b := key("version: 1\x00\n", "*_test.go"), key("version: 2\x00\n", "*_test.go"); a != b {
		t.Fatalf("the notice keys %s and %s differ for the same patterns", a, b)
	}
	if a, b := key("version: 1\x00\n", "*_test.go"), key("version: 1\x00\n", "*_test.go", "*.spec.*"); a == b {
		t.Fatalf("the notice key %s is the same for other patterns", a)
	}
}

// seedOutbox writes a message as an earlier run, or the suggest command, would have.
func (w *cjJobWorld) seedOutbox(message governance.OutboxMessage) {
	w.t.Helper()
	err := w.suites.Do(w.t.Context(), owner.ProjectID(bootRepoID), defaultSuite, func(ctx context.Context, tx governance.Tx) error { return tx.Outbox(ctx, message) })
	if err != nil {
		w.t.Fatal(err)
	}
}

// A review whose key exists (the suggestion is the same on the same file) never keeps its comment from being written: the new canonical or the other
// producer changed the comment, not the review. The two are written apart, the comment first.
func TestChecksWriteTheScopeNoticeWhenItsReviewWasWrittenAlready(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}
	include := scopeSuggestion{List: "include", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"*_test.go"}, Quoting: commandtext.QuotingPlain}
	digest, err := suggestionsDigest([]byte(reviewDecl), []scopeSuggestion{include})
	if err != nil {
		t.Fatal(err)
	}
	review, err := newScopeReviewMessage(commentTarget{RepositoryID: bootRepoID, Repo: bootRepo, Pull: 5}, cjHead2, digest, []scopeSuggestion{include})
	if err != nil {
		t.Fatal(err)
	}
	w.seedOutbox(review)

	w.scopePull(reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))

	if keys := w.scopeKeys("scope-notice:"); len(keys) != 1 {
		t.Fatalf("scope notices %v; want the one of the added path, though its review exists\nlogs: %s", keys, w.logs)
	}
}

func TestChecksWriteTheRunnerCommentWhenItsReviewWasWrittenAlready(t *testing.T) {
	w := newCJWorld(t, true)
	choices := []scopeSuggestion{
		{List: "protect", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"jest.config.*"}, NewList: "runner"},
		{List: "ignore", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"/" + cjJest}, NewList: "exclude"},
	}
	digest, err := suggestionsDigest([]byte(reviewDecl), choices)
	if err != nil {
		t.Fatal(err)
	}
	review, err := newScopeReviewMessage(commentTarget{RepositoryID: bootRepoID, Repo: bootRepo, Pull: 5}, cjHead2, digest, choices)
	if err != nil {
		t.Fatal(err)
	}
	w.seedOutbox(review)

	w.approvedRunnerPull(reviewDecl, "@@ -1,3 +1,3 @@")

	if keys := w.scopeKeys("runner-uncovered:"); len(keys) != 1 {
		t.Fatalf("runner-uncovered comments %v; want one, though its review exists\nlogs: %s", keys, w.logs)
	}
}
