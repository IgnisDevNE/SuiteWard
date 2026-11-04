//go:build windows

package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The loop of run.cmd runs in the console host of its scheduled task: the executor is the child of cmd.exe, which is the child of conhost.exe. Another
// shape is a person's, and the watch leaves it alone (the host of a terminal that stays open never ends the run, and a closed one ends it as it does).
func TestTheConsoleHostIsFoundOnlyInTheChainOfTheTask(t *testing.T) {
	const executorImage = "suiteward-executor.exe"
	tests := map[string]struct {
		procs map[uint32]processEntry
		self  uint32
		host  uint32 // 0 when the executor has none
	}{
		"the task: conhost, cmd, the executor": {
			procs: map[uint32]processEntry{10: {1, "svchost.exe"}, 11: {10, "conhost.exe"}, 12: {11, "cmd.exe"}, 13: {12, executorImage}},
			self:  13, host: 11,
		},
		"the names in another letter case": {
			procs: map[uint32]processEntry{11: {10, "CONHOST.EXE"}, 12: {11, "CMD.EXE"}, 13: {12, executorImage}},
			self:  13, host: 11,
		},
		"a command prompt that a person opened": {
			procs: map[uint32]processEntry{20: {1, "explorer.exe"}, 21: {20, "cmd.exe"}, 22: {21, executorImage}},
			self:  22,
		},
		"PowerShell in Windows Terminal": {
			procs: map[uint32]processEntry{30: {1, "WindowsTerminal.exe"}, 31: {30, "pwsh.exe"}, 32: {31, executorImage}},
			self:  32,
		},
		"the executor started by another program": {
			procs: map[uint32]processEntry{11: {10, "conhost.exe"}, 13: {11, executorImage}},
			self:  13,
		},
		"a command prompt whose parent has ended": {
			procs: map[uint32]processEntry{21: {20, "cmd.exe"}, 22: {21, executorImage}},
			self:  22,
		},
		"a process that the snapshot does not list": {
			procs: map[uint32]processEntry{11: {10, "conhost.exe"}, 12: {11, "cmd.exe"}},
			self:  13,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			host, found := consoleHostOf(tc.self, tc.procs)
			if host != tc.host || found != (tc.host != 0) {
				t.Errorf("consoleHostOf = %d, %v; want %d, %v", host, found, tc.host, tc.host != 0)
			}
		})
	}
}

// helperDirEnv names the folder in which TestWatchHelperProcess leaves its marks. Without it that test is not a test: it returns at once.
const helperDirEnv = "SUITEWARD_TEST_WATCH_DIR"

// TestWatchHelperProcess is the executor of the test below: the process that the script of the task starts. It watches its console host as
// `suiteward execute` does, and writes a mark for each thing that happens, never to the console (which is gone when the mark matters).
func TestWatchHelperProcess(t *testing.T) {
	dir := os.Getenv(helperDirEnv)
	if dir == "" {
		return
	}
	mark := func(name, text string) { _ = os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600) }
	log, err := os.Create(filepath.Join(dir, "log"))
	if err != nil {
		mark("failed", err.Error())
		os.Exit(2)
	}
	ctx, stop := watchConsoleHost(context.Background(), newLogger(log, slog.LevelInfo))
	defer stop()
	mark("armed", fmt.Sprintf("%d %d", os.Getpid(), os.Getppid()))
	select {
	case <-ctx.Done():
		mark("ended", "")
	case <-time.After(30 * time.Second):
		mark("timeout", "")
	}
	os.Exit(0)
}

// What Stop-ScheduledTask did on the machine of the acceptance test (SW-25): only the console host of the task ended, and the script and the executor
// lived on. The chain is the task's: conhost.exe --headless cmd.exe /d /c <the program>, here the test binary running TestWatchHelperProcess. While the
// host runs, the executor goes on; when it is ended, the executor ends its run and says why, once, in plain words.
func TestTheExecutorEndsItsRunWhenTheConsoleHostOfItsLoopIsStopped(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(os.Getenv("SystemRoot"), "System32")
	host := exec.Command(filepath.Join(system, "conhost.exe"))
	host.SysProcAttr = &syscall.SysProcAttr{NoInheritHandles: true, CmdLine: fmt.Sprintf(`"%s\conhost.exe" --headless "%s\cmd.exe" /d /s /c ""%s" -test.run=TestWatchHelperProcess"`, system, system, os.Args[0])}
	host.Env = append(os.Environ(), helperDirEnv+"="+dir)
	if err := host.Start(); err != nil {
		t.Fatalf("cannot start the console host: %v", err)
	}
	var helper, script int
	t.Cleanup(func() {
		_ = host.Process.Kill()
		_ = host.Wait()
		if t.Failed() { // what a failed test may leave: the executor and its script, orphans of a console host that is gone
			for _, pid := range []int{helper, script} {
				if p, err := os.FindProcess(pid); pid != 0 && err == nil {
					_ = p.Kill()
				}
			}
		}
	})
	armed := waitForMark(t, dir, "armed")
	if _, err := fmt.Sscanf(armed, "%d %d", &helper, &script); err != nil {
		t.Fatalf("the mark %q of the executor does not hold its process ids: %v", armed, err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "ended")); err == nil {
		t.Fatal("the executor ended its run while its console host was running")
	}
	if err := host.Process.Kill(); err != nil { // as the task's stop does: no /T, so the script and the executor are left
		t.Fatalf("cannot end the console host: %v", err)
	}
	waitForMark(t, dir, "ended")
	logged, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(logged), "The test runner stopped because its task was stopped."); n != 1 {
		t.Errorf("the log says why the executor stopped %d times, want once:\n%s", n, logged)
	}
}

// waitForMark returns the text of the mark that the helper process leaves, waiting for it a bounded time.
func waitForMark(t *testing.T, dir, name string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if text, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return string(text)
		}
		time.Sleep(50 * time.Millisecond)
	}
	failed, _ := os.ReadFile(filepath.Join(dir, "failed"))
	t.Fatalf("the executor left no mark %q within 20 seconds (it failed with %q)", name, failed)
	return ""
}
