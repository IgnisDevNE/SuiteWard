package commandtext_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// executionOnly is a revision whose only reduction is a lowered execution policy: no rule, no exclude and no path.
func executionOnly(t *testing.T, entries ...string) commandtext.Revision {
	t.Helper()
	r := revision(commandtext.Isolated)
	r.Reduced, r.ReducedRules = true, entries
	return r
}

const (
	lowerCanonical = "execution canonical: required -> advisory"
	lowerReceipt   = "execution receipt: advisory -> off"
)

// A policy that gets weaker is a reduction. With no removed rule or path, the text says that it lowers the execution policy and lists each level
// under its own heading, between the warning and the record.
func TestRevisionCommentOfALoweredExecutionPolicyListsTheLevels(t *testing.T) {
	body := revisionText(t, executionOnly(t, lowerCanonical, lowerReceipt))

	warning := strings.Index(body, "**This revision lowers the test requirements.** Review it carefully before approving.")
	levels := strings.Index(body, "Test requirements lowered:\n\n```\napproved tests: required -> advisory\nreceipt: advisory -> off\n```")
	record := strings.Index(body, "This comment is a record")
	if warning < 0 || levels <= warning || record <= levels {
		t.Errorf("the levels are not between the warning and the record (%d, %d, %d):\n%s", warning, levels, record, body)
	}
	for _, unwanted := range []string{"reduces the protected scope", "Rules removed", "Excludes added", "Paths that the scope no longer covers", "execution canonical", "execution receipt", "omitted"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body contains %q for a revision that only lowers the execution policy:\n%s", unwanted, body)
		}
	}
	checkBody(t, body)
}

func TestRevisionCommentOfOneLoweredLevelListsOnlyThatLevel(t *testing.T) {
	body := revisionText(t, executionOnly(t, lowerReceipt))
	if !strings.Contains(body, "Test requirements lowered:\n\n```\nreceipt: advisory -> off\n```") || strings.Contains(body, "approved tests:") {
		t.Errorf("body does not list the one level that got weaker:\n%s", body)
	}
}

// A reduction of the scope that also lowers the policy says both, and lists the rules, the excludes, the paths and the levels.
func TestRevisionCommentOfAScopeReductionThatAlsoLowersThePolicyStatesBoth(t *testing.T) {
	r := reduced()
	r.ReducedRules = append(r.ReducedRules, lowerCanonical)

	body := revisionText(t, r)

	lead := strings.Index(body, "**This revision reduces the protected scope and lowers the test requirements.** Review it carefully before approving.")
	rules := strings.Index(body, "Rules removed from the scope:\n\n```\ninternal/**\n!keep/**\n```")
	excludes := strings.Index(body, "Excludes added:\n\n```\ntests/b_test.go\n```")
	paths := strings.Index(body, "```\ninternal/a_test.go\ninternal/b_test.go\n```")
	levels := strings.Index(body, "Test requirements lowered:\n\n```\napproved tests: required -> advisory\n```")
	record := strings.Index(body, "This comment is a record")
	if lead < 0 || rules <= lead || excludes <= rules || paths <= excludes || levels <= paths || record <= levels {
		t.Errorf("the warning, the lists and the record are out of order (%d, %d, %d, %d, %d, %d):\n%s", lead, rules, excludes, paths, levels, record, body)
	}
}

// A reduction of the scope alone reads as it did before the policy was a rule.
func TestRevisionCommentOfAScopeReductionHasNoExecutionHeading(t *testing.T) {
	body := revisionText(t, reduced())
	if strings.Contains(body, "Test requirements lowered") || strings.Contains(body, "execution policy") || !strings.Contains(body, "**This revision reduces the protected scope.**") {
		t.Errorf("a reduction of the scope speaks of the execution policy or lacks its own warning:\n%s", body)
	}
}

