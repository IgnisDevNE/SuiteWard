//go:build windows

package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// The command goes to the window in the environment of its process, not in its command line: PowerShell reads the command line as
// code, and the test checks that name the repositories are free text.
const (
	envSetupBin  = "SUITEWARD_SETUP_BIN"
	envSetupArgs = "SUITEWARD_SETUP_ARGS"
	envSetupExit = "SUITEWARD_SETUP_EXIT"
)

// windowScript is what PowerShell runs in the window: the engine runs as a child on the console of the window, its exit code is kept (a
// command that cannot start is a failure) and left in a file for the installer, and the window waits for Enter so that the secret can be
// copied. The screen and its scrollback are cleared in a finally block, so that Ctrl-C at the prompt clears them too. Windows Terminal,
// which a new console may be handed to, keeps a pane open when its process ends with a code other than 0, so the script ends with 0 once
// the code is where the installer reads it (windowOutcome reads that file first), and with the code of setup when it could not leave it.
// The limit: a hard kill of the window (the X after the grace period, Stop-Process) runs no finally block, and what the screen showed
// stays until the pane closes. It has no double quote: the command line of PowerShell would have to escape it.
const windowScript = `$Host.UI.RawUI.WindowTitle = 'SuiteWard setup'; $code = 1; $left = $false; ` +
	`try { ` +
	`try { $p = Start-Process -FilePath $env:SUITEWARD_SETUP_BIN -ArgumentList $env:SUITEWARD_SETUP_ARGS -NoNewWindow -Wait -PassThru; $code = $p.ExitCode } catch { Write-Host $_ }; ` +
	`try { Set-Content -LiteralPath $env:SUITEWARD_SETUP_EXIT -Value $code -ErrorAction Stop; $left = $true } catch { Write-Host $_ }; ` +
	`Read-Host 'Press Enter to close this window' | Out-Null ` +
	`} finally { Clear-Host; [Console]::Write([char]27 + '[3J') }; ` +
	`if ($left) { exit 0 }; exit $code`

// windowEnv is the environment of the window: the one OSInteractive gives the command, and the command, its arguments and the file for
// its exit code as data.
func windowEnv(environ []string, name string, args []string, exitFile string) []string {
	return append(childEnv(environ, name, args), envSetupBin+"="+name, envSetupArgs+"="+windows.ComposeCommandLine(args), envSetupExit+"="+exitFile)
}

func platformSetupWindow() windowStarter { return startConsoleWindow }

// startProcess is syscall.StartProcess: the installer runs PowerShell on purpose, from the system directory and with a fixed script.
var startProcess = syscall.StartProcess

// startConsoleWindow runs the command in PowerShell in a console window of its own. The window waits for Enter after the command, and the
// wait returns how it ended.
func startConsoleWindow(ctx context.Context, name string, args []string) (func() error, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", name, err)
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("find the system directory: %w", err)
	}
	powershell := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	// shortcut: a failed sweep is dropped, because a windowStarter has no way to hand the user a note; give it one when the starter learns to.
	_ = sweepSetupDirs(os.TempDir(), time.Now())
	dir, err := os.MkdirTemp("", setupDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("make a directory for the exit code of setup: %w", err)
	}
	exitFile := filepath.Join(dir, "exit")
	// No standard handles: CreateProcess with CREATE_NEW_CONSOLE then gives the child the console it makes. exec.Cmd hands the child NUL
	// instead, and a console program on NUL ends at once.
	pid, handle, err := startProcess(powershell, nil, &syscall.ProcAttr{
		Env:   windowEnv(os.Environ(), bin, args, exitFile),
		Files: []uintptr{0, 0, 0},
		Sys: &syscall.SysProcAttr{
			CreationFlags: windows.CREATE_NEW_CONSOLE,
			CmdLine:       windows.ComposeCommandLine([]string{powershell, "-NoProfile", "-Command", windowScript}),
		},
	})
	if err != nil {
		_ = os.RemoveAll(dir) // only the file of an exit code that no window will leave
		return nil, fmt.Errorf("start %s: %w", powershell, err)
	}
	process := windows.Handle(handle)
	ended := make(chan error, 1)
	go func() { ended <- waitProcess(process) }()
	return func() error {
		defer func() { _ = windows.CloseHandle(process) }()
		defer func() { _ = os.RemoveAll(dir) }()
		select {
		case err := <-ended:
			return windowOutcome(exitFile, err)
		case <-ctx.Done():
			killTree(system, pid, process)
			<-ended
			return windowOutcome(exitFile, ctx.Err()) // setup that ended well before Ctrl-C is not an interrupted setup
		}
	}, nil
}

// closedWindowCode is the exit code of a console program that its window took with it: the window was closed, or Ctrl-C ended it.
const closedWindowCode = 0xC000013A

// waitProcess waits for the process and reports how it ended: nothing for exit code 0.
func waitProcess(process windows.Handle) error {
	event, err := windows.WaitForSingleObject(process, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("wait for the setup window: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("wait for the setup window: unexpected result %d", event)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return fmt.Errorf("read the exit code of the setup window: %w", err)
	}
	switch code {
	case 0:
		return nil
	case closedWindowCode:
		return fmt.Errorf("the setup window was closed before setup ended (exit code %#x)", code)
	}
	return fmt.Errorf("the setup window ended with exit code %d", code)
}

// killTree ends PowerShell and the engine it started: ending PowerShell alone would leave the engine in a window that stays open, and
// the secret with it. taskkill is the one of the system directory, as PowerShell is, and not whatever the PATH finds first.
func killTree(system string, pid int, process windows.Handle) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := commandContext(ctx, filepath.Join(system, "taskkill.exe"), "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		_ = windows.TerminateProcess(process, 1) // the best that is left; the process may have ended on its own meanwhile
	}
}

// setupDirPrefix names the folder that startConsoleWindow makes for the exit code of a window; staleSetupDir is how long the exit file of
// one must have been there before a later setup removes the folder.
const (
	setupDirPrefix = "suiteward-setup-"
	staleSetupDir  = 24 * time.Hour
)

// sweepSetupDirs removes the folders that earlier windows left in tmp (a crash, a power cut, a file that antivirus held open): the ones
// that hold their exit file, which a window leaves once setup ends, when that file is more than a day old. A folder with no exit file, or a
// newer one, may be the setup of another enroll or install in flight, and what is not a folder of the starter's (a file, a link) is not
// touched. It removes what it can and reports every folder it could not.
func sweepSetupDirs(tmp string, now time.Time) error {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return fmt.Errorf("list %s: %w", tmp, err)
	}
	var failed []error
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), setupDirPrefix) { // IsDir is false for a link or a junction
			continue
		}
		dir := filepath.Join(tmp, e.Name())
		exit, err := os.Lstat(filepath.Join(dir, "exit"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			failed = append(failed, fmt.Errorf("look in %s: %w", dir, err))
			continue
		}
		if !exit.Mode().IsRegular() || now.Sub(exit.ModTime()) <= staleSetupDir {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			failed = append(failed, fmt.Errorf("remove %s: %w", dir, err))
		}
	}
	return errors.Join(failed...)
}
