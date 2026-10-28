package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The last thing an install or an upgrade does on a full screen is run doctor: its counts are a row of the summary, and each result that
// needs a look is a warning with its fix. The install itself still ends well, because doctor reports and a failed check is not a failed
// install. A plain run prints what it printed before.

var doctorSummaryRow = regexp.MustCompile(`(?m)^Doctor: (\d+) ok, (\d+) warnings, (\d+) failures$`)

// doctorCountsIn reads the Doctor row of the summary in the scrollback and checks that its numbers are the lines the summary keeps.
func doctorCountsIn(t *testing.T, tail string) (ok, warned, failed int) {
	t.Helper()
	m := doctorSummaryRow.FindStringSubmatch(tail)
	if m == nil {
		t.Fatalf("the summary has no Doctor row with the counts of doctor:\n%s", tail)
	}
	ok, _ = strconv.Atoi(m[1])
	warned, _ = strconv.Atoi(m[2])
	failed, _ = strconv.Atoi(m[3])
	var warns, fails int
	for _, line := range strings.Split(tail, "\n") {
		switch {
		case strings.HasPrefix(line, "WARN "):
			warns++
		case strings.HasPrefix(line, "FAIL "):
			fails++
		}
	}
	if warns != warned || fails != failed {
		t.Errorf("the row says %d warnings and %d failures, the summary keeps %d WARN and %d FAIL lines:\n%s", warned, failed, warns, fails, tail)
	}
	return ok, warned, failed
}

const (
	rulesetFix = "Require the check SuiteWard / Contract on the default branch, with the SuiteWard App as its source. Require the project's test check on the default branch."
	// What `suiteward check --json` of the instance says: a passing result, a repository without its ruleset (with its file beside the
	// settings), another without it and without a file, an enforced one, and a pass that needs a look.
	instanceChecks = `[{"name":"github.app","ok":true,"detail":"app suiteward-acme","fix":""},` +
		`{"name":"protection.Acme/api","ok":false,"detail":"findings: missing-contract-check, missing-test-check","fix":"` + rulesetFix + `"},` +
		`{"name":"protection.Acme/web","ok":false,"detail":"findings: missing-contract-check","fix":"` + rulesetFix + `"},` +
		`{"name":"owner.enrolled","ok":true,"warning":true,"detail":"no owner is enrolled yet","fix":"run suiteward enroll"}]`
)

func TestTheSummaryOfAnInstallRunsDoctorAndTheInstallStillEndsWell(t *testing.T) {
	withTerminal(t, true)
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	h.first("exec -i suiteward /suiteward check --json", instanceChecks)
	api, err := writeRuleset(h.dir, "Acme/api", []byte("{}\n")) // the file the install of the App wrote beside the settings
	if err != nil {
		t.Fatal(err)
	}
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	if err := Install(liveContext(t), colocatedArgs(h), env); err != nil {
		t.Fatalf("a failed check of doctor failed the install: %v", err)
	}
	tail := c.tail()
	ok, warned, failed := doctorCountsIn(t, tail)
	if ok < 1 || warned < 1 || failed < 2 {
		t.Errorf("the row says %d ok, %d warnings, %d failures, want at least one pass, the warning and the two failed repositories:\n%s", ok, warned, failed, tail)
	}
	for _, want := range []string{
		"WARN owner.enrolled: no owner is enrolled yet — run suiteward enroll",
		"FAIL protection.Acme/api: findings: missing-contract-check, missing-test-check — import the ruleset file " + api, // the file requires both checks: it is the fix
		"FAIL protection.Acme/web: findings: missing-contract-check — " + rulesetFix,                                      // no file: the instruction of the check
	} {
		if !strings.Contains(tail, want) {
			t.Errorf("the summary lacks %q:\n%s", want, tail)
		}
	}
	// The repository whose ruleset file exists is told which file to import; one whose file is not there is not sent to a file that is not.
	var apiLine, webLine string
	for _, line := range strings.Split(tail, "\n") {
		switch {
		case strings.HasPrefix(line, "FAIL protection.Acme/api"):
			apiLine = line
		case strings.HasPrefix(line, "FAIL protection.Acme/web"):
			webLine = line
		}
	}
	if !strings.Contains(apiLine, "import the ruleset file "+api) || !strings.Contains(apiLine, "Import a ruleset") {
		t.Errorf("the repository without its ruleset is not told which file to import (%s):\n%s", api, apiLine)
	}
	if strings.Contains(webLine, "ruleset file") {
		t.Errorf("a repository whose ruleset file does not exist is sent to it:\n%s", webLine)
	}
	if strings.Contains(tail, "Next: suiteward doctor") {
		t.Errorf("the summary still sends the person to a command that has run:\n%s", tail)
	}
	if !strings.Contains(tail, "Check again: suiteward doctor --settings "+quoteArg(h.settingsPath())) {
		t.Errorf("a summary with something to look at does not say how to check again:\n%s", tail)
	}
	noRepeats(t, tail)
}

