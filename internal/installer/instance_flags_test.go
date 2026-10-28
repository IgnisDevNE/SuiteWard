package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// On a computer with a second instance, a person who copies a command that the installer or doctor suggests runs it against the instance that its
// flags name: a command without them reaches the default instance, and an upgrade, an uninstall or a purge of the wrong one is not undone. So a
// suggested command carries the flags of the running command that name the instance, right after its subcommand.

// doctorMain runs `suiteward doctor --json` with args and returns the results by name; a failing check is a result, not a failed run.
func doctorMain(t *testing.T, f *fakeEngine, args ...string) map[string]doctorResult {
	t.Helper()
	var out, stderr bytes.Buffer
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &out}}
	if code := Main(context.Background(), append([]string{"doctor", "--json"}, args...), env, &stderr); code > 1 {
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

// named is the flags that name the instance of a harness in the runs of its tests, as a hint repeats them.
func (h *harness) named() instanceArgs {
	return instanceArgs{settings: h.settingsPath(), tokenFile: h.tokenPath()}
}

// named is the same for an isolated host, whose runs give the tier and the settings file.
func (u *isoUpgrade) named() instanceArgs {
	return instanceArgs{tier: "isolated", settings: settingsFile(u.home)}
}

func TestSuggestRepeatsTheFlagsThatNameTheInstanceRightAfterTheSubcommand(t *testing.T) {
	const (
		settingsPath, accessCopy = `C:\sw m21\settings.yaml`, `C:\sw-m21\mcp-token`
		sums                     = "--sha256sums <the release's SHA256SUMS file>"
	)
	second := Deps{Command: swCmd, instance: instanceArgs{settings: settingsPath, tokenFile: accessCopy}}
	quoted := "--settings '" + settingsPath + "' --token-file " + accessCopy
	for name, c := range map[string]struct{ got, want string }{
		"install":                   {second.suggest("install"), swCmd + " install " + quoted},
		"an upgrade checked":        {second.suggest("install", "--upgrade", sums), swCmd + " install " + quoted + " --upgrade " + sums},
		"a restore of a file":       {second.suggest("restore", "/b/one.tar.age"), swCmd + " restore " + quoted + " /b/one.tar.age"},
		"a two-word subcommand":     {second.suggest("backup init", "--replace"), swCmd + " backup init " + quoted + " --replace"},
		"the tier of the run":       {Deps{instance: instanceArgs{tier: "isolated", settings: "/s.yaml"}}.suggest("uninstall", "--yes"), "suiteward uninstall --tier isolated --settings /s.yaml --yes"},
		"doctor has no --tier":      {Deps{instance: instanceArgs{tier: "isolated", settings: "/s.yaml", tokenFile: "/t"}}.suggest("doctor"), "suiteward doctor --settings /s.yaml --token-file /t"},
		"enroll has no token file":  {Deps{instance: instanceArgs{tier: "isolated", settings: "/s.yaml", tokenFile: "/t"}}.suggest("enroll"), "suiteward enroll --tier isolated --settings /s.yaml"},
		"the host step on a server": {second.suggest("install host", sums), swCmd + " install host " + sums},
		"the default instance":      {Deps{}.suggest("install", "--upgrade"), "suiteward install --upgrade"},
		"another instance":          {second.otherInstance().suggest("uninstall"), swCmd + " uninstall"},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", name, c.got, c.want)
		}
	}
}

// The flags come from the run: --settings of the command line only, the token file and the tier of the command line or the answers file.
func TestTheInstanceOfARunIsWhatItsFlagsAndAnswersNameIt(t *testing.T) {
	term := Terminal{In: strings.NewReader(""), Out: &strings.Builder{}}
	for name, c := range map[string]struct {
		flags, answers map[string]string
		want           instanceArgs
	}{
		"none":               {nil, nil, instanceArgs{}},
		"flags":              {map[string]string{"settings": "/s", "token-file": "/t", "tier": "isolated"}, nil, instanceArgs{tier: "isolated", settings: "/s", tokenFile: "/t"}},
		"answers file":       {map[string]string{"settings": "/s"}, map[string]string{"token-file": "/t", "tier": "co-located"}, instanceArgs{tier: "co-located", settings: "/s", tokenFile: "/t"}},
		"a flag wins":        {map[string]string{"token-file": "/flag"}, map[string]string{"token-file": "/answers"}, instanceArgs{tokenFile: "/flag"}},
		"no settings answer": {nil, map[string]string{"settings": "/s"}, instanceArgs{}}, // settingsPathFrom reads the command line only
	} {
		if got := newTestResolver(t, c.flags, c.answers, term).instance(); got != c.want {
			t.Errorf("%s: got %+v, want %+v", name, got, c.want)
		}
	}
}

