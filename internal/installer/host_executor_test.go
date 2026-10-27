package installer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// The host step of the executor (ADR 0028 section 10; spike S4, sections 4.1, 4.5 and 4.6): an account of its own, the cgroup controllers that
// its limits need, the metadata addresses refused for it by destination, a token that only it reads, a root-owned binary it cannot change, and
// a systemd user unit that runs it. The fake machine answers like a healthy host unless a test says otherwise.

const (
	execAccountName  = "suiteward-exec"
	execDropIn       = "/etc/systemd/system/user@.service.d/delegate.conf"
	execControllers  = "/sys/fs/cgroup/user.slice/user-1002.slice/user@1002.service/cgroup.controllers"
	execRuleFile     = "/etc/suiteward/exec-block.nft"
	execBlockUnit    = "/etc/systemd/system/suiteward-exec-block.service"
	execAuthEtc      = "/etc/suiteward/executor-token"
	execTokenHandOff = "/home/suiteward/.config/suiteward/executor-token"
	execLibDir       = "/usr/local/libexec/suiteward"
	execBinaryPath   = execLibDir + "/suiteward-executor"
	execPinFile      = execLibDir + "/executor.pin"
	execUnitFile     = "/etc/systemd/user/suiteward-executor.service"
	execUserSystemd  = "--user --machine=suiteward-exec@.host"
)

// executorPaths are the files of the executor that the fake answers `cat` for; a path that is not in files is not there.
const execSums = "/root/release/SHA256SUMS"

var executorPaths = []string{execDropIn, execRuleFile, execBlockUnit, execPinFile, execUnitFile, execSums,
	"/home/suiteward/.config/suiteward/settings.yaml", "/home/suiteward/.config/containers/systemd/suiteward.container"}

