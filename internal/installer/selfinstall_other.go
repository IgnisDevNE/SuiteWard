//go:build !windows

package installer

// osSelfInstall is nil: the program is put on the PATH by install.sh (~/.local/bin) and by the package of the system, not by the installer.
func osSelfInstall() *SelfInstall { return nil }
