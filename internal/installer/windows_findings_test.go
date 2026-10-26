package installer

import (
	"context"
	"path/filepath"
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

// upgradeOnAPC runs install --upgrade of the harness on the PC of pc, whose running program is reached by its path (swCmd), for image.
func upgradeOnAPC(t *testing.T, pc *aPC, image string) string {
	t.Helper()
	u := newUpgradeHarness(t, nil)
	u.f.env["LOCALAPPDATA"] = pc.local
	env := u.env()
	env.Self, env.Version = &pc.self, pc.version
	env.Deps.Command = swCmd
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", image, "--yes"}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	return u.out.String()
}

// WIN-6: an upgrade that updated the copy of the program names it as a new terminal finds it, suiteward, in the hints that follow, and not
// by the path of the build that ran. The name is promised only where the copy answers to it: the folder is on the PATH of the user, and the
// shell of this terminal finds either nothing yet (it started before the entry was added) or the copy itself, not another suiteward.
func TestAnUpgradeThatUpdatedTheCopyNamesTheProgramSuiteward(t *testing.T) {
	// onPath puts the folder of the copy on the PATH of the user, as the install that made the copy did.
	onPath := func(pc *aPC) { pc.path.value += ";" + pc.dir() }
	for name, image := range map[string]string{"nothing to upgrade": oldImage, "an upgrade": testImage} {
		for when, found := range map[string]func(pc *aPC) string{
			"the shell finds the copy":                              func(pc *aPC) string { return pc.target() },
			"this terminal started before the PATH entry was added": func(*aPC) string { return "" },
		} {
			t.Run(name+"/"+when, func(t *testing.T) {
				pc := newPC(t)
				writeText(t, pc.target(), "OLD BUILD")
				onPath(pc)
				pc.found = found(pc)
				out := upgradeOnAPC(t, pc, image)
				if readText(t, pc.target()) != "NEW BUILD" || !strings.Contains(out, "`suiteward doctor") {
					t.Errorf("the copy holds %q and the output has no hint that names suiteward:\n%s", readText(t, pc.target()), out)
				}
				if strings.Contains(out, swCmd) {
					t.Errorf("a hint still names the program by the path of the build:\n%s", out)
				}
			})
		}
		t.Run(name+"/the shell finds another suiteward", func(t *testing.T) {
			pc := newPC(t)
			writeText(t, pc.target(), "OLD BUILD")
			onPath(pc)
			pc.found = filepath.Join(t.TempDir(), "suiteward.exe") // another program: the copy does not answer to the name
			if out := upgradeOnAPC(t, pc, image); !strings.Contains(out, "`"+swCmd+" doctor") {
				t.Errorf("the hint does not name the program by its path, which is what runs it:\n%s", out)
			}
		})
		t.Run(name+"/the PATH of the user lacks the folder", func(t *testing.T) {
			pc := newPC(t)
			writeText(t, pc.target(), "OLD BUILD")
			if out := upgradeOnAPC(t, pc, image); !strings.Contains(out, "`"+swCmd+" doctor") {
				t.Errorf("the hint does not name the program by its path, as no name reaches the copy:\n%s", out)
			}
		})
		t.Run(name+"/there is no copy", func(t *testing.T) {
			pc := newPC(t)
			if out := upgradeOnAPC(t, pc, image); !strings.Contains(out, "`"+swCmd+" doctor") {
				t.Errorf("the hint does not name the program by its path:\n%s", out)
			}
		})
	}
}
