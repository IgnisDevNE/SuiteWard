package commandtext_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// A declaration that cannot be shown (hidden characters, or over the size a notice holds) is never shown, and the comment is never lost: every
// comment that quotes a declaration has a form with the missing patterns alone, as copyable lines, and the link to GitHub's editor
// (decision 7 of M1.10).

func patternsOnly() commandtext.ScopeNotice {
	n := notice()
	n.Declaration = nil
	n.AddInclude, n.AddRunner = []string{"*_test.go", "**/testdata/**"}, []string{"conftest.py"}
	return n
}

func TestScopeNoticeWithoutADeclarationOffersThePatternsAlone(t *testing.T) {
	n := patternsOnly()
	body := noticeText(t, n)
	for _, w := range []string{
		"```\ninternal/x/x_test.go\ne2e/testdata/a.json\n```", "Patterns missing from the `include` list", "```\n*_test.go\n**/testdata/**\n```",
		"Patterns missing from the `runner` list", "```\nconftest.py\n```", "creates a protected change",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"version: 1", "this declaration", "paste the declaration"} {
		if strings.Contains(body, w) {
			t.Errorf("body names a declaration it does not show, %q:\n%s", w, body)
		}
	}
	if raw, u := editorLink(t, body); u.Host != "github.com" || u.Path != "/octo-org/widgets/edit/feature/add-tests/.suiteward.yml" || u.RawQuery != "" {
		t.Errorf("the link is %q, want the editor of .suiteward.yml on the branch", raw)
	}
	checkBody(t, body)
	n.Cut, n.Mixed = true, []string{"package.json"}
	if body := noticeText(t, n); !strings.Contains(body, "cut at the limit of 20 patterns") || !strings.Contains(body, "package.json") {
		t.Errorf("the cut warning or the mixed files are lost:\n%s", body)
	}
}

func TestScopeReplyWithoutADeclarationOffersThePatternsAlone(t *testing.T) {
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseDefaultBranch, commandtext.BaseContract} {
		n := reply(base)
		n.Declaration = nil
		if base == commandtext.BaseDefaultBranch {
			n.DefaultBranch = "main"
		}
		body := noticeText(t, n)
		for _, w := range []string{"`/suiteward suggest`", "```\n*_test.go\n```", "```\nconftest.py\n```", "In reply to [your command](#issuecomment-2345678901)."} {
			if !strings.Contains(body, w) {
				t.Errorf("base %d: body lacks %q:\n%s", base, w, body)
			}
		}
		for _, w := range []string{"version: 1", "the declaration", "this declaration"} {
			if strings.Contains(body, w) {
				t.Errorf("base %d: body names a declaration it does not show, %q:\n%s", base, w, body)
			}
		}
		if base != commandtext.BaseContract && !strings.Contains(body, "/suiteward propose") {
			t.Errorf("base %d: the next step is lost:\n%s", base, body)
		}
		checkBody(t, body)
	}
}

func TestScopeNoticeWithoutADeclarationNeedsPatternsAndStaysInTheLimit(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.ScopeNotice){
		"no patterns": func(n *commandtext.ScopeNotice) { n.AddInclude, n.AddRunner = nil, nil },
		"21 patterns": func(n *commandtext.ScopeNotice) {
			n.AddInclude, n.AddRunner = slices.Repeat([]string{"a"}, 11), slices.Repeat([]string{"b"}, 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			n := patternsOnly()
			mutate(&n)
			mustPanic(t, func() { commandtext.ScopeNoticeComment(n) })
			if commandtext.ValidateScopeNotice(n) == nil {
				t.Error("ValidateScopeNotice accepts it")
			}
		})
	}
	n := patternsOnly()
	n.AddInclude, n.AddRunner = slices.Repeat([]string{"a"}, 10), slices.Repeat([]string{"b"}, 10)
	if err := commandtext.ValidateScopeNotice(n); err != nil {
		t.Errorf("20 patterns are refused: %v", err)
	}
}

// protectPatterns are all the runner patterns of the families that the checks job knows: 25, above the 20 of a listing.
var protectPatterns = []string{"pytest.toml", ".pytest.toml", "pytest.ini", ".pytest.ini", "conftest.py", "jest.config.*", "vitest.config.*", "vitest.workspace.*",
	".mocharc.*", "karma.conf.*", "playwright.config.*", "cypress.config.*", ".coveragerc", ".nycrc*", ".c8rc*", "phpunit.xml", "phpunit.xml.dist",
	"phpunit.dist.xml", ".rspec", "spec_helper.rb", "rails_helper.rb", "test_helper.*", "dart_test.yaml", "codecov.yml", ".codecov.yml"}

func protectOnly() commandtext.RunnerUncovered {
	n := runnerNotice()
	n.Protect, n.ProtectPatterns = nil, []string{"jest.config.*"}
	return n
}

func TestRunnerUncoveredCommentWithoutAProtectDeclarationOffersTheRunnerPatternsAlone(t *testing.T) {
	n := protectOnly()
	body := runnerText(t, n)
	for _, w := range []string{"Protect the files", "`runner` list", "```\njest.config.*\n```", "Ignore the files", "```\n" + string(n.Ignore) + "```", "Commit suggestion"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "runner:\n  - jest.config.*") {
		t.Errorf("body shows a protect declaration:\n%s", body)
	}
	if raw, u := editorLink(t, body); u.Path != "/octo-org/widgets/edit/feature/add-tests/.suiteward.yml" {
		t.Errorf("the link is %q, want the editor of .suiteward.yml on the branch", raw)
	}
	checkBody(t, body)

	n.Ignore = nil // no declaration at all: the patterns alone, and the reason for the missing choice covers a declaration that cannot be shown
	body = runnerText(t, n)
	if !strings.Contains(body, "```\njest.config.*\n```") || strings.Contains(body, "the declaration") || strings.Contains(body, "exclude:") || strings.Contains(body, "Ignore the files") {
		t.Errorf("the comment with no declaration at all is wrong:\n%s", body)
	}
	if !strings.Contains(body, "cannot offer to ignore") || !strings.Contains(body, "cannot be shown") {
		t.Errorf("the reason for the missing ignore choice does not cover a declaration that cannot be shown:\n%s", body)
	}
}

