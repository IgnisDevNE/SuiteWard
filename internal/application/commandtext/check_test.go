package commandtext_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// checkReasons are the reasons of the runtime contract, each with the state it belongs to and a phrase only its
// text holds.
var checkReasons = []struct {
	state  commandtext.CheckState
	reason string
	phrase string
	next   string // a phrase of the next action
	name   string // the state as the summary names it
}{
	{commandtext.CheckSuccess, "unchanged", "match the canonical contract", "nothing is required", "success"},
	{commandtext.CheckSuccess, "ready", "ready for integration", "integrate this pull request", "success"},
	{commandtext.CheckInProgress, "approval-missing", "no current approval", "To approve this exact revision, comment:", "in_progress"},
	{commandtext.CheckInProgress, "not-current", "is not current", "update the branch", "in_progress"},
	{commandtext.CheckInProgress, "assessment-pending", "has not finished assessing", "nothing is required", "in_progress"},
	{commandtext.CheckInProgress, "source-unavailable", "truncated the file tree", "reducing the number of files", "in_progress"},
	{commandtext.CheckInProgress, "runner-uncovered", "runner files that the protected scope does not cover", "protect the files", "in_progress"},
	{commandtext.CheckInProgress, "fork-unsupported", "from forks are not supported yet", "from a branch of this repository", "in_progress"},
	{commandtext.CheckFailure, "invalid-source", "is invalid", "and push", "failure"},
	{commandtext.CheckFailure, "integrity-failed", "stored canonical contract failed its integrity verification", "restore the artifact store from a backup", "failure"},
	{commandtext.CheckFailure, "owner-command-rejected", "latest command on this pull request was rejected", "with a new code", "failure"},
}

func check(state commandtext.CheckState, reason string) commandtext.Check {
	return commandtext.Check{State: state, Reason: reason, Proposal: 3, Revision: 2, Version: 4, Tier: commandtext.Isolated}
}

// checkText reports a panic as a test error so every case shows its own failure.
func checkText(t *testing.T, c commandtext.Check) (title, summary string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panicked: %v", c.Reason, r)
		}
	}()
	return commandtext.CheckSummary(c)
}

// TestCheckTitlesAreTheOnesOfADR0015AndTheRunbook pins the titles deploy/HOST.md and the M1.6b phase document quote.
func TestCheckTitlesAreTheOnesOfADR0015AndTheRunbook(t *testing.T) {
	want := map[string]string{
		"unchanged":              "No protected change",
		"ready":                  "Ready for integration",
		"approval-missing":       "Awaiting owner approval",
		"owner-command-rejected": "Owner command rejected",
	}
	for _, tc := range checkReasons {
		if title, ok := want[tc.reason]; ok {
			if got, _ := checkText(t, check(tc.state, tc.reason)); got != title {
				t.Errorf("%s: title %q, want %q", tc.reason, got, title)
			}
		}
	}
}

func TestCheckSummaryStatesEveryReason(t *testing.T) {
	titles := map[string]string{}
	for _, tc := range checkReasons {
		title, summary := checkText(t, check(tc.state, tc.reason))
		if title == "" || strings.Contains(title, "\n") || len(title) > 80 {
			t.Errorf("%s: title %q is empty, has several lines or is long", tc.reason, title)
		}
		if other, dup := titles[title]; dup {
			t.Errorf("%s and %s share the title %q", tc.reason, other, title)
		}
		titles[title] = tc.reason
		if !strings.Contains(summary, tc.phrase) {
			t.Errorf("%s: summary lacks %q:\n%s", tc.reason, tc.phrase, summary)
		}
		if !strings.Contains(summary, tc.next) {
			t.Errorf("%s: summary does not state the next action (%q):\n%s", tc.reason, tc.next, summary)
		}
		if !strings.Contains(summary, "- State: `"+tc.name+"`") {
			t.Errorf("%s: summary does not name the state %q:\n%s", tc.reason, tc.name, summary)
		}
		if !strings.Contains(summary, "P3-R2") || !strings.Contains(summary, "V4") {
			t.Errorf("%s: summary names neither the revision nor the version:\n%s", tc.reason, summary)
		}
	}
}