func TestTheSummaryOfAnUpgradeRunsDoctor(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.first("exec -i suiteward /suiteward check --json", instanceChecks)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatalf("a failed check of doctor failed the upgrade: %v", err)
	}
	tail := c.tail()
	doctorCountsIn(t, tail)
	if !strings.Contains(tail, "WARN owner.enrolled: no owner is enrolled yet — run suiteward enroll") {
		t.Errorf("the summary of the upgrade lacks the warning of doctor:\n%s", tail)
	}
	if strings.Contains(tail, "Next: suiteward doctor") {
		t.Errorf("the summary still sends the person to a command that has run:\n%s", tail)
	}
	noRepeats(t, tail)
}

func TestTheSummaryOfTheIsolatedTierRunsDoctor(t *testing.T) {
	// The install.
	f := newIsoFake(t.TempDir())
	f.noImage = true
	c := newTermConsole(&bytes.Buffer{})
	env := Env{
		Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {},
		HTTPGet:    func(context.Context, string) ([]byte, error) { return []byte(`{"version":"v0.2.0"}`), nil },
		OpenScreen: c.open,
	}
	args := []string{"--tier", "isolated", "--settings", settingsFile(f.home), "--image", testDigest, "--github-app-id", "none"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatalf("a failed check of doctor failed the install: %v", err)
	}
	doctorCountsIn(t, c.tail())
	if strings.Contains(c.tail(), "Next: suiteward doctor") {
		t.Errorf("the summary of the install still sends the person to a command that has run:\n%s", c.tail())
	}

	// The upgrade.
	u := newIsoUpgrade(t, nil)
	u.f.noImage = true
	fastHealthWait(t)
	c = newTermConsole(&u.out)
	env = u.env()
	env.OpenScreen = c.open
	args = []string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testDigest, "--yes"}
	if err := Install(liveContext(t), args, env); err != nil {
		t.Fatalf("a failed check of doctor failed the upgrade: %v", err)
	}
	doctorCountsIn(t, c.tail())
	if strings.Contains(c.tail(), "Next: suiteward doctor") {
		t.Errorf("the summary of the upgrade still sends the person to a command that has run:\n%s", c.tail())
	}
}

// A plain run prints what it printed before: it does not run doctor, which would add a read of GitHub, up to two minutes, to a script, and it
// still names the command.
func TestAPlainInstallDoesNotRunDoctor(t *testing.T) {
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	if err := Install(context.Background(), colocatedArgs(h), h.newEnv()); err != nil {
		t.Fatal(err)
	}
	if calls := h.f.find("/suiteward check --json"); len(calls) != 0 {
		t.Errorf("a plain install ran doctor: %v", calls)
	}
	out := h.out.String()
	if !strings.Contains(out, "Then check that everything works with `suiteward doctor --settings "+quoteArg(h.settingsPath())+" --token-file "+quoteArg(h.tokenPath())+"`.") || doctorSummaryRow.MatchString(out) {
		t.Errorf("the plain output changed:\n%s", out)
	}
}

