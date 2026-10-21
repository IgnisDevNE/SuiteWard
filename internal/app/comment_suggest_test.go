package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// The acknowledgments of /suiteward suggest (docs/contracts/runtime.md, "Scope discovery (M1.9)").

const suggestDeclaration = "version: 1\ninclude:\n  - \"tests/**\"\n  - \"*_test.go\"\n"

func TestCommentPublisherRendersTheSuggestAcknowledgments(t *testing.T) {
	ack := func(a commandtext.Ack) func() string {
		a.Owner, a.Source = cmtOwner, cmtComment
		return func() string { return commandtext.Acknowledge(a) }
	}
	notice := func() string {
		return commandtext.ScopeNoticeComment(commandtext.ScopeNotice{Source: cmtComment, Repo: cmtRepo, Branch: "feature/x", Paths: []string{"pkg/a_test.go", "conftest.py"}, PathsTotal: 30,
			Declaration: []byte(suggestDeclaration), Mixed: []string{"package.json"}, Cut: true})
	}
	for _, tc := range []struct {
		name  string
		extra map[string]any
		text  func() string
	}{
		{"suggest refused for a fork", map[string]any{"disposition": "suggest-refused", "reason": "fork"}, ack(commandtext.Ack{Outcome: commandtext.SuggestRefused, Refusal: commandtext.Fork})},
		{"suggest refused for a pull request not open", map[string]any{"disposition": "suggest-refused", "reason": "not-open"}, ack(commandtext.Ack{Outcome: commandtext.SuggestRefused, Refusal: commandtext.NotOpen})},
		{"suggest refused for an invalid source", map[string]any{"disposition": "suggest-refused", "reason": "invalid-source"}, ack(commandtext.Ack{Outcome: commandtext.SuggestRefused, Refusal: commandtext.InvalidSource})},
		{"suggest refused for an invalid file of the head", map[string]any{"disposition": "suggest-refused", "reason": "invalid-source", "base": "pull"},
			ack(commandtext.Ack{Outcome: commandtext.SuggestRefused, Refusal: commandtext.InvalidSource, Base: commandtext.BasePull})},
		{"suggest refused for an invalid file of the default branch", map[string]any{"disposition": "suggest-refused", "reason": "invalid-source", "base": "default-branch"},
			ack(commandtext.Ack{Outcome: commandtext.SuggestRefused, Refusal: commandtext.InvalidSource, Base: commandtext.BaseDefaultBranch})},
		{"suggested with runner names alone", map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature/x", "base": "pull", "declaration": suggestDeclaration, "addRunner": []string{"conftest.py"}}, func() string {
			return commandtext.ScopeNoticeComment(commandtext.ScopeNotice{Source: cmtComment, Repo: cmtRepo, Branch: "feature/x", Declaration: []byte(suggestDeclaration), Base: commandtext.BasePull, AddRunner: []string{"conftest.py"}})
		}},
		{"suggested with nothing uncovered", map[string]any{"disposition": "suggested", "reason": "nothing-uncovered", "base": "contract"}, ack(commandtext.Ack{Outcome: commandtext.NothingUncovered, Base: commandtext.BaseContract})},
		{"suggested before the first contract", map[string]any{"disposition": "suggested", "reason": "declaration-missing", "branch": "feature/x", "suggestion": []string{"tests/**"}, "runner": []string{"conftest.py"},
			"mixed": []string{"package.json"}, "cut": true}, func() string {
			return commandtext.SuggestDeclaration(commandtext.DeclarationSuggestion{Source: cmtComment, Repo: cmtRepo, Branch: "feature/x", Patterns: []string{"tests/**"},
				Runner: []string{"conftest.py"}, Mixed: []string{"package.json"}, Cut: true, Suggest: true})
		}},
		{"suggested after the first contract", map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature/x", "paths": []string{"pkg/a_test.go", "conftest.py"},
			"pathsTotal": 30, "declaration": suggestDeclaration, "mixed": []string{"package.json"}, "cut": true}, notice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCmtWorld(t)
			key := fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment)
			if err := w.pub.publish(t.Context(), key, cmtAckPayload(tc.extra)); err != nil {
				t.Fatalf("publish: %v", err)
			}
			w.wantPost(key, tc.text())
		})
	}
}

