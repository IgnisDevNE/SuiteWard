package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// A runner name can be missing from the base before any file of that name exists (docs/contracts/runtime.md, "Scope discovery (M1.9)",
// Command /suiteward suggest): the reply then has no paths, and says why the name matters anyway.

func runnerOnlyReply(base commandtext.ScopeBase) commandtext.ScopeNotice {
	n := reply(base)
	n.Paths, n.PathsTotal, n.AddInclude = nil, 0, nil
	return n
}

func TestScopeReplyForRunnerNamesAloneHasNoPathsAndExplainsWhyTheNamesMatter(t *testing.T) {
	for _, base := range []commandtext.ScopeBase{commandtext.BasePull, commandtext.BaseDefaultBranch, commandtext.BaseContract} {
		body := noticeText(t, runnerOnlyReply(base))
		for _, w := range []string{"`/suiteward suggest`", "Nothing changed", "```\nconftest.py\n```", "```\n" + string(reply(base).Declaration) + "```", "before a file of that name exists", "can change how the tests run"} {
			if !strings.Contains(body, w) {
				t.Errorf("base %d: body lacks %q:\n%s", base, w, body)
			}
		}
		for _, w := range []string{"does not cover:", "internal/x/x_test.go", "Patterns missing from the `include` list"} {
			if strings.Contains(body, w) {
				t.Errorf("base %d: body says %q though no path is uncovered:\n%s", base, w, body)
			}
		}
		checkBody(t, body)
	}
}

func TestScopeReplyExplainsRunnerNamesWheneverItListsThem(t *testing.T) {
	if body := noticeText(t, reply(commandtext.BasePull)); !strings.Contains(body, "before a file of that name exists") {
		t.Errorf("body does not explain the runner names:\n%s", body)
	}
	n := reply(commandtext.BasePull)
	n.AddRunner = nil
	if body := noticeText(t, n); strings.Contains(body, "before a file of that name exists") {
		t.Errorf("body explains runner names it does not list:\n%s", body)
	}
}

func TestScopeNoticeWithoutPathsNeedsABaseAndNewPatterns(t *testing.T) {
	n := runnerOnlyReply(commandtext.BasePull)
	n.AddRunner = nil
	mustPanic(t, func() { commandtext.ScopeNoticeComment(n) })
	n = runnerOnlyReply(0)
	n.Source = 0
	n.AddRunner = nil
	mustPanic(t, func() { commandtext.ScopeNoticeComment(n) })
}

func TestSuggestRefusedForAnInvalidFileNamesTheFileWithoutQuotingIt(t *testing.T) {
	ack := func(base commandtext.ScopeBase) string {
		return render(t, func() string {
			return commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.SuggestRefused, Refusal: commandtext.InvalidSource, Owner: owner, Source: source, Base: base})
		})
	}
	for base, file := range map[commandtext.ScopeBase]string{commandtext.BasePull: "this pull request's `.suiteward.yml`", commandtext.BaseDefaultBranch: "the default branch's `.suiteward.yml`"} {
		body := ack(base)
		for _, w := range []string{"`/suiteward suggest` was not processed: " + file + " is not a valid declaration", "Nothing changed.", "comment `/suiteward suggest` again"} {
			if !strings.Contains(body, w) {
				t.Errorf("base %d: body lacks %q:\n%s", base, w, body)
			}
		}
	}
	if body := ack(0); !strings.Contains(body, "could not read the files of this pull request") {
		t.Errorf("without a base the words are the old ones:\n%s", body)
	}
	mustPanic(t, func() {
		commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.SuggestRefused, Refusal: commandtext.Fork, Owner: owner, Source: source, Base: commandtext.BasePull})
	})
}
