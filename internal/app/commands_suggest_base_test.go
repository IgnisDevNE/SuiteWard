package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// /suiteward suggest compares the head with a base declaration (docs/contracts/runtime.md, "Scope discovery (M1.9)", Command
// /suiteward suggest): before the first contract the default branch's .suiteward.yml, else the head's own, as a propose picks its
// source, and strictly the canonical after it. The reply shows the difference, and a scope-review offers it as a Commit suggestion when the pull request already
// edits the file.

// cjFiles is the reader of a commands world with the pull request's changed files under test control.
type cjFiles struct {
	*fakeBootReader
	files []github.PullFile
	err   error
	calls int
}

func (r *cjFiles) PullFiles(context.Context, string, int) ([]github.PullFile, error) {
	r.calls++
	return r.files, r.err
}

func (w *cjWorld) pullFiles(files []github.PullFile) *cjFiles {
	r := &cjFiles{fakeBootReader: w.reader, files: files}
	w.job.reader = r
	return r
}

// scopeMessages are the scope-notice and scope-review messages a unit of work wrote with the prefix.
func (w *cjWorld) scopeMessages(prefix string) []governance.OutboxMessage {
	var found []governance.OutboxMessage
	for _, m := range w.suites.messages {
		if strings.HasPrefix(m.Key, prefix) {
			found = append(found, m)
		}
	}
	return found
}

func (w *cjWorld) requireReview(repositoryID int64, head string, paths []string, suggestions ...map[string]any) {
	w.t.Helper()
	messages := w.scopeMessages("scope-review:")
	want := fmt.Sprintf("scope-review:%d:%d:%s", repositoryID, cjPull, scopeDigest(paths...))
	if len(messages) != 1 || messages[0].Key != want || messages[0].Kind != githubCommentKind {
		w.t.Fatalf("review messages %v; want exactly %s\nlogs: %s", w.scopeMessagesKeys("scope-review:"), want, w.logs)
	}
	var lists []any
	for _, s := range suggestions {
		lists = append(lists, s)
	}
	wantPayload := roundTrip(map[string]any{"repositoryId": repositoryID, "repo": cjRepoName[repositoryID], "pull": cjPull, "head": head, "suggestions": lists})
	var got map[string]any
	if err := json.Unmarshal(messages[0].Payload, &got); err != nil {
		w.t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(wantPayload) {
		w.t.Fatalf("review payload %s; want %v", messages[0].Payload, wantPayload)
	}
}

func (w *cjWorld) scopeMessagesKeys(prefix string) []string {
	var keys []string
	for _, m := range w.scopeMessages(prefix) {
		keys = append(keys, m.Key)
	}
	return keys
}

func (w *cjWorld) requireNoNoticeKey() {
	w.t.Helper()
	if keys := w.scopeMessagesKeys("scope-notice:"); len(keys) != 0 {
		w.t.Fatalf("scope notices %v; a command answers in its acknowledgment and writes no notice", keys)
	}
}

func addPatterns(t *testing.T, raw string, include, runner []string) string {
	t.Helper()
	declaration, err := scope.AddPatterns([]byte(raw), include, runner)
	if err != nil {
		t.Fatal(err)
	}
	return string(declaration)
}

var suggestInclude = suggestion("include", 3, "  - /tests/**", "*_test.go")

func TestSuggestBeforeTheFirstContractComparesTheHeadWithItsOwnDeclarationWhenTheDefaultBranchHasNone(t *testing.T) {
	w := newCjWorld(t)
	w.trees(map[string]string{"README.md": "x"}, reviewFiles(reviewDecl, cjUtilTst))
	reader := w.pullFiles(changed("@@ -1,3 +1,3 @@"))
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "pull", "paths": []string{cjUtilTst}, "pathsTotal": 1,
		"declaration": addPatterns(t, reviewDecl, []string{"*_test.go"}, nil), "addInclude": []string{"*_test.go"}})
	w.requireReview(cjID1, cjHead, []string{cjUtilTst}, suggestInclude)
	w.requireNoNoticeKey()
	if reader.calls != 1 || w.reader.calls["head"] != 1 {
		t.Fatalf("%d reads of the changed files and %d of the default branch; want 1 and 1: the default branch is read first, and has no file", reader.calls, w.reader.calls["head"])
	}
	if len(w.owners.consumed) != 0 {
		t.Fatalf("consumed %v; a suggestion uses no code", w.owners.consumed)
	}
	w.requireNothingLeaked()
}

