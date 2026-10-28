package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// A full screen explains each question of the GitHub steps on a page of its own, where a plain run prints what it printed before.

var (
	frameEscapes = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
	frameBlanks  = regexp.MustCompile(`[\s│]+`)
)

// flat is what a full screen shows as one run of words: a frame draws each row apart, between the borders of the frame, and wraps a
// paragraph at the width of its column, so a phrase of the page may be split by both.
func flat(s string) string {
	return frameBlanks.ReplaceAllString(frameEscapes.ReplaceAllString(s, " "), " ")
}

// shows says that the screen showed want, wherever the frame wrapped it.
func shows(screen, want string) bool { return strings.Contains(flat(screen), flat(want)) }

// without returns flags without keys: the questions of those keys are asked.
func without(flags map[string]string, keys ...string) map[string]string {
	for _, k := range keys {
		delete(flags, k)
	}
	return flags
}

// waitForText waits until the output holds text.
func waitForText(t *testing.T, out *syncBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !shows(out.String(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("the output never held %q:\n%s", text, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGitHubQuestionIsExplainedOnAFullScreen(t *testing.T) {
	// On a full screen the yes/no question is a Yes/No choice; without key input it is answered by number, and 2 is No.
	scr, br, out := fullScreenOn(t, liveContext(t), strings.NewReader("2\n"))
	r := newTestResolver(t, map[string]string{}, nil, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, errors.New("no") }})
	yes, err := (&githubInstall{}).wantsGitHub(context.Background(), r)
	if err != nil || yes {
		t.Fatalf("wantsGitHub = %v, %v", yes, err)
	}
	for _, want := range []string{"What SuiteWard needs on GitHub", "GitHub App", "browser", "without GitHub", "again"} {
		if !shows(out.String(), want) {
			t.Errorf("the page lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Index(flat(out.String()), "What SuiteWard needs on GitHub") > strings.Index(flat(out.String()), "Set up GitHub now") {
		t.Error("the explanation comes after the question")
	}
}

func TestGitHubQuestionIsThePlainQuestionInPlainMode(t *testing.T) {
	term, out := fakeTerminal(true, "n\n")
	r := newTestResolver(t, map[string]string{}, nil, term)
	if _, err := (&githubInstall{}).wantsGitHub(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if want := "Set up GitHub now (creates a GitHub App in your browser)? [y/N]: "; out.String() != want {
		t.Errorf("the plain output is %q, want %q", out.String(), want)
	}
}

func TestTheInstallationIsExplainedOnAFullScreen(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := fullFlowEnv(t, liveContext(t), g, manifestFlags(), strings.NewReader(""), false)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Install the GitHub App", "https://github.example/apps/suiteward-abcd/installations/new", "acme", "Created the GitHub App suiteward-abcd."} {
		if !shows(out.String(), want) {
			t.Errorf("the screen never showed %q", want)
		}
	}
}

func TestTheInstallationIsAskedForAsBeforeInPlainMode(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Created the GitHub App suiteward-abcd.\n",
		"Install it: open https://github.example/apps/suiteward-abcd/installations/new, choose acme, and give it access to the repositories to protect.\n",
		"Waiting for the installation on acme (checking every 2ms, up to 2s)...\n",
	} {
		if !shows(out.String(), want) {
			t.Errorf("the plain output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestTheInstallationOfARecordedAppIsExplainedToo(t *testing.T) {
	g := newFakeGitHub(t)
	flags := without(manifestFlags(), "manifest-code")
	env, out := fullFlowEnv(t, liveContext(t), g, flags, strings.NewReader(""), false)
	env.Pending = &PendingApp{AppID: fakeAppID, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	env.KeyExists = func(context.Context) (bool, error) { return true, nil }
	env.ReadKey = func(context.Context) ([]byte, error) { return testKeyPEM(), nil }
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	if !shows(out.String(), "Install the GitHub App") {
		t.Errorf("the screen never explained the installation:\n%s", out.String())
	}
}

// ---- the repositories ----

func TestRepositoriesAreChosenFromAListOnAFullScreen(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(3)
	env, out := fullFlowEnv(t, liveContext(t), g, without(existingFlags(t, nil), "repositories", "test-check", "owner"), strings.NewReader("1,3\nalice\n1\n1\n"), true)
	res, err := resolve(t, env)
	if err != nil || !slices.Equal(res.Repositories, []string{"acme/r1", "acme/r3"}) {
		t.Fatalf("resolveGitHub = %v, %v", res.Repositories, err)
	}
	for _, want := range []string{"Repositories to protect", "at most 20", "acme/r2"} {
		if !shows(out.String(), want) {
			t.Errorf("the screen never showed %q", want)
		}
	}
	if shows(out.String(), "The App can reach these repositories") {
		t.Error("the list was printed as text above the list that is picked from")
	}
}

func TestRepositoriesAreAskedAgainWhenTheAnswerIsOutOfRange(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(3)
	env, _ := fullFlowEnv(t, liveContext(t), g, without(existingFlags(t, nil), "repositories", "test-check"), strings.NewReader("9\n2\n1\n"), true)
	res, err := resolve(t, env)
	if err != nil || !slices.Equal(res.Repositories, []string{"acme/r2"}) {
		t.Fatalf("resolveGitHub = %v, %v", res.Repositories, err)
	}
}

func TestMoreThanTwentyRepositoriesAreNotAllChosenAtFirst(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(21)
	env, out := fullFlowEnv(t, liveContext(t), g, without(existingFlags(t, nil), "repositories", "test-check"), strings.NewReader("1\n1\n"), true)
	res, err := resolve(t, env)
	if err != nil || !slices.Equal(res.Repositories, []string{"acme/r1"}) {
		t.Fatalf("resolveGitHub = %v, %v\n%s", res.Repositories, err, out.String())
	}
}

func TestRepositoriesAreTheNumberedListAndTheTypedQuestionInPlainMode(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(3)
	env, out := flowEnv(t, g, without(existingFlags(t, nil), "repositories"), strings.NewReader("1,3\n\n\n"), true)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	want := "The App can reach these repositories:\n  1) acme/r1\n  2) acme/r2\n  3) acme/r3\nRepositories to protect (numbers or owner/name, comma-separated, at most 20) [acme/r1,acme/r2,acme/r3]: "
	if !strings.Contains(out.String(), want) {
		t.Errorf("the plain question changed:\n%s", out.String())
	}
}

// ---- the instance owner ----

func TestTheOwnerLoginIsExplainedOnAFullScreen(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := fullFlowEnv(t, liveContext(t), g, without(existingFlags(t, nil), "owner"), strings.NewReader("alice\n"), true)
	res, err := resolve(t, env)
	if err != nil || res.OwnerLogin != "alice" {
		t.Fatalf("resolveGitHub = %q, %v", res.OwnerLogin, err)
	}
	for _, want := range []string{"Instance owner", "github.com/<username>", "/suiteward", "organization"} {
		if !shows(out.String(), want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}

func TestTheOwnerLoginPageIsOnlyDrawnWhereTheQuestionIsAsked(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := fullFlowEnv(t, liveContext(t), g, existingFlags(t, nil), strings.NewReader(""), true)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	if shows(out.String(), "Instance owner") {
		t.Error("the page of a question that has its answer wiped the screen")
	}
}

func TestTheOwnerLoginQuestionIsTheSameInPlainMode(t *testing.T) {
	g := newFakeGitHub(t)
	g.ownerLogin, g.ownerType = "alice", "User"
	g.repos[0].name = "alice/app"
	env, out := flowEnv(t, g, without(existingFlags(t, map[string]string{"repositories": "alice/app"}), "owner"), strings.NewReader("\n"), true)
	res, err := resolve(t, env)
	if err != nil || res.OwnerLogin != "alice" {
		t.Fatalf("resolveGitHub = %q, %v", res.OwnerLogin, err)
	}
	if want := "GitHub username of the owner, the only account that can approve changes [alice]: "; !strings.Contains(out.String(), want) {
		t.Errorf("the plain question changed:\n%s", out.String())
	}
}

// ---- the test check ----

func TestTheTestCheckIsExplainedForEachRepositoryOnAFullScreen(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(2)
	env, out := fullFlowEnv(t, liveContext(t), g, without(existingFlags(t, map[string]string{"repositories": "acme/r1,acme/r2"}), "test-check"), strings.NewReader("1\n1\n"), true)
	res, err := resolve(t, env)
	if err != nil || res.TestChecks["acme/r1"] != "CI / test" || res.TestChecks["acme/r2"] != "CI / test" {
		t.Fatalf("resolveGitHub = %v, %v", res.TestChecks, err)
	}
	for _, want := range []string{"Required test check for acme/r1", "Required test check for acme/r2", "SuiteWard does not replace your CI's tests", "SuiteWard / Contract", "CI / test"} {
		if !shows(out.String(), want) {
			t.Errorf("the screen never showed %q", want)
		}
	}
}

func TestTheTestCheckPageIsOnlyDrawnForARepositoryThatIsAsked(t *testing.T) {
	g := newFakeGitHub(t)
	g.repos = manyRepos(2)
	flags := without(existingFlags(t, map[string]string{"repositories": "acme/r1,acme/r2", "test-check.acme/r1": "one"}), "test-check")
	env, out := fullFlowEnv(t, liveContext(t), g, flags, strings.NewReader("1\n"), true)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	if shows(out.String(), "Required test check for acme/r1") || !shows(out.String(), "Required test check for acme/r2") {
		t.Errorf("a page was drawn for a repository whose check is given, or not for one that is asked")
	}
}

func TestTheTestCheckQuestionIsTheSameInPlainMode(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, without(existingFlags(t, nil), "test-check"), strings.NewReader("2\n"), true)
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	want := "Check runs on the main branch of acme/app:\n  1) CI / test\n  2) lint\nRequired test check for acme/app (number or name): "
	if !strings.Contains(out.String(), want) {
		t.Errorf("the plain question changed:\n%s", out.String())
	}
}

// ---- an App without its key ----

func TestAnAppWithoutItsKeyIsAChoiceOnAFullScreen(t *testing.T) {
	const deleteAt = "https://github.example/settings/apps/suiteward-abcd"
	t.Run("a new App", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := fullFlowEnv(t, liveContext(t), g, map[string]string{}, strings.NewReader("2\n"), true)
		_, err := missingKey(env, "App 4242", deleteAt)
		if !errors.Is(err, errNewApp) {
			t.Fatalf("missingKey = %v, want errNewApp", err)
		}
		for _, want := range []string{"The App has no key", "App 4242", deleteAt, "key file", "new App"} {
			if !shows(out.String(), want) {
				t.Errorf("the screen never showed %q", want)
			}
		}
		if shows(out.String(), "Choose 1 or 2") {
			t.Error("the question is still the typed one")
		}
	})
	t.Run("the key file", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, _ := fullFlowEnv(t, liveContext(t), g, map[string]string{}, strings.NewReader("1\n"+writeKeyFile(t)+"\n"), true)
		key, err := missingKey(env, "App 4242", deleteAt)
		if err != nil || string(key) != string(testKeyPEM()) {
			t.Fatalf("missingKey = %d bytes, %v", len(key), err)
		}
	})
}

func TestAnAppWithoutItsKeyIsAskedAsBeforeInPlainMode(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, map[string]string{}, strings.NewReader("2\n"), true)
	if _, err := missingKey(env, "App 4242", "https://github.example/settings/apps"); !errors.Is(err, errNewApp) {
		t.Fatalf("missingKey = %v", err)
	}
	want := "The App 4242 has no key in the secret store (" + SecretGitHubAppKey + ").\n  1) give the App's private key file\n  2) create a new App (the old one stays on GitHub: delete it at https://github.example/settings/apps)\nChoose 1 or 2: "
	if !strings.Contains(out.String(), want) {
		t.Errorf("the plain question changed:\n%s", out.String())
	}
}

// ---- the rulesets ----

// rulesetRun runs the flow on a full screen that asks for the import, and lets the test answer through the pipe.
type rulesetRun struct {
	env  githubEnv
	out  *syncBuffer
	pw   *io.PipeWriter
	done chan browserResult
}

func startRulesetRun(t *testing.T, g *fakeGitHub) *rulesetRun {
	t.Helper()
	return startRulesetRunWith(t, g, existingFlags(t, nil))
}

func startRulesetRunWith(t *testing.T, g *fakeGitHub, flags map[string]string) *rulesetRun {
	t.Helper()
	return startRulesetRunSized(t, g, flags, 100, 30)
}

// startRulesetRunSized is startRulesetRunWith on a terminal of width by height.
func startRulesetRunSized(t *testing.T, g *fakeGitHub, flags map[string]string, width, height int) *rulesetRun {
	t.Helper()
	return startRulesetRunCtx(t, liveContext(t), g, flags, width, height)
}

// startRulesetRunCtx is startRulesetRunSized on a screen whose context is ctx: the one Ctrl-C cancels.
func startRulesetRunCtx(t *testing.T, ctx context.Context, g *fakeGitHub, flags map[string]string, width, height int) *rulesetRun {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	env, _ := flowEnv(t, g, flags, pr, true)
	env.Deps.Command = flags["_command"] // the name that the messages of the flow give the program: empty, suiteward
	// Where the ruleset files go, when the test needs a folder that is not a temp directory.
	if dir := flags["_settings-dir"]; dir != "" {
		env.SettingsDir = dir
	}
	out := &syncBuffer{}
	br := bufio.NewReader(pr)
	scr, err := ui.Open(ui.Options{Out: out, In: br, Context: ctx, Version: "v0", Mode: ui.ModeFull, Color: ui.ColorNone, Width: width, Height: height})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scr.Close() })
	r, err := NewResolver(env.R.flags, env.R.answers, Terminal{In: br, Out: scr, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, errors.New("no password here") }})
	if err != nil {
		t.Fatal(err)
	}
	env.R, env.Out = r, scr
	env.SkipRulesetWait = false
	run := &rulesetRun{env: env, out: out, pw: pw, done: make(chan browserResult, 1)}
	go func() {
		res, err := resolve(t, env)
		run.done <- browserResult{res, err}
	}()
	return run
}

func (r *rulesetRun) answer(t *testing.T, line string) {
	t.Helper()
	wrote := make(chan struct{})
	go func() { _, _ = io.WriteString(r.pw, line+"\n"); close(wrote) }()
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing read the answer %q:\n%s", line, r.out.String())
	}
}

func (r *rulesetRun) result(t *testing.T) GitHubResult {
	t.Helper()
	select {
	case res := <-r.done:
		if res.err != nil {
			t.Fatalf("resolveGitHub: %v\n%s", res.err, r.out.String())
		}
		return res.res
	case <-time.After(5 * time.Second):
		t.Fatalf("the flow did not return:\n%s", r.out.String())
		return GitHubResult{}
	}
}

func TestTheRulesetStepIsAPageAndAPause(t *testing.T) {
	g := newFakeGitHub(t)
	run := startRulesetRun(t, g)
	waitForText(t, run.out, "I imported it, check now")
	text := run.out.String()
	for _, want := range []string{"Import the rulesets", "suiteward-ruleset-acme-app.json", "https://github.example/acme/app/settings/rules", "Skip, import later", "older SuiteWard ruleset"} {
		if !shows(text, want) {
			t.Errorf("the screen lacks %q:\n%s", want, text)
		}
	}
	if shows(text, "Press Enter when the rulesets are imported") {
		t.Error("the pause still looks like a question that Enter answers with yes")
	}
	run.answer(t, "1")
	if res := run.result(t); res.Protection["acme/app"].Kind != protection.KindEnforced {
		t.Errorf("protection = %+v", res.Protection)
	}
}

func TestARulesetThatIsNotThereIsReportedAndTheUserMayCheckAgain(t *testing.T) {
	g := newFakeGitHub(t)
	g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = true })
	run := startRulesetRun(t, g)
	waitForText(t, run.out, "I imported it, check now")
	run.answer(t, "1") // nothing is imported yet
	waitForText(t, run.out, "missing-contract-check")
	if strings.Count(run.out.String(), "Import the rulesets") == 0 {
		t.Fatal("the page vanished")
	}
	g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = false }) // the user imports it now
	run.answer(t, "1")
	res := run.result(t)
	if got := res.Protection["acme/app"].Kind; got != protection.KindEnforced {
		t.Fatalf("protection = %q, want enforced after the second check", got)
	}
	if n := g.count("GET /repos/acme/app/rules/branches/main"); n != 2 {
		t.Errorf("the protection was read %d times, want once per check", n)
	}
	scr := run.env.Out.(*ui.Screen)
	if err := scr.Close(); err != nil {
		t.Fatal(err)
	}
	tail := run.out.String()[strings.LastIndex(run.out.String(), leaveAlt):]
	if strings.Count(tail, "Protection as GitHub reports it now:") != 1 {
		t.Errorf("the result that is kept is not printed once:\n%s", tail)
	}
}

