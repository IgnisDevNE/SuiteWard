//go:build darwin

package ui

import "golang.org/x/sys/unix"

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
	// ioctlSetTermiosFlush sets the mode and drops the input that nobody has read (tcsetattr with TCSAFLUSH).
	ioctlSetTermiosFlush = unix.TIOCSETAF
)

// flushFd drops the input that nobody has read on the terminal fd, and leaves its mode alone. x/sys has TIOCFLUSH here, but not the FREAD
// that it takes, so this is the mode change that flushes (tcsetattr with TCSAFLUSH), with the mode it already has.
func flushFd(fd int) error {
	mode, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return err
	}
	return unix.IoctlSetTermios(fd, ioctlSetTermiosFlush, mode)
}
