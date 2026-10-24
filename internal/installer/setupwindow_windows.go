//go:build windows

package installer

const windowScript = ""

func windowEnv(environ []string, name string, args []string) []string { return nil }
