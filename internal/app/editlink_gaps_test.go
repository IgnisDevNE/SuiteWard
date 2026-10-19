package app

import (
	"fmt"
	"strings"
	"testing"
)

// D-SCOPE-DISCOVERY, every suggested change links to GitHub's editor: the reply that puts the declaration on the default branch, and the
// refusals that ask the owner to fix .suiteward.yml, carry the branch of the file to edit. A payload queued without it renders without the link.

func (w *cmtWorld) publishedAck(t *testing.T, extra map[string]any) string {
	t.Helper()
	if err := w.pub.publish(t.Context(), fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment), cmtAckPayload(extra)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	posts := w.writer.posts()
	if len(posts) != 1 {
		t.Fatalf("%d posts, want 1", len(posts))
	}
	return posts[0].body
}

func TestCommentPublisherLinksTheEditorOfTheFileTheAcknowledgmentAsksToChange(t *testing.T) {
	scope := map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature/x", "base": "default-branch", "declaration": suggestDeclaration, "addInclude": []string{"*_test.go"}, "paths": []string{"a_test.go"}, "pathsTotal": 1}
	for _, tc := range []struct {
		name  string
		extra map[string]any
		link  string // the editor link, none when the payload has no branch of the file
	}{
		{"scope notice for the default branch", scope, "/edit/release/2%231/.suiteward.yml"},
		{"suggest refused for the head's declaration", map[string]any{"disposition": "suggest-refused", "reason": "invalid-source", "base": "pull", "branch": "feat/a#b"}, "/edit/feat/a%23b/.suiteward.yml"},
		{"suggest refused for the default branch's declaration", map[string]any{"disposition": "suggest-refused", "reason": "invalid-source", "base": "default-branch", "branch": "main"}, "/edit/main/.suiteward.yml"},
		{"suggest refused for a declaration too large", map[string]any{"disposition": "suggest-refused", "reason": "declaration-too-large", "branch": "feat/a#b"}, "/edit/feat/a%23b/.suiteward.yml"},
		{"propose refused for an invalid source", map[string]any{"disposition": "propose-refused", "reason": "invalid-source", "base": "default-branch", "branch": "main"}, "/edit/main/.suiteward.yml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := tc.extra
			if tc.name == "scope notice for the default branch" {
				extra = map[string]any{}
				for k, v := range scope {
					extra[k] = v
				}
				extra["defaultBranch"] = "release/2#1"
			}

			body := newCmtWorld(t).publishedAck(t, extra)

			if want := "https://github.com/" + cmtRepo + tc.link; !strings.Contains(body, "[open GitHub's editor for it]("+want+")") {
				t.Errorf("the body lacks the link %q:\n%s", want, body)
			}
		})
	}
}

func TestCommentPublisherRendersTheseAcknowledgmentsQueuedBeforeTheBranchWithoutTheLink(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"a scope notice for the default branch": {"disposition": "suggested", "reason": "scope-notice", "branch": "feature/x", "base": "default-branch", "declaration": suggestDeclaration, "addInclude": []string{"*_test.go"}, "paths": []string{"a_test.go"}, "pathsTotal": 1},
		"a suggest refused for a file":          {"disposition": "suggest-refused", "reason": "invalid-source", "base": "pull"},
		"a suggest refused, too large":          {"disposition": "suggest-refused", "reason": "declaration-too-large"},
		"a propose refused":                     {"disposition": "propose-refused", "reason": "invalid-source"},
	} {
		t.Run(name, func(t *testing.T) {
			if body := newCmtWorld(t).publishedAck(t, extra); strings.Contains(body, "editor") || strings.Contains(body, "/edit/") {
				t.Errorf("the body has an editor link:\n%s", body)
			}
		})
	}
}

