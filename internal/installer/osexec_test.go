package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	case "fail":
		fmt.Fprint(os.Stderr, "something went wrong")
		os.Exit(3)
	}
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

func TestOSExecFailureCarriesStderrAndNoStdout(t *testing.T) {
	name, args := helperCommand(t, "fail")
	out, err := OSExec(context.Background(), name, args, nil)
	if err == nil || !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("got %v", err)
	}
	if out != nil {
		t.Fatalf("a failed command returned output: %q", out)
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
