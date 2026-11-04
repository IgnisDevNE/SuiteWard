//go:build !windows

package sandbox

import "os/exec"

// NoWindow does nothing off Windows, where no console window opens for the container CLI.
func NoWindow(*exec.Cmd) {}
