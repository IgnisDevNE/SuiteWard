package installer

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// The service account's half of the executor on the isolated tier: the host step (run as root, host_executor_test.go) leaves a copy of the
// executor's token that this account owns, and the install turns it into the secret that the service reads. Without that copy there is no token to
// give, and the service runs without an executor: an install that names no execution profile is unchanged.

var executorTokenValue = strings.Repeat("ab", 32)

func writeHandover(t *testing.T, home, token string) {
	t.Helper()
	path := filepath.Join(home, ".config", "suiteward", executorTokenHandover)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func serveUnitOf(t *testing.T, home string) []string {
	t.Helper()
	return configLines(readFileOrFail(t, filepath.Join(unitDir(home), "suiteward.container")))
}

func TestInstallIsolatedGivesTheServiceTheExecutorTokenOfTheHostStep(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	writeHandover(t, home, executorTokenValue+"\n") // a trailing newline is not part of the token
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	if f.secrets[SecretExecutorAuth] != executorTokenValue {
		t.Fatalf("the secret holds %q, want the token of the host step", f.secrets[SecretExecutorAuth])
	}
	for _, c := range f.calls {
		for _, arg := range c.args {
			if strings.Contains(arg, executorTokenValue) {
				t.Errorf("the token is in an argument of %s: %v", c.name, c.args)
			}
		}
	}
	serve := serveUnitOf(t, home)
	for _, line := range executorUnitLines {
		if !slices.Contains(serve, line) {
			t.Errorf("the service unit lacks %s", line)
		}
	}
	if create, reload := f.index("secret create "+SecretExecutorAuth+" -", 0), f.index("systemctl --user daemon-reload", 0); create < 0 || reload < create {
		t.Errorf("the secret (%d) must exist before the unit that mounts it is loaded (%d)", create, reload)
	}
}

func TestInstallIsolatedWithoutTheExecutorTokenRunsWithoutTheExecutorLines(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	out, err := runIsolated(t, f, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, made := f.secrets[SecretExecutorAuth]; made {
		t.Error("a secret was made with no token to give")
	}
	for _, line := range serveUnitOf(t, home) {
		if strings.Contains(line, "executor") {
			t.Errorf("the service unit mounts a secret that does not exist: %s", line)
		}
	}
	if !strings.Contains(out, "install host") || !strings.Contains(out, "executor") {
		t.Errorf("the output must say that the administrator's host step makes the executor, and name it:\n%s", out)
	}
}

func TestInstallIsolatedRefusesAnExecutorTokenThatIsTooShortBeforeChangingAnything(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	writeHandover(t, home, "short")
	_, err := runIsolated(t, f, nil, "")
	if err == nil || !strings.Contains(err.Error(), executorTokenHandover) || strings.Contains(err.Error(), "short\"") {
		t.Fatalf("install = %v, want a refusal that names the file and not its content", err)
	}
	if f.has("secret create") || f.has("daemon-reload") {
		t.Errorf("something was changed: %v", f.lines())
	}
}

// runningStack is an installed isolated stack that runs, as the "nothing changed" row of the restart table of the install has it.
func runningStack(t *testing.T, edit func(p *UnitParams)) (*isoFake, string) {
	t.Helper()
	terminalForSetup(t)
	home := t.TempDir()
	p := UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort, EnvFile: DefaultEnvFile}
	if edit != nil {
		edit(&p)
	}
	installedUnits(t, home, p)
	writeAccountEnvFile(t, home, flowAppEnvFile)
	f := newIsoFake(home)
	f.serviceRunning, f.everActive = true, true
	f.secrets[SecretGitHubAppKey] = "existing"
	return f, home
}

func TestInstallIsolatedLeavesAnExecutorTokenSecretThatIsTheHostStepsAlone(t *testing.T) {
	f, home := runningStack(t, nil)
	f.secrets[SecretExecutorAuth] = executorTokenValue
	writeHandover(t, home, executorTokenValue)
	g := isoGitHubOn(t, f)
	if err := g.run(); err != nil {
		t.Fatalf("%v\n%s", err, g.out.String())
	}
	if f.has("secret create --replace " + SecretExecutorAuth) {
		t.Errorf("a secret that is the token is written again: %v", f.lines())
	}
	if f.has("systemctl --user restart suiteward.service") {
		t.Errorf("nothing the service reads changed, so it is not restarted: %v", f.lines())
	}
}

func TestInstallIsolatedReplacesAnExecutorTokenSecretThatIsNotTheHostStepsAndRestartsTheService(t *testing.T) {
	f, home := runningStack(t, nil)
	f.secrets[SecretExecutorAuth] = strings.Repeat("cd", 32) // the host step made a new token: the old secret would lock the executor out
	writeHandover(t, home, executorTokenValue)
	g := isoGitHubOn(t, f)
	if err := g.run(); err != nil {
		t.Fatalf("%v\n%s", err, g.out.String())
	}
	if f.secrets[SecretExecutorAuth] != executorTokenValue || !f.has("secret create --replace "+SecretExecutorAuth+" -") {
		t.Errorf("the secret holds %q: %v", f.secrets[SecretExecutorAuth], f.lines())
	}
	if !f.has("systemctl --user restart suiteward.service") {
		t.Errorf("the service reads its secrets when it starts, so a new one needs a restart: %v", f.lines())
	}
}

func TestInstallIsolatedAdoptsAStackThatPredatesTheExecutorAndGivesItTheToken(t *testing.T) {
	f, home := runningStack(t, func(p *UnitParams) { p.NoExecutor = true })
	writeHandover(t, home, executorTokenValue)
	g := isoGitHubOn(t, f)
	if err := g.run(); err != nil {
		t.Fatalf("an installed unit of an earlier release is a known shape: %v\n%s", err, g.out.String())
	}
	serve := serveUnitOf(t, home)
	for _, line := range executorUnitLines {
		if !slices.Contains(serve, line) {
			t.Errorf("the service unit lacks %s", line)
		}
	}
	if f.secrets[SecretExecutorAuth] != executorTokenValue || !f.has("systemctl --user restart suiteward.service") {
		t.Errorf("the service must be restarted to mount the secret: %v", f.lines())
	}
}

func TestInstallIsolatedNamesTheHostStepWhenTheHTTPPortChanges(t *testing.T) {
	home := t.TempDir()
	f := newIsoFake(home)
	installedStackUnits(t, home) // ports 18081 and 18091
	writeAccountEnvFile(t, home, flowAppEnvFile)
	f.secrets[SecretGitHubAppKey] = "existing"
	out, err := installIsolatedArgs(t, f, "--http-port", "19081")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "install host --http-port 19081") {
		t.Errorf("the executor is pointed at the old port by the administrator's step: the output must name the re-run with the new port:\n%s", out)
	}
}

