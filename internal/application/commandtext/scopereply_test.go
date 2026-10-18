package commandtext_test

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The reply of /suiteward suggest that compares the head with a base declaration (docs/contracts/runtime.md, "Scope discovery
// (M1.9)", Command /suiteward suggest): it names the base, lists the missing patterns and ends with the next step.

func reply(base commandtext.ScopeBase) commandtext.ScopeNotice {
	n := notice()
	n.Source, n.Base = source, base
	n.AddInclude, n.AddRunner = []string{"*_test.go"}, []string{"conftest.py"}
	return n
}

func TestScopeReplyNamesTheBaseItComparedWith(t *testing.T) {
	for _, tc := range []struct {
		name    string
		base    commandtext.ScopeBase
		subject string
		other   []string
	}{
		{"this pull request's file", commandtext.BasePull, "this pull request's `.suiteward.yml`", []string{"the default branch's", "the current contract"}},
		{"the default branch's file", commandtext.BaseDefaultBranch, "the default branch's `.suiteward.yml`", []string{"this pull request's `.suiteward.yml` does", "the current contract"}},
		{"the canonical", commandtext.BaseContract, "the current contract", []string{"the default branch's", "this pull request's `.suiteward.yml`"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := noticeText(t, reply(tc.base))
			for _, w := range []string{"`/suiteward suggest`", "Nothing changed", tc.subject + " does not cover", "```\ninternal/x/x_test.go\ne2e/testdata/a.json\n```",
				"```\n*_test.go\n```", "```\nconftest.py\n```", "```\n" + string(reply(tc.base).Declaration) + "```", "In reply to [your command](#issuecomment-2345678901).",
				"does not change the check", "does not run tests"} {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q:\n%s", w, body)
				}
			}
			for _, w := range tc.other {
				if strings.Contains(body, w) {
					t.Errorf("body names another base, %q:\n%s", w, body)
				}
			}
			checkBody(t, body)
		})
	}
}

func TestScopeReplyListsOnlyTheListsThatHaveNewPatterns(t *testing.T) {
	n := reply(commandtext.BasePull)
	n.AddRunner = nil
	body := noticeText(t, n)
	if !strings.Contains(body, "```\n*_test.go\n```") || strings.Contains(body, "conftest.py") || strings.Contains(body, "runner") {
		t.Errorf("body does not list the include patterns alone:\n%s", body)
	}
}

// The next step is the last thing the owner reads, before the line that links the command.
func TestScopeReplyEndsWithTheNextStep(t *testing.T) {
	const link = "\nIn reply to [your command](#issuecomment-2345678901).\n"
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseDefaultBranch} {
		body, found := strings.CutSuffix(noticeText(t, reply(base)), link)
		// A command to copy, on a line of its own: SuiteWard starts the first contract only on request.
		if !found || !strings.HasSuffix(body, "```\n/suiteward propose\n```\n") || !strings.Contains(body, "Nothing is protected until") {
			t.Errorf("base %d: the body does not end with /suiteward propose as a copyable command, after saying that nothing is protected yet:\n%s", base, body)
		}
	}
	body, found := strings.CutSuffix(noticeText(t, reply(commandtext.BaseContract)), link)
	if !found || strings.Contains(body, "/suiteward propose") || !strings.HasSuffix(body, "approve the new revision SuiteWard posts, like any other change to the protected files. Nothing is protected until that change is approved and promoted.\n") {
		t.Errorf("after the first contract the body ends with approving the new revision, not with a propose:\n%s", body)
	}
}

func TestScopeReplyMentionsTheOneClickCommitSuggestionWhenTheHeadsFileIsTheBase(t *testing.T) {
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseContract} {
		if body := noticeText(t, reply(base)); !strings.Contains(body, "Commit suggestion") {
			t.Errorf("base %d: the body does not mention GitHub's Commit suggestion:\n%s", base, body)
		}
	}
	// The default branch's file is not in this pull request's diff: nothing is suggested there.
	if body := noticeText(t, reply(commandtext.BaseDefaultBranch)); strings.Contains(body, "Commit suggestion") || strings.Contains(body, "review comment") {
		t.Errorf("the body offers a review suggestion for the default branch's file:\n%s", body)
	}
}

// The first contract is proposed from the default branch's file, so the change has to reach that branch first. The branch is not
// in the payload, so there is no link to it: the file is shown to copy.
func TestScopeReplyForTheDefaultBranchSaysTheChangeMustReachTheDefaultBranchFirst(t *testing.T) {
	body := noticeText(t, reply(commandtext.BaseDefaultBranch))
	for _, w := range []string{"default branch first", "commit or merge", "```\n/suiteward propose\n```"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"new file", "this pull request's branch", "editor"} {
		if strings.Contains(body, w) {
			t.Errorf("body says %q, which is not where the change goes:\n%s", w, body)
		}
	}
	if links := linkPattern.FindAllString(body, -1); len(links) != 0 {
		t.Errorf("the body links %v; want no link", links)
	}
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseContract} {
		raw, u := editorLink(t, noticeText(t, reply(base)))
		if u.Path != "/octo-org/widgets/edit/feature/add-tests/.suiteward.yml" || u.RawQuery != "" {
			t.Errorf("base %d: the link is %q, want the editor of .suiteward.yml on the branch", base, raw)
		}
	}
}

