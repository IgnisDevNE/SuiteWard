package installer

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

// A real stop of the executor on Windows (owner decision 18). `schtasks /End` ends only the console host of the task: the wrapper (cmd.exe) and the
// program live on, and the wrapper starts the program again after its pause. So every path that stops or replaces the executor ends the whole tree:
// the task, then the processes that the executor's own binary path and the wrapper's path name (never a process by its name alone: another
// instance's executor must survive), with their children. A stop that leaves a process says so and fails before the program is replaced.

// osProc is a process of the fake Windows computer.
type osProc struct {
	pid, parent int
	name        string
	exe, cmd    string
}

// processTable answers the installer's two questions about processes, with the table as its state: the listing (what the PowerShell query of the
// installer prints: one line for each console host, command interpreter or executor, `pid name base64(path) base64(command line)`) and taskkill.
type processTable struct {
	procs    []osProc
	stubborn map[int]bool // processes that taskkill cannot end
	listErr  error
	silent   bool    // the listing prints nothing and exits 0, as PowerShell does when its language mode refuses every call of the script
	junk     string  // a line that is not a process, put before the others
	kills    [][]int // the PIDs of each taskkill, in order
}

// listingEnd is the last line of the listing that the installer's script prints. A listing without it is not trusted: a script that failed half way,
// or that PowerShell's constrained language mode refused for every process, ends with nothing printed and an exit code of 0, and a stop that took
// that for "nothing runs" would start a second tree beside a wrapper that is alive.
const listingEnd = "suiteward-listing-end"

func (pt *processTable) install(d *Deps) {
	inner := d.Exec
	d.Exec = func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
		out, err := inner(ctx, name, args, stdin) // records the call
		switch baseName(name) {
		case "powershell":
			if pt.listErr != nil {
				return nil, pt.listErr
			}
			if pt.silent {
				return nil, nil
			}
			return []byte(pt.junk + listingOf(pt.procs)), nil
		case "taskkill":
			pt.kill(args)
			return nil, nil
		}
		return out, err
	}
}

// listingOf is what the installer's script prints for these processes.
func listingOf(procs []osProc) string {
	var b strings.Builder
	for _, p := range procs {
		if slices.Contains([]string{"conhost.exe", "cmd.exe", "suiteward-executor.exe"}, strings.ToLower(p.name)) {
			enc := base64.StdEncoding.EncodeToString
			fmt.Fprintf(&b, "%d %s %s %s\r\n", p.pid, p.name, enc([]byte(p.exe)), enc([]byte(p.cmd)))
		}
	}
	return b.String() + listingEnd + "\r\n"
}

// kill is `taskkill /T /F /PID n ...`: each process and its children, but not a stubborn one.
func (pt *processTable) kill(args []string) {
	var pids []int
	for i, a := range args {
		if a == "/PID" && i+1 < len(args) {
			pid, err := strconv.Atoi(args[i+1])
			if err != nil {
				panic("taskkill was given a PID that is not a number: " + args[i+1])
			}
			pids = append(pids, pid)
		}
	}
	pt.kills = append(pt.kills, pids)
	for _, pid := range pids {
		pt.end(pid, slices.Contains(args, "/T"))
	}
}

func (pt *processTable) end(pid int, tree bool) {
	if tree {
		for _, p := range pt.procs {
			if p.parent == pid {
				pt.end(p.pid, true)
			}
		}
	}
	if !pt.stubborn[pid] {
		pt.procs = slices.DeleteFunc(pt.procs, func(p osProc) bool { return p.pid == pid })
	}
}

func (pt *processTable) alive() []int {
	var pids []int
	for _, p := range pt.procs {
		pids = append(pids, p.pid)
	}
	return pids
}

func (pt *processTable) has(pid int) bool { return slices.Contains(pt.alive(), pid) }

// killedPIDs is every PID that any taskkill was given.
func (pt *processTable) killedPIDs() []int { return slices.Concat(pt.kills...) }

