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
			[]string{"symbolic link", "```\nspec_link\n```", "remove the link or point it to a regular file, and push"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, func() string { return commandtext.InvalidSourceComment(tc.problem) })

			for _, want := range append(tc.says, "Any canonical contract already in force stays in force", "the check stays `failure` until a push fixes it") {
				if !strings.Contains(body, want) {
					t.Errorf("the comment lacks %q:\n%s", want, body)
				}
			}
			if strings.Contains(strings.ToLower(body), "narrow") || strings.Contains(strings.ToLower(body), "submodule") || !strings.HasSuffix(body, "\n") || strings.Contains(body, "<!--") {
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
		"no problem":                               {},
		"an unknown problem":                       {Problem: commandtext.LinkProblem + 1},
		"a link without a path":                    {Problem: commandtext.LinkProblem},
		"a declaration with path":                  {Problem: commandtext.DeclarationProblem, Path: ".suiteward.yml"},
		"a declaration problem with a declaration": {Problem: commandtext.DeclarationProblem, Declaration: []byte("version: 1\n")},
		"a declaration over the bound":             {Problem: commandtext.LinkProblem, Path: "l", Declaration: make([]byte, commandtext.MaxDeclaration+1)},
		"a declaration that is not UTF-8":          {Problem: commandtext.LinkProblem, Path: "l", Declaration: []byte{0xff}},
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

// D-SCOPE, an accepted link: the comment offers to accept the link with an exact anchored exclude, warns that nothing reached through
// the link is protected, and never suggests narrowing the include patterns.
func TestInvalidSourceCommentOffersToAcceptTheLink(t *testing.T) {
	const declaration = "version: 1\ninclude:\n  - '**/test_*.py'\nexclude:\n  - /spec_link\n"
	tests := []struct {
		name        string
		declaration []byte
		block       string
	}{
		{"with the head's declaration", []byte(declaration), "```\n" + declaration + "```"},
		{"without a declaration", nil, "```\n/spec_link\n```"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, func() string {
				return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link", Declaration: tc.declaration})
			})

			for _, want := range []string{"remove the link or point it to a regular file, and push", "accept the link", "`exclude`", tc.block,
				"nothing reached through the link is protected", "scope reduction", "the owner approves"} {
				if !strings.Contains(body, want) {
					t.Errorf("the comment lacks %q:\n%s", want, body)
				}
			}
			if strings.Contains(strings.ToLower(body), "narrow") {
				t.Errorf("the comment suggests narrowing the pattern:\n%s", body)
			}
		})
	}
}

// The declaration is the head's own file, so it is fenced against backticks, and so is the one-line pattern of a path with them.
func TestInvalidSourceCommentFencesTheDeclarationAndThePatternAgainstBackticks(t *testing.T) {
	declaration := "version: 1\n# ```\nexclude:\n  - /a```b\n"
	withDeclaration := render(t, func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "a```b", Declaration: []byte(declaration)})
	})
	if !strings.Contains(withDeclaration, "````\n"+declaration+"````") {
		t.Errorf("the declaration is not inside a longer fence:\n%s", withDeclaration)
	}

	pattern := render(t, func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "a```b"})
	})
	if !strings.Contains(pattern, "````\n/a```b\n````\n") {
		t.Errorf("the pattern is not inside a longer fence:\n%s", pattern)
	}
}

// A path with a character that exclude reads as a pattern cannot be named exactly, so the comment does not offer to accept it.
func TestInvalidSourceCommentDoesNotOfferToAcceptALinkNoPatternNamesExactly(t *testing.T) {
	for _, path := range []string{"spec_*", "spec_?", "spec_[a]", `spec\link`} {
		body := render(t, func() string {
			return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: path})
		})

		if strings.Contains(body, "accept") || !strings.Contains(body, "remove the link or point it to a regular file, and push") {
			t.Errorf("the comment for %q offers to accept the link or lacks the other step:\n%s", path, body)
		}
	}
}
