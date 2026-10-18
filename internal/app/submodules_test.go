package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// The submodules that a suggested pattern would reach (M1.9-E3): carried in the payloads that suggest a declaration, rendered as a warning.

// submoduleTree is a bootTree whose tree also holds submodules, at these paths.
type submoduleTree struct {
	bootTree
	submodules []string
}

func (t *submoduleTree) Entries(ctx context.Context) ([]inventory.Entry, error) {
	entries, err := t.bootTree.Entries(ctx)
	for _, path := range t.submodules {
		entries = append(entries, inventory.Entry{Path: path, Kind: inventory.Submodule})
	}
	return entries, err
}

func treeWithSubmodules(files map[string]string, submodules ...string) *submoduleTree {
	return &submoduleTree{bootTree: bootTree{files: files}, submodules: submodules}
}

func TestCommentPublisherRendersTheSubmodulesOfADeclarationMissingPayload(t *testing.T) {
	for _, disposition := range []string{"propose-refused", "suggested"} {
		t.Run(disposition, func(t *testing.T) {
			w := newCmtWorld(t)
			key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)
			extra := map[string]any{"disposition": disposition, "reason": "declaration-missing", "branch": "feature", "suggestion": []string{"/tests/"}, "submodules": []string{"ext/lib", "we\nird"}}

			if err := w.pub.publish(t.Context(), key, cmtAckPayload(extra)); err != nil {
				t.Fatalf("publish: %v", err)
			}

			w.wantPost(key, commandtext.SuggestDeclaration(commandtext.DeclarationSuggestion{Source: cmtComment, Repo: cmtRepo, Branch: "feature", Patterns: []string{"/tests/"},
				Submodules: []string{"ext/lib", "we\nird"}, Suggest: disposition == "suggested"}))
		})
	}
}

func TestCommentPublisherRendersTheSubmodulesOfAScopeNoticeAcknowledgment(t *testing.T) {
	w := newCmtWorld(t)
	key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)
	extra := map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature/x", "paths": []string{"pkg/a_test.go"}, "pathsTotal": 1, "declaration": suggestDeclaration,
		"submodules": []string{"ext/lib"}}

	if err := w.pub.publish(t.Context(), key, cmtAckPayload(extra)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(key, commandtext.ScopeNoticeComment(commandtext.ScopeNotice{Source: cmtComment, Repo: cmtRepo, Branch: "feature/x", Paths: []string{"pkg/a_test.go"}, PathsTotal: 1,
		Declaration: []byte(suggestDeclaration), Submodules: []string{"ext/lib"}}))
}

func TestCommentPublisherRendersTheSubmodulesOfAScopeNoticeMessage(t *testing.T) {
	w := newCmtWorld(t)

	if err := w.pub.publish(t.Context(), cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["submodules"] = []string{"ext/lib"} })); err != nil {
		t.Fatalf("publish: %v", err)
	}

	w.wantPost(cmtScopeKey, commandtext.ScopeNoticeComment(commandtext.ScopeNotice{Repo: cmtRepo, Branch: "feature/x", Paths: []string{"pkg/a_test.go", "pkg/b_test.go"}, PathsTotal: 3,
		Declaration: []byte(cmtScopeDecl), Mixed: []string{"package.json"}, Submodules: []string{"ext/lib"}}))
}

// A payload written before M1.9-E3 has no submodules: it renders without the warning.
func TestCommentPublisherRendersPayloadsWithoutSubmodulesWithoutTheWarning(t *testing.T) {
	key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)
	for name, tc := range map[string]struct {
		key     string
		payload json.RawMessage
	}{
		"a declaration-missing ack of v0.1.0": {key, json.RawMessage(`{"repositoryId":1001,"repo":"acme/widgets","pull":7,"disposition":"propose-refused","comment":555,"owner":"octo-owner",` +
			`"reason":"declaration-missing","branch":"feature/x","suggestion":["tests/**"]}`)},
		"a declaration-missing ack of M1.9": {key, cmtAckPayload(map[string]any{"disposition": "suggested", "reason": "declaration-missing", "branch": "feature/x", "suggestion": []string{"tests/**"},
			"runner": []string{"conftest.py"}, "mixed": []string{"package.json"}, "cut": true, "declared": true})},
		"a scope-notice ack of M1.9": {key, cmtAckPayload(map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature/x", "paths": []string{"a_test.go"}, "pathsTotal": 1,
			"declaration": suggestDeclaration, "mixed": []string{"package.json"}, "cut": true})},
		"a scope-notice message of M1.9": {cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["cut"] = true })},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			if err := w.pub.publish(t.Context(), tc.key, tc.payload); err != nil {
				t.Fatalf("publish: %v", err)
			}
			posts := w.writer.posts()
			if len(posts) != 1 || strings.Contains(posts[0].body, "ubmodule") {
				t.Fatalf("posted %d comments; want one without a word on submodules", len(posts))
			}
		})
	}
}

