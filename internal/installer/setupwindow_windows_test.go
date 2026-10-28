//go:build windows

package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowEnvCarriesTheCommandAsDataAndDropsWhatOSInteractiveDrops(t *testing.T) {
	environ := []string{`PATH=C:\Windows`, "CONTAINER_HOST=ssh://elsewhere", `USERPROFILE=C:\Users\me`}
	args := []string{"--connection", "pm", "exec", "-it", "suiteward", "/suiteward", "setup", "--owner", "alice", "--test-check", "acme/app=CI / test", "--test-check", `acme/lib=say "hi" & stop`}
	env := windowEnv(environ, "podman", args, `C:\Temp\suiteward-setup-1\exit`)
	want := map[string]string{
		"SUITEWARD_SETUP_EXIT": `C:\Temp\suiteward-setup-1\exit`,
		"SUITEWARD_SETUP_BIN":  "podman",
		"SUITEWARD_SETUP_ARGS": `--connection pm exec -it suiteward /suiteward setup --owner alice --test-check "acme/app=CI / test" --test-check "acme/lib=say \"hi\" & stop"`,
	}
	for key, value := range want {
		if !slices.Contains(env, key+"="+value) {
			t.Errorf("the environment of the window lacks %s=%s:\n%q", key, value, env)
		}
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "CONTAINER_HOST=") {
			t.Errorf("the window runs the engine with the CONTAINER_HOST that --connection must not meet: %q", env)
		}
	}
	if !slices.Contains(env, `USERPROFILE=C:\Users\me`) {
		t.Errorf("the window lost the environment of the installer: %q", env)
	}
}

// The script runs in PowerShell's command line, where the command's text would be code: it holds none of it, no quotes that the
// command line would have to escape, and it ends by clearing what the screen shows of the secret.
func TestWindowScriptHoldsNoDataAndClearsTheScreenBeforeItEnds(t *testing.T) {
	if strings.Contains(windowScript, `"`) {
		t.Errorf("the script has a double quote, which the command line of PowerShell would have to escape:\n%s", windowScript)
	}
	for _, want := range []string{"$env:SUITEWARD_SETUP_BIN", "$env:SUITEWARD_SETUP_ARGS", "Press Enter to close this window", "Clear-Host", "[char]27", "exit 0", "exit $code"} {
		if !strings.Contains(windowScript, want) {
			t.Errorf("the script lacks %q:\n%s", want, windowScript)
		}
	}
	if !strings.Contains(windowScript, "$code = 1") {
		t.Errorf("a window that cannot start the command must not end as a success:\n%s", windowScript)
	}
}

// Windows Terminal, which this system hands new consoles to, keeps a pane open when its process ends with a code other than 0, and a
// Ctrl-C at the prompt stops the script: the screen and its scrollback are cleared in a finally block, so that they are on every way out
// that PowerShell gets to run, and the script ends with 0 once the code of setup is where the installer reads it. (A hard kill of the
// window runs no finally block; the installer's own Ctrl-C ends the window whole, after the secret was shown and used.)
func TestWindowScriptClearsInAFinallyBlockAndEndsWithZeroOnceTheCodeIsLeft(t *testing.T) {
	leave, pause, finally := strings.Index(windowScript, "Set-Content -LiteralPath $env:SUITEWARD_SETUP_EXIT"), strings.Index(windowScript, "Read-Host"), strings.Index(windowScript, "finally")
	cleared, scrollback, ok, fallback := strings.Index(windowScript, "Clear-Host"), strings.Index(windowScript, "[char]27 + '[3J'"), strings.Index(windowScript, "exit 0"), strings.LastIndex(windowScript, "exit $code")
	if leave < 0 || pause < leave || finally < pause || cleared < finally || scrollback < cleared || ok < scrollback || fallback < ok {
		t.Errorf("the script must leave the code (%d), wait for Enter (%d), then in a finally block (%d) clear the screen (%d) and the scrollback (%d), then exit 0 (%d), else with the code (%d):\n%s",
			leave, pause, finally, cleared, scrollback, ok, fallback, windowScript)
	}
	if !strings.Contains(windowScript, "$left = $true") || !strings.Contains(windowScript, "if ($left)") {
		t.Errorf("the script ends with 0 only when the code was left: a window that could not leave it must end with the code of setup:\n%s", windowScript)
	}
}

