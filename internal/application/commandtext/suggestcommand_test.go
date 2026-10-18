package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The reply of /suiteward suggest before the first contract: the suggestion of a propose without a declaration, in the words of a suggest.

func suggestReply(declared bool, include []string) commandtext.DeclarationSuggestion {
	a := missingWith(include, nil, nil, false)
	a.Suggest, a.Declared = true, declared
	return a
}

func TestSuggestReplyDoesNotSpeakOfAPropose(t *testing.T) {
	for _, declared := range []bool{false, true} {
		body := suggestionText(t, suggestReply(declared, []string{"tests/**", "*_test.go"}))
		for _, w := range []string{"`/suiteward suggest`", "SuiteWard suggests this declaration for this pull request", "```\nversion: 1\ninclude:\n  - \"tests/**\"\n  - \"*_test.go\"\n```",
			"[open GitHub's editor with this file filled in](https://github.com/octo-org/widgets/new/feature/add-tests?filename=.suiteward.yml&value=", "the commit is yours",
			"In reply to [your command](#issuecomment-2345678901)."} {
			if !strings.Contains(body, w) {
				t.Errorf("declared %v: body lacks %q:\n%s", declared, w, body)
			}
		}
		for _, w := range []string{"`/suiteward propose` was not processed", "proposed nothing", "comment `/suiteward propose` again"} {
			if strings.Contains(body, w) {
				t.Errorf("declared %v: body says %q:\n%s", declared, w, body)
			}
		}
	}
}

func TestSuggestReplySaysWhetherADeclarationAlreadyExists(t *testing.T) {
	missing := suggestionText(t, suggestReply(false, []string{"tests/**"}))
	for _, w := range []string{"found no `.suiteward.yml` on the default branch or in the head of this pull request", "Nothing changed.", "comment `/suiteward propose`"} {
		if !strings.Contains(missing, w) {
			t.Errorf("reply without a declaration lacks %q:\n%s", w, missing)
		}
	}
	declared := suggestionText(t, suggestReply(true, []string{"tests/**"}))
	for _, w := range []string{"already has a `.suiteward.yml`", "Compare it with this suggestion", "Nothing changes until you commit a declaration and approve the change"} {
		if !strings.Contains(declared, w) {
			t.Errorf("reply with a declaration lacks %q:\n%s", w, declared)
		}
	}
	for _, w := range []string{"found no `.suiteward.yml`", "comment `/suiteward propose`"} {
		if strings.Contains(declared, w) {
			t.Errorf("reply with a declaration says %q:\n%s", w, declared)
		}
	}
}

// The reply of a propose is pinned: only a suggest takes the other words.
func TestDeclaredWithoutSuggestIsRefused(t *testing.T) {
	a := missingWith([]string{"tests/**"}, nil, nil, false)
	a.Declared = true
	mustPanic(t, func() { commandtext.SuggestDeclaration(a) })
}
