package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// eachHostStepText calls f for every `sudo ... install host ...` that a text of the installer (a line of a source file that is not a comment) tells an
// administrator to run.
func eachHostStepText(t *testing.T, f func(file string, line int, command string)) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	command := regexp.MustCompile("`sudo [^`]*install host[^`]*`")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, m := range command.FindAllString(line, -1) {
				f(e.Name(), i+1, m)
			}
		}
	}
}

// The host step asks for the release's SHA256SUMS file on a terminal and refuses to run without --sha256sums where nobody can be asked, which is what a
// script or a pasted command meets, so no text of the installer may tell an administrator to run it without the flag.
func TestNoTextTellsTheAdministratorToRunTheHostStepWithoutTheSHA256SUMS(t *testing.T) {
	eachHostStepText(t, func(file string, line int, command string) {
		if !strings.Contains(command, "--sha256sums") {
			t.Errorf("%s:%d tells the administrator to run %s, which the host step refuses without --sha256sums", file, line, command)
		}
	})
}

// sudo looks a command up in its own secure_path, and on EL9 that lacks /usr/local/bin, where the install guide puts the program: `sudo suiteward install
// host` there is "sudo: suiteward: command not found". So a text names the program by its path.
func TestNoTextTellsTheAdministratorToRunTheHostStepByABareProgramName(t *testing.T) {
	eachHostStepText(t, func(file string, line int, command string) {
		if !strings.HasPrefix(command, "`sudo /") && !strings.HasPrefix(command, "`sudo '/") {
			t.Errorf("%s:%d tells the administrator to run %s: sudo does not find a program by its name on EL9, so name its path (%s)", file, line, command, hostProgram)
		}
	})
}