func TestSuggestBeforeTheFirstContractListsTheMissingRunnerPatternsToo(t *testing.T) {
	w := newCjWorld(t)
	w.trees(map[string]string{"README.md": "x"}, reviewFiles(reviewBoth, cjUtilTst, "conftest.py"))
	w.pullFiles(changed("@@ -1,5 +1,5 @@"))
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
	w.mustRun()

	runner := []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}
	w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "pull", "paths": []string{"conftest.py", cjUtilTst}, "pathsTotal": 2,
		"declaration": addPatterns(t, reviewBoth, []string{"*_test.go"}, runner), "addInclude": []string{"*_test.go"}, "addRunner": runner})
	// The review leaves out pytest.toml, which the head's list holds.
	w.requireReview(cjID1, cjHead, []string{"conftest.py", cjUtilTst}, suggestInclude, suggestion("runner", 5, "  - pytest.toml", ".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini"))
}

func TestSuggestWritesNoReviewWhenThePullRequestGivesNoLineToSuggestOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		decl  string
		files []github.PullFile
	}{
		{"the file is not in the diff", reviewDecl, []github.PullFile{{Path: "README.md", Status: "modified", Patch: "@@ -1,3 +1,3 @@\n a"}}},
		{"no list line is inside a hunk", reviewDecl, changed("@@ -1,2 +1,2 @@")},
		{"the list is in flow style", "version: 1\ninclude: [/tests/**]\n", changed("@@ -1,2 +1,2 @@")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(map[string]string{"README.md": "x"}, reviewFiles(tc.decl, cjUtilTst))
			w.pullFiles(tc.files)
			w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			w.mustRun()

			w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "pull", "paths": []string{cjUtilTst}, "pathsTotal": 1,
				"declaration": addPatterns(t, tc.decl, []string{"*_test.go"}, nil), "addInclude": []string{"*_test.go"}})
			if keys := w.scopeMessagesKeys("scope-"); len(keys) != 0 {
				t.Fatalf("scope messages %v; want none", keys)
			}
		})
	}
}

func TestSuggestBeforeTheFirstContractComparesWithTheDefaultBranchWhenTheHeadHasNoDeclaration(t *testing.T) {
	w := newCjWorld(t)
	w.trees(bootFiles("v1"), map[string]string{cjUtilTst: "x", "src/main.go": "y"})
	reader := w.pullFiles(changed("@@ -1,3 +1,3 @@"))
	w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
	w.mustRun()

	w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "default-branch", "paths": []string{cjUtilTst}, "pathsTotal": 1,
		"declaration": addPatterns(t, cjDecl, []string{"*_test.go"}, nil), "addInclude": []string{"*_test.go"}})
	if keys := w.scopeMessagesKeys("scope-"); len(keys) != 0 || reader.calls != 0 {
		t.Fatalf("scope messages %v and %d reads of the changed files; want none: the head has no file for a review to edit", keys, reader.calls)
	}
}

func TestSuggestBeforeTheFirstContractSaysNothingIsUncoveredWhenTheBaseCoversTheHead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		main, head map[string]string
		base       string
	}{
		{"the head's own declaration", map[string]string{"README.md": "x"}, bootFiles("v2"), "pull"},
		{"the default branch's declaration", bootFiles("v1"), map[string]string{"tests/a_test.go": "x", "src/main.go": "y"}, "default-branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(tc.main, tc.head)
			reader := w.pullFiles(nil)
			w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			w.mustRun()

			w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "nothing-uncovered", "base": tc.base})
			if keys := w.scopeMessagesKeys("scope-"); len(keys) != 0 || reader.calls != 0 {
				t.Fatalf("scope messages %v and %d reads of the changed files; want none", keys, reader.calls)
			}
		})
	}
}

// A propose proposes the default branch's file when it has one, and so a suggest compares with it first: the head's own file, valid
// or not, is not the base then.
func TestSuggestBeforeTheFirstContractPrefersTheDefaultBranchsDeclarationToTheHeads(t *testing.T) {
	t.Run("a head file that covers more", func(t *testing.T) {
		w := newCjWorld(t)
		w.trees(bootFiles("v1"), reviewFiles("version: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\n", cjUtilTst)) // the head's file covers the new test, the default branch's does not
		reader := w.pullFiles(changed("@@ -1,3 +1,3 @@"))
		w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
		w.mustRun()

		w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "default-branch", "paths": []string{cjUtilTst}, "pathsTotal": 1,
			"declaration": addPatterns(t, cjDecl, []string{"*_test.go"}, nil), "addInclude": []string{"*_test.go"}})
		// The review edits the head's file, which is not the base here: none is offered.
		if keys := w.scopeMessagesKeys("scope-"); len(keys) != 0 || reader.calls != 0 {
			t.Fatalf("scope messages %v and %d reads of the changed files; want none", keys, reader.calls)
		}
	})
	t.Run("a head file that is invalid", func(t *testing.T) {
		w := newCjWorld(t)
		w.trees(bootFiles("v1"), reviewFiles("version: 2\n"))
		w.pullFiles(nil)
		w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
		w.mustRun()

		w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "nothing-uncovered", "base": "default-branch"})
	})
}

