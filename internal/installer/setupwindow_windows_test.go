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
	if err := wait(); err == nil || !strings.Contains(err.Error(), "closed before setup ended") {
		t.Errorf("wait = %v, want the report that the window was closed before setup ended", err)
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
