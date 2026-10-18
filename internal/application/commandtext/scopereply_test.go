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

func TestScopeReplyEndsWithTheNextStep(t *testing.T) {
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseDefaultBranch} {
		body := noticeText(t, reply(base))
		// A command to copy, on a line of its own, before the closing sentences: SuiteWard starts the first contract only on request.
		if !strings.Contains(body, "```\n/suiteward propose\n```") {
			t.Errorf("base %d: the body does not offer /suiteward propose as a copyable command:\n%s", base, body)
		}
		for _, w := range []string{"Nothing is protected until"} {
			if !strings.Contains(body, w) {
				t.Errorf("base %d: body lacks %q:\n%s", base, w, body)
			}
		}
	}
	body := noticeText(t, reply(commandtext.BaseContract))
	if strings.Contains(body, "/suiteward propose") || !strings.Contains(body, "approve the new revision SuiteWard posts") {
		t.Errorf("after the first contract the next step is to approve the new revision, not to propose:\n%s", body)
	}
}

func TestScopeReplyMentionsTheOneClickCommitSuggestion(t *testing.T) {
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseDefaultBranch, commandtext.BaseContract} {
		if body := noticeText(t, reply(base)); !strings.Contains(body, "Commit suggestion") {
			t.Errorf("base %d: the body does not mention GitHub's Commit suggestion:\n%s", base, body)
		}
	}
}

// The default branch's file is not on the pull request's branch, so its editor link would be a 404, and the declaration may be too
// large for a link that carries it: the link opens GitHub's new-file page for .suiteward.yml, to paste into.
func TestScopeReplyForTheDefaultBranchOpensTheNewFilePageWithoutTheDeclaration(t *testing.T) {
	raw, u := editorLink(t, noticeText(t, reply(commandtext.BaseDefaultBranch)))
	if u.Host != "github.com" || u.Path != "/octo-org/widgets/new/feature/add-tests" || u.Query().Get("filename") != ".suiteward.yml" || u.Query().Has("value") {
		t.Errorf("the link is %q, want the new-file page of .suiteward.yml on the branch, without a value", raw)
	}
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseContract} {
		raw, u := editorLink(t, noticeText(t, reply(base)))
		if u.Path != "/octo-org/widgets/edit/feature/add-tests/.suiteward.yml" || u.RawQuery != "" {
			t.Errorf("base %d: the link is %q, want the editor of .suiteward.yml on the branch", base, raw)
		}
	}
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
