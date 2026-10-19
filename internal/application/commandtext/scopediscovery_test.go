package commandtext_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// Scope discovery (M1.9-T2): the texts of the suggestion, the scope notice, the review suggestion and the reduction.

func patternNames(patterns []scope.Pattern) []string {
	var names []string
	for _, p := range patterns {
		names = append(names, p.String())
	}
	return names
}

func missingWith(include, runner, mixed []string, cut bool) commandtext.DeclarationSuggestion {
	a := declarationMissing(include...)
	a.Runner, a.Mixed, a.Cut = runner, mixed, cut
	return a
}

func withSubmodules(a commandtext.DeclarationSuggestion, submodules []string) commandtext.DeclarationSuggestion {
	a.Submodules = submodules
	return a
}

func suggestionText(t *testing.T, a commandtext.DeclarationSuggestion) string {
	t.Helper()
	return render(t, func() string { return commandtext.SuggestDeclaration(a) })
}

// Callers that know nothing of scope discovery see the output of M1.7b, byte for byte.
func TestSuggestDeclarationWithoutDiscoveryFieldsIsUnchanged(t *testing.T) {
	const want = "`/suiteward propose` was not processed: SuiteWard found no `.suiteward.yml` on the default branch or in the head of this pull request, so it cannot tell which files to protect and proposed nothing. Nothing changed.\n\n" +
		"SuiteWard suggests this declaration, built from the test-like paths of this repository. Review it and change it as needed:\n\n```\nversion: 1\ninclude:\n  - \"tests/**\"\n  - \"*_test.go\"\n```\n\n" +
		"To add it on this pull request's branch, [open GitHub's editor with this file filled in](https://github.com/octo-org/widgets/new/feature/add-tests?filename=.suiteward.yml&value=version%3A%201%0Ainclude%3A%0A%20%20-%20%22tests%2F%2A%2A%22%0A%20%20-%20%22%2A_test.go%22%0A). SuiteWard never commits it for you: the commit is yours.\n\n" +
		"After the file is on this branch, comment `/suiteward propose` again.\n\nIn reply to [your command](#issuecomment-2345678901).\n"
	if got := suggestionText(t, missingWith([]string{"tests/**", "*_test.go"}, nil, nil, false)); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestSuggestDeclarationRendersTheRunnerListAfterTheIncludeList(t *testing.T) {
	include, runner := []string{"*_test.go", "testdata/**"}, []string{"pytest.toml", "pytest.ini", "it's/runner"}
	body := suggestionText(t, missingWith(include, runner, nil, false))
	declaration := "version: 1\ninclude:\n  - \"*_test.go\"\n  - \"testdata/**\"\nrunner:\n  - \"pytest.toml\"\n  - \"pytest.ini\"\n  - \"it's/runner\"\n"
	if !strings.Contains(body, fencedYAML(declaration)) {
		t.Errorf("the declaration is not shown in full:\n%s", body)
	}
	linked := linkedDeclaration(t, body, "feature/add-tests")
	if linked != declaration {
		t.Errorf("the link fills in %q, want %q", linked, declaration)
	}
	parsed, err := scope.ParseDeclaration([]byte(linked))
	if err != nil {
		t.Fatalf("the linked declaration is invalid: %v", err)
	}
	if got := patternNames(parsed.Include()); !slices.Equal(got, include) {
		t.Errorf("include is %q, want %q", got, include)
	}
	if got := patternNames(parsed.Runner()); !slices.Equal(got, runner) {
		t.Errorf("runner is %q, want %q", got, runner)
	}
	for _, w := range []string{"cut at the limit", "may hold test settings"} {
		if strings.Contains(body, w) {
			t.Errorf("body says %q without a cut or mixed files:\n%s", w, body)
		}
	}
}

// Runner patterns follow the same rules as include patterns: one that cannot be written safely is left out and counted.
func TestSuggestDeclarationLeavesOutRunnerPatternsThatCannotBeSuggestedSafely(t *testing.T) {
	body := suggestionText(t, missingWith([]string{"tests/**", "has\"quote"}, []string{"ok.ini", "back\\slash", strings.Repeat("a", 101), "a/```/b"}, nil, false))
	declaration := "version: 1\ninclude:\n  - \"tests/**\"\nrunner:\n  - \"ok.ini\"\n  - \"a/```/b\"\n"
	if got := linkedDeclaration(t, body, "feature/add-tests"); got != declaration {
		t.Errorf("the link fills in %q, want %q", got, declaration)
	}
	if !strings.Contains(body, "````\n"+declaration+"````") || !strings.Contains(body, "too long or contain unusual characters: 3.") {
		t.Errorf("the fence does not hold the backticks or the left-out patterns are not counted:\n%s", body)
	}
}

// With runner patterns only, the file is still a suggestion: the placeholder is for a suggestion that has nothing.
func TestSuggestDeclarationWithOnlyRunnerPatternsHasNoPlaceholder(t *testing.T) {
	body := suggestionText(t, missingWith(nil, []string{"pytest.ini"}, nil, false))
	declaration := "version: 1\nrunner:\n  - \"pytest.ini\"\n"
	if got := linkedDeclaration(t, body, "feature/add-tests"); got != declaration {
		t.Errorf("the link fills in %q, want %q", got, declaration)
	}
	if strings.Contains(body, "path/to/tests") || strings.Contains(body, "no test-like path") {
		t.Errorf("body offers the placeholder though it has runner patterns:\n%s", body)
	}
	if _, err := scope.ParseDeclaration([]byte(declaration)); err != nil {
		t.Errorf("the declaration is invalid: %v", err)
	}
}

func TestSuggestDeclarationSaysWhenTheListWasCut(t *testing.T) {
	body := suggestionText(t, missingWith([]string{"tests/**"}, nil, nil, true))
	for _, w := range []string{"cut at the limit of 20 patterns", "review"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if got, want := linkedDeclaration(t, body, "feature/add-tests"), "version: 1\ninclude:\n  - \"tests/**\"\n"; got != want {
		t.Errorf("the cut changed the declaration: %q", got)
	}
}

func TestSuggestDeclarationWarnsAboutMixedFilesInTheTextOnly(t *testing.T) {
	mixed := []string{"package.json", "web/pyproject.toml", "we\nird/Cargo.toml", "tick`/tox.ini"}
	body := suggestionText(t, missingWith([]string{"tests/**"}, []string{"pytest.ini"}, mixed, false))
	for _, w := range []string{"may hold test settings", "does not suggest", "```\npackage.json\nweb/pyproject.toml\n" + `"we\nird/Cargo.toml"` + "\ntick`/tox.ini\n```"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "we\nird") {
		t.Errorf("a control character reaches the body:\n%s", body)
	}
	declaration := "version: 1\ninclude:\n  - \"tests/**\"\nrunner:\n  - \"pytest.ini\"\n"
	if got := linkedDeclaration(t, body, "feature/add-tests"); got != declaration {
		t.Errorf("a mixed file reached the link: %q", got)
	}
	if strings.Contains(body, fencedYAML(declaration[:len(declaration)-1]+"  - \"package.json\"\n")) || strings.Count(body, "package.json") != 1 {
		t.Errorf("a mixed file is in the declaration or listed twice:\n%s", body)
	}
	if got := suggestionText(t, missingWith([]string{"tests/**"}, nil, nil, false)); strings.Contains(got, "may hold test settings") {
		t.Errorf("no mixed file, yet the warning is there:\n%s", got)
	}
}

// Submodules fall outside the scope: a reply that suggests patterns they may match says so, listed inside a fence.
func TestSuggestDeclarationWarnsThatSubmoduleContentsAreNotProtected(t *testing.T) {
	a := missingWith([]string{"tests/**"}, nil, nil, false)
	a.Submodules = []string{"ext/lib", "we\nird/mod", "tick`/mod", "arrow/-->mod"}
	body := suggestionText(t, a)
	for _, w := range []string{"submodules", "not protected", "never reads a submodule", "```\next/lib\n" + `"we\nird/mod"` + "\ntick`/mod\narrow/-->mod\n```"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "we\nird") {
		t.Errorf("a control character reaches the body:\n%s", body)
	}
	if got, want := linkedDeclaration(t, body, "feature/add-tests"), "version: 1\ninclude:\n  - \"tests/**\"\n"; got != want {
		t.Errorf("a submodule reached the link: %q", got)
	}
	if strings.Count(body, "ext/lib") != 1 {
		t.Errorf("a submodule is in the declaration or listed twice:\n%s", body)
	}
}

// A path that holds a run of backticks or a comment end stays inside a fence that it cannot close.
func TestSuggestDeclarationFencesASubmoduleThatHoldsBackticksOrACommentEnd(t *testing.T) {
	a := missingWith([]string{"tests/**"}, nil, nil, false)
	a.Submodules = []string{"a```b/-->x"}
	body := suggestionText(t, a)
	if want := "````\na```b/-->x\n````"; !strings.Contains(body, want) {
		t.Errorf("body lacks %q:\n%s", want, body)
	}
}

func TestSuggestDeclarationWithoutSubmodulesSaysNothingOfThem(t *testing.T) {
	if got := suggestionText(t, missingWith([]string{"tests/**"}, nil, []string{"package.json"}, false)); strings.Contains(got, "ubmodule") {
		t.Errorf("no submodule, yet the warning is there:\n%s", got)
	}
}

func TestSuggestDeclarationLimitsTheListsTogether(t *testing.T) {
	many := func(prefix string, n int) []string {
		var items []string
		for i := range n {
			items = append(items, fmt.Sprintf("%s%d/**", prefix, i))
		}
		return items
	}
	for name, a := range map[string]commandtext.DeclarationSuggestion{
		"include 11 and runner 10": missingWith(many("i", 11), many("r", 10), nil, false),
		"runner alone 21":          missingWith(nil, many("r", 21), nil, false),
		"21 mixed files":           missingWith([]string{"tests/**"}, nil, many("m", 21), false),
		"21 submodules":            withSubmodules(missingWith([]string{"tests/**"}, nil, nil, false), many("s", 21)),
	} {
		t.Run(name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.SuggestDeclaration(a) })
			if commandtext.ValidateSuggestion(a) == nil {
				t.Error("ValidateSuggestion accepts it")
			}
		})
	}
	for name, a := range map[string]commandtext.DeclarationSuggestion{
		"10 and 10":      missingWith(many("i", 10), many("r", 10), many("m", 20), true),
		"20 and none":    missingWith(many("i", 20), nil, nil, false),
		"none and 20":    missingWith(nil, many("r", 20), nil, false),
		"nothing at all": missingWith(nil, nil, nil, false),
		"20 submodules":  withSubmodules(missingWith([]string{"tests/**"}, nil, nil, false), many("s", 20)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := commandtext.ValidateSuggestion(a); err != nil {
				t.Errorf("ValidateSuggestion refuses it: %v", err)
			}
			if body := suggestionText(t, a); body == "" {
				t.Error("empty body")
			}
		})
	}
}

