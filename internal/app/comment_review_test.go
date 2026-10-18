package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

const cmtReviewKey = "scope-review:1001:7:" + cmtScopeDigest

var (
	cmtIncludeList = map[string]any{"list": "include", "line": 3, "anchor": "  - /tests/**", "patterns": []string{"*_test.go"}}
	cmtRunnerList  = map[string]any{"list": "runner", "line": 6, "anchor": "- conftest.py", "patterns": []string{".pytest.ini", "pytest.ini"}}
)

// cmtReviewPayload is the payload of a scope review on cmtPull as the checks job writes it, with change applied.
func cmtReviewPayload(lists []map[string]any, change func(map[string]any)) json.RawMessage {
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "head": cmtScopeHead, "suggestions": lists}
	if change != nil {
		change(payload)
	}
	return cmtMust(json.Marshal(payload))
}

func cmtIncludeBody() string {
	return commandtext.ScopeSuggestionBody("  - /tests/**", "  ", []string{"*_test.go"}) + "\n\n" + commandtext.Marker(cmtInstance, cmtReviewKey+":include")
}

func cmtRunnerBody() string {
	return commandtext.ScopeSuggestionBody("- conftest.py", "", []string{".pytest.ini", "pytest.ini"}) + "\n\n" + commandtext.Marker(cmtInstance, cmtReviewKey+":runner")
}

func TestCommentPublisherPostsTheScopeReviewOnTheLineOfTheList(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList}, nil)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	want := []cmtReviewPosted{{repo: cmtRepo, number: cmtPull, commit: cmtScopeHead, path: ".suiteward.yml", start: 3, line: 3, body: cmtIncludeBody()}}
	if got := w.writer.reviewPosted; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("review comments %+v, want %+v", got, want)
	}
	if len(w.writer.posts()) != 0 || len(w.writer.listed) != 0 {
		t.Fatal("a review suggestion is not an issue comment: the pull request's conversation was read or written")
	}
	if len(w.writer.reviewListed) != 1 || w.writer.reviewListed[0] != cmtBotID {
		t.Fatalf("review readbacks %v, want one, by the bot", w.writer.reviewListed)
	}
}

func TestCommentPublisherPostsOneReviewCommentPerList(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList, cmtRunnerList}, nil)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	posted := w.writer.reviewPosted
	if len(posted) != 2 || posted[0].body != cmtIncludeBody() || posted[0].line != 3 || posted[1].body != cmtRunnerBody() || posted[1].line != 6 {
		t.Fatalf("review comments %+v, want the include suggestion on line 3, then the runner suggestion on line 6", posted)
	}
}

func TestCommentPublisherPostsAScopeReviewOnceAndOnlyWhatIsMissing(t *testing.T) {
	t.Run("two deliveries", func(t *testing.T) {
		w := newCmtWorld(t)
		for range 2 {
			if err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList, cmtRunnerList}, nil)); err != nil {
				t.Fatalf("publish: %v", err)
			}
		}
		if len(w.writer.reviewPosted) != 2 {
			t.Fatalf("%d review comments for two deliveries, want 2", len(w.writer.reviewPosted))
		}
	})
	t.Run("one list was posted before a crash", func(t *testing.T) {
		w := newCmtWorld(t)
		w.writer.reviews = append(w.writer.reviews, github.Comment{ID: 5, AuthorID: cmtBotID, Body: cmtIncludeBody()})

		if err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList, cmtRunnerList}, nil)); err != nil {
			t.Fatalf("publish: %v", err)
		}

		if posted := w.writer.reviewPosted; len(posted) != 1 || posted[0].body != cmtRunnerBody() {
			t.Fatalf("review comments %+v, want only the runner suggestion", posted)
		}
	})
	t.Run("another account's comment with the marker does not count", func(t *testing.T) {
		w := newCmtWorld(t)
		w.writer.reviews = append(w.writer.reviews, github.Comment{ID: 5, AuthorID: cmtBotID + 1, Body: cmtIncludeBody()})

		if err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList}, nil)); err != nil {
			t.Fatalf("publish: %v", err)
		}

		if len(w.writer.reviewPosted) != 1 {
			t.Fatalf("%d review comments, want the suggestion posted", len(w.writer.reviewPosted))
		}
	})
}

