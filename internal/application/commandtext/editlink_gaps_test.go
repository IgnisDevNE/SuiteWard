package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// D-SCOPE-DISCOVERY, every suggested change links to GitHub's editor: also the reply that puts the declaration on the default branch,
// and the refusals that ask the owner to fix .suiteward.yml. The link goes where the file must be fixed.

func TestScopeReplyForTheDefaultBranchLinksToTheEditorOfTheDefaultBranch(t *testing.T) {
	n := reply(commandtext.BaseDefaultBranch)
	n.DefaultBranch = "release/2#1"

	body := noticeText(t, n)

	raw, _ := editorLink(t, body)
	if want := "https://github.com/octo-org/widgets/edit/release/2%231/.suiteward.yml"; raw != want {
		t.Errorf("the link is %q, want %q", raw, want)
	}
	for _, want := range []string{"[open GitHub's editor for it]", "on the default branch", "new branch and a pull request", "there first"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "this pull request's branch") || strings.Contains(body, "feature/add-tests") {
		t.Errorf("the body points at the pull request's branch, which is not where the change goes:\n%s", body)
	}
}

func TestScopeReplyRefusesADefaultBranchThatDoesNotFit(t *testing.T) {
	for name, change := range map[string]func(*commandtext.ScopeNotice){
		"with another base":             func(n *commandtext.ScopeNotice) { n.Base = commandtext.BasePull },
		"with the contract as the base": func(n *commandtext.ScopeNotice) { n.Base = commandtext.BaseContract },
		"that is no git ref":            func(n *commandtext.ScopeNotice) { n.DefaultBranch = "a b" },
	} {
		t.Run(name, func(t *testing.T) {
			n := reply(commandtext.BaseDefaultBranch)
			n.DefaultBranch = "main"
			change(&n)

			if err := commandtext.ValidateScopeNotice(n); err == nil {
				t.Error("ValidateScopeNotice accepted it")
			}
		})
	}
}

func refusal(outcome commandtext.Outcome, refusal commandtext.Refusal, base commandtext.ScopeBase, branch string) commandtext.Ack {
	a := commandtext.Ack{Outcome: outcome, Refusal: refusal, Owner: owner, Source: source, Base: base}
	if branch != "" {
		a.Repo, a.Branch = repository, branch
	}
	return a
}

func TestRefusalsThatAskToFixTheDeclarationLinkToTheEditorOfTheFileRefused(t *testing.T) {
	const pullLink = "https://github.com/octo-org/widgets/edit/feat/a%23b%25c/.suiteward.yml"
	for _, tc := range []struct {
		name  string
		ack   commandtext.Ack
		link  string
		where string
		says  string
	}{
		{"suggest, the head's declaration", refusal(commandtext.SuggestRefused, commandtext.InvalidSource, commandtext.BasePull, "feat/a#b%c"), pullLink, "this pull request's branch", "Fix it"},
		{"suggest, the default branch's declaration", refusal(commandtext.SuggestRefused, commandtext.InvalidSource, commandtext.BaseDefaultBranch, "main"),
			"https://github.com/octo-org/widgets/edit/main/.suiteward.yml", "the default branch", "Fix it"},
		{"propose, the source declaration", refusal(commandtext.ProposeRefused, commandtext.InvalidSource, commandtext.BasePull, "feat/a#b%c"), pullLink, "this pull request's branch", "If the file is the problem"},
		{"propose, the default branch's declaration", refusal(commandtext.ProposeRefused, commandtext.InvalidSource, commandtext.BaseDefaultBranch, "main"),
			"https://github.com/octo-org/widgets/edit/main/.suiteward.yml", "the default branch", "If the file is the problem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := commandtext.ValidateAck(tc.ack); err != nil {
				t.Errorf("ValidateAck refuses it: %v", err)
			}
			body := render(t, func() string { return commandtext.Acknowledge(tc.ack) })

			if raw, _ := editorLink(t, body); raw != tc.link {
				t.Errorf("the link is %q, want %q", raw, tc.link)
			}
			for _, want := range []string{"[open GitHub's editor for it](", "on " + tc.where, tc.says} {
				if !strings.Contains(body, want) {
					t.Errorf("the body lacks %q:\n%s", want, body)
				}
			}
		})
	}
}