// The largest suggestion: 20 patterns of 100 bytes in any split, the longest branch and repository, and 20 mixed files
// and 20 submodules of any length. The link stays at 8000 bytes, as in M1.7b, and the body far below GitHub's limit.
func TestSuggestDeclarationWithRunnerStaysInsideALinkAndAComment(t *testing.T) {
	for name, unit := range map[string]string{"plain": "a", "3-byte": "\u20ac", "backtick": "`", "space": " ", "percent": "%"} {
		for _, runners := range []int{0, 1, 10, 19, 20} {
			t.Run(fmt.Sprintf("%s with %d runner patterns", name, runners), func(t *testing.T) {
				pattern := func(i int) string {
					p := strings.Repeat(unit, 99/len(unit))
					return p + strings.Repeat("x", 100-len(p)-1) + string(rune('a'+i))
				}
				var include, runner, mixed, submodules []string
				for i := range 20 {
					if i < runners {
						runner = append(runner, pattern(i))
					} else {
						include = append(include, pattern(i))
					}
					mixed = append(mixed, strings.Repeat("\u20ac`\n", 400)+string(rune('a'+i)))
					submodules = append(submodules, strings.Repeat("\u20ac`\n", 400)+string(rune('a'+i)))
				}
				// Without mixed files the body keeps the bound of M1.7b, a quarter of the limit; with them, half.
				for bound, withMixed := range map[int][]string{maxComment / 4: nil, maxComment / 2: mixed} {
					a := missingWith(include, runner, withMixed, true)
					if withMixed != nil {
						a.Submodules = submodules
					}
					a.Repo = "a" + strings.Repeat("b", 38) + "/" + strings.Repeat("c", 100)
					a.Branch = strings.Repeat("\u20ac", 85)
					body := suggestionText(t, a)
					raw := linkPattern.FindStringSubmatch(body)
					if raw == nil {
						t.Fatalf("no link in body:\n%s", body)
					}
					if len(raw[1]) > 8000 {
						t.Errorf("the link has %d bytes", len(raw[1]))
					}
					if len(body) > bound || !utf8.ValidString(body) {
						t.Errorf("the body has %d bytes with %d mixed files or is not UTF-8, want at most %d", len(body), len(withMixed), bound)
					}
				}
			})
		}
	}
}

