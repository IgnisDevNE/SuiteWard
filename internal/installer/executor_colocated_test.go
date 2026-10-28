package installer

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
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
	// The Windows ones are named by their full path under the system directory.
	return slices.Contains([]string{"systemctl", "schtasks", "tasklist", "taskkill", "powershell", "launchctl", "id"}, baseName(c.name))
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
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(h.dir, rel)))
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
		if info, err := os.Stat(h.executorBinary()); err != nil || info.Mode().Perm() != 0o700 {
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
		"KillMode=mixed", "TimeoutStopSec=45", "Restart=always", "WantedBy=default.target", // the executor gives a job 30 s after SIGTERM: the stop timeout exceeds it
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
	h.assertOrder("bootstrap gui/501 "+filepath.Join(h.dir, "Library", "LaunchAgents", "io.suiteward.executor.plist"), "kickstart -k gui/501/io.suiteward.executor")
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
	task := utf16le(t, mustRead(t, filepath.Join(h.executorDir(), "task.xml")))
	for _, want := range []string{
		"<LogonTrigger>", "<UserId>" + testSID + "</UserId>", "<LogonType>InteractiveToken</LogonType>", "<RunLevel>LeastPrivilege</RunLevel>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>", "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("the task lacks %s:\n%s", want, task)
		}
	}
	h.assertOrder("/Create /TN SuiteWard Executor /XML "+filepath.Join(h.executorDir(), "task.xml")+" /F", "/Run /TN SuiteWard Executor")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// execAction is the action of the task as Task Scheduler reads it: the program, its arguments and the folder it starts in.
type execAction struct{ Command, Arguments, WorkingDirectory string }

func taskAction(t *testing.T, task string) (action execAction, hasRestart bool) {
	t.Helper()
	var doc struct {
		Actions struct{ Exec execAction }
	}
	dec := xml.NewDecoder(strings.NewReader(task))
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil } // the file declares UTF-16: utf16le has decoded it
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("the task is not XML: %v\n%s", err, task)
	}
	return doc.Actions.Exec, strings.Contains(task, "RestartOnFailure")
}

// The executor is never a window on the user's computer (owner decision 18). A scheduled task of the logged-on user opens a console for a console
// program, so the script runs through conhost.exe --headless, which has no window. Every program is named by its full path under the system
// directory, and the script by its name in the folder the task starts in, with its own path as an argument that cmd ignores: no character of a
// path goes through the quoting rules of cmd /c, and the process list names the instance by that path.
func TestTheScheduledTaskOfTheExecutorRunsItsScriptThroughAHeadlessConsole(t *testing.T) {
	for name, c := range map[string]struct{ systemRoot, system string }{
		"the system directory of the computer": {`D:\Windows`, `D:\Windows\System32`},
		"the default system directory":         {"", `C:\Windows\System32`},
	} {
		t.Run(name, func(t *testing.T) {
			h := machineHarnessOn(t, "windows")
			h.dir = filepath.Join(h.dir, "Ana & Pedro (work)") // a folder name that cmd reads as an operator and a group
			if err := os.MkdirAll(h.dir, 0o700); err != nil {
				t.Fatal(err)
			}
			h.f.env["SystemRoot"] = c.systemRoot
			if err := h.podman(); err != nil {
				t.Fatal(err)
			}
			action, hasRestart := taskAction(t, utf16le(t, mustRead(t, filepath.Join(h.executorDir(), "task.xml"))))
			if want := c.system + `\conhost.exe`; action.Command != want {
				t.Errorf("the task runs %q, want %q: no console of Windows Terminal may open", action.Command, want)
			}
			wrapper := filepath.Join(h.executorDir(), "run.cmd")
			if want := `--headless "` + c.system + `\cmd.exe" /d /c .\run.cmd "` + wrapper + `"`; action.Arguments != want {
				t.Errorf("the task passes %q, want %q", action.Arguments, want)
			}
			if action.WorkingDirectory != h.executorDir() {
				t.Errorf("the task starts in %q, want %q", action.WorkingDirectory, h.executorDir())
			}
			if hasRestart {
				t.Error("the task keeps RestartOnFailure: Task Scheduler does not restart an action that exited, so the wrapper is what brings the executor back")
			}
		})
	}
}