func TestTheRulesetStepCanBeSkippedAndSaysWhatIsLeftToDo(t *testing.T) {
	t.Run("at once", func(t *testing.T) {
		g := newFakeGitHub(t)
		run := startRulesetRun(t, g)
		waitForText(t, run.out, "Skip, import later")
		run.answer(t, "2")
		res := run.result(t)
		if g.sawRequest("GET /repos/acme/app/rules/branches/main") {
			t.Error("the protection was read although the user skipped the check")
		}
		if got := res.Protection["acme/app"].Kind; got != protection.KindUnknown {
			t.Errorf("protection = %q, want unknown (not checked)", got)
		}
		scr := run.env.Out.(*ui.Screen)
		_ = scr.Close()
		tail := run.out.String()[strings.LastIndex(run.out.String(), leaveAlt):]
		for _, want := range []string{"acme/app", "suiteward doctor", "not imported"} {
			if !strings.Contains(tail, want) {
				t.Errorf("the warning that is kept lacks %q:\n%s", want, tail)
			}
		}
	})
	t.Run("after a check that found nothing", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = true })
		run := startRulesetRun(t, g)
		waitForText(t, run.out, "I imported it, check now")
		run.answer(t, "1")
		waitForText(t, run.out, "missing-contract-check")
		run.answer(t, "2")
		res := run.result(t)
		if got := res.Protection["acme/app"].Kind; got != protection.KindFindings {
			t.Errorf("protection = %q, want what the last check found", got)
		}
		// What the last check found, and what is left to do, stay after the screen is gone.
		_ = run.env.Out.(*ui.Screen).Close()
		tail := run.out.String()[strings.LastIndex(run.out.String(), leaveAlt):]
		for _, want := range []string{"Protection as GitHub reports it now:", "acme/app: findings", "findings: missing-contract-check", "Ruleset for acme/app:", "Import it: open https://github.example/acme/app/settings/rules", "suiteward doctor"} {
			if !strings.Contains(tail, want) {
				t.Errorf("the scrollback lacks %q:\n%s", want, tail)
			}
		}
	})
}

