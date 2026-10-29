package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What `suiteward doctor` says of the executor on the isolated tier. It runs as the service account, which cannot run nft, cannot reach the
// manager of the executor's account and cannot read its token: every check is built from what that account can read (the files the host step
// leaves world-readable, `id`, `loginctl`, `systemctl show` of a system unit, `pgrep`) and from what /status says the service saw.

// ownHash is the SHA-256 of the program that runs the test, which stands for the release's binary: doctor compares the executor's with its own.
func ownHash(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Clean(exe))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// isolatedStatus is /status of a service that has an executor token, which an executor last asked it for work sinceLastSeen ago.
func isolatedStatus(sinceLastSeen time.Duration) string {
	seen := time.Now().Add(-sinceLastSeen).UTC().Format(time.RFC3339)
	return isolatedStatusWith(fmt.Sprintf(`{"enabled":true,"jobs":{"pending":0,"leased":0,"complete":3,"unavailable":0,"cancelled":0},"lastSeenAt":%q}`, seen))
}

// isolatedStatusWith is the status of an isolated service with the given execution object.
func isolatedStatusWith(execution string) string {
	base := strings.Replace(okStatus, "co-located", "isolated", 1)
	return base[:len(base)-1] + `,"execution":` + execution + "}"
}

const (
	execManagerControllers = "/sys/fs/cgroup/user.slice/user-1002.slice/user@1002.service/cgroup.controllers"
	execSubIDs             = "suiteward:100000:65536\nsuiteward-exec:165536:65536\n"
)

// healthyIsolatedExecutor answers like a host whose executor is as the host step leaves it.
func healthyIsolatedExecutor(t *testing.T, s Settings) []rule {
	t.Helper()
	unit, err := RenderExecutorUnit(s.HTTPPort)
	if err != nil {
		t.Fatal(err)
	}
	own := ownHash(t)
	return []rule{
		{match: "executor execute$", out: "4242\n"}, // pgrep
		{match: "=-u " + ExecutorAccount, out: "1002\n"},
		{match: "=-u " + ServiceAccount, out: "1001\n"},
		{match: "=/etc/subuid", out: execSubIDs},
		{match: "=/etc/subgid", out: execSubIDs},
		{match: "=" + executorDropIn, out: executorDropInText},
		{match: "=" + execManagerControllers, out: "cpuset cpu io memory pids\n"},
		{match: "=" + executorRuleFile, out: executorRulesText("1002")},
		{match: "=show --property=ActiveState --value " + executorBlockUnit, out: "active\n"},
		{match: "=" + executorUnitPath, out: string(unit)},
		{match: "=" + executorPin, out: own + "  suiteward_v0.4.0_linux_amd64\n"},
		{match: "=" + executorBinary, out: own + "  " + executorBinary + "\n"},
	}
}

func TestDoctorIsolatedExecutorIsHealthy(t *testing.T) {
	f, s := isolatedRig{}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 0 {
		t.Errorf("exit %d, results %+v", code, res)
	}
	wantOK(t, res, "executor.account", "executor.delegation", "executor.network", "executor.unit", "executor.binary", "executor.running")
	if r := res["executor.binary"]; r.Warning || !strings.Contains(r.Detail, "suiteward_v0.4.0_linux_amd64") {
		t.Errorf("a binary that is the release's line of SHA256SUMS is a plain pass that names it: %+v", r)
	}
	if d := res["executor.network"].Detail; !strings.Contains(d, "not the loaded") {
		t.Errorf("the rule check must say that it reads the file and the unit and not the loaded table: %q", d)
	}
}

