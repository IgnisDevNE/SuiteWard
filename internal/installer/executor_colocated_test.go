package installer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// The executor on the co-located tier (ADR 0028, section 10; spike S4, section 5): a host process of this user that runs `suiteward execute`,
// started by the mechanism each operating system has for a program that starts at logon and is kept running: a scheduled task on Windows, a
// launch agent on macOS, a systemd user unit on Linux. It runs a copy of this program that lives beside the token files, so that a program
// that the user moves or deletes does not take the executor with it.

// isOSCall says whether a command is one that keeps the executor running, which the operating system answers and no container engine does.
func isOSCall(c call) bool {
	return slices.Contains([]string{"systemctl", "schtasks", "tasklist", "launchctl", "id"}, c.name)
}

func (h *harness) executorDir() string { return filepath.Join(h.dir, "executor") }

func (h *harness) executorBinary() string {
	name := "suiteward-executor"
	if h.f.goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(h.executorDir(), name)
}

func (h *harness) readHome(rel string) string {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.dir, rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}

// machineHarnessOn is a harness of an operating system that runs Podman in a machine.
func machineHarnessOn(t *testing.T, goos string) *harness {
	t.Helper()
	h := newHarness(t, goos)
	h.f.on("system connection list", machineConnections)
	return h
}

func TestInstallCopiesTheProgramAsTheExecutorAndRecordsItsPin(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(h.program())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(h.executorBinary())
	if err != nil || !bytes.Equal(got, program) {
		t.Fatalf("the executor's copy is %d bytes (%v), want the program's %d", len(got), err, len(program))
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(h.executorBinary()); err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("the copy: %v %v", info, err)
		}
	}
	pin := h.readHome("executor/executor.pin")
	if want := sha256File(t, h.program()) + "  suiteward-executor\n"; pin != want {
		t.Errorf("the pin is %q, want %q", pin, want)
	}
	if !strings.Contains(h.out.String(), "executor") {
		t.Errorf("the output says nothing of the executor:\n%s", h.out.String())
	}
}

// unitPath is a path as a systemd unit spells it inside quotes: the test machine may be a Windows one, whose separators are escaped there.
func unitPath(p string) string { return strings.ReplaceAll(p, "\\", "\\\\") }

func TestInstallStartsTheExecutorAsASystemdUserUnitOnLinux(t *testing.T) {
	h := newHarness(t, "linux")
	h.f.env["PATH"] = "/opt/podman/bin:/usr/bin"
	if err := h.podman("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatal(err)
	}
	unit := h.readHome(".config/systemd/user/suiteward-executor.service")
	lines := configLines([]byte(unit))
	for _, want := range []string{
		`ExecStart="` + unitPath(h.executorBinary()) + `" execute`,
		`Environment="SUITEWARD_EXECUTOR_URL=http://127.0.0.1:18081"`,
		`Environment="SUITEWARD_EXECUTOR_TOKEN_FILE=` + unitPath(executorTokenPath(h)) + `"`,
		`Environment="SUITEWARD_EXECUTOR_RUNTIME=podman"`,
		`Environment="CONTAINER_CONNECTION=pm"`, // the executor must reach the engine the installer chose, and its CLI inherits this
		`Environment="PATH=/opt/podman/bin:/usr/bin"`,
		"TimeoutStopSec=45", "Restart=always", "WantedBy=default.target", // the executor gives a job 30 s after SIGTERM: the stop timeout exceeds it
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("the unit lacks %s:\n%s", want, unit)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "DATABASE_URL") || strings.Contains(line, "MCP_TOKEN") || strings.Contains(line, "GITHUB_APP") {
			t.Errorf("the unit gives the executor a credential of the service: %s", line)
		}
	}
	h.assertOrder("--user daemon-reload", "--user enable --now suiteward-executor.service")
}