func writeProgram(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suiteward")
	if err := os.WriteFile(path, []byte("the program of this run"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (h *hostFake) answerExecutor(c call) (out []byte, handled bool, err error) {
	line := c.line()
	fail := errors.New(c.name + ": exit status 1")
	switch {
	case c.name == "id" && line == "-u "+execAccountName:
		if !h.execAccount {
			return nil, true, fail
		}
		return []byte("1002\n"), true, nil
	case c.name == "useradd" && strings.HasSuffix(line, " "+execAccountName):
		h.execAccount = true
		return nil, true, nil
	case c.name == "cat" && line == execControllers:
		return []byte(h.controllers + "\n"), true, nil
	case c.name == "cat" && slices.Contains(executorPaths, line):
		if content, ok := h.files[line]; ok {
			return []byte(content), true, nil
		}
		return nil, true, fail
	case c.name == "nft" && line == "list table inet suiteward_exec":
		return []byte("table inet suiteward_exec { chain output { meta skuid 1002 ip daddr 169.254.169.254 reject meta skuid 1002 ip6 daddr fd00:ec2::254 reject } }"), true, nil
	case c.name == "sh" && strings.Contains(line, "cat >") && strings.HasSuffix(line, execAuthEtc):
		h.paths[execAuthEtc] = true // the token is made
		return nil, true, nil
	case c.name == "sha256sum":
		if sum, ok := h.hashes[line]; ok {
			return []byte(sum + "  " + line + "\n"), true, nil
		}
		return nil, true, fail
	case c.name == "install" && strings.HasPrefix(line, "-m 0755 -o root -g root "):
		fields := strings.Fields(line)
		src, dest := fields[len(fields)-2], fields[len(fields)-1]
		if raw, readErr := os.ReadFile(filepath.Clean(src)); readErr == nil {
			sum := sha256.Sum256(raw)
			h.hashes[dest] = hex.EncodeToString(sum[:])
		}
		if h.tornCopy {
			h.hashes[dest] = strings.Repeat("9", 64)
		}
		return nil, true, nil
	}
	return h.answerExecutorImage(c)
}

// answerExecutorImage answers the pull of the Go image as the executor's account, and the check that it is there.
func (h *hostFake) answerExecutorImage(c call) (out []byte, handled bool, err error) {
	line := c.line()
	switch {
	case c.name == "systemd-run" && strings.Contains(line, execUserSystemd) && strings.Contains(line, "podman pull "+goprofile.Image):
		if h.pullFails {
			return nil, true, errors.New(c.name + ": exit status 1")
		}
		h.imagePulled = !h.pullLeavesNothing
		return nil, true, nil
	case c.name == "systemd-run" && strings.Contains(line, execUserSystemd) && strings.Contains(line, "podman image exists "+goprofile.Image):
		if !h.imagePulled {
			return nil, true, errors.New(c.name + ": exit status 1")
		}
		return nil, true, nil
	}
	return h.answerExecutorUnits(c)
}

// answerExecutorUnits answers what the host step asks of systemd about the executor.
func (h *hostFake) answerExecutorUnits(c call) (out []byte, handled bool, err error) {
	line := c.line()
	switch {
	case c.name != "systemctl":
		return nil, false, nil
	case line == "restart user@1002.service":
		h.controllers = h.afterRestart
		return nil, true, nil
	case strings.Contains(line, execUserSystemd) && strings.Contains(line, "enable --now suiteward-executor.service"):
		h.execEnabled = true
		return nil, true, nil
	case strings.Contains(line, execUserSystemd) && strings.Contains(line, "show") && strings.HasSuffix(line, "suiteward-executor.service"):
		if !h.execRunning && !h.execEnabled {
			return []byte("inactive\n"), true, nil // a unit that was not there before the step
		}
		return []byte(h.execUnitState + "\n"), true, nil
	}
	return nil, false, nil
}

func hostCallIndex(h *hostFake, name, text string) int {
	for i, c := range h.calls {
		if c.name == name && strings.Contains(c.line(), text) {
			return i
		}
	}
	return -1
}

func writeOf(h *hostFake, path string) string {
	for _, c := range h.callsTo("tee") {
		if c.line() == path {
			return c.stdin
		}
	}
	return ""
}

func TestPrepareHostCreatesTheExecutorAccountApartFromTheServiceAccount(t *testing.T) {
	h := newHostFake()
	h.execAccount = false
	h.subuid, h.subgid = "suiteward:100000:65536\n", "suiteward:100000:65536\n"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("useradd", "--create-home --shell /usr/sbin/nologin suiteward-exec") {
		t.Fatalf("the executor needs an account of its own: %v", h.lines())
	}
	usermod := h.callsTo("usermod")
	if len(usermod) != 1 || !strings.Contains(usermod[0].line(), "--add-subuids 165536-231071") || !strings.Contains(usermod[0].line(), "--add-subgids 165536-231071") || !strings.HasSuffix(usermod[0].line(), " suiteward-exec") {
		t.Fatalf("its ranges must start after the service account's and overlap none: %v", h.lines())
	}
	if !h.has("loginctl", "enable-linger suiteward-exec") {
		t.Fatalf("linger is what starts its unit at boot: %v", h.lines())
	}
	h2 := newHostFake()
	if _, err := runHost(t, h2, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if h2.has("useradd", "") {
		t.Fatalf("an account that exists is not created again: %v", h2.lines())
	}
}

func TestPrepareHostWritesTheDelegationBeforeAnyAccountLingers(t *testing.T) {
	h := newHostFake()
	h.account, h.execAccount = false, false
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := writeOf(h, execDropIn); got != "[Service]\nDelegate=cpu cpuset io memory pids\n" {
		t.Fatalf("the drop-in of user@.service is %q", got)
	}
	drop, reload, linger := hostCallIndex(h, "tee", execDropIn), hostCallIndex(h, "systemctl", "daemon-reload"), hostCallIndex(h, "loginctl", "enable-linger")
	if drop < 0 || reload < drop || linger < reload {
		t.Fatalf("the drop-in (%d) and the reload (%d) must come before the first linger (%d), which starts a user manager: restarting one stops its containers\n%v", drop, reload, linger, h.lines())
	}
	h = newHostFake()
	h.files[execDropIn] = "[Service]\nDelegate=cpu cpuset io memory pids\n"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if writeOf(h, execDropIn) != "" {
		t.Errorf("a drop-in that is there is not written again: %v", h.lines())
	}
}

func TestPrepareHostRestartsOnlyTheManagerOfTheExecutorWhenCPUIsNotDelegated(t *testing.T) {
	h := newHostFake()
	h.controllers = "memory pids"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("systemctl", "restart user@1002.service") {
		t.Fatalf("a manager without cpu is restarted: %v", h.lines())
	}
	if h.has("systemctl", "user@1001.service") {
		t.Fatalf("the manager of the service account stops the containers of the stack and is never restarted: %v", h.lines())
	}

	h = newHostFake()
	h.controllers, h.afterRestart = "memory pids", "memory pids"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || !strings.Contains(err.Error(), "cpu") {
		t.Fatalf("a manager that still lacks cpu after the restart must stop the run naming it, got %v", err)
	}
}

func TestPrepareHostRejectsTheMetadataAddressesForTheExecutorByDestinationWithoutAsking(t *testing.T) {
	h := newHostFake() // nothing answers at the metadata address: the question of the service account's rule is not asked
	if _, err := runHost(t, h, nil, false, ""); err != nil {
		t.Fatal(err)
	}
	rule := writeOf(h, execRuleFile)
	for _, want := range []string{
		"table inet suiteward_exec", "delete table inet suiteward_exec",
		"meta skuid 1002 ip daddr 169.254.169.254 reject", "meta skuid 1002 ip6 daddr fd00:ec2::254 reject",
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("the rule file lacks %q:\n%s", want, rule)
		}
	}
	for _, banned := range []string{"dport", "tcp", "udp"} {
		if strings.Contains(rule, banned) {
			t.Errorf("the rule matches a port (%s), so a service on another port stays reachable:\n%s", banned, rule)
		}
	}
	unit := writeOf(h, execBlockUnit)
	for _, want := range []string{"ExecStart=/usr/sbin/nft -f /etc/suiteward/exec-block.nft", "Before=systemd-user-sessions.service", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit lacks %q:\n%s", want, unit)
		}
	}
	if !h.has("systemctl", "enable --now suiteward-exec-block.service") || !h.has("nft", "list table inet suiteward_exec") {
		t.Fatalf("the unit must be enabled and the loaded table read back: %v", h.lines())
	}
	if writeOf(h, imdsRuleFile) != "" {
		t.Errorf("the consented rule of the service account is not touched: %v", h.lines())
	}
	// The rules load before the executor can run anything.
	if rules, start := hostCallIndex(h, "systemctl", "enable --now suiteward-exec-block.service"), hostCallIndex(h, "systemctl", execUserSystemd+" enable --now suiteward-executor.service"); rules < 0 || start < rules {
		t.Errorf("the rules (%d) must be loaded before the executor starts (%d)", rules, start)
	}
}

func TestPrepareHostFailsWhenTheExecutorRulesAreNotLoaded(t *testing.T) {
	h := newHostFake()
	h.failOn = map[string]error{"nft list table inet suiteward_exec": errInjected}
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || !strings.Contains(err.Error(), "executor rule is not loaded") {
		t.Fatalf("rules that are not loaded must stop the run before the executor starts, got %v", err)
	}
	if h.has("systemctl", "suiteward-executor.service") {
		t.Errorf("the executor was started although its rules are not loaded: %v", h.lines())
	}
}

func TestPrepareHostMakesTheExecutorTokenThatOnlyTheExecutorReads(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	var token string
	for _, c := range h.callsTo("sh") {
		if strings.Contains(c.line(), "umask 077") && strings.Contains(c.line(), execAuthEtc) {
			token = c.stdin
		}
	}
	if !hex64.MatchString(token) {
		t.Fatalf("the token must be written through stdin, 64 hex characters, with a restrictive umask: %v", h.lines())
	}
	for _, c := range h.calls {
		for _, arg := range c.args {
			if strings.Contains(arg, token) {
				t.Errorf("the token is in an argument of %s: %v", c.name, c.args)
			}
		}
	}
	if !h.has("chown", "suiteward-exec "+execAuthEtc) || !h.has("chmod", "0400 "+execAuthEtc) {
		t.Fatalf("the file belongs to the executor and nobody else reads it: %v", h.lines())
	}
}

func TestPrepareHostReusesAnExecutorTokenThatExists(t *testing.T) {
	h := newHostFake()
	h.paths[execAuthEtc] = true
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.callsTo("sh") {
		if strings.Contains(c.line(), execAuthEtc) {
			t.Fatalf("a token that exists is never made again: %v", h.lines())
		}
	}
	if !h.has("chown", "suiteward-exec "+execAuthEtc) || !h.has("chmod", "0400 "+execAuthEtc) {
		t.Errorf("the owner and the mode of an existing token are put right: %v", h.lines())
	}
}

func TestPrepareHostHandsTheTokenToTheServiceAccountAsItHandsTheEnvFile(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("install", "-o suiteward -g suiteward -m 0600 "+execAuthEtc+" "+execTokenHandOff) {
		t.Fatalf("the service account gets a copy it owns, made by install and never by a write of root into its directory: %v", h.lines())
	}
	if !h.has("test", "-L "+execTokenHandOff) {
		t.Errorf("root must not copy through a link the account could plant: %v", h.lines())
	}

	h = newHostFake()
	h.symlinks[execTokenHandOff] = true
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a link at the copy must stop the run, got %v", err)
	}

	h = newHostFake()
	h.paths[execTokenHandOff] = true
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if h.has("install", "-m 0600 "+execAuthEtc) {
		t.Errorf("an identical copy is left alone: %v", h.lines())
	}
}

func TestPrepareHostInstallsTheExecutorBinaryRootOwnedAndRecordsItsPin(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("install", "-d -m 0755 -o root -g root "+execLibDir) {
		t.Fatalf("its directory is root's and nobody else writes it: %v", h.lines())
	}
	if !h.has("install", "-m 0755 -o root -g root "+h.exe+" "+execBinaryPath) {
		t.Fatalf("the binary is a root-owned copy of this program: %v", h.lines())
	}
	sum := sha256File(t, h.exe)
	if got := writeOf(h, execPinFile); got != sum+"  suiteward_v0.4.0_linux_amd64\n" {
		t.Errorf("the pin is %q, want the line of the release's SHA256SUMS that lists the program", got)
	}
}

func TestPrepareHostCopiesTheBinaryOnlyWhenItDiffers(t *testing.T) {
	h := newHostFake()
	h.exe = writeProgram(t)
	h.hashes[execBinaryPath] = sha256File(t, h.exe)
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if h.has("install", "-m 0755 -o root -g root "+h.exe) {
		t.Errorf("the same binary is not copied again: %v", h.lines())
	}
}

func TestPrepareHostRefusesACopyThatIsNotTheProgram(t *testing.T) {
	h := newHostFake()
	h.tornCopy = true // install succeeds, and what it left is not what it was given
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || !strings.Contains(err.Error(), "executor") || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("a copy whose hash is not the program's must stop the run, got %v", err)
	}
	if h.has("systemctl", "suiteward-executor.service") {
		t.Errorf("the executor was started after a bad copy: %v", h.lines())
	}
}

