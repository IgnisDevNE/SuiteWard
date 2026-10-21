package commandtext_test

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The comment of a runner-uncovered check (docs/contracts/runtime.md, "Check reason runner-uncovered"): it lists the runner files,
// gives the two ways out as full declarations and never quotes file content.

func runnerNotice() commandtext.RunnerUncovered {
	return commandtext.RunnerUncovered{
		Repo: repository, Branch: branch,
		Paths: []string{"jest.config.js", "pkg/conftest.py"}, PathsTotal: 2,
		Protect: []byte("version: 1\ninclude:\n  - /tests/**\nrunner:\n  - jest.config.*\n"),
		Ignore:  []byte("version: 1\ninclude:\n  - /tests/**\nexclude:\n  - jest.config.js\n"),
	}
}

func runnerText(t *testing.T, n commandtext.RunnerUncovered) string {
	t.Helper()
	return render(t, func() string { return commandtext.RunnerUncoveredComment(n) })
}

func TestRunnerUncoveredCommentListsTheFilesAndGivesBothDeclarationsAndTheEditLink(t *testing.T) {
	n := runnerNotice()
	body := runnerText(t, n)
	for _, w := range []string{
		"```\njest.config.js\npkg/conftest.py\n```", "can change how the protected tests run", "`in_progress`",
		"Protect the files", "```\n" + string(n.Protect) + "```", "Ignore the files", "```\n" + string(n.Ignore) + "```",
		"reduction", "approves", "Commit suggestion", "not an approval", "does not run tests",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if raw, u := editorLink(t, body); u.Host != "github.com" || u.Path != "/octo-org/widgets/edit/feature/add-tests/.suiteward.yml" || u.RawQuery != "" {
		t.Errorf("the link is %q, want the editor of .suiteward.yml on the branch", raw)
	}
	checkBody(t, body)
	if !strings.HasSuffix(body, "\n") || strings.Contains(body, "and 0 more") {
		t.Errorf("the body lacks its final newline or says 'and 0 more':\n%s", body)
	}
}

func TestRunnerUncoveredCommentCountsTheFilesItLeavesOut(t *testing.T) {
	n := runnerNotice()
	n.PathsTotal = 45
	if body := runnerText(t, n); !strings.Contains(body, "and 43 more") {
		t.Errorf("body does not say how many files it leaves out:\n%s", body)
	}
}

func TestRunnerUncoveredCommentWithoutAnIgnoreDeclarationOffersOnlyToProtect(t *testing.T) {
	n := runnerNotice()
	n.Ignore = nil
	body := runnerText(t, n)
	for _, w := range []string{"Protect the files", "```\n" + string(n.Protect) + "```", "cannot offer to ignore"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "Ignore the files") || strings.Contains(body, "exclude:") {
		t.Errorf("body offers to ignore files it cannot name in exclude:\n%s", body)
	}
}

// The paths and the declarations come from the repository: they are shown only in a fence that backticks cannot close, with control
// characters escaped, and the branch is path-escaped.
func TestRunnerUncoveredCommentEscapesWhatComesFromTheRepository(t *testing.T) {
	n := runnerNotice()
	n.Paths, n.PathsTotal = []string{"a```b\n--> <script>\u202e/conftest.py"}, 1
	n.Protect = []byte("version: 1\n# ````\nrunner:\n  - conftest.py\n")
	n.Branch = "feature/a#c%20"
	body := runnerText(t, n)
	if !strings.Contains(body, "````\n\"a```b\\n--> <script>\\u202e/conftest.py\"\n````\n") {
		t.Errorf("the path is not shown quoted inside a longer fence:\n%s", body)
	}
	if !strings.Contains(body, "`````\nversion: 1\n# ````\nrunner:") {
		t.Errorf("the declaration is not fenced against its own backticks:\n%s", body)
	}
	if _, u := editorLink(t, body); u.EscapedPath() != "/octo-org/widgets/edit/feature/a%23c%2520/.suiteward.yml" {
		t.Errorf("the branch is not path-escaped: %q", u.EscapedPath())
	}
}

func TestRunnerUncoveredCommentStaysFarBelowGitHubsLimit(t *testing.T) {
	n := runnerNotice()
	n.Protect = []byte("version: 1\n# " + strings.Repeat("`", 8100) + "\n")
	n.Ignore = n.Protect
	n.Paths, n.PathsTotal = make([]string, 20), 100
	for i := range n.Paths {
		n.Paths[i] = strings.Repeat("`", 150)
	}
	if body := runnerText(t, n); body == "" || len(body) >= maxComment || !utf8.ValidString(body) {
		t.Errorf("body has %d bytes or is not UTF-8, want 0 < n < %d", len(body), maxComment)
	}
}

func TestRunnerUncoveredCommentIsRefusedOnAnImpossibleState(t *testing.T) {
	tooBig := []byte(strings.Repeat("a", commandtext.MaxDeclaration+1))
	for name, mutate := range map[string]func(*commandtext.RunnerUncovered){
		"no paths":                            func(n *commandtext.RunnerUncovered) { n.Paths, n.PathsTotal = nil, 0 },
		"21 paths":                            func(n *commandtext.RunnerUncovered) { n.Paths, n.PathsTotal = make([]string, 21), 21 },
		"a total below the paths":             func(n *commandtext.RunnerUncovered) { n.PathsTotal = 1 },
		"no protect declaration":              func(n *commandtext.RunnerUncovered) { n.Protect = nil },
		"a protect declaration too big":       func(n *commandtext.RunnerUncovered) { n.Protect = tooBig },
		"an ignore declaration too big":       func(n *commandtext.RunnerUncovered) { n.Ignore = tooBig },
		"a declaration that is not UTF-8":     func(n *commandtext.RunnerUncovered) { n.Protect = []byte("\xff") },
		"a repository that is not owner/name": func(n *commandtext.RunnerUncovered) { n.Repo = "widgets" },
		"an empty branch":                     func(n *commandtext.RunnerUncovered) { n.Branch = "" },
	} {
		t.Run(name, func(t *testing.T) {
			n := runnerNotice()
			mutate(&n)
			mustPanic(t, func() { commandtext.RunnerUncoveredComment(n) })
			if commandtext.ValidateRunnerUncovered(n) == nil {
				t.Error("ValidateRunnerUncovered accepts it")
			}
		})
	}
	if err := commandtext.ValidateRunnerUncovered(runnerNotice()); err != nil {
		t.Errorf("a plain notice is refused: %v", err)
	}
}

// The review suggestion of each choice (the one-click form of the two declarations).

// choiceQuoting is the quoting that a choice carries: the one of the list the anchor is in, none for a list that the choice creates.
func choiceQuoting(anchor, newList string) commandtext.Quoting {
	if newList != "" {
		return ""
	}
	return anchorQuoting(anchor)
}

func choiceBody(t *testing.T, choice commandtext.RunnerChoice, anchor, indent string, patterns []string, newList string) string {
	t.Helper()
	return render(t, func() string {
		return commandtext.RunnerChoiceBody(choice, anchor, indent, patterns, newList, choiceQuoting(anchor, newList))
	})
}

func TestRunnerChoiceBodyAddsTheItemsToAnExistingList(t *testing.T) {
	body := choiceBody(t, commandtext.ProtectChoice, "  - pytest.ini", "  ", []string{"conftest.py", "pytest.toml"}, "")
	block := suggestionBlock.FindStringSubmatch(body)
	if len(block) < 2 || block[1] != "  - pytest.ini\n  - conftest.py\n  - pytest.toml" {
		t.Fatalf("the block is %q in\n%s", block, body)
	}
	for _, w := range []string{"protecting the runner files", "`runner` list", "needs the owner's approval"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	ignore := choiceBody(t, commandtext.IgnoreChoice, `  - "gen/"`, "  ", []string{"jest.config.js"}, "")
	for _, w := range []string{"ignoring the runner files", "`exclude` list", "reduces the protected scope", "needs the owner's approval"} {
		if !strings.Contains(ignore, w) {
			t.Errorf("ignore body lacks %q:\n%s", w, ignore)
		}
	}
	if strings.Contains(body, "not run") || strings.Count(body, "```") != 2 || strings.Count(ignore, "```") != 2 {
		t.Errorf("the body says tests did not run or has more than one block:\n%s", body)
	}
}

// Committing the block must leave a declaration SuiteWard reads, whether the list exists or the block creates it.
func TestRunnerChoiceBodyParsesWhenSplicedAfterTheAnchor(t *testing.T) {
	cases := map[string]struct {
		file, anchor, indent, newList string
		choice                        commandtext.RunnerChoice
		patterns                      []string
		list                          func(scope.Declaration) []scope.Pattern
		want                          []string
	}{
		"an existing exclude list": {"version: 1\nexclude:\n  - gen/\ninclude:\n  - a\n", "  - gen/", "  ", "", commandtext.IgnoreChoice, []string{"jest.config.js"},
			scope.Declaration.Exclude, []string{"gen/", "jest.config.js"}},
		"an existing runner list without indent": {"version: 1\nrunner:\n- a\n", "- a", "", "", commandtext.ProtectChoice, []string{"jest.config.*"},
			scope.Declaration.Runner, []string{"a", "jest.config.*"}},
		"a created exclude list after the include list": {"version: 1\ninclude:\n  - /tests/**\n", "  - /tests/**", "", "exclude", commandtext.IgnoreChoice, []string{"jest.config.js", "x: y"},
			scope.Declaration.Exclude, []string{"jest.config.js", "x: y"}},
		"a created runner list in the middle of the file": {"version: 1\ninclude:\n  - /tests/**\nexclude:\n  - gen/\n", "  - /tests/**", "", "runner", commandtext.ProtectChoice, []string{"jest.config.*"},
			scope.Declaration.Runner, []string{"jest.config.*"}},
		"a created list after an indentless list": {"version: 1\ninclude:\n- a\n", "- a", "", "exclude", commandtext.IgnoreChoice, []string{"c"},
			scope.Declaration.Exclude, []string{"c"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			block := suggestionBlock.FindStringSubmatch(choiceBody(t, c.choice, c.anchor, c.indent, c.patterns, c.newList))
			if block == nil {
				t.Fatal("no suggestion block")
			}
			spliced := strings.Replace(c.file, c.anchor+"\n", block[1]+"\n", 1)
			parsed, err := scope.ParseDeclaration([]byte(spliced))
			if err != nil {
				t.Fatalf("the declaration after the suggestion is invalid: %v\n%s", err, spliced)
			}
			if got := patternNames(c.list(parsed)); strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("the list is %q, want %q", got, c.want)
			}
		})
	}
}

func TestRunnerChoiceBodyIsRefusedOnAnImpossibleState(t *testing.T) {
	cases := map[string]struct {
		choice                  commandtext.RunnerChoice
		anchor, indent, newList string
		patterns                []string
	}{
		"no choice":                               {0, "  - a", "  ", "", []string{"x"}},
		"an unknown choice":                       {3, "  - a", "  ", "", []string{"x"}},
		"no patterns":                             {commandtext.ProtectChoice, "  - a", "  ", "", nil},
		"21 patterns":                             {commandtext.ProtectChoice, "  - a", "  ", "", slices.Repeat([]string{"x"}, 21)},
		"a pattern with a quote":                  {commandtext.IgnoreChoice, "  - a", "  ", "", []string{`a"b`}},
		"a pattern of 101 bytes":                  {commandtext.IgnoreChoice, "  - a", "  ", "", []string{strings.Repeat("a", 101)}},
		"an anchor that is no list item":          {commandtext.ProtectChoice, "  runner:", "  ", "", []string{"x"}},
		"an anchor with a newline":                {commandtext.ProtectChoice, "  - a\n  - b", "  ", "", []string{"x"}},
		"an indent with a tab":                    {commandtext.ProtectChoice, "\t- a", "\t", "", []string{"x"}},
		"an indent that is not the anchor's":      {commandtext.ProtectChoice, "    - a", "  ", "", []string{"x"}},
		"a created list that is not the choice's": {commandtext.ProtectChoice, "  - a", "", "exclude", []string{"x"}},
		"an unknown created list":                 {commandtext.IgnoreChoice, "  - a", "", "include", []string{"x"}},
		"a created list at a deep indent":         {commandtext.ProtectChoice, "  - a", strings.Repeat(" ", 41), "runner", []string{"x"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustPanic(t, func() {
				commandtext.RunnerChoiceBody(c.choice, c.anchor, c.indent, c.patterns, c.newList, choiceQuoting(c.anchor, c.newList))
			})
			if commandtext.ValidateRunnerChoice(c.choice, c.anchor, c.indent, c.patterns, c.newList, choiceQuoting(c.anchor, c.newList)) == nil {
				t.Error("ValidateRunnerChoice accepts it")
			}
		})
	}
}

// A declaration of exactly the limit is accepted and, in the worst fence (one run of backticks, which the fence outgrows at both ends), the
// comment with both declarations still fits; one byte more is refused.
func TestRunnerUncoveredCommentAcceptsDeclarationsOfExactlyTheLimit(t *testing.T) {
	n := runnerNotice()
	n.Protect = []byte(strings.Repeat("`", commandtext.MaxDeclaration))
	n.Ignore = n.Protect
	if err := commandtext.ValidateRunnerUncovered(n); err != nil {
		t.Fatalf("declarations of %d bytes are refused: %v", commandtext.MaxDeclaration, err)
	}
	if body := runnerText(t, n); body == "" || len(body) >= maxComment || !utf8.ValidString(body) {
		t.Errorf("body has %d bytes or is not UTF-8, want 0 < n < %d", len(body), maxComment)
	}
	n.Protect = append(n.Protect, '`')
	if commandtext.ValidateRunnerUncovered(n) == nil {
		t.Errorf("a protect declaration of %d bytes is accepted", len(n.Protect))
	}
}