func TestCheckSummaryWithoutAReferenceOrAVersionNamesNeither(t *testing.T) {
	named := regexp.MustCompile(`P[0-9]|V[0-9]`)
	for _, tc := range checkReasons {
		if tc.reason == "approval-missing" {
			continue
		}
		c := check(tc.state, tc.reason)
		c.Proposal, c.Revision, c.Version = 0, 0, 0
		_, summary := checkText(t, c)
		if summary == "" || named.MatchString(summary) {
			t.Errorf("%s: the summary is empty or names a reference or a version that does not exist:\n%s", tc.reason, summary)
		}
	}
}

func TestCheckSummaryOffersTheApproveCommandOnlyWhenApprovalIsMissing(t *testing.T) {
	for _, tc := range checkReasons {
		_, summary := checkText(t, check(tc.state, tc.reason))
		has := strings.Contains(summary, "/suiteward approve P3-R2 <code>")
		if has != (tc.reason == "approval-missing") {
			t.Errorf("%s: approve command present = %v", tc.reason, has)
		}
		if strings.Contains(summary, "/suiteward revoke") {
			t.Errorf("%s: the summary offers a revocation", tc.reason)
		}
	}
	_, summary := checkText(t, check(commandtext.CheckInProgress, "approval-missing"))
	if !strings.Contains(summary, "```\n/suiteward approve P3-R2 <code>\n```") {
		t.Errorf("the approve command is not a copyable block:\n%s", summary)
	}
}

func TestCheckSummaryStatesTheTier(t *testing.T) {
	for _, tc := range checkReasons {
		c := check(tc.state, tc.reason)
		_, isolated := checkText(t, c)
		if !strings.Contains(isolated, "`isolated`") || strings.Contains(isolated, "reduced") {
			t.Errorf("%s: the isolated summary says %q", tc.reason, isolated)
		}
		c.Tier = commandtext.CoLocated
		_, colocated := checkText(t, c)
		if !strings.Contains(colocated, "`co-located`") || !strings.Contains(colocated, "assurance is reduced") {
			t.Errorf("%s: the co-located summary lacks the tier or the reduced assurance:\n%s", tc.reason, colocated)
		}
	}
}

func TestCheckSummaryNeverClaimsThatTestsRan(t *testing.T) {
	claim := regexp.MustCompile(`(?i)tests? (ran|run successfully|passed|pass\b)|all tests|build passed|ci passed`)
	for _, tc := range checkReasons {
		for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
			c := check(tc.state, tc.reason)
			c.Tier = tier
			title, summary := checkText(t, c)
			if claim.MatchString(title + summary) {
				t.Errorf("%s: claims that tests ran:\n%s", tc.reason, summary)
			}
			if !strings.Contains(summary, "This check reports on the protected contract only.") || strings.Contains(summary, "does not run tests") {
				t.Errorf("%s: the summary lacks its closing sentence, or says that SuiteWard does not run tests:\n%s", tc.reason, summary)
			}
		}
	}
}

func TestCheckSummaryStaysFarBelowGitHubsLimit(t *testing.T) {
	longest := 0
	for _, tc := range checkReasons {
		c := commandtext.Check{State: tc.state, Reason: tc.reason, Proposal: 999999999, Revision: 999999999, Version: 999999999, Tier: commandtext.CoLocated}
		title, summary := checkText(t, c)
		longest = max(longest, len(title), len(summary))
	}
	if longest == 0 || longest > 4096 { // GitHub accepts 65,535 characters of summary and 255 of title
		t.Errorf("the longest output is %d bytes", longest)
	}
}

func TestCheckSummaryHoldsNoCommentTextOrMarker(t *testing.T) {
	for _, tc := range checkReasons {
		title, summary := checkText(t, check(tc.state, tc.reason))
		for _, s := range []string{title, summary} {
			if s == "" || strings.Contains(s, "<!--") || strings.Contains(s, "@") || strings.Contains(s, "#issuecomment") {
				t.Errorf("%s: the output is empty or holds a marker, a mention or a comment link: %q", tc.reason, s)
			}
		}
	}
}

