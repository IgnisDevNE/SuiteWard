package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// The review suggestion that goes with a scope notice (docs/contracts/runtime.md, "Scope discovery (M1.9)"): when the pull
// request already edits .suiteward.yml, the new patterns are also offered on the list lines inside the diff.

const (
	// reviewDecl is a block-style declaration whose include item is on line 3.
	reviewDecl = "version: 1\ninclude:\n  - /tests/**\n"
	// reviewBoth has the include item on line 3 and the runner item on line 5.
	reviewBoth = "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - pytest.toml\n"
)

// reviewFiles is the tree of the canonical with the declaration replaced and the extra paths added.
func reviewFiles(declaration string, extra ...string) map[string]string {
	return withFiles(withDeclaration(bootFiles("v1"), declaration), extra...)
}

// changed is the PullFiles entry of .suiteward.yml whose diff has one hunk of the given header.
func changed(header string) []github.PullFile {
	return []github.PullFile{{Path: ".suiteward.yml", Status: "modified", Patch: header + "\n context\n-old\n+new"}}
}

// wantReviewDigest is the digest of a review key: the SHA-256 hex digest of the set digest, a newline and the head's file.
func wantReviewDigest(declaration string, paths ...string) string {
	sum := sha256.Sum256([]byte(scopeDigest(paths...) + "\n" + declaration))
	return hex.EncodeToString(sum[:])
}

func reviewKey(declaration string, paths ...string) string {
	return "scope-review:1001:5:" + wantReviewDigest(declaration, paths...)
}

func (w *cjJobWorld) wantReview(declaration string, paths []string, suggestions ...map[string]any) {
	w.t.Helper()
	messages := w.scopeMessages("scope-review:")
	if len(messages) != 1 || messages[0].Key != reviewKey(declaration, paths...) || messages[0].Kind != githubCommentKind {
		w.t.Fatalf("review messages %v, want exactly %s\nlogs: %s", w.scopeKeys("scope-review:"), reviewKey(declaration, paths...), w.logs)
	}
	var payload map[string]any
	if err := json.Unmarshal(messages[0].Payload, &payload); err != nil {
		w.t.Fatal(err)
	}
	var lists []any
	for _, s := range suggestions {
		lists = append(lists, s)
	}
	want := map[string]any{"repositoryId": float64(bootRepoID), "repo": bootRepo, "pull": float64(5), "head": cjHead2, "suggestions": lists}
	if !reflect.DeepEqual(payload, roundTrip(want)) {
		w.t.Fatalf("payload %s, want %v", messages[0].Payload, want)
	}
}

func roundTrip(value map[string]any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

func suggestion(list string, line int, anchor string, patterns ...string) map[string]any {
	return map[string]any{"list": list, "line": line, "anchor": anchor, "patterns": patterns}
}

func TestChecksWriteAScopeReviewOnTheIncludeLineInsideTheDiff(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}

	w.scopePull(cjHead2, reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))

	if keys := w.scopeKeys("scope-notice:"); !slices.Equal(keys, []string{"scope-notice:1001:5:" + scopeDigest(cjUtilTst)}) {
		t.Fatalf("notices %v, want the one of the added path", keys)
	}
	w.wantReview(reviewDecl, []string{cjUtilTst}, suggestion("include", 3, "  - /tests/**", "*_test.go"))
}

func TestChecksWriteOneScopeReviewForBothListsWithoutThePatternsTheHeadAlreadyHas(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,5 +1,5 @@")}

	w.scopePull(cjHead2, reviewFiles(reviewBoth, cjUtilTst, "conftest.py"), bootFiles("v1"))

	paths := []string{"conftest.py", cjUtilTst}
	w.wantReview(reviewBoth, paths, suggestion("include", 3, "  - /tests/**", "*_test.go"),
		suggestion("runner", 5, "  - pytest.toml", ".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini")) // pytest.toml is in the head's list
}

func TestChecksWriteAScopeReviewOnlyForTheListWithALineInsideTheDiff(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -4,2 +4,2 @@")} // lines 4 and 5: the runner list, not the include item on line 3

	w.scopePull(cjHead2, reviewFiles(reviewBoth, cjUtilTst, "conftest.py"), bootFiles("v1"))

	w.wantReview(reviewBoth, []string{"conftest.py", cjUtilTst}, suggestion("runner", 5, "  - pytest.toml", ".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini"))
}

func TestChecksStripTheLineBreakOfAWindowsFileFromTheAnchor(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}

	w.scopePull(cjHead2, reviewFiles(strings.ReplaceAll(reviewDecl, "\n", "\r\n"), cjUtilTst), bootFiles("v1"))

	w.wantReview(reviewDecl, []string{cjUtilTst}, suggestion("include", 3, "  - /tests/**", "*_test.go"))
}

