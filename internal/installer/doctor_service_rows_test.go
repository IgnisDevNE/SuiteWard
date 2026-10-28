package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The rows that doctor takes from the service (`suiteward check --json`) name a command of the program by its bare name, because the service cannot
// know the --settings and --token-file of the instance a person works on. A row that says which subcommands its fix names (the "commands" key) has
// them written with the flags of the instance, as every other hint is. The sentences of the service do not change, so a row of an older service,
// which has no key, shows as it always did.

const (
	notConfiguredRow = `{"name":"github.app","ok":true,"warning":true,"detail":"not configured: GitHub is off in this instance","fix":"run ` + "`suiteward install`" + ` again with --github yes (or the GitHub values), then enroll the owner"`
	noOwnerRow       = `{"name":"owner.enrolled","ok":false,"detail":"no owner is enrolled","fix":"run ` + "`suiteward enroll`" + ` where you ran ` + "`suiteward install`" + ` (as ` + "`suiteward`" + ` on an isolated host), on a real terminal"`
	setupRow         = `{"name":"protection.Acme/api","ok":false,"detail":"findings: missing-test-check","fix":"No test check is configured for this project yet: run suiteward enroll (it asks for the check), then require that check on the default branch."`
)

func TestDoctorWritesTheCommandsOfTheServiceRowsWithTheFlagsOfTheInstance(t *testing.T) {
	checks := "[" + notConfiguredRow + `,"commands":["install"]},` + noOwnerRow + `,"commands":["enroll","install"]},` + setupRow + `,"commands":["enroll"]}]`
	f, s := colocated{checkOut: checks}.build(t)
	settings, tokenFile := savedAt(t, s), coloTokenFile(t, f)
	res := doctorMain(t, f, "--settings", settings, "--token-file", tokenFile)
	install := "suiteward install --settings " + quoteArg(settings) + " --token-file " + quoteArg(tokenFile)
	enroll := "suiteward enroll --settings " + quoteArg(settings) // enroll has no token file to repeat
	for name, want := range map[string]string{
		"github.app":          "run `" + install + "` again with --github yes (or the GitHub values), then enroll the owner",
		"owner.enrolled":      "run `" + enroll + "` where you ran `" + install + "` (as `suiteward` on an isolated host), on a real terminal",
		"protection.Acme/api": "No test check is configured for this project yet: run `" + enroll + "` (it asks for the check), then require that check on the default branch.",
	} {
		if got := res[name].Fix; got != want {
			t.Errorf("%s: the fix is %q, want %q", name, got, want)
		}
	}
}

// A row of an older service has no key: nothing is written again.
func TestDoctorShowsTheRowsOfAnOlderServiceAsTheyAre(t *testing.T) {
	f, s := colocated{checkOut: "[" + notConfiguredRow + "}," + noOwnerRow + "}," + setupRow + "}]"}.build(t)
	res := doctorMain(t, f, "--settings", savedAt(t, s), "--token-file", coloTokenFile(t, f))
	for name, want := range map[string]string{"github.app": "run `suiteward install` again", "owner.enrolled": "run `suiteward enroll` where you ran `suiteward install`", "protection.Acme/api": "run suiteward enroll (it asks"} {
		if got := res[name].Fix; !strings.Contains(got, want) {
			t.Errorf("%s: the fix is %q, want it to keep %q", name, got, want)
		}
	}
}

// The rows come from another process: a key that names anything but the two commands that a fix may name, or one that the fix does not spell, leaves
// the fix as the service wrote it.
func TestDoctorIgnoresACommandsKeyThatTheFixDoesNotSpell(t *testing.T) {
	const fix = "run `suiteward uninstall --purge` now"
	rows := `[{"name":"a","ok":false,"detail":"d","fix":"` + fix + `","commands":["uninstall"]},` +
		`{"name":"b","ok":false,"detail":"d","fix":"fix the check","commands":["install"]},` +
		`{"name":"c","ok":false,"detail":"d","fix":"` + fix + `","commands":[""]}]`
	f, s := colocated{checkOut: rows}.build(t)
	res := doctorMain(t, f, "--settings", savedAt(t, s), "--token-file", coloTokenFile(t, f))
	for name, want := range map[string]string{"a": fix, "b": "fix the check", "c": fix} {
		if got := res[name].Fix; got != want {
			t.Errorf("%s: the fix is %q, want it as the service wrote it: %q", name, got, want)
		}
	}
}

// The import of the ruleset file replaces the instruction of a check only when it is the whole fix; the instruction to enroll is never one that an
// import fixes, so it stays. doctor has written that command with the flags of the instance, and a program that is run by another name has no
// "suiteward enroll" in it, so the summary reads the row's key too.
func TestTheSummaryKeepsTheEnrollInstructionWhenTheProgramIsRunByAnotherName(t *testing.T) {
	const program = "/opt/sw/ward"
	raw, err := json.Marshal([]doctorResult{protectionResult("Acme/api", protection.Facts{}, "")}) // no test check yet: the evaluator asks for enroll
	if err != nil {
		t.Fatal(err)
	}
	f, s := colocated{checkOut: string(raw)}.build(t)
	t.Chdir(t.TempDir())
	const path = "settings.yaml"
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := writeRuleset(".", "Acme/api", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}}
	env.Deps.Command = program
	var rows summaryRows
	rows.addDoctor(context.Background(), env, path, coloTokenFile(t, f), env.Deps.suggest("doctor"))
	i := slices.IndexFunc(rows, func(kv ui.KV) bool { return kv.Key == "" && strings.Contains(kv.Value, "protection.Acme/api") })
	if i < 0 {
		t.Fatalf("the summary has no warning for the repository: %+v", rows)
	}
	for _, want := range []string{"import the ruleset file", "`" + program + " enroll`"} {
		if !strings.Contains(rows[i].Value, want) {
			t.Errorf("the warning lacks %q:\n%s", want, rows[i].Value)
		}
	}
}