// A settings file that is gone is a doctor that cannot run: the row says so and the install is still done.
func TestTheDoctorRowSaysWhenDoctorCannotRun(t *testing.T) {
	withTerminal(t, true)
	h := newHarness(t, "linux")
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	env.Deps.Interactive = func(context.Context, string, []string) error { // setup is the last thing that runs before the summary
		return os.Remove(h.settingsPath())
	}
	if err := Install(liveContext(t), colocatedArgs(h), env); err != nil {
		t.Fatalf("a doctor that cannot run failed the install: %v", err)
	}
	if !strings.Contains(c.tail(), "Doctor: not run: ") {
		t.Errorf("the row does not say that doctor could not run:\n%s", c.tail())
	}
	if m := doctorSummaryRow.FindString(c.tail()); m != "" {
		t.Errorf("counts of a doctor that did not run: %q", m)
	}
}

// A healthy instance is the counts and nothing else: no warning, and no command to check again.
func TestTheDoctorRowOfAHealthyInstanceHasNoWarningsAndNothingToCheckAgain(t *testing.T) {
	f, s := colocated{}.build(t)
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}}
	var rows summaryRows
	rows.addDoctor(context.Background(), env, path, coloTokenFile(t, f), "suiteward doctor")
	assertReadOnly(t, f)
	if len(rows) != 1 || rows[0].Key != "Doctor" || !regexp.MustCompile(`^\d+ ok, 0 warnings, 0 failures$`).MatchString(rows[0].Value) {
		t.Errorf("the rows of a healthy instance are %+v, want the Doctor row alone", rows)
	}
}

// A summary that carries what doctor found still shows the rows a person needs on the smallest window. The page keeps its key rows and trims
// the warnings from the end when it is taller than the window: this is the case of the first install, two repositories without their
// ruleset and an owner who is not enrolled, and the Status and Doctor rows stay, with the first warning.
func TestTheSummaryPageKeepsTheStatusAndDoctorRowsOnASmallWindow(t *testing.T) {
	f, s := colocated{checkOut: instanceChecks}.build(t)
	// The settings and the ruleset files are named relative to a folder the test works in: a path under the temp folder wraps at 80 columns when
	// that folder is long, and the page would lose the first warning for the length of the path and not for the number of rows.
	t.Chdir(t.TempDir())
	const path = "settings.yaml"
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"Acme/api", "Acme/web"} {
		if _, err := writeRuleset(".", repo, []byte("{}\n")); err != nil {
			t.Fatal(err)
		}
	}
	var rows summaryRows
	for _, kv := range [][2]string{{"Installation type", "co-located"}, {"Container program", "Podman (runs without administrator rights)"}, {"Instance ID", "1a2b3c4d"}, {"Version", "v0.4.0-alpha.2"}, {"GitHub App", "1234, installed on Acme"},
		{"Repositories", "Acme/api, Acme/web"}, {"Owner", "alice (enrolled now)"}, {"Status", "http://127.0.0.1:47081/status"}, {"Settings file", path}} {
		rows.add(kv[0], kv[1])
	}
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}}
	rows.addDoctor(context.Background(), env, path, coloTokenFile(t, f), doctorHint("suiteward", path, true))

	c := sizedConsole{termConsole: newTermConsole(&bytes.Buffer{}), w: 80, h: 24}
	scr, err := c.open(liveContext(t), strings.NewReader("\n"), "v0", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := showSummary(scr, func() []ui.KV { return rows }); err != nil {
		t.Fatal(err)
	}
	if err := scr.Close(); err != nil {
		t.Fatal(err)
	}
	out := c.out.String()
	page := pageText(t, out[:strings.LastIndex(out, leaveAlt)], 80, 24)
	for _, want := range []string{"Installation type co-located", "Instance ID 1a2b3c4d", "Status http://127.0.0.1:47081/status", "Doctor ", "failures", "Check again", "FAIL protection.Acme/api"} {
		if !strings.Contains(page, want) {
			t.Errorf("the summary page lacks %q at 80x24:\n%s", want, page)
		}
	}
}