// A private repository on GitHub Free never becomes enforced by an import: the question is not asked again for it.
func TestTheRulesetStepDoesNotAskAgainWhenEnforcementIsUnavailable(t *testing.T) {
	g := newFakeGitHub(t)
	g.changeRepos(func(repos []fakeRepo) { repos[0].private, repos[0].rulesStatus = true, 403 })
	run := startRulesetRun(t, g)
	waitForText(t, run.out, "I imported it, check now")
	run.answer(t, "1")
	if res := run.result(t); res.Protection["acme/app"].Kind != protection.KindEnforcementUnavailable {
		t.Errorf("protection = %+v", res.Protection)
	}
}

// With the answer given there is nobody to pause for: the protection is read once, as it always was.
func TestTheRulesetStepDoesNotPauseWhenTheAnswerIsGiven(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := fullFlowEnv(t, liveContext(t), g, existingFlags(t, map[string]string{"ruleset-imported": "yes"}), strings.NewReader(""), true)
	env.SkipRulesetWait = false
	res, err := resolve(t, env)
	if err != nil || res.Protection["acme/app"].Kind != protection.KindEnforced {
		t.Fatalf("resolveGitHub = %+v, %v", res.Protection, err)
	}
	if shows(out.String(), "I imported it") {
		t.Error("a question that has its answer was asked")
	}
}

