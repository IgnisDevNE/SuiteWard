package installer

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// The executor on the co-located tier (ADR 0028, section 10; spike S4, section 5): a host process of this user that runs `suiteward execute`.
// Its lifecycle is the mechanism each operating system has for a program that starts when the user logs on and is kept running, and that needs
// neither administrator rights nor a stored password: a scheduled task of the logged-on user on Windows, a launch agent on macOS and a systemd
// user unit on Linux. The spike names this family (a logon task, a launch agent, a user unit) and ADR 0028 leaves the choice to this task.
// It runs a copy of this program that lives beside the token files, so that a program that the user moves or deletes does not take it along.

const (
	executorTaskName = "SuiteWard Executor"    // the scheduled task of Windows
	executorLabel    = "io.suiteward.executor" // the launch agent of macOS
)

// plainValue is a value that can go between double quotes in a unit, a plist or a wrapper without a quote or a line break to end it early.
var plainValue = regexp.MustCompile(`^[^"\x00-\x1f]+$`)

// envEntry is one variable of the executor's environment.
type envEntry struct{ key, value string }

// executorPlan is where the executor is on this computer and what it runs with.
type executorPlan struct {
	goos       string
	dir        string // beside the token files: the copy of the program, its pin, its log and the files of the lifecycle that are not the system's
	binary     string
	pin        string
	log        string
	tokenFile  string // the executor's copy of its token
	env        []envEntry
	wrapper    string // Windows: the script that sets the environment and starts the program
	taskXML    string // Windows: the definition of the task
	system     string // Windows: the system directory, where conhost.exe, cmd.exe and ping.exe are
	definition string // macOS: the launch agent; Linux: the user unit
}

// executorBinaryName is the file name of the copy: not "suiteward", so that its process is told apart from every run of the installer.
func executorBinaryName(goos string) string {
	if goos == "windows" {
		return "suiteward-executor.exe"
	}
	return "suiteward-executor"
}