func TestUpgradeIsolatedGivesAStackThatPredatesTheExecutorItsTokenAndMountsIt(t *testing.T) {
	u := newIsoUpgrade(t, func(p *UnitParams) { p.NoExecutor = true })
	writeHandover(t, u.home, executorTokenValue)
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, u.out.String())
	}
	if u.f.secrets[SecretExecutorAuth] != executorTokenValue {
		t.Errorf("the secret holds %q", u.f.secrets[SecretExecutorAuth])
	}
	serve := configLines([]byte(u.unit("suiteward.container")))
	for _, line := range executorUnitLines {
		if !slices.Contains(serve, line) {
			t.Errorf("the upgraded service unit lacks %s", line)
		}
	}
	if create, stop := u.f.index("secret create "+SecretExecutorAuth+" -", 0), u.f.index("systemctl --user start suiteward.service", 0); create < 0 || stop < create {
		t.Errorf("the secret (%d) must exist before the service starts with it mounted (%d)", create, stop)
	}
}

func TestUpgradeIsolatedWithoutAnExecutorTokenWarnsAndLeavesTheServiceWithoutTheExecutor(t *testing.T) {
	u := newIsoUpgrade(t, func(p *UnitParams) { p.NoExecutor = true })
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, u.out.String())
	}
	for _, line := range configLines([]byte(u.unit("suiteward.container"))) {
		if strings.Contains(line, "executor") {
			t.Errorf("the upgraded unit mounts a secret that does not exist: %s", line)
		}
	}
	if !strings.Contains(u.out.String(), "install host") {
		t.Errorf("the output must say that the host step makes the executor:\n%s", u.out.String())
	}
}

func TestUninstallIsolatedSaysThatTheExecutorIsTheAdministratorsAndHowToStopIt(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	if err := u.uninstall("--yes"); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	for _, want := range []string{"suiteward-exec", "systemctl --user --machine=suiteward-exec@.host disable --now suiteward-executor.service"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if u.f.has("--machine=") {
		t.Errorf("the service account cannot reach the executor's manager, and uninstall does not try: %v", u.f.lines())
	}
}

func TestUninstallIsolatedPurgeRemovesTheCopyOfTheExecutorToken(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	writeHandover(t, u.home, executorTokenValue)
	if err := u.uninstall("--purge", "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(u.home, ".config", "suiteward", executorTokenHandover)) {
		t.Error("the copy of the executor's token is still there")
	}
	if !u.f.has("secret rm --ignore " + SecretExecutorAuth) {
		t.Error("the secret was not removed")
	}
	u2 := newIsoUpgrade(t, nil)
	writeHandover(t, u2.home, executorTokenValue)
	if err := u2.uninstall("--yes"); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(u2.home, ".config", "suiteward", executorTokenHandover)) {
		t.Error("an uninstall without --purge removed the copy of the token")
	}
}