// A plain run keeps its question and its answer: a typed no is no more than Enter, as before, and the protection that is read says
// what is missing.
func TestThePlainRulesetQuestionIsUnchanged(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, existingFlags(t, nil), strings.NewReader("no\n"), true)
	env.SkipRulesetWait = false
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	want := "Import it: open https://github.example/acme/app/settings/rules, choose New ruleset, then Import a ruleset, select that file and create the ruleset.\n\nPress Enter when the rulesets are imported [yes]: "
	if !strings.Contains(out.String(), want) {
		t.Errorf("the plain question changed:\n%s", out.String())
	}
}

// A page that goes on from the one before would push the files off the screen, where the user needs them again after a check that
// found something missing: the page is drawn again for the repositories that are not protected yet, with their files.
func TestTheFilesOfWhatIsStillMissingAreOnTheScreenAfterACheck(t *testing.T) {
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
	frame := lastFrame(run.out.String())
	for _, want := range []string{"suiteward-ruleset-acme-r2.json", "missing-contract-check", "New ruleset"} {
		if !shows(frame, want) {
			t.Errorf("the screen no longer shows %q after the check:\n%s", want, frame)
		}
	}
	if shows(frame, "suiteward-ruleset-acme-r1.json") {
		t.Errorf("the screen still shows the file of a repository that is protected:\n%s", frame)
	}
	run.answer(t, "2")
	run.result(t)
}