func notice() commandtext.ScopeNotice {
	return commandtext.ScopeNotice{
		Repo: repository, Branch: branch,
		Paths: []string{"internal/x/x_test.go", "e2e/testdata/a.json"}, PathsTotal: 2,
		Declaration: []byte("version: 1\ninclude:\n  - \"*_test.go\"\n  - \"**/testdata/**\"\n"),
	}
}

func noticeText(t *testing.T, n commandtext.ScopeNotice) string {
	t.Helper()
	return render(t, func() string { return commandtext.ScopeNoticeComment(n) })
}

func TestScopeNoticeListsThePathsAndGivesTheDeclarationAndTheEditLink(t *testing.T) {
	n := notice()
	body := noticeText(t, n)
	for _, w := range []string{
		"```\ninternal/x/x_test.go\ne2e/testdata/a.json\n```", "not protected by the current contract",
		"```\n" + string(n.Declaration) + "```",
		"creates a protected change", "owner approves", "Nothing is protected until",
		"does not change the check", "does not run tests",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if raw, u := editorLink(t, body); u.Host != "github.com" || u.Path != "/octo-org/widgets/edit/feature/add-tests/.suiteward.yml" || u.RawQuery != "" {
		t.Errorf("the link is %q, want the editor of .suiteward.yml on the branch", raw)
	}
	for _, w := range []string{"and 0 more", "more.", "may hold test settings", "In reply to"} {
		if strings.Contains(body, w) {
			t.Errorf("body contains %q:\n%s", w, body)
		}
	}
	checkBody(t, body)
}

func TestScopeNoticeSaysWhenTheDeclarationWasCut(t *testing.T) {
	n := notice()
	n.Cut = true
	body := noticeText(t, n)
	for _, w := range []string{"cut at the limit of 20 patterns", "review"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if plain := noticeText(t, notice()); strings.Contains(plain, "cut at the limit") {
		t.Errorf("a notice that was not cut says it was:\n%s", plain)
	}
	checkBody(t, body)
}

func TestScopeNoticeEscapesTheBranchInThePath(t *testing.T) {
	n := notice()
	n.Branch = "fix/a#b%d/ünï"
	raw, _ := editorLink(t, noticeText(t, n))
	if want := "https://github.com/octo-org/widgets/edit/fix/a%23b%25d/%C3%BCn%C3%AF/.suiteward.yml"; raw != want {
		t.Errorf("the link is %q, want %q", raw, want)
	}
}

func TestScopeNoticeCountsThePathsItDoesNotList(t *testing.T) {
	n := notice()
	n.PathsTotal = 37
	if body := noticeText(t, n); !strings.Contains(body, "and 35 more") {
		t.Errorf("body does not count the unlisted paths:\n%s", body)
	}
	n.PathsTotal = 0 // a total below the list is the length of the list
	if body := noticeText(t, n); strings.Contains(body, "more") {
		t.Errorf("body counts unlisted paths:\n%s", body)
	}
}

func TestScopeNoticeShowsRepositoryTextOnlyInsideCode(t *testing.T) {
	n := notice()
	n.Paths = []string{"a\nb/x_test.go", "\u202ebidi_test.go", "\xff_test.go", "tick```/x_test.go"}
	n.Declaration = []byte("version: 1\ninclude:\n  - \"a/```/b\"\n")
	n.Mixed = []string{"package.json", "we\nird/tox.ini"}
	body := noticeText(t, n)
	for _, w := range []string{`"a\nb/x_test.go"`, `\u202e`, "tick```/x_test.go\n````", `\xff`, "````\nversion: 1\ninclude:\n  - \"a/```/b\"\n````", "may hold test settings", "```\npackage.json\n" + `"we\nird/tox.ini"` + "\n```"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "a\nb") || strings.Contains(body, "we\nird") || strings.Contains(body, "\u202e") {
		t.Errorf("a control character reaches the body:\n%s", body)
	}
}

func TestScopeNoticeWarnsThatSubmoduleContentsAreNotProtected(t *testing.T) {
	n := notice()
	n.Mixed = []string{"package.json"}
	n.Submodules = []string{"ext/lib", "we\nird/mod", "tick`/mod", "arrow/-->mod"}
	body := noticeText(t, n)
	for _, w := range []string{"submodules", "not protected", "never reads a submodule", "```\next/lib\n" + `"we\nird/mod"` + "\ntick`/mod\narrow/-->mod\n```"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "we\nird") {
		t.Errorf("a control character reaches the body:\n%s", body)
	}
	mixed, warning, editor := strings.Index(body, "may hold test settings"), strings.Index(body, "never reads a submodule"), strings.Index(body, "open GitHub's editor")
	if mixed < 0 || warning < mixed || editor < warning {
		t.Errorf("the submodule warning is not between the mixed-file warning and the editor link:\n%s", body)
	}
	n.Submodules = []string{"a```b/-->x"}
	if want := "````\na```b/-->x\n````"; !strings.Contains(noticeText(t, n), want) {
		t.Errorf("a submodule path does not stay inside its fence")
	}
}

// Without submodules the notice is what it was before M1.9-E3, byte for byte.
func TestScopeNoticeWithoutSubmodulesIsUnchanged(t *testing.T) {
	const want = "**SuiteWard scope notice**\n\n" +
		"Test-like files in this pull request's head that the current contract does not protect:\n\n```\ninternal/x/x_test.go\ne2e/testdata/a.json\n```\n\n" +
		"These files are not protected by the current contract: SuiteWard assesses only the protected files, so a change to them needs no approval.\n\n" +
		"SuiteWard suggests this declaration, the current canonical `.suiteward.yml` with the new patterns added. Review it and change it as needed:\n\n```\nversion: 1\ninclude:\n  - \"*_test.go\"\n  - \"**/testdata/**\"\n```\n\n" +
		"To change `.suiteward.yml` on this pull request's branch, [open GitHub's editor for it](https://github.com/octo-org/widgets/edit/feature/add-tests/.suiteward.yml) and paste the declaration. SuiteWard never commits it for you: the commit is yours.\n\n" +
		"Committing it creates a protected change, which the owner approves like any other change to the protected files. Nothing is protected until that change is approved and promoted.\n\n" +
		"This comment is a suggestion, not an approval. It does not change the check, and SuiteWard does not run tests.\n"
	if got := noticeText(t, notice()); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// The reply to a command differs from the unprompted notice only by its closing link, and the suggest reply is as it was.
func TestRepliesToASuggestWithoutSubmodulesAreUnchanged(t *testing.T) {
	n := notice()
	plain := noticeText(t, n)
	n.Source = source
	if got, want := noticeText(t, n), strings.TrimSuffix(plain, "\n")+"\n\nIn reply to [your command](#issuecomment-2345678901).\n"; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	const want = "`/suiteward suggest`: SuiteWard found no `.suiteward.yml` on the default branch or in the head of this pull request, so no files are protected yet. Nothing changed.\n\n" +
		"SuiteWard suggests this declaration for this pull request, built from the test-like paths of this repository. Review it and change it as needed:\n\n```\nversion: 1\ninclude:\n  - \"tests/**\"\n```\n\n" +
		"To add it on this pull request's branch, [open GitHub's editor with this file filled in](https://github.com/octo-org/widgets/new/feature/add-tests?filename=.suiteward.yml&value=version%3A%201%0Ainclude%3A%0A%20%20-%20%22tests%2F%2A%2A%22%0A). SuiteWard never commits it for you: the commit is yours.\n\n" +
		"After the file is on this branch, comment `/suiteward propose` to start the first contract.\n\nIn reply to [your command](#issuecomment-2345678901).\n"
	a := missingWith([]string{"tests/**"}, nil, nil, false)
	a.Suggest = true
	if got := suggestionText(t, a); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestScopeNoticeEndsTheDeclarationWithANewlineBeforeTheFence(t *testing.T) {
	n := notice()
	n.Declaration = []byte("version: 1\ninclude:\n  - \"x\"")
	if body := noticeText(t, n); !strings.Contains(body, "  - \"x\"\n```") {
		t.Errorf("the closing fence is not on its own line:\n%s", body)
	}
}

func TestScopeNoticeRepliesToTheCommandWhenThereIsOne(t *testing.T) {
	n := notice()
	n.Source = source
	if body := noticeText(t, n); !strings.Contains(body, "In reply to [your command](#issuecomment-2345678901).") {
		t.Errorf("body does not link the command:\n%s", body)
	}
}

func TestScopeNoticeNeverClaimsThatTestsRan(t *testing.T) {
	claim := regexp.MustCompile(`(?i)tests? (ran|run successfully|passed|pass\b)|all tests|build passed|ci passed`)
	if body := noticeText(t, notice()); claim.MatchString(body) {
		t.Errorf("the body claims that tests ran:\n%s", body)
	}
}

func scopeNoticeCases() map[string]func(*commandtext.ScopeNotice) {
	repo := func(r string) func(*commandtext.ScopeNotice) { return func(n *commandtext.ScopeNotice) { n.Repo = r } }
	br := func(b string) func(*commandtext.ScopeNotice) {
		return func(n *commandtext.ScopeNotice) { n.Branch = b }
	}
	return map[string]func(*commandtext.ScopeNotice){
		"a negative source":               func(n *commandtext.ScopeNotice) { n.Source = -1 },
		"no repository":                   repo(""),
		"a repository with markup":        repo("octo-org/w)[x](y"),
		"a repository named dot":          repo("octo-org/.."),
		"no branch":                       br(""),
		"a branch with a space":           br("a b"),
		"a branch with two dots":          br("a..b"),
		"a branch of 256 bytes":           br(strings.Repeat("b", 256)),
		"no paths":                        func(n *commandtext.ScopeNotice) { n.Paths, n.PathsTotal = nil, 0 },
		"21 paths":                        func(n *commandtext.ScopeNotice) { n.Paths = slices.Repeat([]string{"x_test.go"}, 21) },
		"21 mixed files":                  func(n *commandtext.ScopeNotice) { n.Mixed = slices.Repeat([]string{"package.json"}, 21) },
		"21 submodules":                   func(n *commandtext.ScopeNotice) { n.Submodules = slices.Repeat([]string{"ext/lib"}, 21) },
		"no declaration":                  func(n *commandtext.ScopeNotice) { n.Declaration = nil },
		"a declaration that is not UTF-8": func(n *commandtext.ScopeNotice) { n.Declaration = []byte("version: 1\n\xff") },
		"a declaration of 8193 bytes":     func(n *commandtext.ScopeNotice) { n.Declaration = []byte(strings.Repeat("#", 8193)) },
	}
}

func TestScopeNoticeIsRefusedOnAnImpossibleState(t *testing.T) {
	for name, mutate := range scopeNoticeCases() {
		t.Run(name, func(t *testing.T) {
			n := notice()
			mutate(&n)
			mustPanic(t, func() { commandtext.ScopeNoticeComment(n) })
			if commandtext.ValidateScopeNotice(n) == nil {
				t.Error("ValidateScopeNotice accepts it")
			}
		})
	}
	// The largest allowed notice.
	n := notice()
	n.Paths, n.Mixed, n.Submodules, n.Declaration = slices.Repeat([]string{"x_test.go"}, 20), slices.Repeat([]string{"package.json"}, 20), slices.Repeat([]string{"ext/lib"}, 20), []byte(strings.Repeat("#", 8192))
	n.Repo, n.Branch = "a"+strings.Repeat("b", 38)+"/"+strings.Repeat("c", 100), strings.Repeat("b", 255)
	if err := commandtext.ValidateScopeNotice(n); err != nil {
		t.Errorf("the largest notice is refused: %v", err)
	}
	if err := commandtext.ValidateScopeNotice(notice()); err != nil {
		t.Errorf("the plain notice is refused: %v", err)
	}
}

// The largest notice: 20 paths of 150 bytes of what escapes worst, 20 mixed files of any length, a declaration at the
// limit, and the longest branch and repository. A quarter of GitHub's limit stays free: a declaration that is one run of
// backticks is fenced with a run longer than itself at both ends, so it counts three times.
func TestScopeNoticeFitsInAComment(t *testing.T) {
	for name, unit := range map[string]string{"plain": "a", "invalid UTF-8": "\xff", "backticks": "`", "bidi control": "\u202e", "newline": "\n", "3-byte printable": "\u20ac"} {
		t.Run(name, func(t *testing.T) {
			long := func(size int) []string {
				items := make([]string, 20)
				for i := range items {
					items[i] = strings.Repeat(unit, size/len(unit))
				}
				return items
			}
			n := notice()
			n.Repo, n.Branch = "a"+strings.Repeat("b", 38)+"/"+strings.Repeat("c", 100), strings.Repeat("\u20ac", 85)
			n.PathsTotal, n.Source = 1<<62, source
			n.Declaration = []byte(strings.Repeat("a: b\n", 8192/5)) // valid UTF-8 whatever the unit
			if unit == "`" {
				n.Declaration = []byte(strings.Repeat("`", 8192)) // one backtick run: the fence grows with it
			}
			for _, size := range []int{150, 4096} {
				n.Paths, n.Mixed, n.Submodules = long(size), long(size), long(size)
				body := noticeText(t, n)
				if body == "" || len(body) >= maxComment*3/4 || !utf8.ValidString(body) {
					t.Errorf("entries of %d bytes: body has %d bytes or is not UTF-8, want 0 < n < %d", size, len(body), maxComment*3/4)
				}
			}
		})
	}
}

var suggestionBlock = regexp.MustCompile("(?s)```suggestion\n(.*)\n```\n$")

func suggestionBody(t *testing.T, anchor, indent string, patterns []string) string {
	t.Helper()
	return render(t, func() string { return commandtext.ScopeSuggestionBody(anchor, indent, patterns) })
}

func TestScopeSuggestionBodyHoldsTheAnchorAndTheNewPatterns(t *testing.T) {
	body := suggestionBody(t, `  - "*_test.go"`, "  ", []string{"**/testdata/**", "it's/x", "pytest.ini"})
	block := suggestionBlock.FindStringSubmatch(body)
	if block == nil {
		t.Fatalf("body has no suggestion block:\n%s", body)
	}
	if want := "  - \"*_test.go\"\n  - \"**/testdata/**\"\n  - \"it's/x\"\n  - \"pytest.ini\""; block[1] != want {
		t.Errorf("the block is %q, want %q", block[1], want)
	}
	intro, _, _ := strings.Cut(body, "\n")
	if strings.Count(intro, ".") != 1 || len(intro) > 200 || strings.Contains(intro, "```") || strings.Contains(body, "not run") {
		t.Errorf("the sentence before the block is %q", intro)
	}
	if strings.Count(body, "```") != 2 || strings.Contains(body, "tests ran") {
		t.Errorf("the body has more than one block:\n%s", body)
	}
	if body := suggestionBody(t, `- "a"`, "", []string{"b"}); !strings.Contains(body, "```suggestion\n- \"a\"\n- \"b\"\n```") {
		t.Errorf("an unindented list:\n%s", body)
	}
}

// Committing the block must leave a declaration SuiteWard reads, in each list and at each indentation.
func TestScopeSuggestionBodyParsesWhenSplicedAfterTheAnchor(t *testing.T) {
	cases := map[string]struct {
		file, anchor, indent string
		patterns             []string
		list                 func(scope.Declaration) []scope.Pattern
		want                 []string
	}{
		"include": {"version: 1\ninclude:\n  - \"a/**\"\n  - \"b/**\"\nexclude:\n  - c\n", `  - "a/**"`, "  ", []string{"*_test.go", "x: y/**"},
			scope.Declaration.Include, []string{"a/**", "*_test.go", "x: y/**", "b/**"}},
		"runner":    {"version: 1\nrunner:\n    - pytest.ini\n", "    - pytest.ini", "    ", []string{"pytest.toml"}, scope.Declaration.Runner, []string{"pytest.ini", "pytest.toml"}},
		"no indent": {"version: 1\ninclude:\n- a\n", "- a", "", []string{"b"}, scope.Declaration.Include, []string{"a", "b"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			block := suggestionBlock.FindStringSubmatch(suggestionBody(t, c.anchor, c.indent, c.patterns))
			if block == nil {
				t.Fatal("no suggestion block")
			}
			spliced := strings.Replace(c.file, c.anchor+"\n", block[1]+"\n", 1)
			parsed, err := scope.ParseDeclaration([]byte(spliced))
			if err != nil {
				t.Fatalf("the declaration after the suggestion is invalid: %v\n%s", err, spliced)
			}
			if got := patternNames(c.list(parsed)); !slices.Equal(got, c.want) {
				t.Errorf("the list is %q, want %q", got, c.want)
			}
		})
	}
}

func TestScopeSuggestionBodyFencesAnchorAgainstBackticks(t *testing.T) {
	body := suggestionBody(t, "  - \"a/```/b\" # ```", "  ", []string{"x"})
	if !strings.Contains(body, "````suggestion\n  - \"a/```/b\" # ```\n  - \"x\"\n````") {
		t.Errorf("a backtick run can close the block:\n%s", body)
	}
}

func TestScopeSuggestionBodyIsRefusedOnAnImpossibleState(t *testing.T) {
	cases := map[string]struct {
		anchor, indent string
		patterns       []string
	}{
		"no patterns":                        {`  - "a"`, "  ", nil},
		"21 patterns":                        {`  - "a"`, "  ", slices.Repeat([]string{"x"}, 21)},
		"a pattern with a quote":             {`  - "a"`, "  ", []string{`a"b`}},
		"a pattern with a backslash":         {`  - "a"`, "  ", []string{`a\b`}},
		"a pattern with a newline":           {`  - "a"`, "  ", []string{"a\nb"}},
		"a pattern of 101 bytes":             {`  - "a"`, "  ", []string{strings.Repeat("a", 101)}},
		"an anchor with a newline":           {"  - \"a\"\n  - b", "  ", []string{"x"}},
		"an anchor with a carriage return":   {"  - \"a\"\r", "  ", []string{"x"}},
		"an anchor of 1001 bytes":            {"  - " + strings.Repeat("a", 997), "  ", []string{"x"}},
		"an anchor that is not UTF-8":        {"  - \xff", "  ", []string{"x"}},
		"an anchor that is no list item":     {"  include:", "  ", []string{"x"}},
		"an indent with a tab":               {"\t- a", "\t", []string{"x"}},
		"an indent that is not the anchor's": {"    - a", "  ", []string{"x"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.ScopeSuggestionBody(c.anchor, c.indent, c.patterns) })
			if commandtext.ValidateScopeSuggestion(c.anchor, c.indent, c.patterns) == nil {
				t.Error("ValidateScopeSuggestion accepts it")
			}
		})
	}
	if err := commandtext.ValidateScopeSuggestion(`  - "a"`, "  ", slices.Repeat([]string{strings.Repeat("a", 100)}, 20)); err != nil {
		t.Errorf("the largest suggestion is refused: %v", err)
	}
}

// Without detail, a reduced revision reads as in M1.8, byte for byte.
func TestRevisionCommentWithoutReductionDetailIsUnchanged(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Reduced = true
	const want = "**SuiteWard contract change P7-R3**\n\nThis pull request changes the protected files against the canonical contract V4. SuiteWard proposes the change as P7-R3 for the owner's approval.\n\n- Base canonical version: V4\n- Installation tier: `isolated`\n\n" +
		"Protected files added:\n\n```\ntests/new_test.go\n```\n\nProtected files modified:\n\n```\ntests/a.go\ntests/b.go\n```\n\nProtected files removed:\n\n```\ntests/old.go\n```\n\n" +
		"**This revision reduces the protected scope.** Review it carefully before approving.\n\n" +
		"This comment is a record, not an approval. SuiteWard compares the protected files only; it does not run tests. To approve this exact revision, comment:\n\n```\n/suiteward approve P7-R3 <code>\n```\n\nReplace `<code>` with a current code from your authenticator app.\n\nThis supersedes `P7-R2`.\n"
	if got := revisionText(t, r); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func reduced() commandtext.Revision {
	r := revision(commandtext.Isolated)
	r.Reduced = true
	r.ReducedRules = []string{"internal/**", "!keep/**"}
	r.ReducedPaths, r.ReducedPathsTotal = []string{"internal/a_test.go", "internal/b_test.go"}, 2
	return r
}

func TestRevisionCommentListsWhatTheReductionRemoves(t *testing.T) {
	body := revisionText(t, reduced())
	reduces := strings.Index(body, "**This revision reduces the protected scope.**")
	rules := strings.Index(body, "```\ninternal/**\n!keep/**\n```")
	paths := strings.Index(body, "```\ninternal/a_test.go\ninternal/b_test.go\n```")
	record := strings.Index(body, "This comment is a record")
	if reduces < 0 || rules <= reduces || paths <= rules || record <= paths {
		t.Errorf("the lists are not between the warning and the record (%d, %d, %d, %d):\n%s", reduces, rules, paths, record, body)
	}
	for _, w := range []string{"no longer", "Rules removed", "Paths"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "more") {
		t.Errorf("body counts unlisted entries though all are listed:\n%s", body)
	}
}

func TestRevisionCommentCountsTheReducedPathsItDoesNotList(t *testing.T) {
	r := reduced()
	r.ReducedPathsTotal = 52
	if body := revisionText(t, r); !strings.Contains(body, "and 50 more") {
		t.Errorf("body does not count the unlisted paths:\n%s", body)
	}
	r.ReducedPaths, r.ReducedPathsTotal = nil, 0 // rules alone are enough detail
	if body := revisionText(t, r); !strings.Contains(body, "```\ninternal/**\n!keep/**\n```") || strings.Contains(body, "internal/a_test.go") {
		t.Errorf("rules alone are not listed alone:\n%s", body)
	}
}

func TestRevisionCommentShowsTheReductionOnlyInsideCode(t *testing.T) {
	r := reduced()
	r.ReducedRules = []string{"a\nb/**", "\u202e**", "tick```/**"}
	r.ReducedPaths, r.ReducedPathsTotal = []string{"x\ny_test.go", "\xff_test.go"}, 2
	body := revisionText(t, r)
	for _, w := range []string{`"a\nb/**"`, `\u202e`, "tick```/**\n````", `"x\ny_test.go"`, `\xff`} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "a\nb") || strings.Contains(body, "x\ny") || strings.Contains(body, "\u202e") {
		t.Errorf("a control character reaches the body:\n%s", body)
	}
}

func TestRevisionCommentRefusesReductionDetailThatDoesNotFit(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Revision){
		"detail without a reduction": func(r *commandtext.Revision) { r.Reduced = false },
		"21 rules":                   func(r *commandtext.Revision) { r.ReducedRules = slices.Repeat([]string{"x/**"}, 21) },
		"21 paths":                   func(r *commandtext.Revision) { r.ReducedPaths = slices.Repeat([]string{"x_test.go"}, 21) },
		"a negative paths total":     func(r *commandtext.Revision) { r.ReducedPathsTotal = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := reduced()
			mutate(&r)
			mustPanic(t, func() { commandtext.RevisionComment(r) })
		})
	}
}

