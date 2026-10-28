package installer

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// Owner decision 20: the ruleset file the install writes for the import does not stay behind once the repository's protection reads as
// enforced. While the protection still needs the import, the file stays: the pages and the summary name it.

func rulesetFileExists(t *testing.T, dir, repo string) bool {
	t.Helper()
	_, err := os.Stat(rulesetPath(dir, repo))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestTheRulesetFileIsDeletedOnceTheImportWorked(t *testing.T) {
	g := newFakeGitHub(t)
	run := startRulesetRun(t, g)
	waitForText(t, run.out, "I imported it, check now")
	if !rulesetFileExists(t, run.env.SettingsDir, "acme/app") {
		t.Fatal("the file is not there while the page asks for the import")
	}
	run.answer(t, "1")
	if res := run.result(t); res.Protection["acme/app"].Kind != protection.KindEnforced {
		t.Fatalf("protection = %+v", res.Protection)
	}
	if rulesetFileExists(t, run.env.SettingsDir, "acme/app") {
		t.Error("the ruleset file stayed after the protection read as enforced")
	}
}

func TestThePlainRulesetStepDeletesTheFileOnceTheProtectionIsEnforced(t *testing.T) {
	g := newFakeGitHub(t)
	env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	if rulesetFileExists(t, env.SettingsDir, "acme/app") {
		t.Error("the ruleset file stayed after the protection read as enforced")
	}
}

func TestTheRulesetFileStaysWhileTheProtectionNeedsTheImport(t *testing.T) {
	t.Run("after a check that found it missing", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = true })
		run := startRulesetRun(t, g)
		waitForText(t, run.out, "I imported it, check now")
		run.answer(t, "1")
		waitForText(t, run.out, "missing-contract-check")
		run.answer(t, "2")
		run.result(t)
		if !rulesetFileExists(t, run.env.SettingsDir, "acme/app") {
			t.Error("the file went although the protection still needs the import")
		}
	})
	t.Run("when the check was skipped", func(t *testing.T) {
		g := newFakeGitHub(t)
		run := startRulesetRun(t, g)
		waitForText(t, run.out, "Skip, import later")
		run.answer(t, "2")
		run.result(t)
		if !rulesetFileExists(t, run.env.SettingsDir, "acme/app") {
			t.Error("the file went although nobody read the protection")
		}
	})
	t.Run("on a plain run", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = true })
		env, _ := flowEnv(t, g, existingFlags(t, nil), strings.NewReader(""), false)
		if _, err := resolve(t, env); err != nil {
			t.Fatal(err)
		}
		if !rulesetFileExists(t, env.SettingsDir, "acme/app") {
			t.Error("the file went although the protection still needs the import")
		}
	})
}

// Each repository has its own file: the one that is enforced loses it, the other keeps it.
func TestOnlyTheFileOfAnEnforcedRepositoryIsDeleted(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(2)
	g.changeRepos(func(repos []fakeRepo) {
		repos[0].ruleChecks, repos[0].ruleStrict = []fakeRuleChk{{protection.ContractCheck, fakeAppID}, {"CI / test", 0}}, true // imported
		repos[1].noRule = true                                                                                                  // not yet
	})
	flags := existingFlags(t, map[string]string{"repositories": "acme/r1,acme/r2", "test-check.acme/r1": "CI / test", "test-check.acme/r2": "CI / test"})
	delete(flags, "test-check")
	run := startRulesetRunWith(t, g, flags)
	waitForText(t, run.out, "I imported it, check now")
	run.answer(t, "1")
	waitForText(t, run.out, "missing-contract-check")
	run.answer(t, "2")
	run.result(t)
	if rulesetFileExists(t, run.env.SettingsDir, "acme/r1") {
		t.Error("the file of the enforced repository stayed")
	}
	if !rulesetFileExists(t, run.env.SettingsDir, "acme/r2") {
		t.Error("the file of the repository that is not enforced went")
	}
}

// A repository whose protection is enforced when the summary checks it (the import was made after the install skipped the check) loses
// its file there; one that is not enforced keeps it, and the summary names it.
func TestTheSummaryDeletesTheFileOfARepositoryThatIsEnforced(t *testing.T) {
	withTerminal(t, true)
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	h.first("exec -i suiteward /suiteward check --json", `[{"name":"protection.Acme/api","ok":true,"detail":"enforced","fix":""},`+
		`{"name":"protection.Acme/web","ok":false,"detail":"findings: missing-contract-check","fix":"`+rulesetFix+`"}]`)
	for _, repo := range []string{"Acme/api", "Acme/web"} {
		if _, err := writeRuleset(h.dir, repo, []byte("{}\n")); err != nil {
			t.Fatal(err)
		}
	}
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	if err := Install(liveContext(t), colocatedArgs(h), env); err != nil {
		t.Fatal(err)
	}
	if rulesetFileExists(t, h.dir, "Acme/api") {
		t.Error("the file of the repository that is enforced stayed")
	}
	if !rulesetFileExists(t, h.dir, "Acme/web") {
		t.Error("the file of the repository that is not enforced went")
	}
	if tail := c.tail(); !strings.Contains(tail, rulesetPath(h.dir, "Acme/web")) {
		t.Errorf("the summary does not name the file that is left:\n%s", tail)
	}
}