func TestPrepareHostPinsTheBinaryToTheReleaseSums(t *testing.T) {
	h := newHostFake()
	h.exe = writeProgram(t)
	sum := sha256File(t, h.exe)
	line := sum + "  suiteward_v0.4.0_linux_amd64"
	h.files[execSums] = strings.Join([]string{strings.Repeat("a", 64) + "  suiteward_v0.4.0_windows_amd64.exe", line, strings.Repeat("b", 64) + "  install.sh"}, "\n") + "\n"
	if _, err := runHost(t, h, map[string]string{"metadata-rule": "no", "sha256sums": execSums}, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := writeOf(h, execPinFile); got != line+"\n" {
		t.Errorf("the pin is %q, want the line of the release's SHA256SUMS", got)
	}
}

func TestPrepareHostRefusesABinaryThatTheSumsDoNotList(t *testing.T) {
	h := newHostFake()
	h.files[execSums] = strings.Repeat("a", 64) + "  suiteward_v0.4.0_linux_amd64\n"
	_, err := runHost(t, h, map[string]string{"metadata-rule": "no", "sha256sums": execSums}, false, "")
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("a program that the release does not list must be refused naming SHA256SUMS, got %v", err)
	}
	if h.has("install", "-m 0755 -o root -g root") || h.has("systemctl", "suiteward-executor.service") {
		t.Errorf("nothing of the executor is installed or started: %v", h.lines())
	}
	for _, c := range h.calls { // the refusal comes before every step: podman, the accounts, the rules, the token
		if c.name != "id" && c.name != "cat" {
			t.Errorf("a step ran before the refusal: %v", h.lines())
		}
	}
}