// Five lists of 20 entries: three quarters of the limit, as for the notice.
func TestRevisionCommentWithReductionDetailFitsInAComment(t *testing.T) {
	for name, unit := range map[string]string{"plain": "a", "invalid UTF-8": "\xff", "backticks": "`", "bidi control": "\u202e", "newline": "\n"} {
		t.Run(name, func(t *testing.T) {
			long := func(size int) []string {
				items := make([]string, 20)
				for i := range items {
					items[i] = strings.Repeat(unit, size/len(unit))
				}
				return items
			}
			r := revision(commandtext.CoLocated)
			r.AddedTotal, r.ModifiedTotal, r.RemovedTotal, r.ReducedPathsTotal = 1<<62, 1<<62, 1<<62, 1<<62
			r.Proposal, r.Revision, r.Canonical, r.Previous, r.Reduced = 999999999, 999999999, 999999999, "P999999999-R999999998", true
			for _, size := range []int{150, 4096} {
				r.Added, r.Modified, r.Removed = long(size), long(size), long(size)
				r.ReducedRules, r.ReducedPaths = long(size), long(size)
				body := revisionText(t, r)
				if body == "" || len(body) >= maxComment*3/4 || !utf8.ValidString(body) {
					t.Errorf("entries of %d bytes: body has %d bytes or is not UTF-8, want 0 < n < %d", size, len(body), maxComment*3/4)
				}
			}
		})
	}
}