func TestCheckSummaryPanicsOnAnImpossibleCheck(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Check){
		"an unknown reason":             func(c *commandtext.Check) { c.Reason = "tests-failed" },
		"an empty reason":               func(c *commandtext.Check) { c.Reason = "" },
		"a state the reason is not of":  func(c *commandtext.Check) { c.State = commandtext.CheckFailure },
		"an unknown state":              func(c *commandtext.Check) { c.State = 0 },
		"an unknown tier":               func(c *commandtext.Check) { c.Tier = 0 },
		"a proposal without a revision": func(c *commandtext.Check) { c.Revision = 0 },
		"a revision without a proposal": func(c *commandtext.Check) { c.Proposal = 0 },
		"a proposal out of range":       func(c *commandtext.Check) { c.Proposal = 1_000_000_000 },
		"a negative version":            func(c *commandtext.Check) { c.Version = -1 },
		"a version out of range":        func(c *commandtext.Check) { c.Version = 1_000_000_000 },
	} {
		t.Run(name, func(t *testing.T) {
			c := check(commandtext.CheckSuccess, "ready")
			mutate(&c)
			mustPanic(t, func() { commandtext.CheckSummary(c) })
		})
	}
	t.Run("approval missing without a reference", func(t *testing.T) {
		c := check(commandtext.CheckInProgress, "approval-missing")
		c.Proposal, c.Revision = 0, 0
		mustPanic(t, func() { commandtext.CheckSummary(c) })
	})
}

// Before the first contract no canonical exists, so a head cannot change it: the text names the proposed first contract.
func TestCheckSummaryOfAMissingApprovalBeforeTheFirstContractNamesTheProposedFirstContract(t *testing.T) {
	c := check(commandtext.CheckInProgress, "approval-missing")
	c.Version = 0
	_, summary := checkText(t, c)
	for _, w := range []string{"carries the proposed first contract P3-R2", "awaiting the owner's approval of that exact revision", "```\n/suiteward approve P3-R2 <code>\n```", "- Proposal revision: P3-R2"} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
	if strings.Contains(summary, "changes the protected contract") || strings.Contains(summary, "V0") {
		t.Errorf("a head cannot change a contract that does not exist:\n%s", summary)
	}
}

func TestCheckSummaryOfAMissingApprovalAgainstACanonicalIsUnchanged(t *testing.T) {
	_, summary := checkText(t, check(commandtext.CheckInProgress, "approval-missing"))
	if !strings.Contains(summary, "This head changes the protected contract and has no current approval from the owner for its exact revision.") || strings.Contains(summary, "first contract") {
		t.Errorf("the text against a canonical changed:\n%s", summary)
	}
}

func bootstrapReady() commandtext.Check {
	return commandtext.Check{State: commandtext.CheckSuccess, Reason: "bootstrap-ready", Proposal: 3, Revision: 2, Tier: commandtext.Isolated}
}

func TestCheckSummaryOfABootstrapThatIsReadyStatesWhatWillHappen(t *testing.T) {
	title, summary := checkText(t, bootstrapReady())
	if title != "Bootstrap ready for integration" {
		t.Errorf("title %q", title)
	}
	for _, w := range []string{
		"- State: `success`", "- Proposal revision: P3-R2", "approved the exact revision", "assessed the integrity of this head",
		"No canonical contract exists yet", "first canonical version", "from the exact merge commit after", "- Installation tier: `isolated`",
		"This check reports on the protected contract only.",
	} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
	if strings.Contains(summary, "Canonical version:") || strings.Contains(summary, "/suiteward") || strings.Contains(summary, "reduced") {
		t.Errorf("summary names a canonical version that does not exist, offers a command or states a reduced assurance:\n%s", summary)
	}
}

func TestCheckSummaryOfABootstrapThatIsReadyStatesTheTierAndNeverClaimsThatTestsRan(t *testing.T) {
	claim := regexp.MustCompile(`(?i)tests? (ran|run successfully|passed|pass\b)|all tests|build passed|ci passed`)
	c := bootstrapReady()
	c.Tier = commandtext.CoLocated
	title, summary := checkText(t, c)
	if !strings.Contains(summary, "`co-located`") || !strings.Contains(summary, "assurance is reduced") || claim.MatchString(title+summary) {
		t.Errorf("the summary lacks the tier or claims that tests ran:\n%s", summary)
	}
	c.Proposal, c.Revision = 999999999, 999999999
	if _, summary := checkText(t, c); len(summary) > 4096 {
		t.Errorf("the summary has %d bytes", len(summary))
	}
}

func TestCheckSummaryAcceptsBootstrapReadyOnlyUnderSuccess(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Check){
		"in progress":        func(c *commandtext.Check) { c.State = commandtext.CheckInProgress },
		"failure":            func(c *commandtext.Check) { c.State = commandtext.CheckFailure },
		"no reference":       func(c *commandtext.Check) { c.Proposal, c.Revision = 0, 0 },
		"a canonical exists": func(c *commandtext.Check) { c.Version = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := bootstrapReady()
			mutate(&c)
			mustPanic(t, func() { commandtext.CheckSummary(c) })
		})
	}
}