// The wrapper is what brings the executor back (owner decision 18): Task Scheduler's RestartOnFailure does not restart an action that exited, so a
// closed console, a crash or an exit starts the program again after about a minute. The pause is ping, which reads no console (timeout does).
func TestTheWrapperOfTheWindowsTaskStartsTheExecutorAgainAfterAPause(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(h.readHome("executor/run.cmd")), "\r\n")
	start := `"` + h.executorBinary() + `" execute >> "` + filepath.Join(h.executorDir(), "executor.log") + `" 2>&1`
	at := func(from int, want string) int {
		t.Helper()
		for i := from; i < len(lines); i++ {
			if strings.Contains(lines[i], want) {
				return i
			}
		}
		t.Fatalf("the wrapper has no line with %q after line %d:\n%s", want, from, strings.Join(lines, "\n"))
		return -1
	}
	label := at(0, ":again")
	run := at(label, start)
	pause := at(run, `"%SystemRoot%\System32\ping.exe" -n 61 127.0.0.1 >nul`)
	at(pause, "goto again")
	if strings.Contains(strings.Join(lines, "\n"), "timeout") {
		t.Error("the wrapper pauses with timeout, which reads the console")
	}
}

func TestTheInstallSaysThatTheExecutorOfWindowsHasNoWindow(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	if !strings.Contains(out, "no window") {
		t.Errorf("the output must say that the executor has no window:\n%s", out)
	}
	if strings.Contains(out, "console window") || strings.Contains(out, "Closing the window") {
		t.Errorf("the output still says that a window shows and that closing it stops the executor:\n%s", out)
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
	if err := os.WriteFile(newer, []byte("the next release"), 0o600); err != nil {
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
	if err := os.WriteFile(newer, []byte("the next release"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := machineHarnessOn(t, "windows")
	again.dir = h.dir
	again.exe = newer
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	again.first("/Query /TN SuiteWard Executor", "") // the task of the first install is registered
	again.f.on("IMAGENAME eq suiteward-executor.exe", `"suiteward-executor.exe","4242","Console","1","20,000 K"`)
	again.f.on("Win32_Process", listingOf(nil)) // no process listed: the task's tree is already gone
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
	if !strings.Contains(out, "could not be set up") || !strings.Contains(out, "Access is denied") {
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
	if !strings.Contains(h.out.String(), "podman-docker") {
		t.Errorf("the output must say that Docker's CLI on Podman's socket fails closed and that the podman-docker shim works:\n%s", h.out.String())
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
		filepath.Join(h.executorDir(), "suiteward-executor"):                             "a program",
		filepath.Join(h.executorDir(), "executor.pin"):                                   strings.Repeat("a", 64) + "  suiteward-executor\n",
		filepath.Join(h.dir, ".config", "systemd", "user", "suiteward-executor.service"): "[Service]\nExecStart=\"" + unitValue(filepath.Join(h.executorDir(), "suiteward-executor")) + "\" execute\n", // this instance's
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
	if _, err := os.Stat(filepath.Join(h.dir, ".config", "systemd", "user", "suiteward-executor.service")); !os.IsNotExist(err) {
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

// A launch agent that is loaded but not running (waiting, throttled) cannot be bootstrapped again: launchctl refuses a label that is loaded.
func TestInstallBootsOutALoadedLaunchAgentBeforeItLoadsTheNewDefinition(t *testing.T) {
	h := machineHarnessOn(t, "darwin")
	h.f.on("=-u", "501\n")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(t.TempDir(), "suiteward")
	if err := os.WriteFile(newer, []byte("the next release"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := machineHarnessOn(t, "darwin")
	again.dir = h.dir
	again.exe = newer
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	again.f.on("=-u", "501\n").on("=print gui/501/"+executorLabel, "state = waiting\n")
	if err := again.podman(); err != nil {
		t.Fatal(err)
	}
	again.assertOrder("bootout gui/501/"+executorLabel, "bootstrap gui/501 ", "kickstart -k gui/501/"+executorLabel)
}

// A command that succeeds and prints nothing is not an error to format: the message names what is missing and never prints a nil.
func TestInstallSaysThatTheUserIDIsMissingWhenIdPrintsNothing(t *testing.T) {
	h := machineHarnessOn(t, "darwin")
	h.f.on("=-u", "")
	if err := h.podman(); err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, "could not be set up") || !strings.Contains(out, "id of this user") {
		t.Errorf("the output must say that the executor could not be set up because the id of the user is not known:\n%s", out)
	}
	if strings.Contains(out, "<nil>") || strings.Contains(out, "%!") {
		t.Errorf("the output prints a nil error:\n%s", out)
	}
}

// A shell with no systemd user manager (WSL without systemd, a sudo or an ssh session) cannot answer for the unit. With no executor made here there
// is nothing to ask it; with one, the uninstall goes on with the stack and says what is left to do.
func TestUninstallWithNoUserManagerAndNoExecutorRemovesTheStack(t *testing.T) {
	un := newHarness(t, "linux")
	podmanStackExists(un)
	un.writeFiles()
	un.f.fail("--user show -p LoadState", errors.New("Failed to connect to bus: No medium found"))
	if err := un.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
		t.Fatalf("an uninstall must not fail for a user manager that is not there: %v", err)
	}
	if len(un.f.find("rm --force --ignore --time 45 suiteward")) == 0 {
		t.Errorf("the stack was not removed: %v", un.f.calls)
	}
}

func TestUninstallWarnsAndGoesOnWhenTheUserManagerCannotBeAskedAboutAnExecutorThatIsThere(t *testing.T) {
	un := newHarness(t, "linux")
	podmanStackExists(un)
	un.writeFiles()
	un.writeExecutorFiles()
	un.f.fail("--user show -p LoadState", errors.New("Failed to connect to bus: No medium found"))
	if err := un.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
		t.Fatalf("an uninstall must go on with the stack: %v", err)
	}
	if len(un.f.find("rm --force --ignore --time 45 suiteward")) == 0 {
		t.Errorf("the stack was not removed: %v", un.f.calls)
	}
	out := un.out.String()
	if !strings.Contains(out, "was not removed") || !strings.Contains(out, un.executorDir()) || !strings.Contains(out, "disable --now suiteward-executor.service") {
		t.Errorf("the output must say that the executor is still there, where, and how to remove it:\n%s", out)
	}
}

// A computer has one executor: the unit, the launch agent and the task have one name for each user. An executor that is registered for another
// instance's token file (its program is in another directory) is not repointed by an install, and an uninstall does not remove it.
const anotherInstancesUnit = "[Service]\nExecStart=\"/elsewhere/executor/suiteward-executor\" execute\n"

func writeAnotherInstancesUnit(t *testing.T, h *harness) string {
	t.Helper()
	path := filepath.Join(h.dir, ".config", "systemd", "user", "suiteward-executor.service")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(anotherInstancesUnit), 0o600); err != nil {
		t.Fatal(err)
	}
	h.f.on("--user show -p LoadState -p ActiveState -p SubState suiteward-executor.service", "LoadState=loaded\nActiveState=active\nSubState=running\n")
	return path
}

func TestInstallDoesNotRepointTheExecutorOfAnotherInstance(t *testing.T) {
	h := newHarness(t, "linux")
	path := writeAnotherInstancesUnit(t, h)
	if err := h.podman(); err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Clean(path)); err != nil || string(raw) != anotherInstancesUnit {
		t.Errorf("the unit of the other instance was rewritten: %q (%v)", raw, err)
	}
	if len(h.f.find("enable --now suiteward-executor.service")) != 0 || len(h.f.find("--user stop")) != 0 {
		t.Errorf("the executor of the other instance was stopped or started: %v", h.f.calls)
	}
	if out := h.out.String(); !strings.Contains(out, "another instance") || !strings.Contains(out, "one test runner") {
		t.Errorf("the output must say that the computer has one executor and that it is another instance's:\n%s", out)
	}
	if _, err := os.Stat(h.executorDir()); !os.IsNotExist(err) {
		t.Errorf("the install made a directory for an executor that it did not set up: %v", err)
	}
}

func TestUninstallKeepsTheExecutorThatIsRegisteredForAnotherInstance(t *testing.T) {
	un := newHarness(t, "linux")
	podmanStackExists(un)
	un.writeFiles()
	un.writeExecutorFiles()
	path := writeAnotherInstancesUnit(t, un)
	if err := un.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Clean(path)); err != nil || string(raw) != anotherInstancesUnit {
		t.Errorf("the unit of the other instance was removed or rewritten: %q (%v)", raw, err)
	}
	if len(un.f.find("disable --now suiteward-executor.service")) != 0 {
		t.Errorf("the executor of the other instance was stopped: %v", un.f.calls)
	}
	if out := un.out.String(); !strings.Contains(out, "another instance") {
		t.Errorf("the output must say that the executor is another instance's:\n%s", out)
	}
}

func TestInstallDoesNotRepointAScheduledTaskOfAnotherInstance(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	h.first("/Query /TN SuiteWard Executor", "") // a task is registered, and this instance has no executor files: it is another instance's
	if err := h.podman(); err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", err)
	}
	if len(h.f.find("/Create /TN")) != 0 || len(h.f.find("/End /TN")) != 0 {
		t.Errorf("the task of the other instance was recreated or ended: %v", h.f.calls)
	}
	if out := h.out.String(); !strings.Contains(out, "another instance") {
		t.Errorf("the output must say that the task is another instance's:\n%s", out)
	}
}

// An upgrade at the installed release has no image to replace, but the program that runs it may be a newer one than the executor's copy: the
// executor is set up as an install does, so that the copy is replaced and the executor restarted.
func TestUpgradeAtTheInstalledReleaseRefreshesTheExecutor(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.have(kindSecret, SecretExecutorAuth)
	u.writeExecutorFiles() // an executor of an earlier program: "a program"
	u.f.on("--user show -p LoadState -p ActiveState -p SubState suiteward-executor.service", "LoadState=loaded\nActiveState=active\nSubState=running\n")
	if err := u.upgradeAt(false, "", "--yes", "--image", oldImage); err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(u.program())
	if err != nil {
		t.Fatal(err)
	}
	if got := u.readHome("executor/suiteward-executor"); got != string(program) {
		t.Errorf("the executor still runs %q, not this program", got)
	}
	if len(u.f.find("--user enable --now suiteward-executor.service")) == 0 {
		t.Errorf("the executor was not started again: %v", u.f.calls)
	}
	if out := u.out.String(); !strings.Contains(out, "Nothing to upgrade") || !strings.Contains(out, "executor") {
		t.Errorf("the output must say that there is nothing to upgrade and what became of the executor:\n%s", out)
	}
}

// The executor reads its token from the copy that the install made: without it the executor is not started, and the output says what to run.
func TestUpgradeAtTheInstalledReleaseDoesNotStartAnExecutorWithoutItsTokenCopy(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.have(kindSecret, SecretExecutorAuth)
	if err := u.upgradeAt(false, "", "--yes", "--image", oldImage); err != nil {
		t.Fatalf("the executor is not the stack: %v", err)
	}
	if len(u.f.find("--user enable --now suiteward-executor.service")) != 0 {
		t.Errorf("an executor with no token was started: %v", u.f.calls)
	}
	if out := u.out.String(); !strings.Contains(out, "token file") || !strings.Contains(out, "install") {
		t.Errorf("the output must say that the token copy is missing and what to run:\n%s", out)
	}
}

// A batch file reads a percent sign: `%` in the folder of the executor (a profile or a token file under a name with one) has to be doubled in the
// line that starts the program and in the one that names its log, as it is in the `set` lines.
func TestTheWrapperOfTheWindowsTaskDoublesAPercentSignInItsProgramAndLogPaths(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	h.dir = filepath.Join(h.dir, "50%off")
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	doubled := func(path string) string { return strings.ReplaceAll(path, "%", "%%") }
	want := `"` + doubled(h.executorBinary()) + `" execute >> "` + doubled(filepath.Join(h.executorDir(), "executor.log")) + `" 2>&1`
	if wrapper := h.readHome("executor/run.cmd"); !strings.Contains(wrapper, want) {
		t.Errorf("the wrapper lacks the line that starts the program with its percent signs doubled, %s:\n%s", want, wrapper)
	}
}

// An uninstall that could not remove the executor says how to, with the commands of the operating system that keeps it running.
func TestTheUninstallWarningNamesTheCommandsOfTheOperatingSystemThatKeepsTheExecutor(t *testing.T) {
	cases := []struct {
		goos string
		set  func(un *harness)
		want []string
	}{
		{"windows", func(un *harness) {
			un.first("/Query /TN SuiteWard Executor", "") // the task is registered, and this instance made its two files
			writeFile(un.t, filepath.Join(un.executorDir(), executorWrapperName), "the wrapper")
			writeFile(un.t, filepath.Join(un.executorDir(), "task.xml"), "the task")
			un.f.fail("IMAGENAME eq suiteward-executor.exe", errors.New("tasklist: access denied"))
		}, []string{`schtasks /End /TN "SuiteWard Executor"`, `schtasks /Delete /TN "SuiteWard Executor" /F`, "sign out of Windows"}},
		{"darwin", func(un *harness) { un.f.fail("=-u", errors.New("id: not found")) }, []string{"launchctl bootout gui/$(id -u)/" + executorLabel}},
	}
	for _, c := range cases {
		t.Run(c.goos, func(t *testing.T) {
			un := machineHarnessOn(t, c.goos)
			podmanStackExists(un)
			un.writeFiles()
			un.writeExecutorFiles()
			c.set(un)
			if err := un.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
				t.Fatalf("an uninstall must go on with the stack: %v", err)
			}
			out := un.out.String()
			if !strings.Contains(out, "was not removed") || strings.Contains(out, "systemctl") {
				t.Errorf("the output must say that the executor is still there, and not give the command of Linux:\n%s", out)
			}
			// The wrapper starts the program again after its pause, so ending the program by its name alone would not stop the executor, and it would
			// end another instance's too: a person signs out, which ends everything of the user, after the task is deleted.
			if strings.Contains(out, "taskkill") {
				t.Errorf("the output teaches a kill by name:\n%s", out)
			}
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("the output lacks %s:\n%s", want, out)
				}
			}
		})
	}
}