func TestChecksWriteAScopeNoticeButNoReviewWhenTheDiffGivesNoLineToSuggestOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		decl  string
		files map[int][]github.PullFile
	}{
		{"the pull request does not change the declaration", reviewDecl, map[int][]github.PullFile{5: {{Path: "README.md", Status: "modified", Patch: "@@ -1,3 +1,3 @@\n a"}}}},
		{"no list line is inside a hunk", reviewDecl, map[int][]github.PullFile{5: changed("@@ -1,2 +1,2 @@")}},
		{"the declaration is deleted from the diff's view", reviewDecl, map[int][]github.PullFile{5: {{Path: ".suiteward.yml", Status: "removed"}}}},
		{"the list is in flow style", "version: 1\ninclude: [/tests/**]\n", map[int][]github.PullFile{5: changed("@@ -1,2 +1,2 @@")}},
		{"the head's declaration is not valid", "version: 2\ninclude:\n  - /tests/**\n", map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}},
		{"the head already lists every new pattern", "version: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\nexclude:\n  - pkg/**\n", map[int][]github.PullFile{5: changed("@@ -1,6 +1,6 @@")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCJWorld(t, true)
			w.reader.files = tc.files

			w.scopePull(cjHead2, reviewFiles(tc.decl, cjUtilTst), bootFiles("v1"))

			if keys := w.scopeKeys("scope-notice:"); !slices.Equal(keys, []string{"scope-notice:1001:5:" + scopeDigest(cjUtilTst)}) {
				t.Fatalf("notices %v, want the one of the added path\nlogs: %s", keys, w.logs)
			}
			if keys := w.scopeKeys("scope-review:"); len(keys) != 0 {
				t.Fatalf("reviews %v, want none", keys)
			}
		})
	}
}

func TestChecksWriteNoScopeReviewWithoutANewScopeNotice(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}
	w.scopePull(cjHead2, reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))
	w.wantReview(reviewDecl, []string{cjUtilTst}, suggestion("include", 3, "  - /tests/**", "*_test.go"))
	fileReads := w.reader.fileReads

	// Another head with the same added path: the notice exists, so there is nothing new to suggest on.
	w.tree(cjHead3, reviewFiles(reviewDecl, cjUtilTst, "src/more.go"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()

	if w.reader.fileReads != fileReads {
		t.Fatalf("the files of the pull request were read %d times, want %d: the set was already decided", w.reader.fileReads, fileReads)
	}

	if keys := w.scopeKeys("scope-"); len(keys) != 2 {
		t.Fatalf("scope messages %v, want only the first notice and its review", keys)
	}
}

func TestChecksWriteTheScopeNoticeWhenTheListOfFilesCannotBeRead(t *testing.T) {
	t.Run("a failure that will repeat", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.filesErr = &rest.StatusError{Status: 500, Body: bootSecret}

		w.scopePull(cjHead2, reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))

		if keys := w.scopeKeys("scope-notice:"); len(keys) != 1 || len(w.scopeKeys("scope-review:")) != 0 {
			t.Fatalf("scope messages %v, want the notice and no review", w.scopeKeys("scope-"))
		}
		if strings.Contains(w.logs.String(), bootSecret) {
			t.Fatalf("logs hold GitHub's text: %s", w.logs)
		}
	})
	t.Run("a rate limit waits for the next run", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}
		w.reader.filesErr = &rest.RateLimitedError{Status: 403}

		w.scopePull(cjHead2, reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))

		w.wantNoScopeMessages()
		w.reader.filesErr = nil // the limit ended
		w.mustCheck()
		w.wantReview(reviewDecl, []string{cjUtilTst}, suggestion("include", 3, "  - /tests/**", "*_test.go"))
	})
}

func TestListSuggestionNeverAnchorsOnAScalarThatSpansLines(t *testing.T) {
	for name, raw := range map[string]string{
		"a folded scalar":        "version: 1\ninclude:\n  - /tests/**\n  - >-\n    /x/**\n",
		"a multi-line plain one": "version: 1\ninclude:\n  - /tests/**\n  - /x/\n    y/**\n",
		"a multi-line quoted":    "version: 1\ninclude:\n  - /tests/**\n  - \"/x/\n    y/**\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			var root yaml.Node
			if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
				t.Fatal(err)
			}

			got, ok := listSuggestion(root.Content[0], strings.Split(raw, "\n"), "include", []string{"*_test.go"}, []github.LineRange{{Start: 1, End: 6}})

			if want := (scopeSuggestion{List: "include", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"*_test.go"}}); !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("suggestion %+v (%v), want %+v on the last item that is one line", got, ok, want)
			}
		})
	}
}

func TestChecksLogTheReasonWhenTheReviewCannotBeRendered(t *testing.T) {
	long := strings.Repeat("a", 1100)
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,4 +1,4 @@")}

	w.scopePull(cjHead2, reviewFiles(reviewDecl+"  - /"+long+"/**\n", cjUtilTst), bootFiles("v1"))

	if keys := w.scopeKeys("scope-notice:"); len(keys) != 1 || len(w.scopeKeys("scope-review:")) != 0 {
		t.Fatalf("scope messages %v, want the notice and no review", w.scopeKeys("scope-"))
	}
	line := w.logged("scope review skipped: it cannot be rendered")
	if reason, _ := line["reason"].(string); reason == "" || strings.Contains(reason, long) {
		t.Fatalf("log line %v, want a reason that does not quote the anchor line", line)
	}
	if strings.Contains(w.logs.String(), long) {
		t.Fatal("the logs quote the anchor line")
	}
}