func TestPrepareHostRefusesWithoutTheReleasesSHA256SUMSBeforeChangingAnything(t *testing.T) {
	h := newHostFake()
	h.exe = writeProgram(t)
	_, err := runHostAsGiven(t, h, declineMetadataRule, false, "")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("the step refuses a command line that lacks --sha256sums as a usage error, got %v", err)
	}
	for _, want := range []string{"--sha256sums", "SHA256SUMS", "release"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q lacks %q: it names the flag and says where the file comes from", err, want)
		}
	}
	for _, c := range h.calls {
		if c.name != "id" {
			t.Errorf("a command ran before the refusal: %v", h.lines())
		}
	}
}

func TestInstallHostWithoutTheSHA256SUMSExitsWithTheUsageCode(t *testing.T) {
	h := newHostFake()
	h.exe = writeProgram(t)
	var out, stderr bytes.Buffer
	env := Env{Deps: h.deps(), Term: Terminal{In: strings.NewReader(""), Out: &out}, OpenScreen: openScreenOn(&out, nil), PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {}}
	if code := Main(liveContext(t), []string{"install", "host", "--plain", "--metadata-rule", "no"}, env, &stderr); code != 2 || !strings.Contains(stderr.String(), "--sha256sums") {
		t.Errorf("exit %d, stderr %q: want 2 and the flag named", code, stderr.String())
	}
}

