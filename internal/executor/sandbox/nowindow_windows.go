//go:build windows

package sandbox

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// NoWindow starts the console program of cmd without a console window. A console program that a process with no console starts gets a console
// of its own, shown on the screen, and the executor has none once its task was stopped (its console host is gone) while it still runs the
// container CLI to stop the sandbox of a job in flight: with CREATE_NO_WINDOW that program starts and shows nothing.
func NoWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
