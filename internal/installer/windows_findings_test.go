package installer

import (
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// The small findings of the live run on Windows (M1.15, WIN-n): the texts and screens that read wrong.

// WIN-8: "did not see it, keep it or log it" reads as an order to keep or log the secret.
func TestTheEndOfSetupSaysTheInstallerDidNotSeeKeepOrLogTheSecret(t *testing.T) {
	scr, _, out := fullScreenOn(t, liveContext(t), strings.NewReader(""))
	t.Cleanup(func() { _ = scr.Close() })
	if err := afterSetup(scr, "suiteward", nil); err != nil {
		t.Fatal(err)
	}
	if !shows(out.String(), "the installer did not see, keep or log it") || shows(out.String(), "see it, keep it") {
		t.Errorf("the page of the end of setup is:\n%s", out.String())
	}
}

// WIN-1: the question of the hidden paste is one clause: the screen adds "(input is hidden)", so the label has no parenthesis of its own.
func TestTheHiddenPasteQuestionIsOneClauseWithOneParenthesis(t *testing.T) {
	for _, mode := range manifestModes {
		t.Run(mode.name, func(t *testing.T) {
			hidden := newHiddenReader(t)
			env, out := mode.env(t, strings.NewReader(""), hidden.read)
			env.PasteAfter = 40 * time.Millisecond
			done := startManifest(env, "acme")
			base := waitForListener(t, out)
			action, _ := fetchForm(t, base)
			hidden.readsReach(t, 1, out)
			want := "No answer from GitHub yet. Paste the address, or press Enter if the browser says SuiteWard received the answer (input is hidden): "
			if !shows(out.String(), want) {
				t.Errorf("the question is not %q:\n%s", want, out.String())
			}
			if strings.Contains(flat(out.String()), ") (") {
				t.Errorf("the question has two parentheses in a row:\n%s", out.String())
			}
			callback(t, base, "browsercode", stateOf(t, action))
			hidden.send("")
			if res := awaitFor(t, done, 3*time.Second); res.err != nil || res.code != "browsercode" {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

// WIN-2: the page of the ruleset pause gives the exact link of each repository on its row, never a template to fill in.
func TestTheRulesetPageGivesTheExactLinkOfEachRepositoryOnItsRow(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(2)
	names := []string{"acme/r1", "acme/r2"}
	flags := existingFlags(t, map[string]string{"repositories": strings.Join(names, ",")})
	delete(flags, "test-check")
	for _, n := range names {
		flags["test-check."+n] = "CI / test"
	}
	run := startRulesetRunWith(t, g, flags)
	waitForText(t, run.out, "I imported it, check now")
	frame := lastFrame(run.out.String())
	for _, n := range names {
		if link := "https://github.example/" + n + "/settings/rules"; !shows(frame, link) {
			t.Errorf("the page lacks the link %s:\n%s", link, frame)
		}
	}
	if shows(frame, "<name>") {
		t.Errorf("the page still has a template for the link:\n%s", frame)
	}
	run.answer(t, "2")
	run.result(t)
}

// WIN-3: what is missing reads in words next to each code.
func TestTheRulesetPageSaysInWordsWhatIsMissing(t *testing.T) {
	g := newFakeGitHub(t)
	g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = true })
	run := startRulesetRun(t, g)
	waitForText(t, run.out, "I imported it, check now")
	run.answer(t, "1")
	waitForText(t, run.out, "missing-contract-check")
	frame := lastFrame(run.out.String())
	for _, want := range []string{"missing-contract-check (SuiteWard / Contract is not required)", "missing-test-check (the test check is not required)"} {
		if !shows(frame, want) {
			t.Errorf("the page lacks %q:\n%s", want, frame)
		}
	}
	run.answer(t, "2")
	run.result(t)
}

func TestEveryFindingOfAProtectionHasItsWords(t *testing.T) {
	for code, want := range map[protection.Code]string{
		protection.MissingContractCheck: "missing-contract-check (SuiteWard / Contract is not required)",
		protection.MissingTestCheck:     "missing-test-check (the test check is not required)",
		protection.NotUpToDate:          "not-up-to-date (branches need not be up to date)",
		protection.AdminBypass:          "admin-bypass (administrators can bypass it)",
	} {
		if got := unfinishedText(protection.Result{Kind: protection.KindFindings, Findings: []protection.Code{code}}); got != want {
			t.Errorf("%s reads %q, want %q", code, got, want)
		}
	}
	both := protection.Result{Kind: protection.KindFindings, Findings: []protection.Code{protection.MissingContractCheck, protection.MissingTestCheck}}
	if got, want := unfinishedText(both), "missing-contract-check (SuiteWard / Contract is not required), missing-test-check (the test check is not required)"; got != want {
		t.Errorf("two findings read %q, want %q", got, want)
	}
}
