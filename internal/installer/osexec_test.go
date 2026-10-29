package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// TestMain lets the test binary stand in for the container CLI: a child started with SUITEWARD_TEST_HELPER set runs the
// helper instead of the tests.
func TestMain(m *testing.M) {
	switch os.Getenv("SUITEWARD_TEST_HELPER") {
	case "echo": // stdout: the arguments after "--", then stdin; stderr: a note
		args := os.Args
		for i, a := range args {
			if a == "--" {
				args = args[i+1:]
				break
			}
		}
		fmt.Print(strings.Join(args, " "), "|")
		buf := make([]byte, 64)
		n, _ := os.Stdin.Read(buf)
		fmt.Print(string(buf[:n]))
		fmt.Fprint(os.Stderr, "helper note")
		os.Exit(0)
	case "failout": // a long stdout that ends with the reason, then stderr, then a failure
		fmt.Print("FIRSTLINE ", strings.Repeat("log ", 3000), "migration 21 failed")
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(3)
	case "fail":
		fmt.Fprint(os.Stderr, "something went wrong")
		os.Exit(3)
	// The two stand-ins for runuser and the systemctl under it: the command starts a child that keeps its output open (as systemctl does when runuser
	// is killed) and then waits to be killed.
	case "orphan":
		child := exec.Command(os.Args[0], "--")
		child.Env = append(os.Environ(), "SUITEWARD_TEST_HELPER=holdpipe")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(99)
		}
		time.Sleep(time.Hour)
		os.Exit(1)
	case "holdpipe": // ends by itself, so that a run that fails to end the wait leaves nothing behind
		time.Sleep(20 * time.Second)
		os.Exit(0)
	case "record": // stdout: each argument after "--" on a line of its own, after "arg="; the exit code is SUITEWARD_TEST_EXIT
		args := os.Args
		for i, a := range args {
			if a == "--" {
				args = args[i+1:]
				break
			}
		}
		for _, a := range args {
			fmt.Println("arg=" + a)
		}
		code, _ := strconv.Atoi(os.Getenv("SUITEWARD_TEST_EXIT"))
		os.Exit(code)
	// The stand-ins for the PowerShell of a setup window (the Windows tests swap startProcess for them): no console, and no window. The
	// exit code that Windows gives a console program that its window took with it is 0xC000013A.
	case "window-left": // setup ended well and left its code in the exit file of the working directory, then the window was closed
		if err := os.WriteFile("exit", []byte("0\n"), 0o600); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(99)
		}
		os.Exit(-1073741510)
	case "window-closed": // the window was closed before setup left a code
		os.Exit(-1073741510)
	case "window-blocked": // setup is at its prompt and stays there
		time.Sleep(time.Hour)
		os.Exit(1)
	}
	// No test opens a window of its own: the window is for the real machine, where it would start a real terminal.
	setupMachine = machine{goos: "linux", getenv: func(string) string { return "" }}
	// An install writes the list of installations beside the default settings file, and a command that is given no flag reads the default one:
	// no test may reach the configuration folder of whoever runs it. A test that needs a folder of its own calls useDefaultConfigDir.
	configDir, err := os.MkdirTemp("", "suiteward-config-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "make the test configuration folder:", err)
		os.Exit(1)
	}
	for _, key := range []string{"APPDATA", "XDG_CONFIG_HOME", "HOME"} {
		if err := os.Setenv(key, configDir); err != nil {
			fmt.Fprintln(os.Stderr, "point the test configuration folder:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(configDir)
	os.Exit(code)
}

func helperCommand(t *testing.T, mode string) (name string, args []string) {
	t.Helper()
	t.Setenv("SUITEWARD_TEST_HELPER", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe, []string{"--"}
}

func TestOSExecReturnsStdoutAndFeedsStdin(t *testing.T) {
	name, args := helperCommand(t, "echo")
	out, err := OSExec(context.Background(), name, append(args, "one", "two"), strings.NewReader("from stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "one two|from stdin" {
		t.Fatalf("got %q", got)
	}
}

func TestOSExecFailureCarriesStderrAndReturnsStdout(t *testing.T) {
	name, args := helperCommand(t, "fail")
	_, err := OSExec(context.Background(), name, args, nil)
	if err == nil || !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("got %v", err)
	}
	// Whatever the failed command printed is returned with the error (doctor reads a JSON report from a failing check).
	name, args = helperCommand(t, "failout")
	out, err := OSExec(context.Background(), name, args, nil)
	if err == nil || !strings.HasPrefix(string(out), "FIRSTLINE ") || !strings.HasSuffix(string(out), "migration 21 failed") {
		t.Fatalf("a failed command did not return its whole stdout: %d bytes, %v", len(out), err)
	}
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("the exit code is not available through errors.As: %v", err)
	}
}

func TestOSExecDoesNotRepeatALongScriptInItsError(t *testing.T) {
	name, args := helperCommand(t, "fail")
	_, err := OSExec(context.Background(), name, append(args, strings.Repeat("x", 500)), nil)
	if err == nil || strings.Count(err.Error(), "x") > 200 {
		t.Fatalf("the error repeats the arguments in full: %v", err)
	}
}

func TestOSInteractive(t *testing.T) {
	name, args := helperCommand(t, "echo")
	if err := OSInteractive(context.Background(), name, args); err != nil {
		t.Fatal(err)
	}
	name, args = helperCommand(t, "fail")
	if err := OSInteractive(context.Background(), name, args); err == nil {
		t.Fatal("a failing command succeeded")
	}
}

func TestOSDepsRunRealCommands(t *testing.T) {
	d := OSDeps()
	if d.Exec == nil || d.Interactive == nil || d.Getenv == nil || d.GOOS == "" {
		t.Fatalf("incomplete: %+v", d)
	}
	if err := d.validate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUITEWARD_TEST_GETENV", "yes")
	if d.Getenv("SUITEWARD_TEST_GETENV") != "yes" {
		t.Fatal("Getenv does not read the process environment")
	}
}

func TestUnitParamsFromSettings(t *testing.T) {
	s := DefaultSettings()
	s.Tier, s.HTTPPort, s.MCPPort, s.JobTimeout = config.TierIsolated, 18081, 18091, "3m"
	p := UnitParamsFrom(s, testDigest)
	if p.Digest != testDigest || p.Tier != config.TierIsolated || p.HTTPPort != 18081 || p.MCPPort != 18091 || p.JobTimeout != "3m" || p.EnvFile != "" {
		t.Fatalf("got %+v", p)
	}
}

func TestOSExecErrorCarriesStderrOnly(t *testing.T) {
	name, args := helperCommand(t, "failout")
	_, err := OSExec(context.Background(), name, args, nil)
	// The stdout of a failed command is returned beside the error; the error says only what stderr said, so that a value
	// a command printed (a secret read, say) cannot leak through an error message.
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("the error lacks stderr: %v", err)
	}
	if strings.Contains(err.Error(), "migration 21 failed") || strings.Contains(err.Error(), "log log") {
		t.Fatalf("the error repeats the standard output: %v", err)
	}
}

// A cancelled command whose child keeps the output pipe open (runuser killed, systemctl under it still running) must not hold OSExec: without a bound
// on the wait for the pipes, cmd.Run waits for the child, and the step that cancelled it waits with it.
func TestOSExecEndsWhenTheContextIsCancelledAndAChildOfTheCommandKeepsItsOutputOpen(t *testing.T) {
	old := execWaitDelay
	execWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { execWaitDelay = old })
	name, args := helperCommand(t, "orphan")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := OSExec(ctx, name, args, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a command that was cancelled reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OSExec did not return after the context ended: a child of the command holds its output open and nothing bounds the wait")
	}
}