// treeOf is the processes of the executor of one instance as Windows lists them when its task runs: the console host with the wrapper's path as the
// argument of the task, the command interpreter, and the program. first is the PID of the host; the other two follow it.
func treeOf(first int, wrapper, binary string) []osProc {
	system := `C:\Windows\System32`
	script := `.\run.cmd "` + wrapper + `"`
	return []osProc{
		{pid: first, parent: 4, name: "conhost.exe", exe: system + `\conhost.exe`, cmd: `"` + system + `\conhost.exe" --headless "` + system + `\cmd.exe" /d /c ` + script},
		{pid: first + 1, parent: first, name: "cmd.exe", exe: system + `\cmd.exe`, cmd: `"` + system + `\cmd.exe" /d /c ` + script},
		{pid: first + 2, parent: first + 1, name: "suiteward-executor.exe", exe: binary, cmd: `"` + binary + `" execute`},
	}
}

// anotherInstancesProcesses are what must survive every stop of this instance: the tree of an executor of another folder, an executor that has the
// name of ours and another path, and a command prompt of the user.
func anotherInstancesProcesses(root string) []osProc {
	other := treeOf(200, filepath.Join(root, "elsewhere", "executor", "run.cmd"), filepath.Join(root, "elsewhere", "executor", "suiteward-executor.exe"))
	return append(other,
		osProc{pid: 300, parent: 4, name: "suiteward-executor.exe", exe: filepath.Join(root, "downloads", "suiteward-executor.exe"), cmd: "suiteward-executor.exe execute"},
		osProc{pid: 301, parent: 4, name: "cmd.exe", exe: `C:\Windows\System32\cmd.exe`, cmd: `"C:\Windows\System32\cmd.exe"`},
	)
}

var survivors = []int{200, 201, 202, 300, 301}

// reinstalled is a second install on a Windows computer whose first install made the executor, with a newer program: the task is registered, and the
// fake computer has the processes that procs says (it gets this instance's wrapper and program paths).
type reinstalled struct {
	first, again *harness
	table        *processTable
	files        map[string][]byte // the executor's files before the second install
	err          error
}

