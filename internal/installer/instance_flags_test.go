package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// On a computer with a second instance, a person who copies a command that the installer or doctor suggests runs it against the instance that its
// flags name: a command without them reaches the default instance, and an upgrade, an uninstall or a purge of the wrong one is not undone. So a
// suggested command carries the flags of the running command that name the instance, right after its subcommand.

// doctorMain runs `suiteward doctor --json` with args and returns the results by name.
func doctorMain(t *testing.T, f *fakeEngine, args ...string) map[string]doctorResult {
	t.Helper()
	var out, stderr bytes.Buffer
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &out}}
	if code := Main(context.Background(), append([]string{"doctor", "--json"}, args...), env, &stderr); code != 0 {
		t.Fatalf("doctor exited %d: %s%s", code, stderr.String(), out.String())
	}
	var list []doctorResult
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatalf("doctor --json printed %q: %v", out.String(), err)
	}
	byName := map[string]doctorResult{}
	for _, r := range list {
		byName[r.Name] = r
	}
	return byName
}

func TestDoctorSuggestsTheInstallOfTheInstanceItWasGiven(t *testing.T) {
	t.Run("a second instance: its settings file and token file follow the subcommand", func(t *testing.T) {
		f, s := colocated{}.build(t)
		writePin(t, f, "suiteward-executor") // a pin that no release's SHA256SUMS made: the fix names the install that checks it
		settings, token := savedAt(t, s), coloTokenFile(t, f)
		fix := doctorMain(t, f, "--settings", settings, "--token-file", token)["executor.binary"].Fix
		want := "To check it, run `suiteward install --settings " + quoteArg(settings) + " --token-file " + quoteArg(token) +
			" --upgrade --sha256sums <the release's SHA256SUMS file>` with the program from that release."
		if fix != want {
			t.Errorf("the fix is %q, want %q", fix, want)
		}
	})
	t.Run("the default instance: the command reads as it always did", func(t *testing.T) {
		f, s := colocated{}.build(t)
		settings, err := DefaultSettingsPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := SaveSettings(settings, s); err != nil {
			t.Fatal(err)
		}
		writePin(t, f, "suiteward-executor")
		fix := doctorMain(t, f)["executor.binary"].Fix
		want := "To check it, run `suiteward install --upgrade --sha256sums <the release's SHA256SUMS file>` with the program from that release."
		if fix != want {
			t.Errorf("the fix is %q, want %q", fix, want)
		}
	})
}
