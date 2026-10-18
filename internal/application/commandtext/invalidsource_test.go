package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The comment of an invalid-source check (M1.9-E9): it names the problem, never quotes file content, and says what stays in force.

func TestInvalidSourceCommentNamesTheProblemAndTheNextStep(t *testing.T) {
	tests := []struct {
		name    string
		problem commandtext.InvalidSourceNotice
		says    []string
	}{
		{"declaration", commandtext.InvalidSourceNotice{Problem: commandtext.DeclarationProblem},
			[]string{"`.suiteward.yml` of this pull request's head is not a valid declaration", "fix `.suiteward.yml`"}},
		{"link", commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link"},
			[]string{"symbolic link", "```\nspec_link\n```", "remove the link, point it to a file, or narrow the pattern that reaches it"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, func() string { return commandtext.InvalidSourceComment(tc.problem) })

			for _, want := range append(tc.says, "Any canonical contract already in force stays in force", "the check stays `failure` until a push fixes it") {
				if !strings.Contains(body, want) {
					t.Errorf("the comment lacks %q:\n%s", want, body)
				}
			}
			if strings.Contains(strings.ToLower(body), "submodule") || !strings.HasSuffix(body, "\n") || strings.Contains(body, "<!--") {
				t.Errorf("the comment mentions submodules, lacks its final newline or holds a marker:\n%s", body)
			}
		})
	}
}

// The path comes from the repository: it is shown only in a fence that backticks cannot close, with control characters escaped.
func TestInvalidSourceCommentShowsThePathInAnEscapedFence(t *testing.T) {
	body := render(t, func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "a```b\n--> <script>\u202e"})
	})

	if !strings.Contains(body, "````\n\"a```b\\n--> <script>\\u202e\"\n````\n") {
		t.Errorf("the path is not shown quoted inside a longer fence:\n%s", body)
	}
}

func TestInvalidSourceCommentRefusesWhatItCannotRender(t *testing.T) {
	tests := map[string]commandtext.InvalidSourceNotice{
		"no problem":              {},
		"an unknown problem":      {Problem: commandtext.LinkProblem + 1},
		"a link without a path":   {Problem: commandtext.LinkProblem},
		"a declaration with path": {Problem: commandtext.DeclarationProblem, Path: ".suiteward.yml"},
	}
	for name, problem := range tests {
		t.Run(name, func(t *testing.T) {
			if err := commandtext.ValidateInvalidSource(problem); err == nil {
				t.Fatal("ValidateInvalidSource accepted it")
			}
			defer func() {
				if recover() == nil {
					t.Error("InvalidSourceComment did not panic")
				}
			}()
			commandtext.InvalidSourceComment(problem)
		})
	}
}

// The check says the same: links and the declaration, no submodule (a submodule is outside the scope since D-SCOPE).
func TestTheInvalidSourceCheckSummaryNamesLinksAndTheDeclarationButNoSubmodule(t *testing.T) {
	title, summary := commandtext.CheckSummary(check(commandtext.CheckFailure, "invalid-source"))

	if title != "Invalid contract source" {
		t.Errorf("title %q, want %q", title, "Invalid contract source")
	}
	for _, want := range []string{"`.suiteward.yml`", "symlink", "The canonical contract stays in force.", "and push"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(strings.ToLower(summary), "submodule") {
		t.Errorf("the summary mentions submodules:\n%s", summary)
	}
}
