package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
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

// wantSuggestionsDigest is the digest of a review key: the SHA-256 hex digest of the head's file, a newline and the suggestions as JSON.
func wantSuggestionsDigest(declaration string, suggestions ...map[string]any) string {
	raw, err := json.Marshal(suggestions)
	if err != nil {
		panic(err)
	}
	var decoded []scopeSuggestion
	if err := json.Unmarshal(raw, &decoded); err != nil {
		panic(err)
	}
	digest, err := suggestionsDigest([]byte(declaration), decoded)
	if err != nil {
		panic(err)
	}
	return digest
}

func reviewKey(declaration string, suggestions ...map[string]any) string {
	return "scope-review:1001:5:" + wantSuggestionsDigest(declaration, suggestions...)
}

func (w *cjJobWorld) wantReview(declaration string, suggestions ...map[string]any) {
	w.t.Helper()
	messages := w.scopeMessages("scope-review:")
	if len(messages) != 1 || messages[0].Key != reviewKey(declaration, suggestions...) || messages[0].Kind != githubCommentKind {
		w.t.Fatalf("review messages %v, want exactly %s\nlogs: %s", w.scopeKeys("scope-review:"), reviewKey(declaration, suggestions...), w.logs)
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

// suggestion is the payload of one suggestion on a list whose items are all in the style of the anchor; the payload carries that style.
func suggestion(list string, line int, anchor string, patterns ...string) map[string]any {
	style := commandtext.QuotingPlain
	switch {
	case strings.HasPrefix(strings.TrimLeft(anchor, " -"), `"`):
		style = commandtext.QuotingDouble
	case strings.HasPrefix(strings.TrimLeft(anchor, " -"), "'"):
		style = commandtext.QuotingSingle
	}
	return map[string]any{"list": list, "line": line, "anchor": anchor, "patterns": patterns, "quoting": string(style)}
}

func TestChecksWriteAScopeReviewOnTheIncludeLineInsideTheDiff(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}

	w.scopePull(reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))

	if keys := w.scopeKeys("scope-notice:"); !slices.Equal(keys, []string{"scope-notice:1001:5:" + scopeDigest(cjUtilTst)}) {
		t.Fatalf("notices %v, want the one of the added path", keys)
	}
	w.wantReview(reviewDecl, suggestion("include", 3, "  - /tests/**", "*_test.go"))
}

func TestChecksWriteOneScopeReviewForBothListsWithoutThePatternsTheHeadAlreadyHas(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,5 +1,5 @@")}

	w.scopePull(reviewFiles(reviewBoth, cjUtilTst, "conftest.py"), bootFiles("v1"))

	w.wantReview(reviewBoth, suggestion("include", 3, "  - /tests/**", "*_test.go"),
		suggestion("runner", 5, "  - pytest.toml", ".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini")) // pytest.toml is in the head's list
}

func TestChecksWriteAScopeReviewOnlyForTheListWithALineInsideTheDiff(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -4,2 +4,2 @@")} // lines 4 and 5: the runner list, not the include item on line 3

	w.scopePull(reviewFiles(reviewBoth, cjUtilTst, "conftest.py"), bootFiles("v1"))

	w.wantReview(reviewBoth, suggestion("runner", 5, "  - pytest.toml", ".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini"))
}

