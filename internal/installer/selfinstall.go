package installer

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// selfName is what a command of the installer is called where the shell finds the program by name.
const selfName = "suiteward"

// command is how a hint names this program: Deps.Command, or suiteward when the run did not work it out.
func (d Deps) command() string { return cmp.Or(d.Command, selfName) }

// commandName is the Deps.Command of the running program: executable is os.Executable and lookPath is exec.LookPath. A hint that says
// `suiteward doctor` is a command only where the shell finds this program by that name, as it does after the release is installed on the
// PATH. A release downloaded to a folder is not there, and it is run by its path: the path is quoted for a shell (quoteArg), and PowerShell,
// which reads a quoted string at the start of a line as text, runs it with the call operator.
func commandName(goos string, executable func() (string, error), lookPath func(string) (string, error)) string {
	exe, err := executable()
	if err != nil {
		return selfName // there is no path to name: the bare name is the best that is known
	}
	if found, err := lookPath(selfName); err == nil && sameFile(found, exe) { // exec.ErrDot is an error: a shell does not run a file of the current folder by its name
		return selfName
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

// pathKey is an entry of a PATH value as it is compared: without case, which Windows ignores, and without a trailing separator.
func pathKey(entry string) string { return strings.ToLower(strings.TrimRight(entry, `\/`)) }

// addPathEntry appends dir to a PATH value unless it is there already. The other entries stay as they are, in their order.
func addPathEntry(path, dir string) (string, bool) {
	switch {
	case slices.ContainsFunc(strings.Split(path, ";"), func(e string) bool { return pathKey(e) == pathKey(dir) }):
		return path, false
	case path == "":
		return dir, true
	case strings.HasSuffix(path, ";"):
		return path + dir, true
	}
	return path + ";" + dir, true
}

// removePathEntry removes every entry of a PATH value that is dir. The other entries stay as they are, in their order.
func removePathEntry(path, dir string) (string, bool) {
	entries := strings.Split(path, ";")
	kept := slices.DeleteFunc(slices.Clone(entries), func(e string) bool { return pathKey(e) == pathKey(dir) })
	if len(kept) == len(entries) {
		return path, false
	}
	return strings.Join(kept, ";"), true
}

// addToUserPath puts dir on the user's PATH, in the kind of value it has: a Path that does not exist yet is made of the expandable kind,
// as Windows makes one. It says whether it changed the value; the programs that run are told by Announce.
func (s SelfInstall) addToUserPath(dir string) (changed bool, err error) {
	value, expand, found, err := s.Path.Read()
	if err != nil {
		return false, fmt.Errorf("read the PATH of this user: %w", err)
	}
	if !found {
		expand = true
	}
	updated, changed := addPathEntry(value, dir)
	if !changed {
		return false, nil
	}
	if err := s.Path.Write(updated, expand); err != nil {
		return false, fmt.Errorf("write the PATH of this user: %w", err)
	}
	return true, nil
}

// removeFromUserPath takes dir off the user's PATH, keeping the kind of value it has.
func (s SelfInstall) removeFromUserPath(dir string) (changed bool, err error) {
	value, expand, found, err := s.Path.Read()
	if err != nil {
		return false, fmt.Errorf("read the PATH of this user: %w", err)
	}
	if !found {
		return false, nil
	}
	updated, changed := removePathEntry(value, dir)
	if !changed {
		return false, nil
	}
	if err := s.Path.Write(updated, expand); err != nil {
		return false, fmt.Errorf("write the PATH of this user: %w", err)
	}
	return true, nil
}

// folder is where the copy lives: in the local application data of the user, outside any roaming profile.
func (s SelfInstall) folder(d Deps) (string, error) {
	local := d.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", errors.New("LOCALAPPDATA is not set, so there is no folder of this user to copy the program to")
	}
	return filepath.Join(local, "SuiteWard", "bin"), nil
}

// copyTo writes the program at src to dir as suiteward.exe and returns that path: a temporary file in dir, renamed over the copy, so that
// the copy is whole or as it was.
func (s SelfInstall) copyTo(src, dir string) (target string, err error) {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return "", fmt.Errorf("read the program: %w", err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	staged, err := os.CreateTemp(dir, "suiteward-*.tmp")
	if err != nil {
		return "", fmt.Errorf("write the copy: %w", err)
	}
	if _, err = io.Copy(staged, in); err == nil {
		err = staged.Sync()
	}
	if err = errors.Join(err, staged.Close()); err != nil {
		return "", errors.Join(fmt.Errorf("write the copy: %w", err), os.Remove(staged.Name()))
	}
	target = filepath.Join(dir, selfFile)
	if err := s.place(staged.Name(), target); err != nil {
		return "", errors.Join(err, os.Remove(staged.Name()))
	}
	return target, nil
}

// place renames staged over target. A program that runs holds its file, which cannot be replaced but can be renamed: the copy goes aside
// (suiteward.exe.old, which the next run removes) and the new one takes its place. If it cannot, the old copy goes back.
func (s SelfInstall) place(staged, target string) error {
	err := s.Rename(staged, target)
	if err == nil {
		return nil
	}
	if _, statErr := os.Stat(target); statErr != nil {
		return fmt.Errorf("put the copy in place: %w", err) // nothing holds it: this is another failure
	}
	stale := target + staleSuffix
	if rmErr := os.Remove(stale); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return fmt.Errorf("%s cannot be replaced (%v) and the copy that an earlier run put aside, %s, cannot be removed: %w", target, err, stale, rmErr)
	}
	if aside := s.Rename(target, stale); aside != nil {
		return fmt.Errorf("%s cannot be replaced (%v) nor renamed to %s: %w", target, err, stale, aside)
	}
	if err := s.Rename(staged, target); err != nil {
		if back := s.Rename(stale, target); back != nil {
			return fmt.Errorf("put the copy in place: %w; the old copy could not be put back either, it is %s: %v", err, stale, back)
		}
		return fmt.Errorf("put the copy in place: %w", err)
	}
	return nil
}

// selfInstallStep is the part of the summary of an install or an upgrade that deals with the copy of the program, on a full screen of a PC
// that offers one. It removes the copy that an earlier run put aside, refreshes a copy that exists (its owner said yes once), and otherwise
// explains the copy and the PATH entry and asks. It returns the Deps the rest of the summary builds its hints with: after a copy that is on the
// PATH they name the program suiteward. A failure is a warning in the summary, because the install it ends has worked.
func selfInstallStep(ctx context.Context, env Env, rows *summaryRows) Deps {
	d, s, scr := env.Deps, env.Self, screenOn(env.Term.Out)
	if s == nil || !scr.FullScreen() || ctx.Err() != nil {
		return d
	}
	exe, err := s.Executable()
	if err != nil {
		rows.warn("suiteward.exe was not copied to a folder on your PATH: this program was not found: " + err.Error())
		return d
	}
	dir, err := s.folder(d)
	if err != nil {
		rows.warn("suiteward.exe was not copied to a folder on your PATH: " + err.Error())
		return d
	}
	target := filepath.Join(dir, selfFile)
	_ = os.Remove(target + staleSuffix) // a copy that a program held in the run before: if it still does, the next run tries again
	if found, err := s.LookPath(selfName); sameFile(exe, target) || (err == nil && sameFile(found, exe)) {
		return d // this program is the copy, or the shell finds it by its name already
	}
	if _, err := os.Stat(target); err == nil {
		if _, err := s.copyTo(exe, dir); err != nil {
			rows.warn(fmt.Sprintf("The copy of suiteward.exe in %s was not updated: %v", dir, err))
			return d
		}
		rows.add("Command", target+" (updated)")
		return d
	}
	return offerCopy(ctx, env, rows, exe, dir)
}

// offerCopy explains the copy of the program and the PATH entry, asks, and on a yes makes both. The default is No: Enter never changes the PATH.
func offerCopy(ctx context.Context, env Env, rows *summaryRows, exe, dir string) Deps {
	d, s, scr := env.Deps, env.Self, screenOn(env.Term.Out)
	target := filepath.Join(dir, selfFile)
	scr.Page("Run suiteward from any terminal",
		ui.Para("This installer is "+exe+", which is not on your PATH, so a command that SuiteWard prints, such as suiteward doctor, does not work in a terminal."),
		ui.Fields(ui.KV{Key: "Copy to", Value: target}, ui.KV{Key: "Add to user PATH", Value: dir}),
		ui.Para("New terminals find suiteward by name. This terminal keeps the PATH it started with: the summary shows the one line that fixes it here."))
	picked, err := scr.Select("Copy suiteward.exe and add it to your PATH?", yesNo, 1)
	switch {
	case ctx.Err() != nil || errors.Is(err, io.EOF): // Ctrl-C, or an input that ended: no answer is not a yes
		return d
	case err != nil:
		rows.warn("suiteward.exe was not copied to a folder on your PATH: the answer could not be read: " + err.Error())
		return d
	case picked != 0:
		return d
	}
	if _, err := s.copyTo(exe, dir); err != nil {
		rows.warn(fmt.Sprintf("suiteward.exe was not copied to %s: %v. SuiteWard works without it: run it by the path of this program, %s.", dir, err, exe))
		return d
	}
	changed, err := s.addToUserPath(dir)
	if err != nil {
		rows.warn(fmt.Sprintf("suiteward.exe was copied to %s, but its folder was not added to your PATH: %v. Add %s to the PATH of your user (Settings, Environment Variables) to run suiteward by name.", target, err, dir))
		return d
	}
	if changed {
		if err := s.Path.Announce(); err != nil {
			rows.warn(fmt.Sprintf("Your PATH was changed, but the programs that run were not told (%v): a new terminal may need you to sign out and in again.", err))
		}
	}
	rows.add("Command", target+" (new terminals find it as suiteward)")
	rows.add("This terminal", `$env:Path += ";`+dir+`"`)
	d.Command = selfName
	return d
}
