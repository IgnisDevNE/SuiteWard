//go:build windows

package installer

import (
	"slices"
	"strings"
	"testing"
)

func TestWindowEnvCarriesTheCommandAsDataAndDropsWhatOSInteractiveDrops(t *testing.T) {
	environ := []string{`PATH=C:\Windows`, "CONTAINER_HOST=ssh://elsewhere", `USERPROFILE=C:\Users\me`}
	args := []string{"--connection", "pm", "exec", "-it", "suiteward", "/suiteward", "setup", "--owner", "alice", "--test-check", "acme/app=CI / test", "--test-check", `acme/lib=say "hi" & stop`}
	env := windowEnv(environ, "podman", args, `C:\Temp\suiteward-setup-1\exit`)
	want := map[string]string{
		"SUITEWARD_SETUP_EXIT": `C:\Temp\suiteward-setup-1\exit`,
		"SUITEWARD_SETUP_BIN":  "podman",
		"SUITEWARD_SETUP_ARGS": `--connection pm exec -it suiteward /suiteward setup --owner alice --test-check "acme/app=CI / test" --test-check "acme/lib=say \"hi\" & stop"`,
	}
	for key, value := range want {
		if !slices.Contains(env, key+"="+value) {
			t.Errorf("the environment of the window lacks %s=%s:\n%q", key, value, env)
		}
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "CONTAINER_HOST=") {
			t.Errorf("the window runs the engine with the CONTAINER_HOST that --connection must not meet: %q", env)
		}
	}
	if !slices.Contains(env, `USERPROFILE=C:\Users\me`) {
		t.Errorf("the window lost the environment of the installer: %q", env)
	}
}

// The script runs in PowerShell's command line, where the command's text would be code: it holds none of it, no quotes that the
// command line would have to escape, and it ends by clearing what the screen shows of the secret.
func TestWindowScriptHoldsNoDataAndClearsTheScreenBeforeItEnds(t *testing.T) {
	if strings.Contains(windowScript, `"`) {
		t.Errorf("the script has a double quote, which the command line of PowerShell would have to escape:\n%s", windowScript)
	}
	for _, want := range []string{"$env:SUITEWARD_SETUP_BIN", "$env:SUITEWARD_SETUP_ARGS", "Press Enter to close this window", "Clear-Host", "exit $code"} {
		if !strings.Contains(windowScript, want) {
			t.Errorf("the script lacks %q:\n%s", want, windowScript)
		}
	}
	if pause, cleared, end := strings.Index(windowScript, "Read-Host"), strings.Index(windowScript, "Clear-Host"), strings.Index(windowScript, "exit $code"); pause < 0 || cleared < pause || end < cleared {
		t.Errorf("the script must wait for Enter, then clear, then exit with the code of setup:\n%s", windowScript)
	}
	if !strings.Contains(windowScript, "$code = 1") {
		t.Errorf("a window that cannot start the command must not end as a success:\n%s", windowScript)
	}
}

// The code of setup is left for the installer before the window waits for Enter: a window that the user closes after that, with the X or
// with Ctrl-C, ends with a code of its own, and setup has ended well.
func TestWindowScriptLeavesTheExitCodeBeforeItWaitsForEnter(t *testing.T) {
	leave, pause := strings.Index(windowScript, "$env:SUITEWARD_SETUP_EXIT"), strings.Index(windowScript, "Read-Host")
	if leave < 0 || pause < leave {
		t.Errorf("the script must write $code to the file of SUITEWARD_SETUP_EXIT before Read-Host:\n%s", windowScript)
	}
	if !strings.Contains(windowScript[:max(leave, 0)], "$code = $p.ExitCode") {
		t.Errorf("the code that is left must be the one of the command, which is read before it is written:\n%s", windowScript)
	}
}
