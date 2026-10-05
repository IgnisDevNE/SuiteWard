//go:build linux

package filesystem

import (
	"errors"
	"os"
)

// syncDirectory makes a new directory entry durable across a crash.
func syncDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