func TestCommentPublisherRefusesSubmodulesThatDoNotFit(t *testing.T) {
	twentyOne := slices.Repeat([]string{"ext/lib"}, 21)
	long := []string{strings.Repeat("m", 151)}
	missing := func(submodules []string) map[string]any {
		return map[string]any{"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature", "submodules": submodules}
	}
	notice := func(submodules []string) map[string]any {
		return map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature", "paths": []string{"a_test.go"}, "pathsTotal": 1, "declaration": suggestDeclaration, "submodules": submodules}
	}
	for name, extra := range map[string]map[string]any{
		"21 submodules on a missing declaration":    missing(twentyOne),
		"a long submodule on a missing declaration": missing(long),
		"21 submodules on a scope notice":           notice(twentyOne),
		"a long submodule on a scope notice":        notice(long),
		"submodules on a fork refusal":              {"disposition": "propose-refused", "reason": "fork", "submodules": []string{"ext/lib"}},
		"submodules on a skip":                      {"disposition": "skipped", "submodules": []string{"ext/lib"}},
		"submodules on nothing uncovered":           {"disposition": "suggested", "reason": "nothing-uncovered", "submodules": []string{"ext/lib"}},
		"submodules on a processed acknowledgment":  {"project": string(owner.ProjectID(cmtRepoID)), "suite": "default", "operation": "op-1", "submodules": []string{"ext/lib"}},
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
	for name, submodules := range map[string][]string{"21 submodules": twentyOne, "a long submodule": long} {
		t.Run(name+" on a scope-notice message", func(t *testing.T) {
			w := newCmtWorld(t)
			if err := w.pub.publish(t.Context(), cmtScopeKey, cmtScopePayload(func(m map[string]any) { m["submodules"] = submodules })); err == nil {
				t.Fatal("publish succeeded")
			}
			if len(w.writer.posts()) != 0 {
				t.Fatal("a comment was posted")
			}
		})
	}
}

func TestAcknowledgmentsBoundTheirSubmodules(t *testing.T) {
	long := strings.Repeat("x", 300)
	missing, err := newDeclarationMissingAck(cmtTarget(), cmtComment, cmtOwner, "feature", inventory.Suggestion{Include: []string{"tests/**"}, Submodules: []string{long}}, commands.ProposeRefused)
	if err != nil {
		t.Fatal(err)
	}
	noticeAck, err := newScopeNoticeAck(cmtTarget(), cmtComment, cmtOwner, "feature", "contract", []string{"a_test.go"}, []byte(suggestDeclaration), inventory.Suggestion{}, nil, []string{long})
	if err != nil {
		t.Fatal(err)
	}
	for name, message := range map[string]json.RawMessage{"declaration-missing": missing.Payload, "scope-notice": noticeAck.Payload} {
		var payload ackPayload
		if err := json.Unmarshal(message, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Submodules) != 1 || len(payload.Submodules[0]) > maxSummaryText {
			t.Fatalf("%s: submodules %q, want one entry cut to %d bytes", name, payload.Submodules, maxSummaryText)
		}
	}
	none, err := newDeclarationMissingAck(cmtTarget(), cmtComment, cmtOwner, "feature", inventory.Suggestion{Include: []string{"tests/**"}}, commands.ProposeRefused)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(none.Payload), "submodules") {
		t.Fatalf("payload %s names submodules although there are none", none.Payload)
	}
}

func TestProposeAndSuggestCarryTheSubmodulesTheSuggestionReaches(t *testing.T) {
	main := map[string]string{"README.md": "x"}
	head := treeWithSubmodules(map[string]string{"tests/a_test.go": "x", "src/main.go": "y"}, "ext/lib")
	for _, tc := range []struct {
		name    string
		command cjComment
		d       commands.Disposition
	}{
		{"propose", cjPropose(500, cjAt(10)), commands.ProposeRefused},
		{"suggest", cjSuggest(500, cjAt(10)), commands.Suggested},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(main, nil)
			w.reader.repo(cjRepo1).trees[cjHead] = head
			w.syncAll([]cjComment{tc.command})
			w.mustRun()

			w.requireAckPayload(cjID1, 500, tc.d, map[string]any{"reason": "declaration-missing", "branch": cjBranch, "suggestion": []string{"/tests/", "*_test.go"}, "submodules": []string{"ext/lib"}})
		})
	}
}

func TestSuggestAfterTheFirstContractCarriesTheSubmodulesOfTheHead(t *testing.T) {
	w := newCjWorld(t)
	w.seedDeclared(cjID2, cjDecl, bootFiles("v1"))
	w.reader.repo(cjRepo2).trees[cjHead] = treeWithSubmodules(withFiles(bootFiles("v1"), "pkg/util_test.go"), "ext/lib", "vendor/lib")
	w.syncAll(nil, cjSuggest(500, cjAt(10)))
	w.mustRun()

	_, ack := w.requireDisposition(cjID2, 500, commands.Suggested)
	var payload ackPayload
	if err := json.Unmarshal(ack.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Reason != "scope-notice" || !slices.Equal(payload.Submodules, []string{"ext/lib"}) {
		t.Fatalf("reason %q and submodules %v; want the scope notice with ext/lib (vendor/lib is vendored, so it is not offered)", payload.Reason, payload.Submodules)
	}
}

func TestChecksScopeNoticeCarriesTheSubmodulesOfTheHead(t *testing.T) {
	w := newCJWorld(t, true)
	w.bases = map[int]string{5: cjBase}
	w.reader.repos[bootRepo].trees[cjHead2] = treeWithSubmodules(withFiles(bootFiles("v1"), cjUtilTst), "ext/lib")
	w.tree(cjBase, bootFiles("v1"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()

	messages := w.scopeMessages("scope-notice:")
	if len(messages) != 1 {
		t.Fatalf("%d scope notices, want 1\nlogs: %s", len(messages), w.logs)
	}
	var payload scopeNoticePayload
	if err := json.Unmarshal(messages[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(payload.Submodules, []string{"ext/lib"}) {
		t.Fatalf("submodules %v, want ext/lib", payload.Submodules)
	}
}
