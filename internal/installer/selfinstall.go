package installer

import (
	"cmp"
	"os"
	"path/filepath"
)

// command is how a hint names this program: Deps.Command, or suiteward when the run did not work it out.
func (d Deps) command() string { return cmp.Or(d.Command, "suiteward") }

// commandName is the Deps.Command of the running program: executable is os.Executable and lookPath is exec.LookPath. A hint that says
// `suiteward doctor` is a command only where the shell finds this program by that name, as it does after the release is installed on the
// PATH. A release downloaded to a folder is not there, and it is run by its path: the path is quoted for a shell (quoteArg), and PowerShell,
// which reads a quoted string at the start of a line as text, runs it with the call operator.
func commandName(goos string, executable func() (string, error), lookPath func(string) (string, error)) string {
	exe, err := executable()
	if err != nil {
		return "suiteward" // there is no path to name: the bare name is the best that is known
	}
	if found, err := lookPath("suiteward"); err == nil && sameFile(found, exe) { // exec.ErrDot is an error: a shell does not run a file of the current folder by its name
		return "suiteward"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	name := quoteArg(exe)
	if goos == "windows" && name != exe {
		name = "& " + name
	}
	return name
}

// sameFile says that both paths are one file, whatever their spelling: a link, a short name or another case of a letter.
func sameFile(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}
