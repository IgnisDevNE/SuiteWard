package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The comment of a runner-uncovered check and its review suggestions (docs/contracts/runtime.md, "Check reason runner-uncovered").

const (
	cmtRunnerKey     = "runner-uncovered:1001:7:" + cmtScopeDigest
	cmtRunnerProtect = "version: 1\ninclude:\n  - /tests/**\nrunner:\n  - jest.config.*\n"
	cmtRunnerIgnore  = "version: 1\ninclude:\n  - /tests/**\nexclude:\n  - jest.config.js\n"
)

// cmtRunnerPayload is the payload of a runner-uncovered comment on cmtPull as the checks job writes it, with change applied.
func cmtRunnerPayload(change func(map[string]any)) json.RawMessage {
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "head": cmtScopeHead, "branch": "feature/x",
		"paths": []string{"jest.config.js", "pkg/conftest.py"}, "pathsTotal": 3, "protect": cmtRunnerProtect, "ignore": cmtRunnerIgnore}
	if change != nil {
		change(payload)
	}
	return cmtMust(json.Marshal(payload))
}

func TestCommentPublisherRendersTheRunnerUncoveredComment(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtRunnerKey, cmtRunnerPayload(nil)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtRunnerKey, commandtext.RunnerUncoveredComment(commandtext.RunnerUncovered{Repo: cmtRepo, Branch: "feature/x", Paths: []string{"jest.config.js", "pkg/conftest.py"}, PathsTotal: 3,
		Protect: []byte(cmtRunnerProtect), Ignore: []byte(cmtRunnerIgnore)}))
}

func TestCommentPublisherRendersARunnerUncoveredCommentWithoutAnIgnoreDeclaration(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { delete(m, "ignore") })); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtRunnerKey, commandtext.RunnerUncoveredComment(commandtext.RunnerUncovered{Repo: cmtRepo, Branch: "feature/x", Paths: []string{"jest.config.js", "pkg/conftest.py"}, PathsTotal: 3,
		Protect: []byte(cmtRunnerProtect)}))
}

func TestCommentPublisherPostsTheRunnerUncoveredCommentOnce(t *testing.T) {
	w := newCmtWorld(t)

	for range 2 {
		if err := w.pub.publish(t.Context(), cmtRunnerKey, cmtRunnerPayload(nil)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	if posts := w.writer.posts(); len(posts) != 1 {
		t.Fatalf("posted %d comments for two deliveries, want 1", len(posts))
	}
}

func TestCommentPublisherRefusesARunnerUncoveredCommentThatDoesNotMatchItsKeyOrCannotBeRendered(t *testing.T) {
	long := strings.Repeat("x", 8193)
	twentyOne := strings.Split(strings.Repeat("jest.config.js,", 21), ",")[:21]
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"another pull request", "runner-uncovered:1001:8:" + cmtScopeDigest, cmtRunnerPayload(nil)},
		{"another repository", "runner-uncovered:1002:7:" + cmtScopeDigest, cmtRunnerPayload(nil)},
		{"a digest that is not SHA-256 hex", "runner-uncovered:1001:7:" + strings.Repeat("g", 64), cmtRunnerPayload(nil)},
		{"a key without a digest", "runner-uncovered:1001:7", cmtRunnerPayload(nil)},
		{"a key with five parts", cmtRunnerKey + ":1", cmtRunnerPayload(nil)},
		{"an unknown field", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["body"] = cmtSecretText })},
		{"a head that is not a commit SHA", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["head"] = "SECRET" })},
		{"a branch that is not a git ref", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["branch"] = "a..b" })},
		{"no paths", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["paths"] = []string{}; m["pathsTotal"] = 0 })},
		{"more than 20 paths", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["paths"] = twentyOne; m["pathsTotal"] = 21 })},
		{"a total below the paths", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["pathsTotal"] = 1 })},
		{"a path over the payload bound", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["paths"] = []string{strings.Repeat("p", 151)}; m["pathsTotal"] = 1 })},
		{"a protect declaration over 8192 bytes", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["protect"] = long })},
		{"an ignore declaration over 8192 bytes", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["ignore"] = long })},
		{"no protect declaration", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["protect"] = "" })},
		{"a repository that is not owner/name", cmtRunnerKey, cmtRunnerPayload(func(m map[string]any) { m["repo"] = "SECRET" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), tc.key, tc.payload)
			if err == nil {
				t.Fatal("publish succeeded")
			}
			for _, leak := range []string{cmtSecretText, "SECRET"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("the error %q holds payload text %q", err, leak)
				}
			}
			if len(w.writer.posts()) != 0 || len(w.writer.listed) != 0 {
				t.Fatal("GitHub was called for a payload that was refused")
			}
		})
	}
}