func TestInstallStartsTheExecutorAsALaunchAgentOnMacOS(t *testing.T) {
	h := machineHarnessOn(t, "darwin")
	h.f.on("=-u", "501\n")
	if err := h.podman("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatal(err)
	}
	plist := h.readHome("Library/LaunchAgents/io.suiteward.executor.plist")
	for _, want := range []string{
		"<key>Label</key><string>io.suiteward.executor</string>",
		"<string>" + h.executorBinary() + "</string><string>execute</string>",
		"<key>SUITEWARD_EXECUTOR_URL</key><string>http://127.0.0.1:18081</string>",
		"<key>SUITEWARD_EXECUTOR_RUNTIME</key><string>podman</string>",
		"<key>CONTAINER_CONNECTION</key><string>pm</string>",
		"<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><true/>", "<key>ExitTimeOut</key><integer>45</integer>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("the plist lacks %s:\n%s", want, plist)
		}
	}
	h.assertOrder("bootstrap gui/501 "+filepath.Join(h.dir, "Library/LaunchAgents/io.suiteward.executor.plist"), "kickstart -k gui/501/io.suiteward.executor")
}

func utf16le(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE || len(raw)%2 != 0 {
		t.Fatalf("not UTF-16 little-endian with a byte order mark: % x", raw[:min(len(raw), 8)])
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 2; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

func TestInstallStartsTheExecutorAsAScheduledTaskAtLogonOnWindows(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	if err := h.podman("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatal(err)
	}
	wrapper := h.readHome("executor/run.cmd")
	for _, want := range []string{
		`set "SUITEWARD_EXECUTOR_URL=http://127.0.0.1:18081"`,
		`set "SUITEWARD_EXECUTOR_TOKEN_FILE=` + executorTokenPath(h) + `"`,
		`set "SUITEWARD_EXECUTOR_RUNTIME=podman"`,
		`set "CONTAINER_CONNECTION=pm"`,
		`"` + h.executorBinary() + `" execute`,
	} {
		if !strings.Contains(wrapper, want) {
			t.Errorf("the wrapper lacks %s:\n%s", want, wrapper)
		}
	}
	raw, err := os.ReadFile(filepath.Join(h.executorDir(), "task.xml"))
	if err != nil {
		t.Fatal(err)
	}
	task := utf16le(t, raw)
	for _, want := range []string{
		"<LogonTrigger>", "<UserId>" + testSID + "</UserId>", "<LogonType>InteractiveToken</LogonType>", "<RunLevel>LeastPrivilege</RunLevel>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>", "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<RestartOnFailure>", "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<Command>" + filepath.Join(h.executorDir(), "run.cmd") + "</Command>",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("the task lacks %s:\n%s", want, task)
		}
	}
	h.assertOrder("/Create /TN SuiteWard Executor /XML "+filepath.Join(h.executorDir(), "task.xml")+" /F", "/Run /TN SuiteWard Executor")
	if !strings.Contains(h.out.String(), "console window") {
		t.Errorf("the output must say that a console window shows (a task of the logged-on user cannot hide it):\n%s", h.out.String())
	}
}

func TestInstallLeavesARunningExecutorAloneWhenNothingChanged(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	again := newHarness(t, "linux")
	again.dir = h.dir // the stack of the first run: its token copies, and the secrets that belong to them
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	again.f.on("--user show -p LoadState -p ActiveState -p SubState suiteward-executor.service", "LoadState=loaded\nActiveState=active\nSubState=running\n")
	if err := again.podman(); err != nil {
		t.Fatal(err)
	}
	for _, line := range again.f.lines() {
		if strings.Contains(line, "suiteward-executor.service") && (strings.Contains(line, "stop") || strings.Contains(line, "restart") || strings.Contains(line, "enable")) {
			t.Errorf("a running executor with nothing new was touched: %s", line)
		}
	}
}

func TestInstallReplacesTheExecutorsProgramAndRestartsIt(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(t.TempDir(), "suiteward")
	if err := os.WriteFile(newer, []byte("the next release"), 0o755); err != nil {
		t.Fatal(err)
	}
	again := newHarness(t, "linux")
	again.dir = h.dir
	again.exe = newer
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	again.f.on("--user show -p LoadState -p ActiveState -p SubState suiteward-executor.service", "LoadState=loaded\nActiveState=active\nSubState=running\n")
	if err := again.podman(); err != nil {
		t.Fatal(err)
	}
	if got := h.readHome("executor/suiteward-executor"); got != "the next release" {
		t.Errorf("the executor still has %q", got)
	}
	again.assertOrder("--user stop suiteward-executor.service", "--user enable --now suiteward-executor.service")
}

func TestInstallStopsAScheduledTaskBeforeItReplacesTheProgramItHolds(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(t.TempDir(), "suiteward.exe")
	if err := os.WriteFile(newer, []byte("the next release"), 0o755); err != nil {
		t.Fatal(err)
	}
	again := machineHarnessOn(t, "windows")
	again.dir = h.dir
	again.exe = newer
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	again.f.on("IMAGENAME eq suiteward-executor.exe", `"suiteward-executor.exe","4242","Console","1","20,000 K"`)
	if err := again.podman(); err != nil {
		t.Fatal(err)
	}
	again.assertOrder("/End /TN SuiteWard Executor", "/Create /TN SuiteWard Executor")
	if got := h.readHome("executor/suiteward-executor.exe"); got != "the next release" {
		t.Errorf("the executor still has %q", got)
	}
}

func TestInstallOnlyWarnsWhenTheExecutorCannotBeStarted(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	h.f.fail("/Create /TN", errors.New("schtasks: Access is denied"))
	if err := h.podman(); err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, "executor could not be set up") || !strings.Contains(out, "Access is denied") {
		t.Errorf("the output must say that the executor could not be set up, and why:\n%s", out)
	}
	if _, err := os.Stat(h.settingsPath()); err != nil {
		t.Errorf("the settings are not saved: %v", err)
	}
}

