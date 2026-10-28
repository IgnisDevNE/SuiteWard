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
	"regexp"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// selfName is what a command of the installer is called where the shell finds the program by name.
const selfName = "suiteward"

// instanceArgs are the flags of the running command that name the instance it works on. A command that a hint suggests repeats them: on a
// computer with a second instance, a command without them reaches the default one, and an upgrade, an uninstall or a purge of the wrong
// instance is not undone. The ports, the runtime and the connection are not carried: the settings file of the instance holds them.
type instanceArgs struct{ tier, settings, tokenFile string }

// instance reads them from the run: --settings only of the command line (settingsPathFrom reads it nowhere else), and --token-file and
// --tier of the command line or the answers file, as the commands resolve them; each, after those, from the installation that was chosen.
func (r *Resolver) instance() instanceArgs {
	return instanceArgs{tier: r.preset("tier", ""), settings: r.settingsFlag(), tokenFile: r.preset("token-file", "")}
}

// flags are the ones that subcommand defines, as a person pastes them: a command takes a flag only if it defines it (enroll has no --token-file,
// doctor no --tier), and `install host`, the administrator's step on a server, names no instance.
func (a instanceArgs) flags(sub string) []string {
	tier, tokenFile := a.tier, a.tokenFile
	switch sub {
	case "install", "uninstall", "backup", "backup init", "restore":
	case "enroll":
		tokenFile = ""
	case "doctor":
		tier = ""
	default:
		return nil
	}
	var words []string
	for _, f := range []struct{ name, value string }{{"tier", tier}, {"settings", a.settings}, {"token-file", tokenFile}} {
		if f.value != "" {
			words = append(words, "--"+f.name, quoteArg(f.value))
		}
	}
	return words
}

// suggest is a command of this program as a hint writes it, the only way the installer writes the program's name into one: Deps.Command, or
// suiteward when the run did not work it out, then sub (the subcommand, "install" or "backup init"), then the flags of the running command
// that name the instance (Deps.instance), then args. The flags come right after the subcommand because the flag package stops at the first
// argument that is not a flag, so they would not be read after a file or a placeholder.
func (d Deps) suggest(sub string, args ...string) string {
	words := append([]string{cmp.Or(d.Command, selfName), sub}, d.instance.flags(sub)...)
	return strings.Join(append(words, args...), " ")
}

// serviceCommands are the subcommands whose bare spelling a row of the service may ask to have written with the flags of the instance.
var serviceCommands = []string{"enroll", "install"}

// writeCommands is a fix of a row of the service with each command that the row says it names (named) written as every other hint is, with the
// flags that name the instance (suggest): the service writes the bare name, because it cannot know them. The row comes from another process, so
// only a subcommand of serviceCommands is written, and only where the fix spells it as a whole command (commandWord); anything else, such as a
// command with flags of its own or a longer word, stays as the service wrote it. A command is written between backticks, as the service had it
// or, when it stood alone, with them, because the flags make it longer than a word. All the spellings are found in the service's own text first,
// so what is written is never read again, and two that share a backtick ("`a`b`") are not both written: the first is written and the other is left
// as the service wrote it. Whatever the text, the function reads only inside it.
func (d Deps) writeCommands(fix string, named []string) string {
	type span struct {
		from, to int
		with     string
	}
	var spans []span
	for _, sub := range serviceCommands {
		if !slices.Contains(named, sub) {
			continue
		}
		bare, written := selfName+" "+sub, "`"+d.suggest(sub)+"`"
		for at := 0; ; {
			i := strings.Index(fix[at:], bare)
			if i < 0 {
				break
			}
			from, to := at+i, at+i+len(bare)
			at = to
			if first, last, whole := commandWord(fix, from, to); whole {
				spans = append(spans, span{first, last, written})
			}
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return a.from - b.from })
	var out strings.Builder
	end := 0
	for _, s := range spans {
		if s.from < end { // it would reuse the closing backtick of the span before it: the first one stays
			continue
		}
		out.WriteString(fix[end:s.from])
		out.WriteString(s.with)
		end = s.to
	}
	out.WriteString(fix[end:])
	return out.String()
}

