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
			[]string{"`.suiteward.yml` file of this pull request is not valid", "fix `.suiteward.yml`"}},
		{"link", commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link"},
			[]string{"symbolic link", "```\nspec_link\n```", "remove the link or point it to a regular file, and push"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, func() string { return commandtext.InvalidSourceComment(tc.problem) })

			for _, want := range append(tc.says, "The approved contract stays as it is", "check stays `failure` until a push fixes this") {
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

	if title != "Contract files are invalid" {
		t.Errorf("title %q, want %q", title, "Contract files are invalid")
	}
	for _, want := range []string{"`.suiteward.yml`", "symlink", "The approved contract stays in force.", "remove the symlink, or point it to a regular file, and push", "the exact path of the symlink, from the top of the repository, to `exclude`", "The owner must approve that, and nothing reached through the symlink stays protected."} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(strings.ToLower(summary), "submodule") || strings.Contains(strings.ToLower(summary), "narrow") {
		t.Errorf("the summary mentions submodules or narrowing:\n%s", summary)
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
				"nothing reached through the link is protected", "That removes protection", "the owner approves"} {
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

// A path that the caller cut is not the link's path, so the one-line pattern would name another path: the comment offers to accept the
// link only with a declaration, which the caller built from the whole path.
func TestInvalidSourceCommentOffersTheCutLinkOnlyWithItsDeclaration(t *testing.T) {
	cut := strings.Repeat("l", 140) + "…"
	without := render(t, func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: cut, PathCut: true})
	})
	if strings.Contains(without, "accept") || strings.Contains(without, "/"+cut) || !strings.Contains(without, "remove the link or point it to a regular file, and push") {
		t.Errorf("the comment for a cut path without a declaration offers to accept it or lacks the other step:\n%s", without)
	}

	const declaration = "version: 1\nexclude:\n  - /whole-path\n"
	with := render(t, func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: cut, PathCut: true, Declaration: []byte(declaration)})
	})
	if !strings.Contains(with, "accept the link") || !strings.Contains(with, "```\n"+declaration+"```") || strings.Contains(with, "/"+cut) {
		t.Errorf("the comment for a cut path with a declaration lacks the offer or shows the cut pattern:\n%s", with)
	}

	if err := commandtext.ValidateInvalidSource(commandtext.InvalidSourceNotice{Problem: commandtext.DeclarationProblem, PathCut: true}); err == nil {
		t.Error("ValidateInvalidSource accepted a cut path on a declaration problem")
	}
}

// D-SCOPE-DISCOVERY, every suggested change links to GitHub's editor: the declaration problem links where the owner fixes the file, the
// link problem links with the choice to accept the link, and never with the step to remove it, which the web editor cannot do.
const (
	editorBranch = "feat/a#b%c/ünï"
	editorURL    = "https://github.com/octo-org/widgets/edit/feat/a%23b%25c/%C3%BCn%C3%AF/.suiteward.yml"
)

func TestInvalidSourceCommentLinksToTheEditorWithTheEscapedBranch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		notice commandtext.InvalidSourceNotice
		after  string // the editor link comes after this text of the comment
	}{
		{"declaration", commandtext.InvalidSourceNotice{Problem: commandtext.DeclarationProblem}, "fix `.suiteward.yml`"},
		{"link with a declaration", commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link", Declaration: []byte("version: 1\nexclude:\n  - /spec_link\n")}, "accept the link"},
		{"link with the one-line pattern", commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link"}, "accept the link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.notice.Repo, tc.notice.Branch = "octo-org/widgets", editorBranch
			body := render(t, func() string { return commandtext.InvalidSourceComment(tc.notice) })

			raw, _ := editorLink(t, body)
			if raw != editorURL {
				t.Errorf("the link is %q, want %q", raw, editorURL)
			}
			if !strings.Contains(body, "[open GitHub's editor for it]("+editorURL+")") || strings.Index(body, editorURL) < strings.Index(body, tc.after) {
				t.Errorf("the link is not in the sentence style of the other comments or comes before %q:\n%s", tc.after, body)
			}
		})
	}
}