// Only a revision whose every entry lowers the policy removes no file from the scope. A removed file of such a revision was deleted, so the
// numbers say removed; a reduction of the scope still says removed from the protected files.
func TestRevisionCommentThatOnlyLowersThePolicyWordsARemovalAsAnotherChange(t *testing.T) {
	r := revisionWith(t, commandtext.Isolated, removalChange()...)
	r.Reduced, r.ReducedRules = true, []string{lowerCanonical, lowerReceipt}

	body := revisionText(t, r)

	for _, want := range []string{"- Files: 1 added, 0 modified, 2 removed\n", "- Marker lines seen: 1 removed test file\n", "**This revision lowers the test requirements.**"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "from the protected files") {
		t.Errorf("body says that a file left the protected files, though the scope did not change:\n%s", body)
	}

	r.ReducedRules = []string{"rule tests/old_test.go", lowerCanonical}
	r.ReducedPaths, r.ReducedPathsTotal = []string{"tests/old_test.go"}, 1
	body = revisionText(t, r)
	for _, want := range []string{"- Files: 1 added, 0 modified, 2 removed from the protected files\n", "- Marker lines seen: 1 test file removed from the protected files\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("a reduction of the scope that also lowers the policy lacks %q:\n%s", want, body)
		}
	}
}

// The comment states what it accepts: exactly the entries that scope.Reductions writes, and no other kind.
func TestRevisionCommentAcceptsTheTwoExecutionEntriesAndNoOtherKind(t *testing.T) {
	for _, entry := range []string{lowerCanonical, lowerReceipt, "execution canonical: required -> off", "execution receipt: before-approval -> advisory"} {
		r := executionOnly(t, entry)
		if body := revisionText(t, r); body == "" {
			t.Errorf("an execution entry %q is refused", entry)
		}
	}
	for _, entry := range []string{
		"execution canonical:required -> off", "execution policy: required -> off", "Execution canonical: required -> off", "execution ",
		"execution canonicalx: required -> off", "executions receipt: advisory -> off", "canonical: required -> advisory", "execute receipt: advisory -> off", "",
	} {
		t.Run(entry, func(t *testing.T) {
			mustPanic(t, func() { commandtext.RevisionComment(executionOnly(t, entry)) })
			mustPanic(t, func() { commandtext.RevisionComment(executionOnly(t, lowerCanonical, entry)) })
		})
	}
}

// What follows the prefix is shown as text of the repository would be: inside the fence, escaped.
func TestRevisionCommentShowsAnExecutionEntryOnlyInsideCode(t *testing.T) {
	body := revisionText(t, executionOnly(t, "execution receipt: a\nb```\n<!-- x -->\n/suiteward approve P9-R9 123456", "execution canonical: \u202e"))
	for _, want := range []string{`a\nb`, `\u202e`, "````"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "a\nb") || strings.Contains(body, "\u202e") || strings.Contains(body, "\n<!--") {
		t.Errorf("a control character or a marker reaches the body:\n%s", body)
	}
	checkBodyInert(t, body)
}

// checkBodyInert holds when no line of body reads as a command.
func checkBodyInert(t *testing.T, body string) {
	t.Helper()
	if parsed := commandtext.Parse(body); parsed.Kind != commandtext.KindNotCommand {
		t.Errorf("the body reads as the command %+v:\n%s", parsed, body)
	}
}

func TestRevisionCommentRefusesExecutionDetailThatDoesNotFit(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Revision){
		"an execution entry without a reduction": func(r *commandtext.Revision) { r.Reduced = false },
		"21 execution entries": func(r *commandtext.Revision) {
			r.ReducedRules = slices.Repeat([]string{lowerCanonical}, 21)
		},
		"execution entries that are also omitted": func(r *commandtext.Revision) { r.ReducedOmitted = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := executionOnly(t, lowerCanonical)
			mutate(&r)
			mustPanic(t, func() { commandtext.RevisionComment(r) })
		})
	}
}