// preCanonical are the reasons of a pull request before the first canonical version exists (the explicit
// bootstrap, M1.7b rework): no canonical version and no reference to name.
var preCanonical = []struct {
	state  commandtext.CheckState
	reason string
}{
	{commandtext.CheckInProgress, "bootstrap-awaiting"},
	{commandtext.CheckSuccess, "bootstrap-skipped"},
}

func noCanonical(state commandtext.CheckState, reason string) commandtext.Check {
	return commandtext.Check{State: state, Reason: reason, Tier: commandtext.Isolated}
}

func TestCheckSummaryOfAwaitingTheBootstrapStatesHowToStartOrSkip(t *testing.T) {
	title, summary := checkText(t, noCanonical(commandtext.CheckInProgress, "bootstrap-awaiting"))
	if title == "" || strings.Contains(title, "\n") || len(title) > 80 {
		t.Errorf("title %q", title)
	}
	for _, w := range []string{
		"SuiteWard has no canonical contract yet.", "- State: `in_progress`", "- Installation tier: `isolated`",
		"`/suiteward propose`", "pull request that should carry the first contract", "`/suiteward skip <code>`", "This check reports on the protected contract only.",
	} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
	if strings.Contains(summary, "/suiteward approve") || strings.Contains(summary, "Canonical version:") || strings.Contains(summary, "Proposal revision:") {
		t.Errorf("summary names an approve command, a canonical version or a revision that does not exist:\n%s", summary)
	}
}

func TestCheckSummaryOfASkippedBootstrapSaysNothingWasAssessed(t *testing.T) {
	title, summary := checkText(t, noCanonical(commandtext.CheckSuccess, "bootstrap-skipped"))
	if title == "" || strings.Contains(title, "\n") || len(title) > 80 {
		t.Errorf("title %q", title)
	}
	for _, w := range []string{
		"Skipped by the owner before the first contract.", "- State: `success`", "No canonical contract exists", "nothing was assessed", "This check reports on the protected contract only.",
	} {
		if !strings.Contains(summary, w) {
			t.Errorf("summary lacks %q:\n%s", w, summary)
		}
	}
	if strings.Contains(summary, "/suiteward approve") || strings.Contains(summary, "Canonical version:") {
		t.Errorf("summary offers an approve command or names a canonical version that does not exist:\n%s", summary)
	}
}

func TestCheckSummaryBeforeTheFirstContractStaysInsideItsBounds(t *testing.T) {
	claim := regexp.MustCompile(`(?i)tests? (ran|run successfully|passed|pass\b)|all tests|build passed|ci passed`)
	for _, tc := range preCanonical {
		for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
			c := noCanonical(tc.state, tc.reason)
			c.Tier = tier
			title, summary := checkText(t, c)
			if claim.MatchString(title+summary) || strings.Contains(summary, "<!--") || strings.Contains(summary, "@") || strings.Contains(summary, "#issuecomment") || len(summary) > 4096 {
				t.Errorf("%s: the summary claims a test result, holds a marker, a mention or a comment link, or is long:\n%s", tc.reason, summary)
			}
			if tier == commandtext.CoLocated && (!strings.Contains(summary, "`co-located`") || !strings.Contains(summary, "assurance is reduced")) {
				t.Errorf("%s: the co-located summary lacks the tier or the reduced assurance:\n%s", tc.reason, summary)
			}
		}
	}
}

func TestCheckSummaryBeforeTheFirstContractRefusesWhatCannotExist(t *testing.T) {
	for _, tc := range preCanonical {
		for name, mutate := range map[string]func(*commandtext.Check){
			"a canonical version": func(c *commandtext.Check) { c.Version = 1 },
			"a state the reason is not of": func(c *commandtext.Check) {
				c.State = commandtext.CheckFailure
			},
			"an unknown tier": func(c *commandtext.Check) { c.Tier = 0 },
		} {
			t.Run(tc.reason+" with "+name, func(t *testing.T) {
				c := noCanonical(tc.state, tc.reason)
				mutate(&c)
				mustPanic(t, func() { commandtext.CheckSummary(c) })
			})
		}
	}
}
