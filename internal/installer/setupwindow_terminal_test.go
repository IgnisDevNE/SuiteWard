package installer

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func withFastPolls(t *testing.T, poll, limit time.Duration) {
	t.Helper()
	oldPoll, oldLimit := setupPoll, setupWaitLimit
	setupPoll, setupWaitLimit = poll, limit
	t.Cleanup(func() { setupPoll, setupWaitLimit = oldPoll, oldLimit })
}

var tricky = []string{"--connection", "pm", "exec", "-it", "suiteward", "/suiteward", "setup", "--owner", "alice",
	"--test-check", "acme/app=CI / test", "--test-check", `acme/lib=it's "$(rm -rf x)" ; ` + "`id` & done"}

func TestTerminalScriptRemovesItselfFirstAndHoldsOnlyTheCommand(t *testing.T) {
	script := terminalScript("/opt/podman/bin/podman", tricky, []string{"PATH=/usr/bin"}, "/tmp/sw-123")
	if !strings.HasPrefix(script, "#!/bin/sh\nrm -f -- \"$0\"\n") {
		t.Errorf("the script does not remove itself first:\n%s", script)
	}
	for _, want := range []string{
		`'/opt/podman/bin/podman' '--connection' 'pm' 'exec' '-it' 'suiteward' '/suiteward' 'setup' '--owner' 'alice'`,
		`'acme/app=CI / test'`,
		`'acme/lib=it'\''s "$(rm -rf x)" ; ` + "`id` & done'",
		"Press Enter to close this window",
		"/tmp/sw-123",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script lacks %q:\n%s", want, script)
		}
	}
	for _, leaked := range []string{"otpauth", "TOTP", "base32"} {
		if strings.Contains(script, leaked) {
			t.Errorf("the script mentions %q: it holds the command line and nothing the command prints", leaked)
		}
	}
}

// The screen and its scrollback are cleared on every way out of the script once the command has ended and its secret may be on the screen:
// the Enter, and Ctrl-C, a terminated shell or a hung-up terminal at the prompt, which would end sh before a clear that follows the prompt.
func TestTerminalScriptClearsTheScreenOnEveryWayOutOfThePrompt(t *testing.T) {
	script := terminalScript("podman", []string{"exec", "-it", "suiteward"}, nil, "/tmp/sw")
	ran, onExit, onSignal, prompt, leave := strings.Index(script, "code=$?\n"), strings.Index(script, "trap 'clear; printf \"\\033[3J\"' EXIT\n"),
		strings.Index(script, "trap 'exit \"$code\"' INT TERM HUP\n"), strings.Index(script, "read -r"), strings.LastIndex(script, "exit \"$code\"\n")
	if ran < 0 || onExit < ran || onSignal < ran || prompt < onExit || prompt < onSignal || leave < prompt {
		t.Errorf("the traps must be set after the command ran (%d), before the prompt (%d): the clear on EXIT at %d, the exit on INT TERM HUP at %d, the last exit at %d:\n%s",
			ran, prompt, onExit, onSignal, leave, script)
	}
	if n := strings.Count(script, "clear"); n != 1 {
		t.Errorf("the script clears %d times; the clear on EXIT is the only one, which runs whatever way the script ends:\n%s", n, script)
	}
}