func TestCommentPublisherRefusesASuggestPayloadThatDoesNotFit(t *testing.T) {
	notice := func(key string, value any) map[string]any {
		payload := map[string]any{"disposition": "suggested", "reason": "scope-notice", "branch": "feature", "paths": []string{"a_test.go"}, "pathsTotal": 1, "declaration": suggestDeclaration}
		if value == nil {
			delete(payload, key)
		} else {
			payload[key] = value
		}
		return payload
	}
	twentyOne := strings.Split(strings.Repeat("a_test.go,", 21), ",")[:21]
	for name, extra := range map[string]map[string]any{
		"a suggest refused for a declaration too large (v0.1.1)": {"disposition": "suggest-refused", "reason": "declaration-too-large"},
		"a declared flag on a suggest (v0.1.1)":                  {"disposition": "suggested", "reason": "declaration-missing", "branch": "feature/x", "suggestion": []string{"tests/**"}, "declared": true},
		"nothing uncovered without a base (before M1.9-E5)":      {"disposition": "suggested", "reason": "nothing-uncovered"},
		"a declared flag on a propose refusal":                   {"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature", "declared": true},
		"a declared flag on a scope notice":                      notice("declared", true),
		"a declared flag on nothing uncovered":                   {"disposition": "suggested", "reason": "nothing-uncovered", "declared": true},
		"a suggest refused for a canonical":                      {"disposition": "suggest-refused", "reason": "canonical-present"},
		"a suggest refused for a base":                           {"disposition": "suggest-refused", "reason": "base-not-target"},
		"a suggest refused without a reason":                     {"disposition": "suggest-refused"},
		"a reference on a suggest":                               {"disposition": "suggested", "reason": "nothing-uncovered", "reference": "P1-R1"},
		"a suggested for a fork":                                 {"disposition": "suggested", "reason": "fork"},
		"a suggested without a reason":                           {"disposition": "suggested"},
		"a branch on nothing uncovered":                          {"disposition": "suggested", "reason": "nothing-uncovered", "branch": "feature"},
		"paths on nothing uncovered":                             {"disposition": "suggested", "reason": "nothing-uncovered", "paths": []string{"a_test.go"}, "pathsTotal": 1},
		"a declaration on a missing one":                         {"disposition": "suggested", "reason": "declaration-missing", "branch": "feature", "declaration": suggestDeclaration},
		"a suggestion on a scope notice":                         notice("suggestion", []string{"tests/**"}),
		"a notice without a branch":                              notice("branch", nil),
		"a notice with a branch of two dots":                     notice("branch", "a..b"),
		"a notice without paths":                                 notice("paths", nil),
		"a notice without a declaration":                         notice("declaration", nil),
		"a notice with a total below the list":                   notice("pathsTotal", 0),
		"a notice with more than 20 paths":                       notice("paths", twentyOne),
		"a notice with a path over the bound":                    notice("paths", []string{strings.Repeat("p", 151)}),
		"a notice with a mixed path too long":                    notice("mixed", []string{strings.Repeat("m", 151)}),
		"a notice with a declaration too long":                   notice("declaration", "version: 1\n# "+strings.Repeat("x", 9000)+"\n"),
		"a notice on another disposition":                        {"disposition": "propose-refused", "reason": "declaration-missing", "branch": "feature", "paths": []string{"a_test.go"}, "pathsTotal": 1},
		"paths on a processed acknowledgment":                    {"project": "github-repo:1001", "suite": "default", "operation": "op-1", "paths": []string{"a_test.go"}, "pathsTotal": 1},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCmtWorld(t)
			err := w.pub.publish(t.Context(), fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment), cmtAckPayload(extra))
			if err == nil || !strings.Contains(err.Error(), "does not match its key") && !strings.Contains(err.Error(), "does not decode") {
				t.Fatalf("publish = %v; want a validation or decoding error, not a render failure", err)
			}
			if len(w.writer.posts()) != 0 {
				t.Fatal("a comment was posted")
			}
		})
	}
}

func TestScopeNoticeAckBoundsItsRepositoryText(t *testing.T) {
	long := strings.Repeat("x", 300)
	var paths []string
	for range 25 {
		paths = append(paths, long)
	}
	message, err := newScopeNoticeAck(cmtTarget(), cmtComment, cmtOwner, "feature", "contract", "", paths, []byte(suggestDeclaration), inventory.Suggestion{Cut: true}, []string{long}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload ackPayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Paths) != 20 || payload.PathsTotal != 25 || len(payload.Mixed) != 1 || !payload.Cut {
		t.Fatalf("paths %d of %d, mixed %d, cut %v; want 20 of 25, 1 and cut", len(payload.Paths), payload.PathsTotal, len(payload.Mixed), payload.Cut)
	}
	for _, text := range append(payload.Paths, payload.Mixed...) {
		if len(text) > maxSummaryText {
			t.Fatalf("a text of %d bytes; want it cut to %d", len(text), maxSummaryText)
		}
	}
	if message.Key != fmt.Sprintf("ack:%d:%d", cmtRepoID, cmtComment) {
		t.Fatalf("key %q; the reply to a command is its acknowledgment, never a scope notice key", message.Key)
	}
}

func TestDeclarationMissingAckTakesTheDispositionOfItsCommand(t *testing.T) {
	for _, d := range []string{"propose-refused", "suggested"} {
		message, err := newDeclarationMissingAck(cmtTarget(), cmtComment, cmtOwner, "feature", inventory.Suggestion{Include: []string{"tests/**"}}, commands.Disposition(d))
		if err != nil {
			t.Fatal(err)
		}
		var payload ackPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Disposition != d || payload.Reason != "declaration-missing" {
			t.Fatalf("payload %+v; want %s for a missing declaration", payload, d)
		}
	}
}
