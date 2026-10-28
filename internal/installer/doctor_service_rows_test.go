package installer

import (
	"strings"
	"testing"
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
