package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// On Windows the release is a file in Downloads that is not on the PATH. At the end of an install or an upgrade on a full screen the installer
// offers to copy itself, renamed suiteward.exe, into %LOCALAPPDATA%\SuiteWard\bin and to add that folder to the user's PATH, so that the
// commands it prints work in a terminal. Nothing else ever touches the PATH: not a plain run, not a run on answers, not one that cannot ask.

// ---- the PATH value ----

const folderOnPath = `C:\Users\me\AppData\Local\SuiteWard\bin`

func TestAddingAFolderToAPathValue(t *testing.T) {
	for _, c := range []struct {
		name, path, want string
		changed          bool
	}{
		{"an empty value", "", folderOnPath, true},
		{"after the others", `C:\A;C:\B`, `C:\A;C:\B;` + folderOnPath, true},
		{"a value that ends with a separator", `C:\A;`, `C:\A;` + folderOnPath, true},
		{"the other entries stay as they are", `%SystemRoot%\system32; C:\With Space ;D:\B\`, `%SystemRoot%\system32; C:\With Space ;D:\B\;` + folderOnPath, true},
		{"already there", `C:\A;` + folderOnPath + `;C:\B`, `C:\A;` + folderOnPath + `;C:\B`, false},
		{"already there in another case and with a trailing backslash", `C:\A;c:\users\ME\appdata\local\suiteward\bin\`, `C:\A;c:\users\ME\appdata\local\suiteward\bin\`, false},
		{"only a folder with the same name in front is another folder", `C:\Other\` + folderOnPath, `C:\Other\` + folderOnPath + `;` + folderOnPath, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, changed := addPathEntry(c.path, folderOnPath)
			if got != c.want || changed != c.changed {
				t.Errorf("addPathEntry(%q) = %q, %v; want %q, %v", c.path, got, changed, c.want, c.changed)
			}
			if strings.Contains(got, ";;") && !strings.Contains(c.path, ";;") {
				t.Errorf("addPathEntry made an empty entry: %q", got)
			}
		})
	}
}

func TestRemovingAFolderFromAPathValue(t *testing.T) {
	for _, c := range []struct {
		name, path, want string
		changed          bool
	}{
		{"between others", `C:\A;` + folderOnPath + `;C:\B`, `C:\A;C:\B`, true},
		{"in another case and with a trailing backslash", `C:\A;C:\USERS\me\appdata\local\suiteward\BIN\;C:\B`, `C:\A;C:\B`, true},
		{"the last one", `C:\A;` + folderOnPath, `C:\A`, true},
		{"the only one", folderOnPath, ``, true},
		{"each of two entries", folderOnPath + `;C:\A;` + folderOnPath, `C:\A`, true},
		{"the value ends with a separator", `C:\A;` + folderOnPath + `;`, `C:\A;`, true},
		{"the other entries stay as they are", `%SystemRoot%\system32; C:\With Space ;` + folderOnPath + `;D:\B\`, `%SystemRoot%\system32; C:\With Space ;D:\B\`, true},
		{"not there", `C:\A;C:\B`, `C:\A;C:\B`, false},
		{"a folder that only starts with it", folderOnPath + `2`, folderOnPath + `2`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, changed := removePathEntry(c.path, folderOnPath)
			if got != c.want || changed != c.changed {
				t.Errorf("removePathEntry(%q) = %q, %v; want %q, %v", c.path, got, changed, c.want, c.changed)
			}
		})
	}
}

// fakeUserPath is the Path variable of a user, as the registry holds it.
type fakeUserPath struct {
	value                          string
	expand, found                  bool
	readErr, writeErr, announceErr error
	writes                         []string
	announced                      int
}

func (f *fakeUserPath) Read() (string, bool, bool, error) {
	return f.value, f.expand, f.found, f.readErr
}

func (f *fakeUserPath) Write(value string, expand bool) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.value, f.expand, f.found = value, expand, true
	f.writes = append(f.writes, value)
	return nil
}

func (f *fakeUserPath) Announce() error { f.announced++; return f.announceErr }

func TestTheUserPathKeepsItsKindWhenTheFolderIsAddedOrRemoved(t *testing.T) {
	for _, expand := range []bool{true, false} {
		p := &fakeUserPath{value: `C:\A;C:\B`, expand: expand, found: true}
		self := SelfInstall{Path: p}
		if changed, err := self.addToUserPath(folderOnPath); err != nil || !changed {
			t.Fatalf("expand=%v: addToUserPath = %v, %v", expand, changed, err)
		}
		if p.value != `C:\A;C:\B;`+folderOnPath || p.expand != expand {
			t.Errorf("expand=%v: the Path is %q (expand %v) after the add", expand, p.value, p.expand)
		}
		if changed, err := self.addToUserPath(folderOnPath); err != nil || changed || len(p.writes) != 1 {
			t.Errorf("expand=%v: a second add = %v, %v after %d writes; want no change", expand, changed, err, len(p.writes))
		}
		if changed, err := self.removeFromUserPath(folderOnPath); err != nil || !changed {
			t.Fatalf("expand=%v: removeFromUserPath = %v, %v", expand, changed, err)
		}
		if p.value != `C:\A;C:\B` || p.expand != expand {
			t.Errorf("expand=%v: the Path is %q (expand %v) after the removal", expand, p.value, p.expand)
		}
		if changed, err := self.removeFromUserPath(folderOnPath); err != nil || changed || len(p.writes) != 2 {
			t.Errorf("expand=%v: a second removal = %v, %v after %d writes; want no change", expand, changed, err, len(p.writes))
		}
	}
}

func TestAUserWithoutAPathGetsOneOfTheExpandableKind(t *testing.T) {
	p := &fakeUserPath{}
	if changed, err := (SelfInstall{Path: p}).addToUserPath(folderOnPath); err != nil || !changed {
		t.Fatalf("addToUserPath = %v, %v", changed, err)
	}
	if p.value != folderOnPath || !p.expand {
		t.Errorf("the new Path is %q (expand %v), want the folder, expandable as Windows makes it", p.value, p.expand)
	}
	if changed, err := (SelfInstall{Path: &fakeUserPath{}}).removeFromUserPath(folderOnPath); err != nil || changed {
		t.Errorf("removeFromUserPath of no Path = %v, %v; want nothing to do", changed, err)
	}
}

func TestAFailedReadOrWriteOfTheUserPathChangesNothingAndSaysSo(t *testing.T) {
	read := &fakeUserPath{value: `C:\A`, found: true, readErr: errors.New("registry: access denied")}
	if _, err := (SelfInstall{Path: read}).addToUserPath(folderOnPath); err == nil || !strings.Contains(err.Error(), "access denied") || len(read.writes) != 0 {
		t.Errorf("a failed read: err = %v after %d writes, want the error and no write", err, len(read.writes))
	}
	write := &fakeUserPath{value: `C:\A`, found: true, writeErr: errors.New("registry: key is locked")}
	if _, err := (SelfInstall{Path: write}).addToUserPath(folderOnPath); err == nil || !strings.Contains(err.Error(), "key is locked") {
		t.Errorf("a failed write: err = %v, want the error", err)
	}
	if _, err := (SelfInstall{Path: write}).removeFromUserPath(`C:\A`); err == nil || !strings.Contains(err.Error(), "key is locked") {
		t.Errorf("a failed removal: err = %v, want the error", err)
	}
}

// ---- the copy ----

func TestTheCopyLivesInTheLocalApplicationDataOfTheUser(t *testing.T) {
	local := t.TempDir()
	d := Deps{Getenv: func(k string) string {
		if k == "LOCALAPPDATA" {
			return local
		}
		return ""
	}}
	got, err := (SelfInstall{}).folder(d)
	if want := filepath.Join(local, "SuiteWard", "bin"); err != nil || got != want {
		t.Errorf("folder = %q, %v; want %q", got, err, want)
	}
	if _, err := (SelfInstall{}).folder(Deps{Getenv: func(string) string { return "" }}); err == nil || !strings.Contains(err.Error(), "LOCALAPPDATA") {
		t.Errorf("folder with no LOCALAPPDATA = %v, want an error that names it", err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return "(no such file)"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// heldRename is os.Rename on a PC where a running program holds the copy: a file cannot be renamed over it, though it can be renamed away.
func heldRename(target string) func(string, string) error {
	held := true
	return func(oldpath, newpath string) error {
		if held && newpath == target {
			return errors.New("rename: access is denied")
		}
		if oldpath == target {
			held = false
		}
		return os.Rename(oldpath, newpath)
	}
}

// copyOf is the one copy of a test: the program at src, a folder with the copy in it, and the path of the copy.
func copyOf(t *testing.T, installed string) (src, dir, target string) {
	t.Helper()
	src, dir = filepath.Join(t.TempDir(), "x.exe"), t.TempDir()
	target = filepath.Join(dir, "suiteward.exe")
	writeText(t, src, "NEW BUILD")
	if installed != "" {
		writeText(t, target, installed)
	}
	return src, dir, target
}

func TestTheCopyIsWrittenIntoAFolderThatIsNotThereYet(t *testing.T) {
	src := filepath.Join(t.TempDir(), "suiteward_v0.4.0_windows_amd64.exe")
	writeText(t, src, "NEW BUILD")
	dir := filepath.Join(t.TempDir(), "SuiteWard", "bin")
	got, err := (SelfInstall{Rename: os.Rename}).copyTo(src, dir)
	if want := filepath.Join(dir, "suiteward.exe"); err != nil || got != want {
		t.Fatalf("copyTo = %q, %v; want %q", got, err, want)
	}
	if readText(t, got) != "NEW BUILD" || !slices.Equal(names(t, dir), []string{"suiteward.exe"}) {
		t.Errorf("the folder holds %v and the copy %q, want only the copy, whole", names(t, dir), readText(t, got))
	}
}

func TestTheCopyReplacesACopyThatNothingRuns(t *testing.T) {
	src, dir, target := copyOf(t, "OLD BUILD")
	if _, err := (SelfInstall{Rename: os.Rename}).copyTo(src, dir); err != nil {
		t.Fatal(err)
	}
	if readText(t, target) != "NEW BUILD" || !slices.Equal(names(t, dir), []string{"suiteward.exe"}) {
		t.Errorf("the folder holds %v after the copy was replaced", names(t, dir))
	}
}

func TestTheCopyPutsAsideACopyThatARunningProgramHolds(t *testing.T) {
	src, dir, target := copyOf(t, "OLD BUILD")
	writeText(t, target+".old", "OLDER BUILD, left by an earlier run")
	if _, err := (SelfInstall{Rename: heldRename(target)}).copyTo(src, dir); err != nil {
		t.Fatal(err)
	}
	if readText(t, target) != "NEW BUILD" || readText(t, target+".old") != "OLD BUILD" || !slices.Equal(names(t, dir), []string{"suiteward.exe", "suiteward.exe.old"}) {
		t.Errorf("the folder holds %v: the copy %q, the one aside %q", names(t, dir), readText(t, target), readText(t, target+".old"))
	}
}

func TestACopyAsideThatCannotBeRemovedStopsTheCopyAndChangesNothing(t *testing.T) {
	src, dir, target := copyOf(t, "OLD BUILD")
	writeText(t, filepath.Join(target+".old", "held"), "a directory cannot be removed while it holds a file")
	_, err := (SelfInstall{Rename: heldRename(target)}).copyTo(src, dir)
	if err == nil || !strings.Contains(err.Error(), "suiteward.exe.old") {
		t.Fatalf("err = %v, want one that names the copy aside", err)
	}
	if readText(t, target) != "OLD BUILD" || !slices.Equal(names(t, dir), []string{"suiteward.exe", "suiteward.exe.old"}) {
		t.Errorf("the folder holds %v, the copy %q: nothing may change", names(t, dir), readText(t, target))
	}
}

func TestACopyThatCannotBeRenamedAwayStopsTheCopyAndLeavesNoTemporaryFile(t *testing.T) {
	src, dir, target := copyOf(t, "OLD BUILD")
	refuse := func(oldpath, newpath string) error { return errors.New("rename: access is denied") }
	_, err := (SelfInstall{Rename: refuse}).copyTo(src, dir)
	if err == nil || !strings.Contains(err.Error(), "access is denied") {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if readText(t, target) != "OLD BUILD" || !slices.Equal(names(t, dir), []string{"suiteward.exe"}) {
		t.Errorf("the folder holds %v, the copy %q: the temporary file must be gone and the copy as it was", names(t, dir), readText(t, target))
	}
}

func TestTheOldCopyGoesBackWhenTheNewOneCannotTakeItsPlace(t *testing.T) {
	src, dir, target := copyOf(t, "OLD BUILD")
	rename := func(oldpath, newpath string) error {
		if newpath == target && oldpath != target+".old" {
			return errors.New("rename: access is denied") // the new file never gets there
		}
		return os.Rename(oldpath, newpath)
	}
	if _, err := (SelfInstall{Rename: rename}).copyTo(src, dir); err == nil {
		t.Fatal("copyTo succeeded although the new copy could not take its place")
	}
	if readText(t, target) != "OLD BUILD" || !slices.Equal(names(t, dir), []string{"suiteward.exe"}) {
		t.Errorf("the folder holds %v, the copy %q: the old copy must be back, alone", names(t, dir), readText(t, target))
	}
}

func TestAProgramThatCannotBeReadIsNotCopied(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if _, err := (SelfInstall{Rename: os.Rename}).copyTo(filepath.Join(t.TempDir(), "gone.exe"), dir); err == nil {
		t.Error("copyTo succeeded without a program to copy")
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		t.Errorf("the folder holds %v after a copy that failed", names(t, dir))
	}
}

// ---- the offer ----

// aPC is a PC of a user on Windows: a program that is running from Downloads, a user's PATH, and nothing of SuiteWard.
type aPC struct {
	t     *testing.T
	local string // %LOCALAPPDATA%
	exe   string // the running program
	found string // what the shell finds as suiteward, "" for nothing
	path  *fakeUserPath
	self  SelfInstall
}

func newPC(t *testing.T) *aPC {
	t.Helper()
	pc := &aPC{t: t, local: t.TempDir(), path: &fakeUserPath{value: `C:\Windows\system32;C:\Tools`, expand: true, found: true}}
	pc.exe = filepath.Join(t.TempDir(), "Downloads", "suiteward_v0.4.0-alpha.1_windows_amd64.exe")
	writeText(t, pc.exe, "NEW BUILD")
	pc.self = SelfInstall{
		Executable: func() (string, error) { return pc.exe, nil },
		LookPath: func(string) (string, error) {
			if pc.found == "" {
				return "", exec.ErrNotFound
			}
			return pc.found, nil
		},
		Rename: os.Rename,
		Path:   pc.path,
	}
	return pc
}

func (pc *aPC) deps() Deps {
	return Deps{GOOS: "windows", Command: swCmd, Getenv: func(k string) string {
		if k == "LOCALAPPDATA" {
			return pc.local
		}
		return ""
	}}
}
func (pc *aPC) dir() string    { return filepath.Join(pc.local, "SuiteWard", "bin") }
func (pc *aPC) target() string { return filepath.Join(pc.dir(), "suiteward.exe") }

// offered is what the step of the summary did on a screen of 100x30 whose terminal typed answers: the frames, the rows and the name the hints use after it.
type offered struct {
	run  *screenRun
	rows summaryRows
	d    Deps
}

func (pc *aPC) offer(plain bool, answers ...string) offered {
	pc.t.Helper()
	return pc.offerWith(liveContext(pc.t), plain, answers...)
}

func (pc *aPC) offerWith(ctx context.Context, plain bool, answers ...string) offered {
	pc.t.Helper()
	run := newScreenRun(100, 30, answers...)
	env := run.on(Env{Deps: pc.deps(), Self: &pc.self})
	scr, env, err := env.openConsole(ctx, plain)
	if err != nil {
		pc.t.Fatal(err)
	}
	var rows summaryRows
	d := selfInstallStep(ctx, env, &rows)
	if err := scr.Close(); err != nil {
		pc.t.Fatal(err)
	}
	return offered{run: run, rows: rows, d: d}
}

func (o offered) row(key string) string {
	for _, kv := range o.rows {
		if kv.Key == key {
			return kv.Value
		}
	}
	return ""
}

func (o offered) warnings() []string {
	var out []string
	for _, kv := range o.rows {
		if kv.Key == "" {
			out = append(out, kv.Value)
		}
	}
	return out
}

// compact is text without its spaces, so that a path that the frame wrapped is read as it was written.
func compact(s string) string { return strings.Join(strings.Fields(s), "") }

func (o offered) asked() bool { return len(o.run.in.shown) > 0 }

func TestTheOfferExplainsTheCopyAndThePathChangeBeforeItAsks(t *testing.T) {
	pc := newPC(t)
	o := pc.offer(false, "2\n") // No
	if !o.asked() {
		t.Fatal("a full screen of a program that is not on the PATH was not asked")
	}
	v := o.run.question(t, 0)
	text := compact(v.text())
	for _, want := range []string{"suiteward.exe", pc.exe, pc.target(), pc.dir(), "user PATH", "New terminals find suiteward"} {
		if !strings.Contains(text, compact(want)) {
			t.Errorf("the page lacks %q:\n%s", want, v.dump())
		}
	}
	if !strings.Contains(v.text(), "Copy suiteward.exe and add it to your PATH?") {
		t.Errorf("the page does not ask the question:\n%s", v.dump())
	}
	if strings.Contains(v.text(), trimmed) {
		t.Errorf("the page is trimmed at 100x30:\n%s", v.dump())
	}
	if !strings.HasPrefix(v.rows[0], "Run suiteward from any terminal") {
		t.Errorf("the page is titled %q", v.rows[0])
	}
}

func TestOnlyAYesCopiesTheProgramAndChangesThePath(t *testing.T) {
	for _, answer := range []string{"2\n", "\n"} { // No, and Enter, which never says yes
		pc := newPC(t)
		o := pc.offer(false, answer)
		if _, err := os.Stat(pc.target()); err == nil || len(pc.path.writes) != 0 || pc.path.announced != 0 {
			t.Errorf("answer %q: the copy or the PATH changed (%v, %d writes, %d announced)", answer, err, len(pc.path.writes), pc.path.announced)
		}
		if len(o.rows) != 0 || o.d.Command != swCmd {
			t.Errorf("answer %q: the rows are %v and the hints use %q, want nothing changed", answer, o.rows, o.d.Command)
		}
	}
	pc := newPC(t)
	o := pc.offer(false, "1\n")
	if readText(t, pc.target()) != "NEW BUILD" {
		t.Errorf("the copy holds %q, want this program", readText(t, pc.target()))
	}
	if want := `C:\Windows\system32;C:\Tools;` + pc.dir(); pc.path.value != want || !pc.path.expand || pc.path.announced != 1 {
		t.Errorf("the PATH is %q (expand %v, announced %d times), want %q announced once", pc.path.value, pc.path.expand, pc.path.announced, want)
	}
	if got := o.row("Command"); !strings.Contains(got, pc.target()) || !strings.Contains(got, "new terminals") {
		t.Errorf("the Command row says %q, want the copy and that new terminals have suiteward", got)
	}
	if got, want := o.row("This terminal"), `$env:Path += ";`+pc.dir()+`"`; got != want {
		t.Errorf("the This terminal row says %q, want the line to run in PowerShell: %q", got, want)
	}
	if o.d.Command != "suiteward" || o.d.command() != "suiteward" {
		t.Errorf("the hints after the copy use %q, want suiteward", o.d.Command)
	}
	if len(o.warnings()) != 0 {
		t.Errorf("warnings after a copy that worked: %v", o.warnings())
	}
}

func TestTheOfferIsMadeOnlyWhereItCanBeAnsweredAndHelps(t *testing.T) {
	for name, set := range map[string]func(pc *aPC, env *Env, plain *bool){
		"a plain run":                    func(_ *aPC, _ *Env, plain *bool) { *plain = true },
		"no copy of the program offered": func(_ *aPC, env *Env, _ *bool) { env.Self = nil },
		"the program is the copy": func(pc *aPC, _ *Env, _ *bool) {
			pc.exe = pc.target()
			writeText(pc.t, pc.exe, "NEW BUILD")
		},
		"the shell finds this program as suiteward": func(pc *aPC, _ *Env, _ *bool) { pc.found = pc.exe },
	} {
		t.Run(name, func(t *testing.T) {
			pc := newPC(t)
			run := newScreenRun(100, 30, "1\n")
			env, plain := run.on(Env{Deps: pc.deps(), Self: &pc.self}), false
			set(pc, &env, &plain)
			scr, env, err := env.openConsole(liveContext(t), plain)
			if err != nil {
				t.Fatal(err)
			}
			var rows summaryRows
			d := selfInstallStep(liveContext(t), env, &rows)
			_ = scr.Close()
			if len(run.in.shown) != 0 || len(rows) != 0 || d.Command != swCmd || len(pc.path.writes) != 0 {
				t.Errorf("asked %d questions, rows %v, command %q, %d PATH writes; want nothing", len(run.in.shown), rows, d.Command, len(pc.path.writes))
			}
			if _, err := os.Stat(pc.target()); err == nil && !strings.Contains(name, "is the copy") {
				t.Error("a copy was made")
			}
		})
	}
}

func TestACancelledRunIsNotOfferedAnything(t *testing.T) {
	pc := newPC(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := pc.offerWith(ctx, false, "1\n")
	if o.asked() || len(o.rows) != 0 || len(pc.path.writes) != 0 {
		t.Errorf("a run that was interrupted asked %d questions and left rows %v", len(o.run.in.shown), o.rows)
	}
}

func TestACopyOfAnEarlierInstallIsRefreshedWithoutAskingAndTheStalePartIsRemoved(t *testing.T) {
	pc := newPC(t)
	writeText(t, pc.target(), "OLD BUILD")
	writeText(t, pc.target()+".old", "a copy that a running program held")
	o := pc.offer(false, "1\n")
	if o.asked() {
		t.Error("the person who said yes to the copy was asked again")
	}
	if readText(t, pc.target()) != "NEW BUILD" {
		t.Errorf("the copy holds %q, want this program", readText(t, pc.target()))
	}
	if _, err := os.Stat(pc.target() + ".old"); err == nil {
		t.Error("the copy aside of an earlier run was left")
	}
	if len(pc.path.writes) != 0 {
		t.Errorf("the PATH was written %d times: a refresh does not touch it", len(pc.path.writes))
	}
	if got := o.row("Command"); !strings.Contains(got, pc.target()) || !strings.Contains(got, "updated") {
		t.Errorf("the Command row says %q, want the copy and that it was updated", got)
	}
}

func TestARefreshOfACopyThatARunningProgramHoldsPutsItAsideAndTheNextRunRemovesIt(t *testing.T) {
	pc := newPC(t)
	writeText(t, pc.target(), "OLD BUILD")
	pc.self.Rename = heldRename(pc.target())
	pc.offer(false)
	if readText(t, pc.target()) != "NEW BUILD" || readText(t, pc.target()+".old") != "OLD BUILD" {
		t.Fatalf("the copy holds %q and the one aside %q", readText(t, pc.target()), readText(t, pc.target()+".old"))
	}
	pc.self.Rename = os.Rename
	pc.offer(false) // the next run: the program that held the old copy has ended
	if _, err := os.Stat(pc.target() + ".old"); err == nil {
		t.Error("the next run left the copy aside")
	}
}

func TestACopyThatFailsIsAWarningAndTheRunGoesOn(t *testing.T) {
	pc := newPC(t)
	writeText(t, filepath.Join(pc.local, "SuiteWard", "bin"), "a file where the folder should be")
	o := pc.offer(false, "1\n")
	if w := o.warnings(); len(w) != 1 || !strings.Contains(w[0], "was not copied") || !strings.Contains(w[0], pc.exe) {
		t.Errorf("warnings = %q, want one that says the copy failed and names the program to run", w)
	}
	if len(pc.path.writes) != 0 || o.d.Command != swCmd || o.row("This terminal") != "" {
		t.Errorf("the PATH changed or the hints were renamed after a copy that failed (%d writes, %q, %q)", len(pc.path.writes), o.d.Command, o.row("This terminal"))
	}
}

func TestAPathThatCannotBeChangedLeavesTheCopyAndSaysHowToAddTheFolder(t *testing.T) {
	pc := newPC(t)
	pc.path.writeErr = errors.New("registry: access denied")
	o := pc.offer(false, "1\n")
	if readText(t, pc.target()) != "NEW BUILD" {
		t.Error("the copy was removed")
	}
	if w := o.warnings(); len(w) != 1 || !strings.Contains(w[0], "not added to your PATH") || !strings.Contains(w[0], "access denied") || !strings.Contains(w[0], pc.dir()) {
		t.Errorf("warnings = %q, want one that says the folder was not added, why, and which folder", w)
	}
	if o.d.Command != swCmd {
		t.Errorf("the hints use %q although suiteward is not on the PATH", o.d.Command)
	}
}

func TestProgramsThatWereNotToldOfThePathAreAWarningNotAFailure(t *testing.T) {
	pc := newPC(t)
	pc.path.announceErr = errors.New("SendMessageTimeout: timed out")
	o := pc.offer(false, "1\n")
	if w := o.warnings(); len(w) != 1 || !strings.Contains(w[0], "not told") || !strings.Contains(w[0], "timed out") {
		t.Errorf("warnings = %q, want one that says the programs were not told", w)
	}
	if o.d.Command != "suiteward" || o.row("This terminal") == "" {
		t.Errorf("the PATH was changed, so the hints use %q and the row says %q", o.d.Command, o.row("This terminal"))
	}
}

func TestAPcWithoutLocalAppDataIsNotOffered(t *testing.T) {
	pc := newPC(t)
	pc.local = ""
	o := pc.offer(false, "1\n")
	if o.asked() || len(pc.path.writes) != 0 {
		t.Error("a PC with no per-user folder was offered a copy")
	}
	if w := o.warnings(); len(w) != 1 || !strings.Contains(w[0], "LOCALAPPDATA") {
		t.Errorf("warnings = %q, want one that names LOCALAPPDATA", w)
	}
}

func TestAFailedAnswerIsNotAYes(t *testing.T) {
	pc := newPC(t)
	o := pc.offer(false) // the input ends at the question
	if _, err := os.Stat(pc.target()); err == nil || len(pc.path.writes) != 0 || o.d.Command != swCmd {
		t.Errorf("the copy or the PATH changed without an answer (%v, %d writes, %q)", err, len(pc.path.writes), o.d.Command)
	}
}

// ---- the install and the upgrade ----

// installOn runs the co-located install on a full screen of this PC, whose terminal types stdin: the Enter that returns from setup, then the answer to the offer.
func installOnPC(t *testing.T, pc *aPC, stdin string, extra ...string) (*harness, *termConsole, error) {
	t.Helper()
	withTerminal(t, true)
	h := newHarness(t, "linux")
	h.f.env["LOCALAPPDATA"] = pc.local
	h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
	h.first("exec -i suiteward /suiteward check --json", instanceChecks)
	h.stdin = stdin
	c := newTermConsole(&bytes.Buffer{})
	env := h.newEnv()
	env.OpenScreen = c.open
	env.Self = &pc.self
	env.Deps.Command = swCmd
	err := Install(liveContext(t), colocatedArgs(h, extra...), env)
	return h, c, err
}

func TestTheSummaryOfAnInstallOffersTheCopyAndSaysWhatToRunInThisTerminal(t *testing.T) {
	pc := newPC(t)
	h, c, err := installOnPC(t, pc, "\n1\n")
	if err != nil {
		t.Fatal(err)
	}
	tail := c.tail()
	if readText(t, pc.target()) != "NEW BUILD" || !strings.HasSuffix(pc.path.value, ";"+pc.dir()) {
		t.Fatalf("the program was not copied or the PATH not changed (%q): %s", pc.path.value, tail)
	}
	for _, want := range []string{"Command: " + pc.target(), `This terminal: $env:Path += ";` + pc.dir() + `"`} {
		if !strings.Contains(tail, want) {
			t.Errorf("the scrollback lacks %q:\n%s", want, tail)
		}
	}
	// What the summary tells the person to run after the copy is the command of a new terminal.
	if want := "Check again: suiteward doctor --settings " + quoteArg(h.settingsPath()); !strings.Contains(tail, want) {
		t.Errorf("the scrollback lacks %q:\n%s", want, tail)
	}
	if strings.Contains(tail, swCmd) {
		t.Errorf("the summary still names the program by %s after it was copied:\n%s", swCmd, tail)
	}
	noRepeats(t, tail)
}

func TestARunThatCannotBeAnsweredNeverTouchesThePath(t *testing.T) {
	answersFile := filepath.Join(t.TempDir(), "answers.yaml")
	writeText(t, answersFile, "connection: pm\n")
	for name, extra := range map[string][]string{
		"--yes":     {"--yes"},
		"--answers": {"--answers", answersFile},
	} {
		t.Run(name, func(t *testing.T) {
			pc := newPC(t)
			_, c, err := installOnPC(t, pc, "\n1\n", extra...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(pc.target()); err == nil || len(pc.path.writes) != 0 {
				t.Errorf("the copy or the PATH changed in a run with %s (%v, %d writes)", name, err, len(pc.path.writes))
			}
			if strings.Contains(c.out.String(), "Run suiteward from any terminal") {
				t.Errorf("a run with %s was offered the copy", name)
			}
		})
	}
}

// ---- the purge ----

// installed is the PC after the offer was taken: the copy is there and its folder is on the PATH.
func (pc *aPC) installed() *aPC {
	writeText(pc.t, pc.target(), "NEW BUILD")
	pc.path.value = `C:\Windows\system32;` + pc.dir() + `;C:\Tools`
	return pc
}

// purgeOn runs uninstall of the stack of a harness on a full screen of this PC, whose terminal types the answers.
func purgeOn(t *testing.T, pc *aPC, answers []string, flags ...string) (*screenRun, error) {
	t.Helper()
	h := purgeHarness(t)
	h.f.env["LOCALAPPDATA"] = pc.local
	run := newScreenRun(termWidth, termHeight, answers...)
	env := run.on(h.newEnv())
	env.Self = &pc.self
	args := append([]string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "pm"}, flags...)
	return run, Uninstall(liveContext(t), args, env)
}

func TestAPurgeOffersToRemoveTheCopyAndThePathEntry(t *testing.T) {
	pc := newPC(t).installed()
	run, err := purgeOn(t, pc, []string{testInstance + "\n", "1\n", "\n"}, "--purge")
	if err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, run.output())
	}
	v := run.question(t, 1)
	if !strings.HasPrefix(v.rows[0], "Remove the suiteward command") {
		t.Errorf("the page is titled %q, want \"Remove the suiteward command\":\n%s", v.rows[0], v.dump())
	}
	text := compact(v.text())
	for _, want := range []string{pc.target(), pc.dir(), "user PATH"} {
		if !strings.Contains(text, compact(want)) {
			t.Errorf("the page lacks %q:\n%s", want, v.dump())
		}
	}
	if !strings.Contains(v.text(), "Remove the copy and its PATH entry?") {
		t.Errorf("the page does not ask the question:\n%s", v.dump())
	}
	if _, err := os.Stat(pc.target()); err == nil {
		t.Error("the copy is still there after a yes")
	}
	if want := `C:\Windows\system32;C:\Tools`; pc.path.value != want || !pc.path.expand || pc.path.announced != 1 {
		t.Errorf("the PATH is %q (expand %v, announced %d times), want %q announced once", pc.path.value, pc.path.expand, pc.path.announced, want)
	}
	if _, err := os.Stat(pc.dir()); err == nil {
		t.Error("the folder of the copy is left although it is empty")
	}
}

func TestAPurgeThatIsNotAnsweredWithAYesKeepsTheCopyAndThePath(t *testing.T) {
	for name, answer := range map[string]string{"No": "2\n", "Enter": "\n"} {
		t.Run(name, func(t *testing.T) {
			pc := newPC(t).installed()
			before := pc.path.value
			if run, err := purgeOn(t, pc, []string{testInstance + "\n", answer, "\n"}, "--purge"); err != nil {
				t.Fatalf("Uninstall: %v\n%s", err, run.output())
			}
			if readText(t, pc.target()) != "NEW BUILD" || pc.path.value != before || len(pc.path.writes) != 0 {
				t.Errorf("the copy or the PATH changed without a yes (%q, %q)", readText(t, pc.target()), pc.path.value)
			}
		})
	}
}

func TestAPurgeOffersOnlyWhatIsThere(t *testing.T) {
	for name, c := range map[string]struct {
		set        func(pc *aPC)
		asked      bool
		copyThere  bool // after the purge
		pathWrites int
	}{
		"nothing of it":       {func(pc *aPC) {}, false, false, 0},
		"the copy only":       {func(pc *aPC) { writeText(pc.t, pc.target(), "NEW BUILD") }, true, false, 0},
		"the PATH entry only": {func(pc *aPC) { pc.path.value += ";" + pc.dir() }, true, false, 1},
	} {
		t.Run(name, func(t *testing.T) {
			pc := newPC(t)
			c.set(pc)
			run, err := purgeOn(t, pc, []string{testInstance + "\n", "1\n", "\n"}, "--purge")
			if err != nil {
				t.Fatalf("Uninstall: %v\n%s", err, run.output())
			}
			if asked := strings.Contains(run.output(), "Remove the suiteward command"); asked != c.asked {
				t.Errorf("the page was shown: %v, want %v", asked, c.asked)
			}
			if _, err := os.Stat(pc.target()); (err == nil) != c.copyThere {
				t.Errorf("the copy after the purge: %v", err)
			}
			if want := `C:\Windows\system32;C:\Tools`; pc.path.value != want || len(pc.path.writes) != c.pathWrites {
				t.Errorf("the PATH is %q after %d writes, want %q after %d", pc.path.value, len(pc.path.writes), want, c.pathWrites)
			}
		})
	}
}

func TestAPurgeThatCannotBeAnsweredOrIsNotFullScreenNeverRemovesThem(t *testing.T) {
	for name, flags := range map[string][]string{
		"--yes":   {"--purge", "--yes", "--instance", testInstance},
		"--plain": {"--purge", "--plain"},
		"a keep":  {}, // uninstall without --purge removes the containers and keeps the rest
	} {
		t.Run(name, func(t *testing.T) {
			pc := newPC(t).installed()
			before := pc.path.value
			answers := []string{testInstance + "\n", "1\n", "1\n", "\n"}
			if name == "a keep" {
				answers = []string{"1\n", "1\n", "\n"}
			}
			run, err := purgeOn(t, pc, answers, flags...)
			if err != nil {
				t.Fatalf("Uninstall: %v\n%s", err, run.output())
			}
			if readText(t, pc.target()) != "NEW BUILD" || pc.path.value != before || strings.Contains(run.output(), "Remove the suiteward command") {
				t.Errorf("the copy or the PATH was dealt with in a run with %s (%q, %q)", name, readText(t, pc.target()), pc.path.value)
			}
		})
	}
}

// A copy that a running program holds cannot be deleted. The test makes the copy a directory with a file in it, which os.Remove refuses as
// Windows refuses a running program, and which can be renamed as a running program can.
func TestACopyThatARunningProgramHoldsIsRenamedAsideAndTheRunSaysSo(t *testing.T) {
	pc := newPC(t).installed()
	if err := os.Remove(pc.target()); err != nil {
		t.Fatal(err)
	}
	writeText(t, filepath.Join(pc.target(), "held"), "a running program")
	run, err := purgeOn(t, pc, []string{testInstance + "\n", "1\n", "\n"}, "--purge")
	if err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, run.output())
	}
	if _, err := os.Stat(pc.target()); err == nil {
		t.Error("the copy is still under its name")
	}
	if _, err := os.Stat(pc.target() + ".old"); err != nil {
		t.Errorf("the copy was not put aside: %v", err)
	}
	if strings.Contains(pc.path.value, pc.dir()) {
		t.Errorf("the PATH still holds the folder: %q", pc.path.value)
	}
	if out := compact(run.console.tail()); !strings.Contains(out, compact("renamed to "+pc.target()+".old")) || !strings.Contains(out, "nextrun") {
		t.Errorf("the run does not say where the copy went and that the next run removes it:\n%s", run.output())
	}
}

func TestAPathThatCannotBeChangedIsAWarningAndTheCopyIsStillRemoved(t *testing.T) {
	pc := newPC(t).installed()
	pc.path.writeErr = errors.New("registry: access denied")
	run, err := purgeOn(t, pc, []string{testInstance + "\n", "1\n", "\n"}, "--purge")
	if err != nil {
		t.Fatalf("a PATH that could not be changed failed the purge: %v", err)
	}
	if _, err := os.Stat(pc.target()); err == nil {
		t.Error("the copy was kept because the PATH could not be changed")
	}
	if out := compact(run.console.tail()); !strings.Contains(out, "accessdenied") || !strings.Contains(out, compact(pc.dir())) {
		t.Errorf("the run does not say that the PATH was not changed, why and which folder:\n%s", run.output())
	}
}

// ---- the environment ----

func TestAnEnvThatOffersTheCopyWithoutWhatItNeedsIsRefusedBeforeAnythingRuns(t *testing.T) {
	pc := newPC(t)
	for name, broken := range map[string]func(s *SelfInstall){
		"no way to find the program":      func(s *SelfInstall) { s.Executable = nil },
		"no way to look the name up":      func(s *SelfInstall) { s.LookPath = nil },
		"no way to rename a file":         func(s *SelfInstall) { s.Rename = nil },
		"no PATH of the user to change":   func(s *SelfInstall) { s.Path = nil },
		"nothing at all of what it needs": func(s *SelfInstall) { *s = SelfInstall{} },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "linux")
			self := pc.self
			broken(&self)
			env := h.newEnv()
			env.Self = &self
			err := Install(context.Background(), []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath()}, env)
			if err == nil || !strings.Contains(err.Error(), "copy of the program") {
				t.Errorf("Install = %v, want a refusal that names the copy of the program", err)
			}
			if len(h.f.calls) != 0 {
				t.Errorf("%d calls of the engine were made before the refusal", len(h.f.calls))
			}
		})
	}
}