func TestSuggestBeforeTheFirstContractRefusesAnInvalidDeclarationAsInvalidSource(t *testing.T) {
	invalid := "version: 2\ninclude:\n  - /tests/**\n"
	for _, tc := range []struct {
		name       string
		main, head map[string]string
	}{
		{"the head's declaration is invalid and the default branch has none", map[string]string{"README.md": "x"}, reviewFiles(invalid, cjUtilTst)},
		{"the default branch's declaration is invalid and the head has none", withDeclaration(bootFiles("v1"), invalid), map[string]string{cjUtilTst: "x"}},
		{"the default branch's declaration is invalid although the head's is valid", withDeclaration(bootFiles("v1"), invalid), reviewFiles(reviewDecl, cjUtilTst)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(tc.main, tc.head)
			w.pullFiles(nil)
			w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			w.mustRun()

			w.requireAckPayload(cjID1, 500, commands.SuggestRefused, map[string]any{"reason": "invalid-source"})
			if !strings.Contains(w.logs.String(), "github suggest refused") {
				t.Fatalf("no log line says the suggestion was refused\nlogs: %s", w.logs)
			}
			w.requireNothingLeaked()
		})
	}
}

func TestSuggestAfterTheFirstContractNeverStartsTheDeclarationFromTheHeadsFile(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(cjID2, cjDecl, reviewFiles(reviewDecl, cjUtilTst)) // the canonical's declaration is in flow style, the head's in block style
	w.pullFiles(changed("@@ -1,3 +1,3 @@"))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	declaration := addPatterns(t, cjDecl, []string{"*_test.go"}, nil)
	if declaration == addPatterns(t, reviewDecl, []string{"*_test.go"}, nil) {
		t.Fatal("the test does not tell the canonical's file from the head's")
	}
	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "contract", "paths": []string{cjUtilTst}, "pathsTotal": 1,
		"declaration": declaration, "addInclude": []string{"*_test.go"}})
	// The review edits the head's file, the only place a line of it can be: the lines come from the head, the declaration does not.
	w.requireReview(cjID2, cjHead, []string{cjUtilTst}, suggestInclude)
	w.requireNoNoticeKey()
	if len(w.owners.consumed) != 0 {
		t.Fatalf("consumed %v; a suggestion uses no code", w.owners.consumed)
	}
}

// After the first contract the base is the canonical and nothing else: a path that only the head's own file covers is still reported,
// where the checks job's notice leaves it out because the owner's approval of that very file is what is pending.
func TestSuggestAfterTheFirstContractReportsAPathOnlyTheHeadsFileCovers(t *testing.T) {
	headDecl := "version: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\n"
	w := newCjWorld(t)
	w.seedDeclared(cjID2, cjDecl, reviewFiles(headDecl, cjUtilTst))
	w.pullFiles(changed("@@ -1,4 +1,4 @@"))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "contract", "paths": []string{cjUtilTst}, "pathsTotal": 1,
		"declaration": addPatterns(t, cjDecl, []string{"*_test.go"}, nil), "addInclude": []string{"*_test.go"}})
	// The head's list already holds the pattern, so there is nothing to suggest on its line.
	if keys := w.scopeMessagesKeys("scope-"); len(keys) != 0 {
		t.Fatalf("scope messages %v; want none", keys)
	}
}

// The checks job may have written the review of the same set already: the key dedupes, so the command's answer is recorded and the
// review stays one.
func TestSuggestAfterTheFirstContractAddsNoSecondReviewForASetTheChecksJobReviewed(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(cjID2, cjDecl, reviewFiles(reviewDecl, cjUtilTst))
	w.pullFiles(changed("@@ -1,3 +1,3 @@"))
	existing, err := newScopeReviewMessage(cjTarget(cjID2), cjHead, scopeDigest(cjUtilTst), []scopeSuggestion{{List: "include", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"*_test.go"}}})
	if err != nil {
		t.Fatal(err)
	}
	err = w.mem.Do(t.Context(), "github-repo:1002", "default", func(ctx context.Context, tx governance.Tx) error { return tx.Outbox(ctx, existing) })
	if err != nil {
		t.Fatal(err)
	}
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	w.requireDisposition(cjID2, 500, commands.Suggested)
	keys, err := w.mem.OutboxKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var reviews []string
	for _, key := range keys {
		if strings.HasPrefix(key, "scope-review:") {
			reviews = append(reviews, key)
		}
	}
	if !slices.Equal(reviews, []string{existing.Key}) {
		t.Fatalf("reviews %v; want only the one the checks job wrote", reviews)
	}
}

