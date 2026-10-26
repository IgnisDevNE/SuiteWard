//go:build darwin

package ui

import "golang.org/x/sys/unix"

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
	// ioctlSetTermiosFlush sets the mode and drops the input that nobody has read (tcsetattr with TCSAFLUSH).
	ioctlSetTermiosFlush = unix.TIOCSETAF
)