// An acknowledgment queued without the repository and the branch has no link, and the words stay as they were.
func TestRefusalsWithoutARepositoryAndABranchHaveNoLink(t *testing.T) {
	for _, ack := range []commandtext.Ack{
		refusal(commandtext.SuggestRefused, commandtext.InvalidSource, commandtext.BasePull, ""),
		refusal(commandtext.ProposeRefused, commandtext.InvalidSource, 0, ""),
	} {
		body := render(t, func() string { return commandtext.Acknowledge(ack) })

		if strings.Contains(body, "github.com") || strings.Contains(body, "editor") {
			t.Errorf("the body has a link though it has no repository and branch:\n%s", body)
		}
	}
}

// A link belongs only to the refusals above, and a repository goes with its branch.
func TestAcknowledgeRefusesAnEditorLinkThatDoesNotFit(t *testing.T) {
	for name, ack := range map[string]commandtext.Ack{
		"a fork":                          refusal(commandtext.SuggestRefused, commandtext.Fork, 0, "feature"),
		"a propose that is not open":      refusal(commandtext.ProposeRefused, commandtext.NotOpen, 0, "feature"),
		"a suggest with another file":     refusal(commandtext.SuggestRefused, commandtext.InvalidSource, commandtext.BaseContract, "feature"),
		"an invalid suggest with no base": refusal(commandtext.SuggestRefused, commandtext.InvalidSource, 0, "feature"),
		"a skip":                          refusal(commandtext.SkipRefused, commandtext.Fork, 0, "feature"),
		"a branch that is no ref":         refusal(commandtext.SuggestRefused, commandtext.InvalidSource, commandtext.BasePull, "a b"),
		"a branch without a repository":   {Outcome: commandtext.SuggestRefused, Refusal: commandtext.InvalidSource, Base: commandtext.BasePull, Owner: owner, Source: source, Branch: "feature"},
		"a repository without a branch":   {Outcome: commandtext.SuggestRefused, Refusal: commandtext.InvalidSource, Base: commandtext.BasePull, Owner: owner, Source: source, Repo: repository},
	} {
		t.Run(name, func(t *testing.T) {
			if commandtext.ValidateAck(ack) == nil {
				t.Error("ValidateAck accepts it")
			}
			defer func() {
				if recover() == nil {
					t.Error("Acknowledge did not panic")
				}
			}()
			commandtext.Acknowledge(ack)
		})
	}
}

// The head has no .suiteward.yml: accepting the link needs a new file, so the link is GitHub's new-file page for it.
func TestInvalidSourceCommentLinksTheNewFilePageForAHeadWithoutADeclaration(t *testing.T) {
	n := commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: "spec_link", Repo: "octo-org/widgets", Branch: editorBranch, NoDeclaration: true}

	body := render(t, func() string { return commandtext.InvalidSourceComment(n) })

	raw, _ := editorLink(t, body)
	if want := "https://github.com/octo-org/widgets/new/feat/a%23b%25c/%C3%BCn%C3%AF?filename=.suiteward.yml"; raw != want {
		t.Errorf("the link is %q, want %q", raw, want)
	}
	if !strings.Contains(body, "[open GitHub's editor for a new file](") || strings.Contains(body, "/edit/") {
		t.Errorf("the body lacks the new-file words or has an edit link:\n%s", body)
	}
	for name, bad := range map[string]commandtext.InvalidSourceNotice{
		"on a declaration problem":       {Problem: commandtext.DeclarationProblem, NoDeclaration: true},
		"with the declaration of a link": {Problem: commandtext.LinkProblem, Path: "l", NoDeclaration: true, Declaration: []byte("version: 1\n")},
	} {
		if commandtext.ValidateInvalidSource(bad) == nil {
			t.Errorf("ValidateInvalidSource accepted NoDeclaration %s", name)
		}
	}
}
