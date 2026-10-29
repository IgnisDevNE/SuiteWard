package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// ADR 0029, section 7: the commands that work on an installation ask which one when there are several, and a choice is not a flag that was typed.

// choiceDeps is the machine of the question: the files it names are the ones of the test.
// noSettingsWayOut is what the refusal adds for a stack that has no settings file (a first install that failed before it saved them, while other
// installations are listed): such a stack is named by its token file and its engine.
const noSettingsWayOut = " A stack that has no settings file is named with `--token-file <file>` plus `--runtime` and `--connection`."

func choiceDeps(t *testing.T) Deps {
	t.Helper()
	f := newFake(runtime.GOOS)
	f.env["LOCALAPPDATA"] = t.TempDir()
	return f.deps()
}

// installationsOf is a computer with the default installation (its settings file, no entry) and a second one that an install listed, with
// files of its own.
type installationsOf struct {
	def, second, token string
}

func twoInstallations(t *testing.T) installationsOf {
	t.Helper()
	useDefaultConfigDir(t)
	s := colocatedSettings()
	s.Connection, s.HTTPPort, s.MCPPort = "pm", 27081, 27091
	other := colocatedSettings()
	other.Connection, other.HTTPPort, other.MCPPort = "pm2", 28081, 28091
	i := installationsOf{def: defaultSettingsOf(t, s), second: settingsOf(t, other), token: filepath.Join(t.TempDir(), "mcp-token")}
	if err := recordInstallation(runtime.GOOS, i.second, i.token, other, "5e6f7a8b"); err != nil {
		t.Fatal(err)
	}
	return i
}

func TestSeveralInstallationsAndNoFlagShowTheScreenAndTheChoiceNamesTheCommandsOfTheRun(t *testing.T) {
	i := twoInstallations(t)
	r, c := keyResolver(t, false, nil, nil, "\x1b[B\x1b[B\r") // no row is highlighted at first: the first down is the default installation, the second is the next
	if err := r.chooseInstallation(choiceDeps(t)); err != nil {
		t.Fatal(err)
	}
	page := c.out.String()
	// Above the choice, the full text of each installation: a row of the choice cuts a long path, and its description with it.
	for _, want := range []string{
		"Which installation?", "This computer has 2 SuiteWard installations. Choose the one this command is for.",
		"The default installation. Instance not known yet, on this computer, with podman (connection pm), ports 27081 and 27091.",
		"Instance 5e6f7a8b, on this computer, with podman (connection pm2), ports 28081 and 28091.",
	} {
		if !shows(page, want) {
			t.Errorf("the screen lacks %q:\n%s", want, page)
		}
	}
	if want := (instanceArgs{settings: i.second, tokenFile: i.token}); r.instance() != want {
		t.Errorf("the instance of the run is %+v, want %+v", r.instance(), want)
	}
	hint := Deps{instance: r.instance()}.suggest("doctor")
	if want := "suiteward doctor --settings " + quoteArg(i.second) + " --token-file " + quoteArg(i.token); hint != want {
		t.Errorf("a hint reads %q, want %q", hint, want)
	}
	if path, isDefault, err := settingsPathFrom(r); err != nil || path != i.second || isDefault {
		t.Errorf("the settings file is %q (default %v), %v: want the chosen one", path, isDefault, err)
	}
	if _, given := r.given("settings"); given || !r.asks("tier") {
		t.Errorf("a chosen installation is not a typed one: settings given %v, tier asked %v", given, r.asks("tier"))
	}
}

func TestTheDefaultInstallationChosenSetsNothing(t *testing.T) {
	i := twoInstallations(t)
	r, _ := keyResolver(t, false, nil, nil, "\x1b[B\r") // the first row is the default installation
	if err := r.chooseInstallation(choiceDeps(t)); err != nil {
		t.Fatal(err)
	}
	if len(r.chosen) != 0 || r.instance() != (instanceArgs{}) {
		t.Errorf("the default installation set %v (instance %+v)", r.chosen, r.instance())
	}
	if path, isDefault, err := settingsPathFrom(r); err != nil || path != i.def || !isDefault {
		t.Errorf("the settings file is %q (default %v), %v: want the default one", path, isDefault, err)
	}
	if hint := (Deps{instance: r.instance()}).suggest("doctor"); hint != "suiteward doctor" {
		t.Errorf("a hint of the default installation reads %q, want no flag", hint)
	}
}