func TestPrepareHostWritesTheExecutorUnitForTheUserManagerOfTheExecutorOnly(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	unit := writeOf(h, execUnitFile)
	lines := configLines([]byte(unit))
	for _, want := range []string{
		"ConditionUser=suiteward-exec",
		"ExecStart=/usr/local/libexec/suiteward/suiteward-executor execute",
		"Environment=SUITEWARD_EXECUTOR_URL=http://127.0.0.1:27081",
		"Environment=SUITEWARD_EXECUTOR_TOKEN_FILE=/etc/suiteward/executor-token",
		"Environment=SUITEWARD_EXECUTOR_RUNTIME=podman",
		"MemoryMax=3G", "MemorySwapMax=0", "Environment=GOMEMLIMIT=2500MiB", "KillMode=mixed", "TimeoutStopSec=45", "Restart=always", "WantedBy=default.target",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("the unit lacks %q:\n%s", want, unit)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "NoNewPrivileges") {
			t.Errorf("%s would stop newuidmap, so rootless Podman could not run", line)
		}
	}
	for _, want := range []string{"daemon-reload", "enable --now suiteward-executor.service"} {
		if !h.has("systemctl", execUserSystemd+" "+want) {
			t.Errorf("missing systemctl %s %s: %v", execUserSystemd, want, h.lines())
		}
	}
	for _, c := range h.callsTo("systemctl") {
		if strings.Contains(c.line(), "--global") {
			t.Errorf("a global enable would start the executor for every account: %v", c.args)
		}
	}
}