func TestNothingUncoveredNamesTheBaseWhenThereIsOne(t *testing.T) {
	ack := func(base commandtext.ScopeBase) string {
		return render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.NothingUncovered, Owner: owner, Source: source, Base: base})
		})
	}
	for base, subject := range map[commandtext.ScopeBase]string{commandtext.BasePull: "this pull request's `.suiteward.yml`", commandtext.BaseDefaultBranch: "the default branch's `.suiteward.yml`", commandtext.BaseContract: "the current contract"} {
		if body := ack(base); !strings.Contains(body, "covered by "+subject) || strings.Contains(body, "in the scope already") {
			t.Errorf("base %d: the body does not name %q:\n%s", base, subject, body)
		}
	}
	if body := ack(0); !strings.Contains(body, "is in the scope already") {
		t.Errorf("without a base the words are the old ones:\n%s", body)
	}
	mustPanic(t, func() { commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.NothingUncovered, Owner: owner, Source: source, Base: commandtext.BaseDefaultBranch + 1}) })
	mustPanic(t, func() { commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.Skipped, Owner: owner, Source: source, Base: commandtext.BasePull}) })
}

// Without a base the notice is the checks job's, which says nothing of a command.
func TestScopeNoticeWithoutABaseKeepsItsWords(t *testing.T) {
	n := notice()
	n.Source = source
	body := noticeText(t, n)
	for _, w := range []string{"**SuiteWard scope notice**", "not protected by the current contract", "SuiteWard never commits it for you"} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	for _, w := range []string{"`/suiteward suggest`", "Commit suggestion", "/suiteward propose", "approve the new revision"} {
		if strings.Contains(body, w) {
			t.Errorf("body says %q:\n%s", w, body)
		}
	}
}

func TestScopeReplyIsRefusedOnAnImpossibleState(t *testing.T) {
	for name, change := range map[string]func(*commandtext.ScopeNotice){
		"an unknown base":          func(n *commandtext.ScopeNotice) { n.Base = commandtext.BaseDefaultBranch + 1 },
		"a negative base":          func(n *commandtext.ScopeNotice) { n.Base = -1 },
		"a base without a command": func(n *commandtext.ScopeNotice) { n.Source = 0 },
		"21 new patterns":          func(n *commandtext.ScopeNotice) { n.AddInclude = slices.Repeat([]string{"x"}, 21) },
		"21 new patterns in both lists": func(n *commandtext.ScopeNotice) {
			n.AddInclude, n.AddRunner = slices.Repeat([]string{"x"}, 11), slices.Repeat([]string{"y"}, 10)
		},
		"new patterns without a base":          func(n *commandtext.ScopeNotice) { n.Base = 0 },
		"a new include pattern without a base": func(n *commandtext.ScopeNotice) { n.Base, n.AddRunner = 0, nil },
		"a new runner pattern without a base":  func(n *commandtext.ScopeNotice) { n.Base, n.AddInclude = 0, nil },
	} {
		t.Run(name, func(t *testing.T) {
			n := reply(commandtext.BasePull)
			change(&n)
			if commandtext.ValidateScopeNotice(n) == nil {
				t.Error("ValidateScopeNotice accepts it")
			}
			mustPanic(t, func() { commandtext.ScopeNoticeComment(n) })
		})
	}
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseDefaultBranch, commandtext.BaseContract} {
		if err := commandtext.ValidateScopeNotice(reply(base)); err != nil {
			t.Errorf("base %d: %v", base, err)
		}
	}
}

// The largest reply: the largest notice with 20 new patterns of the longest length the renderer shows, in the worst escaping.
func TestScopeReplyFitsInAComment(t *testing.T) {
	for name, unit := range map[string]string{"plain": "a", "invalid UTF-8": "\xff", "backticks": "`", "newline": "\n"} {
		t.Run(name, func(t *testing.T) {
			long := make([]string, 10)
			for i := range long {
				long[i] = strings.Repeat(unit, 150/len(unit))
			}
			for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseDefaultBranch, commandtext.BaseContract} {
				n := reply(base)
				n.Repo, n.Branch = "a"+strings.Repeat("b", 38)+"/"+strings.Repeat("c", 100), strings.Repeat("\u20ac", 85)
				n.PathsTotal = 1 << 62
				n.Declaration = []byte(strings.Repeat("a: b\n", 8192/5))
				n.Paths, n.Mixed, n.Submodules, n.AddInclude, n.AddRunner = long, long, long, long, long
				body := noticeText(t, n)
				if body == "" || len(body) >= maxComment*3/4 || !utf8.ValidString(body) {
					t.Errorf("base %d: body has %d bytes or is not UTF-8, want 0 < n < %d", base, len(body), maxComment*3/4)
				}
			}
		})
	}
}