func TestAPlainScreenListsTheInstallationsAndAsksForTheTypedSettingsFile(t *testing.T) {
	i := twoInstallations(t)
	r, out := plainResolver(t, nil, nil, i.second+"\n")
	if err := r.chooseInstallation(choiceDeps(t)); err != nil {
		t.Fatal(err)
	}
	t.Logf("the plain transcript:\n%s", out.String())
	for _, want := range []string{
		"This computer has 2 SuiteWard installations:",
		"\n  " + i.def + "\n    The default installation. ",
		"\n  " + i.second + "\n    Instance 5e6f7a8b, on this computer, with podman (connection pm2), ports 28081 and 28091.\n",
		"Installation (type the settings file of one of them): ", // no default: Enter never picks an installation
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the transcript lacks %q:\n%s", want, out.String())
		}
	}
	if r.chosen["settings"] != i.second || r.chosen["token-file"] != i.token {
		t.Errorf("chosen is %v, want the second installation", r.chosen)
	}
	// A mistyped settings file says what to do.
	r, _ = plainResolver(t, nil, nil, "/nowhere/settings.yaml\n")
	want := "is not the settings file of one of the installations listed above. Nothing was changed. Run the command again and type one of the settings files listed"
	if err := r.chooseInstallation(choiceDeps(t)); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a settings file that is not listed: err %v, want it to say %q", err, want)
	}
}

// Enter never picks an installation, and above all not the main one: nothing is preselected, an empty line is asked again, and an input that ends
// with no answer is an error.
func TestEnterNeverPicksAnInstallation(t *testing.T) {
	i := twoInstallations(t)
	t.Run("a plain screen asks again", func(t *testing.T) {
		r, out := plainResolver(t, nil, nil, "\n\n"+i.second+"\n")
		if err := r.chooseInstallation(choiceDeps(t)); err != nil || r.chosen["settings"] != i.second {
			t.Fatalf("err %v, chosen %v: want the second installation after two empty lines", err, r.chosen)
		}
		if got, want := strings.Count(out.String(), "Type the settings file of one of the installations listed above."), 2; got != want {
			t.Errorf("an empty line was explained %d times, want %d:\n%s", got, want, out.String())
		}
	})
	t.Run("a plain screen whose input ends with no answer", func(t *testing.T) {
		r, _ := plainResolver(t, nil, nil, "\n")
		if err := r.chooseInstallation(choiceDeps(t)); err == nil || len(r.chosen) != 0 {
			t.Errorf("err %v, chosen %v: want an error and nothing chosen", err, r.chosen)
		}
	})
	t.Run("a full screen refuses an Enter before a row is highlighted", func(t *testing.T) {
		r, c := keyResolver(t, false, nil, nil, "\r")
		if err := r.chooseInstallation(choiceDeps(t)); err == nil || len(r.chosen) != 0 {
			t.Errorf("err %v, chosen %v: want an error and nothing chosen", err, r.chosen)
		}
		if !strings.Contains(c.out.String(), "Pick one with the arrows.") {
			t.Errorf("the screen did not say to pick one:\n%s", c.out.String())
		}
	})
}

// A row says which installation it is: the instance, where it runs, the engine and the two ports; the default one says so.
func TestARowSaysWhatTheInstallationIs(t *testing.T) {
	for name, c := range map[string]struct {
		c    candidate
		want string
	}{
		"the default one": {
			candidate{installation{Tier: config.TierCoLocated, Runtime: RuntimePodman, Connection: "pm", HTTPPort: 27081, MCPPort: 27091}, true},
			"The default installation. Instance not known yet, on this computer, with podman (connection pm), ports 27081 and 27091.",
		},
		"another one": {
			candidate{installation{Tier: config.TierCoLocated, Runtime: RuntimeDocker, Connection: "desktop", Instance: "5e6f7a8b", HTTPPort: 28081, MCPPort: 28091}, false},
			"Instance 5e6f7a8b, on this computer, with docker (connection desktop), ports 28081 and 28091.",
		},
		"on a separate server": {
			candidate{installation{Tier: config.TierIsolated, Runtime: RuntimePodman, Instance: "5e6f7a8b", HTTPPort: 27081, MCPPort: 27091}, false},
			"Instance 5e6f7a8b, on a separate server, with podman, ports 27081 and 27091.",
		},
	} {
		if got := c.c.describe(); got != c.want {
			t.Errorf("%s: the row says %q, want %q", name, got, c.want)
		}
	}
}