func TestRunnerUncoveredCommentWithoutAProtectDeclarationNeedsRunnerPatterns(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.RunnerUncovered){
		"no patterns": func(n *commandtext.RunnerUncovered) { n.ProtectPatterns = nil },
		"31 patterns": func(n *commandtext.RunnerUncovered) {
			n.ProtectPatterns = append(slices.Repeat([]string{"x"}, 6), protectPatterns...)
		},
		"patterns and a declaration": func(n *commandtext.RunnerUncovered) { n.Protect = []byte("version: 1\n") },
		"an empty pattern":           func(n *commandtext.RunnerUncovered) { n.ProtectPatterns = []string{""} },
	} {
		t.Run(name, func(t *testing.T) {
			n := protectOnly()
			mutate(&n)
			mustPanic(t, func() { commandtext.RunnerUncoveredComment(n) })
			if commandtext.ValidateRunnerUncovered(n) == nil {
				t.Error("ValidateRunnerUncovered accepts it")
			}
		})
	}
	n := protectOnly()
	n.ProtectPatterns = protectPatterns
	if body := runnerText(t, n); !strings.Contains(body, ".codecov.yml") {
		t.Errorf("a notice of all 25 runner patterns lacks the last:\n%s", body)
	}
}

// A review suggestion extends the list as scope.AddPatterns would: the quoting that every item shares, and plain for a list of mixed quoting.
// The payload carries that style, so a mixed list no longer follows the quoting of its anchor item.
func TestReviewSuggestionsFollowTheQuotingStyleOfTheListTheyExtend(t *testing.T) {
	files := map[string]string{"include": "version: 1\ninclude:\n  - \"a/**\"\n  - 'b/**'\n", "runner": "version: 1\nrunner:\n  - conftest.py\n  - 'x'\n"}
	patterns := []string{"c/d", "e/f"}
	for list, c := range map[string]struct {
		anchor string
		add    func([]byte) ([]byte, error)
		body   func(anchor string) string
	}{
		"include": {"  - 'b/**'", func(raw []byte) ([]byte, error) { return scope.AddPatterns(raw, patterns, nil) }, func(anchor string) string {
			return commandtext.ScopeSuggestionBody(anchor, "  ", patterns, commandtext.QuotingPlain)
		}},
		"runner": {"  - 'x'", func(raw []byte) ([]byte, error) { return scope.AddPatterns(raw, nil, patterns) }, func(anchor string) string {
			return commandtext.RunnerChoiceBody(commandtext.ProtectChoice, anchor, "  ", patterns, "", commandtext.QuotingPlain)
		}},
	} {
		t.Run(list, func(t *testing.T) {
			want := addedLines(t, files[list], c.add, c.anchor)
			if want != "  - c/d\n  - e/f" {
				t.Fatalf("the library writes %q for the mixed list, not plain items", want)
			}
			block := suggestionBlock.FindStringSubmatch(render(t, func() string { return c.body(c.anchor) }))
			if len(block) < 2 || block[1] != c.anchor+"\n"+want {
				t.Errorf("the block is %q, want the anchor and\n%s", block, want)
			}
		})
	}
}

func TestAQuotingStyleIsCarriedAsGiven(t *testing.T) {
	for style, want := range map[commandtext.Quoting]string{commandtext.QuotingDouble: "\n  - \"c/d\"", commandtext.QuotingSingle: "\n  - 'c/d'", commandtext.QuotingPlain: "\n  - c/d"} {
		anchor := "  - \"a\"" // the anchor's own quoting is not the style asked for, but for the double one
		if style == commandtext.QuotingDouble {
			anchor = "  - 'a'"
		}
		if got := render(t, func() string { return commandtext.ScopeSuggestionBody(anchor, "  ", []string{"c/d"}, style) }); !strings.Contains(got, want) {
			t.Errorf("style %q: body lacks %q:\n%s", style, want, got)
		}
	}
	for _, bad := range []commandtext.Quoting{"", "bold", "Plain"} {
		mustPanic(t, func() { commandtext.ScopeSuggestionBody("  - a", "  ", []string{"x"}, bad) })
		if commandtext.ValidateScopeSuggestion("  - a", "  ", []string{"x"}, bad) == nil {
			t.Errorf("ValidateScopeSuggestion accepts the style %q", bad)
		}
		if commandtext.ValidateRunnerChoice(commandtext.ProtectChoice, "  - a", "  ", []string{"x"}, "", bad) == nil {
			t.Errorf("ValidateRunnerChoice accepts the style %q", bad)
		}
	}
	// A list that a choice creates has no items to follow, so it has no style.
	if commandtext.ValidateRunnerChoice(commandtext.ProtectChoice, "  - a", "  ", []string{"x"}, "runner", commandtext.QuotingPlain) == nil {
		t.Error("ValidateRunnerChoice accepts a style for a created list")
	}
	if err := commandtext.ValidateRunnerChoice(commandtext.ProtectChoice, "  - a", "  ", []string{"x"}, "runner", ""); err != nil {
		t.Errorf("ValidateRunnerChoice refuses a created list with no style: %v", err)
	}
}
