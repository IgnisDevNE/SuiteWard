package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// windowStarter opens a terminal window of its own that runs the command (setup, for the owner) and returns a function that waits for
// the command to end and reports how it ended. A start error means that no window was opened. The wait gives up when ctx is done and
// returns its error, and takes the window with it where the system lets it.
type windowStarter func(ctx context.Context, name string, args []string) (wait func() error, err error)

// machine is what runSetup asks of the machine it runs on to choose where setup runs; tests replace setupMachine.
type machine struct {
	goos   string
	getenv func(string) string
	window windowStarter // nil where this system has no window to open
}

var setupMachine = machine{goos: runtime.GOOS, getenv: os.Getenv, window: platformSetupWindow()}

// setupWindow is the window setup runs in, or nil when it keeps the terminal. Setup leaves the full screen for a window of its own on
// Windows and macOS, where a desktop opens one; in plain mode nothing moves, and an SSH session has no desktop to open it on.
func (m machine) setupWindow(fullScreen bool) windowStarter {
	if !fullScreen || m.window == nil || m.getenv("SSH_CONNECTION") != "" || m.getenv("SSH_TTY") != "" {
		return nil
	}
	if m.goos != "windows" && m.goos != "darwin" {
		return nil // Linux has no standard terminal to open, and the isolated tier runs only on Linux (isolated.go)
	}
	return m.window
}

// setupPoll and setupWaitLimit bound the wait for a window that the installer cannot wait on (Terminal): it polls for the exit code the
// script leaves, and gives up after the limit. Variables so that a test does not wait for them.
var (
	setupPoll      = 500 * time.Millisecond
	setupWaitLimit = 15 * time.Minute
)

// terminalWindow is the window starter of a system that opens a script in a terminal and cannot wait for it: open runs the script, and
// the script leaves the exit code of the command in a file that the wait polls for. The directory of the script and the file is private
// to the user, and goes when the wait ends or the window does not open.
func terminalWindow(open func(ctx context.Context, script string) error) windowStarter {
	return func(ctx context.Context, name string, args []string) (func() error, error) {
		bin, err := exec.LookPath(name) // the PATH of the terminal's own shell is not the installer's
		if err != nil {
			return nil, fmt.Errorf("find %s: %w", name, err)
		}
		dir, err := os.MkdirTemp("", "suiteward-setup-")
		if err != nil {
			return nil, fmt.Errorf("make a directory for the setup script: %w", err)
		}
		script := filepath.Join(dir, "setup.command")
		if err := writeScript(script, terminalScript(bin, args, os.Environ(), dir)); err != nil {
			_ = os.RemoveAll(dir) // a directory that stays is only a script with no secret in it
			return nil, err
		}
		if err := open(ctx, script); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("open the setup script in a terminal: %w", err)
		}
		return func() error {
			defer func() { _ = os.RemoveAll(dir) }()
			return waitForExitCode(ctx, filepath.Join(dir, "exit"))
		}, nil
	}
}

// writeScript writes a script that only its owner can read and run: Terminal runs a .command file only with the execute bit.
func writeScript(path, text string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write the setup script: %w", err)
	}
	if err := f.Chmod(0o700); err != nil {
		_ = f.Close() // the Chmod error is the one to report
		return fmt.Errorf("make the setup script executable: %w", err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close() // the write error is the one to report
		return fmt.Errorf("write the setup script: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write the setup script: %w", err)
	}
	return nil
}

// waitForExitCode polls for the file the script leaves with the exit code of setup, and reports how setup ended.
func waitForExitCode(ctx context.Context, file string) error {
	limit, tick := time.NewTimer(setupWaitLimit), time.NewTicker(setupPoll)
	defer limit.Stop()
	defer tick.Stop()
	for {
		data, err := os.ReadFile(filepath.Clean(file))
		switch {
		case err == nil:
			return exitCodeError(strings.TrimSpace(string(data)))
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("read the exit code of setup: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-limit.C:
			return fmt.Errorf("setup did not end within %s: finish it in its window, or run suiteward enroll again", setupWaitLimit)
		case <-tick.C:
		}
	}
}

// exitCodeError is the error of a setup that ended with code, the text of an exit code: none for 0.
func exitCodeError(code string) error {
	n, err := strconv.Atoi(code)
	if err != nil {
		return fmt.Errorf("the exit code of setup is %q, which is not a number", code)
	}
	if n != 0 {
		return fmt.Errorf("the setup window ended with exit code %d", n)
	}
	return nil
}

// windowOutcome says how setup ended in a window that the installer can wait on: by the exit code that the window left in exitFile before it
// waited for Enter. Once setup has ended, a window that the user closes (the X, Ctrl-C at its prompt) or that Ctrl-C at the installer
// closes ends with a code of its own, and that is not how setup ended. Only when no code was left is how the window ended (fallback) the
// answer.
func windowOutcome(exitFile string, fallback error) error {
	data, err := os.ReadFile(filepath.Clean(exitFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fallback
	case err != nil:
		return fmt.Errorf("read the exit code of setup: %w", err)
	}
	return exitCodeError(strings.TrimSpace(string(data)))
}

// terminalScript is the script a terminal runs for setup: it removes itself, runs the command in the environment OSInteractive gives
// it, leaves the exit code in exitDir for the installer, waits for Enter so that the secret can be copied, and clears the screen and
// its scrollback before it ends, so that the secret does not stay in a window the profile keeps open. It holds the command line and
// nothing that the command prints.
func terminalScript(bin string, args, environ []string, exitDir string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nrm -f -- \"$0\"\n")
	b.WriteString(shellEnv(environ, childEnv(environ, bin, args)))
	b.WriteString(shellQuote(bin))
	for _, a := range args {
		b.WriteString(" " + shellQuote(a))
	}
	exit := shellQuote(filepath.ToSlash(exitDir) + "/exit")
	b.WriteString("\ncode=$?\n")
	b.WriteString("printf '%s\\n' \"$code\" > " + exit + ".tmp && mv " + exit + ".tmp " + exit + "\n") // whole or not there, for the poll
	b.WriteString("printf '\\nPress Enter to close this window. '\nread -r answer\nclear\nprintf '\\033[3J'\nexit \"$code\"\n")
	return b.String()
}

// shellQuote is s as one word of a POSIX shell: in single quotes, which only a single quote ends.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellEnv is the lines that turn the environment before into the environment after in a shell.
func shellEnv(before, after []string) string {
	was, is := envMap(before), envMap(after)
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(was)) {
		if _, kept := is[k]; !kept && envName.MatchString(k) {
			b.WriteString("unset " + k + "\n")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(is)) {
		if v, had := was[k]; (!had || v != is[k]) && envName.MatchString(k) {
			b.WriteString("export " + k + "=" + shellQuote(is[k]) + "\n")
		}
	}
	return b.String()
}