func TestPrepareHostPointsTheExecutorAtThePortOfTheServiceItWillFind(t *testing.T) {
	cases := []struct {
		name  string
		flags map[string]string
		files map[string]string
		port  string
	}{
		{"the default", nil, nil, "27081"},
		{"the flag", map[string]string{"http-port": "18081"}, nil, "18081"},
		{"the settings of the service account", nil, map[string]string{"/home/suiteward/.config/suiteward/settings.yaml": "tier: isolated\nhttp_port: 19081\nmcp_port: 19091\n"}, "19081"},
		{"the unit of a host installed by hand", nil, map[string]string{"/home/suiteward/.config/containers/systemd/suiteward.container": "PublishPort=127.0.0.1:8081:8080\nPublishPort=127.0.0.1:8091:8090\n"}, "8081"},
		{"the flag over the settings", map[string]string{"http-port": "18081"}, map[string]string{"/home/suiteward/.config/suiteward/settings.yaml": "http_port: 19081\n"}, "18081"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHostFake()
			for path, content := range tc.files {
				h.files[path] = content
			}
			flags := map[string]string{"metadata-rule": "no"}
			for k, v := range tc.flags {
				flags[k] = v
			}
			if _, err := runHost(t, h, flags, false, ""); err != nil {
				t.Fatal(err)
			}
			if want := "Environment=SUITEWARD_EXECUTOR_URL=http://127.0.0.1:" + tc.port; !slices.Contains(configLines([]byte(writeOf(h, execUnitFile))), want) {
				t.Errorf("the unit lacks %s:\n%s", want, writeOf(h, execUnitFile))
			}
		})
	}
}

func TestPrepareHostRestartsAnExecutorThatRunsOldFiles(t *testing.T) {
	h := newHostFake()
	h.exe = writeProgram(t)
	h.hashes[execBinaryPath] = strings.Repeat("0", 64) // another build of the program is installed
	h.execRunning = true
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("systemctl", execUserSystemd+" restart suiteward-executor.service") {
		t.Errorf("an executor that runs a replaced binary is restarted: %v", h.lines())
	}
}

func TestPrepareHostFailsWhenTheExecutorUnitIsNotActiveAfterTheStart(t *testing.T) {
	h := newHostFake()
	h.execUnitState = "failed"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || !strings.Contains(err.Error(), "suiteward-executor.service") || !strings.Contains(err.Error(), "journalctl") {
		t.Fatalf("a unit that is not active must be reported with where to read why, got %v", err)
	}
}

func TestTheExecutorUnitIsNotAQuadlet(t *testing.T) {
	templates, err := embeddedUnits()
	if err != nil {
		t.Fatal(err)
	}
	for name := range templates {
		if strings.HasSuffix(name, ".service") || strings.Contains(name, "executor") {
			t.Errorf("deploy/quadlet holds %s: the units of the service account's directory, the quadlet dry run and the adoption read every file of it", name)
		}
	}
}

// A host whose administrator has umask 077 makes every file that root writes 0600, and sudo keeps the umask: the files that another account reads
// (the executor's manager reads its unit, the service account's doctor reads the rest) are made readable on purpose.
func TestPrepareHostMakesTheFilesThatOtherAccountsReadReadable(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{execDropIn, execRuleFile, execUnitFile, execPinFile} {
		write, mode := hostCallIndex(h, "tee", path), hostCallIndex(h, "chmod", "0644 "+path)
		if write < 0 || mode < write {
			t.Errorf("%s must be made 0644 after it is written (tee at %d, chmod at %d)\n%v", path, write, mode, h.lines())
		}
	}
	for _, dir := range []string{"/etc/suiteward", "/etc/systemd/user", "/etc/systemd/system/user@.service.d"} {
		if !h.has("chmod", "0755 "+dir) {
			t.Errorf("%s is not made 0755: the token of the executor lives in the first, and the others are read by other accounts' managers: %v", dir, h.lines())
		}
	}
}

// The executor reads its token only when it starts, so a running executor that the step gives a new token is restarted, with nothing else changed.
func TestPrepareHostRestartsARunningExecutorOnlyWhenItMakesANewToken(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tokenThere bool
		restarted  bool
	}{{"a new token", false, true}, {"a token that exists", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHostFake()
			h.exe = writeProgram(t)
			unit, err := RenderExecutorUnit(DefaultHTTPPort)
			if err != nil {
				t.Fatal(err)
			}
			h.files[execUnitFile] = string(unit) // the unit and the binary are those of this release already
			h.hashes[execBinaryPath] = sha256File(t, h.exe)
			h.execRunning = true
			h.paths[execAuthEtc] = tc.tokenThere
			if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
				t.Fatal(err)
			}
			if got := h.has("systemctl", execUserSystemd+" restart suiteward-executor.service"); got != tc.restarted {
				t.Errorf("restarted %v, want %v: %v", got, tc.restarted, h.lines())
			}
		})
	}
}