// The question has no key of its own: a flag or an answers-file key of its name is not an answer to it, so no key of the answers file changes meaning.
func TestNoFlagAndNoAnswersKeyAnswersTheQuestionWhichInstallation(t *testing.T) {
	i := twoInstallations(t)
	for name, c := range map[string]struct{ flags, answers map[string]string }{
		"a flag":    {map[string]string{"installation": i.second}, nil},
		"an answer": {nil, map[string]string{"installation": i.second}},
		"both":      {map[string]string{"installation": i.second}, map[string]string{"installation": i.second}},
	} {
		r, out := plainResolver(t, c.flags, c.answers, i.def+"\n") // the default installation, typed
		if err := r.chooseInstallation(choiceDeps(t)); err != nil || len(r.chosen) != 0 {
			t.Errorf("%s: err %v, chosen %v: want the question asked and the default installation taken", name, err, r.chosen)
		}
		if !strings.Contains(out.String(), "Installation (type the settings file of one of them)") {
			t.Errorf("%s: the question was not asked:\n%s", name, out.String())
		}
	}
	r, c := keyResolver(t, false, nil, map[string]string{"installation": i.second}, "\x1b[B\r")
	if err := r.chooseInstallation(choiceDeps(t)); err != nil || len(r.chosen) != 0 || !strings.Contains(c.out.String(), "Which installation?") {
		t.Errorf("on a full screen: err %v, chosen %v, want the page and the default installation\n%s", err, r.chosen, c.out.String())
	}
}

// plainResolver is a resolver on a terminal with a plain screen, which types input.
func plainResolver(t *testing.T, flags, answers map[string]string, input string) (*Resolver, *bytes.Buffer) {
	t.Helper()
	term, out := fakeTerminal(true, input)
	return newTestResolver(t, flags, answers, term), out
}

func TestSeveralInstallationsWithNoTerminalRefuseAndNameTheFlags(t *testing.T) {
	i := twoInstallations(t)
	term, out := fakeTerminal(false, "")
	r := newTestResolver(t, nil, nil, term)
	err := r.chooseInstallation(choiceDeps(t))
	want := "This computer has several SuiteWard installations (" + i.def + ", " + i.second + "). Say which one with `--settings <file>` and `--token-file <file>`." + noSettingsWayOut
	if err == nil || err.Error() != want {
		t.Errorf("the refusal is %v, want %q", err, want)
	}
	if out.Len() != 0 || len(r.chosen) != 0 {
		t.Errorf("a refusal asked or chose something: %q, %v", out.String(), r.chosen)
	}
}

func TestOneInstallationIsUsedAndTheLineSaysWhich(t *testing.T) {
	t.Run("the default one", func(t *testing.T) {
		useDefaultConfigDir(t)
		def := defaultSettingsOf(t, colocatedSettings())
		for _, terminal := range []bool{false, true} {
			term, out := fakeTerminal(terminal, "")
			r := newTestResolver(t, nil, nil, term)
			if err := r.chooseInstallation(choiceDeps(t)); err != nil {
				t.Fatal(err)
			}
			if want := "This computer has one listed SuiteWard installation, so the command uses it. Its settings file is " + def + ".\n"; out.String() != want {
				t.Errorf("terminal %v: the output is %q, want %q", terminal, out.String(), want)
			}
			if len(r.chosen) != 0 {
				t.Errorf("the default installation set %v", r.chosen)
			}
		}
	})
	t.Run("another one, with no default settings file", func(t *testing.T) {
		useDefaultConfigDir(t)
		s := colocatedSettings()
		second, token := settingsOf(t, s), filepath.Join(t.TempDir(), "mcp-token")
		if err := recordInstallation(runtime.GOOS, second, token, s, ""); err != nil {
			t.Fatal(err)
		}
		term, out := fakeTerminal(false, "")
		r := newTestResolver(t, nil, nil, term)
		if err := r.chooseInstallation(choiceDeps(t)); err != nil {
			t.Fatal(err)
		}
		if want := "This computer has one listed SuiteWard installation, so the command uses it. Its settings file is " + second + ".\n"; out.String() != want {
			t.Errorf("the output is %q, want %q", out.String(), want)
		}
		if r.chosen["settings"] != second || r.chosen["token-file"] != token {
			t.Errorf("chosen is %v, want that installation", r.chosen)
		}
	})
	t.Run("none: nothing is said, and the command finds its own refusal", func(t *testing.T) {
		useDefaultConfigDir(t)
		term, out := fakeTerminal(true, "")
		r := newTestResolver(t, nil, nil, term)
		if err := r.chooseInstallation(choiceDeps(t)); err != nil || out.Len() != 0 || len(r.chosen) != 0 {
			t.Errorf("err %v, output %q, chosen %v: want nothing", err, out.String(), r.chosen)
		}
	})
}

