package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

const (
	cmtInvalidDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cmtInvalidKey    = "invalid-source:1001:7:" + cmtInvalidDigest
)

// cmtInvalidPayload is the payload of an invalid-source comment on cmtPull as the checks job writes it, with change applied.
func cmtInvalidPayload(kind string, change func(map[string]any)) json.RawMessage {
	payload := map[string]any{"repositoryId": cmtRepoID, "repo": cmtRepo, "pull": cmtPull, "kind": kind}
	if kind == "link" {
		payload["path"] = "spec_link"
	}
	if change != nil {
		change(payload)
	}
	return cmtMust(json.Marshal(payload))
}

func TestCommentPublisherRendersTheInvalidSourceOfEachKind(t *testing.T) {
	for _, tc := range []struct{ kind, says string }{
		{"declaration", "not a valid declaration"},
		{"link", "```\nspec_link\n```"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			w := newCmtWorld(t)

			if err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload(tc.kind, nil)); err != nil {
				t.Fatalf("publish: %v", err)
			}

			posts := w.writer.posts()
			if len(posts) != 1 || posts[0].repo != cmtRepo || posts[0].number != cmtPull {
				t.Fatalf("posts %v, want one on %s#%d", posts, cmtRepo, cmtPull)
			}
			if !strings.Contains(posts[0].body, tc.says) || !strings.Contains(posts[0].body, "stays in force") || !commandtext.HasMarker(posts[0].body, cmtInstance, cmtInvalidKey) {
				t.Fatalf("body lacks %q or the canonical's standing or its marker:\n%s", tc.says, posts[0].body)
			}
		})
	}
}

func TestCommentPublisherPostsTheInvalidSourceOnce(t *testing.T) {
	w := newCmtWorld(t)

	for range 2 {
		if err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload("link", nil)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	if posts := w.writer.posts(); len(posts) != 1 {
		t.Fatalf("posted %d comments for two deliveries, want 1", len(posts))
	}
}

func TestCommentPublisherRefusesAnInvalidSourceThatDoesNotMatchItsKeyOrCannotBeRendered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"another pull request", "invalid-source:1001:8:" + cmtInvalidDigest, cmtInvalidPayload("link", nil)},
		{"another repository", "invalid-source:1002:7:" + cmtInvalidDigest, cmtInvalidPayload("link", nil)},
		{"a digest that is not SHA-256 hex", "invalid-source:1001:7:" + strings.Repeat("g", 64), cmtInvalidPayload("link", nil)},
		{"a key without a digest", "invalid-source:1001:7", cmtInvalidPayload("link", nil)},
		{"a key with five parts", cmtInvalidKey + ":1", cmtInvalidPayload("link", nil)},
		{"an unknown field", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { m["declaration"] = cmtSecretText })},
		{"an unknown kind", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { m["kind"] = "SECRET" })},
		{"no kind", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { delete(m, "kind") })},
		{"a link without a path", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { delete(m, "path") })},
		{"a declaration with a path", cmtInvalidKey, cmtInvalidPayload("declaration", func(m map[string]any) { m["path"] = ".suiteward.yml" })},
		{"a kind that no longer exists", cmtInvalidKey, cmtInvalidPayload("truncated", nil)},
		{"a path over the payload bound", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { m["path"] = strings.Repeat("p", 151) })},
		{"a repository that is not owner/name", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { m["repo"] = "SECRET" })},
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

func TestSplitKeyKnowsTheInvalidSourceKeyAndKeepsTheOthers(t *testing.T) {
	kind, id, tail, ok := splitKey(cmtInvalidKey)
	if !ok || kind != "invalid-source" || id != 1001 || tail != "7:"+cmtInvalidDigest {
		t.Fatalf("splitKey = %q, %d, %q, %v", kind, id, tail, ok)
	}
	for _, key := range []string{"ack:1001:555", "bootstrap-notice:1001:7", "merge:1001:7", "revision:1001:P2-R2", "scope-notice:1001:7:" + cmtInvalidDigest} {
		if _, _, _, ok := splitKey(key); !ok {
			t.Errorf("splitKey refused %q", key)
		}
	}
}
