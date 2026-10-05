//go:build !linux

package filesystem

import "os"

// syncDirectory is a no-op where directories cannot be opened and synced.
func syncDirectory(*os.Root) error { return nil }
