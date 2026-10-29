package commandtext_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// A runner-uncovered check (docs/contracts/runtime.md, "Check reason runner-uncovered") lists the runner files and gives both ways out.
func TestCheckSummaryOfRunnerUncoveredListsTheFilesAndBothWaysOut(t *testing.T) {
	c := check(commandtext.CheckInProgress, "runner-uncovered")
	c.Paths, c.PathsTotal = []string{"jest.config.js", "pkg/conftest.py"}, 25
	title, summary := checkText(t, c)
	if title != "Runner files outside the protected scope" {
		t.Errorf("title %q", title)
	}
	for _, w := range []string{"```\njest.config.js\npkg/conftest.py\n```", "and 23 more", "can change how the protected tests run", "commit the suggested declaration", "add their paths to `exclude`", "approve", "which control how tests run", "- State: `in_progress`"} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
}

func TestCheckSummaryOfRunnerUncoveredShowsThePathsEscapedInAFence(t *testing.T) {
	c := check(commandtext.CheckInProgress, "runner-uncovered")
	c.Paths, c.PathsTotal = []string{"a```b\n--> <script>/conftest.py"}, 1
	if _, summary := checkText(t, c); !strings.Contains(summary, "````\n\"a```b\\n--> <script>/conftest.py\"\n````\n") {
		t.Errorf("the path is not shown quoted inside a longer fence:\n%s", summary)
	}
}

func TestCheckSummaryListsPathsOnlyForRunnerUncovered(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Check){
		"paths of another reason": func(c *commandtext.Check) {
			c.State, c.Reason, c.Paths, c.PathsTotal = commandtext.CheckInProgress, "approval-missing", []string{"a"}, 1
		},
		"a total of another reason": func(c *commandtext.Check) { c.State, c.Reason, c.PathsTotal = commandtext.CheckSuccess, "ready", 1 },
		"21 paths":                  func(c *commandtext.Check) { c.Paths, c.PathsTotal = slices.Repeat([]string{"a"}, 21), 21 },
		"a total below the paths":   func(c *commandtext.Check) { c.Paths, c.PathsTotal = []string{"a", "b"}, 1 },
		"a total without paths":     func(c *commandtext.Check) { c.Paths, c.PathsTotal = nil, 3 },
	} {
		t.Run(name, func(t *testing.T) {
			c := check(commandtext.CheckInProgress, "runner-uncovered")
			mutate(&c)
			mustPanic(t, func() { commandtext.CheckSummary(c) })
		})
	}
}
