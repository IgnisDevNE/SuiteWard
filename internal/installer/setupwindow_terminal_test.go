package installer

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	if clear, end := strings.Index(script, "clear"), strings.LastIndex(script, "exit"); clear < 0 || end < clear || !strings.Contains(script, `\033[3J`) {
		t.Errorf("the script does not clear the screen and its scrollback before it ends:\n%s", script)
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
	// With no connection flag and no directory to put first, there is nothing to change.
	plain := terminalScript("podman", []string{"exec", "-it", "suiteward"}, environ, "/tmp/sw")
	if strings.Contains(plain, "unset") || strings.Contains(plain, "export") {
		t.Errorf("the script changes the environment of a command that needs no change:\n%s", plain)
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
	t.Setenv("SUITEWARD_TEST_RECORD", filepath.ToSlash(filepath.Join(dir, "args")))
	t.Setenv("SUITEWARD_TEST_EXIT", "3")
	cmd := exec.Command(sh, "setup.command")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("\n") // the Enter that closes the window
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(tricky, "\n") + "\n"; string(got) != want {
		t.Errorf("the command got %q, want %q", got, want)
	}
	if code, err := os.ReadFile(filepath.Join(dir, "exit")); err != nil || strings.TrimSpace(string(code)) != "3" {
		t.Errorf("the exit code the script left is %q, %v; want 3", code, err)
	}
	if _, err := os.Stat(script); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the script is still there: %v", err)
	}
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
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o500) // a file made read-only can still be written through the handle that made it
	if err != nil {
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
			_ = os.WriteFile(filepath.Join(filepath.Dir(script), "exit"), []byte(code+"\n"), 0o600)
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
	if err := wait(); err == nil || !strings.Contains(err.Error(), "did not end") {
		t.Errorf("wait = %v, want the report that setup did not end in time", err)
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
