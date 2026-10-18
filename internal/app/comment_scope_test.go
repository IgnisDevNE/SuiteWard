package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

const (
	cmtScopeHead   = "cccccccccccccccccccccccccccccccccccccccc"
	cmtScopeDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cmtScopeKey    = "scope-notice:1001:7:" + cmtScopeDigest
	cmtScopeDecl   = "version: 1\ninclude:\n  - /tests/**\n  - \"*_test.go\"\n"
)

// cmtScopePayload is the payload of a scope notice on cmtPull as the checks job writes it, with change applied.
func cmtScopePayload(change func(map[string]any)) json.RawMessage {
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "head": cmtScopeHead, "branch": "feature/x",
		"paths": []string{"pkg/a_test.go", "pkg/b_test.go"}, "pathsTotal": 3, "declaration": cmtScopeDecl, "mixed": []string{"package.json"}}
	if change != nil {
		change(payload)
	}
	return cmtMust(json.Marshal(payload))
}

func TestCommentPublisherRendersTheScopeNotice(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtScopeKey, cmtScopePayload(nil)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtScopeKey, commandtext.ScopeNoticeComment(commandtext.ScopeNotice{Repo: cmtRepo, Branch: "feature/x", Paths: []string{"pkg/a_test.go", "pkg/b_test.go"}, PathsTotal: 3,
		Declaration: []byte(cmtScopeDecl), Mixed: []string{"package.json"}}))
}

func TestCommentPublisherPostsTheScopeNoticeOnce(t *testing.T) {
	w := newCmtWorld(t)

	for range 2 {
		if err := w.pub.publish(t.Context(), cmtScopeKey, cmtScopePayload(nil)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	if posts := w.writer.posts(); len(posts) != 1 {
		t.Fatalf("posted %d comments for two deliveries, want 1", len(posts))
	}
}

func TestCommentPublisherRefusesAScopeNoticeThatDoesNotMatchItsKeyOrCannotBeRendered(t *testing.T) {
	long := strings.Repeat("x", 8193)
	twentyOne := strings.Split(strings.Repeat("pkg/a_test.go,", 21), ",")[:21]
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"another pull request", "scope-notice:1001:8:" + cmtScopeDigest, cmtScopePayload(nil)},
		{"another repository", "scope-notice:1002:7:" + cmtScopeDigest, cmtScopePayload(nil)},
		{"a digest that is not SHA-256 hex", "scope-notice:1001:7:" + strings.Repeat("g", 64), cmtScopePayload(nil)},
		{"a key without a digest", "scope-notice:1001:7", cmtScopePayload(nil)},
		{"a key with five parts", "scope-notice:1001:7:" + cmtScopeDigest + ":1", cmtScopePayload(nil)},
		{"an acknowledgment key with four parts", "ack:1001:555:1", cmtScopePayload(nil)},
		{"a revision key with four parts", "revision:1001:P2-R2:1", cmtScopePayload(nil)},
		{"an unknown field", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["body"] = cmtSecretText })},
		{"a head that is not a commit SHA", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["head"] = "SECRET" })},
		{"a branch that is not a git ref", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["branch"] = "a..b" })},
		{"no paths", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["paths"] = []string{}; m["pathsTotal"] = 0 })},
		{"more than 20 paths", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["paths"] = twentyOne; m["pathsTotal"] = 21 })},
		{"a total below the paths", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["pathsTotal"] = 1 })},
		{"a path over the payload bound", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["paths"] = []string{strings.Repeat("p", 151)}; m["pathsTotal"] = 1 })},
		{"a declaration over 8192 bytes", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["declaration"] = long })},
		{"an empty declaration", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["declaration"] = "" })},
		{"a repository that is not owner/name", cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["repo"] = "SECRET" })},
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