// A hint that is built, not written out, names the program the same way: suggestHostStep keeps the path of a program that the shell does not find by
// its name (as suggest does), and names the program it runs as by the path it runs from, which is a file that exists where the guide's path may not
// (a program in /usr/bin or in a folder of the service account). Only where nothing better is known does it give the path of the guide.
func TestSuggestHostStepNamesTheProgramByAPathThatSudoFinds(t *testing.T) {
	const sums = "--sha256sums <the release's SHA256SUMS file>"
	from := func(path string) func() (string, error) { return func() (string, error) { return path, nil } }
	for name, c := range map[string]struct {
		d    Deps
		want string
	}{
		"the name the shell finds, the program is in /usr/bin": {Deps{Command: "suiteward", GOOS: "linux", Executable: from("/usr/bin/suiteward")}, "sudo /usr/bin/suiteward install host " + sums},
		"no name given, the program is in a home folder":       {Deps{GOOS: "linux", Executable: from("/home/suiteward/.local/bin/suiteward")}, "sudo /home/suiteward/.local/bin/suiteward install host " + sums},
		"a path with a space is quoted":                        {Deps{Command: "suiteward", GOOS: "linux", Executable: from("/opt/my app/suiteward")}, "sudo '/opt/my app/suiteward' install host " + sums},
		"no way to find the program":                           {Deps{Command: "suiteward", GOOS: "linux"}, "sudo /usr/local/bin/suiteward install host " + sums},
		"the program cannot be found":                          {Deps{Command: "suiteward", GOOS: "linux", Executable: func() (string, error) { return "", errInjected }}, "sudo /usr/local/bin/suiteward install host " + sums},
		"a path that is not absolute":                          {Deps{Command: "suiteward", GOOS: "linux", Executable: from("suiteward")}, "sudo /usr/local/bin/suiteward install host " + sums},
		"this is not the server's system":                      {Deps{Command: "suiteward", GOOS: "windows", Executable: from(`C:\sw\suiteward.exe`)}, "sudo /usr/local/bin/suiteward install host " + sums},
		"a program that is not on PATH keeps its own path":     {Deps{Command: "'/opt/my suiteward/suiteward'", GOOS: "linux", Executable: from("/usr/bin/suiteward")}, "sudo '/opt/my suiteward/suiteward' install host " + sums},
	} {
		c.d.instance = instanceArgs{settings: "/s.yaml"} // the host step names no instance
		if got := c.d.suggestHostStep(sums); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// A run as a user that is not root names the command to run, by the path sudo finds (the very mistake of `sudo suiteward` on EL9 that this step's hints
// were fixed for).
func TestPrepareHostAsAUserThatIsNotRootNamesTheCommandToRun(t *testing.T) {
	h := newHostFake()
	h.euid = "1000"
	_, err := runHost(t, h, declineMetadataRule, false, "")
	want := "`" + h.deps().suggestHostStep("--sha256sums <the release's SHA256SUMS file>") + "`"
	if err == nil || !strings.Contains(err.Error(), "root") || !strings.Contains(err.Error(), want) {
		t.Fatalf("a non-root run must be refused naming %s, got %v", want, err)
	}
	noBareSudoProgram(t, err.Error())
}

// One doctor run names one program in every host step it suggests: the one it runs from.
func TestDoctorNamesOneProgramInEveryHostStepItSuggests(t *testing.T) {
	const exe = "/usr/bin/suiteward"
	f, s := isolatedRig{over: []rule{
		{match: "exec -i " + ContainerDB + " bash", out: "reachable\n"},
		{match: "=-u " + ExecutorAccount, err: errors.New("id: no such user")},
	}}.build(t)
	f.exe = exe
	_, res, _ := runDoctor(t, f, s)
	for _, check := range []string{"metadata", "executor.account"} {
		if fix := res[check].Fix; !strings.Contains(fix, "`sudo "+exe+" install host ") {
			t.Errorf("the fix of %s does not name the program it runs from (%s): %q", check, exe, fix)
		}
	}
}

// noBareSudoProgram fails when text tells an administrator to run `sudo suiteward ...`, which sudo does not find on EL9. The texts that are built at
// run time (a doctor fix, a usage error) cannot be read from the source by the scan above, so the tests that read them call this.
func noBareSudoProgram(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "sudo suiteward") {
		t.Errorf("%q tells the administrator to run `sudo suiteward`: sudo does not find /usr/local/bin on EL9, so name the path (%s)", text, hostProgram)
	}
}

// The texts of the install that the service account runs name the host step as the rest of its run does, by the path it runs from, and not by the
// path of the guide where that is another file: a hint that names a program that is not there is the mistake of `sudo suiteward` again.
func TestTheInstallsHostStepHintsNameTheProgramTheRunExecutesFrom(t *testing.T) {
	d := Deps{GOOS: "linux", Executable: func() (string, error) { return "/usr/bin/suiteward", nil }}
	const step = "sudo /usr/bin/suiteward install host --sha256sums"
	answer := func(out string, err error) func(string, ...string) (string, error) {
		return func(string, ...string) (string, error) { return out, err }
	}
	mustName := func(what, text string) {
		t.Helper()
		if !strings.Contains(text, step) {
			t.Errorf("%s does not name %q: %s", what, step, text)
		}
	}

	_, err := checkAccount(answer("0", nil), "/home/suiteward", d) // run by root
	if err == nil {
		t.Fatal("root was not refused")
	}
	mustName("the refusal of the wrong account", err.Error())
	if !strings.Contains(err.Error(), "bus /usr/bin/suiteward install (") {
		t.Errorf("the command of the service account does not name the same program: %v", err)
	}
	mustName("the manager that cannot be asked", checkUserManager(answer("", errInjected), d).Error())
	mustName("the linger that is off", checkUserManager(func(name string, args ...string) (string, error) {
		if name == "loginctl" {
			return "no", nil
		}
		return "running", nil
	}, d).Error())

	var said strings.Builder
	sayChanges(func(format string, args ...any) { _, _ = fmt.Fprintf(&said, format, args...) }, d, Settings{HTTPPort: 18081}, Settings{HTTPPort: 19081})
	mustName("the change of the HTTP port", said.String())
	if !strings.Contains(said.String(), "--http-port 19081") {
		t.Errorf("the change of the HTTP port does not give the new port: %s", said.String())
	}

	dir := t.TempDir()
	if err := os.WriteFile(executorHandoverFile(dir), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readExecutorSetup(d, dir)
	if err == nil {
		t.Fatal("a token that is too short was accepted")
	}
	mustName("the token that is too short", err.Error())
	setup, err := readExecutorSetup(d, t.TempDir()) // no copy: the service runs without a test runner, and the note says how to make one
	if err != nil {
		t.Fatal(err)
	}
	mustName("the note that the test runner is missing", noExecutorNote(setup.hostStep, "this install again"))
}

// A hand-made host adopted by an install, an upgrade, an enroll or an uninstall that has not been given the account's env file names the host step as
// the rest of the run does, by the path the run executes from: the host may have no suiteward in /usr/local/bin.
func TestAdoptionOfAHandMadeHostNamesTheProgramTheRunExecutesFrom(t *testing.T) {
	home := t.TempDir()
	p := isolatedParams()
	p.EnvFile = "/etc/suiteward/github.env"
	installedUnits(t, home, p)
	d := Deps{GOOS: "linux", Executable: func() (string, error) { return "/usr/bin/suiteward", nil }}
	_, err := adoptUnitsDir(d, unitDir(home), filepath.Join(home, ".config", "suiteward", "github.env"), true)
	want := "`sudo /usr/bin/suiteward install host --sha256sums"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal must name %s, got %v", want, err)
	}
}

// checkAccount's command for the service account is run by the administrator through sudo: a program inside a home directory (EL9 homes are 0700)
// is not one the service account can run, which the host step's own text says (host.next).
func TestTheWrongAccountRefusalSaysWhenTheProgramMustMoveToASharedPlace(t *testing.T) {
	root := func(string, ...string) (string, error) { return "0", nil }
	for name, c := range map[string]struct {
		exe      string
		wantNote bool
	}{
		"the program is in the home of the administrator": {"/home/admin/suiteward", true},
		"the program is in root's home":                   {"/root/suiteward", true},
		"the program is in a shared place":                {"/usr/local/bin/suiteward", false},
		"the program is in /usr/bin":                      {"/usr/bin/suiteward", false},
	} {
		d := Deps{GOOS: "linux", Executable: func() (string, error) { return c.exe, nil }}
		_, err := checkAccount(root, "/root", d)
		if err == nil {
			t.Fatalf("%s: root was not refused", name)
		}
		if got := strings.Contains(err.Error(), "copy the program to a shared place such as /usr/local/bin first"); got != c.wantNote {
			t.Errorf("%s: the note to copy the program is %v, want %v: %v", name, got, c.wantNote, err)
		}
	}
}
