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
		{"declaration", "is not valid"},
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
			if !strings.Contains(posts[0].body, tc.says) || !strings.Contains(posts[0].body, "stays as it is") || !commandtext.HasMarker(posts[0].body, cmtInstance, cmtInvalidKey) {
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
		{"an unknown field", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { m["body"] = cmtLeakText })},
		{"a declaration of a declaration problem", cmtInvalidKey, cmtInvalidPayload("declaration", func(m map[string]any) { m["declaration"] = "version: 1\n" })},
		{"a declaration over the bound", cmtInvalidKey, cmtInvalidPayload("link", func(m map[string]any) { m["declaration"] = strings.Repeat("#", commandtext.MaxDeclaration+1) })},
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
			for _, leak := range []string{cmtLeakText, "SECRET"} {
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

// D-SCOPE, an accepted link: the payload carries the head's declaration with the link's exact anchored path in exclude. A payload that
// v0.1.1-rc.6 queued has none and still renders, with the one-line pattern.
func TestCommentPublisherRendersTheDeclarationThatAcceptsTheLink(t *testing.T) {
	const declaration = "version: 1\ninclude: ['test_*.py']\nexclude:\n  - /spec_link\n"
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		says   string
	}{
		{"with a declaration", func(m map[string]any) { m["declaration"] = declaration }, "```\n" + declaration + "```"},
		{"queued before it existed", nil, "```\n/spec_link\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)

			if err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload("link", tc.change)); err != nil {
				t.Fatalf("publish: %v", err)
			}

			posts := w.writer.posts()
			if len(posts) != 1 || !strings.Contains(posts[0].body, tc.says) || !strings.Contains(posts[0].body, "nothing reached through the link is protected") {
				t.Fatalf("posts %v, want one that holds %q and the warning", posts, tc.says)
			}
		})
	}
}

func TestInvalidSourceMessageCarriesTheDeclarationAndDropsOneOverTheBound(t *testing.T) {
	target := commentTarget{RepositoryID: cmtRepoID, Repo: cmtRepo, Pull: cmtPull}
	declaration := []byte("version: 1\nexclude:\n  - /spec_link\n")
	for _, tc := range []struct {
		name        string
		declaration []byte
		want        string
	}{
		{"within the bound", declaration, string(declaration)},
		{"over the bound", append([]byte("# "), make([]byte, commandtext.MaxDeclaration)...), ""},
		{"none", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, err := newInvalidSourceMessage(target, "feature", sourceProblem{kind: sourceLink, path: "spec_link", declaration: tc.declaration})
			if err != nil {
				t.Fatalf("newInvalidSourceMessage: %v", err)
			}

			p, err := decodePayload[invalidSourcePayload](githubCommentKind, message.Key, message.Payload)
			if err != nil || p.Declaration != tc.want || p.validate() != nil {
				t.Fatalf("decoded payload %+v, %v; want the declaration %q and a valid payload", p, err, tc.want)
			}
		})
	}
}

// The payload says that its path was cut, so the comment does not offer the cut path as an exclude pattern.
func TestInvalidSourceMessageMarksACutPathAndTheCommentOffersNoCutPattern(t *testing.T) {
	target := commentTarget{RepositoryID: cmtRepoID, Repo: cmtRepo, Pull: cmtPull}
	long := strings.Repeat("l", 200) + "_link"
	for _, tc := range []struct {
		name        string
		path        string
		declaration []byte
		cut, offers bool
	}{
		{"a short path", "spec_link", nil, false, true},
		{"a long path", long, nil, true, false},
		{"a long path with its declaration", long, []byte("version: 1\nexclude:\n  - /whole\n"), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, err := newInvalidSourceMessage(target, "feature", sourceProblem{kind: sourceLink, path: tc.path, declaration: tc.declaration})
			if err != nil {
				t.Fatalf("newInvalidSourceMessage: %v", err)
			}
			p, err := decodePayload[invalidSourcePayload](githubCommentKind, message.Key, message.Payload)
			if err != nil || p.PathCut != tc.cut {
				t.Fatalf("decoded payload %+v, %v; want pathCut %v", p, err, tc.cut)
			}

			text := commandtext.InvalidSourceComment(p.source())

			if offers := strings.Contains(text, "accept the link"); offers != tc.offers {
				t.Errorf("the comment offers to accept the link: %v, want %v:\n%s", offers, tc.offers, text)
			}
		})
	}
}

// D-SCOPE-DISCOVERY, every suggested change links to GitHub's editor: the payload carries the pull request's head ref as branch, next
// to the repo every comment payload has. A payload that v0.1.1-rc.7 queued has no branch and renders without the link.
func TestCommentPublisherLinksTheInvalidSourceToTheEditorOnItsBranch(t *testing.T) {
	const branch = "feat/a#b%c/ünï"
	for _, kind := range []string{"declaration", "link"} {
		t.Run(kind, func(t *testing.T) {
			w := newCmtWorld(t)

			if err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload(kind, func(m map[string]any) { m["branch"] = branch })); err != nil {
				t.Fatalf("publish: %v", err)
			}

			posts := w.writer.posts()
			want := "[open GitHub's editor for it](https://github.com/" + cmtRepo + "/edit/feat/a%23b%25c/%C3%BCn%C3%AF/.suiteward.yml)"
			if len(posts) != 1 || !strings.Contains(posts[0].body, want) {
				t.Fatalf("posts %v, want one that holds %q", posts, want)
			}
		})
	}
}

func TestCommentPublisherRendersAnInvalidSourceQueuedBeforeTheBranchWithoutTheLink(t *testing.T) {
	for _, kind := range []string{"declaration", "link"} {
		t.Run(kind, func(t *testing.T) {
			w := newCmtWorld(t)

			if err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload(kind, nil)); err != nil {
				t.Fatalf("publish: %v", err)
			}

			if posts := w.writer.posts(); len(posts) != 1 || strings.Contains(posts[0].body, "editor") || strings.Contains(posts[0].body, "/edit/") {
				t.Fatalf("posts %v, want one without the editor link", posts)
			}
		})
	}
}

func TestCommentPublisherRefusesAnInvalidSourceWithABranchThatIsNoRef(t *testing.T) {
	w := newCmtWorld(t)

	err := w.pub.publish(t.Context(), cmtInvalidKey, cmtInvalidPayload("declaration", func(m map[string]any) { m["branch"] = "SECRET branch" }))

	if err == nil || strings.Contains(err.Error(), "SECRET") || len(w.writer.posts()) != 0 {
		t.Fatalf("publish = %v with %d posts, want a refusal that quotes nothing", err, len(w.writer.posts()))
	}
}

func TestInvalidSourceMessageCarriesTheBranchForTheEditorLink(t *testing.T) {
	message, err := newInvalidSourceMessage(commentTarget{RepositoryID: cmtRepoID, Repo: cmtRepo, Pull: cmtPull}, "feat/x", sourceProblem{kind: sourceDeclaration})
	if err != nil {
		t.Fatalf("newInvalidSourceMessage: %v", err)
	}

	p, err := decodePayload[invalidSourcePayload](githubCommentKind, message.Key, message.Payload)
	if err != nil || p.Branch != "feat/x" || p.validate() != nil {
		t.Fatalf("decoded payload %+v, %v; want the branch and a valid payload", p, err)
	}
	if _, err := newInvalidSourceMessage(commentTarget{RepositoryID: cmtRepoID, Repo: cmtRepo, Pull: cmtPull}, "a b", sourceProblem{kind: sourceDeclaration}); err == nil {
		t.Error("newInvalidSourceMessage accepted a branch that is no ref")
	}
}