func TestSuggestKeepsTheReplyWhenTheChangedFilesCannotBeRead(t *testing.T) {
	t.Run("a failure that will repeat drops only the review", func(t *testing.T) {
		w := newCjWorld(t)
		w.trees(bootFiles("v1"), reviewFiles(reviewDecl, cjUtilTst))
		reader := w.pullFiles(changed("@@ -1,3 +1,3 @@"))
		reader.err = &rest.StatusError{Status: 500, Body: bootSecret}
		w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
		w.mustRun()

		w.requireAckPayload(cjID1, 500, commands.Suggested, map[string]any{"reason": "scope-notice", "branch": cjBranch, "base": "pull", "paths": []string{cjUtilTst}, "pathsTotal": 1,
			"declaration": addPatterns(t, reviewDecl, []string{"*_test.go"}, nil), "addInclude": []string{"*_test.go"}})
		if keys := w.scopeMessagesKeys("scope-"); len(keys) != 0 {
			t.Fatalf("scope messages %v; want none", keys)
		}
		if !strings.Contains(w.logs.String(), `"kind"`) || !strings.Contains(w.logs.String(), `"status":500`) {
			t.Fatalf("the log lacks the kind and status of the failure\nlogs: %s", w.logs)
		}
		w.requireNothingLeaked(bootSecret)
	})
	t.Run("a rate limit waits for the next run", func(t *testing.T) {
		w := newCjWorld(t)
		w.trees(bootFiles("v1"), reviewFiles(reviewDecl, cjUtilTst))
		reader := w.pullFiles(changed("@@ -1,3 +1,3 @@"))
		reader.err = &rest.RateLimitedError{Status: 403}
		w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
		w.mustRun()

		w.requireNoEntry(cjID1, 500)
		reader.err = nil // the limit ended
		w.mustRun()
		w.requireDisposition(cjID1, 500, commands.Suggested)
		w.requireReview(cjID1, cjHead, []string{cjUtilTst}, suggestInclude)
	})
}

// The acknowledgments the publisher renders for the reply of a suggest with a base.
func TestCommentPublisherRendersTheSuggestReplyWithABase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		base  string
		want  []string
		other []string
	}{
		{"this pull request's file", "pull", []string{"this pull request's `.suiteward.yml` does not cover", "```\n/suiteward propose\n```", "Commit suggestion", "/edit/feature/x/.suiteward.yml"}, []string{"the default branch's", "the current contract"}},
		{"the default branch's file", "default-branch", []string{"the default branch's `.suiteward.yml` does not cover", "default branch first", "```\n/suiteward propose\n```"}, []string{"this pull request's `.suiteward.yml`", "the current contract", "Commit suggestion", "/new/", "/edit/"}},
		{"the canonical", "contract", []string{"the current contract does not cover", "approve the new revision SuiteWard posts", "Commit suggestion"}, []string{"/suiteward propose", "the default branch's"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			payload := map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature/x", "base": tc.base, "paths": []string{"pkg/a_test.go", "conftest.py"}, "pathsTotal": 2,
				"declaration": suggestDeclaration, "addInclude": []string{"*_test.go"}, "addRunner": []string{"conftest.py"}}
			key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)
			if err := w.pub.publish(t.Context(), key, cmtAckPayload(payload)); err != nil {
				t.Fatalf("publish: %v", err)
			}
			posts := w.writer.posts()
			if len(posts) != 1 {
				t.Fatalf("posted %d comments, want 1", len(posts))
			}
			for _, text := range tc.want {
				if !strings.Contains(posts[0].body, text) {
					t.Errorf("the comment lacks %q:\n%s", text, posts[0].body)
				}
			}
			for _, text := range tc.other {
				if strings.Contains(posts[0].body, text) {
					t.Errorf("the comment says %q:\n%s", text, posts[0].body)
				}
			}
		})
	}
}

