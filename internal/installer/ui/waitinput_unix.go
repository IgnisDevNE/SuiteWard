//go:build linux || darwin

package ui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// keyPollEvery is how long a wait for a key polls the terminal before it looks at its context again.
const keyPollEvery = 100 // milliseconds

// inputReadiness is the wait for a key of the terminal behind f that Options.InputReady is: it polls the terminal and does not read it, so
// the end of its context leaves nothing pending. It is nil where polling this terminal does not work: the read then blocks as it always did.
func inputReadiness(f *os.File) func(context.Context) error {
	fd := int32(f.Fd())
	probe := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
	if _, err := unix.Poll(probe, 0); err != nil || probe[0].Revents&unix.POLLNVAL != 0 {
		return nil
	}
	return func(ctx context.Context) error {
		fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := unix.Poll(fds, keyPollEvery)
			switch {
			case errors.Is(err, unix.EINTR):
			case err != nil:
				return fmt.Errorf("wait for a key: %w", err)
			case n > 0: // a key, or the end of the input, which the read then reports
				return nil
			}
		}
	}
}
