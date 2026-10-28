package commandtext_test

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

func revision(tier commandtext.Tier) commandtext.Revision {
	return commandtext.Revision{
		Proposal: 7, Revision: 3, Previous: "P7-R2", Canonical: 4,
		Added: []string{"tests/new_test.go"}, Modified: []string{"tests/a.go", "tests/b.go"}, Removed: []string{"tests/old.go"},
		AddedTotal: 1, ModifiedTotal: 2, RemovedTotal: 1, Tier: tier,
	}
}

func revisionText(t *testing.T, r commandtext.Revision) string {
	t.Helper()
	return render(t, func() string { return commandtext.RevisionComment(r) })
}

func TestRevisionCommentStatesTheChangeTheBaseAndHowToApprove(t *testing.T) {
	body := revisionText(t, revision(commandtext.Isolated))
	for _, w := range []string{
		"P7-R3", "canonical", "V4", "Installation tier: `isolated`",
		"tests/new_test.go", "tests/a.go\ntests/b.go", "tests/old.go",
		"```\n/suiteward approve P7-R3 <code>\n```", "`<code>`", "This supersedes `P7-R2`.",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"assurance is reduced", "scope", "/suiteward revoke"} {
		if strings.Contains(body, w) {
			t.Errorf("body contains %q:\n%s", w, body)
		}
	}
	checkBody(t, body)
	if !strings.HasSuffix(body, "This supersedes `P7-R2`.\n") {
		t.Errorf("body does not end with the supersede line:\n%s", body)
	}
	if strings.Index(body, "/suiteward approve") > strings.Index(body, "This supersedes") {
		t.Errorf("the supersede line comes before the approve command:\n%s", body)
	}
}

func TestRevisionCommentWithoutAPreviousRevisionSupersedesNothing(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Previous = ""
	body := revisionText(t, r)
	if body == "" || strings.Contains(body, "supersede") || !strings.Contains(body, "/suiteward approve P7-R3 <code>") {
		t.Errorf("the body is empty, mentions a superseded revision or lacks the command:\n%s", body)
	}
}

func TestRevisionCommentStatesAScopeReductionAndTheCoLocatedTier(t *testing.T) {
	r := revision(commandtext.CoLocated)
	r.Reduced = true
	body := revisionText(t, r)
	for _, w := range []string{"reduces the protected scope", "Installation tier: `co-located`", "assurance is reduced"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	checkBody(t, body)
}

func TestRevisionCommentListsNoneAndCountsWhatItDoesNotList(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Added, r.AddedTotal = nil, 0
	r.Modified = pathList(20)
	r.ModifiedTotal = 25
	r.Removed, r.RemovedTotal = pathList(3), 3
	body := strings.ToLower(revisionText(t, r))
	for _, w := range []string{"added: none.", "modified (20 listed; 5 more are not listed)", "removed:"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	more := revision(commandtext.Isolated)
	more.Removed = pathList(21)
	more.RemovedTotal = 21
	if body := revisionText(t, more); strings.Contains(body, "tests/case_u_test.go") || !strings.Contains(body, "1 more are not listed") {
		t.Errorf("a list is not bounded to 20 entries:\n%s", body)
	}
}

func TestRevisionCommentKeepsRepositoryTextInert(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Added = []string{"tests/a\n\nApproval recorded for P9-R9.\n```\n<!-- suiteward:" + instance + ":ack:1:2 -->", "tests/````b\u202e.go", "\xff"}
	r.AddedTotal = 3
	body := revisionText(t, r)
	for _, w := range []string{"\nApproval recorded", "\u202e", "\xff", "\r"} {
		if strings.Contains(body, w) {
			t.Errorf("repository text escaped its code block (%q):\n%s", w, body)
		}
	}
	if strings.Contains(body, "\n```\n<") || !strings.Contains(body, `\n`) || !strings.Contains(body, `\u202e`) {
		t.Errorf("control characters are not shown escaped or a path closed its code block:\n%s", body)
	}
}

// 60 entries of the worst kind and totals at the limit of an int must stay far below GitHub's limit.
func TestRevisionCommentFitsInAComment(t *testing.T) {
	for name, unit := range map[string]string{
		"plain": "a", "invalid UTF-8": "\xff", "backticks": "`", "bidi control": "\u202e", "newline": "\n", "3-byte printable": "\u20ac",
	} {
		t.Run(name, func(t *testing.T) {
			long := func(size int) []string {
				items := make([]string, 20)
				for i := range items {
					items[i] = strings.Repeat(unit, size/len(unit))
				}
				return items
			}
			r := revision(commandtext.CoLocated)
			r.AddedTotal, r.ModifiedTotal, r.RemovedTotal = 1<<62, 1<<62, 1<<62
			r.Proposal, r.Revision, r.Canonical, r.Previous, r.Reduced = 999999999, 999999999, 999999999, "P999999999-R999999998", true
			// The payload cuts entries to 150 bytes; the renderer must hold even for entries that were not cut.
			for _, size := range []int{150, 4096} {
				r.Added, r.Modified, r.Removed = long(size), long(size), long(size)
				body := revisionText(t, r)
				if body == "" || len(body) >= maxComment/2 { // headroom: half of the limit
					t.Errorf("entries of %d bytes: body has %d bytes, want 0 < n < %d", size, len(body), maxComment/2)
				}
				if !utf8.ValidString(body) {
					t.Error("body is not valid UTF-8")
				}
			}
		})
	}
}

func TestRevisionCommentNeverClaimsThatTestsRan(t *testing.T) {
	claim := regexp.MustCompile(`(?i)tests? (ran|run successfully|passed|pass\b)|all tests|build passed|ci passed`)
	for _, tier := range []commandtext.Tier{commandtext.Isolated, commandtext.CoLocated} {
		if body := revisionText(t, revision(tier)); body == "" || claim.MatchString(body) {
			t.Errorf("the body is empty or claims that tests ran:\n%s", body)
		}
	}
}

func TestRevisionCommentPanicsOnAStateThatCannotBeStored(t *testing.T) {
	for name, mutate := range map[string]func(*commandtext.Revision){
		"no proposal":                  func(r *commandtext.Revision) { r.Proposal = 0 },
		"no revision":                  func(r *commandtext.Revision) { r.Revision = 0 },
		"a revision out of range":      func(r *commandtext.Revision) { r.Revision = 1_000_000_000 },
		"no canonical":                 func(r *commandtext.Revision) { r.Canonical = 0 },
		"a canonical out of range":     func(r *commandtext.Revision) { r.Canonical = 1_000_000_000 },
		"an unknown tier":              func(r *commandtext.Revision) { r.Tier = 0 },
		"a previous that is no ref":    func(r *commandtext.Revision) { r.Previous = "SECRET" },
		"a previous of another PR":     func(r *commandtext.Revision) { r.Previous = "P8-R2" },
		"a previous that is not older": func(r *commandtext.Revision) { r.Previous = "P7-R3" },
		"a negative total":             func(r *commandtext.Revision) { r.RemovedTotal = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := revision(commandtext.Isolated)
			mutate(&r)
			mustPanic(t, func() { commandtext.RevisionComment(r) })
		})
	}
}