// The code of setup is left for the installer before the window waits for Enter: a window that the user closes after that, with the X or
// with Ctrl-C, ends with a code of its own, and setup has ended well.
func TestWindowScriptLeavesTheExitCodeBeforeItWaitsForEnter(t *testing.T) {
	leave, pause := strings.Index(windowScript, "$env:SUITEWARD_SETUP_EXIT"), strings.Index(windowScript, "Read-Host")
	if leave < 0 || pause < leave {
		t.Errorf("the script must write $code to the file of SUITEWARD_SETUP_EXIT before Read-Host:\n%s", windowScript)
	}
	if !strings.Contains(windowScript[:max(leave, 0)], "$code = $p.ExitCode") {
		t.Errorf("the code that is left must be the one of the command, which is read before it is written:\n%s", windowScript)
	}
}

// swapPowerShell makes startConsoleWindow start the test binary, as the helper mode says, where it would start PowerShell: with no console
// and no window, so that no test opens one. It returns where the starter said that the exit code goes.
func swapPowerShell(t *testing.T, mode string) (exe string, exitFile *string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exitFile = new(string)
	original := startProcess
	startProcess = func(_ string, _ []string, attr *syscall.ProcAttr) (int, uintptr, error) {
		for _, entry := range attr.Env {
			if file, ok := strings.CutPrefix(entry, envSetupExit+"="); ok {
				*exitFile = file
			}
		}
		return original(exe, []string{exe}, &syscall.ProcAttr{
			Env:   append(slices.Clone(attr.Env), "SUITEWARD_TEST_HELPER="+mode),
			Dir:   filepath.Dir(*exitFile), // where the helper leaves its exit file, as "exit"
			Files: []uintptr{0, 0, 0},
			Sys:   &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW},
		})
	}
	t.Cleanup(func() { startProcess = original })
	return exe, exitFile
}

