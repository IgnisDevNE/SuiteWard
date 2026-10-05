//go:build linux

package filesystem

import "os"

// syncDirectory is a stub until directory durability is implemented.
func syncDirectory(*os.Root) error { return nil }
