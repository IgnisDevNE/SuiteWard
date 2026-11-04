//go:build windows

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// What the log says, in plain words: once, when the run ends because the console host of its loop is gone, and once when the host cannot be watched
// or its console cannot be given up.
const (
	consoleHostGone      = "The test runner stopped because its task was stopped."
	consoleHostUnwatched = "The test runner cannot see when its task is stopped, so stopping the task will not stop it."
	consoleNotFreed      = "The test runner could not give up the console of its task, so it may not be able to stop the sandbox of a job that runs."
)

// procFreeConsole is FreeConsole, which golang.org/x/sys/windows has no wrapper for.
var procFreeConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole")

// processEntry is a process of the computer as the snapshot lists it: the process that started it, and the name of its image.
type processEntry struct {
	parent uint32
	name   string
}

// consoleHostOf is the process id of the console host that started the loop of run.cmd, when self runs in that loop. The task runs
// `conhost.exe --headless cmd.exe /d /c run.cmd`, and the script starts the executor: the executor is the child of cmd.exe, which is the child of
// conhost.exe. Any other chain is a person's (a terminal, an Explorer window, a script), and has no host that this watch may end the run for.
// shortcut: the ids come from one snapshot and a reused id is not checked (the real host would have to be gone already), add a creation-time check if a stale chain is ever seen.
func consoleHostOf(self uint32, procs map[uint32]processEntry) (uint32, bool) {
	me, ok := procs[self]
	if !ok {
		return 0, false
	}
	script, ok := procs[me.parent]
	if !ok || !strings.EqualFold(script.name, "cmd.exe") {
		return 0, false
	}
	host, ok := procs[script.parent]
	if !ok || !strings.EqualFold(host.name, "conhost.exe") {
		return 0, false
	}
	return script.parent, true
}

// processEntries lists the processes of the computer.
func processEntries() (map[uint32]processEntry, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	procs := map[uint32]processEntry{}
	for err := windows.Process32First(snapshot, &entry); ; err = windows.Process32Next(snapshot, &entry) {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return procs, nil
		}
		if err != nil {
			return nil, err
		}
		procs[entry.ProcessID] = processEntry{parent: entry.ParentProcessID, name: windows.UTF16ToString(entry.ExeFile[:])}
	}
}

// openConsoleHost opens the console host of this process's loop to wait on it. A process that does not run in the loop of the task has none, and that
// is no error: nothing is watched.
func openConsoleHost() (windows.Handle, bool, error) {
	procs, err := processEntries()
	if err != nil {
		return 0, false, fmt.Errorf("list the processes of the computer: %w", err)
	}
	pid, found := consoleHostOf(windows.GetCurrentProcessId(), procs)
	if !found {
		return 0, false, nil
	}
	host, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return 0, false, fmt.Errorf("open the console host (process %d): %w", pid, err)
	}
	return host, true, nil
}

// consoleHostPollMs is how long one wait on the console host lasts, in milliseconds.
const consoleHostPollMs = 1000

// watchConsoleHost ends the run when the console host of the loop that started the executor exits. A stop of the scheduled task (Stop-ScheduledTask)
// ends only that conhost.exe: the script and the executor live on without a console, and the script would start the executor again. The context it
// returns is cancelled then, so the run ends as at the signal (the job in flight gets its grace, its sandbox is stopped, the exit is clean). It
// stays alive while the host does, and a person's run of `suiteward execute` has no such host (consoleHostOf), so nothing is watched and nothing is
// logged. When the host cannot be opened the run goes on and says so once: stopping the task will not stop it.
func watchConsoleHost(ctx context.Context, log *slog.Logger) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	host, found, err := openConsoleHost()
	switch {
	case err != nil:
		log.Warn(consoleHostUnwatched, "error", err)
	case found:
		go func() {
			defer func() { _ = windows.CloseHandle(host) }()
			// The wait is short so that the handle is closed here, after the run ended for another reason, and never while a wait uses it.
			for ctx.Err() == nil {
				event, err := windows.WaitForSingleObject(host, consoleHostPollMs)
				switch {
				case err != nil:
					log.Warn(consoleHostUnwatched, "error", err)
					return
				case event == windows.WAIT_OBJECT_0:
					// The console went with its host, and every console program that the executor starts from now on fails at once (0xc0000142), the container
					// CLI that stops the sandbox of a job in flight among them. Without a console the next program gets one of its own.
					if r, _, err := procFreeConsole.Call(); r == 0 {
						log.Warn(consoleNotFreed, "error", err)
					}
					log.Info(consoleHostGone)
					cancel()
					return
				}
			}
		}()
	}
	return ctx, cancel
}