// A message that no publisher could post is never written: the builder refuses a payload over the outbox bound and a key that is no marker.
func TestNewRunnerUncoveredMessageIsRefusedWhenItCannotBePosted(t *testing.T) {
	target := commentTarget{RepositoryID: 1001, Repo: "octo-org/widgets", Pull: 7}
	digest := cmtScopeDigest
	paths := []string{"jest.config.js"}
	big := []byte("version: 1\n#" + strings.Repeat("<", commandtext.MaxDeclaration-13) + "\n") // JSON writes each < as six bytes

	message, err := newRunnerUncoveredMessage(target, cmtScopeHead, "feature/x", paths, []byte(cmtRunnerProtect), []byte(cmtRunnerIgnore), digest)
	if err != nil {
		t.Fatalf("a plain message is refused: %v", err)
	}
	if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9:_-]{0,199}$`).MatchString(message.Key) || message.Key != "runner-uncovered:1001:7:"+digest || message.Kind != githubCommentKind {
		t.Fatalf("message %+v, want the comment key with its digest", message)
	}
	if _, err := newRunnerUncoveredMessage(target, cmtScopeHead, "feature/x", paths, big, big, digest); err == nil || strings.Contains(err.Error(), "<<<<") {
		t.Fatalf("a payload over the outbox bound is accepted, or its error quotes it: %v", err)
	}
	if _, err := newRunnerUncoveredMessage(target, cmtScopeHead, "a..b", paths, []byte(cmtRunnerProtect), nil, digest); err == nil {
		t.Fatal("a branch that is no git ref is accepted")
	}
}

// The review suggestions of the two choices share the scope-review message and the publisher: each keeps its own marker suffix.

const cmtRunnerReviewKey = "scope-review:1001:7:" + cmtScopeDigest

var (
	cmtProtectList = map[string]any{"list": "protect", "line": 5, "anchor": "  - pytest.toml", "patterns": []string{"jest.config.*"}}
	cmtIgnoreList  = map[string]any{"list": "ignore", "line": 3, "anchor": "  - /tests/**", "patterns": []string{"jest.config.js"}, "newList": "exclude"}
)

func cmtChoiceBody(choice commandtext.RunnerChoice, anchor, indent string, patterns []string, newList, suffix string) string {
	return cmtTrimmed(commandtext.RunnerChoiceBody(choice, anchor, indent, patterns, newList)) + "\n\n" + commandtext.Marker(cmtInstance, cmtRunnerReviewKey+":"+suffix)
}

func TestCommentPublisherPostsTheProtectAndIgnoreSuggestionsEachWithItsOwnMarker(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtRunnerReviewKey, cmtReviewPayload([]map[string]any{cmtProtectList, cmtIgnoreList}, nil)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	want := []cmtReviewPosted{
		{repo: cmtRepo, number: cmtPull, commit: cmtScopeHead, path: ".suiteward.yml", start: 5, line: 5, body: cmtChoiceBody(commandtext.ProtectChoice, "  - pytest.toml", "  ", []string{"jest.config.*"}, "", "protect")},
		{repo: cmtRepo, number: cmtPull, commit: cmtScopeHead, path: ".suiteward.yml", start: 3, line: 3, body: cmtChoiceBody(commandtext.IgnoreChoice, "  - /tests/**", "", []string{"jest.config.js"}, "exclude", "ignore")},
	}
	if got := w.writer.reviewPosted; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("review comments %+v, want %+v", got, want)
	}
}

func TestCommentPublisherRefusesAChoiceSuggestionThatCannotBeRendered(t *testing.T) {
	for name, list := range map[string]map[string]any{
		"a list that creates the other choice's list": {"list": "protect", "line": 3, "anchor": "  - /tests/**", "patterns": []string{"x"}, "newList": "exclude"},
		"a created list of an include suggestion":     {"list": "include", "line": 3, "anchor": "  - /tests/**", "patterns": []string{"x"}, "newList": "runner"},
		"a key indent without a created list":         {"list": "ignore", "line": 3, "anchor": "  - /tests/**", "patterns": []string{"x"}, "keyIndent": "  "},
		"an indent that is not spaces":                {"list": "ignore", "line": 3, "anchor": "- a", "patterns": []string{"x"}, "newList": "exclude", "keyIndent": "SECRET"},
		"a pattern with a quote":                      {"list": "ignore", "line": 3, "anchor": "  - /tests/**", "patterns": []string{`a"b`}},
		"a repeated choice":                           cmtProtectList,
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			lists := []map[string]any{list}
			if name == "a repeated choice" {
				lists = []map[string]any{cmtProtectList, cmtProtectList}
			}
			err := w.pub.publish(t.Context(), cmtRunnerReviewKey, cmtReviewPayload(lists, nil))
			if err == nil {
				t.Fatal("publish succeeded")
			}
			if strings.Contains(err.Error(), "SECRET") || len(w.writer.reviewPosted) != 0 || len(w.writer.reviewListed) != 0 {
				t.Fatalf("the error %q quotes the payload, or GitHub was called", err)
			}
		})
	}
}
