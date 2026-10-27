package installer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
		p.wrapper, p.taskXML = filepath.Join(p.dir, "run.cmd"), filepath.Join(p.dir, "task.xml")
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
	for _, v := range append([]string{p.dir, p.binary, p.tokenFile, p.definition}, envValues(p.env)...) {
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

// cmdValue is a value inside `set "K=V"` of a batch file: a percent sign is doubled so that cmd does not read a variable.
func cmdValue(v string) string { return strings.ReplaceAll(v, "%", "%%") }

// windowsWrapper is the script that the task runs: the environment of the executor, then the program. The window of the script shows, as a
// console program started by a task of the logged-on user does; its output goes to the log.
func (p executorPlan) windowsWrapper() string {
	var b strings.Builder
	b.WriteString("@echo off\r\nrem Written by `suiteward install`: the environment of the SuiteWard executor, then the executor. Do not put a credential here.\r\n")
	for _, e := range p.env {
		value := cmdValue(e.value)
		if e.key == "PATH" {
			value = cmdValue(strings.TrimSuffix(e.value, ";%PATH%")) + ";%PATH%"
		}
		fmt.Fprintf(&b, "set \"%s=%s\"\r\n", e.key, value)
	}
	fmt.Fprintf(&b, "\"%s\" execute >> \"%s\" 2>&1\r\n", p.binary, p.log)
	return b.String()
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// windowsTask is the definition of the scheduled task, as Task Scheduler reads it: UTF-16 little-endian with a byte order mark. It starts at the
// logon of the user, as the user, with no password and no elevation; it does not stop for the battery or after a time; and it starts the script
// again after a failure, once a minute.
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
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscaper.Replace(p.wrapper) + `</Command><WorkingDirectory>` + xmlEscaper.Replace(p.dir) + `</WorkingDirectory></Exec></Actions>
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

// stop stops the executor if it runs; a failure is nothing to act on, because the next step says what is wrong.
func (p executorPlan) stop(ctx context.Context, d Deps) {
	switch p.goos {
	case "windows":
		_, _ = d.Exec(ctx, "schtasks", []string{"/End", "/TN", executorTaskName}, nil)
	case "darwin":
		if uid, err := userID(ctx, d); err == nil {
			_, _ = d.Exec(ctx, "launchctl", []string{"bootout", "gui/" + uid + "/" + executorLabel}, nil)
		}
	default:
		_, _ = d.Exec(ctx, "systemctl", []string{"--user", "stop", executorUnitName}, nil)
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
// the program, its pin, its log and the definitions.
func (p executorPlan) remove(ctx context.Context, d Deps, registered bool) error {
	if registered {
		p.stop(ctx, d)
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