func reinstallOnWindows(t *testing.T, running bool, procs func(wrapper, binary string) []osProc, tune func(*processTable)) reinstalled {
	t.Helper()
	first := machineHarnessOn(t, "windows")
	if err := first.podman(); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(t.TempDir(), "suiteward.exe")
	if err := os.WriteFile(newer, []byte("the next release"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := machineHarnessOn(t, "windows")
	again.dir, again.exe = first.dir, newer
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	again.first("/Query /TN SuiteWard Executor", "") // the task of the first install is registered
	if running {
		again.f.on("IMAGENAME eq suiteward-executor.exe", `"suiteward-executor.exe","4242","Console","1","20,000 K"`)
	}
	wrapper := filepath.Join(again.executorDir(), "run.cmd")
	table := &processTable{procs: slices.Concat(procs(wrapper, again.executorBinary()), anotherInstancesProcesses(again.dir)), stubborn: map[int]bool{}}
	if tune != nil {
		tune(table)
	}
	files := map[string][]byte{}
	for _, name := range []string{"run.cmd", "task.xml", "suiteward-executor.exe", "executor.pin"} {
		files[name] = mustRead(t, filepath.Join(again.executorDir(), name))
	}
	env := again.newEnv()
	table.install(&env.Deps)
	args := []string{"--token-file", again.tokenPath(), "--settings", again.settingsPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage}
	return reinstalled{first: first, again: again, table: table, files: files, err: Install(context.Background(), args, env)}
}

func (r reinstalled) wantOnlyThisInstancesTreeEnded(t *testing.T) {
	t.Helper()
	for _, pid := range r.table.killedPIDs() {
		if slices.Contains(survivors, pid) {
			t.Errorf("taskkill was given process %d, which is not this instance's: %v", pid, r.table.kills)
		}
	}
	if left := r.table.alive(); !slices.Equal(left, survivors) {
		t.Errorf("the processes left are %v, want only the others' %v", left, survivors)
	}
}

func TestInstallEndsTheWholeTreeOfTheExecutorBeforeItReplacesTheProgramOnWindows(t *testing.T) {
	r := reinstallOnWindows(t, true, func(wrapper, binary string) []osProc { return treeOf(100, wrapper, binary) }, nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.wantOnlyThisInstancesTreeEnded(t)
	r.again.assertOrder("/End /TN SuiteWard Executor", "Win32_Process", "/T /F /PID", "/Create /TN SuiteWard Executor", "/Run /TN SuiteWard Executor")
	r.again.assertOrder("/End /TN SuiteWard Executor", "/T /F /PID", "/Create /TN SuiteWard Executor")
	if got := r.first.readHome("executor/suiteward-executor.exe"); got != "the next release" {
		t.Errorf("the executor still has %q", got)
	}
	// The wrapper's tree first: ended after the program, its pause would start the program again.
	if kill := r.table.kills[0]; slices.Index(kill, 102) < slices.Index(kill, 100) || slices.Index(kill, 102) < slices.Index(kill, 101) {
		t.Errorf("the program is ended before the wrapper that would start it again: %v", r.table.kills)
	}
}

// The wrapper waits a minute between two runs: a stop in that pause finds no program, and a wrapper that is left starts the old program with the old
// environment again, next to the new one.
func TestInstallEndsTheWrapperThatWaitsBetweenTwoRunsOfTheExecutor(t *testing.T) {
	r := reinstallOnWindows(t, false, func(wrapper, binary string) []osProc { return treeOf(100, wrapper, binary)[:2] }, nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	r.wantOnlyThisInstancesTreeEnded(t)
	r.again.assertOrder("/End /TN SuiteWard Executor", "/T /F /PID", "/Create /TN SuiteWard Executor")
}

func TestInstallDoesNotReplaceTheExecutorWhenItCannotBeStoppedOnWindows(t *testing.T) {
	r := reinstallOnWindows(t, true, func(wrapper, binary string) []osProc { return treeOf(100, wrapper, binary) }, func(pt *processTable) { pt.stubborn[102] = true })
	if r.err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", r.err)
	}
	out := r.again.out.String()
	for _, want := range []string{"could not be set up", "still running", "102"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	for name, before := range r.files {
		if got := mustRead(t, filepath.Join(r.again.executorDir(), name)); string(got) != string(before) {
			t.Errorf("%s was rewritten while the executor still ran", name)
		}
	}
	if len(r.again.f.find("/Create /TN")) != 0 || len(r.again.f.find("/Run /TN")) != 0 {
		t.Errorf("the task was created or started again: %v", r.again.f.calls)
	}
	if !r.table.has(200) || !r.table.has(300) {
		t.Errorf("another instance's executor was ended: %v", r.table.alive())
	}
}

func TestInstallDoesNotReplaceTheExecutorWhenTheProcessesCannotBeListedOnWindows(t *testing.T) {
	r := reinstallOnWindows(t, true, func(wrapper, binary string) []osProc { return treeOf(100, wrapper, binary) }, func(pt *processTable) { pt.listErr = errors.New("powershell: not found") })
	if r.err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", r.err)
	}
	if out := r.again.out.String(); !strings.Contains(out, "could not be set up") || !strings.Contains(out, "powershell: not found") {
		t.Errorf("the output must say that the executor could not be set up, and why:\n%s", out)
	}
	if got := mustRead(t, filepath.Join(r.again.executorDir(), "suiteward-executor.exe")); string(got) != string(r.files["suiteward-executor.exe"]) {
		t.Error("the program was replaced while nobody knew what still ran")
	}
}

// A first install has no task and no process: there is nothing to end, and nothing is asked of PowerShell.
func TestAFirstInstallOnWindowsEndsNothing(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"/End /TN", "/T /F", "Win32_Process"} {
		if got := h.f.find(marker); len(got) != 0 {
			t.Errorf("a first install called %s: %v", marker, h.f.lines())
		}
	}
}

// legacyTask is the task of the executor before the headless console: it runs the wrapper itself, which opens a window, and asks for the restarts that
// Task Scheduler does not make.
func legacyTask(plan executorPlan) []byte {
	xml := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Settings><RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure></Settings>
  <Actions Context="Author"><Exec><Command>` + plan.wrapper + `</Command><WorkingDirectory>` + plan.dir + `</WorkingDirectory></Exec></Actions>
</Task>
`
	return utf16Bytes(xml)
}

func utf16Bytes(text string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(text)) {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

// An install of an earlier build (a task that runs run.cmd with a window) is replaced by the new definition on `install` again and on `install
// --upgrade`, with the old tree ended: its cmd.exe is a child of the console of Windows Terminal, and its command line names the wrapper.
func TestAnInstallFromBeforeTheHeadlessConsoleIsReplacedAndItsOldTreeEnded(t *testing.T) {
	for _, via := range []string{"install", "install --upgrade"} {
		t.Run(via, func(t *testing.T) {
			first := machineHarnessOn(t, "windows")
			if err := first.podman(); err != nil {
				t.Fatal(err)
			}
			again := machineHarnessOn(t, "windows")
			again.dir, again.exe = first.dir, first.program() // the same program: only the definition is old
			again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
			again.first("/Query /TN SuiteWard Executor", "")
			again.f.on("IMAGENAME eq suiteward-executor.exe", `"suiteward-executor.exe","4242","Console","1","20,000 K"`)
			env := again.newEnv()
			plan, err := planExecutor(env.Deps, again.tokenPath(), Settings{HTTPPort: 27081, Runtime: RuntimePodman, Connection: "pm"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(plan.wrapper, []byte("@echo off\r\n\""+plan.binary+"\" execute >> \""+plan.log+"\" 2>&1\r\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(plan.taskXML, legacyTask(plan), 0o600); err != nil {
				t.Fatal(err)
			}
			old := []osProc{
				{pid: 100, parent: 4, name: "cmd.exe", exe: `C:\Windows\system32\cmd.exe`, cmd: `C:\Windows\system32\cmd.exe /c ""` + plan.wrapper + `" "`},
				{pid: 101, parent: 100, name: "suiteward-executor.exe", exe: plan.binary, cmd: `"` + plan.binary + `" execute`},
			}
			table := &processTable{procs: slices.Concat(old, anotherInstancesProcesses(again.dir)), stubborn: map[int]bool{}}
			table.install(&env.Deps)
			if via == "install" {
				args := []string{"--token-file", again.tokenPath(), "--settings", again.settingsPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage}
				if err := Install(context.Background(), args, env); err != nil {
					t.Fatal(err)
				}
			} else {
				refreshExecutor(context.Background(), env, nil, Settings{HTTPPort: 27081, Runtime: RuntimePodman, Connection: "pm"}, again.tokenPath(), "")
			}
			if left := table.alive(); !slices.Equal(left, survivors) {
				t.Errorf("the processes left are %v, want only the others' %v", left, survivors)
			}
			action, hasRestart := taskAction(t, utf16le(t, mustRead(t, plan.taskXML)))
			if !strings.HasSuffix(action.Command, `\conhost.exe`) || hasRestart {
				t.Errorf("the task was not replaced by the new definition: %+v (restart %v)", action, hasRestart)
			}
			again.assertOrder("/End /TN SuiteWard Executor", "/T /F /PID", "/Create /TN SuiteWard Executor", "/Run /TN SuiteWard Executor")
		})
	}
}

// An uninstall, which also ends what a purge removes, ends the whole tree before it deletes the task and the executor's folder, and leaves another
// instance's processes.
func windowsUninstall(t *testing.T, tune func(*processTable)) (un *harness, table *processTable, err error) {
	t.Helper()
	un = machineHarnessOn(t, "windows")
	podmanStackExists(un)
	un.writeFiles()
	un.writeExecutorFiles()
	un.first("/Query /TN SuiteWard Executor", "") // the task is registered, and this instance made its two files
	writeFile(t, filepath.Join(un.executorDir(), executorWrapperName), "the wrapper")
	writeFile(t, filepath.Join(un.executorDir(), "task.xml"), "the task")
	un.f.on("IMAGENAME eq suiteward-executor.exe", `"suiteward-executor.exe","4242","Console","1","20,000 K"`)
	wrapper := filepath.Join(un.executorDir(), executorWrapperName)
	table = &processTable{procs: slices.Concat(treeOf(100, wrapper, un.executorBinary()), anotherInstancesProcesses(un.dir)), stubborn: map[int]bool{}}
	if tune != nil {
		tune(table)
	}
	env := un.newEnv()
	table.install(&env.Deps)
	args := []string{"--settings", un.settingsPath(), "--token-file", un.tokenPath(), "--runtime", "podman", "--connection", "pm", "--yes"}
	return un, table, Uninstall(context.Background(), args, env)
}

func TestUninstallEndsTheWholeTreeOfTheExecutorBeforeItDeletesTheTaskOnWindows(t *testing.T) {
	un, table, err := windowsUninstall(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if left := table.alive(); !slices.Equal(left, survivors) {
		t.Errorf("the processes left are %v, want only the others' %v", left, survivors)
	}
	un.assertOrder("/End /TN SuiteWard Executor", "/T /F /PID", "/Delete /TN SuiteWard Executor /F")
	if _, err := os.Stat(un.executorDir()); !os.IsNotExist(err) {
		t.Errorf("the executor's folder is still there: %v", err)
	}
}

func TestUninstallKeepsTheExecutorWhenItCannotBeEndedOnWindowsAndSaysSo(t *testing.T) {
	un, _, err := windowsUninstall(t, func(pt *processTable) { pt.stubborn[102] = true })
	if err != nil {
		t.Fatalf("an uninstall must go on with the stack: %v", err)
	}
	out := un.out.String()
	for _, want := range []string{"was not removed", "still running", "102"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if len(un.f.find("/Delete /TN")) != 0 {
		t.Errorf("the task was deleted while the executor still ran: %v", un.f.calls)
	}
	if !pathExists(un.executorDir()) {
		t.Error("the executor's folder was removed while its program still ran")
	}
	if len(un.f.find("rm --force --ignore --time 45 suiteward")) == 0 {
		t.Errorf("the stack was not removed: %v", un.f.calls)
	}
}

// `schtasks /End` ends only the console host of the task, so a wrapper is left, waiting or running (what an earlier release's documents told people
// to run, and what a closed console host leaves). An install again with nothing changed finds the task registered and no program running, and
// starts the task: the wrapper that is left would start a second program next to the new one, and two executors on one engine remove each other's
// sandboxes. The install ends what is left first.
func TestAnInstallAgainEndsAWrapperThatIsLeftWhenNothingChangedOnWindows(t *testing.T) {
	first := machineHarnessOn(t, "windows")
	if err := first.podman(); err != nil {
		t.Fatal(err)
	}
	again := machineHarnessOn(t, "windows")
	again.dir, again.exe = first.dir, first.program() // the same program and the same definition: nothing changes
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	again.first("/Query /TN SuiteWard Executor", "") // the task is registered, and no program runs: tasklist finds none
	wrapper := filepath.Join(again.executorDir(), "run.cmd")
	left := treeOf(100, wrapper, again.executorBinary())[1:2] // the wrapper of the console host that ended
	table := &processTable{procs: slices.Concat(left, anotherInstancesProcesses(again.dir)), stubborn: map[int]bool{}}
	env := again.newEnv()
	table.install(&env.Deps)
	args := []string{"--token-file", again.tokenPath(), "--settings", again.settingsPath(), "--runtime", "podman", "--connection", "pm", "--image", testImage}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	if left := table.alive(); !slices.Equal(left, survivors) {
		t.Errorf("the processes left are %v, want only the others' %v", left, survivors)
	}
	again.assertOrder("/T /F /PID", "/Run /TN SuiteWard Executor")
}

// Under PowerShell's constrained language mode (AppLocker, WDAC) the script's method calls are refused for every process, and PowerShell prints
// nothing and exits 0. The script stops at its first error, and it ends with a line that the installer requires, so that no failure of the listing
// reads as "no process runs".
func TestTheListingScriptStopsAtItsFirstErrorAndEndsWithASentinel(t *testing.T) {
	if !strings.HasPrefix(listProcessesScript, "$ErrorActionPreference = 'Stop'; ") {
		t.Errorf("the script does not stop at its first error:\n%s", listProcessesScript)
	}
	if !strings.HasSuffix(listProcessesScript, "; '"+listingEnd+"'") {
		t.Errorf("the script does not end by printing %q:\n%s", listingEnd, listProcessesScript)
	}
}

func TestInstallDoesNotReplaceTheExecutorWhenTheListingIsEmptyOnWindows(t *testing.T) {
	r := reinstallOnWindows(t, true, func(wrapper, binary string) []osProc { return treeOf(100, wrapper, binary) }, func(pt *processTable) { pt.silent = true })
	if r.err != nil {
		t.Fatalf("the stack is up and the install does not fail for the executor: %v", r.err)
	}
	if out := r.again.out.String(); !strings.Contains(out, "could not be set up") || !strings.Contains(out, "could not list the programs") {
		t.Errorf("the output must say that the executor could not be set up because the programs could not be listed:\n%s", out)
	}
	for name, before := range r.files {
		if got := mustRead(t, filepath.Join(r.again.executorDir(), name)); string(got) != string(before) {
			t.Errorf("%s was rewritten while nobody knew what still ran", name)
		}
	}
	if len(r.again.f.find("/Create /TN")) != 0 || len(r.again.f.find("/Run /TN")) != 0 {
		t.Errorf("the task was created or started again: %v", r.again.f.lines())
	}
}

func TestUninstallKeepsTheExecutorWhenTheListingIsEmptyOnWindows(t *testing.T) {
	un, _, err := windowsUninstall(t, func(pt *processTable) { pt.silent = true })
	if err != nil {
		t.Fatalf("an uninstall must go on with the stack: %v", err)
	}
	if out := un.out.String(); !strings.Contains(out, "was not removed") || !strings.Contains(out, "could not list the programs") {
		t.Errorf("the output must say that the executor was not removed because the programs could not be listed:\n%s", out)
	}
	if len(un.f.find("/Delete /TN")) != 0 || !pathExists(un.executorDir()) {
		t.Errorf("the task or the folder of an executor that may still run was removed: %v", un.f.lines())
	}
}

// A line of the listing that is not a process may hold the command line of another program: the error names what is wrong with it, never the line.
func TestAMalformedLineOfTheListingIsNeverEchoed(t *testing.T) {
	for name, junk := range map[string]string{
		"too few fields":   "worker-secret-command-line --token=abc\r\n",
		"no process id":    "x conhost.exe worker-secret-command-line worker-secret-command-line\r\n",
		"not base64 paths": "7 cmd.exe worker-secret-command-line! worker-secret-command-line!\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := reinstallOnWindows(t, true, func(wrapper, binary string) []osProc { return treeOf(100, wrapper, binary) }, func(pt *processTable) { pt.junk = junk })
			if r.err != nil {
				t.Fatal(r.err)
			}
			out := r.again.out.String()
			if strings.Contains(out, "worker-secret-command-line") {
				t.Errorf("the output echoes a line of the listing:\n%s", out)
			}
			if !strings.Contains(out, "could not list the programs") {
				t.Errorf("the output must say that the programs could not be listed:\n%s", out)
			}
		})
	}
}