func TestTheExecutorOfADockerStackUsesTheEngineTheInstallerChose(t *testing.T) {
	h := dockerHarness(t, "windows")
	h.f.env["ProgramFiles"] = `C:\Program Files`
	if err := h.docker("--connection", "desktop-linux"); err != nil {
		t.Fatal(err)
	}
	wrapper := h.readHome("executor/run.cmd")
	for _, want := range []string{`set "SUITEWARD_EXECUTOR_RUNTIME=docker"`, `set "DOCKER_CONTEXT=desktop-linux"`, `set "PATH=C:\Program Files\Docker\Docker\resources\bin;%PATH%"`} {
		if !strings.Contains(wrapper, want) {
			t.Errorf("the wrapper lacks %s:\n%s", want, wrapper)
		}
	}
	if !strings.Contains(h.out.String(), "Podman") {
		t.Errorf("the output must say that a docker command that reaches Podman's socket fails closed:\n%s", h.out.String())
	}
}

func TestInstallPullsTheGoImageOfTheSandboxesAsItPullsTheStacksImages(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if len(h.f.find("pull "+goprofile.Image)) != 1 {
		t.Errorf("the image of the Go profile must be pulled once, by its digest: %v", h.f.lines())
	}
	h.assertOrder("pull "+goprofile.Image, "run --detach --replace --name suiteward --network")
}

func TestTheExecutorTokenIsNeverInAnArgumentOrInTheWrapperOfTheUnit(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	token := h.secretWrites(SecretExecutorAuth)[0]
	h.f.assertNoSecret(t, token)
	if strings.Contains(h.readHome(".config/systemd/user/suiteward-executor.service"), token) {
		t.Error("the unit holds the token: it names the file")
	}
}

var secretsOfTheService = regexp.MustCompile(`(?i)(DATABASE_URL|MCP_TOKEN|GITHUB_APP)`)