func TestCommentPublisherRefusesABranchOfTheFileThatDoesNotFit(t *testing.T) {
	scope := func(change func(map[string]any)) map[string]any {
		payload := map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature", "base": "default-branch", "declaration": suggestDeclaration, "addInclude": []string{"*_test.go"}, "paths": []string{"a_test.go"}, "pathsTotal": 1, "defaultBranch": "main"}
		change(payload)
		return payload
	}
	for name, extra := range map[string]map[string]any{
		"a default branch on a notice of the head's file":  scope(func(m map[string]any) { m["base"] = "pull" }),
		"a default branch on a notice of the contract":     scope(func(m map[string]any) { m["base"] = "contract" }),
		"a default branch that is no ref":                  scope(func(m map[string]any) { m["defaultBranch"] = "a b" }),
		"a default branch on nothing uncovered":            {"disposition": "suggested", "reason": "nothing-uncovered", "base": "default-branch", "defaultBranch": "main"},
		"a branch on a fork refusal":                       {"disposition": "suggest-refused", "reason": "fork", "branch": "feature"},
		"a branch on a suggest refused with no file":       {"disposition": "suggest-refused", "reason": "invalid-source", "branch": "feature"},
		"a branch that is no ref on a suggest refused":     {"disposition": "suggest-refused", "reason": "invalid-source", "base": "pull", "branch": "a b"},
		"a branch on a propose refused for another reason": {"disposition": "propose-refused", "reason": "not-open", "branch": "feature"},
		"a base on a propose refused for another reason":   {"disposition": "propose-refused", "reason": "fork", "base": "pull"},
		"a contract as the file of a propose refused":      {"disposition": "propose-refused", "reason": "invalid-source", "base": "contract", "branch": "main"},
		"a branch on a skip refused":                       {"disposition": "skip-refused", "reason": "fork", "branch": "feature"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)

			err := w.pub.publish(t.Context(), fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment), cmtAckPayload(extra))

			if err == nil || !strings.Contains(err.Error(), "does not match its key") || len(w.writer.posts()) != 0 {
				t.Fatalf("publish = %v with %d posts; want a validation error and no post", err, len(w.writer.posts()))
			}
		})
	}
}

// An invalid-source comment for a head that has no .suiteward.yml links to GitHub's new-file page, not to an editor that would be a 404.
func TestCommentPublisherLinksTheNewFilePageForALinkOfAHeadWithoutADeclaration(t *testing.T) {
	body := newCmtWorld(t).publishedInvalidSource(t, "link", map[string]any{"branch": "feat/a#b", "noDeclaration": true})

	want := "[open GitHub's editor for a new file](https://github.com/" + cmtRepo + "/new/feat/a%23b?filename=.suiteward.yml)"
	if !strings.Contains(body, want) || strings.Contains(body, "/edit/") {
		t.Errorf("the body lacks %q or has an edit link:\n%s", want, body)
	}
}

func TestCommentPublisherRefusesANoDeclarationFlagThatDoesNotFit(t *testing.T) {
	for name, tc := range map[string]struct {
		kind   string
		change map[string]any
	}{
		"on a declaration problem":       {"declaration", map[string]any{"noDeclaration": true}},
		"with the declaration of a link": {"link", map[string]any{"noDeclaration": true, "declaration": "version: 1\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)

			err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload(tc.kind, func(m map[string]any) {
				for k, v := range tc.change {
					m[k] = v
				}
			}))

			if err == nil || len(w.writer.posts()) != 0 {
				t.Fatalf("publish = %v with %d posts, want a refusal", err, len(w.writer.posts()))
			}
		})
	}
}

func (w *cmtWorld) publishedInvalidSource(t *testing.T, kind string, extra map[string]any) string {
	t.Helper()
	if err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload(kind, func(m map[string]any) {
		for k, v := range extra {
			m[k] = v
		}
	})); err != nil {
		t.Fatalf("publish: %v", err)
	}
	posts := w.writer.posts()
	if len(posts) != 1 {
		t.Fatalf("%d posts, want 1", len(posts))
	}
	return posts[0].body
}

// The refusal of a declaration too large for the default branch's file links to that branch's editor, in the words of the default branch.
func TestCommentPublisherLinksTheDefaultBranchForADeclarationTooLargeAgainstIt(t *testing.T) {
	body := newCmtWorld(t).publishedAck(t, map[string]any{"disposition": "suggest-refused", "reason": "declaration-too-large", "base": "default-branch", "branch": "main"})

	if want := "[open GitHub's editor for it](https://github.com/" + cmtRepo + "/edit/main/.suiteward.yml)"; !strings.Contains(body, want) || !strings.Contains(body, "on the default branch") {
		t.Errorf("the body lacks %q or the words of the default branch:\n%s", want, body)
	}
}