func TestCommentPublisherRefusesASuggestReplyWithABaseThatDoesNotFit(t *testing.T) {
	notice := func(key string, value any) map[string]any {
		payload := map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature", "base": "pull", "paths": []string{"a_test.go"}, "pathsTotal": 1,
			"declaration": suggestDeclaration, "addInclude": []string{"*_test.go"}}
		if value == nil {
			delete(payload, key)
		} else {
			payload[key] = value
		}
		return payload
	}
	for name, extra := range map[string]map[string]any{
		"an unknown base":                   notice("base", "canonical"),
		"new patterns without a base":       notice("base", nil),
		"an unknown base on nothing uncovered": {"disposition": "suggested", "reason": "nothing-uncovered", "base": "canonical"},
		"a base on a missing declaration":   {"disposition": "suggested", "reason": "declaration-missing", "branch": "feature", "base": "pull"},
		"new patterns on nothing uncovered": {"disposition": "suggested", "reason": "nothing-uncovered", "addInclude": []string{"*_test.go"}},
		"21 new patterns":                   notice("addInclude", strings.Split(strings.Repeat("x,", 21), ",")[:21]),
		"a base on a refusal":               {"disposition": "suggest-refused", "reason": "invalid-source", "base": "pull"},
		"a declared flag with a base":       notice("declared", true),
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment), cmtAckPayload(extra))
			if err == nil || !strings.Contains(err.Error(), "does not match its key") {
				t.Fatalf("publish = %v; want a validation error, not a render failure", err)
			}
			if len(w.writer.posts()) != 0 {
				t.Fatal("a comment was posted")
			}
		})
	}
}

// The outbox takes at most 64 KiB of payload, and JSON writes <, > and & as six bytes: a payload that would not fit is not renderable. A
// suggestion that cannot be rendered is refused, and the checks job skips its notice, instead of failing the same write on every run.
func escapedWorstCase(unit string) (paths, mixed, submodules []string, declaration []byte) {
	long := make([]string, 20)
	for i := range long {
		long[i] = strings.Repeat(unit, 150/len(unit))
	}
	return long, long, long, []byte("version: 1\n# " + strings.Repeat(unit, 8000/len(unit)) + "\n")
}

func TestPayloadsOverTheOutboxLimitAreNotRenderable(t *testing.T) {
	for _, unit := range []string{"<", "\x01"} {
		paths, mixed, submodules, declaration := escapedWorstCase(unit)
		if _, err := newScopeNoticeAck(cmtTarget(), cmtComment, cmtOwner, "feature", "contract", paths, declaration, inventory.Suggestion{}, mixed, submodules); err == nil {
			t.Errorf("unit %q: the acknowledgment is accepted", unit)
		}
		if _, err := newScopeNoticeMessage(cmtTarget(), cmtScopeHead, "feature", paths, declaration, mixed, submodules, false); err == nil {
			t.Errorf("unit %q: the scope notice is accepted", unit)
		}
	}
	// What fits is still written: plain text of the same sizes, and the largest review.
	paths, mixed, submodules, declaration := escapedWorstCase("a")
	if _, err := newScopeNoticeAck(cmtTarget(), cmtComment, cmtOwner, "feature", "contract", paths, declaration, inventory.Suggestion{Include: paths}, mixed, submodules); err != nil {
		t.Errorf("the acknowledgment of plain text: %v", err)
	}
	if _, err := newScopeNoticeMessage(cmtTarget(), cmtScopeHead, "feature", paths, declaration, mixed, submodules, false); err != nil {
		t.Errorf("the scope notice of plain text: %v", err)
	}
	var suggestions []scopeSuggestion
	for _, list := range []string{"include", "runner"} {
		patterns := make([]string, 20)
		for i := range patterns {
			patterns[i] = strings.Repeat("a", 99) + fmt.Sprint(i%10)
		}
		suggestions = append(suggestions, scopeSuggestion{List: list, Line: 3, Anchor: "  - " + strings.Repeat("a", 900), Patterns: patterns})
	}
	if _, err := newScopeReviewMessage(cmtTarget(), cmtScopeHead, scopeDigest("x"), suggestions); err != nil {
		t.Errorf("the largest review: %v", err)
	}
}

func TestSuggestRefusesAnAnswerTheOutboxCouldNotTake(t *testing.T) {
	var tests []string
	for i := range 20 {
		tests = append(tests, fmt.Sprintf("<%02d%s/x_test.go", i, strings.Repeat("<", 120)))
	}
	_, _, _, declaration := escapedWorstCase("<")
	w := newCjWorld(t)
	w.seedDeclared(cjID2, "version: 1\ninclude: [/tests/**]\n"+strings.TrimPrefix(string(declaration), "version: 1\n"), withFiles(bootFiles("v1"), tests...))
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	w.requireAckPayload(cjID2, 500, commands.SuggestRefused, map[string]any{"reason": "invalid-source"})
	if !strings.Contains(w.logs.String(), "github suggest refused") {
		t.Fatalf("no log line says the suggestion was refused\nlogs: %s", w.logs)
	}
}
