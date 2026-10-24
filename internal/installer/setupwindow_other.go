//go:build !windows && !darwin

package installer

// platformSetupWindow is nil: Linux has no standard terminal to open, so setup keeps the terminal of the installer.
func platformSetupWindow() windowStarter { return nil }