func TestCommentPublisherCompletesAScopeReviewThatGitHubRefusesAndLogsOnlyTheKindAndStatus(t *testing.T) {
	w := newCmtWorld(t)
	w.writer.reviewPostErrs = []error{fmt.Errorf("post review comment: %w", &rest.StatusError{Status: 422, Body: cmtSecretText})}

	err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList, cmtRunnerList}, nil))

	if err != nil {
		t.Fatalf("publish = %v, want the message completed without a retry", err)
	}
	if posted := w.writer.reviewPosted; len(posted) != 1 || posted[0].body != cmtRunnerBody() {
		t.Fatalf("review comments %+v, want the other list still posted", posted)
	}
	logs := w.logs.String()
	if !strings.Contains(logs, `"kind":"unprocessable"`) || !strings.Contains(logs, `"status":422`) || strings.Contains(logs, cmtSecretText) {
		t.Fatalf("logs %s, want the kind and the status of the failure and none of GitHub's text", logs)
	}
}

func TestCommentPublisherRetriesAScopeReviewOnAnyOtherFailure(t *testing.T) {
	for name, failure := range map[string]error{
		"a server error":  &rest.StatusError{Status: 500, Body: cmtSecretText},
		"a rate limit":    &rest.RateLimitedError{Status: 403, Body: cmtSecretText},
		"a transport one": errors.New("connection reset"),
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			w.writer.reviewPostErrs = []error{failure}

			err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList}, nil))

			if err == nil || strings.Contains(err.Error(), cmtSecretText) {
				t.Fatalf("publish = %v, want an error that holds none of GitHub's text", err)
			}
		})
	}
	t.Run("a failed readback", func(t *testing.T) {
		w := newCmtWorld(t)
		w.writer.reviewListErr = &rest.StatusError{Status: 500, Body: cmtSecretText}

		err := w.pub.publish(t.Context(), cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList}, nil))

		if err == nil || strings.Contains(err.Error(), cmtSecretText) || len(w.writer.reviewPosted) != 0 {
			t.Fatalf("publish = %v with %d posts, want an error and nothing posted", err, len(w.writer.reviewPosted))
		}
	})
}

func TestCommentPublisherRefusesAScopeReviewThatDoesNotMatchItsKeyOrCannotBeRendered(t *testing.T) {
	with := func(list map[string]any, field string, value any) map[string]any {
		changed := map[string]any{}
		for k, v := range list {
			changed[k] = v
		}
		changed[field] = value
		return changed
	}
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"another pull request", "scope-review:1001:8:" + cmtScopeDigest, cmtReviewPayload([]map[string]any{cmtIncludeList}, nil)},
		{"another repository", "scope-review:1002:7:" + cmtScopeDigest, cmtReviewPayload([]map[string]any{cmtIncludeList}, nil)},
		{"a key with three parts", "scope-review:1001:7", cmtReviewPayload([]map[string]any{cmtIncludeList}, nil)},
		{"a digest that is not SHA-256 hex", "scope-review:1001:7:" + strings.Repeat("g", 64), cmtReviewPayload([]map[string]any{cmtIncludeList}, nil)},
		{"an unknown field", cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList}, func(m map[string]any) { m["body"] = cmtSecretText })},
		{"a head that is not a commit SHA", cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList}, func(m map[string]any) { m["head"] = "SECRET" })},
		{"no suggestion", cmtReviewKey, cmtReviewPayload(nil, nil)},
		{"the same list twice", cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList, cmtIncludeList}, nil)},
		{"a list that is neither include nor runner", cmtReviewKey, cmtReviewPayload([]map[string]any{with(cmtIncludeList, "list", "exclude")}, nil)},
		{"a line of zero", cmtReviewKey, cmtReviewPayload([]map[string]any{with(cmtIncludeList, "line", 0)}, nil)},
		{"an anchor that is not a list item", cmtReviewKey, cmtReviewPayload([]map[string]any{with(cmtIncludeList, "anchor", "include: [a]")}, nil)},
		{"an anchor with a line break", cmtReviewKey, cmtReviewPayload([]map[string]any{with(cmtIncludeList, "anchor", "  - a\n  - b")}, nil)},
		{"no pattern", cmtReviewKey, cmtReviewPayload([]map[string]any{with(cmtIncludeList, "patterns", []string{})}, nil)},
		{"a pattern YAML would have to escape", cmtReviewKey, cmtReviewPayload([]map[string]any{with(cmtIncludeList, "patterns", []string{"a\"b"})}, nil)},
		{"a repository that is not owner/name", cmtReviewKey, cmtReviewPayload([]map[string]any{cmtIncludeList}, func(m map[string]any) { m["repo"] = "SECRET" })},
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
			if len(w.writer.reviewPosted) != 0 || len(w.writer.reviewListed) != 0 || len(w.writer.posts()) != 0 || len(w.writer.listed) != 0 {
				t.Fatal("GitHub was called for a payload that was refused")
			}
		})
	}
}