// ---- the ruleset pages on a small screen ----

// Every repository is a group of rows of the page: its name and the file of its ruleset, the link of its rules page and what is missing,
// the folder said once. A page that is only 18 rows must show the file of each repository that still needs one, up to what the rows hold:
// the user imports from it. Three repositories that each miss one thing fit; a repository that misses two, with their words, takes three
// rows of its own, so two of them fit and the page says that the rest is cut (the scrollback keeps every file).
func TestTheRulesetPagesShowTheFileOfEveryRepositoryOnA80By24Screen(t *testing.T) {
	t.Run("three repositories that each miss one thing", func(t *testing.T) {
		checkRulesetPagesOn80By24(t, 3, "not-up-to-date", func(r *fakeRepo) {
			r.ruleChecks, r.ruleStrict = []fakeRuleChk{{protection.ContractCheck, fakeAppID}, {"CI / test", 0}}, false
		})
	})
	t.Run("two repositories with nothing imported", func(t *testing.T) {
		checkRulesetPagesOn80By24(t, 2, "missing-contract-check", func(r *fakeRepo) { r.noRule = true })
	})
}

// checkRulesetPagesOn80By24 runs the pause for n repositories that mutate makes miss something, which the page says with the code found.
func checkRulesetPagesOn80By24(t *testing.T, n int, found string, mutate func(*fakeRepo)) {
	t.Helper()
	g := newFakeGitHub(t)
	g.repos = manyRepos(n)
	g.changeRepos(func(repos []fakeRepo) {
		for i := range repos {
			mutate(&repos[i])
		}
	})
	var names []string
	for i := 1; i <= n; i++ {
		names = append(names, fmt.Sprintf("acme/r%d", i))
	}
	flags := existingFlags(t, map[string]string{"repositories": strings.Join(names, ",")})
	delete(flags, "test-check")
	for _, name := range names {
		flags["test-check."+name] = "CI / test"
	}
	// The page says the folder of the files once, above the rows, as one long word that it cuts wherever a row ends (after a separator when
	// one fits): with the path of a temp directory, which the test name and a random number make long, the last part of it could be cut in
	// two, and the rows below it come under the trim. The folder is a short one, relative to a temp directory, so that the page does not
	// depend on how long that path is.
	t.Chdir(t.TempDir())
	flags["_settings-dir"] = "rulesets"
	run := startRulesetRunSized(t, g, flags, 80, 24)
	waitForText(t, run.out, "I imported it, check now")
	folder := filepath.Base(run.env.SettingsDir) + ":" // the folder, which the page says once, above the rows
	onScreen := func(when string, codes bool) {
		t.Helper()
		frame := lastFrame(run.out.String())
		for _, name := range names {
			file := "suiteward-ruleset-" + strings.ReplaceAll(name, "/", "-") + ".json"
			if got := strings.Count(flat(frame), file); got != 1 {
				t.Errorf("%s: the file %s shows %d times, want once:\n%s", when, file, got, frame)
			}
			if link := "https://github.example/" + name + "/settings/rules"; !shows(frame, link) {
				t.Errorf("%s: the link %s is not on the screen:\n%s", when, link, frame)
			}
		}
		if got := strings.Count(flat(frame), folder); got != 1 {
			t.Errorf("%s: the folder of the files shows %d times, want once:\n%s", when, got, frame)
		}
		if codes {
			if got := strings.Count(flat(frame), found); got != len(names) {
				t.Errorf("%s: %s shows %d times, want once in the rows of each repository:\n%s", when, found, got, frame)
			}
		}
	}
	onScreen("the first page", false)
	run.answer(t, "1")
	waitForText(t, run.out, found)
	onScreen("after a check", true)
	run.answer(t, "1")
	waitForText(t, run.out, found)
	onScreen("after a second check", true)
	run.answer(t, "2")
	run.result(t)
}

