//go:build linux || darwin

package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// keyPollEvery is how long a wait for a key waits on the terminal before it looks at its context again.
const keyPollEvery = 100 * time.Millisecond

// inputReadiness is the wait for a key of the terminal behind f that Options.InputReady is: it selects on the terminal and does not read it,
// so the end of its context leaves nothing pending. select(2) is what works on a terminal of macOS, where poll(2) is documented not to work on
// devices. It is nil where the terminal cannot be waited on (a descriptor select cannot hold, an error at the first try): the read then blocks
// as it always did.
func inputReadiness(f *os.File) func(context.Context) error {
	fd := int(f.Fd())
	if fd < 0 || fd >= unix.FD_SETSIZE {
		return nil
	}
	readable := func(wait time.Duration) (bool, error) {
		var set unix.FdSet
		set.Set(fd)
		timeout := unix.NsecToTimeval(wait.Nanoseconds())
		n, err := unix.Select(fd+1, &set, nil, nil, &timeout)
		return n > 0, err
	}
	if _, err := readable(0); err != nil {
		return nil
	}
	return func(ctx context.Context) error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			ready, err := readable(keyPollEvery)
			switch {
			case errors.Is(err, unix.EINTR):
			case err != nil:
				return fmt.Errorf("wait for a key: %w", err)
			case ready: // a key, or the end of the input, which the read then reports
				return nil
			}
		}
	}
}