// commandWord says whether fix[from:to], the program's name and a subcommand, is a whole command, and where it stands with its backticks. It is
// one when it is exactly between a pair of backticks, or when it stands alone: after the start of the text, a space or an opening bracket, and
// before the end of the text, a line break, a punctuation mark or a space that no flag follows (a flag belongs to the command, and the instance's flags would go
// before it). Inside a longer backticked command, glued to a word or followed by one it is not.
func commandWord(fix string, from, to int) (first, last int, whole bool) {
	var before, after byte
	if from > 0 {
		before = fix[from-1]
	}
	if to < len(fix) {
		after = fix[to]
	}
	switch {
	case before == '`' && after == '`':
		return from - 1, to + 1, true
	case before != 0 && !strings.ContainsRune(" (\n", rune(before)):
		return 0, 0, false
	case after == 0:
		return from, to, true
	case after == ' ':
		rest := strings.TrimLeft(fix[to:], " ")
		return from, to, rest == "" || rest[0] != '-'
	default:
		return from, to, strings.ContainsRune(".,;:!?)\n\r", rune(after))
	}
}

// onIsolated is d for the install and the upgrade of the isolated tier, whose hints name it with --tier isolated: a host that was set up by
// hand has no settings file to say it.
func (d Deps) onIsolated() Deps {
	d.instance.tier = string(config.TierIsolated)
	return d
}

// otherInstance is d for a hint about another instance than the one the run works on, which has none of its flags: the person adds those.
func (d Deps) otherInstance() Deps {
	d.instance = instanceArgs{}
	return d
}

// removeOtherInstance is how a text names the removal of the instance that owns the computer's test runner, which is not known here: the
// command has none of this run's flags, and the note says that the other instance's go with it, because a bare uninstall removes the default
// instance, which may be the one this run works on. The install's refusal and doctor's fix both say it with this, so that they cannot drift.
func (d Deps) removeOtherInstance() string {
	return "`" + d.otherInstance().suggest("uninstall") + "` (pass that instance's `--settings` and `--token-file`; the default instance needs neither)"
}

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
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
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

// percentVariable is a reference to an environment variable in an entry of a Path of the expandable kind: %LOCALAPPDATA%.
var percentVariable = regexp.MustCompile(`%[^%;]+%`)

// pathEnv is the environment that the entries of a user Path are compared with: Windows expands the variables of a Path of the expandable kind
// (REG_EXPAND_SZ) and takes one of the plain kind as it is written.
func pathEnv(expand bool, getenv func(string) string) func(string) string {
	if expand {
		return getenv
	}
	return func(string) string { return "" }
}

// pathKey is an entry of a PATH value as it is compared: with the variables it names replaced by their value (a terminal reads
// %LOCALAPPDATA%\SuiteWard\bin as the folder of the copy), without case, which Windows ignores, and without a trailing separator. Only the
// comparison expands: the value keeps the text of its entries.
func pathKey(entry string, getenv func(string) string) string {
	expanded := percentVariable.ReplaceAllStringFunc(entry, func(ref string) string {
		if value := getenv(strings.Trim(ref, "%")); value != "" {
			return value
		}
		return ref // a variable that is not defined stays as it is written, as Windows leaves it
	})
	return strings.ToLower(strings.TrimRight(expanded, `\/`))
}

// addPathEntry appends dir to a PATH value unless it is there already. The other entries stay as they are, in their order.
func addPathEntry(path, dir string, getenv func(string) string) (string, bool) {
	switch {
	case slices.ContainsFunc(strings.Split(path, ";"), func(e string) bool { return pathKey(e, getenv) == pathKey(dir, getenv) }):
		return path, false
	case path == "":
		return dir, true
	case strings.HasSuffix(path, ";"):
		return path + dir, true
	}
	return path + ";" + dir, true
}

// removePathEntry removes every entry of a PATH value that is dir. The other entries stay as they are, in their order.
func removePathEntry(path, dir string, getenv func(string) string) (string, bool) {
	entries := strings.Split(path, ";")
	kept := slices.DeleteFunc(slices.Clone(entries), func(e string) bool { return pathKey(e, getenv) == pathKey(dir, getenv) })
	if len(kept) == len(entries) {
		return path, false
	}
	return strings.Join(kept, ";"), true
}

// addToUserPath puts dir on the user's PATH, in the kind of value it has: a Path that does not exist yet is made of the expandable kind,
// as Windows makes one. It says whether it changed the value; the programs that run are told by Announce.
func (s SelfInstall) addToUserPath(dir string, getenv func(string) string) (changed bool, err error) {
	value, expand, found, err := s.Path.Read()
	if err != nil {
		return false, fmt.Errorf("read the PATH of this user: %w", err)
	}
	if !found {
		expand = true
	}
	updated, changed := addPathEntry(value, dir, pathEnv(expand, getenv))
	if !changed {
		return false, nil
	}
	if err := s.Path.Write(updated, expand); err != nil {
		return false, fmt.Errorf("write the PATH of this user: %w", err)
	}
	return true, nil
}

