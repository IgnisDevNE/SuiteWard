package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The reply of /suiteward suggest before the first contract: the suggestion of a propose without a declaration, in the words of a suggest.

func suggestReply(include []string) commandtext.DeclarationSuggestion {
	a := missingWith(include, nil, nil, false)
	a.Suggest = true
	return a
}

func TestSuggestReplyDoesNotSpeakOfAPropose(t *testing.T) {
	body := suggestionText(t, suggestReply([]string{"tests/**", "*_test.go"}))
	for _, w := range []string{"`/suiteward suggest`", "SuiteWard suggests this declaration for this pull request", "```\nversion: 1\ninclude:\n  - \"tests/**\"\n  - \"*_test.go\"\n```",
		"[open GitHub's editor with this file filled in](https://github.com/octo-org/widgets/new/feature/add-tests?filename=.suiteward.yml&value=", "the commit is yours",
		"In reply to [your command](#issuecomment-2345678901)."} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"`/suiteward propose` was not processed", "proposed nothing", "comment `/suiteward propose` again"} {
		if strings.Contains(body, w) {
			t.Errorf("body says %q:\n%s", w, body)
		}
	}
}

func TestSuggestReplySaysThatNoDeclarationExists(t *testing.T) {
	body := suggestionText(t, suggestReply([]string{"tests/**"}))
	for _, w := range []string{"found no `.suiteward.yml` on the default branch or in the head of this pull request", "Nothing changed.", "comment `/suiteward propose`"} {
		if !strings.Contains(body, w) {
			t.Errorf("reply lacks %q:\n%s", w, body)
		}
	}
}