func TestDoctorIsolatedExecutorFailures(t *testing.T) {
	cases := []struct {
		name  string
		over  []rule
		check string
		want  []string
		fix   string
	}{
		{"no account", []rule{{match: "=-u " + ExecutorAccount, err: errors.New("id: no such user")}}, "executor.account", []string{ExecutorAccount}, "install host"},
		{"the same account", []rule{{match: "=-u " + ExecutorAccount, out: "1001\n"}}, "executor.account", []string{"same"}, "install host"},
		{"ranges that overlap", []rule{{match: "=/etc/subuid", out: "suiteward:100000:65536\nsuiteward-exec:150000:65536\n"}}, "executor.account", []string{"overlap"}, "install host"},
		{"no ranges", []rule{{match: "=/etc/subgid", out: "suiteward:100000:65536\n"}}, "executor.account", []string{"subgid"}, "install host"},
		{"no linger", []rule{{match: "show-user suiteward-exec", out: "Linger=no\n"}}, "executor.account", []string{"linger"}, "install host"},
		{"no drop-in", []rule{{match: "=" + executorDropIn, err: errors.New("cat: no such file")}}, "executor.delegation", []string{"delegate.conf"}, "install host"},
		{"a manager without cpu", []rule{{match: "=" + execManagerControllers, out: "memory pids\n"}}, "executor.delegation", []string{"cpu"}, "user@1002.service"},
		{"another rule file", []rule{{match: "=" + executorRuleFile, out: "table inet other {}\n"}}, "executor.network", []string{executorRuleFile}, "install host"},
		{"the rule unit is not active", []rule{{match: "=show --property=ActiveState --value " + executorBlockUnit, out: "inactive\n"}}, "executor.network", []string{executorBlockUnit, "inactive"}, "install host"},
		{"no unit", []rule{{match: "=" + executorUnitPath, err: errors.New("cat: no such file")}}, "executor.unit", []string{executorUnitPath}, "install host"},
		{"the unit names another port", []rule{{match: "=" + executorUnitPath, out: strings.Replace(mustUnit(t, 27081), "27081", "9", 1)}}, "executor.unit", []string{"127.0.0.1:9"}, "--http-port"},
		{"another unit", []rule{{match: "=" + executorUnitPath, out: strings.Replace(mustUnit(t, 0), "MemoryMax=3G", "MemoryMax=infinity", 1)}}, "executor.unit", []string{"not the unit file"}, "install host"},
		{"no binary", []rule{{match: "=" + executorBinary, err: errors.New("sha256sum: no such file")}}, "executor.binary", []string{executorBinary}, "install host"},
		{"a binary that changed since it was installed", []rule{{match: "=" + executorBinary, out: strings.Repeat("0", 64) + "  x\n"}}, "executor.binary", []string{"changed"}, "install host"},
		{"a binary of another build than this program", []rule{{match: "=" + executorPin, out: strings.Repeat("0", 64) + "  suiteward_v0.4.0_linux_amd64\n"}, {match: "=" + executorBinary, out: strings.Repeat("0", 64) + "  x\n"}}, "executor.binary", []string{"different build"}, "install host"},
		{"no pin", []rule{{match: "=" + executorPin, err: errors.New("cat: no such file")}}, "executor.binary", []string{executorPin}, "install host"},
		{"no process", []rule{{match: "executor execute$", err: errors.New("pgrep: exit status 1")}}, "executor.running", []string{"no process"}, "journalctl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := isolatedRig{over: c.over}.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, c.check, c.want...)
			if !strings.Contains(res[c.check].Fix, c.fix) {
				t.Errorf("fix %q does not mention %q", res[c.check].Fix, c.fix)
			}
			noBareSudoProgram(t, res[c.check].Fix)
			if fix := res[c.check].Fix; strings.Contains(fix, "install host") && !strings.Contains(fix, "--sha256sums") {
				t.Errorf("fix %q sends the administrator to a host step that refuses to run without --sha256sums", fix)
			}
		})
	}
}

func mustUnit(t *testing.T, port int) string {
	t.Helper()
	if port == 0 {
		port = DefaultHTTPPort
	}
	unit, err := RenderExecutorUnit(port)
	if err != nil {
		t.Fatal(err)
	}
	return string(unit)
}

// The pin of the isolated executor is the signed release's: a binary that no line of the release's SHA256SUMS lists (the pin carries the name of the
// copy and not of a release asset) fails the check, and the fix is the host step with the release's sums.
func TestDoctorIsolatedExecutorBinaryThatTheReleaseSumsDoNotListFails(t *testing.T) {
	f, s := isolatedRig{over: []rule{{match: "=" + executorPin, out: ownHash(t) + "  suiteward-executor\n"}}}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 1 {
		t.Errorf("exit %d, want 1: %+v", code, res)
	}
	wantFail(t, res, "executor.binary", "SHA256SUMS")
	noBareSudoProgram(t, res["executor.binary"].Fix)
	if fix := res["executor.binary"].Fix; !strings.Contains(fix, "install host") || !strings.Contains(fix, "--sha256sums") {
		t.Errorf("the fix %q must name the host step with --sha256sums", fix)
	}
}

func TestDoctorIsolatedExecutorRunning(t *testing.T) {
	cases := []struct {
		name string
		rig  isolatedRig
		want []string
		fix  string
	}{
		{"the service last saw an executor long ago", isolatedRig{lastSeen: 10 * time.Minute}, []string{"ago"}, "token"},
		{"the service has no executor token", isolatedRig{executionOff: true}, []string{"EXECUTOR_TOKEN"}, "install host"},
		{"the service is older than M2.1", isolatedRig{noExecution: true}, []string{"added the test runner"}, "install --upgrade"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := c.rig.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, "executor.running", c.want...)
			if !strings.Contains(res["executor.running"].Fix, c.fix) {
				t.Errorf("fix %q does not mention %q", res["executor.running"].Fix, c.fix)
			}
		})
	}
}

func TestDoctorIsolatedExecutorSaysWhenItWasLastSeen(t *testing.T) {
	f, s := isolatedRig{lastSeen: 12 * time.Second}.build(t)
	_, res, _ := runDoctor(t, f, s)
	if d := res["executor.running"].Detail; !strings.Contains(d, "last seen") || !strings.Contains(d, "ago") {
		t.Errorf("the detail must report when the service last saw the executor: %q", d)
	}
}

// The service of an isolated host that has no executor token gets it from an install of the service account's, not from an upgrade, which at the
// installed release only says that there is nothing to upgrade and names the install: the fix says `install --tier isolated`.
func TestDoctorIsolatedNoTokenFixNamesTheInstallOfTheIsolatedTier(t *testing.T) {
	f, s := isolatedRig{executionOff: true}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantFail(t, res, "executor.running", "EXECUTOR_TOKEN")
	fix := res["executor.running"].Fix
	if !strings.Contains(fix, "install --tier isolated") || strings.Contains(fix, "--upgrade") {
		t.Errorf("the fix %q must name `install --tier isolated`, and not an upgrade that does nothing here", fix)
	}
}