// removeFromUserPath takes dir off the user's PATH, keeping the kind of value it has.
func (s SelfInstall) removeFromUserPath(dir string, getenv func(string) string) (changed bool, err error) {
	value, expand, found, err := s.Path.Read()
	if err != nil {
		return false, fmt.Errorf("read the PATH of this user: %w", err)
	}
	if !found {
		return false, nil
	}
	updated, changed := removePathEntry(value, dir, pathEnv(expand, getenv))
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

// copyTo writes the program at src to dir as suiteward.exe: a temporary file in dir, renamed over the copy, so that the copy is whole or as it was.
func (s SelfInstall) copyTo(src, dir string) error {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return fmt.Errorf("read the program: %w", err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	staged, err := os.CreateTemp(dir, "suiteward-*.tmp")
	if err != nil {
		return fmt.Errorf("write the copy: %w", err)
	}
	if _, err = io.Copy(staged, in); err == nil {
		err = staged.Sync()
	}
	if err = errors.Join(err, staged.Close()); err != nil {
		return errors.Join(fmt.Errorf("write the copy: %w", err), os.Remove(staged.Name()))
	}
	if err := s.place(staged.Name(), filepath.Join(dir, selfFile)); err != nil {
		return errors.Join(err, os.Remove(staged.Name()))
	}
	return nil
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

// devBuild says that the running program is a development build, which carries no release: the build version is "dev", or not given.
func (e Env) devBuild() bool { return cmp.Or(e.Version, "dev") == "dev" }

// refreshCopy keeps the copy of the program that an earlier run made up to date. An upgrade calls it once it has succeeded, on every screen and
// whatever the command line says: the copy exists because its owner said yes to it, so this asks nothing and leaves the PATH alone. An install
// never calls it, since it may be any build and a copy is not replaced in silence, and a development build leaves the copy as it is and says so.
// It also removes the copy that a run before put aside. A failure is a warning, because the upgrade it follows has worked.
// It returns the Deps that the hints of the rest of the run are built with: once the copy is updated and the name suiteward reaches it, they
// name the program suiteward, and not the path of the build that is running.
func refreshCopy(ctx context.Context, env Env) Deps {
	d, s, scr := env.Deps, env.Self, screenOn(env.Term.Out)
	if s == nil || ctx.Err() != nil {
		return d
	}
	exe, err := s.Executable()
	if err != nil {
		return d // without the path of this program there is nothing to copy from, and offerSelf says so where a person can answer
	}
	dir, err := s.folder(d)
	if err != nil {
		return d // this user has no folder of local data, so no copy of the program was ever put there
	}
	target := filepath.Join(dir, selfFile)
	_ = os.Remove(target + staleSuffix) // a copy that a program held in the run before: if it still does, the next run tries again
	if _, err := os.Stat(target); err != nil || sameFile(exe, target) {
		return d // no copy, or this program is the copy
	}
	if env.devBuild() {
		scr.Keep("This is a development build, so the copy of suiteward.exe at %s was left as it is.\n", target)
		return d
	}
	if err := s.copyTo(exe, dir); err != nil {
		scr.Warn("Could not update the copy of suiteward.exe in %s (%v). SuiteWard itself is not affected, and the older copy still runs.\n", dir, err)
		return d
	}
	scr.Keep("Updated the copy of suiteward.exe: %s\n", target)
	// The name reaches the copy in a new terminal when the PATH of the user holds its folder. This terminal may have started before that, and
	// finds nothing yet, which is no reason to name the path; another suiteward that the shell finds first is.
	onPath, err := s.onUserPath(dir, d.Getenv)
	found, lookErr := s.LookPath(selfName)
	if err == nil && onPath && (lookErr != nil || sameFile(found, target)) {
		d.Command = selfName
	}
	return d
}

// offerSelf is the part of the end of an install or an upgrade that offers the copy of the program, on a full screen of a PC that has none and
// of a person who answers (not env.Unattended), when the program is a release: a development build is not offered as the command. It explains the
// copy and the PATH entry and asks. It returns the Deps the rest of the summary builds its hints with: after a copy that the name suiteward
// reaches they name the program suiteward. A failure is a warning in the summary, because the install it ends has worked.
func offerSelf(ctx context.Context, env Env, rows *summaryRows) Deps {
	d, s, scr := env.Deps, env.Self, screenOn(env.Term.Out)
	if s == nil || env.Unattended || env.devBuild() || !scr.FullScreen() || ctx.Err() != nil {
		return d
	}
	exe, err := s.Executable()
	if err != nil {
		rows.warn("suiteward.exe was not copied: the installer could not find its own file (" + err.Error() + "). SuiteWard works without the copy.")
		return d
	}
	dir, err := s.folder(d)
	if err != nil {
		rows.warn("suiteward.exe was not copied: " + err.Error() + ". SuiteWard works without the copy.")
		return d
	}
	target := filepath.Join(dir, selfFile)
	found, lookErr := s.LookPath(selfName)
	if _, statErr := os.Stat(target); statErr == nil || sameFile(exe, target) || (lookErr == nil && sameFile(found, exe)) {
		return d // there is a copy (refreshCopy dealt with it), this program is the copy, or the shell finds it by its name already
	}
	other := ""
	if lookErr == nil {
		other = found // the name suiteward reaches another program
	}
	return offerCopy(ctx, env, rows, exe, dir, other)
}

// offerCopy explains the copy of the program and the PATH entry, asks, and on a yes makes both. The default is Yes: the offer is for the person
// who does not know that the commands need the PATH, and an input that ended or a Ctrl-C is a No. other is the program that the shell runs for
// the name suiteward, when there is one: the folder of the copy goes at the end of the PATH, so that program keeps the name, the page says so,
// the hints keep naming the path, and the default is No, because the copy would not be the command that the person asked for.
func offerCopy(ctx context.Context, env Env, rows *summaryRows, exe, dir, other string) Deps {
	d, s, scr := env.Deps, env.Self, screenOn(env.Term.Out)
	target := filepath.Join(dir, selfFile)
	blocks := []ui.Block{
		ui.Para("SuiteWard prints commands such as suiteward doctor, but a terminal cannot find this program (" + exe + "): its folder is not on your PATH, the list of folders a terminal searches for programs. A copy fixes that."),
		ui.Fields(ui.KV{Key: "Copy to", Value: target}, ui.KV{Key: "Add to user PATH", Value: dir}),
	}
	if other != "" {
		blocks = append(blocks, ui.Para("A terminal already finds another suiteward at "+other+". The copy goes after it on your PATH, so that one keeps the name suiteward."))
	} else {
		blocks = append(blocks, ui.Para("New terminals will find suiteward by name. This terminal keeps the PATH it started with: the summary shows one line that fixes it here."))
	}
	scr.Page("Run suiteward from any terminal", blocks...)
	def := 0 // Yes
	if other != "" {
		def = 1 // No
	}
	yes, err := askYesNo(ctx, scr, "Copy suiteward.exe and add it to your PATH?", def)
	if err != nil {
		rows.warn("suiteward.exe was not copied: " + err.Error() + ". SuiteWard works without the copy.")
	}
	if !yes {
		return d
	}
	if err := s.copyTo(exe, dir); err != nil {
		rows.warn(fmt.Sprintf("suiteward.exe was not copied to %s: %v. SuiteWard works without it: run it by the path of this program, %s.", dir, err, exe))
		return d
	}
	changed, err := s.addToUserPath(dir, d.Getenv)
	if err != nil {
		rows.warn(fmt.Sprintf("suiteward.exe was copied to %s, but its folder was not added to your PATH (%v). To run suiteward by name, add %s to the PATH of your user (Settings, Environment Variables).", target, err, dir))
		return d
	}
	if changed {
		if err := s.Path.Announce(); err != nil {
			rows.warn(fmt.Sprintf("Your PATH was changed, but running programs were not told (%v). A new terminal may need you to sign out and in again.", err))
		}
	}
	if other != "" {
		rows.add("Command", target+" (the shell finds another suiteward first: "+other+")")
		return d
	}
	line := `$env:Path += ';` + strings.ReplaceAll(dir, "'", "''") + `'` // a string that does not expand: a $ or a backtick in dir is text
	rows.add("Command", target+" (new terminals find it as suiteward)")
	rows.add("This terminal (PowerShell)", line)
	d.Command = selfName
	return d
}

// askYesNo asks a question with the arrow keys, Yes in the first row and the default the row def names (0 Yes, 1 No), and says whether the
// answer is Yes. A Ctrl-C and an input that ended are a No: no answer is not a yes. So is a question that could not be read, which err reports.
func askYesNo(ctx context.Context, scr *ui.Screen, question string, def int) (yes bool, err error) {
	picked, err := scr.Select(question, yesNo, def)
	if err != nil && !errors.Is(err, io.EOF) && ctx.Err() == nil {
		return false, fmt.Errorf("the answer could not be read: %w", err)
	}
	return err == nil && ctx.Err() == nil && picked == 0, nil
}

// onUserPath says whether dir is on the user's PATH.
func (s SelfInstall) onUserPath(dir string, getenv func(string) string) (bool, error) {
	value, expand, found, err := s.Path.Read()
	if err != nil || !found {
		return false, err
	}
	_, there := removePathEntry(value, dir, pathEnv(expand, getenv))
	return there, nil
}

// offerRemoveCopy is the end of a purge on a full screen of a PC that has the copy of the program or its entry on the PATH: it asks whether to
// remove them, unless the run was told not to ask (--yes). The default is No. What cannot be removed is a warning, because the purge it ends has worked.
func offerRemoveCopy(ctx context.Context, env Env) {
	d, s, scr := env.Deps, env.Self, screenOn(env.Term.Out)
	if s == nil || env.Unattended || !scr.FullScreen() || ctx.Err() != nil {
		return
	}
	dir, err := s.folder(d)
	if err != nil {
		return // this user has no folder of local data, so no copy of the program was ever put there
	}
	target := filepath.Join(dir, selfFile)
	there := func(path string) bool { _, err := os.Stat(path); return err == nil }
	hasCopy := there(target) || there(target+staleSuffix)
	onPath, err := s.onUserPath(dir, d.Getenv)
	if err != nil {
		scr.Warn("Could not read your PATH (%v), so it is not known whether it holds %s.\n", err, dir)
	}
	if !hasCopy && !onPath {
		return
	}
	var found []ui.KV
	if hasCopy {
		found = append(found, ui.KV{Key: "Copy", Value: target})
	}
	if onPath {
		found = append(found, ui.KV{Key: "User PATH entry", Value: dir})
	}
	scr.Page("Remove the suiteward command",
		ui.Para("An earlier install made the suiteward command available in terminals: it put a copy of this program in a folder and added that folder to your PATH. SuiteWard itself is already removed."),
		ui.Fields(found...),
		ui.Para("If a program is using the copy, it is renamed to suiteward.exe.old instead: delete that file yourself."))
	yes, err := askYesNo(ctx, scr, "Remove the copy and its PATH entry?", 1)
	if err != nil {
		scr.Warn("The suiteward command was not removed: %v\n", err)
	}
	if !yes {
		return
	}
	if onPath {
		s.removeFromPath(scr, dir, d.Getenv)
	}
	if hasCopy {
		s.removeCopy(scr, dir)
	}
	_ = os.Remove(dir) // only when it is empty: the folder of the copy has nothing else in it
}

// removeFromPath takes dir off the user's PATH and tells the programs that run.
func (s SelfInstall) removeFromPath(scr *ui.Screen, dir string, getenv func(string) string) {
	changed, err := s.removeFromUserPath(dir, getenv)
	if err != nil {
		scr.Warn("Could not remove the folder %s from your PATH (%v). Remove it yourself in Settings, Environment Variables.\n", dir, err)
		return
	}
	if changed {
		if err := s.Path.Announce(); err != nil {
			scr.Warn("Your PATH was changed, but running programs were not told (%v). A new terminal may need you to sign out and in again.\n", err)
		}
		scr.Say("Removed %s from your PATH.\n", dir)
	}
}

// removeCopy deletes the copy, and the one that an earlier run put aside. A copy that a running program holds cannot be deleted, but it can be
// renamed: it goes aside, and the person is told to delete it (a later run may remove it too, but nothing promises that).
func (s SelfInstall) removeCopy(scr *ui.Screen, dir string) {
	target := filepath.Join(dir, selfFile)
	stale := target + staleSuffix
	err := os.Remove(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) { // held
		if rmErr := os.Remove(stale); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			scr.Warn("%s is in use (%v), and so is %s, where it would go. Close the terminals that run suiteward and delete both files yourself.\n", target, err, stale)
			return
		}
		if renameErr := s.Rename(target, stale); renameErr != nil {
			scr.Warn("Could not remove or rename %s (%v). Close the terminals that run suiteward and delete the file yourself.\n", target, renameErr)
			return
		}
		scr.Say("%s is in use, so it was renamed to %s. Delete that file yourself.\n", target, stale)
		return
	}
	if err == nil {
		scr.Say("Removed %s.\n", target)
	}
	if rmErr := os.Remove(stale); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		scr.Warn("Could not remove %s (%v). Delete it yourself.\n", stale, rmErr)
	}
}

// installSelf is what the end of a successful install of the co-located tier does about the copy of the program, before its summary is built: a
// person on a full screen who has no copy is offered one. A copy that exists is left as it is. The summary shows what the offer left in copyRows
// and builds its hints with the name it left in the Deps.
func (c *colocatedInstall) installSelf(ctx context.Context) {
	c.env.Deps = offerSelf(ctx, c.env, &c.copyRows)
}