// The terminal must run the command in the environment OSInteractive gives it, which childEnv decides.
func TestTerminalScriptGivesTheCommandTheEnvironmentOfOSInteractive(t *testing.T) {
	environ := []string{"PATH=/usr/bin:/bin", "CONTAINER_HOST=ssh://elsewhere", "HOME=/home/me"}
	bin := filepath.Join(os.TempDir(), "engine", "podman") // absolute on every system, so that childEnv puts its directory first
	var path string
	for _, entry := range childEnv(environ, bin, tricky) {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	if !strings.HasPrefix(path, filepath.Dir(bin)) {
		t.Fatalf("childEnv does not put the directory of the command first: %q", path)
	}
	script := terminalScript(bin, tricky, environ, "/tmp/sw")
	for _, want := range []string{"unset CONTAINER_HOST\n", "export PATH='" + path + "'\n"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "HOME") {
		t.Errorf("the script sets what the command already has:\n%s", script)
	}
	// With no connection flag, no directory to put first and no engine selection, there is nothing to change.
	plain := terminalScript("podman", []string{"exec", "-it", "suiteward"}, []string{"PATH=/usr/bin:/bin", "HOME=/home/me"}, "/tmp/sw")
	if strings.Contains(plain, "unset") || strings.Contains(plain, "export") {
		t.Errorf("the script changes the environment of a command that needs no change:\n%s", plain)
	}
}

// The login shell of Terminal has an environment of its own, so what selects the engine in the installer's environment is exported
// whether or not OSInteractive changes it, except what childEnv drops.
func TestTerminalScriptExportsWhatSelectsTheEngineInTheEnvironmentOfTheInstaller(t *testing.T) {
	environ := []string{"PATH=/usr/bin:/bin", "HOME=/home/me", "DOCKER_HOST=unix:///run/user/1/docker.sock", "DOCKER_CONTEXT=desktop-linux",
		"CONTAINER_CONNECTION=pm", "CONTAINER_HOST=ssh://elsewhere", "DOCKER_CERT_PATH=/home/me/it's", "DOCKER_TLS_VERIFY=1"}
	script := terminalScript("podman", []string{"exec", "-it", "suiteward"}, environ, "/tmp/sw")
	for _, want := range []string{"export DOCKER_HOST='unix:///run/user/1/docker.sock'\n", "export DOCKER_CONTEXT='desktop-linux'\n", "export CONTAINER_CONNECTION='pm'\n",
		"export CONTAINER_HOST='ssh://elsewhere'\n", `export DOCKER_CERT_PATH='/home/me/it'\''s'` + "\n", "export DOCKER_TLS_VERIFY='1'\n"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "HOME") {
		t.Errorf("the script exports what does not select the engine:\n%s", script)
	}
	// --connection makes childEnv drop CONTAINER_HOST: the terminal must not have it either, whatever its own shell sets.
	withConnection := terminalScript("podman", []string{"--connection", "pm", "exec", "-it", "suiteward"}, environ, "/tmp/sw")
	if !strings.Contains(withConnection, "unset CONTAINER_HOST\n") || strings.Contains(withConnection, "export CONTAINER_HOST") {
		t.Errorf("CONTAINER_HOST must be unset, not exported, with --connection:\n%s", withConnection)
	}
}

// The script is run by a real shell, with the test binary standing in for the engine, and every argument arrives as it was.
func TestTerminalScriptRunsTheCommandWithEveryArgumentIntactAndLeavesItsExitCode(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell here")
	}
	dir := t.TempDir()
	linkOrCopyTestBinary(t, filepath.Join(dir, "engine"+exeSuffix()))
	// A relative command, as the shell finds it from the directory it runs in: no PATH of this machine is involved.
	script := filepath.Join(dir, "setup.command")
	if err := os.WriteFile(script, []byte(terminalScript("./engine", append([]string{"--"}, tricky...), nil, filepath.ToSlash(dir))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUITEWARD_TEST_HELPER", "record")
	t.Setenv("SUITEWARD_TEST_EXIT", "3")
	t.Setenv("MSYS_NO_PATHCONV", "1") // Git's shell on Windows would turn /suiteward into a path of its own
	cmd := commandContext(context.Background(), sh, "setup.command")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("\n") // the Enter that closes the window
	// The script ends with the exit code of the command, which is 3 here.
	out, err := cmd.Output()
	if exitCodeOf(err) != 3 {
		t.Fatalf("the script ended with %v, want exit code 3\n%s", err, out)
	}
	var got []string
	for _, line := range strings.Split(string(out), "\n") {
		if arg, ok := strings.CutPrefix(line, "arg="); ok {
			got = append(got, arg)
		}
	}
	if !slices.Equal(got, tricky) {
		t.Errorf("the command got %q, want %q", got, tricky)
	}
	if !strings.Contains(string(out), "\x1b[3J") {
		t.Errorf("the script did not clear the scrollback on its way out:\n%q", out)
	}
	if code, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "exit"))); err != nil || strings.TrimSpace(string(code)) != "3" {
		t.Errorf("the exit code the script left is %q, %v; want 3", code, err)
	}
	if _, err := os.Stat(script); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the script is still there: %v", err)
	}
}

