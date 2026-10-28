package installer

import (
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
// its name (as suggest does) and gives the one the guide puts it at to one that it does.
func TestSuggestHostStepNamesTheProgramByAPathThatSudoFinds(t *testing.T) {
	const sums = "--sha256sums <the release's SHA256SUMS file>"
	for name, c := range map[string]struct{ command, want string }{
		"the name the shell finds":      {"suiteward", "sudo /usr/local/bin/suiteward install host " + sums},
		"no name given":                 {"", "sudo /usr/local/bin/suiteward install host " + sums},
		"a program that is not on PATH": {"'/opt/my suiteward/suiteward'", "sudo '/opt/my suiteward/suiteward' install host " + sums},
	} {
		d := Deps{Command: c.command, instance: instanceArgs{settings: "/s.yaml"}} // the host step names no instance
		if got := d.suggestHostStep(sums); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
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
