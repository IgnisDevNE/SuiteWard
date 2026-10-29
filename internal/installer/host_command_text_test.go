package installer

import (
	"errors"
	"os"
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
