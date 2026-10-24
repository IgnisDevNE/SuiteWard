package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

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
	case "record": // the arguments after "--", one per line, into the file named by SUITEWARD_TEST_RECORD; the exit code is SUITEWARD_TEST_EXIT
		args := os.Args
		for i, a := range args {
			if a == "--" {
				args = args[i+1:]
				break
			}
		}
		if err := os.WriteFile(os.Getenv("SUITEWARD_TEST_RECORD"), []byte(strings.Join(args, "\n")+"\n"), 0o600); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(99)
		}
		code, _ := strconv.Atoi(os.Getenv("SUITEWARD_TEST_EXIT"))
		os.Exit(code)
	}
	// No test opens a window of its own: the window is for the real machine, where it would start a real terminal.
	setupMachine = machine{goos: "linux", getenv: func(string) string { return "" }}
	os.Exit(m.Run())
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
