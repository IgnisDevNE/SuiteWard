package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// A computer has one executor: the unit, the launch agent and the task have one name for each user. Doctor of a second instance (a token file of its
// own) finds the executor of the main instance registered, so it must say that the executor is registered for another instance: the install skips
// it, and a fix that names the install would never make it right.

// registerAnotherInstancesExecutor makes the executor that the fake operating system has registered the main instance's: it names the program in
// the default place, and the doctor that runs is of an instance whose token file is somewhere else. It returns that token file.
func registerAnotherInstancesExecutor(t *testing.T, f *fakeEngine) string {
	t.Helper()
	main := coloExecFilesOf(t, f)
	var definition, content string
	switch f.goos {
	case "linux":
		definition = filepath.Join(f.env["HOME"], ".config", "systemd", "user", executorUnitName)
		content = "[Service]\nExecStart=\"" + unitValue(main.binary) + "\" execute\n"
	case "darwin":
		definition = filepath.Join(f.env["HOME"], "Library", "LaunchAgents", executorLabel+".plist")
		content = "<plist><dict><key>ProgramArguments</key><array><string>" + xmlEscaper.Replace(main.binary) + "</string></array></dict></plist>\n"
	}
	if definition != "" { // a Windows task is not read: it is another instance's when this one has not made its own files
		if err := os.MkdirAll(filepath.Dir(definition), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, definition, content)
	}
	return filepath.Join(t.TempDir(), "mcp-token")
}

func TestDoctorOfASecondInstanceSaysThatTheExecutorIsRegisteredForAnotherInstance(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			// The executor runs and is registered, for the main instance; this instance's service has not seen an executor for ten minutes.
			f, s := colocated{goos: goos, status: coloStatus(10 * time.Minute)}.build(t)
			tokenFile := registerAnotherInstancesExecutor(t, f)
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := SaveSettings(path, s); err != nil {
				t.Fatal(err)
			}
			res := map[string]doctorResult{}
			for _, r := range doctorChecks(context.Background(), f.deps(), httpGet, path, tokenFile) {
				res[r.Name] = r
			}
			for _, name := range []string{"executor.lifecycle", "executor.running"} {
				wantFail(t, res, name, "another instance")
			}
			// This instance has no executor binary of its own, and the install skips the executor that is registered: no fix of these is an install.
			wantFail(t, res, "executor.binary", "program is missing")
			for _, name := range []string{"executor.lifecycle", "executor.running", "executor.binary"} {
				fix := res[name].Fix
				if !strings.Contains(fix, "uninstall") || strings.Contains(fix, "install --upgrade") {
					t.Errorf("%s: the fix %q must name the removal of the other instance, not an install that skips the executor", name, fix)
				}
				if !strings.Contains(fix, "that instance's `--settings` and `--token-file`; the default instance needs neither") {
					t.Errorf("%s: the fix %q must say which settings file and token file go with the removal", name, fix)
				}
			}
		})
	}
}

// The files of an executor are beside the token file, and the default token file is the main instance's. Doctor with another settings file and no
// --token-file would read the main instance's folder and report its executor as this instance's: it asks for the token file, as the install does,
// and checks no other row of the executor.
func TestDoctorOfAnotherSettingsFileWithoutATokenFileAsksForIt(t *testing.T) {
	f, s := colocated{}.build(t) // the main instance's executor, healthy, at the default place
	defaultFile, err := defaultTokenFile(f.deps())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	res := map[string]doctorResult{}
	for _, r := range doctorChecks(context.Background(), f.deps(), httpGet, path, "") {
		res[r.Name] = r
	}
	wantFail(t, res, "executor.token", "--token-file", "second instance")
	if fix := res["executor.token"].Fix; !strings.Contains(fix, "--token-file") || !strings.Contains(fix, "the only instance on this computer") || !strings.Contains(fix, defaultFile) {
		t.Errorf("the fix %q must name --token-file, the default token file %s for a sole instance, and say so", fix, defaultFile)
	}
	for _, name := range []string{"executor.binary", "executor.lifecycle", "executor.running", "executor.image"} {
		if _, checked := res[name]; checked {
			t.Errorf("%s is checked, and it read the main instance's folder: %+v", name, res[name])
		}
	}
}

// The list of installations says which instance holds the test runner, and the texts name it. A test writes a list only under the temporary
// configuration folder that build, defaultTokenPlace or useDefaultConfigDir pointed at: a write to a real list would change a real computer's.
const (
	holderID    = "1a2b3c4d" // the instance whose program the registered test runner starts
	bystanderID = "9f8e7d6c" // an instance of the list that does not hold it
)

// generalHint is the removal that names no instance, which a fix gives when the list does not say which one holds the test runner.
const generalHint = "`suiteward uninstall` (pass that instance's `--settings` and `--token-file`; the default instance needs neither)"

// removing is the removal of an installation as a fix writes it when the list names it: the flags that select it, ready to be copied.
func removing(settings, tokenFile string) string {
	return "`suiteward uninstall --settings " + quoteArg(settings) + " --token-file " + quoteArg(tokenFile) + "`"
}

// listedAt puts the installation of settingsPath, with the token file and instance id given, into the list as an install records it. It refuses a
// list outside the temporary folder.
func listedAt(t *testing.T, goos, settingsPath, tokenFile string, s Settings, instance string) {
	t.Helper()
	path, err := installationsPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, os.TempDir()) {
		t.Fatalf("the list of installations %s is outside the temporary folder: a test must not write a real list", path)
	}
	if err := recordInstallation(goos, settingsPath, tokenFile, s, instance); err != nil {
		t.Fatal(err)
	}
}

// listedElsewhere makes a settings file for an installation on the container program and lists it; it returns that file. A doctor result bounds
// its detail to 300 characters (oneLine) and the names come first, but the test's own folder is a long path: the settings file goes in a short one.
func listedElsewhere(t *testing.T, goos, tokenFile, runtime, instance string) string {
	t.Helper()
	s := colocatedSettings()
	s.Runtime = runtime
	path := filepath.Join(shortDir(t), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	listedAt(t, goos, path, tokenFile, s, instance)
	return path
}

// otherToken is the token file of an installation that is not the main one.
func otherToken(t *testing.T) string { return filepath.Join(t.TempDir(), "mcp-token") }

// secondInstanceBesides is the doctor of a second instance, whose service has not seen an executor for ten minutes, on a computer whose registered
// test runner starts the program of the main instance (the one beside the default token file). The second instance is in the list. list adds the
// others and gets the main instance's token file. The results come with the token file that the doctor was given.
func secondInstanceBesides(t *testing.T, goos string, list func(mainToken string)) (map[string]doctorResult, string) {
	t.Helper()
	f, s := colocated{goos: goos, status: coloStatus(10 * time.Minute)}.build(t)
	tokenFile := registerAnotherInstancesExecutor(t, f)
	path := savedAt(t, s)
	listedAt(t, goos, path, tokenFile, s, "")
	list(coloTokenFile(t, f))
	res := map[string]doctorResult{}
	for _, r := range doctorChecks(context.Background(), f.deps(), httpGet, path, tokenFile) {
		res[r.Name] = r
	}
	return res, tokenFile
}

// Where the definition of the registered test runner names the program of one listed installation (Linux, macOS), or one other installation is
// listed (Windows, which cannot read the task), the failure names that instance with its container program, and the fix carries the flags that
// select it.
func TestDoctorNamesTheInstanceThatHoldsTheTestRunner(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			var holder, mainToken string
			res, _ := secondInstanceBesides(t, goos, func(main string) {
				mainToken = main
				holder = listedElsewhere(t, goos, main, RuntimeDocker, holderID)
				if goos != "windows" { // the definition does not name its program; Windows cannot tell the two apart
					listedElsewhere(t, goos, otherToken(t), RuntimePodman, bystanderID)
				}
			})
			wantFail(t, res, "executor.lifecycle", "belongs to another instance (the instance `"+holderID+"`, on Docker), not to the one that uses the token file `")
			if detail := res["executor.lifecycle"].Detail; strings.Contains(detail, bystanderID) || strings.Contains(detail, "Podman") {
				t.Errorf("the detail names an instance that does not hold the test runner: %q", detail)
			}
			for _, name := range []string{"executor.lifecycle", "executor.running", "executor.binary"} {
				r := res[name]
				if !strings.Contains(r.Fix, "Remove that instance with "+removing(holder, mainToken)+".") || strings.Contains(r.Fix, "pass that instance's") {
					t.Errorf("%s: the fix %q must carry the removal that selects the instance, and not leave the flags to the person", name, r.Fix)
				}
				// The id is introduced once, as the instance it is, in the failure; the fix carries the command and not the id again.
				if n := strings.Count(r.Detail+r.Fix, "`"+holderID+"`"); n > 1 || strings.Contains(r.Detail+r.Fix, "instance id") {
					t.Errorf("%s: the id is named %d times, want it introduced once as the instance: %q %q", name, n, r.Detail, r.Fix)
				}
			}
		})
	}
}

// An entry of an older list has no instance id: it is named by its settings file and its container program.
func TestDoctorNamesTheInstanceThatHoldsTheTestRunnerByItsSettingsFileWhenTheListHasNoId(t *testing.T) {
	var holder, mainToken string
	res, _ := secondInstanceBesides(t, "linux", func(main string) {
		mainToken = main
		holder = listedElsewhere(t, "linux", main, RuntimePodman, "")
	})
	wantFail(t, res, "executor.lifecycle", "belongs to another instance (the instance with the settings file `"+holder+"`, on Podman), not to")
	if fix := res["executor.lifecycle"].Fix; !strings.Contains(fix, "Remove that instance with "+removing(holder, mainToken)+".") {
		t.Errorf("the fix %q must carry the removal that selects the instance", fix)
	}
}

// On Windows the task is not read, so with several other installations listed the failure says that the test runner is one of theirs and lists
// them, and the fix keeps the removal that leaves the flags to the person.
func TestDoctorListsTheInstancesThatMayHoldTheTestRunnerWhenItCannotTellWhich(t *testing.T) {
	var first, second string
	res, _ := secondInstanceBesides(t, "windows", func(string) {
		first = listedElsewhere(t, "windows", otherToken(t), RuntimeDocker, holderID)
		second = listedElsewhere(t, "windows", otherToken(t), RuntimePodman, "")
	})
	wantFail(t, res, "executor.lifecycle", "belongs to another instance (one of the other instances on this computer: the instance `"+holderID+"`, on Docker; "+
		"the instance with the settings file `"+second+"`, on Podman), not to the one")
	for _, name := range []string{"executor.lifecycle", "executor.running", "executor.binary"} {
		fix := res[name].Fix
		if !strings.Contains(fix, "Remove that instance with "+generalHint+".") || strings.Contains(fix, first) || strings.Contains(fix, second) {
			t.Errorf("%s: the fix %q must keep the general removal", name, fix)
		}
	}
}

// Nothing in the list names the instance that holds the test runner: the words are the ones the doctor always had.
func TestDoctorKeepsItsWordsWhenTheListDoesNotNameTheInstanceThatHoldsTheTestRunner(t *testing.T) {
	for name, c := range map[string]struct {
		goos string
		list func(t *testing.T, mainToken string)
	}{
		"only this instance is listed": {"windows", func(*testing.T, string) {}},
		"another instance whose program the definition does not name": {"linux", func(t *testing.T, _ string) {
			listedElsewhere(t, "linux", otherToken(t), RuntimePodman, bystanderID)
		}},
		"an installation on a separate server has no test runner": {"windows", func(t *testing.T, main string) {
			s := colocatedSettings()
			s.Tier = config.TierIsolated
			listedAt(t, "windows", savedAt(t, s), main, s, bystanderID)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			res, tokenFile := secondInstanceBesides(t, c.goos, func(main string) { c.list(t, main) })
			wantFail(t, res, "executor.lifecycle")
			want := "The test runner on this computer belongs to another instance, not to the one that uses the token file `" + tokenFile + "`. A computer has one test runner."
			if got := res["executor.lifecycle"].Detail; got != want {
				t.Errorf("the detail is %q, want %q", got, want)
			}
			if fix := res["executor.lifecycle"].Fix; !strings.Contains(fix, "Remove that instance with "+generalHint+".") {
				t.Errorf("the fix %q must keep the general removal", fix)
			}
		})
	}
}

// A list that cannot be read does not hide the failure: it says that it could not tell which instance holds the test runner, and why.
func TestDoctorSaysWhyItCannotNameTheInstanceThatHoldsTheTestRunnerWhenTheListCannotBeRead(t *testing.T) {
	res, _ := secondInstanceBesides(t, "linux", func(string) {
		path, err := installationsPath()
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, "installations: [not the version line\n")
	})
	wantFail(t, res, "executor.lifecycle", "belongs to another instance (which one is not known: ", installationsFile)
	if fix := res["executor.lifecycle"].Fix; !strings.Contains(fix, "Remove that instance with "+generalHint+".") {
		t.Errorf("the fix %q must keep the general removal", fix)
	}
}

// The default settings file, named by its own path or left out, is the main instance's: its executor is the one beside the default token file.
func TestDoctorOfTheDefaultSettingsFileChecksTheExecutorBesideTheDefaultTokenFile(t *testing.T) {
	f, s := colocated{}.build(t)
	path, err := DefaultSettingsPath() // inside the test's configuration directory (build redirected it)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	for name, settings := range map[string]string{"left out": "", "named by its path": path} {
		t.Run(name, func(t *testing.T) {
			res := map[string]doctorResult{}
			for _, r := range doctorChecks(context.Background(), f.deps(), httpGet, settings, "") {
				res[r.Name] = r
			}
			wantOK(t, res, "executor.token", "executor.binary", "executor.lifecycle", "executor.running")
		})
	}
}