func TestAStaleEntryIsSkippedAndCounted(t *testing.T) {
	i := twoInstallations(t)
	gone := settingsOf(t, colocatedSettings())
	if err := recordInstallation(runtime.GOOS, gone, filepath.Join(t.TempDir(), "mcp-token"), colocatedSettings(), ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	t.Run("on the screen", func(t *testing.T) {
		r, c := keyResolver(t, false, nil, nil, "\x1b[B\r")
		if err := r.chooseInstallation(choiceDeps(t)); err != nil {
			t.Fatal(err)
		}
		if want := "1 listed installation was skipped: its settings file is gone."; !strings.Contains(c.out.String(), want) {
			t.Errorf("the screen lacks %q:\n%s", want, c.out.String())
		}
		if strings.Contains(c.out.String(), gone) {
			t.Errorf("the stale entry is listed:\n%s", c.out.String())
		}
	})
	t.Run("in plain lines", func(t *testing.T) {
		r, out := plainResolver(t, nil, nil, i.def+"\n")
		if err := r.chooseInstallation(choiceDeps(t)); err != nil {
			t.Fatal(err)
		}
		if want := "(1 listed installation was skipped: its settings file is gone.)"; !strings.Contains(out.String(), want) {
			t.Errorf("the transcript lacks %q:\n%s", want, out.String())
		}
	})
	t.Run("one live installation and a stale one is one candidate", func(t *testing.T) {
		if err := os.Remove(i.second); err != nil {
			t.Fatal(err)
		}
		term, out := fakeTerminal(false, "")
		r := newTestResolver(t, nil, nil, term)
		if err := r.chooseInstallation(choiceDeps(t)); err != nil || !strings.Contains(out.String(), "one listed SuiteWard installation") {
			t.Errorf("err %v, output %q: want the default one used", err, out.String())
		}
	})
}

// With several installations, the settings file and the token file are what name one: a flag of either answers the question, nothing is asked,
// refused or said, and nothing is chosen. An answers-file key does the same, except settings, which only the command line gives.
func TestAnInstallationFlagIsNeverAskedAbout(t *testing.T) {
	twoInstallations(t)
	for _, key := range []string{"settings", "token-file"} {
		for name, give := range map[string]func(string) (flags, answers map[string]string){
			"a flag":    func(v string) (map[string]string, map[string]string) { return map[string]string{key: v}, nil },
			"an answer": func(v string) (map[string]string, map[string]string) { return nil, map[string]string{key: v} },
			"flag+answer": func(v string) (map[string]string, map[string]string) {
				return map[string]string{key: v}, map[string]string{key: v}
			},
		} {
			if name == "an answer" && key == "settings" {
				continue
			}
			for _, terminal := range []bool{false, true} {
				flags, answers := give("x")
				term, out := fakeTerminal(terminal, "")
				r := newTestResolver(t, flags, answers, term)
				if err := r.chooseInstallation(choiceDeps(t)); err != nil || out.Len() != 0 || len(r.chosen) != 0 {
					t.Errorf("%s %s (terminal %v): err %v, output %q, chosen %v: want nothing", name, key, terminal, err, out.String(), r.chosen)
				}
			}
		}
	}
	// --settings is read of the command line only: an answers-file key of that name does not name an installation.
	term, _ := fakeTerminal(false, "")
	r := newTestResolver(t, nil, map[string]string{"settings": "/x"}, term)
	if err := r.chooseInstallation(choiceDeps(t)); err == nil || !strings.Contains(err.Error(), "several SuiteWard installations") {
		t.Errorf("an answers-file settings key answered the question: %v", err)
	}
}

// The tier, the runtime and the connection do not name an installation among several: a script that gave only one of them used to get the default
// one, which may be another installation than the one it meant. They are asked about, and refused where nobody can be asked.
func TestOnlyTheSettingsFileAndTheTokenFileAnswerTheQuestionAmongSeveral(t *testing.T) {
	i := twoInstallations(t)
	for _, key := range []string{"tier", "runtime", "connection"} {
		for name, give := range map[string]func(string) (flags, answers map[string]string){
			"a flag":    func(v string) (map[string]string, map[string]string) { return map[string]string{key: v}, nil },
			"an answer": func(v string) (map[string]string, map[string]string) { return nil, map[string]string{key: v} },
		} {
			flags, answers := give("x")
			r, out := plainResolver(t, flags, answers, i.second+"\n")
			if err := r.chooseInstallation(choiceDeps(t)); err != nil || r.chosen["settings"] != i.second || !strings.Contains(out.String(), "SuiteWard installations") {
				t.Errorf("%s %s on a terminal: err %v, chosen %v: want the question asked and answered", name, key, err, r.chosen)
			}
			term, _ := fakeTerminal(false, "")
			if err := newTestResolver(t, flags, answers, term).chooseInstallation(choiceDeps(t)); err == nil || !strings.Contains(err.Error(), "several SuiteWard installations") {
				t.Errorf("%s %s with no terminal: %v, want the refusal", name, key, err)
			}
		}
	}
}

// A script never waits on the question and never gets the default silently: --yes or --answers with several installations and no flag that names one
// refuse as a run with no terminal does, even on a terminal. One installation is still used, and a flag that names one still answers.
func TestAnUnattendedRunRefusesSeveralInstallationsAndNeverAsks(t *testing.T) {
	i := twoInstallations(t)
	for name, flags := range map[string]map[string]string{
		"--yes":     {"yes": "true"},
		"--answers": {"answers": "a.yaml"},
	} {
		r, out := plainResolver(t, flags, nil, i.second+"\n")
		err := r.chooseInstallation(choiceDeps(t))
		want := "This computer has several SuiteWard installations (" + i.def + ", " + i.second + "). Say which one with `--settings <file>` and `--token-file <file>`." + noSettingsWayOut
		if err == nil || err.Error() != want || out.Len() != 0 || len(r.chosen) != 0 {
			t.Errorf("%s on a terminal: err %v, output %q, chosen %v: want the refusal and no question", name, err, out.String(), r.chosen)
		}
		withFlag := map[string]string{"token-file": i.token}
		for k, v := range flags {
			withFlag[k] = v
		}
		r, out = plainResolver(t, withFlag, nil, "")
		if err := r.chooseInstallation(choiceDeps(t)); err != nil || out.Len() != 0 {
			t.Errorf("%s with a flag that names an installation: err %v, output %q: want nothing", name, err, out.String())
		}
	}
	r, _ := plainResolver(t, map[string]string{"yes": "false"}, nil, i.second+"\n")
	if err := r.chooseInstallation(choiceDeps(t)); err != nil || r.chosen["settings"] != i.second {
		t.Errorf("--yes=false is not a script: err %v, chosen %v, want the question asked", err, r.chosen)
	}
	if err := os.Remove(i.second); err != nil {
		t.Fatal(err)
	}
	r, out := plainResolver(t, map[string]string{"yes": "true"}, nil, "")
	if err := r.chooseInstallation(choiceDeps(t)); err != nil || !strings.Contains(out.String(), "one listed SuiteWard installation") {
		t.Errorf("one installation under --yes: err %v, output %q, want it used and said", err, out.String())
	}
}

// A chosen installation is not a typed flag: the tier page and its notice are still shown. The list never gives a tier either: the settings file of
// every candidate exists and names its own, and a list that is stale or was edited must not send a command to the other tier.
func TestAChosenInstallationDoesNotSkipTheTierPage(t *testing.T) {
	useDefaultConfigDir(t)
	defaultSettingsOf(t, colocatedSettings())
	second := settingsOf(t, colocatedSettings())
	lying := colocatedSettings()
	lying.Tier = config.TierIsolated // what the list says, against the file
	if err := recordInstallation(runtime.GOOS, second, "", lying, ""); err != nil {
		t.Fatal(err)
	}
	r, c := keyResolver(t, false, nil, nil, "\x1b[B\x1b[B\r"+"\r") // the installation (down to the first row, down to the second), then the tier
	if err := r.chooseInstallation(choiceDeps(t)); err != nil {
		t.Fatal(err)
	}
	if r.chosen["settings"] != second {
		t.Fatalf("chosen is %v, want the second installation", r.chosen)
	}
	if tier, set := r.chosen["tier"]; set || r.preset("tier", string(config.TierCoLocated)) != string(config.TierCoLocated) {
		t.Errorf("the list gave the tier %q: only the settings file names it", tier)
	}
	if _, given := r.given("tier"); given || !r.asks("tier") {
		t.Error("a chosen installation counts as a typed tier: the tier page would be skipped")
	}
	if _, err := chooseTier(r, config.TierCoLocated); err != nil {
		t.Fatal(err)
	}
	// The step column of the frame is drawn between the rows of the notice, so only what one row holds is looked for.
	for _, want := range []string{"Where SuiteWard runs", "not a determined agent"} {
		if !strings.Contains(c.out.String(), want) {
			t.Errorf("the tier page lacks %q, so the notice of the co-located tier was skipped:\n%s", want, c.out.String())
		}
	}
}

// An entry that says isolated for a co-located settings file does not send a purge to the other tier.
func TestAnEntryThatNamesTheOtherTierDoesNotChangeWhatAUninstallDoes(t *testing.T) {
	useDefaultConfigDir(t)
	defaultSettingsOf(t, colocatedSettings())
	h := purgeHarness(t)
	lying := colocatedSettings()
	lying.Tier = config.TierIsolated
	if err := recordInstallation(runtime.GOOS, h.settingsPath(), h.tokenPath(), lying, testInstance); err != nil {
		t.Fatal(err)
	}
	env := h.consoleOf(true, h.settingsPath()+"\n"+testInstance+"\n")
	if err := Uninstall(context.Background(), []string{"--plain", "--purge"}, env); err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if got, want := h.removed("volume rm"), []string{VolumeData, VolumeDBData}; !slices.Equal(got, want) {
		t.Errorf("volumes removed: %v, want %v: the purge did not run on the co-located tier\n%s", got, want, h.out.String())
	}
}

// A refusal never says that --settings names a file: a chosen installation is not a typed one, and the file is the same either way.
func TestARefusalNeverNamesASettingsFileThatWasNotTyped(t *testing.T) {
	i := twoInstallations(t)
	env := Env{Deps: choiceDeps(t)}
	target := installTarget{settingsPath: i.second}
	typed := newTestResolver(t, map[string]string{"settings": i.second}, nil, Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	if _, err := instanceTokenFile(typed, env, target); err == nil || !strings.Contains(err.Error(), i.second) || !strings.Contains(err.Error(), "second instance") {
		t.Errorf("a typed settings file: %v, want the refusal to name the file", err)
	}
	chosen := newTestResolver(t, nil, nil, Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	chosen.chosen = map[string]string{"settings": i.second}
	if _, err := instanceTokenFile(chosen, env, target); err == nil || strings.Contains(err.Error(), "--settings") || !strings.Contains(err.Error(), i.second) {
		t.Errorf("a chosen settings file: %v, want a refusal that names the file and not a --settings that nobody typed", err)
	}
}

// A second installation may use the per-user default token file, as long as it says so (instanceTokenFile accepts it with --token-file). The picker keeps that token file
// for an installation that is not the default one, so that the command it picked does not refuse for a flag that nobody typed.
func TestAChosenSecondInstallationKeepsTheDefaultTokenFileItUses(t *testing.T) {
	useDefaultConfigDir(t)
	d := choiceDeps(t) // one machine for the default token file, the picker and the command: each call of choiceDeps makes a new one
	tokenFile, err := defaultTokenFile(d)
	if err != nil {
		t.Fatal(err)
	}
	s := colocatedSettings()
	s.Connection, s.HTTPPort, s.MCPPort = "pm", 27081, 27091
	other := colocatedSettings()
	other.Connection, other.HTTPPort, other.MCPPort = "pm2", 28081, 28091
	defaultSettingsOf(t, s)
	second := settingsOf(t, other)
	if err := recordInstallation(runtime.GOOS, second, tokenFile, other, "5e6f7a8b"); err != nil {
		t.Fatal(err)
	}
	r, _ := plainResolver(t, nil, nil, second+"\n")
	if err := r.chooseInstallation(d); err != nil {
		t.Fatal(err)
	}
	if r.chosen["settings"] != second || r.chosen["token-file"] != tokenFile {
		t.Errorf("chosen is %v, want the second installation's settings file and its token file %q", r.chosen, tokenFile)
	}
	if got, err := instanceTokenFile(r, Env{Deps: d}, installTarget{settingsPath: second}); err != nil || got != tokenFile {
		t.Errorf("instanceTokenFile = %q, %v; want the token file %q: a flagless upgrade of the chosen installation must not refuse", got, err, tokenFile)
	}
}

// Every command that works on an installation asks, and refuses where nobody can be asked, before it touches anything.
func TestEveryCommandThatWorksOnAnInstallationRefusesSeveralWhereNobodyCanAsk(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--upgrade", "--image", testImage},
		{"uninstall"},
		{"enroll"},
		{"backup"},
		{"backup", "init"},
		{"restore", "backup.tar.age"},
		{"doctor", "--plain"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			twoInstallations(t)
			withTerminal(t, true) // enroll reads this before anything else
			h := newHarness(t, "linux")
			var stderr bytes.Buffer
			if code := Main(context.Background(), args, h.newEnv(), &stderr); code != 1 || !strings.Contains(stderr.String(), "several SuiteWard installations") {
				t.Fatalf("exit %d, stderr %q: want 1 and the refusal", code, stderr.String())
			}
			if len(h.f.calls) != 0 {
				t.Errorf("the engine was called before the refusal: %v", h.f.lines())
			}
		})
	}
}

// The installation that was chosen is the one the command works on: its settings file, and its engine as the file names it.
func TestUninstallWorksOnTheInstallationThatWasChosen(t *testing.T) {
	useDefaultConfigDir(t)
	defaultSettingsOf(t, colocatedSettings())
	h := purgeHarness(t) // its settings and token file are in a folder of the harness, not the default ones
	if err := recordInstallation(runtime.GOOS, h.settingsPath(), h.tokenPath(), colocatedSettings(), testInstance); err != nil {
		t.Fatal(err)
	}
	env := h.consoleOf(true, h.settingsPath()+"\n"+"yes\n")
	if err := Uninstall(context.Background(), []string{"--plain"}, env); err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if want := "Its settings file: " + h.settingsPath() + "."; !strings.Contains(h.out.String(), want) {
		t.Errorf("the uninstall did not work on the chosen installation (%q):\n%s", want, h.out.String())
	}
	if len(h.f.find("rm --force --ignore --time 45 "+ContainerDB)) == 0 {
		t.Errorf("the containers of the chosen installation were not removed: %v", h.f.lines())
	}
}

// doctor --json is for a program: it checks the default installation as it always did, and never asks or refuses.
func TestDoctorJSONNeitherAsksNorRefuses(t *testing.T) {
	i := twoInstallations(t)
	for _, terminal := range []bool{false, true} {
		f := newFake("linux")
		var out, stderr bytes.Buffer
		env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader("x\n"), Out: &out, IsTerminal: terminal, ReadPassword: func() ([]byte, error) { return nil, nil }}}
		if code := Main(context.Background(), []string{"doctor", "--json"}, env, &stderr); code > 1 {
			t.Fatalf("terminal %v: exit %d: %s%s", terminal, code, stderr.String(), out.String())
		}
		if strings.Contains(out.String(), "several") || strings.Contains(stderr.String(), "several") {
			t.Errorf("terminal %v: doctor --json refused or asked:\n%s%s", terminal, out.String(), stderr.String())
		}
		if !strings.Contains(strings.ReplaceAll(out.String(), `\\`, `\`), i.def) {
			t.Errorf("terminal %v: doctor --json did not check the default installation (%s):\n%s", terminal, i.def, out.String())
		}
	}
}

// doctor on a screen asks, and its hints name the installation that was chosen.
func TestDoctorAsksWhichInstallationAndItsHintsNameIt(t *testing.T) {
	i := twoInstallations(t)
	f := newFake("linux")
	var out, stderr bytes.Buffer
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(i.second + "\n"), Out: &out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}}
	Main(context.Background(), []string{"doctor", "--plain"}, env, &stderr)
	want := "`suiteward doctor --settings " + quoteArg(i.second) + " --token-file " + quoteArg(i.token) + "`"
	if !strings.Contains(out.String(), want) {
		t.Errorf("no hint of doctor carries %q:\n%s%s", want, out.String(), stderr.String())
	}
}
