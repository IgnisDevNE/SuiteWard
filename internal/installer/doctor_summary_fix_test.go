package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// Ctrl-C during the checks that end an install or an upgrade ends the command as interrupted, as it ends `suiteward doctor`: the checks
// that it cancelled are not findings about the instance.
func TestCtrlCDuringTheClosingChecksEndsTheCommandAsInterrupted(t *testing.T) {
	cancelAtTheInstanceCheck := func(cancel context.CancelFunc, env *Env) {
		inner := env.Deps.Exec
		env.Deps.Exec = func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
			if slices.Contains(args, "/suiteward") { // the last check of doctor
				cancel()
			}
			return inner(ctx, name, args, stdin)
		}
	}
	run := func(t *testing.T, args []string, env Env) (*termConsole, int, string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		cancelAtTheInstanceCheck(cancel, &env)
		c := newTermConsole(&bytes.Buffer{})
		env.OpenScreen = c.open
		var stderr bytes.Buffer
		return c, Main(ctx, args, env, &stderr), stderr.String()
	}
	check := func(t *testing.T, c *termConsole, code int, stderr string) {
		t.Helper()
		if code != 130 || !strings.Contains(stderr, "interrupted") {
			t.Errorf("exit %d with stderr %q, want 130 and a line that says interrupted", code, stderr)
		}
		tail := c.tail()
		for _, want := range []string{"Doctor: interrupted\n", "Check again: suiteward doctor"} {
			if !strings.Contains(tail, want) {
				t.Errorf("the summary lacks %q:\n%s", want, tail)
			}
		}
		for _, not := range []string{"FAIL ", "WARN ", "failures", "context canceled"} {
			if strings.Contains(tail, not) {
				t.Errorf("the summary reports the checks that the Ctrl-C cancelled (%q):\n%s", not, tail)
			}
		}
	}
	t.Run("an install", func(t *testing.T) {
		h := newHarness(t, "linux")
		args := append([]string{"install"}, "--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage, "--github-app-id", "none")
		c, code, stderr := run(t, args, h.newEnv())
		check(t, c, code, stderr)
	})
	t.Run("an upgrade", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		c, code, stderr := run(t, append([]string{"install"}, upgradeArgs(u)...), u.env())
		check(t, c, code, stderr)
	})
}

// A Ctrl-C that came before the checks (at the Enter that ends setup, or at the wait for the service) only skips them, as it skips the wait:
// the work is done, and the exit code is the command's.
func TestCtrlCBeforeTheClosingChecksOnlySkipsThem(t *testing.T) {
	withTerminal(t, true)
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	env.Deps.Interactive = func(context.Context, string, []string) error {
		cancel() // Ctrl-C while the owner reads the secret
		return nil
	}
	var stderr bytes.Buffer
	if code := Main(ctx, append([]string{"install"}, colocatedArgs(h)...), env, &stderr); code != 0 {
		t.Fatalf("exit %d with stderr %q: a Ctrl-C after setup only skips what remains", code, stderr.String())
	}
	tail := c.tail()
	if !strings.Contains(tail, "Doctor: skipped (Ctrl-C)\n") || strings.Contains(tail, "Doctor: interrupted") || strings.Contains(tail, "failures") {
		t.Errorf("the summary reports the checks that were never run, or says they were interrupted:\n%s", tail)
	}
}

// protectionResult is the result of one repository's protection as the evaluator judges it and `suiteward check` words it.
func protectionResult(repo string, facts protection.Facts, testCheck string) doctorResult {
	res := protection.Evaluate(facts, 1234, testCheck)
	codes := make([]string, len(res.Findings))
	for i, c := range res.Findings {
		codes[i] = string(c)
	}
	return doctorResult{Name: "protection." + repo, Detail: string(res.Kind) + ": " + strings.Join(codes, ", "), Fix: strings.Join(res.Instructions, " ")}
}

// The ruleset file fixes what the ruleset requires: the contract check, the test check and that branches are up to date. It replaces the
// instruction of the check only when that is all that is wrong; anything else the evaluator says stays, with the import in front of it.
func TestTheImportOfTheRulesetReplacesTheInstructionOnlyWhenItIsTheWholeFix(t *testing.T) {
	const test = "CI / test"
	contract := protection.Check{Context: protection.ContractCheck, SourceID: 1234}
	dir := t.TempDir()
	file, err := writeRuleset(dir, "Acme/api", []byte("{}\n"))
	if err != nil {
		t.Fatal(err)
	}
	imports := "import the ruleset file " + file

	for name, tc := range map[string]struct {
		facts     protection.Facts
		testCheck string
		replaced  bool     // the import is the whole fix
		keeps     []string // sentences of the evaluator that an import does not fix
	}{
		"both checks missing":   {facts: protection.Facts{}, testCheck: test, replaced: true},
		"not up to date":        {facts: protection.Facts{Rules: []protection.Rule{{Checks: []protection.Check{contract, {Context: test}}}}}, testCheck: test, replaced: true},
		"administrators bypass": {facts: protection.Facts{Classic: protection.Classic{State: protection.ClassicPresent}}, testCheck: test, keeps: []string{"Enable enforcement for administrators"}},
		"no test check set up":  {facts: protection.Facts{}, testCheck: "", keeps: []string{"run suiteward enroll"}},
		"a ruleset to look at":  {facts: protection.Facts{FromRuleset: true}, testCheck: test, keeps: []string{"check in GitHub that the bypass list is empty"}},
	} {
		t.Run(name, func(t *testing.T) {
			r := protectionResult("Acme/api", tc.facts, tc.testCheck)
			got := importRulesetFix(r, dir)
			if !strings.HasPrefix(got, imports) || !strings.Contains(got, "Import a ruleset") {
				t.Fatalf("the fix does not start with the import of %s:\n%s", file, got)
			}
			if tc.replaced {
				if strings.Contains(got, "Require the check") || strings.Contains(got, "Require branches") {
					t.Errorf("the fix repeats the instruction that the import satisfies:\n%s", got)
				}
				return
			}
			for _, want := range append([]string{r.Fix}, tc.keeps...) {
				if !strings.Contains(got, want) {
					t.Errorf("the fix lost %q:\n%s", want, got)
				}
			}
		})
	}

	// A file that is not there changes nothing.
	r := protectionResult("Acme/web", protection.Facts{}, test)
	if got := importRulesetFix(r, dir); got != r.Fix {
		t.Errorf("a repository whose ruleset file does not exist got %q", got)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}

// The isolated way back after a failed migration is scoped to this instance, as the closing line of the upgrade is: the tier and the settings
// file of the run.
func TestTheWayBackAfterAFailedMigrationOnTheIsolatedTierNamesTheTierAndTheSettings(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.failOn["systemctl --user restart suiteward-migrate.service"] = errors.New("migrate failed")
	err := u.upgrade("--image", testDigest, "--yes")
	if err == nil {
		t.Fatal("the upgrade went on after the migration failed")
	}
	want := " --tier isolated --settings " + quoteArg(settingsFile(u.home)) + "` with that kit"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the way back is not scoped to this instance, want %q:\n%v", want, err)
	}
}