func TestChecksStripTheLineBreakOfAWindowsFileFromTheAnchor(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,3 +1,3 @@")}

	windows := strings.ReplaceAll(reviewDecl, "\n", "\r\n")
	w.scopePull(reviewFiles(windows, cjUtilTst), bootFiles("v1"))

	w.wantReview(windows, suggestion("include", 3, "  - /tests/**", "*_test.go"))
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

			w.scopePull(reviewFiles(tc.decl, cjUtilTst), bootFiles("v1"))

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
	w.scopePull(reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))
	w.wantReview(reviewDecl, suggestion("include", 3, "  - /tests/**", "*_test.go"))
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
		w.reader.filesErr = fmt.Errorf("%w: %s", &rest.StatusError{Status: 500}, bootSecret)

		w.scopePull(reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))

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

		w.scopePull(reviewFiles(reviewDecl, cjUtilTst), bootFiles("v1"))

		w.wantNoScopeMessages()
		w.reader.filesErr = nil // the limit ended
		w.mustCheck()
		w.wantReview(reviewDecl, suggestion("include", 3, "  - /tests/**", "*_test.go"))
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

			if want := (scopeSuggestion{List: "include", Line: 3, Anchor: "  - /tests/**", Patterns: []string{"*_test.go"}, Quoting: commandtext.QuotingPlain}); !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("suggestion %+v (%v), want %+v on the last item that is one line", got, ok, want)
			}
		})
	}
}

func TestChecksLogTheReasonWhenTheReviewCannotBeRendered(t *testing.T) {
	long := strings.Repeat("a", 1100)
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,4 +1,4 @@")}

	w.scopePull(reviewFiles(reviewDecl+"  - /"+long+"/**\n", cjUtilTst), bootFiles("v1"))

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

// A list of mixed quoting is extended in plain items, as scope.AddPatterns writes them, whatever the quoting of the item the suggestion sits on
// (decision 9 of M1.10): the suggestion carries the style of the list, not the anchor's.
func TestChecksSuggestOnAMixedListInTheStyleOfScopeAddPatterns(t *testing.T) {
	const declaration = "version: 1\ninclude:\n  - /other/**\n  - \"/tests/**\"\n"
	w := newCJWorld(t, true)
	w.reader.files = map[int][]github.PullFile{5: changed("@@ -1,4 +1,4 @@")}

	w.scopePull(reviewFiles(declaration, cjUtilTst), bootFiles("v1"))

	w.wantReview(declaration, map[string]any{"list": "include", "line": 4, "anchor": `  - "/tests/**"`, "patterns": []string{"*_test.go"}, "quoting": "plain"})
	got := decodeSuggestions(t, w.scopeMessages("scope-review:")[0].Payload)
	if applied, want := apply(t, declaration, got[0]), addPatterns(t, declaration, []string{"*_test.go"}, nil); applied != want {
		t.Errorf("the suggestion applied gives\n%s\nwant what AddPatterns writes\n%s", applied, want)
	}
}

// Each list takes its own style: a list whose items all share a quoting keeps it, and one that does not is plain.
func TestListSuggestionCarriesTheQuotingOfTheList(t *testing.T) {
	for name, c := range map[string]struct {
		list string
		want commandtext.Quoting
	}{
		"double quoted items":  {"  - \"a/**\"\n  - \"b/**\"", commandtext.QuotingDouble},
		"single quoted items":  {"  - 'a/**'\n  - 'b/**'", commandtext.QuotingSingle},
		"plain items":          {"  - a/**\n  - b/**", commandtext.QuotingPlain},
		"plain then quoted":    {"  - a/**\n  - \"b/**\"", commandtext.QuotingPlain},
		"quoted then plain":    {"  - \"a/**\"\n  - b/**", commandtext.QuotingPlain},
		"double then single":   {"  - \"a/**\"\n  - 'b/**'", commandtext.QuotingPlain},
		"one quoted item only": {"  - \"a/**\"", commandtext.QuotingDouble},
	} {
		t.Run(name, func(t *testing.T) {
			raw := "version: 1\ninclude:\n" + c.list + "\n"
			var root yaml.Node
			if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
				t.Fatal(err)
			}

			got, ok := listSuggestion(root.Content[0], strings.Split(raw, "\n"), "include", []string{"c/**"}, []github.LineRange{{Start: 1, End: 6}})

			if !ok || got.Quoting != c.want {
				t.Fatalf("suggestion %+v (%v), want the quoting %q", got, ok, c.want)
			}
		})
	}
}
