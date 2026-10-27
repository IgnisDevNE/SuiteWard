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
