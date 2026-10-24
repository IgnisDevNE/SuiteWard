package installer

import (
	"cmp"
	"context"
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

// The copy of the program that the installer offers on Windows: %LOCALAPPDATA%\SuiteWard\bin\suiteward.exe, with that folder on the user's PATH.
const (
	selfFile    = "suiteward.exe"
	staleSuffix = ".old" // the copy that a running program held: renamed aside, removed by the next run that can
)

// SelfInstall is what the copy of the program and the change of the user's PATH read and change on this PC. OSEnv fills it on Windows,
// and nil means that this PC is not offered one: a test, or another system.
type SelfInstall struct {
	Executable func() (string, error)              // os.Executable
	LookPath   func(string) (string, error)        // exec.LookPath
	Rename     func(oldpath, newpath string) error // os.Rename; a test makes the copy that a running program holds refuse to be replaced
	Path       UserPath
}

// UserPath is the Path variable of the user's own environment, which a new terminal reads.
type UserPath interface {
	// Read is the raw value: expand says that it is of the expandable kind (REG_EXPAND_SZ) and found that there is one.
	Read() (value string, expand, found bool, err error)
	Write(value string, expand bool) error
	// Announce tells the programs that run that the environment changed (WM_SETTINGCHANGE), so that a new terminal reads it.
	Announce() error
}

func addPathEntry(path, dir string) (string, bool) { return path, false }

func removePathEntry(path, dir string) (string, bool) { return path, false }

// folder is where the copy lives.
func (s SelfInstall) folder(d Deps) (string, error) { return "", nil }

// copyTo writes the program at src to dir as suiteward.exe and returns that path.
func (s SelfInstall) copyTo(src, dir string) (string, error) { return "", nil }

func (s SelfInstall) addToUserPath(dir string) (changed bool, err error) { return false, nil }

func (s SelfInstall) removeFromUserPath(dir string) (changed bool, err error) { return false, nil }

// selfInstallStep is the part of the summary of an install or an upgrade that offers the copy and the PATH entry.
func selfInstallStep(ctx context.Context, env Env, rows *summaryRows) Deps { return env.Deps }