// A command is a whole word of the fix: the program's name and the subcommand, between backticks or standing alone. What is part of a longer command
// (one with its own flags) or of a longer word is not written again, and the fix stays as the service wrote it. Two backticked commands may share a
// backtick ("`suiteward install`suiteward install`"): the first is written, and the second, which would reuse its closing backtick, is not.
func TestWriteCommandsWritesOnlyWholeCommands(t *testing.T) {
	d := Deps{instance: instanceArgs{settings: "/s.yaml"}}
	install, enroll := "`"+d.suggest("install")+"`", "`"+d.suggest("enroll")+"`"
	for name, c := range map[string]struct {
		fix   string
		named []string
		want  string
	}{
		"between backticks":                {"run `suiteward install` now", []string{"install"}, "run " + install + " now"},
		"alone, before a space":            {"run suiteward install now", []string{"install"}, "run " + install + " now"},
		"alone, before punctuation":        {"(run suiteward enroll), then wait", []string{"enroll"}, "(run " + enroll + "), then wait"},
		"alone, at the end":                {"run suiteward install", []string{"install"}, "run " + install},
		"two commands, one pass":           {"run `suiteward enroll` where you ran `suiteward install`", []string{"enroll", "install"}, "run " + enroll + " where you ran " + install},
		"a command that is not named":      {"run `suiteward enroll` or `suiteward install`", []string{"enroll"}, "run " + enroll + " or `suiteward install`"},
		"a longer command, quoted":         {"run `suiteward install --upgrade` now", []string{"install"}, "run `suiteward install --upgrade` now"},
		"a longer command, alone":          {"run suiteward install --upgrade now", []string{"install"}, "run suiteward install --upgrade now"},
		"a longer word":                    {"the suiteward installer", []string{"install"}, "the suiteward installer"},
		"a word before it":                 {"the xsuiteward install", []string{"install"}, "the xsuiteward install"},
		"a long command and a longer word": {"run `suiteward install --upgrade` now; the suiteward installer", []string{"install"}, "run `suiteward install --upgrade` now; the suiteward installer"},
		"a shared backtick, the same one":  {"`suiteward install`suiteward install`", []string{"enroll", "install"}, install + "suiteward install`"},
		"a shared backtick, two commands":  {"`suiteward enroll`suiteward install`", []string{"enroll", "install"}, enroll + "suiteward install`"},
	} {
		if got := writing(t, d, c.fix, c.named); got != c.want {
			t.Errorf("%s: %q became %q, want %q", name, c.fix, got, c.want)
		}
	}
}

// writing is writeCommands with a panic reported as a failure of the test: the fix is the text of a row from another process, which doctor reads
// without a recover, and the install's closing summary runs doctor in the same process.
func writing(t *testing.T, d Deps, fix string, named []string) (got string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("writeCommands panics on %q: %v", fix, r)
			got = fix
		}
	}()
	return d.writeCommands(fix, named)
}

// Whatever the service sends, the fix is written without a panic, and it differs from the text only where a command stood: with the commands that
// were written turned back into their bare spelling, and the backticks that stood around them or were added left out of both, nothing else changed.
func FuzzWriteCommands(f *testing.F) {
	d := Deps{instance: instanceArgs{settings: "ZZ"}}
	named := []string{"enroll", "install"}
	for _, seed := range []string{
		"", "`", "``", "suiteward install", "suiteward enroll.", "`suiteward install`", "``suiteward install``",
		"`suiteward install`suiteward install`", "`suiteward enroll`suiteward install`", "`suiteward install`suiteward enroll`suiteward install`",
		"suiteward install suiteward install", "suiteward enroll`suiteward enroll`suiteward enroll", "run `suiteward install --upgrade` now; the suiteward installer",
		"run `suiteward enroll` where you ran `suiteward install` (as `suiteward` on an isolated host)", "(suiteward enroll)\nsuiteward install\r\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, fix string) {
		if strings.Contains(fix, "ZZ") { // the mark of what is written would be the text's own
			t.Skip()
		}
		got := writing(t, d, fix, named)
		turnedBack := strings.NewReplacer("`"+d.suggest("enroll")+"`", "suiteward enroll", "`"+d.suggest("install")+"`", "suiteward install").Replace(got)
		if strings.ReplaceAll(turnedBack, "`", "") != strings.ReplaceAll(fix, "`", "") {
			t.Errorf("%q became %q: more than commands changed", fix, got)
		}
	})
}