// Through the real code path: setup ended well and left its code, then the window was closed (the X, Ctrl-C at its prompt), which ends
// the window with the code of a closed console. That is a success, and the directory of the exit file is gone.
func TestWindowClosedAfterSetupLeftItsCodeIsASuccess(t *testing.T) {
	exe, exitFile := swapPowerShell(t, "window-left")
	wait, err := startConsoleWindow(context.Background(), exe, []string{"--", "setup"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Errorf("wait = %v, want success: setup left the code 0 before the window was closed", err)
	}
	if _, err := os.Stat(filepath.Dir(*exitFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory of the exit file is still there: %v", err)
	}
}

func TestWindowClosedBeforeSetupLeftACodeIsAFailure(t *testing.T) {
	exe, exitFile := swapPowerShell(t, "window-closed")
	wait, err := startConsoleWindow(context.Background(), exe, []string{"--", "setup"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wait(); err == nil || !strings.Contains(err.Error(), "closed before setup finished") {
		t.Errorf("wait = %v, want the report that the window was closed before setup finished", err)
	}
	if _, err := os.Stat(filepath.Dir(*exitFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory of the exit file is still there: %v", err)
	}
}

// Ctrl-C while setup is at its prompt ends the window (taskkill of the system directory, which also takes its children), and the wait
// reports the interrupt.
func TestCtrlCWhileTheWindowWaitsEndsItAndRemovesItsDirectory(t *testing.T) {
	exe, exitFile := swapPowerShell(t, "window-blocked")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait, err := startConsoleWindow(ctx, exe, []string{"--", "setup"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("wait = %v, want the interrupt", err)
	}
	if _, err := os.Stat(filepath.Dir(*exitFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory of the exit file is still there: %v", err)
	}
}

// hiddenPowerShell makes startConsoleWindow start the real PowerShell with the real script but with no window: CREATE_NO_WINDOW in place of
// CREATE_NEW_CONSOLE, the Enter of the window read from stdin, and what PowerShell and the engine print written to out. No test
// opens a window. It returns where the starter said that the exit code goes.
func hiddenPowerShell(t *testing.T, stdin, out *os.File) (exitFile *string) {
	t.Helper()
	exitFile = new(string)
	original := startProcess
	startProcess = func(name string, argv []string, attr *syscall.ProcAttr) (int, uintptr, error) {
		if filepath.Base(name) != "powershell.exe" || !strings.Contains(attr.Sys.CmdLine, "Start-Process") {
			t.Errorf("the window does not start the PowerShell script: %s %q", name, attr.Sys.CmdLine)
		}
		for _, entry := range attr.Env {
			if file, ok := strings.CutPrefix(entry, envSetupExit+"="); ok {
				*exitFile = file
			}
		}
		sys := *attr.Sys
		sys.CreationFlags = windows.CREATE_NO_WINDOW
		hidden := *attr
		hidden.Files, hidden.Sys = []uintptr{stdin.Fd(), out.Fd(), out.Fd()}, &sys
		return original(name, argv, &hidden)
	}
	t.Cleanup(func() { startProcess = original })
	return exitFile
}

// testBinary is the path of the running test binary, which is the engine of the windows that the tests start.
func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// runHiddenWindow runs the real window script with no window, with exe (the test binary, or a copy of it) as the engine (the helper
// "record", which prints its arguments and ends with code). It returns what the script had left in the exit file before it waited for
// Enter, the arguments the engine printed, and how the wait ended once the Enter was pressed.
func runHiddenWindow(t *testing.T, exe string, args []string, code string) (left string, got []string, waitErr error) {
	t.Helper()
	t.Setenv("SUITEWARD_TEST_HELPER", "record")
	t.Setenv("SUITEWARD_TEST_EXIT", code)
	enter, typed, err := os.Pipe() // the Enter that closes the window, which the test presses once it has seen the exit file
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = enter.Close(); _ = typed.Close() })
	out, err := os.Create(filepath.Clean(filepath.Join(t.TempDir(), "powershell.txt")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	exitFile := hiddenPowerShell(t, enter, out)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	wait, err := startConsoleWindow(ctx, exe, args)
	if err != nil {
		t.Fatal(err)
	}
	// The window ends whatever the test does next: a failure still presses the Enter and waits, which removes the directory of the exit file.
	// It is registered last, so it runs first.
	ended := false
	finish := func() error {
		if !ended {
			ended = true
			_, _ = typed.WriteString("\r\n") // a window that has ended already needs no Enter
			waitErr = wait()
		}
		return waitErr
	}
	t.Cleanup(func() { _ = finish() })
	for strings.TrimSpace(left) == "" { // the exit file is there before the Enter, and whole once it has text
		data, err := os.ReadFile(filepath.Clean(*exitFile))
		// Not there yet, or held by Set-Content while it writes it.
		if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatal(err)
		}
		if left = string(data); ctx.Err() != nil {
			shown, _ := os.ReadFile(out.Name())
			t.Fatalf("the script left no exit code: %v\n%s", ctx.Err(), shown)
		}
		time.Sleep(10 * time.Millisecond)
	}
	printed, err := os.ReadFile(out.Name()) // the engine has ended: it printed its arguments before the script left its code
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(printed), "\n") {
		if arg, ok := strings.CutPrefix(line, "arg="); ok {
			got = append(got, arg)
		}
	}
	return strings.TrimSpace(left), got, finish()
}

// The real script, in the real PowerShell and with no window: the engine gets every argument as it was, text that PowerShell would read as
// code included, and the code it ends with is left for the installer before the window waits for Enter, then read from there.
func TestWindowScriptRunsTheEngineWithEveryArgumentIntactAndLeavesItsExitCode(t *testing.T) {
	args := []string{"--", "setup", "--owner", "alice", "--test-check", "acme/app=CI / test",
		"--test-check", `acme/lib=CI / test (ubuntu, "go 1.27") $HOME & 'x' ; ` + "`whoami`", "--dir", `C:\Program Files\SuiteWard\`, "--empty", ""}
	for code, wantErr := range map[string]string{"0": "", "3": "exit code 3"} {
		t.Run("exit code "+code, func(t *testing.T) {
			left, got, err := runHiddenWindow(t, testBinary(t), args, code)
			if left != code {
				t.Errorf("the script left the exit code %q, want %s", left, code)
			}
			if (err == nil) != (wantErr == "") || (err != nil && !strings.Contains(err.Error(), wantErr)) {
				t.Errorf("wait = %v, want %q", err, wantErr)
			}
			if !slices.Equal(got, args[1:]) {
				t.Errorf("the engine got %q, want %q", got, args[1:])
			}
		})
	}
}

// A space in a path is data too: the engine in a folder with a space in its name (C:\Program Files) and a temp folder that holds one (a user
// name with a space puts it in %TEMP%) reach the script intact. The script starts the engine, leaves its code in the folder the installer
// made, and the folder goes with the window.
func TestWindowScriptWorksWithSpacesInTheProgramPathAndTheTempFolder(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "program files", "suite ward engine.exe")
	if err := os.MkdirAll(filepath.Dir(engine), 0o750); err != nil {
		t.Fatal(err)
	}
	linkOrCopyTestBinary(t, engine)
	spaced := filepath.Join(t.TempDir(), "temp folder")
	if err := os.Mkdir(spaced, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", spaced) // os.TempDir reads TMP, then TEMP
	t.Setenv("TEMP", spaced)
	if !strings.Contains(os.TempDir(), " ") || !strings.Contains(engine, " ") {
		t.Fatalf("the test needs a space in both paths: temp %q, engine %q", os.TempDir(), engine)
	}
	args := []string{"--", "setup", "--owner", "alice"}
	left, got, err := runHiddenWindow(t, engine, args, "0")
	if err != nil || left != "0" {
		t.Errorf("the window ended with %v after leaving the exit code %q, want a success that left 0", err, left)
	}
	if !slices.Equal(got, args[1:]) {
		t.Errorf("the engine got %q, want %q", got, args[1:])
	}
	if left, err := filepath.Glob(filepath.Join(spaced, "suiteward-setup-*")); err != nil || len(left) != 0 {
		t.Errorf("the folder of the exit file is still in the temp folder: %v, %v", left, err)
	}
}

// ---- the folders of windows that were never cleaned up ----

// setupDir makes a folder as the starter makes one, in tmp, with the exit file of a setup that ended (or not). Everything in it is age old,
// the folder last: a file made in it changes its time.
func setupDir(t *testing.T, tmp, name string, withExit bool, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(tmp, name)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(dir, "other")}
	if withExit {
		files = append(files, filepath.Join(dir, "exit"))
	}
	old := time.Now().Add(-age)
	for _, file := range files {
		if err := os.WriteFile(file, []byte("0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Only a folder that holds its exit file and has not been touched for more than a day goes: the setup of another enroll or install is in
// flight in a folder that is new or that has no exit file yet, and what is not a folder of the starter's is not the starter's to remove.
func TestSweepRemovesOnlyOldSetupFoldersThatHoldTheirExitFile(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name     string
		folder   string
		withExit bool
		age      time.Duration
		stays    bool
	}{
		{"old with its exit file", "suiteward-setup-1", true, 25 * time.Hour, false},
		{"new with its exit file", "suiteward-setup-2", true, time.Hour, true},
		{"just under a day, with its exit file", "suiteward-setup-3", true, 23*time.Hour + 30*time.Minute, true},
		{"old with no exit file", "suiteward-setup-4", false, 25 * time.Hour, true},
		{"old with its exit file, another name", "suiteward-other-5", true, 25 * time.Hour, true},
		{"old with its exit file, a longer name", "x-suiteward-setup-6", true, 25 * time.Hour, true},
	}
	for _, tc := range cases {
		setupDir(t, tmp, tc.folder, tc.withExit, tc.age)
	}
	file := filepath.Join(tmp, "suiteward-setup-file") // a file with the name of a setup folder is not one
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	if err := sweepSetupDirs(tmp, time.Now()); err != nil {
		t.Fatalf("sweepSetupDirs = %v", err)
	}
	for _, tc := range cases {
		if got := exists(filepath.Join(tmp, tc.folder)); got != tc.stays {
			t.Errorf("%s: the folder exists = %v after the sweep, want %v", tc.name, got, tc.stays)
		}
	}
	if !exists(file) {
		t.Error("a file that is named like a setup folder was removed")
	}
}

// A folder that cannot be removed (Windows does not delete a file that is open) is reported and does not stop the others.
func TestSweepReportsTheFolderItCouldNotRemoveAndRemovesTheRest(t *testing.T) {
	tmp := t.TempDir()
	locked := setupDir(t, tmp, "suiteward-setup-1", true, 25*time.Hour)
	other := setupDir(t, tmp, "suiteward-setup-2", true, 25*time.Hour)
	held, err := os.Open(filepath.Clean(filepath.Join(locked, "exit")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	err = sweepSetupDirs(tmp, time.Now())
	if err == nil || !strings.Contains(err.Error(), locked) {
		t.Errorf("sweepSetupDirs = %v, want the error that names %s", err, locked)
	}
	if exists(other) {
		t.Errorf("%s stays because another folder could not be removed", other)
	}
}

// The starter sweeps before it makes its own folder, and a sweep that fails does not fail the setup: an engine that ended well is a
// success whatever an old folder holds.
func TestTheWindowSweepsOldSetupFoldersAndAFailedSweepDoesNotFailSetup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp) // os.TempDir reads TMP, then TEMP
	t.Setenv("TEMP", tmp)
	removed := setupDir(t, tmp, "suiteward-setup-1", true, 25*time.Hour)
	locked := setupDir(t, tmp, "suiteward-setup-2", true, 25*time.Hour)
	held, err := os.Open(filepath.Clean(filepath.Join(locked, "exit")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	exe, _ := swapPowerShell(t, "window-left")
	wait, err := startConsoleWindow(context.Background(), exe, []string{"--", "setup"})
	if err != nil {
		t.Fatalf("startConsoleWindow = %v, want a window that opens although an old folder could not be removed", err)
	}
	if err := wait(); err != nil {
		t.Errorf("wait = %v, want success", err)
	}
	if exists(removed) {
		t.Errorf("%s, a setup folder of two days ago, is still there", removed)
	}
	if !exists(locked) {
		t.Errorf("%s is gone although a file of it was open", locked)
	}
}