// A notice of `/suiteward suggest` covers the whole head, not only what the pull request added.
func TestScopeNoticeDoesNotClaimThatThePullRequestAddedTheFiles(t *testing.T) {
	body := noticeText(t, notice())
	if !strings.Contains(body, "in this pull request's head that the current contract does not protect") || strings.Contains(body, "adds") {
		t.Errorf("the title claims more than the head holds:\n%s", body)
	}
}

// The notice and the bootstrap reply warn about a cut in the same words.
func TestScopeNoticeAndSuggestDeclarationWarnAboutACutAlike(t *testing.T) {
	n := notice()
	n.Cut = true
	const line = "This list was cut at the limit of 20 patterns, so test-like paths may be missing from it. Please review it and add what is missing."
	for name, body := range map[string]string{"notice": noticeText(t, n), "reply": suggestionText(t, missingWith([]string{"tests/**"}, nil, nil, true))} {
		if !strings.Contains(body, line) {
			t.Errorf("the %s lacks the line %q:\n%s", name, line, body)
		}
	}
}

// A revision whose reduction detail did not fit the outbox still states the reduction, and says that the detail was left out.
func TestRevisionCommentSaysThatTheReductionDetailWasOmitted(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Reduced, r.ReducedOmitted, r.ReducedPathsTotal = true, true, 52
	body := revisionText(t, r)
	for _, want := range []string{"**This revision reduces the protected scope.**", "omitted", "no longer covered: 52"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Rules removed") || strings.Contains(body, "no longer covers") {
		t.Errorf("body lists detail that was omitted:\n%s", body)
	}
	r.ReducedPathsTotal = 0
	if body := revisionText(t, r); !strings.Contains(body, "omitted") || strings.Contains(body, "paths") {
		t.Errorf("without a count of paths the body must say omitted and name no paths:\n%s", body)
	}
	for name, mutate := range map[string]func(*commandtext.Revision){
		"omitted without a reduction": func(r *commandtext.Revision) { r.Reduced = false },
		"omitted with rules":          func(r *commandtext.Revision) { r.ReducedRules = []string{"x/**"} },
		"omitted with paths":          func(r *commandtext.Revision) { r.ReducedPaths = []string{"x_test.go"} },
	} {
		t.Run(name, func(t *testing.T) {
			r := revision(commandtext.Isolated)
			r.Reduced, r.ReducedOmitted = true, true
			mutate(&r)
			mustPanic(t, func() { commandtext.RevisionComment(r) })
		})
	}
}