// A flag that a command does not define is a usage error for it: every flag that suggest repeats for a subcommand is one that subcommand takes.
func TestEveryFlagASuggestionCarriesIsOneItsCommandTakes(t *testing.T) {
	all := Deps{instance: instanceArgs{tier: "isolated", settings: "/s.yaml", tokenFile: "/t"}}
	for _, sub := range []string{"install", "uninstall", "enroll", "backup", "backup init", "restore", "doctor", "install host"} {
		t.Run(sub, func(t *testing.T) {
			args := slices.Concat(strings.Fields(all.suggest(sub))[1:], []string{"--help"}) // --help is reached only when every flag before it parsed
			var stderr bytes.Buffer
			if code := Main(context.Background(), args, newHarness(t, "linux").newEnv(), &stderr); code != 0 {
				t.Errorf("suiteward %s exited %d: %s", strings.Join(args, " "), code, stderr.String())
			}
		})
	}
}

func TestAnInstallThatNamesAnInstanceSuggestsCommandsThatNameIt(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		if err := e.StoreKey(ctx, []byte(testPEM)); err != nil {
			return GitHubResult{}, err
		}
		return githubResult(), nil
	}
	if err := h.podman(append(githubFlags, "--manifest-port", "9999")...); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	enroll, doctor := (Deps{instance: instanceArgs{settings: h.settingsPath()}}).suggest("enroll"), (Deps{instance: h.named()}).suggest("doctor")
	for _, want := range []string{"`" + doctor + "`", enroll + "\n"} { // enroll has no token file to repeat, and a line of it ends the command
		if !strings.Contains(out, want) {
			t.Errorf("the next steps lack %q:\n%s", want, out)
		}
	}
}

// The token file of an answers file names the instance as the flag does.
func TestAnInstallGivenItsTokenFileInAnAnswersFileSuggestsCommandsThatNameIt(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(ctx context.Context, e githubEnv) (GitHubResult, error) {
		if err := e.StoreKey(ctx, []byte(testPEM)); err != nil {
			return GitHubResult{}, err
		}
		return githubResult(), nil
	}
	answers := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(answers, []byte("token-file: '"+h.tokenPath()+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--settings", h.settingsPath(), "--answers", answers, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--manifest-port", "9999"}, githubFlags...)
	if err := Install(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if want := (Deps{instance: h.named()}).suggest("doctor"); !strings.Contains(h.out.String(), "`"+want+"`") {
		t.Errorf("the next steps lack %q:\n%s", want, h.out.String())
	}
}

// The removal of another instance names no flag of this one, which would remove this instance instead; the install of this one does.
func TestDoctorOfASecondInstanceSuggestsNoFlagsForTheRemovalOfAnother(t *testing.T) {
	f, s := colocated{status: coloStatus(10 * time.Minute)}.build(t)
	tokenFile := registerAnotherInstancesExecutor(t, f)
	settings := savedAt(t, s)
	fix := doctorMain(t, f, "--settings", settings, "--token-file", tokenFile)["executor.lifecycle"].Fix
	want := "Remove that instance with `suiteward uninstall` (pass that instance's `--settings` and `--token-file`; the default instance needs neither). Then run `suiteward install --settings " +
		quoteArg(settings) + " --token-file " + quoteArg(tokenFile) + "` again for this one."
	if !strings.Contains(fix, want) {
		t.Errorf("the fix is %q, want it to contain %q", fix, want)
	}
}

// bareCommand is a command of this program written as a literal: the program's name and a subcommand.
var bareCommand = regexp.MustCompile(`\bsuiteward (install|uninstall|doctor|backup|restore|enroll)\b`)

// A hint writes a command of this program only with Deps.suggest, which puts the flags that name the instance after the subcommand. So no string
// of the installer spells one by itself, and nothing but suggest reads the program's name (Deps.Command, selfName): a new hint that did would
// drop the flags again. The files that may spell one are listed with the reason: they are not hints of this instance.
func TestNoHintWritesACommandOfThisProgramWithoutTheFlagsOfTheInstance(t *testing.T) {
	spelled := map[string]string{
		"command.go":            "the usage text",
		"selfinstall.go":        "the prose of the page that offers the copy of the program",
		"executor_colocated.go": "a line of the script the test runner is started by",
		"isolated.go":           "the line that the host step prints for the service account, with the user bus filled in",
		"summary.go":            "setupInstructionMark, which the summary matches in a sentence of the service's own check",
	}
	reads := map[string]bool{"command.go": true, "selfinstall.go": true} // OSEnv and commandName set the name, and suggest reads it
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				// An `install host` command is the administrator's step on a server: it names no instance.
				if s, err := strconv.Unquote(n.Value); n.Kind == token.STRING && err == nil && bareCommand.MatchString(s) && !strings.Contains(s, "install host") && spelled[name] == "" {
					t.Errorf("%s spells a command of this program by itself: %s (write it with Deps.suggest)", name, n.Value)
				}
			case *ast.SelectorExpr:
				if n.Sel.Name == "Command" && !reads[name] {
					t.Errorf("%s reads Deps.Command: write the command with Deps.suggest", name)
				}
			case *ast.Ident:
				if n.Name == "selfName" && !reads[name] {
					t.Errorf("%s uses selfName: write the command with Deps.suggest", name)
				}
			}
			return true
		})
	}
}