func TestTheExecutorOfEveryOperatingSystemHoldsNoCredentialOfTheService(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			h := machineHarnessOn(t, goos)
			if goos == "darwin" {
				h.f.on("=-u", "501\n")
			}
			if err := h.podman(); err != nil {
				t.Fatal(err)
			}
			var definition string
			switch goos {
			case "linux":
				definition = h.readHome(".config/systemd/user/suiteward-executor.service")
			case "darwin":
				definition = h.readHome("Library/LaunchAgents/io.suiteward.executor.plist")
			default:
				definition = h.readHome("executor/run.cmd")
			}
			if secretsOfTheService.MatchString(definition) {
				t.Errorf("the definition of the executor names a credential of the service:\n%s", definition)
			}
		})
	}
}

func TestUpgradeSetsUpTheExecutorOfAStackThatPredatesIt(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(u.secretWrites(SecretExecutorAuth)) != 1 {
		t.Errorf("the executor token was not made: %v", u.f.lines())
	}
	if _, err := os.Stat(u.executorBinary()); err != nil {
		t.Errorf("the executor was not installed: %v", err)
	}
	u.assertOrder("secret create suiteward-executor-token -", "run --detach --replace --name suiteward --network", "--user enable --now suiteward-executor.service")
}

// writeExecutorFiles puts down what an install of the executor leaves on a Linux computer: its directory, its unit and the copy of its token.
func (h *harness) writeExecutorFiles() {
	h.t.Helper()
	for path, content := range map[string]string{
		filepath.Join(h.executorDir(), "suiteward-executor"):                    "a program",
		filepath.Join(h.executorDir(), "executor.pin"):                          strings.Repeat("a", 64) + "  suiteward-executor\n",
		filepath.Join(h.dir, ".config/systemd/user/suiteward-executor.service"): "[Service]\n",
		executorTokenPath(h): strings.Repeat("b", 64),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			h.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
}

func TestUninstallStopsAndRemovesTheExecutorAndKeepsItsTokenWithoutPurge(t *testing.T) {
	un := newHarness(t, "linux")
	h := un
	podmanStackExists(un)
	un.writeFiles()
	un.writeExecutorFiles()
	un.f.on("--user show -p LoadState -p ActiveState -p SubState suiteward-executor.service", "LoadState=loaded\nActiveState=active\nSubState=running\n")
	if err := un.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
		t.Fatal(err)
	}
	un.assertOrder("--user disable --now suiteward-executor.service", "rm --force --ignore --time 45 suiteward")
	if _, err := os.Stat(un.executorDir()); !os.IsNotExist(err) {
		t.Errorf("the executor's directory is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, ".config/systemd/user/suiteward-executor.service")); !os.IsNotExist(err) {
		t.Errorf("the unit is still there: %v", err)
	}
	if _, err := os.Stat(executorTokenPath(h)); err != nil {
		t.Errorf("an uninstall without --purge removed the executor's token copy: %v", err)
	}
}

func TestUninstallPurgeRemovesTheExecutorsTokenCopyToo(t *testing.T) {
	un := newHarness(t, "linux")
	h := un
	podmanStackExists(un)
	un.writeFiles()
	un.writeExecutorFiles()
	if err := un.uninstall("--runtime", "podman", "--connection", "pm", "--purge", "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(executorTokenPath(h)); !os.IsNotExist(err) {
		t.Errorf("the executor's token copy is still there: %v", err)
	}
	if !strings.Contains(un.out.String(), executorTokenPath(h)) {
		t.Errorf("a purge says what it deletes, and the executor's token copy is one of them:\n%s", un.out.String())
	}
}

// The executor removes every suiteward-sandbox container of its engine when it starts: two executors on one engine would remove each other's
// sandboxes, so the installer says so where it installs one, on both tiers.
func TestTheInstallsSayThatAnEngineHasOneExecutor(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if out := h.out.String(); !strings.Contains(out, "one executor") || !strings.Contains(out, "suiteward-sandbox-") {
		t.Errorf("the co-located install must say that an engine has one executor, and why:\n%s", out)
	}
	host := newHostFake()
	out, err := runHost(t, host, declineMetadataRule, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "one executor") || !strings.Contains(out, "suiteward-sandbox-") {
		t.Errorf("the host step must say that an engine has one executor, and why:\n%s", out)
	}
}