// The step to remove the link has no link, and a link that cannot be accepted has no step that the editor could do.
func TestInvalidSourceCommentLinksNothingForRemovingTheLink(t *testing.T) {
	for name, notice := range map[string]commandtext.InvalidSourceNotice{
		"a pattern that names more than the link": {Problem: commandtext.LinkProblem, Path: "spec_*"},
		"a cut path without a declaration":        {Problem: commandtext.LinkProblem, Path: strings.Repeat("l", 140) + "…", PathCut: true},
	} {
		t.Run(name, func(t *testing.T) {
			notice.Repo, notice.Branch = "octo-org/widgets", editorBranch
			body := render(t, func() string { return commandtext.InvalidSourceComment(notice) })

			if strings.Contains(body, "github.com") || !strings.Contains(body, "remove the link or point it to a regular file, and push") {
				t.Errorf("the comment has a link or lacks the step to remove the link:\n%s", body)
			}
		})
	}
	body := render(t, func() string {
		return commandtext.InvalidSourceComment(commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link", Repo: "octo-org/widgets", Branch: editorBranch})
	})
	if before, _, _ := strings.Cut(body, "Or accept the link"); strings.Contains(before, "github.com") {
		t.Errorf("the step to remove the link has a link:\n%s", body)
	}
}

// A notice queued without the repository and the branch renders without a link.
func TestInvalidSourceCommentHasNoLinkWithoutTheRepositoryAndTheBranch(t *testing.T) {
	for _, notice := range []commandtext.InvalidSourceNotice{
		{Problem: commandtext.DeclarationProblem},
		{Problem: commandtext.LinkProblem, Path: "spec_link"},
	} {
		body := render(t, func() string { return commandtext.InvalidSourceComment(notice) })

		if strings.Contains(body, "github.com") || strings.Contains(body, "editor") {
			t.Errorf("the comment has a link though it has no repository and branch:\n%s", body)
		}
	}
}

func TestInvalidSourceNoticeNeedsBothTheRepositoryAndTheBranchOrNeither(t *testing.T) {
	for name, notice := range map[string]commandtext.InvalidSourceNotice{
		"a repository without a branch": {Problem: commandtext.DeclarationProblem, Repo: "octo-org/widgets"},
		"a branch without a repository": {Problem: commandtext.DeclarationProblem, Branch: "feature"},
		"a repository that is not one":  {Problem: commandtext.DeclarationProblem, Repo: "SECRET", Branch: "feature"},
		"a branch that is not a ref":    {Problem: commandtext.DeclarationProblem, Repo: "octo-org/widgets", Branch: "a b"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := commandtext.ValidateInvalidSource(notice); err == nil {
				t.Fatal("ValidateInvalidSource accepted it")
			}
			defer func() {
				if recover() == nil {
					t.Error("InvalidSourceComment did not panic")
				}
			}()
			commandtext.InvalidSourceComment(notice)
		})
	}
}

// A declaration of exactly the limit is accepted and, in the worst fence (one run of backticks), the comment still fits; one byte more is refused.
func TestInvalidSourceCommentAcceptsADeclarationOfExactlyTheLimit(t *testing.T) {
	notice := commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link", Declaration: []byte(strings.Repeat("`", commandtext.MaxDeclaration))}
	if err := commandtext.ValidateInvalidSource(notice); err != nil {
		t.Fatalf("a declaration of %d bytes is refused: %v", commandtext.MaxDeclaration, err)
	}
	if body := render(t, func() string { return commandtext.InvalidSourceComment(notice) }); body == "" || len(body) >= maxComment/2 {
		t.Errorf("body has %d bytes, want 0 < n < %d", len(body), maxComment/2)
	}
	notice.Declaration = append(notice.Declaration, '`')
	if commandtext.ValidateInvalidSource(notice) == nil {
		t.Errorf("a declaration of %d bytes is accepted", len(notice.Declaration))
	}
}