// Ctrl-C at the prompt of the window is an interrupt of the shell: it must still clear the screen, which holds the secret, and end with the
// code of setup. The shell is bash in the mode macOS runs it as sh: dash does not leave a blocked read for a trap.
func TestTerminalScriptClearsTheScreenWhenInterruptedAtItsPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go cannot send an interrupt to a process on Windows")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash here")
	}
	dir := t.TempDir()
	linkOrCopyTestBinary(t, filepath.Join(dir, "engine"+exeSuffix()))
	script := filepath.Join(dir, "setup.command")
	if err := os.WriteFile(script, []byte(terminalScript("./engine", []string{"--", "setup"}, nil, dir)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUITEWARD_TEST_HELPER", "record")
	t.Setenv("SUITEWARD_TEST_EXIT", "3")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := commandContext(ctx, bash, "--posix", "setup.command")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe() // never written to: the prompt waits for an Enter that does not come
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	buf := make([]byte, 256)
	for !strings.Contains(seen.String(), "Press Enter to close this window") {
		n, err := stdout.Read(buf)
		seen.Write(buf[:n])
		if err != nil {
			t.Fatalf("the script ended before its prompt: %v\n%q", err, seen.String())
		}
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(stdout)
	if err := cmd.Wait(); exitCodeOf(err) != 3 {
		t.Errorf("the interrupted script ended with %v, want the exit code of setup, 3", err)
	}
	if !strings.Contains(string(rest), "\x1b[3J") {
		t.Errorf("the interrupted script did not clear the scrollback:\n%q", rest)
	}
}

// exitCodeOf is the exit code a command ended with: 0 for no error, -1 for an error that is not an exit.
func exitCodeOf(err error) int {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	}
	return -1
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// linkOrCopyTestBinary makes path a file that runs the test binary, which answers to SUITEWARD_TEST_HELPER (TestMain).
func linkOrCopyTestBinary(t *testing.T, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if os.Link(exe, path) == nil {
		return
	}
	src, err := os.Open(filepath.Clean(exe))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Chmod(0o700); err != nil { // the copy has to run
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

// openSimulating is the open of a test: Terminal runs the script, which here only leaves the exit code it would have left.
func openSimulating(code string, delay time.Duration, opened *string) func(context.Context, string) error {
	return func(_ context.Context, script string) error {
		*opened = script
		go func() {
			time.Sleep(delay)
			exit := filepath.Join(filepath.Dir(script), "exit")
			// Whole or not there, as the script leaves it. A link and not a rename: a poll that opens the file as it is renamed in fails
			// with a sharing violation on Windows.
			_ = os.WriteFile(exit+".tmp", []byte(code+"\n"), 0o600)
			_ = os.Link(exit+".tmp", exit)
		}()
		return nil
	}
}

func TestTerminalWindowWaitsForTheExitCodeTheScriptLeaves(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	withFastPolls(t, time.Millisecond, 5*time.Second)
	for name, tc := range map[string]struct {
		code string
		fail bool
	}{"a clean end": {"0", false}, "an error of setup": {"1", true}} {
		t.Run(name, func(t *testing.T) {
			var script string
			start := terminalWindow(openSimulating(tc.code, 20*time.Millisecond, &script))
			wait, err := start(context.Background(), exe, []string{"--", "setup"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(script); err != nil {
				t.Errorf("the script was not written where open was told: %v", err)
			}
			err = wait()
			if (err != nil) != tc.fail || (tc.fail && !strings.Contains(err.Error(), "exit code "+tc.code)) {
				t.Errorf("wait = %v, want failure %v naming exit code %s", err, tc.fail, tc.code)
			}
			if _, err := os.Stat(filepath.Dir(script)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the directory of the script and its exit code is still there: %v", err)
			}
		})
	}
}

func TestTerminalWindowGivesUpAfterTheLimitAndOnCtrlC(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	withFastPolls(t, time.Millisecond, 50*time.Millisecond)
	var script string
	never := func(_ context.Context, s string) error { script = s; return nil } // the owner closes Terminal, or never answers
	wait, err := terminalWindow(never)(context.Background(), exe, []string{"--", "setup"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wait(); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("wait = %v, want the report that setup did not finish in time", err)
	}
	if _, err := os.Stat(filepath.Dir(script)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory is still there after the limit: %v", err)
	}

	withFastPolls(t, time.Millisecond, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	wait, err = terminalWindow(never)(ctx, exe, []string{"--", "setup"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("wait = %v, want the interrupt", err)
	}
	if _, err := os.Stat(filepath.Dir(script)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory is still there after Ctrl-C: %v", err)
	}
}

func TestTerminalWindowThatFailsToOpenLeavesNothingBehind(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var script string
	boom := errors.New("open: no such application")
	start := terminalWindow(func(_ context.Context, s string) error { script = s; return boom })
	if wait, err := start(context.Background(), exe, []string{"--", "setup"}); !errors.Is(err, boom) || wait != nil {
		t.Fatalf("start = %v (a wait: %t); want the failure of open and no wait", err, wait != nil)
	}
	if _, err := os.Stat(filepath.Dir(script)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory is still there after a failed open: %v", err)
	}
	if wait, err := start(context.Background(), "no-such-engine-binary", nil); err == nil || wait != nil {
		t.Errorf("start with no engine on the PATH = %v (a wait: %t); want an error and no window", err, wait != nil)
	}
}