// engineBinDir is the directory of the container CLI that the installer drives, when it drives one by its path (Docker Desktop's, which the
// docker of the PATH may not be: Podman's shim is another program). The executor's own CLI is the one it finds on its PATH.
func engineBinDir(rt Runtime) string {
	var bin string
	switch r := rt.(type) {
	case *Docker:
		bin = r.bin
	case *Podman:
		bin = r.bin
	}
	i := strings.LastIndexAny(bin, `/\`) // not filepath.Dir: the path of a Windows machine is read on any system
	if i < 0 {
		return ""
	}
	return bin[:i]
}

// planExecutor decides where the executor lives and what it is given. tokenFile is where this instance keeps the MCP token: the executor's
// files go beside it. engineDir is engineBinDir. The environment carries the three settings of the executor, the engine the installer chose
// (the executor's CLI inherits it: the executor itself reads only its three settings) and the PATH its CLI is found by.
func planExecutor(d Deps, tokenFile string, s Settings, engineDir string) (executorPlan, error) {
	p := executorPlan{goos: d.GOOS, tokenFile: executorTokenFile(tokenFile)}
	p.dir = executorDirOf(tokenFile)
	p.binary = filepath.Join(p.dir, executorBinaryName(d.GOOS))
	p.pin = filepath.Join(p.dir, "executor.pin")
	p.log = filepath.Join(p.dir, "executor.log")
	p.env = []envEntry{
		{executorURLEnv, "http://127.0.0.1:" + fmt.Sprint(s.HTTPPort)},
		{executorAuthFileEnv, p.tokenFile},
		{executorRuntimeEnv, s.Runtime},
	}
	if s.Connection != "" {
		switch s.Runtime {
		case RuntimePodman:
			p.env = append(p.env, envEntry{"CONTAINER_CONNECTION", s.Connection})
		case RuntimeDocker:
			p.env = append(p.env, envEntry{"DOCKER_CONTEXT", s.Connection})
		}
	}
	switch d.GOOS {
	case "windows":
		p.wrapper, p.taskXML = filepath.Join(p.dir, executorWrapperName), filepath.Join(p.dir, "task.xml")
		// Built with a backslash, not filepath.Join: the path of a Windows computer is read on any system. An empty SystemRoot reads C:\Windows, as
		// an empty ProgramFiles reads C:\Program Files.
		p.system = strings.TrimRight(cmp.Or(d.Getenv("SystemRoot"), `C:\Windows`), `\`) + `\System32`
		if engineDir != "" {
			p.env = append(p.env, envEntry{"PATH", engineDir + ";%PATH%"}) // the one value that is meant to be expanded
		}
	case "darwin", "linux":
		path := d.Getenv("PATH")
		if engineDir != "" {
			path = engineDir + ":" + path
		}
		if path != "" {
			p.env = append(p.env, envEntry{"PATH", strings.TrimSuffix(path, ":")}) // launchd and a user manager start with a short PATH of their own
		}
		home := d.Getenv("HOME")
		if home == "" {
			return executorPlan{}, errors.New("HOME is not set, so there is no place for the launch agent or the user unit of the executor")
		}
		if d.GOOS == "darwin" {
			p.definition = filepath.Join(home, "Library", "LaunchAgents", executorLabel+".plist")
		} else {
			config := d.Getenv("XDG_CONFIG_HOME")
			if config == "" {
				config = filepath.Join(home, ".config")
			}
			p.definition = filepath.Join(config, "systemd", "user", executorUnitName)
		}
	default:
		return executorPlan{}, fmt.Errorf("there is no way to keep the executor running on %s", d.GOOS)
	}
	for _, v := range append([]string{p.dir, p.binary, p.tokenFile, p.definition, p.system}, envValues(p.env)...) {
		if v != "" && !plainValue.MatchString(v) {
			return executorPlan{}, fmt.Errorf("%q cannot be written into the definition of the executor: it holds a quote or a control character", v)
		}
	}
	return p, nil
}

func envValues(env []envEntry) []string {
	out := make([]string, len(env))
	for i, e := range env {
		out[i] = e.value
	}
	return out
}

// cmdValue is a value inside `set "K=V"`, or a path inside the quotes of a command, of a batch file: a percent sign is doubled so that cmd does not read a variable.
func cmdValue(v string) string { return strings.ReplaceAll(v, "%", "%%") }

// windowsWrapper is the script that the task runs: the environment of the executor, then the program, started again after a pause whenever it ends
// (Task Scheduler does not restart an action that exited: RestartOnFailure covers a start that failed). Its output goes to the log. The pause is
// ping: timeout reads the console, which the task does not have.
func (p executorPlan) windowsWrapper() string {
	var b strings.Builder
	b.WriteString("@echo off\r\nrem Written by `suiteward install`: the environment of the SuiteWard executor, then the executor, again after a pause when it ends. Do not put a credential here.\r\n")
	for _, e := range p.env {
		value := cmdValue(e.value)
		if e.key == "PATH" {
			value = cmdValue(strings.TrimSuffix(e.value, ";%PATH%")) + ";%PATH%"
		}
		fmt.Fprintf(&b, "set \"%s=%s\"\r\n", e.key, value)
	}
	log := cmdValue(p.log)
	fmt.Fprintf(&b, ":again\r\n\"%s\" execute >> \"%s\" 2>&1\r\n", cmdValue(p.binary), log)
	fmt.Fprintf(&b, ">> \"%s\" echo %%date%% %%time%% The executor stopped. It starts again in a minute.\r\n", log)
	b.WriteString("\"%SystemRoot%\\System32\\ping.exe\" -n 61 127.0.0.1 >nul\r\ngoto again\r\n")
	return b.String()
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// windowsTask is the definition of the scheduled task, as Task Scheduler reads it: UTF-16 little-endian with a byte order mark. It starts at the
// logon of the user, as the user, with no password and no elevation; and it does not stop for the battery or after a time. It has no window: the
// script runs through conhost.exe --headless, because a console program started by a task of the logged-on user gets a window (in Windows Terminal,
// where it is the first thing the user sees). Each program is named by its full path under the system directory. The script is named by its name in
// the folder the task starts in, and its own path follows as an argument that it ignores: no character of a path goes through the quoting rules of
// cmd /c (a folder with an ampersand breaks them), and the process list tells this instance's tree from another's by that path.
func (p executorPlan) windowsTask(sid string) []byte {
	user := xmlEscaper.Replace(sid)
	xml := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>The SuiteWard executor: runs the tests of canonical execution in disposable sandboxes.</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + user + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + user + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscaper.Replace(p.system+`\conhost.exe`) + `</Command><Arguments>` + xmlEscaper.Replace(`--headless "`+p.system+`\cmd.exe" /d /c .\`+executorWrapperName+` "`+p.wrapper+`"`) + `</Arguments><WorkingDirectory>` + xmlEscaper.Replace(p.dir) + `</WorkingDirectory></Exec></Actions>
</Task>
`
	units := utf16.Encode([]rune(strings.ReplaceAll(xml, "\n", "\r\n")))
	out := []byte{0xFF, 0xFE}
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

// launchAgent is the launch agent of macOS: started at load, kept alive, with its own environment.
func (p executorPlan) launchAgent() string {
	var env strings.Builder
	for _, e := range p.env {
		fmt.Fprintf(&env, "    <key>%s</key><string>%s</string>\n", xmlEscaper.Replace(e.key), xmlEscaper.Replace(e.value))
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + executorLabel + `</string>
  <key>ProgramArguments</key>
  <array><string>` + xmlEscaper.Replace(p.binary) + `</string><string>execute</string></array>
  <key>EnvironmentVariables</key>
  <dict>
` + env.String() + `  </dict>
  <key>RunAtLoad</key><true/>
  <key>ExitTimeOut</key><integer>45</integer>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>` + xmlEscaper.Replace(p.log) + `</string>
  <key>StandardErrorPath</key><string>` + xmlEscaper.Replace(p.log) + `</string>
</dict>
</plist>
`
}

// unitValue is a value inside the quotes of an Environment= or ExecStart= line: systemd reads a percent sign as a specifier and a backslash
// as an escape.
func unitValue(v string) string {
	return strings.NewReplacer(`\`, `\\`, "%", "%%").Replace(v)
}

// userUnit is the systemd user unit of the executor on a Linux computer that also runs the co-located stack: started with the user's manager,
// which lingers when the stack's containers restart at boot (doctor checks that).
func (p executorPlan) userUnit() string {
	var env strings.Builder
	for _, e := range p.env {
		fmt.Fprintf(&env, "Environment=\"%s=%s\"\n", e.key, unitValue(e.value))
	}
	return `# The SuiteWard executor of this computer: ` + "`suiteward execute`" + `, a program of this user that runs the sandboxes of canonical execution through the container runtime.
[Unit]
Description=SuiteWard executor
StartLimitIntervalSec=0

[Service]
ExecStart="` + unitValue(p.binary) + `" execute
` + env.String() + `# On stop systemd signals the main process only: the default would also reach the container CLI that runs a sandbox, which forwards the signal
# to the tests and makes a job fail. The executor stops its sandbox itself; what is left after the timeout is killed.
KillMode=mixed
TimeoutStopSec=45
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`
}

// definitionText is what the operating system reads to start the executor, apart from the program: the files the plan writes.
func (p executorPlan) definitionFiles(sid string) map[string][]byte {
	switch p.goos {
	case "windows":
		return map[string][]byte{p.wrapper: []byte(p.windowsWrapper()), p.taskXML: p.windowsTask(sid)}
	case "darwin":
		return map[string][]byte{p.definition: []byte(p.launchAgent())}
	default:
		return map[string][]byte{p.definition: []byte(p.userUnit())}
	}
}

// userID is the numeric id of this user, which launchctl names the user's domain by.
func userID(ctx context.Context, d Deps) (string, error) {
	out, err := d.Exec(ctx, "id", []string{"-u"}, nil)
	uid := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("find the id of this user: %w", err)
	}
	if uid == "" {
		return "", errors.New("find the id of this user: `id -u` printed nothing")
	}
	return uid, nil
}

// state says whether the operating system knows the executor and whether it runs. A command that fails is the executor not being there, except
// when the command itself cannot be asked.
func (p executorPlan) state(ctx context.Context, d Deps) (registered, running bool, err error) {
	switch p.goos {
	case "windows":
		_, queryErr := d.Exec(ctx, "schtasks", []string{"/Query", "/TN", executorTaskName}, nil)
		if taskThere := queryErr == nil; !taskThere { // a task that is not there is how the query fails
			return false, false, nil
		}
		out, err := d.Exec(ctx, "tasklist", []string{"/FI", "IMAGENAME eq " + executorBinaryName("windows"), "/FO", "CSV", "/NH"}, nil)
		if err != nil {
			return true, false, fmt.Errorf("list the processes: %w", err)
		}
		return true, strings.Contains(strings.ToLower(string(out)), strings.ToLower(executorBinaryName("windows"))), nil
	case "darwin":
		uid, err := userID(ctx, d)
		if err != nil {
			return false, false, err
		}
		out, printErr := d.Exec(ctx, "launchctl", []string{"print", "gui/" + uid + "/" + executorLabel}, nil)
		if agentLoaded := printErr == nil; !agentLoaded { // an agent that is not loaded is how the query fails
			return false, false, nil
		}
		return true, strings.Contains(string(out), "state = running"), nil
	default:
		out, err := d.Exec(ctx, "systemctl", []string{"--user", "show", "-p", "LoadState", "-p", "ActiveState", "-p", "SubState", executorUnitName}, nil)
		if err != nil {
			return false, false, fmt.Errorf("ask the user manager about %s: %w", executorUnitName, err)
		}
		props := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[k] = v
			}
		}
		return props["LoadState"] == "loaded", props["ActiveState"] == "active" && props["SubState"] == "running", nil
	}
}

// stop stops the executor if it runs. On macOS and Linux a failure is nothing to act on, because the next step says what is wrong. On Windows it is:
// the program that is replaced or removed must not run, so the whole tree is ended, and a process that is left is an error.
func (p executorPlan) stop(ctx context.Context, d Deps) error {
	switch p.goos {
	case "windows":
		return p.stopTree(ctx, d)
	case "darwin":
		if uid, err := userID(ctx, d); err == nil {
			_, _ = d.Exec(ctx, "launchctl", []string{"bootout", "gui/" + uid + "/" + executorLabel}, nil)
		}
	default:
		_, _ = d.Exec(ctx, "systemctl", []string{"--user", "stop", executorUnitName}, nil)
	}
	return nil
}

// osProcess is a process of the Windows computer, as listProcesses reads it.
type osProcess struct {
	pid           int
	name, exe, at string // the image name, the path of the image, and the command line
}

// listProcessesScript prints one line for each console host, command interpreter or executor on the computer, `pid name base64(path) base64(command
// line)`: the paths and command lines go out as base64 of UTF-8, so that no code page changes a letter of them, and the matching is done here, on
// the paths that this instance owns, and not in the script, which then has no path to quote.
var listProcessesScript = `Get-CimInstance Win32_Process | Where-Object { 'conhost.exe','cmd.exe','` + executorBinaryName("windows") + `' -contains $_.Name } | ForEach-Object { '{0} {1} {2} {3}' -f $_.ProcessId, $_.Name, [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]$_.ExecutablePath)), [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]$_.CommandLine)) }`

func (p executorPlan) listProcesses(ctx context.Context, d Deps) ([]osProcess, error) {
	out, err := d.Exec(ctx, p.system+`\WindowsPowerShell\v1.0\powershell.exe`, []string{"-NoProfile", "-NonInteractive", "-Command", listProcessesScript}, nil)
	if err != nil {
		return nil, fmt.Errorf("list the processes of this computer: %w", err)
	}
	var procs []osProcess
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimRight(line, "\r"); line == "" {
			continue
		}
		fields := strings.Split(line, " ") // an empty path or command line leaves an empty field
		pid, pidErr := strconv.Atoi(fields[0])
		if len(fields) != 4 || pidErr != nil {
			return nil, fmt.Errorf("list the processes of this computer: unexpected line %q", line)
		}
		proc := osProcess{pid: pid, name: fields[1]}
		for i, text := range []*string{&proc.exe, &proc.at} {
			raw, err := base64.StdEncoding.DecodeString(fields[2+i])
			if err != nil {
				return nil, fmt.Errorf("list the processes of this computer: unexpected line %q: %w", line, err)
			}
			*text = string(raw)
		}
		procs = append(procs, proc)
	}
	return procs, nil
}

// ownProcesses is what runs of this instance's executor: the console hosts and command interpreters whose command line names this instance's
// wrapper (the argument of the task, and what the task of an earlier build ran), then the programs that run from this instance's binary. Never a
// process by its name alone: another instance's executor, and a command prompt of the user, are not in it. The wrapper's processes come first:
// ended after the program, its pause would start the program again.
func (p executorPlan) ownProcesses(procs []osProcess) []osProcess {
	var wrappers, programs []osProcess
	for _, proc := range procs {
		switch {
		case (strings.EqualFold(proc.name, "conhost.exe") || strings.EqualFold(proc.name, "cmd.exe")) && strings.Contains(strings.ToLower(proc.at), strings.ToLower(p.wrapper)):
			wrappers = append(wrappers, proc)
		case strings.EqualFold(proc.name, executorBinaryName("windows")) && strings.EqualFold(proc.exe, p.binary):
			programs = append(programs, proc)
		}
	}
	return append(wrappers, programs...)
}

// stopAttempts is how many times stopTree ends what is left before it says that something still runs.
const stopAttempts = 2

// stopTree ends the task, and then every process of this instance's tree with its children. `schtasks /End` ends only the console host of the task:
// the wrapper and the program live on, and the wrapper starts the program again after its pause. What taskkill does not end is found by listing again.
func (p executorPlan) stopTree(ctx context.Context, d Deps) error {
	_, _ = d.Exec(ctx, "schtasks", []string{"/End", "/TN", executorTaskName}, nil) // it fails when there is no task, which is nothing to act on
	var killErr error
	for attempt := 0; ; attempt++ {
		procs, err := p.listProcesses(ctx, d)
		if err != nil {
			return err
		}
		own := p.ownProcesses(procs)
		if len(own) == 0 {
			return nil
		}
		if attempt == stopAttempts {
			left := make([]string, len(own))
			for i, proc := range own {
				left[i] = fmt.Sprintf("process %d, %s", proc.pid, proc.name)
			}
			err := fmt.Errorf("the executor is still running (%s) after it was told to stop", strings.Join(left, "; "))
			if killErr != nil {
				err = fmt.Errorf("%w: %w", err, killErr)
			}
			return err
		}
		args := []string{"/T", "/F"}
		for _, proc := range own {
			args = append(args, "/PID", strconv.Itoa(proc.pid))
		}
		_, killErr = d.Exec(ctx, p.system+`\taskkill.exe`, args, nil) // a process that ended on its own, or with its parent, is an error here and no failure: the next listing says what is left
	}
}

// start registers the executor with the operating system and starts it. The definition files are written.
func (p executorPlan) start(ctx context.Context, d Deps) error {
	switch p.goos {
	case "windows":
		if _, err := d.Exec(ctx, "schtasks", []string{"/Create", "/TN", executorTaskName, "/XML", p.taskXML, "/F"}, nil); err != nil {
			return fmt.Errorf("create the scheduled task: %w", err)
		}
		if _, err := d.Exec(ctx, "schtasks", []string{"/Run", "/TN", executorTaskName}, nil); err != nil {
			return fmt.Errorf("start the scheduled task: %w", err)
		}
	case "darwin":
		uid, err := userID(ctx, d)
		if err != nil {
			return err
		}
		// A label that is loaded cannot be bootstrapped again, running or not (waiting, throttled): it is booted out first, when it is there.
		_, _ = d.Exec(ctx, "launchctl", []string{"bootout", "gui/" + uid + "/" + executorLabel}, nil)
		if _, err := d.Exec(ctx, "launchctl", []string{"bootstrap", "gui/" + uid, p.definition}, nil); err != nil {
			return fmt.Errorf("load the launch agent: %w", err)
		}
		if _, err := d.Exec(ctx, "launchctl", []string{"kickstart", "-k", "gui/" + uid + "/" + executorLabel}, nil); err != nil {
			return fmt.Errorf("start the launch agent: %w", err)
		}
	default:
		for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", executorUnitName}} {
			if _, err := d.Exec(ctx, "systemctl", args, nil); err != nil {
				return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
			}
		}
	}
	return nil
}

// remove stops the executor and takes it out of the operating system when registered says it is there, and deletes its files, the token copy apart:
// the program, its pin, its log and the definitions. A stop that fails (a process of the Windows tree that is left) fails the removal before anything
// is deleted. On Windows the stop runs without a registered task too: a wrapper whose task was deleted by hand still starts the program.
func (p executorPlan) remove(ctx context.Context, d Deps, registered bool) error {
	if registered || p.goos == "windows" {
		if err := p.stop(ctx, d); err != nil {
			return err
		}
	}
	if registered {
		switch p.goos {
		case "windows":
			if _, err := d.Exec(ctx, "schtasks", []string{"/Delete", "/TN", executorTaskName, "/F"}, nil); err != nil {
				return fmt.Errorf("delete the scheduled task: %w", err)
			}
		case "linux":
			if _, err := d.Exec(ctx, "systemctl", []string{"--user", "disable", "--now", executorUnitName}, nil); err != nil {
				return fmt.Errorf("systemctl --user disable --now %s: %w", executorUnitName, err)
			}
		}
	}
	if p.definition != "" {
		if err := os.Remove(p.definition); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p.definition, err)
		}
	}
	if err := os.RemoveAll(p.dir); err != nil {
		return fmt.Errorf("remove %s: %w", p.dir, err)
	}
	if registered && p.goos == "linux" {
		if _, err := d.Exec(ctx, "systemctl", []string{"--user", "daemon-reload"}, nil); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %w", err)
		}
	}
	return nil
}

// foreign says whether the executor that this user has registered belongs to another instance. The unit, the launch agent and the task have one
// name for each user, so an executor of another token file is registered under the same one: its definition names a program in another directory.
// registered is what state said. On Windows the task is read from no answer of schtasks, which comes in the console code page and not in UTF-8
// (a profile path with a letter outside ASCII would not match): a registered task is this instance's when this instance made its two files.
func (p executorPlan) foreign(registered bool) (bool, error) {
	switch p.goos {
	case "windows":
		return registered && (!pathExists(p.wrapper) || !pathExists(p.taskXML)), nil
	case "darwin":
		return definitionNames(p.definition, xmlEscaper.Replace(p.binary))
	default:
		return definitionNames(p.definition, unitValue(p.binary))
	}
}

// definitionNames says that the file exists and does not name the program: the definition is another instance's.
func definitionNames(file, program string) (other bool, err error) {
	raw, err := os.ReadFile(filepath.Clean(file))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", file, err)
	}
	return !strings.Contains(string(raw), program), nil
}