// A Ctrl-C at the pause ends the install, and the page that holds the files goes with the screen: the files of what is not imported
// must be in the scrollback, where the user finds them again.
func TestTheFilesAreInTheScrollbackAfterACtrlCAtTheRulesetPause(t *testing.T) {
	for name, checked := range map[string]bool{"before any check": false, "after a check": true} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.changeRepos(func(repos []fakeRepo) { repos[0].noRule = true })
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			run := startRulesetRunCtx(t, ctx, g, existingFlags(t, nil), 100, 30)
			waitForText(t, run.out, "I imported it, check now")
			if checked {
				run.answer(t, "1")
				waitForText(t, run.out, "missing-contract-check")
			}
			time.Sleep(50 * time.Millisecond) // the pause is open
			cancel()
			select {
			case res := <-run.done:
				if !errors.Is(res.err, context.Canceled) {
					t.Fatalf("resolveGitHub = %v, want the interrupt", res.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("the flow did not end with Ctrl-C:\n%s", run.out.String())
			}
			_ = run.env.Out.(*ui.Screen).Close()
			tail := run.out.String()[strings.LastIndex(run.out.String(), leaveAlt):]
			for _, want := range []string{"Ruleset for acme/app:", "suiteward-ruleset-acme-app.json", "Import it: open https://github.example/acme/app/settings/rules"} {
				if !strings.Contains(tail, want) {
					t.Errorf("the scrollback lacks %q:\n%s", want, tail)
				}
			}
		})
	}
}

// Since M2.1 SuiteWard runs tests, and no text of the installer says that it does not (runtime contract, "What a text says about running
// tests"): the sources are read, because a screen shows only the texts a test happens to draw.
func TestNoInstallerTextSaysThatSuiteWardRunsNoTests(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"runs no tests", "does not run tests", "did not run any tests", "did not run the tests"} {
			if strings.Contains(strings.ToLower(string(raw)), banned) {
				t.Errorf("%s says %q", name, banned)
			}
		}
	}
}
