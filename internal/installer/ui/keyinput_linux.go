//go:build linux

package ui

import "golang.org/x/sys/unix"

const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
	// ioctlSetTermiosFlush sets the mode and drops the input that nobody has read (tcsetattr with TCSAFLUSH).
	ioctlSetTermiosFlush = unix.TCSETSF
)

// flushFd drops the input that nobody has read on the terminal fd, and leaves its mode alone (tcflush with TCIFLUSH).
func flushFd(fd int) error { return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }
