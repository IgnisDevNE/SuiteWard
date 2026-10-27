package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			wantFail(t, res, "executor.binary", "no executor binary")
			for _, name := range []string{"executor.lifecycle", "executor.running", "executor.binary"} {
				fix := res[name].Fix
				if !strings.Contains(fix, "uninstall") || strings.Contains(fix, "install --upgrade") {
					t.Errorf("%s: the fix %q must name the removal of the other instance, not an install that skips the executor", name, fix)
				}
				if !strings.Contains(fix, "that instance's --settings and --token-file (none for the default instance)") {
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
