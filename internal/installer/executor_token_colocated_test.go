package installer

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The co-located install gives the service an executor token and keeps a copy for the executor, a file only this user reads, beside the copy of
// the MCP token (the executor runs as this user). Like the MCP token, the copy is written first and restricted, and the secret comes after it.

func executorTokenPath(h *harness) string { return filepath.Join(h.dir, "executor-token") }

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestInstallCreatesTheExecutorTokenSecretAndItsFileBeforeTheServiceStarts(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	secret := h.secretWrites(SecretExecutorAuth)
	if len(secret) != 1 || !hex64.MatchString(secret[0]) {
		t.Fatalf("executor token writes: %q", secret)
	}
	got, err := os.ReadFile(executorTokenPath(h))
	if err != nil || string(got) != secret[0] {
		t.Fatalf("the executor's copy %q does not hold the secret (%v)", got, err)
	}
	if mcp := h.secretWrites(SecretMCPAuth); len(mcp) != 1 || mcp[0] == secret[0] {
		t.Errorf("the executor token must not be the MCP token")
	}
	h.assertOrder("secret create suiteward-executor-token -", "run --detach --replace --name suiteward --network")
	serve := h.f.find("--name suiteward --network")[0].line()
	if !strings.Contains(serve, "--secret suiteward-executor-token,type=mount,target=/run/secrets/executor-token,uid=65532,mode=0400") ||
		!strings.Contains(serve, "SUITEWARD_EXECUTOR_TOKEN_FILE=/run/secrets/executor-token") {
		t.Errorf("serve does not get the executor token: %s", serve)
	}
	for _, name := range []string{"suiteward-db", "suiteward-migrate"} {
		if line := h.f.find("--name " + name + " ")[0].line(); strings.Contains(line, "executor") {
			t.Errorf("%s reaches the executor token: %s", name, line)
		}
	}
	h.assertNoLeak()
	if strings.Contains(h.out.String(), secret[0]) {
		t.Error("the executor token was printed")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(executorTokenPath(h)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the executor's copy: %v %v", info, err)
		}
	}
}

func TestInstallRestrictsTheExecutorTokenCopyToTheUserOnWindowsBeforeItIsWritten(t *testing.T) {
	h := newHarness(t, "windows")
	h.f.on("system connection list", machineConnections)
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	icacls, create := -1, -1
	for i, c := range h.f.calls {
		if c.name == "icacls" && c.args[0] == executorTokenPath(h) {
			if !slices.Equal(c.args, []string{executorTokenPath(h), "/inheritance:r", "/grant:r", "*" + testSID + ":F"}) {
				t.Errorf("icacls: %v", c.args)
			}
			icacls = i
		}
		if strings.Contains(c.line(), "secret create suiteward-executor-token") {
			create = i
		}
	}
	if icacls < 0 || create < icacls {
		t.Errorf("icacls at %d, executor token secret at %d", icacls, create)
	}
}

func TestInstallKeepsAnExecutorTokenThatExists(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(kindSecret, SecretExecutorAuth)
	if err := os.WriteFile(executorTokenPath(h), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if writes := h.secretWrites(SecretExecutorAuth); len(writes) != 0 {
		t.Errorf("the secret was written again: %q", writes)
	}
	if got, _ := os.ReadFile(executorTokenPath(h)); string(got) != "kept" {
		t.Errorf("the executor's copy was replaced: %q", got)
	}
}

func TestInstallWritesTheExecutorTokenCopyAgainFromItsSecret(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(kindSecret, SecretExecutorAuth)
	value := strings.Repeat("c", 64)
	h.f.on("secret inspect --showsecret --format {{.SecretData}} "+SecretExecutorAuth, value+"\n")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if writes := h.secretWrites(SecretExecutorAuth); len(writes) != 0 {
		t.Errorf("the secret was written again: %q", writes)
	}
	if got, err := os.ReadFile(executorTokenPath(h)); err != nil || string(got) != value {
		t.Errorf("the executor's copy is %q (%v), want the secret's value", got, err)
	}
	h.f.assertNoSecret(t, value)
}

func TestInstallRefusesAnExecutorTokenCopyThatIsAnotherInstancesBeforeChangingAnything(t *testing.T) {
	h := newHarness(t, "linux")
	if err := os.WriteFile(executorTokenPath(h), []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := h.podman()
	if err == nil || !strings.Contains(err.Error(), executorTokenPath(h)) || !strings.Contains(err.Error(), SecretExecutorAuth) {
		t.Fatalf("install = %v, want a refusal that names the file and the secret", err)
	}
	h.mustNotMutate()
	if got, _ := os.ReadFile(executorTokenPath(h)); string(got) != "theirs" {
		t.Errorf("the file was replaced: %q", got)
	}
}

// A service reads its secrets when it starts: a re-run that makes the executor token (an upgrade of a stack that predates it) runs the service
// again, and the database it did not touch is left alone.
func TestInstallDockerRerunRecreatesTheServiceWhenTheExecutorTokenIsMade(t *testing.T) {
	h := dockerRerun(t, stateHealthy, stateRunning)
	h.first("test -f /s/executor-token", "no")
	if err := os.Remove(executorTokenPath(h)); err != nil {
		t.Fatal(err)
	}
	if err := dockerInstall(t, h); err != nil {
		t.Fatal(err)
	}
	if !ranServe(h) || ran(h, ContainerDB) {
		t.Errorf("the service must be run again and the database must not:\n%s", strings.Join(h.f.lines(), "\n"))
	}
	if writes := h.secretWrites(SecretExecutorAuth); len(writes) != 1 || !hex64.MatchString(writes[0]) {
		t.Errorf("executor token writes: %q", writes)
	}
}
